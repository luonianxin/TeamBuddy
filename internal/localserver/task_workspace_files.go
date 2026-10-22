package localserver

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"goteams-client/internal/i18n"
)

const (
	workspaceEntryLimit       = 1000
	workspaceSearchLimit      = 100
	workspaceSearchVisitLimit = 10000
	workspaceTextLimit        = 2 << 20
	workspaceImageLimit       = 10 << 20
)

var (
	errWorkspaceRootNotFound = errors.New("workspace root not found")
	errWorkspaceUnavailable  = errors.New("workspace unavailable")
	errWorkspacePathInvalid  = errors.New("workspace path invalid")
	errWorkspacePathOutside  = errors.New("workspace path outside root")
	errWorkspacePathSymlink  = errors.New("workspace path contains symlink")
	errWorkspaceFileTooLarge = errors.New("workspace file exceeds limit")
)

type taskWorkspaceRoot struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	Primary   bool   `json:"primary"`
	Available bool   `json:"available"`
	ErrorCode string `json:"error_code,omitempty"`
}

type taskWorkspaceEntry struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	IsDir      bool   `json:"is_dir"`
	FileType   string `json:"file_type,omitempty"`
	Size       int64  `json:"size,omitempty"`
	ModifiedAt int64  `json:"modified_at,omitempty"`
	Hidden     bool   `json:"hidden,omitempty"`
	Ignored    bool   `json:"ignored,omitempty"`
}

func (h *TasksHandler) listTaskWorkspaceRoots(c *gin.Context) {
	roots, err := h.loadTaskWorkspaceRoots(c.Request.Context(), c.Param("uuid"))
	if err != nil {
		h.writeWorkspaceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"roots": roots})
}

func (h *TasksHandler) listTaskWorkspaceEntries(c *gin.Context) {
	root, err := h.resolveTaskWorkspaceRoot(c.Request.Context(), c.Param("uuid"), c.Query("root_id"))
	if err != nil {
		h.writeWorkspaceError(c, err)
		return
	}
	relativePath := strings.TrimSpace(c.Query("path"))
	directory, _, target, err := openVerifiedWorkspacePath(root.Path, relativePath, true)
	if err != nil {
		h.writeWorkspaceError(c, err)
		return
	}
	defer directory.Close()
	entries, err := directory.ReadDir(-1)
	if err != nil {
		h.writeWorkspaceError(c, fmt.Errorf("%w: %v", errWorkspaceUnavailable, err))
		return
	}
	_ = target
	showIgnored := c.Query("show_ignored") == "true"
	result := make([]taskWorkspaceEntry, 0, len(entries))
	for _, entry := range entries {
		if err := c.Request.Context().Err(); err != nil {
			return
		}
		ignored := workspaceIgnoredName(entry.Name(), entry.IsDir())
		if entry.Name() == ".git" || (ignored && !showIgnored) {
			continue
		}
		item, ok := workspaceEntryFromDirEntry(relativePath, entry, ignored)
		if ok {
			result = append(result, item)
		}
	}
	sortWorkspaceEntries(result)
	truncated := len(result) > workspaceEntryLimit
	if truncated {
		result = result[:workspaceEntryLimit]
	}
	c.JSON(http.StatusOK, gin.H{
		"root_id":   root.ID,
		"path":      filepath.ToSlash(filepath.Clean(relativePath)),
		"entries":   result,
		"truncated": truncated,
		"limit":     workspaceEntryLimit,
	})
}

