package cloudsync

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"goteams-client/internal/cloud"
	"goteams-client/internal/pipeline"
)

type syncQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type executionSnapshot struct {
	Task  cloud.PipelineTaskSync `json:"task"`
	Steps []stepSnapshot         `json:"steps"`
}

type stepSnapshot struct {
	Payload   cloud.PipelineTaskSessionSync `json:"payload"`
	StepUUID  string                        `json:"step_uuid"`
	Watermark int64                         `json:"watermark"`
}

func ownsTask(ctx context.Context, db syncQueryer, client *cloud.Client, taskUUID string) (bool, error) {
	primary, alternate := pipeline.CloudSourceKeys(client.BaseURL())
	var count int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM gt_tasks
		WHERE uuid=? AND source_type='cloud' AND cloud_source_key IN (?, ?) AND cloud_user_id=?`,
		taskUUID, primary, alternate, client.UserID()).Scan(&count)
	return count == 1, err
}

func pendingTaskKeys(ctx context.Context, db *sql.DB, client *cloud.Client) ([]string, error) {
	primary, alternate := pipeline.CloudSourceKeys(client.BaseURL())
	rows, err := db.QueryContext(ctx, `SELECT uuid FROM gt_tasks WHERE source_type='cloud'
		AND cloud_source_key IN (?, ?) AND cloud_user_id=?
		UNION SELECT task_uuid FROM gt_cloud_sync_outbox WHERE cloud_source_key IN (?, ?)
		AND cloud_user_id=? AND delivered_revision < revision ORDER BY 1`,
		primary, alternate, client.UserID(), primary, alternate, client.UserID())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

// CaptureTaskExecution persists a consistent snapshot before doing any I/O to
// the cloud. The switch transaction calls the Tx variant before deleting rows.
func CaptureTaskExecution(ctx context.Context, db *sql.DB, taskUUID string) error {
	if db == nil {
		return fmt.Errorf("task database is not ready")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = CaptureTaskExecutionTx(ctx, tx, taskUUID); err != nil {
		return err
	}
	return tx.Commit()
}

func CaptureTaskExecutionTx(ctx context.Context, tx *sql.Tx, taskUUID string) error {
	var source, sourceKey, userID, mode, tool string
	err := tx.QueryRowContext(ctx, `SELECT source_type, cloud_source_key, cloud_user_id,
		execution_mode, execution_tool FROM gt_tasks WHERE uuid=?`, taskUUID).
		Scan(&source, &sourceKey, &userID, &mode, &tool)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if source != "cloud" {
		return nil
	}
	task, err := BuildTaskSync(ctx, tx, taskUUID, 0)
	if err != nil {
		return err
	}
	if task == nil {
		return fmt.Errorf("cloud task projection is incomplete")
	}
	snapshot := executionSnapshot{Task: *task, Steps: make([]stepSnapshot, 0)}
	// Timestamps used solely for transport versions are assigned after hashing.
	snapshot.Task.LocalRevision = 0
	rows, err := tx.QueryContext(ctx, `SELECT st.uuid FROM gt_task_steps st WHERE st.task_uuid=?
		AND EXISTS (SELECT 1 FROM gt_cli_sessions s WHERE s.task_uuid=st.task_uuid AND s.step_uuid=st.uuid)
		ORDER BY st.step_order, st.rowid`, taskUUID)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		payload, watermark, err := buildStepSessionPayload(ctx, tx, taskUUID, id)
		if err != nil {
			return err
		}
		snapshot.Steps = append(snapshot.Steps, stepSnapshot{Payload: *payload, StepUUID: id, Watermark: watermark})
	}
	if mode == "vibe_coding" {
		payload, err := buildVibeSession(ctx, tx, taskUUID, tool, task.ExecutionIndex)
		if err != nil {
			return err
		}
		// Vibe has no managed process to update task execution counters. Export
		// the Skill conversation's completed state and timeline with its body.
		if payload.Status == "running" || payload.Status == "success" {
			snapshot.Task.ExecutionStatus = payload.Status
		}
		snapshot.Task.Pipeline.CliType = tool
		snapshot.Task.Usage.ConversationRounds = payload.ConversationRounds
		snapshot.Task.StartedAt = unixMillisToRFC3339(payload.StartedAt)
		snapshot.Task.FinishedAt = unixMillisToRFC3339(payload.FinishedAt)
		snapshot.Steps = append(snapshot.Steps, stepSnapshot{Payload: *payload})
	}
	content, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])
	var previousHash string
	var previousRevision int64
	err = tx.QueryRowContext(ctx, `SELECT content_hash, revision FROM gt_cloud_sync_outbox
		WHERE task_uuid=? AND execution_index=?`, taskUUID, task.ExecutionIndex).Scan(&previousHash, &previousRevision)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if previousHash == hash {
		return nil
	}
	revision := max(time.Now().UnixMilli(), previousRevision+1)
	snapshot.Task.LocalRevision = revision
	for i := range snapshot.Steps {
		snapshot.Steps[i].Payload.ClientUpdatedAt = revision
	}
	content, err = json.Marshal(snapshot)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO gt_cloud_sync_outbox
		(task_uuid, execution_index, cloud_source_key, cloud_user_id, revision, content_hash, payload_json)
		VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT(task_uuid, execution_index) DO UPDATE SET
		revision=excluded.revision, content_hash=excluded.content_hash, payload_json=excluded.payload_json`,
		taskUUID, task.ExecutionIndex, sourceKey, userID, revision, hash, string(content))
	return err
}

