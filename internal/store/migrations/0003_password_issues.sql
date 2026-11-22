-- Every password that somebody else chose for an account. The owner sees it, nobody can remove it in the app:
-- an admin could otherwise use an issued password and cover that with a second reset.
CREATE TABLE password_issues (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id     INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    kind        TEXT    NOT NULL CHECK (kind IN ('created', 'reset', 'operator')),
    actor_name  TEXT    NOT NULL, -- empty for the operator, only for display
    issued_at   INTEGER NOT NULL,
    redeemed_at INTEGER           -- when an own password was set with it
);

CREATE INDEX password_issues_by_user ON password_issues (user_id, id);
