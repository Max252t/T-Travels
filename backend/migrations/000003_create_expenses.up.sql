CREATE TABLE expenses (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    trip_id UUID NOT NULL REFERENCES trips (id),
    payer_id UUID NOT NULL,
    created_by UUID NOT NULL,
    title TEXT NOT NULL CHECK (char_length(btrim(title)) BETWEEN 1 AND 120),
    description TEXT CHECK (char_length(description) <= 1000),
    category TEXT NOT NULL CHECK (category ~ '^[a-z][a-z0-9_]{0,31}$'),
    amount BIGINT NOT NULL CHECK (amount BETWEEN 1 AND 1000000000000),
    split_type TEXT NOT NULL CHECK (split_type IN ('equal', 'exact', 'percentage')),
    expense_date DATE NOT NULL,
    version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
    deleted_at TIMESTAMPTZ,
    UNIQUE (trip_id, id),
    FOREIGN KEY (trip_id, payer_id) REFERENCES trip_members (trip_id, user_id),
    FOREIGN KEY (trip_id, created_by) REFERENCES trip_members (trip_id, user_id)
);

CREATE INDEX expenses_active_trip_date_idx ON expenses (trip_id, expense_date DESC, id DESC) WHERE deleted_at IS NULL;

CREATE TABLE expense_splits (
    trip_id UUID NOT NULL,
    expense_id UUID NOT NULL,
    user_id UUID NOT NULL,
    amount BIGINT NOT NULL CHECK (amount BETWEEN 0 AND 1000000000000),
    basis_points BIGINT CHECK (basis_points BETWEEN 0 AND 10000),
    PRIMARY KEY (expense_id, user_id),
    FOREIGN KEY (trip_id, expense_id) REFERENCES expenses (trip_id, id),
    FOREIGN KEY (trip_id, user_id) REFERENCES trip_members (trip_id, user_id)
);

ALTER TABLE notifications ADD COLUMN expense_id UUID;
ALTER TABLE notifications ADD FOREIGN KEY (trip_id, expense_id) REFERENCES expenses (trip_id, id);
