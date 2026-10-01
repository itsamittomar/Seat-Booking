CREATE TABLE IF NOT EXISTS shows (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name             text NOT NULL,
    price_paise      bigint NOT NULL CHECK (price_paise >= 0),
    per_user_limit   int NOT NULL CHECK (per_user_limit > 0),
    hold_ttl_seconds int NOT NULL CHECK (hold_ttl_seconds > 0),
    created_at       timestamptz NOT NULL DEFAULT now()
);

DO $$ BEGIN
    CREATE TYPE seat_status AS ENUM ('available', 'held', 'confirmed');
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

-- One row per physical seat. The primary key makes a second copy of a seat impossible;
-- status transitions are guarded updates under row locks.
CREATE TABLE IF NOT EXISTS seats (
    show_id         uuid NOT NULL REFERENCES shows(id),
    label           text NOT NULL,
    status          seat_status NOT NULL DEFAULT 'available',
    reservation_id  uuid,
    held_by         text,
    hold_expires_at timestamptz,
    PRIMARY KEY (show_id, label),
    -- a non-available seat always names its owner; an available seat names nobody
    CHECK ((status = 'available') = (reservation_id IS NULL AND held_by IS NULL)),
    CHECK ((status = 'held') = (hold_expires_at IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS seats_held_by_idx ON seats (show_id, held_by) WHERE held_by IS NOT NULL;
CREATE INDEX IF NOT EXISTS seats_reservation_idx ON seats (reservation_id) WHERE reservation_id IS NOT NULL;

DO $$ BEGIN
    CREATE TYPE reservation_status AS ENUM ('held', 'confirmed', 'cancelled', 'expired');
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

CREATE TABLE IF NOT EXISTS reservations (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    show_id      uuid NOT NULL REFERENCES shows(id),
    user_id      text NOT NULL,
    status       reservation_status NOT NULL,
    seats        text[] NOT NULL,
    amount_paise bigint NOT NULL CHECK (amount_paise >= 0),
    expires_at   timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS reservations_expiry_idx ON reservations (expires_at) WHERE status = 'held';

-- Exactly-once store for reserve requests, scoped per user.
CREATE TABLE IF NOT EXISTS idempotency_keys (
    user_id         text NOT NULL,
    key             text NOT NULL,
    request_hash    text NOT NULL,
    reservation_id  uuid REFERENCES reservations(id),
    decline_code    text,
    decline_message text,
    created_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, key)
);
