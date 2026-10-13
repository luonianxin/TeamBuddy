package taskruntime

import (
	"context"
	"database/sql"

	"goteams-client/internal/applog"
	"goteams-client/internal/artifactstore"
)

// DeleteTaskSessionRowsTx 在删除 gt_cli_sessions 之前删掉事件和摘要。
// 必须在调用方的事务里、删除会话行之前执行。
func DeleteTaskSessionRowsTx(ctx context.Context, tx *sql.Tx, taskUUID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT uuid FROM gt_cli_sessions WHERE task_uuid = ?`, taskUUID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	for _, stmt := range []string{
		`DELETE FROM gt_session_events WHERE session_uuid IN (SELECT uuid FROM gt_cli_sessions WHERE task_uuid = ?)`,
		`DELETE FROM gt_conversation_summaries WHERE session_uuid IN (SELECT uuid FROM gt_cli_sessions WHERE task_uuid = ?)`,
	} {
		if _, err = tx.ExecContext(ctx, stmt, taskUUID); err != nil {
			return nil, err
		}
	}
	return ids, rows.Err()
}

// RemoveSessionArtifacts 在事务提交成功之后删除留存目录。失败只记日志。
func RemoveSessionArtifacts(ids []string) {
	store := artifactstore.Default()
	if store == nil {
		return
	}
	for _, id := range ids {
		if err := store.RemoveSession(id); err != nil {
			applog.Warn("[Cleanup] 删除会话留存目录失败", "session", id, "error", err)
		}
	}
}
