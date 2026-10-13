-- +goose Up
-- 微信通道取消任务时支持二次确认：
-- 用户首次发送 /c 后，把待取消的任务 UUID 写到 state 表里；
-- 用户下一条消息若是 yes / 是 / 对 / 取消 / y，则真正执行取消；
-- 任意非确认消息或命令会清空该字段，避免误取消。
ALTER TABLE gt_remote_wechat_state ADD COLUMN pending_cancel_task_uuid TEXT NOT NULL DEFAULT '';
