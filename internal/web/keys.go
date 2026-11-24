package web

import (
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/Schenna43lp1/openchat/internal/store"
)

const (
	keyPath       = "/account/key"
	keyUnlockPath = "/account/key/unlock"

	// Per account. A browser stores a key once, the budget only stops scripts that hammer the endpoint.
	keyWriteBurst    = 5
	keyWriteInterval = time.Minute

	keyIDBytes   = 16
	keySaltBytes = 16
	// AES-GCM: 12 bytes IV, the PKCS#8 encoding of a P-256 key (about 138 bytes) and a 16 byte tag.
	minWrappedBytes = 12 + 64 + 16
	maxWrappedBytes = 1024
)

// b64 is binary data as unpadded base64url in JSON, the form browsers produce most easily.
type b64 []byte

func (b b64) MarshalJSON() ([]byte, error) {
	return json.Marshal(base64.RawURLEncoding.EncodeToString(b))
}

func (b *b64) UnmarshalJSON(raw []byte) error {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return err
	}
	decoded, err := base64.RawURLEncoding.DecodeString(text)
	if err != nil {
		return err
	}
	*b = decoded
	return nil
}

// ownKey describes the own account and its public key. The IDs go into the authenticated data of the wrapping.
type ownKey struct {
	UserID     int64   `json:"user_id"`
	Username   string  `json:"username"`
	InstanceID b64     `json:"instance_id"`
	Key        *keyDoc `json:"key"`
}

// keyDoc carries the wrapped private key only in answers to the password: with a session alone, it would let
// whoever stole the cookie guess the password offline.
type keyDoc struct {
	KeyID     string `json:"key_id"`
	PublicKey b64    `json:"public_key"`
	Salt      b64    `json:"key_salt,omitempty"`
	Wrapped   b64    `json:"wrapped,omitempty"`
	CreatedAt int64  `json:"created_at,omitempty"`
}

type newKeyRequest struct {
	Password  string `json:"password"`
	KeyID     string `json:"key_id"`
	PublicKey b64    `json:"public_key"`
	Salt      b64    `json:"key_salt"`
	Wrapped   b64    `json:"wrapped"`
}

type unlockRequest struct {
	Password string `json:"password"`
}

type publicKeyDoc struct {
	Username  string `json:"username"`
	KeyID     string `json:"key_id"`
	PublicKey b64    `json:"public_key"`
	CreatedAt int64  `json:"created_at"`
}

