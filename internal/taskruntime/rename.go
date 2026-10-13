package taskruntime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrInvalidTaskTitle = errors.New("invalid task title")

// Rename changes only task metadata and the Markdown heading. The caller holds
// the task execution gate so deletion cannot race with the file update.
func (s *Service) Rename(ctx context.Context, taskUUID, title string) error {
	title = strings.TrimSpace(title)
	if title == "" || strings.ContainsAny(title, "\r\n") || utf8.RuneCountInString(title) > 200 {
		return ErrInvalidTaskTitle
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var path string
	if err = tx.QueryRowContext(ctx, `SELECT task_md_path FROM gt_tasks WHERE uuid=?`, taskUUID).Scan(&path); err == sql.ErrNoRows {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	root, err := filepath.EvalSymlinks(s.taskRoot)
	if err != nil {
		return err
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	if rel, err := filepath.Rel(root, canonical); err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("task markdown outside task root")
	}
	path = canonical
	existing, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	// Keep the body byte-for-byte, including internal attachment references.
	lineEnd := strings.IndexByte(string(existing), '\n')
	if !strings.HasPrefix(string(existing), "# ") || lineEnd < 0 {
		return fmt.Errorf("invalid task markdown heading")
	}
	updated := "# " + title + string(existing[lineEnd:])
	if _, err = tx.ExecContext(ctx, `UPDATE gt_tasks SET title=?, updated_at=? WHERE uuid=?`, title, time.Now().UnixMilli(), taskUUID); err != nil {
		return err
	}
	if err = replaceTaskTitleFile(path, []byte(updated)); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return errors.Join(err, replaceTaskTitleFile(path, existing))
	}
	return nil
}

// Write beside the original and replace it, so a failed write never truncates
// task.md. Keep this private to renaming rather than changing existing editors.
func replaceTaskTitleFile(path string, content []byte) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".task-title-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	writeErr := file.Chmod(info.Mode().Perm())
	if writeErr == nil {
		_, writeErr = file.Write(content)
	}
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
