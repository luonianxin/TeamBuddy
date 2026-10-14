package workspace

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	_ "modernc.org/sqlite"

	"goteams-client/internal/applog"
)

const (
	migrationStagePrefix  = ".teambuddy-copy-"
	migrateJournalName    = ".teambuddy.migrate.json"
	migrateLockName       = ".teambuddy.migrate.lock"
	stageManifestName     = ".teambuddy-copy-owned"
	migrateJournalVersion = 2
	phaseStaged           = "staged"
	phasePublished        = "published"
	sqliteHeader          = "SQLite format 3\x00"
)

var errEmptyDatabase = errors.New("empty database")

type copyJournal struct {
	Version    int    `json:"version"`
	Phase      string `json:"phase"`
	Token      string `json:"token"`
	Stage      string `json:"stage,omitempty"`
	OldRoot    string `json:"old_root"`
	NewRoot    string `json:"new_root"`
	LinkTarget string `json:"link_target,omitempty"`
}

var (
	migrateMu   sync.Mutex
	migrateHome string
	migrateDone bool
	migrateErr  error
)

// ensureMigrated copies HOME/.goteams to HOME/.teambuddy when the current scheme
// is not yet active. It returns a non-nil error on copy, validation, recovery,
// or pointer-commit failure. Callers MUST fail startup on error: do not swallow
// it, do not create an empty current root, and do not fall back to the old root.
// Success invalidates the scheme cache. A finished attempt (success or failure)
// is remembered for this process and home; later calls return the same result
// without retrying.
func ensureMigrated(home string) error {
	migrateMu.Lock()
	defer migrateMu.Unlock()
	if migrateDone && migrateHome == home {
		return migrateErr
	}
	err := ensureMigratedLocked(home)
	migrateHome = home
	migrateDone = true
	migrateErr = err
	if err == nil {
		invalidateScheme(home)
	}
	return err
}

func ensureMigratedLocked(home string) error {
	if currentSchemeReady(home) && !journalPresent(home) {
		return nil
	}
	releaseLock, err := acquireMigrationLock(home)
	if err != nil {
		return err
	}
	defer releaseLock()

	if err := recoverMigration(home); err != nil {
		return fmt.Errorf("恢复未完成的数据目录复制失败: %w", err)
	}
	if currentSchemeReady(home) {
		return nil
	}
	if err := migrateLegacyDataRoot(home); err != nil {
		return fmt.Errorf("旧数据目录复制失败: %w", err)
	}
	return nil
}

func invalidateScheme(home string) {
	schemeMu.Lock()
	defer schemeMu.Unlock()
	if schemeReady && schemeHome == home {
		schemeReady = false
	}
}

func migrateLegacyDataRoot(home string) error {
	oldRoot := filepath.Join(home, legacyDataRootDirName)
	oldLink := filepath.Join(home, legacyDataRootLinkFileName)
	newRoot := filepath.Join(home, currentDataRootDirName)
	newLink := filepath.Join(home, currentDataRootLinkFileName)

	if !pathExists(oldLink) && !pathExists(oldRoot) {
		return nil
	}

	live, fromLink, err := inspectLegacySource(home, oldLink, oldRoot)
	if err != nil {
		return err
	}
	if live == "" {
		return nil
	}

	dest, needsLink := mappedDestination(oldRoot, newRoot, live, fromLink)
	releaseInst, err := holdLiveInstanceLock(live)
	if err != nil {
		return err
	}
	defer releaseInst()

	if !pathExists(oldRoot) {
		if !needsLink {
			return nil
		}
		if err := atomicWriteFile(newLink, []byte(dest)); err != nil {
			return fmt.Errorf("写入新工作空间指针失败: %w", err)
		}
		applog.Info("已保留外部工作空间并写入新指针，外部目录未改动", "target", dest)
		return nil
	}
	if isSymlink(oldRoot) {
		return fmt.Errorf("旧数据根是符号链接，拒绝复制: %s", oldRoot)
	}

	token, err := newMigrationToken()
	if err != nil {
		return err
	}
	stage, err := os.MkdirTemp(home, migrationStagePrefix)
	if err != nil {
		return err
	}
	if err := writeStageManifest(stage, token); err != nil {
		_ = os.RemoveAll(stage)
		return err
	}
	journal := copyJournal{
		Version: migrateJournalVersion,
		Phase:   phaseStaged,
		Token:   token,
		Stage:   stage,
		OldRoot: oldRoot,
		NewRoot: newRoot,
	}
	if needsLink {
		journal.LinkTarget = dest
	}
	if err := writeJournal(home, journal); err != nil {
		_ = os.RemoveAll(stage)
		return err
	}

	published := false
	defer func() {
		if published {
			return
		}
		if samePath(stage, oldRoot) || samePath(stage, live) {
			return
		}
		if stageOwned(stage, token) {
			_ = os.RemoveAll(stage)
			_ = removeJournal(home)
		}
	}()

	if err := copyDirTree(oldRoot, stage); err != nil {
		return err
	}
	if err := writeStageManifest(stage, token); err != nil {
		return err
	}
	if err := rewriteManagedPaths(stage, oldRoot, newRoot); err != nil {
		return err
	}
	if err := validateCopiedDatabases(stage); err != nil {
		return err
	}
	if err := prepareDestination(newRoot); err != nil {
		return err
	}
	if err := os.Rename(stage, newRoot); err != nil {
		return fmt.Errorf("发布复制结果失败: %w", err)
	}
	published = true
	journal.Phase = phasePublished
	if err := writeJournal(home, journal); err != nil {
		return fmt.Errorf("已复制到 %s 但无法更新迁移日志（旧目录未改动）: %w", newRoot, err)
	}
	if needsLink {
		if err := atomicWriteFile(newLink, []byte(dest)); err != nil {
			return fmt.Errorf("新数据根已就位，但写入新指针失败（旧目录与外部目录未删除）: %w", err)
		}
	}
	if err := os.Remove(journalPath(home)); err != nil && !os.IsNotExist(err) {
		return err
	}
	_ = os.Remove(filepath.Join(newRoot, stageManifestName))
	applog.Info("已复制旧数据目录并切换到新数据根，旧目录保持不变", "from", oldRoot, "to", newRoot, "live", dest)
	return nil
}