// accountKey describes the own key (GET) and stores the first one (PUT).
func (s *server) accountKey(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.getOwnKey(w, r)
	case http.MethodPut:
		s.putOwnKey(w, r)
	default:
		w.Header().Set("Allow", "GET, PUT")
		jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *server) getOwnKey(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	instance, err := s.Keys.InstanceID(r.Context())
	if err != nil {
		s.Logger.Error("read instance id", "err", err)
		jsonError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	doc := ownKey{UserID: user.ID, Username: user.Username, InstanceID: instance}
	key, err := s.Keys.CurrentKey(r.Context(), user.ID)
	switch {
	case err == nil:
		doc.Key = &keyDoc{KeyID: key.ID, PublicKey: key.PublicKey, CreatedAt: key.CreatedAt.UnixMilli()}
	case errors.Is(err, store.ErrNotFound):
	default:
		s.Logger.Error("read key", "user", user.Username, "err", err)
		jsonError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

func (s *server) putOwnKey(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	if allowed, wait := s.keyWrites.allow(strconv.FormatInt(user.ID, 10)); !allowed {
		setRetryAfter(w, wait)
		jsonError(w, http.StatusTooManyRequests, "too many requests")
		return
	}
	var req newKeyRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := validateKey(req); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !s.confirmOwnPassword(w, user, req.Password) {
		return
	}

	key, err := s.Keys.AddKey(r.Context(), store.UserKey{
		ID: req.KeyID, UserID: user.ID, PublicKey: req.PublicKey, Salt: req.Salt, Wrapped: req.Wrapped,
	}, user.PasswordHash)
	switch {
	case errors.Is(err, store.ErrTaken):
		jsonError(w, http.StatusConflict, "the account already has a key")
		return
	case errors.Is(err, store.ErrRefused), errors.Is(err, store.ErrNotFound):
		jsonError(w, http.StatusForbidden, "forbidden")
		return
	case err != nil:
		s.Logger.Error("store key", "user", user.Username, "err", err)
		jsonError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	s.Logger.Info("key created", "user", user.Username, "key_id", key.ID)
	writeJSON(w, http.StatusCreated, keyDoc{KeyID: key.ID, PublicKey: key.PublicKey, CreatedAt: key.CreatedAt.UnixMilli()})
}

// unlockKey hands out the wrapped private key against the password.
func (s *server) unlockKey(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	var req unlockRequest
	if !decodeJSON(w, r, &req) || !s.confirmOwnPassword(w, user, req.Password) {
		return
	}
	key, err := s.Keys.CurrentKey(r.Context(), user.ID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		jsonError(w, http.StatusNotFound, "no key")
		return
	case err != nil:
		s.Logger.Error("read key", "user", user.Username, "err", err)
		jsonError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	writeJSON(w, http.StatusOK, keyDoc{KeyID: key.ID, PublicKey: key.PublicKey, Salt: key.Salt, Wrapped: key.Wrapped,
		CreatedAt: key.CreatedAt.UnixMilli()})
}

// confirmOwnPassword uses the budgets of the password forms, so that a stolen session is no oracle for the password.
func (s *server) confirmOwnPassword(w http.ResponseWriter, user store.User, password string) bool {
	allowed, wait, settle := s.admitPassword(user.ID)
	if !allowed {
		setRetryAfter(w, wait)
		jsonError(w, http.StatusTooManyRequests, "too many requests")
		return false
	}
	err := s.Auth.ConfirmPassword(user, password)
	settle(err)
	if err != nil {
		jsonError(w, http.StatusForbidden, "wrong password")
		return false
	}
	return true
}

// decodeJSON reads exactly one JSON object without unknown fields.
func decodeJSON(w http.ResponseWriter, r *http.Request, into any) bool {
	if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mediaType != "application/json" {
		jsonError(w, http.StatusUnsupportedMediaType, "expected application/json")
		return false
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid request")
		return false
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		jsonError(w, http.StatusBadRequest, "invalid request")
		return false
	}
	return true
}

// validateKey checks the form only. Whether the wrapped key opens with the password, only the browser can tell.
func validateKey(req newKeyRequest) error {
	if id, err := base64.RawURLEncoding.DecodeString(req.KeyID); err != nil || len(id) != keyIDBytes {
		return errors.New("invalid key_id")
	}
	if _, err := ecdh.P256().NewPublicKey(req.PublicKey); err != nil {
		return errors.New("invalid public_key")
	}
	if len(req.Salt) != keySaltBytes {
		return errors.New("invalid key_salt")
	}
	if len(req.Wrapped) < minWrappedBytes || len(req.Wrapped) > maxWrappedBytes {
		return errors.New("invalid wrapped")
	}
	return nil
}

// publicKey serves the current public key of any account to signed-in accounts. A missing account and an account
// without a key look the same.
func (s *server) publicKey(w http.ResponseWriter, r *http.Request) {
	user, key, err := s.Keys.CurrentKeyByName(r.Context(), r.PathValue("name"))
	switch {
	case errors.Is(err, store.ErrNotFound):
		jsonError(w, http.StatusNotFound, "no key")
		return
	case err != nil:
		s.Logger.Error("read public key", "err", err)
		jsonError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	writeJSON(w, http.StatusOK, publicKeyDoc{Username: user.Username, KeyID: key.ID, PublicKey: key.PublicKey,
		CreatedAt: key.CreatedAt.UnixMilli()})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n'))
}

func jsonError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
