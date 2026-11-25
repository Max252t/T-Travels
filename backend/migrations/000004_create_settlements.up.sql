ALTER TABLE trips ADD COLUMN financial_revision BIGINT NOT NULL DEFAULT 1 CHECK (financial_revision > 0);

CREATE TABLE settlements (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    trip_id UUID NOT NULL REFERENCES trips (id),
    from_user_id UUID NOT NULL,
    to_user_id UUID NOT NULL,
    amount BIGINT NOT NULL CHECK (amount BETWEEN 1 AND 2000000000000),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (from_user_id <> to_user_id),
    UNIQUE (trip_id, id),
    FOREIGN KEY (trip_id, from_user_id) REFERENCES trip_members (trip_id, user_id),
    FOREIGN KEY (trip_id, to_user_id) REFERENCES trip_members (trip_id, user_id)
);

CREATE INDEX settlements_trip_created_idx ON settlements (trip_id, created_at DESC, id DESC);

ALTER TABLE notifications ADD COLUMN settlement_id UUID;
ALTER TABLE notifications ADD FOREIGN KEY (trip_id, settlement_id) REFERENCES settlements (trip_id, id);
