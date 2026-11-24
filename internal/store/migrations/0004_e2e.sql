-- Random per installation. Browsers mix it into the key derived from a password, so that tables computed for
-- one installation do not help with another.
CREATE TABLE instance (
    id          INTEGER PRIMARY KEY CHECK (id = 1),
    instance_id BLOB    NOT NULL CHECK (length(instance_id) = 16)
);

INSERT INTO instance (id, instance_id) VALUES (1, randomblob(16));

-- The key pairs of the accounts. The server only ever has the private key wrapped with a key derived from the
-- password in the browser. A retired key loses its wrapped part and stays, so that a change stays visible.
CREATE TABLE user_keys (
    key_id     TEXT    NOT NULL PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    public_key BLOB    NOT NULL,
    key_salt   BLOB,
    wrapped    BLOB,
    created_at INTEGER NOT NULL, -- Unix milliseconds
    retired_at INTEGER,
    CHECK ((retired_at IS NULL AND key_salt IS NOT NULL AND wrapped IS NOT NULL) OR
           (retired_at IS NOT NULL AND key_salt IS NULL AND wrapped IS NULL))
);

CREATE UNIQUE INDEX user_keys_current ON user_keys (user_id) WHERE retired_at IS NULL;
CREATE INDEX user_keys_by_user ON user_keys (user_id, created_at);

-- 'e2e-v1': body is an envelope that only browsers can open.
ALTER TABLE messages ADD COLUMN format TEXT NOT NULL DEFAULT 'plain' CHECK (format IN ('plain', 'e2e-v1'));