func (h *TasksHandler) getTaskWorkspaceContent(c *gin.Context) {
	root, err := h.resolveTaskWorkspaceRoot(c.Request.Context(), c.Param("uuid"), c.Query("root_id"))
	if err != nil {
		h.writeWorkspaceError(c, err)
		return
	}
	relativePath := strings.TrimSpace(c.Query("path"))
	if relativePath == "" {
		i18n.Error(c, http.StatusBadRequest, "localserver_path_required", "path_required")
		return
	}
	file, info, target, err := openVerifiedWorkspacePath(root.Path, relativePath, false)
	if err != nil {
		h.writeWorkspaceError(c, err)
		return
	}
	defer file.Close()

	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(target), "."))
	encoding := "text"
	limit := int64(workspaceTextLimit)
	if isImageFileType(ext) {
		encoding = "base64"
		limit = workspaceImageLimit
	}
	if info.Size() > limit {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{
			"error": i18n.T(c, "localserver_bad_request"),
			"code":  "workspace_file_too_large",
			"size":  info.Size(),
			"limit": limit,
		})
		return
	}
	content, err := readWorkspaceFile(file, limit)
	if err != nil {
		h.writeWorkspaceError(c, err)
		return
	}
	if encoding == "text" && !workspaceTextFile(target, content) {
		c.JSON(http.StatusUnsupportedMediaType, gin.H{
			"error": i18n.Format(c, "localserver_readable_file_type", i18n.Params{"FileType": ext}),
			"code":  "workspace_binary_file",
		})
		return
	}
	rendered := string(content)
	if encoding == "base64" {
		rendered = base64.StdEncoding.EncodeToString(content)
	}
	mimeType := mime.TypeByExtension(filepath.Ext(target))
	if mimeType == "" && encoding == "text" {
		mimeType = "text/plain; charset=utf-8"
	}
	c.JSON(http.StatusOK, gin.H{
		"root_id":     root.ID,
		"path":        filepath.ToSlash(relativePath),
		"name":        filepath.Base(target),
		"file_type":   ext,
		"mime_type":   mimeType,
		"size":        info.Size(),
		"modified_at": info.ModTime().UnixMilli(),
		"encoding":    encoding,
		"content":     rendered,
		"read_only":   true,
	})
}

func (h *TasksHandler) searchTaskWorkspace(c *gin.Context) {
	root, err := h.resolveTaskWorkspaceRoot(c.Request.Context(), c.Param("uuid"), c.Query("root_id"))
	if err != nil {
		h.writeWorkspaceError(c, err)
		return
	}
	query := strings.ToLower(strings.TrimSpace(c.Query("q")))
	if len([]rune(query)) < 2 {
		i18n.LocalServerKeyError(c, http.StatusBadRequest, "localserver_bad_request", "workspace_search_query_short")
		return
	}
	limit := workspaceSearchLimit
	if requested, parseErr := strconv.Atoi(c.Query("limit")); parseErr == nil && requested > 0 {
		limit = min(requested, workspaceSearchLimit)
	}
	searchCtx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()
	showIgnored := c.Query("show_ignored") == "true"
	results := make([]taskWorkspaceEntry, 0, limit)
	visited := 0
	truncated := false
	partial := false
	truncationReason := ""
	walkErr := filepath.WalkDir(root.Path, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			partial = true
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if err := searchCtx.Err(); err != nil {
			truncated = true
			truncationReason = "timeout"
			return err
		}
		if path == root.Path {
			return nil
		}
		visited++
		if visited > workspaceSearchVisitLimit {
			truncated = true
			truncationReason = "visit_limit"
			return io.EOF
		}
		ignored := workspaceIgnoredName(entry.Name(), entry.IsDir())
		if entry.IsDir() && (entry.Name() == ".git" || (ignored && !showIgnored)) {
			return filepath.SkipDir
		}
		if ignored && !showIgnored {
			return nil
		}
		if !strings.Contains(strings.ToLower(entry.Name()), query) {
			return nil
		}
		relativePath, relErr := filepath.Rel(root.Path, path)
		if relErr != nil {
			partial = true
			return nil
		}
		item, ok := workspaceEntryFromDirEntry(filepath.Dir(relativePath), entry, ignored)
		if !ok {
			partial = true
			return nil
		}
		item.Path = filepath.ToSlash(relativePath)
		results = append(results, item)
		if len(results) >= limit {
			truncated = true
			truncationReason = "result_limit"
			return io.EOF
		}
		return nil
	})
	if c.Request.Context().Err() != nil {
		return
	}
	if walkErr != nil && !errors.Is(walkErr, io.EOF) && !errors.Is(walkErr, context.Canceled) && !errors.Is(walkErr, context.DeadlineExceeded) {
		h.writeWorkspaceError(c, fmt.Errorf("%w: %v", errWorkspaceUnavailable, walkErr))
		return
	}
	sortWorkspaceEntries(results)
	c.JSON(http.StatusOK, gin.H{
		"root_id":           root.ID,
		"query":             query,
		"entries":           results,
		"truncated":         truncated,
		"truncation_reason": truncationReason,
		"partial":           partial,
		"limit":             limit,
	})
}