func deliverTaskSnapshots(ctx context.Context, db *sql.DB, client *cloud.Client, taskUUID string) error {
	primary, alternate := pipeline.CloudSourceKeys(client.BaseURL())
	for count := 0; count < maxStepPushesPerTaskPerRound; count++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		var raw string
		var index, attempts int
		var revision, nextAttempt int64
		err := db.QueryRowContext(ctx, `SELECT execution_index, revision, payload_json, attempts, next_attempt_at
			FROM gt_cloud_sync_outbox WHERE task_uuid=? AND cloud_source_key IN (?, ?) AND cloud_user_id=?
			AND delivered_revision < revision ORDER BY execution_index LIMIT 1`,
			taskUUID, primary, alternate, client.UserID()).Scan(&index, &revision, &raw, &attempts, &nextAttempt)
		if err == sql.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		if nextAttempt > time.Now().UnixMilli() {
			return nil
		}
		var snapshot executionSnapshot
		if err = json.Unmarshal([]byte(raw), &snapshot); err != nil {
			return err
		}
		err = pushSnapshot(ctx, client, snapshot)
		if err != nil {
			delay := min(300, 10<<min(attempts, 5))
			_, saveErr := db.ExecContext(ctx, `UPDATE gt_cloud_sync_outbox SET attempts=attempts+1,
				next_attempt_at=?, last_error=? WHERE task_uuid=? AND execution_index=? AND revision=?`,
				time.Now().Add(time.Duration(delay)*time.Second).UnixMilli(), truncateRunes(err.Error(), syncErrorMaxRunes), taskUUID, index, revision)
			if saveErr != nil {
				return fmt.Errorf("upload failed: %v; save retry state: %w", err, saveErr)
			}
			return err
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if err = acknowledgeSnapshot(ctx, tx, snapshot, revision); err == nil {
			err = tx.Commit()
		} else {
			_ = tx.Rollback()
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func pushSnapshot(ctx context.Context, client *cloud.Client, snapshot executionSnapshot) error {
	if snapshot.Task.WorkItem.ID <= 0 {
		return fmt.Errorf("cloud work-item identity is missing")
	}
	if snapshot.Task.WorkItem.WorkspaceID <= 0 || (snapshot.Task.WorkItem.Type != "requirement" && snapshot.Task.WorkItem.Type != "defect") {
		work, err := client.GetMyWork(ctx)
		if err != nil {
			return err
		}
		workspace, kind, found := cloud.LookupMyWorkItem(work.List, fmt.Sprint(snapshot.Task.WorkItem.ID))
		if !found || workspace <= 0 {
			return fmt.Errorf("cloud work-item link is unresolved")
		}
		snapshot.Task.WorkItem.WorkspaceID, snapshot.Task.WorkItem.Type = workspace, kind
		content, err := json.Marshal(snapshot.Task)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(content)
		snapshot.Task.ProjectionHash = hex.EncodeToString(sum[:])
	}
	result, err := client.PushPipelineTaskSync(ctx, snapshot.Task)
	if err != nil {
		return err
	}
	if result.Result != "accepted" && result.Result != "duplicate" {
		return fmt.Errorf("task projection was not accepted: %s", result.Result)
	}
	for _, step := range snapshot.Steps {
		if err = client.PushPipelineTaskSession(ctx, step.Payload); err != nil {
			return err
		}
	}
	return nil
}

func acknowledgeSnapshot(ctx context.Context, tx *sql.Tx, snapshot executionSnapshot, revision int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE gt_cloud_sync_outbox SET delivered_revision=?, payload_json='',
		attempts=0, next_attempt_at=0, last_error='' WHERE task_uuid=? AND execution_index=? AND revision=?`,
		revision, snapshot.Task.LocalTaskUUID, snapshot.Task.ExecutionIndex, revision)
	if err != nil {
		return err
	}
	for _, step := range snapshot.Steps {
		if step.StepUUID == "" {
			continue
		}
		_, err = tx.ExecContext(ctx, `UPDATE gt_cli_sessions SET cloud_sync_status='success', cloud_sync_error='',
			cloud_synced_at=updated_at WHERE task_uuid=? AND step_uuid=? AND updated_at<=?
			AND EXISTS (SELECT 1 FROM gt_tasks t WHERE t.uuid=gt_cli_sessions.task_uuid AND t.execution_index=?)`,
			snapshot.Task.LocalTaskUUID, step.StepUUID, step.Watermark, snapshot.Task.ExecutionIndex)
		if err != nil {
			return err
		}
	}
	return nil
}
