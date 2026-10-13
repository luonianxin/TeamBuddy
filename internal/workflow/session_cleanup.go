package workflow

import (
	"context"
	"database/sql"
	"time"

	"goteams-client/internal/applog"
	"goteams-client/internal/artifactstore"
)

// SweepOrphanSessionRows 分批删掉会话行已经不存在的事件和摘要。重复执行无害。
func SweepOrphanSessionRows(ctx context.Context, db *sql.DB) (events, summaries int64, err error) {
	events, err = sweepOrphans(ctx, db, `DELETE FROM gt_session_events WHERE id IN (
		SELECT e.id FROM gt_session_events e LEFT JOIN gt_cli_sessions s ON s.uuid = e.session_uuid
		WHERE s.uuid IS NULL LIMIT 5000)`)
	if err != nil {
		return events, 0, err
	}
	summaries, err = sweepOrphans(ctx, db, `DELETE FROM gt_conversation_summaries WHERE uuid IN (
		SELECT c.uuid FROM gt_conversation_summaries c LEFT JOIN gt_cli_sessions s ON s.uuid = c.session_uuid
		WHERE s.uuid IS NULL LIMIT 5000)`)
	if events > 0 || summaries > 0 {
		applog.Info("[Cleanup] orphan session rows removed", "events", events, "summaries", summaries)
	}
	return events, summaries, err
}

func sweepOrphans(ctx context.Context, db *sql.DB, stmt string) (int64, error) {
	var total int64
	for {
		if ctx.Err() != nil {
			return total, ctx.Err()
		}
		result, err := db.ExecContext(ctx, stmt)
		if err != nil {
			return total, err
		}
		n, _ := result.RowsAffected()
		total += n
		if n == 0 {
			return total, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// StartSessionCleanup 启动时清一次孤儿行，之后每 6 小时清一次行和留存目录。
func StartSessionCleanup(ctx context.Context, db *sql.DB, store *artifactstore.ArtifactStore) {
	if store == nil {
		store = artifactstore.Default()
	}
	run := func() {
		_, _, _ = SweepOrphanSessionRows(ctx, db)
		if store == nil {
			return
		}
		store.Sweep(time.Now(), func(uuid string) (bool, bool, time.Time) {
			var status string
			var updated int64
			err := db.QueryRowContext(ctx, `SELECT status, updated_at FROM gt_cli_sessions WHERE uuid = ?`, uuid).Scan(&status, &updated)
			if err == sql.ErrNoRows {
				return false, false, time.Time{}
			}
			if err != nil {
				return true, true, time.Now()
			}
			running := status == "created" || status == "running" || status == "waiting_input" || status == "stop_requested"
			return true, running, time.UnixMilli(updated)
		})
	}
	go func() {
		run()
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				run()
			}
		}
	}()
}
