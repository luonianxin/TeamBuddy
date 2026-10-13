-- +goose Up
-- 通道级别 CLI 配置表，与连接状态完全解耦。
-- channel_type 为通道标识（如 wechat），cli_type / cli_model 为该通道的默认兜底 CLI。
CREATE TABLE IF NOT EXISTS gt_remote_channel_cli_configs (
    channel_type TEXT PRIMARY KEY,
    cli_type     TEXT NOT NULL DEFAULT '',
    cli_model    TEXT NOT NULL DEFAULT '',
    updated_at   INTEGER NOT NULL DEFAULT 0
);

-- +goose Down
DROP TABLE IF EXISTS gt_remote_channel_cli_configs;
