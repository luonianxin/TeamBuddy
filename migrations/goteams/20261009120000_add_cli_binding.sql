-- +goose Up
-- 增加微信通道CLI绑定配置字段，作为默认配置存储
ALTER TABLE gt_remote_wechat_state ADD COLUMN cli_type TEXT NOT NULL DEFAULT '';
ALTER TABLE gt_remote_wechat_state ADD COLUMN cli_model TEXT NOT NULL DEFAULT '';

-- +goose Down
-- 删除微信通道CLI绑定配置字段
ALTER TABLE gt_remote_wechat_state DROP COLUMN IF EXISTS cli_model;
ALTER TABLE gt_remote_wechat_state DROP COLUMN IF EXISTS cli_type;
