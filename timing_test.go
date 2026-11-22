// Tests that unknown names cost as much as wrong passwords.
package main

import (
	"errors"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestUnknownUserCostsOnePasswordCheck(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "users.json"))
	var costs []int
	store.compare = func(hash, password []byte) error {
		cost, err := bcrypt.Cost(hash)
		if err != nil {
			t.Fatal(err)
		}
		costs = append(costs, cost)
		return bcrypt.ErrMismatchedHashAndPassword
	}
	if _, err := store.Authenticate("ghost", testPassword); !errors.Is(err, errInvalidCredentials) {
		t.Fatalf("error = %v", err)
	}
	if len(costs) != 1 || costs[0] != bcrypt.DefaultCost {
		t.Fatalf("checks = %v, want one at the default cost", costs)
	}
}
