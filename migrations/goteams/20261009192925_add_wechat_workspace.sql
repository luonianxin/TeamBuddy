-- +goose Up
-- 微信通道等远端通道的兜底任务需要独立工作目录，确保 CLI 执行路径可追溯。
-- workspace 为空时由业务层使用默认目录（如 ~/goteams/wechat_workspace）。
ALTER TABLE gt_remote_channel_cli_configs ADD COLUMN workspace TEXT NOT NULL DEFAULT '';