func inspectLegacySource(home, oldLink, oldRoot string) (string, bool, error) {
	if pathExists(oldLink) {
		target, err := parseLegacyLink(oldLink)
		if err != nil {
			return "", true, err
		}
		if err := rejectUnsafeTarget(home, oldRoot, target); err != nil {
			return "", true, err
		}
		return target, true, nil
	}
	if !pathExists(oldRoot) {
		return "", false, nil
	}
	if isSymlink(oldRoot) {
		return "", false, fmt.Errorf("旧数据根是符号链接，拒绝复制: %s", oldRoot)
	}
	info, err := os.Lstat(oldRoot)
	if err != nil {
		return "", false, err
	}
	if !info.IsDir() {
		return "", false, fmt.Errorf("旧数据根不是目录: %s", oldRoot)
	}
	return oldRoot, false, nil
}

func parseLegacyLink(oldLink string) (string, error) {
	data, err := os.ReadFile(oldLink)
	if err != nil {
		return "", fmt.Errorf("读取旧工作空间指针失败: %w", err)
	}
	target := strings.TrimSpace(string(data))
	if target == "" {
		return "", fmt.Errorf("旧工作空间指针为空，拒绝改用外层旧目录（指针: %s）", oldLink)
	}
	if !filepath.IsAbs(target) {
		return "", fmt.Errorf("旧工作空间指针不是绝对路径: %q（指针: %s）", target, oldLink)
	}
	target = filepath.Clean(target)
	info, err := os.Lstat(target)
	if os.IsNotExist(err) {
		return "", fmt.Errorf("旧工作空间指针目标不存在，拒绝改用外层旧目录: %s", target)
	}
	if err != nil {
		return "", fmt.Errorf("旧工作空间指针目标不可用: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("旧工作空间指针目标是符号链接，拒绝复制: %s", target)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("旧工作空间指针目标不是目录: %s", target)
	}
	return target, nil
}

func rejectUnsafeTarget(home, oldRoot, target string) error {
	if samePath(target, home) || isPathWithin(home, target) {
		return fmt.Errorf("旧工作空间指针指向主目录或其祖先，拒绝复制: %s", target)
	}
	if pathExists(oldRoot) && isPathWithin(oldRoot, target) {
		return fmt.Errorf("旧工作空间指针指向旧数据根的祖先，拒绝复制: %s", target)
	}
	return nil
}

func mappedDestination(oldRoot, newRoot, live string, fromLink bool) (string, bool) {
	if !fromLink || samePath(live, oldRoot) {
		return newRoot, false
	}
	if isPathWithin(live, oldRoot) {
		relative, err := filepath.Rel(oldRoot, live)
		if err != nil {
			return live, true
		}
		return filepath.Join(newRoot, relative), true
	}
	return live, true
}

func currentSchemeReady(home string) bool {
	newRoot := filepath.Join(home, currentDataRootDirName)
	return pathExists(filepath.Join(home, currentDataRootLinkFileName)) ||
		directoryHasManagedData(newRoot) ||
		pathExists(filepath.Join(newRoot, currentDataRootSentinelName))
}

func recoverMigration(home string) error {
	path := journalPath(home)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var probe struct {
		Version int    `json:"version"`
		Phase   string `json:"phase"`
		Src     string `json:"src"`
		Dst     string `json:"dst"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return fmt.Errorf("迁移日志损坏，已停止以免误删数据: %w", err)
	}
	if probe.Version != migrateJournalVersion {
		return fmt.Errorf("发现旧版改名迁移日志（%s），已停止自动处理以免误删数据。请检查目录后删除该文件再启动", path)
	}
	var journal copyJournal
	if err := json.Unmarshal(data, &journal); err != nil {
		return fmt.Errorf("迁移日志损坏，已停止以免误删数据: %w", err)
	}
	if !samePath(journal.OldRoot, filepath.Join(home, legacyDataRootDirName)) ||
		!samePath(journal.NewRoot, filepath.Join(home, currentDataRootDirName)) ||
		!samePath(filepath.Dir(journal.Stage), home) ||
		!strings.HasPrefix(filepath.Base(journal.Stage), migrationStagePrefix) || journal.Token == "" {
		return fmt.Errorf("迁移日志路径或所有权标记无效，拒绝自动恢复")
	}
	if journal.LinkTarget != "" && !filepath.IsAbs(journal.LinkTarget) {
		return fmt.Errorf("迁移日志目标不是绝对路径，拒绝自动恢复")
	}
	switch journal.Phase {
	case phaseStaged:
		return recoverStaged(home, journal)
	case phasePublished:
		return finishPublished(home, journal)
	default:
		return fmt.Errorf("迁移日志阶段无法识别: %q", journal.Phase)
	}
}

func recoverStaged(home string, journal copyJournal) error {
	stage := filepath.Clean(journal.Stage)
	if stage == "" {
		return fmt.Errorf("迁移日志缺少暂存路径，已停止以免误删数据")
	}
	if samePath(stage, journal.OldRoot) || samePath(stage, journal.NewRoot) {
		return fmt.Errorf("迁移日志暂存路径指向数据根，已停止以免误删: %s", stage)
	}
	if stageOwned(stage, journal.Token) {
		if err := os.RemoveAll(stage); err != nil {
			return err
		}
		return removeJournal(home)
	}
	if !pathExists(stage) && pathExists(journal.NewRoot) {
		return finishPublished(home, journal)
	}
	if !pathExists(stage) {
		return removeJournal(home)
	}
	return fmt.Errorf("迁移暂存目录无法验证所有权，已停止以免误删: %s", stage)
}

func finishPublished(home string, journal copyJournal) error {
	if journal.NewRoot == "" || !pathExists(journal.NewRoot) {
		return fmt.Errorf("复制已记录为发布但新数据根不存在: %s", journal.NewRoot)
	}
	if isSymlink(journal.NewRoot) {
		return fmt.Errorf("复制目标已被替换为符号链接，拒绝自动恢复")
	}
	if journal.LinkTarget != "" {
		if err := atomicWriteFile(filepath.Join(home, currentDataRootLinkFileName), []byte(journal.LinkTarget)); err != nil {
			return fmt.Errorf("恢复新工作空间指针失败（旧目录与外部目录未删除）: %w", err)
		}
	}
	_ = os.Remove(filepath.Join(journal.NewRoot, stageManifestName))
	return removeJournal(home)
}

func prepareDestination(newRoot string) error {
	if !pathExists(newRoot) {
		return nil
	}
	if isSymlink(newRoot) {
		return fmt.Errorf("目标数据目录是符号链接: %s", newRoot)
	}
	if directoryHasManagedData(newRoot) ||
		pathExists(filepath.Join(newRoot, currentDataRootSentinelName)) ||
		pathExists(filepath.Join(newRoot, legacyDataRootSentinelName)) {
		return fmt.Errorf("目标数据目录已存在: %s", newRoot)
	}
	entries, err := os.ReadDir(newRoot)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return os.Remove(newRoot)
	}
	if !isSafeRuntimeShell(newRoot) {
		return fmt.Errorf("目标目录已存在且不是可接续的空壳: %s", newRoot)
	}
	if err := removeKnownRuntimeShell(newRoot); err != nil {
		return err
	}
	if err := os.Remove(newRoot); err != nil {
		return fmt.Errorf("无法清空可接续的运行时空壳: %w", err)
	}
	return nil
}

func isSafeRuntimeShell(dir string) bool {
	if directoryHasManagedData(dir) {
		return false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
			return false
		}
		switch entry.Name() {
		case "runtime", "logs":
			if !runtimeShellSubdirOK(filepath.Join(dir, entry.Name()), entry.Name() == "runtime") {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func runtimeShellSubdirOK(dir string, allowRuntimeJSON bool) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return false
		}
		path := filepath.Join(dir, entry.Name())
		if entry.IsDir() {
			n, err := countEntries(path)
			if err != nil || n > 0 {
				return false
			}
			continue
		}
		if allowRuntimeJSON && entry.Name() == "runtime.json" {
			continue
		}
		return false
	}
	return true
}

func removeKnownRuntimeShell(dir string) error {
	runtimeJSON := filepath.Join(dir, "runtime", "runtime.json")
	if pathExists(runtimeJSON) {
		if err := os.Remove(runtimeJSON); err != nil {
			return err
		}
	}
	for _, name := range []string{
		filepath.Join(dir, "runtime"),
		filepath.Join(dir, "logs"),
	} {
		if !pathExists(name) {
			continue
		}
		if err := removeEmptyDirTree(name); err != nil {
			return err
		}
	}
	return nil
}

func removeEmptyDirTree(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			return fmt.Errorf("运行时空壳含有未预期文件: %s", filepath.Join(dir, entry.Name()))
		}
		if err := removeEmptyDirTree(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	return os.Remove(dir)
}

func validateCopiedDatabases(root string) error {
	return walkManagedFiles(root, func(path, kind string) error {
		if kind != "db" {
			return nil
		}
		if err := validateSQLiteFile(path); err != nil {
			if errors.Is(err, errEmptyDatabase) {
				return nil
			}
			return err
		}
		db, err := sql.Open("sqlite", path+"?mode=ro")
		if err != nil {
			return err
		}
		var result string
		err = db.QueryRow(`PRAGMA integrity_check`).Scan(&result)
		db.Close()
		if err != nil {
			return fmt.Errorf("校验数据库失败: %w", err)
		}
		if result != "ok" {
			return fmt.Errorf("数据库完整性检查失败: %s", path)
		}
		return nil
	})
}

func rewriteManagedPaths(root, oldPrefix, newPrefix string) error {
	if samePath(oldPrefix, newPrefix) {
		return nil
	}
	return walkManagedFiles(root, func(path, kind string) error {
		switch kind {
		case "db":
			return rewriteSQLitePaths(path, oldPrefix, newPrefix)
		case "json":
			return rewriteJSONFile(path, oldPrefix, newPrefix)
		default:
			return nil
		}
	})
}

func walkManagedFiles(root string, fn func(path, kind string) error) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			switch entry.Name() {
			case "task", "logs", "runtime", "session-artifacts":
				return filepath.SkipDir
			}
			return nil
		}
		name := strings.ToLower(entry.Name())
		parent := filepath.Base(filepath.Dir(path))
		switch {
		case name == stageManifestName:
			return nil
		case parent == "data" && strings.HasSuffix(name, ".db"):
			return fn(path, "db")
		case parent == "config" && strings.HasSuffix(name, ".json"):
			return fn(path, "json")
		}
		return nil
	})
}

func rewriteSQLitePaths(dbPath, oldPrefix, newPrefix string) error {
	if err := validateSQLiteFile(dbPath); err != nil {
		if errors.Is(err, errEmptyDatabase) {
			return nil
		}
		return err
	}
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return fmt.Errorf("合并 WAL 失败: %w", err)
	}
	tables, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return err
	}
	defer tables.Close()
	var names []string
	for tables.Next() {
		var name string
		if err := tables.Scan(&name); err != nil {
			return err
		}
		names = append(names, name)
	}
	if err := tables.Err(); err != nil {
		return err
	}
	for _, table := range names {
		if err := rewriteTablePaths(db, table, oldPrefix, newPrefix); err != nil {
			return err
		}
	}
	return nil
}

func rewriteTablePaths(db *sql.DB, table, oldPrefix, newPrefix string) error {
	if !safeSQLName(table) {
		return nil
	}
	rows, err := db.Query(`SELECT name, type FROM pragma_table_info(?)`, table)
	if err != nil {
		return err
	}
	defer rows.Close()
	type column struct{ name, kind string }
	var columns []column
	for rows.Next() {
		var item column
		if err := rows.Scan(&item.name, &item.kind); err != nil {
			return err
		}
		columns = append(columns, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, column := range columns {
		if !safeSQLName(column.name) || !isTextColumn(column.kind) {
			continue
		}
		selectQuery := fmt.Sprintf(`SELECT rowid, "%s" FROM "%s" WHERE "%s" IS NOT NULL`, column.name, table, column.name)
		values, err := db.Query(selectQuery)
		if err != nil {
			return err
		}
		type change struct {
			id    int64
			value string
		}
		var changes []change
		for values.Next() {
			var item change
			if err := values.Scan(&item.id, &item.value); err != nil {
				values.Close()
				return err
			}
			if updated, ok := replaceRootPrefix(item.value, oldPrefix, newPrefix); ok {
				item.value = updated
				changes = append(changes, item)
			}
		}
		err = values.Err()
		values.Close()
		if err != nil {
			return err
		}
		updateQuery := fmt.Sprintf(`UPDATE "%s" SET "%s" = ? WHERE rowid = ?`, table, column.name)
		for _, item := range changes {
			if _, err := db.Exec(updateQuery, item.value, item.id); err != nil {
				return err
			}
		}
	}
	return nil
}

func rewriteJSONFile(path, oldPrefix, newPrefix string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || info.Size() == 0 {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil
	}
	if !rewriteJSONValue(&value, oldPrefix, newPrefix) {
		return nil
	}
	out, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return atomicWriteFile(path, out)
}

func rewriteJSONValue(value *any, oldPrefix, newPrefix string) bool {
	switch typed := (*value).(type) {
	case string:
		if updated, ok := replaceRootPrefix(typed, oldPrefix, newPrefix); ok {
			*value = updated
			return true
		}
	case map[string]any:
		changed := false
		for key, item := range typed {
			next := item
			if rewriteJSONValue(&next, oldPrefix, newPrefix) {
				typed[key] = next
				changed = true
			}
		}
		return changed
	case []any:
		changed := false
		for i := range typed {
			next := typed[i]
			if rewriteJSONValue(&next, oldPrefix, newPrefix) {
				typed[i] = next
				changed = true
			}
		}
		return changed
	}
	return false
}

func validateSQLiteFile(dbPath string) error {
	info, err := os.Lstat(dbPath)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("数据库是符号链接，拒绝改写: %s", dbPath)
	}
	if info.Size() == 0 {
		return errEmptyDatabase
	}
	file, err := os.Open(dbPath)
	if err != nil {
		return err
	}
	header := make([]byte, 16)
	n, readErr := file.Read(header)
	file.Close()
	if readErr != nil && n == 0 {
		return readErr
	}
	if n < 16 || string(header[:n]) != sqliteHeader {
		return fmt.Errorf("数据库文件不是有效的 SQLite: %s", dbPath)
	}
	return nil
}

func isTextColumn(kind string) bool {
	kind = strings.ToUpper(kind)
	return kind == "" || strings.Contains(kind, "CHAR") || strings.Contains(kind, "CLOB") || strings.Contains(kind, "TEXT")
}

func replaceRootPrefix(value, oldPrefix, newPrefix string) (string, bool) {
	for _, old := range []string{filepath.Clean(oldPrefix), strings.ReplaceAll(filepath.Clean(oldPrefix), `\`, "/")} {
		if old == "." || old == "" {
			continue
		}
		next := newPrefix
		if strings.Contains(old, `\`) {
			next = strings.ReplaceAll(newPrefix, "/", `\`)
		} else if strings.Contains(old, "/") {
			next = strings.ReplaceAll(newPrefix, `\`, "/")
		}
		if value == old {
			return next, true
		}
		separator := "/"
		if strings.Contains(old, `\`) {
			separator = `\`
		}
		if strings.HasPrefix(value, old+separator) {
			return next + value[len(old):], true
		}
	}
	return value, false
}

func safeSQLName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return true
}

func isSymlink(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode()&os.ModeSymlink != 0
}

func newMigrationToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func journalPath(home string) string {
	return filepath.Join(home, migrateJournalName)
}

func journalPresent(home string) bool {
	return pathExists(journalPath(home))
}

func writeJournal(home string, journal copyJournal) error {
	data, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	return atomicWriteFile(journalPath(home), data)
}

func removeJournal(home string) error {
	if err := os.Remove(journalPath(home)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func writeStageManifest(stage, token string) error {
	return os.WriteFile(filepath.Join(stage, stageManifestName), []byte(token+"\n"), 0o600)
}

func stageOwned(stage, token string) bool {
	if stage == "" || token == "" || isSymlink(stage) || isSymlink(filepath.Join(stage, stageManifestName)) {
		return false
	}
	base := filepath.Base(stage)
	if !strings.HasPrefix(base, migrationStagePrefix) {
		return false
	}
	data, err := os.ReadFile(filepath.Join(stage, stageManifestName))
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(data)) == token
}

func atomicWriteFile(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
