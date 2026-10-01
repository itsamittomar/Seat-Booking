-- Scope idempotency keys to (show, user, key) instead of (user, key).
-- A load script that reuses the same user ids and keys against a fresh show must start clean,
-- while a reused key on the same show with different seats is still rejected.
ALTER TABLE idempotency_keys ADD COLUMN IF NOT EXISTS show_id uuid REFERENCES shows(id);

UPDATE idempotency_keys k SET show_id = r.show_id
  FROM reservations r
 WHERE k.reservation_id = r.id AND k.show_id IS NULL;

-- Stored declines carry no show reference and only ever replay a 409; they cannot be re-scoped.
DELETE FROM idempotency_keys WHERE show_id IS NULL;

ALTER TABLE idempotency_keys ALTER COLUMN show_id SET NOT NULL;
ALTER TABLE idempotency_keys DROP CONSTRAINT IF EXISTS idempotency_keys_pkey;
ALTER TABLE idempotency_keys ADD PRIMARY KEY (show_id, user_id, key);
