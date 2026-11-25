CREATE TABLE trips (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 100),
    destination TEXT NOT NULL CHECK (char_length(btrim(destination)) BETWEEN 1 AND 150),
    start_date DATE NOT NULL,
    end_date DATE NOT NULL CHECK (end_date >= start_date),
    budget BIGINT NOT NULL CHECK (budget BETWEEN 0 AND 1000000000000),
    currency TEXT NOT NULL DEFAULT 'RUB' CHECK (currency = 'RUB'),
    version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE trip_members (
    trip_id UUID NOT NULL REFERENCES trips (id),
    user_id UUID NOT NULL REFERENCES users (id),
    role TEXT NOT NULL CHECK (role IN ('owner', 'member')),
    joined_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    left_at TIMESTAMPTZ,
    PRIMARY KEY (trip_id, user_id),
    CHECK (role <> 'owner' OR left_at IS NULL)
);

CREATE UNIQUE INDEX trip_members_one_owner_idx ON trip_members (trip_id) WHERE role = 'owner';
CREATE INDEX trip_members_active_user_idx ON trip_members (user_id, trip_id) WHERE left_at IS NULL;

CREATE TABLE invitations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    trip_id UUID NOT NULL REFERENCES trips (id),
    created_by UUID NOT NULL,
    code_hash BYTEA NOT NULL UNIQUE CHECK (octet_length(code_hash) = 32),
    expires_at TIMESTAMPTZ NOT NULL,
    FOREIGN KEY (trip_id, created_by) REFERENCES trip_members (trip_id, user_id)
);

CREATE TABLE idempotency_keys (
    user_id UUID NOT NULL REFERENCES users (id),
    key UUID NOT NULL,
    request_hash BYTEA NOT NULL CHECK (octet_length(request_hash) = 32),
    response_body JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, key)
);

CREATE TABLE notifications (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    recipient_id UUID NOT NULL,
    trip_id UUID NOT NULL,
    actor_id UUID NOT NULL,
    event_type TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    read_at TIMESTAMPTZ,
    FOREIGN KEY (trip_id, recipient_id) REFERENCES trip_members (trip_id, user_id),
    FOREIGN KEY (trip_id, actor_id) REFERENCES trip_members (trip_id, user_id),
    CHECK (recipient_id <> actor_id)
);

CREATE INDEX notifications_recipient_idx ON notifications (recipient_id, created_at DESC, id DESC);
