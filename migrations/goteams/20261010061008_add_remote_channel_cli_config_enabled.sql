-- +goose Up
-- 为 gt_remote_channel_cli_configs 增加 enabled 字段，用于在远程通道页面单独控制
-- 该通道 CLI 兜底处理是否生效。新列默认值为 1，保留已有用户当前的兜底行为。
ALTER TABLE gt_remote_channel_cli_configs ADD COLUMN enabled INTEGER NOT NULL DEFAULT 1;
