ALTER TABLE users ADD COLUMN must_change_password INTEGER NOT NULL DEFAULT 0 CHECK (must_change_password IN (0, 1));

-- Never cleared: otherwise demoting an admin would allow resetting their password in the app.
ALTER TABLE users ADD COLUMN operator_reset_only INTEGER NOT NULL DEFAULT 0 CHECK (operator_reset_only IN (0, 1));

-- Direct messages up to this ID are hidden from this account only.
ALTER TABLE users ADD COLUMN dm_visible_after_id INTEGER NOT NULL DEFAULT 0;

UPDATE users SET operator_reset_only = 1 WHERE role = 'admin';
