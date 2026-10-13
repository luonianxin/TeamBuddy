-- +goose Up
CREATE TABLE gt_sidebar_workspaces (
    directory_key TEXT PRIMARY KEY,
    work_dir TEXT NOT NULL,
    display_name TEXT NOT NULL DEFAULT '',
    pinned_at INTEGER NOT NULL DEFAULT 0
);
