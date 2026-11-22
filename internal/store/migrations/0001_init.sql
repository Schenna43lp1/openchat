-- AUTOINCREMENT: an ID must never be reused, it decides who may read a message.
CREATE TABLE users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    password_hash TEXT    NOT NULL,
    role          TEXT    NOT NULL CHECK (role IN ('admin', 'moderator', 'user')),
    banned        INTEGER NOT NULL DEFAULT 0 CHECK (banned IN (0, 1)),
    created_at    INTEGER -- Unix milliseconds, NULL if an imported account had no valid time
);

CREATE TABLE sessions (
    token_hash BLOB    NOT NULL PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    expires_at INTEGER NOT NULL
) WITHOUT ROWID;

CREATE INDEX sessions_by_user ON sessions (user_id);
CREATE INDEX sessions_by_expiry ON sessions (expires_at);

-- The IDs decide visibility. The names are only for display.
-- An imported message keeps a NULL ID where no account may claim it.
CREATE TABLE messages (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    kind           TEXT    NOT NULL CHECK (kind IN ('message', 'direct')),
    sender_id      INTEGER REFERENCES users (id) ON DELETE SET NULL,
    sender_name    TEXT    NOT NULL,
    recipient_id   INTEGER REFERENCES users (id) ON DELETE SET NULL,
    recipient_name TEXT    NOT NULL DEFAULT '',
    body           TEXT    NOT NULL,
    created_at     INTEGER NOT NULL
);

CREATE INDEX messages_by_kind ON messages (kind, id);
CREATE INDEX messages_by_sender ON messages (sender_id, kind, id);
CREATE INDEX messages_by_recipient ON messages (recipient_id, kind, id);

CREATE TABLE legacy_import (
    id          INTEGER PRIMARY KEY CHECK (id = 1),
    imported_at INTEGER NOT NULL
);