func (h *TasksHandler) loadTaskWorkspaceRoots(ctx context.Context, taskUUID string) ([]taskWorkspaceRoot, error) {
	db := h.taskDB()
	if db == nil {
		return nil, errors.New("本地任务数据库尚未就绪")
	}
	var primaryPath string
	if err := db.QueryRowContext(ctx, `SELECT work_dir FROM gt_tasks WHERE uuid = ?`, taskUUID).Scan(&primaryPath); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, sql.ErrNoRows
		}
		return nil, err
	}
	paths := make([]string, 0, 4)
	if strings.TrimSpace(primaryPath) != "" {
		paths = append(paths, primaryPath)
	}
	rows, err := db.QueryContext(ctx, `SELECT path FROM gt_task_work_dirs WHERE task_uuid = ? ORDER BY sort_order, id`, taskUUID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		if strings.TrimSpace(path) != "" {
			paths = append(paths, path)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	roots := make([]taskWorkspaceRoot, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, rawPath := range paths {
		absolute, err := filepath.Abs(strings.TrimSpace(rawPath))
		if err != nil {
			continue
		}
		absolute = filepath.Clean(absolute)
		key := workspacePathKey(absolute)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		available, errorCode := workspaceRootAvailability(absolute)
		roots = append(roots, taskWorkspaceRoot{
			ID:        workspaceRootID(taskUUID, absolute),
			Name:      workspaceRootName(absolute),
			Path:      absolute,
			Primary:   len(roots) == 0,
			Available: available,
			ErrorCode: errorCode,
		})
	}
	return roots, nil
}

func (h *TasksHandler) resolveTaskWorkspaceRoot(ctx context.Context, taskUUID, rootID string) (taskWorkspaceRoot, error) {
	if strings.TrimSpace(rootID) == "" {
		return taskWorkspaceRoot{}, errWorkspaceRootNotFound
	}
	roots, err := h.loadTaskWorkspaceRoots(ctx, taskUUID)
	if err != nil {
		return taskWorkspaceRoot{}, err
	}
	for _, root := range roots {
		if root.ID != rootID {
			continue
		}
		if !root.Available {
			return taskWorkspaceRoot{}, errWorkspaceUnavailable
		}
		return root, nil
	}
	return taskWorkspaceRoot{}, errWorkspaceRootNotFound
}

func (h *TasksHandler) writeWorkspaceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		i18n.Error(c, http.StatusNotFound, "localserver_task_not_found", "task_not_found")
	case errors.Is(err, errWorkspaceRootNotFound):
		i18n.LocalServerKeyError(c, http.StatusNotFound, "localserver_not_found", "workspace_root_not_found")
	case errors.Is(err, errWorkspaceUnavailable), errors.Is(err, os.ErrPermission):
		i18n.Error(c, http.StatusBadRequest, "localserver_workspace_unavailable", "workspace_unavailable")
	case errors.Is(err, errWorkspacePathOutside):
		i18n.Error(c, http.StatusBadRequest, "localserver_file_path_outside", "file_path_outside")
	case errors.Is(err, errWorkspacePathSymlink):
		i18n.Error(c, http.StatusBadRequest, "localserver_file_path_symlink", "file_path_symlink")
	case errors.Is(err, errWorkspacePathInvalid):
		i18n.Error(c, http.StatusBadRequest, "localserver_file_path_invalid", "file_path_invalid")
	case errors.Is(err, errWorkspaceFileTooLarge):
		i18n.LocalServerKeyError(c, http.StatusRequestEntityTooLarge, "localserver_bad_request", "workspace_file_too_large")
	case errors.Is(err, os.ErrNotExist):
		i18n.Error(c, http.StatusNotFound, "localserver_file_not_found", "file_not_found")
	default:
		i18n.LocalServerError(c, http.StatusInternalServerError, err)
	}
}

