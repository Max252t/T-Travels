CREATE TABLE expense_disputes (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    trip_id UUID NOT NULL,
    expense_id UUID NOT NULL,
    created_by UUID NOT NULL,
    reason TEXT NOT NULL CHECK (char_length(btrim(reason)) BETWEEN 1 AND 1000),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at TIMESTAMPTZ,
    FOREIGN KEY (trip_id, expense_id) REFERENCES expenses (trip_id, id),
    FOREIGN KEY (trip_id, created_by) REFERENCES trip_members (trip_id, user_id),
    CHECK (resolved_at IS NULL OR resolved_at >= created_at)
);

CREATE UNIQUE INDEX expense_disputes_one_open_idx ON expense_disputes (expense_id) WHERE resolved_at IS NULL;
CREATE INDEX expense_disputes_open_trip_idx ON expense_disputes (trip_id) WHERE resolved_at IS NULL;

ALTER TABLE notifications ADD COLUMN member_id UUID;
ALTER TABLE notifications ADD FOREIGN KEY (trip_id, member_id) REFERENCES trip_members (trip_id, user_id);
