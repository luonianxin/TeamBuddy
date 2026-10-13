-- +goose Up
-- 微信通道恢复/完成任务时同样支持二次确认：
-- 用户首次发送 /r /d 后，把待处理的任务 UUID 写到 state 表里；
-- 用户下一条消息若是 yes / 是 / 对，则真正执行对应动作；
-- 任意非确认消息或命令会清空这两个字段，避免误操作。
ALTER TABLE gt_remote_wechat_state ADD COLUMN pending_resume_task_uuid TEXT NOT NULL DEFAULT '';
ALTER TABLE gt_remote_wechat_state ADD COLUMN pending_done_task_uuid TEXT NOT NULL DEFAULT '';