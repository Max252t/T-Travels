ALTER TABLE trips ADD COLUMN snapshot_revision BIGINT NOT NULL DEFAULT 1 CHECK (snapshot_revision > 0);

CREATE TABLE receipts (
    expense_id UUID PRIMARY KEY,
    trip_id UUID NOT NULL,
    object_key TEXT NOT NULL UNIQUE,
    content_type TEXT NOT NULL CHECK (content_type IN ('image/jpeg', 'image/png')),
    size_bytes BIGINT NOT NULL CHECK (size_bytes BETWEEN 1 AND 5242880),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY (trip_id, expense_id) REFERENCES expenses (trip_id, id)
);

CREATE TABLE receipt_cleanup (
    object_key TEXT PRIMARY KEY,
    delete_after TIMESTAMPTZ NOT NULL
);

CREATE INDEX receipt_cleanup_due_idx ON receipt_cleanup (delete_after, object_key);
