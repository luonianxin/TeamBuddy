-- +goose Up
CREATE TABLE gt_cloud_sync_outbox (
    task_uuid TEXT NOT NULL,
    execution_index INTEGER NOT NULL,
    cloud_source_key TEXT NOT NULL,
    cloud_user_id TEXT NOT NULL,
    revision INTEGER NOT NULL,
    content_hash TEXT NOT NULL,
    payload_json TEXT NOT NULL,
    delivered_revision INTEGER NOT NULL DEFAULT 0,
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (task_uuid, execution_index)
);
CREATE INDEX idx_cloud_sync_outbox_owner ON gt_cloud_sync_outbox (cloud_source_key, cloud_user_id, next_attempt_at);
