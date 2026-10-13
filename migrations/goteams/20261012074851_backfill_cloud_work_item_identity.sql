-- +goose Up
-- 团队看板按 work_item_* 认已配置任务，历史唯一索引按 cloud_work_item_*。
-- 只写了其中一侧时，换版本后同一条本地任务会被看成「待配置」。
-- 把非空的一侧补到空的一侧；会撞上唯一索引的行留着，避免升级中断。

UPDATE gt_tasks
SET cloud_work_item_type = work_item_type,
    cloud_work_item_id = work_item_id
WHERE source_type = 'cloud'
  AND IFNULL(cloud_work_item_id, '') = ''
  AND IFNULL(work_item_id, '') <> ''
  AND NOT EXISTS (
    SELECT 1 FROM gt_tasks AS other
    WHERE other.rowid <> gt_tasks.rowid
      AND other.source_type = 'cloud'
      AND other.cloud_source_key = gt_tasks.cloud_source_key
      AND other.cloud_user_id = gt_tasks.cloud_user_id
      AND other.cloud_work_item_type = gt_tasks.work_item_type
      AND other.cloud_work_item_id = gt_tasks.work_item_id
  );

UPDATE gt_tasks
SET work_item_type = cloud_work_item_type,
    work_item_id = cloud_work_item_id
WHERE source_type = 'cloud'
  AND IFNULL(work_item_id, '') = ''
  AND IFNULL(cloud_work_item_id, '') <> ''
  AND NOT EXISTS (
    SELECT 1 FROM gt_tasks AS other
    WHERE other.rowid <> gt_tasks.rowid
      AND other.source_type = 'cloud'
      AND other.cloud_source_key = gt_tasks.cloud_source_key
      AND other.cloud_user_id = gt_tasks.cloud_user_id
      AND other.work_item_type = gt_tasks.cloud_work_item_type
      AND other.work_item_id = gt_tasks.cloud_work_item_id
  );