func openVerifiedWorkspacePath(root, relativePath string, requireDirectory bool) (*os.File, os.FileInfo, string, error) {
	target, err := safeWorkspacePath(root, relativePath)
	if err != nil {
		return nil, nil, "", err
	}
	file, err := os.Open(target)
	if err != nil {
		return nil, nil, "", err
	}
	closeWithError := func(err error) (*os.File, os.FileInfo, string, error) {
		_ = file.Close()
		return nil, nil, "", err
	}
	openedInfo, err := file.Stat()
	if err != nil {
		return closeWithError(err)
	}
	currentInfo, err := os.Stat(target)
	if err != nil {
		return closeWithError(err)
	}
	if !os.SameFile(openedInfo, currentInfo) {
		return closeWithError(errWorkspacePathSymlink)
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return closeWithError(err)
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil || !pathWithinDirectory(absoluteRoot, resolvedTarget) {
		return closeWithError(errWorkspacePathOutside)
	}
	if requireDirectory && !openedInfo.IsDir() {
		return closeWithError(errWorkspacePathInvalid)
	}
	if !requireDirectory && !openedInfo.Mode().IsRegular() {
		return closeWithError(errWorkspacePathInvalid)
	}
	return file, openedInfo, target, nil
}

func safeWorkspacePath(root, relativePath string) (string, error) {
	absoluteRoot, err := filepath.Abs(strings.TrimSpace(root))
	if err != nil || strings.TrimSpace(root) == "" {
		return "", errWorkspacePathInvalid
	}
	rootInfo, err := os.Lstat(absoluteRoot)
	if err != nil {
		return "", err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 {
		return "", errWorkspacePathSymlink
	}
	normalized := strings.ReplaceAll(strings.TrimSpace(relativePath), "/", string(filepath.Separator))
	cleanPath := filepath.Clean(normalized)
	if cleanPath == "." && normalized == "" {
		cleanPath = "."
	}
	if filepath.IsAbs(cleanPath) || cleanPath == ".." || strings.HasPrefix(cleanPath, ".."+string(filepath.Separator)) {
		return "", errWorkspacePathOutside
	}
	target := filepath.Join(absoluteRoot, cleanPath)
	if !pathWithinDirectory(absoluteRoot, target) {
		return "", errWorkspacePathOutside
	}
	containsSymlink, err := pathContainsSymlink(absoluteRoot, target)
	if err != nil {
		return "", err
	}
	if containsSymlink {
		return "", errWorkspacePathSymlink
	}
	return target, nil
}

func workspaceEntryFromDirEntry(parent string, entry os.DirEntry, ignored bool) (taskWorkspaceEntry, bool) {
	if entry.Type()&os.ModeSymlink != 0 {
		return taskWorkspaceEntry{}, false
	}
	info, err := entry.Info()
	if err != nil {
		return taskWorkspaceEntry{}, false
	}
	relativePath := filepath.Join(parent, entry.Name())
	if parent == "." {
		relativePath = entry.Name()
	}
	return taskWorkspaceEntry{
		Name:       entry.Name(),
		Path:       filepath.ToSlash(relativePath),
		IsDir:      entry.IsDir(),
		FileType:   strings.ToLower(strings.TrimPrefix(filepath.Ext(entry.Name()), ".")),
		Size:       info.Size(),
		ModifiedAt: info.ModTime().UnixMilli(),
		Hidden:     strings.HasPrefix(entry.Name(), "."),
		Ignored:    ignored,
	}, true
}

func sortWorkspaceEntries(entries []taskWorkspaceEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		left := strings.ToLower(entries[i].Name)
		right := strings.ToLower(entries[j].Name)
		if left == right {
			return entries[i].Name < entries[j].Name
		}
		return left < right
	})
}

func workspaceIgnoredName(name string, isDir bool) bool {
	if !isDir {
		return false
	}
	switch strings.ToLower(name) {
	case ".git", "node_modules", "vendor", "dist", "build", ".cache", ".next", ".nuxt", ".turbo", "coverage", "target":
		return true
	default:
		return false
	}
}

func workspaceRootID(taskUUID, path string) string {
	sum := sha256.Sum256([]byte(taskUUID + "\x00" + workspacePathKey(path)))
	return fmt.Sprintf("root-%x", sum[:8])
}

func workspacePathKey(path string) string {
	clean := filepath.Clean(path)
	if runtime.GOOS == "windows" {
		return strings.ToLower(clean)
	}
	return clean
}

func workspaceRootAvailability(path string) (bool, string) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, "workspace_root_missing"
		}
		if errors.Is(err, os.ErrPermission) {
			return false, "workspace_root_permission_denied"
		}
		return false, "workspace_root_unavailable"
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, "workspace_root_symlink"
	}
	if !info.IsDir() {
		return false, "workspace_root_not_directory"
	}
	return true, ""
}

func workspaceRootName(path string) string {
	clean := filepath.Clean(path)
	name := filepath.Base(clean)
	if name == "." || name == string(filepath.Separator) || name == "" {
		return clean
	}
	return name
}

func readWorkspaceFile(file *os.File, limit int64) ([]byte, error) {
	content, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > limit {
		return nil, errWorkspaceFileTooLarge
	}
	return content, nil
}

func workspaceTextFile(path string, content []byte) bool {
	if !utf8.Valid(content) {
		return false
	}
	for _, b := range content {
		if b == 0 {
			return false
		}
	}
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	if isTextFileType(ext) {
		return true
	}
	switch strings.ToLower(filepath.Base(path)) {
	case "dockerfile", "makefile", "license", "readme", "notice":
		return true
	}
	return true
}
