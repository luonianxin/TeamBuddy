// Package workspace 管理工作空间（数据根）的解析与重定位。
//
// 数据根默认是 <HOME>/.teambuddy。启动时如果只有旧的 <HOME>/.goteams 或 .goteams.mlink，
// 会把 HOME 下旧数据根整树复制到新目录并切换；旧目录和旧指针都原样保留。
// 外部自定义目录保持原位置与原名，新指针指向同一位置；旧根内部的指针目标前缀映射到新根，
// 内层目录名不变。复制或校验失败时启动失败，不创建空库、不回退旧数据。
// 桌面端用户在配置中心选择新的父目录后，搬迁流程把整个数据根
// 搬到 <新父目录>/<当前目录名>，并在 HOME 下长期保留对应指针文件；之后每次启动由
// ResolveDataRoot 解析指针回到真实数据根。因为整个数据根（含数据库）一起搬迁，数据库里的
// 绝对路径在新位置保持成立；Windows 换盘符场景在启动时按卷序列号把指针与历史路径
// 归一到真实盘符。
package workspace

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"goteams-client/internal/applog"
)

const (
	// 旧安装使用这一套名字。新方案生效前继续读取它。
	legacyDataRootDirName      = ".goteams"
	legacyDataRootLinkFileName = ".goteams.mlink"
	legacyDataRootSentinelName = ".goteams-root"

	// 新安装和已完成复制切换的安装使用这一套名字。
	currentDataRootDirName      = ".teambuddy"
	currentDataRootLinkFileName = ".teambuddy.mlink"
	currentDataRootSentinelName = ".teambuddy-root"
)

// rootScheme 是一套数据根目录名、指针文件名和结构标记名。
type rootScheme struct {
	dirName      string
	linkFileName string
	sentinelName string
}

func legacyRootScheme() rootScheme {
	return rootScheme{
		dirName:      legacyDataRootDirName,
		linkFileName: legacyDataRootLinkFileName,
		sentinelName: legacyDataRootSentinelName,
	}
}

func currentRootScheme() rootScheme {
	return rootScheme{
		dirName:      currentDataRootDirName,
		linkFileName: currentDataRootLinkFileName,
		sentinelName: currentDataRootSentinelName,
	}
}

// activeScheme 选择当前用户的数据根方案。新指针、新库或新结构标记存在即使用 .teambuddy；
// runtime.json 不参与判定。新旧数据同时存在时新数据优先。
// 同一 HOME 在本次进程内只判断一次；复制切换成功后必须 invalidate，避免继续使用旧方案。
func activeScheme() rootScheme {
	home := realHomeDir()
	schemeMu.Lock()
	defer schemeMu.Unlock()
	if schemeReady && schemeHome == home {
		return schemeValue
	}
	schemeHome = home
	schemeValue = detectScheme(home)
	schemeReady = true
	return schemeValue
}

func detectScheme(home string) rootScheme {
	if currentSchemeReady(home) {
		return currentRootScheme()
	}
	oldRoot := filepath.Join(home, legacyDataRootDirName)
	oldLink := filepath.Join(home, legacyDataRootLinkFileName)
	if pathExists(oldLink) || pathExists(oldRoot) {
		return legacyRootScheme()
	}
	return currentRootScheme()
}

func directoryHasManagedData(root string) bool {
	for _, name := range []string{"base.db", "goteams.db"} {
		if pathExists(filepath.Join(root, "data", name)) {
			return true
		}
	}
	return false
}

var (
	schemeMu    sync.Mutex
	schemeHome  string
	schemeValue rootScheme
	schemeReady bool
)

func pathExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func dataRootDirName() string { return activeScheme().dirName }

func dataRootLinkFileName() string { return activeScheme().linkFileName }

// DataRootDirName 返回当前用户生效的数据根目录名。
func DataRootDirName() string { return dataRootDirName() }

func sentinelNameForRoot(root string) string {
	base := filepath.Base(filepath.Clean(root))
	if strings.EqualFold(base, legacyDataRootDirName) {
		return legacyDataRootSentinelName
	}
	if strings.EqualFold(base, currentDataRootDirName) {
		return currentDataRootSentinelName
	}
	return activeScheme().sentinelName
}

// ErrWorkspaceConflict 表示目标父目录下已存在数据根目录（禁止合并）。
var ErrWorkspaceConflict = errors.New("目标父目录下已存在数据目录")

// DefaultDataRoot 返回当前用户的默认数据根路径。
// 新安装和复制切换完成后是 HOME/.teambuddy；切换前仍为 HOME/.goteams。
func DefaultDataRoot() string {
	return filepath.Join(realHomeDir(), dataRootDirName())
}

// LinkFilePath 返回 HOME 下当前方案指针文件的路径（错误提示与排障时使用）。
func LinkFilePath() string {
	return filepath.Join(realHomeDir(), dataRootLinkFileName())
}

func readLinkTarget(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	target := filepath.Clean(strings.TrimSpace(string(data)))
	if target == "" || target == "." {
		return "", false
	}
	return target, true
}

// ResolveDataRoot 在启动早期解析数据根：
//  1. 先完成旧方案复制切换；失败则返回错误，不创建空库、不使用旧数据。
//  2. HOME 下无当前方案指针文件 → 使用并确保默认根存在。
//  3. 有指针文件 → 必须是绝对路径、真实目录且不是符号链接；Windows 下先按卷序列号
//     归一盘符。目标不可用时返回明确错误，不回退默认根或旧数据。
//     指针可以指向默认根本身（绝对路径且结构/数据校验通过）。
func ResolveDataRoot() (string, error) {
	home := realHomeDir()
	if err := ensureMigrated(home); err != nil {
		return "", err
	}
	scheme := activeScheme()
	defaultRoot := filepath.Join(home, scheme.dirName)
	linkPath := filepath.Join(home, scheme.linkFileName)

	data, err := os.ReadFile(linkPath)
	if errors.Is(err, os.ErrNotExist) {
		return defaultRoot, os.MkdirAll(defaultRoot, 0o755)
	}
	if err != nil {
		return "", fmt.Errorf("读取工作空间指针文件失败: %w", err)
	}

	target := strings.TrimSpace(string(data))
	if target == "" {
		return "", fmt.Errorf("工作空间指针文件为空，拒绝创建空数据根（指针: %s）", linkPath)
	}
	if !filepath.IsAbs(target) {
		return "", fmt.Errorf("工作空间指针不是绝对路径: %q（指针: %s）", target, linkPath)
	}
	target = filepath.Clean(target)

	if runtime.GOOS == "windows" {
		if canonical, changed := canonicalizeWindowsTarget(target); changed {
			if pointerTargetUsable(canonical) {
				if writeErr := os.WriteFile(linkPath, []byte(canonical), 0o600); writeErr != nil {
					applog.Warn("重写工作空间指针失败", "link", linkPath, "error", writeErr.Error())
				} else {
					applog.Info("检测到系统盘符变化，已校正工作空间指针", "from", target, "to", canonical)
				}
				target = canonical
			} else {
				applog.Warn("盘符变化后的工作空间目录不完整，继续使用旧指针路径",
					"from", target, "to", canonical)
			}
		}
	}

	if err := validatePointerTarget(home, defaultRoot, target); err != nil {
		return "", fmt.Errorf("工作空间目录不可用：%w（不会改用其他数据目录；请检查磁盘连接后重启，或修复 %s）", err, linkPath)
	}

	return target, nil
}

// validatePointerTarget 校验指针目标目录：
//   - 目标必须是绝对路径下的真实目录，不能是符号链接；
//   - Windows 下目标卷必须能读到（盘已拔出则失败）；换盘符仍由 canonicalizeWindowsTarget 按卷序列号归一。
//     跨卷搬迁是允许的，不能因为和 HOME 不同卷就把已存在的数据根判丢。
//   - 目标必须位于某个用户目录之下（防止指向系统/程序目录）；
//   - 目标必须带结构标记或受管数据库；可以等于默认根。
func validatePointerTarget(_, defaultRoot, target string) error {
	if samePath(target, filepath.Dir(defaultRoot)) {
		return fmt.Errorf("目标 %s 不能是用户主目录本身", target)
	}
	info, err := os.Lstat(target)
	if err != nil {
		return fmt.Errorf("目标目录不可读: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("目标 %s 是符号链接，拒绝跟随", target)
	}
	if !info.IsDir() {
		return fmt.Errorf("目标 %s 不是目录", target)
	}
	if runtime.GOOS == "windows" {
		if _, err := volumeSerialNumber(target); err != nil {
			return fmt.Errorf("目标目录所在卷不可用: %w", err)
		}
		if !isUnderUserHome(target) {
			return fmt.Errorf("目标目录 %s 不在任何用户目录下", target)
		}
	}
	if !pointerTargetUsable(target) {
		return fmt.Errorf("目标目录 %s 缺少完整的工作空间结构或数据", target)
	}
	return nil
}

func pointerTargetUsable(dir string) bool {
	return dirLooksComplete(dir) || directoryHasManagedData(dir)
}

// dirLooksComplete 判断目录是否是已建立结构的数据根（存在结构标记文件）。
func dirLooksComplete(dir string) bool {
	for _, name := range []string{legacyDataRootSentinelName, currentDataRootSentinelName} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err == nil && info.Mode().IsRegular() {
			return true
		}
	}
	return false
}

// MarkDataRootStructure 建立数据根结构标记。首次初始化和搬迁后都会调用。
func MarkDataRootStructure(root string) error {
	marker := filepath.Join(root, sentinelNameForRoot(root))
	content := "goteams data root\n" + time.Now().Format(time.RFC3339) + "\n"
	if err := os.WriteFile(marker, []byte(content), 0o600); err != nil {
		return fmt.Errorf("写入工作空间结构标记失败: %w", err)
	}
	return nil
}

// samePath 宽松比较两个路径（清洗 + Windows 忽略大小写）。
func samePath(a, b string) bool {
	a = filepath.Clean(a)
	b = filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// isUnderUserHome 判断路径是否位于某用户目录之内（Windows: <卷>:\Users\<name>\...）。
// 用于阻止把工作空间指到系统盘根目录或 Program Files 等位置。
func isUnderUserHome(p string) bool {
	if runtime.GOOS == "windows" {
		p = filepath.Clean(p)
	}
	return windowsPathUnderUserHome(p)
}

// windowsPathUnderUserHome 识别 Windows 用户目录。filepath.Clean 在 Windows 上得到
// ["C:", "USERS", "<name>", ...]，不能按带前导空段的 Unix 形态判断，否则已搬迁的数据根
// 会被拒绝，启动回退到空壳目录。
func windowsPathUnderUserHome(p string) bool {
	p = strings.ReplaceAll(strings.TrimSpace(p), "/", `\`)
	for strings.Contains(p, `\\`) {
		p = strings.ReplaceAll(p, `\\`, `\`)
	}
	p = strings.TrimSuffix(p, `\`)
	parts := strings.Split(strings.ToUpper(p), `\`)
	if len(parts) > 0 && parts[0] == "" {
		parts = parts[1:]
	}
	return len(parts) >= 4 && strings.HasSuffix(parts[0], ":") && parts[1] == "USERS" && parts[2] != ""
}

// copyDirTree 递归复制目录；遇到符号链接按链接本身复制（不跟随），
// 避免重定位时把链接指向的外部内容复制进数据根。
func copyDirTree(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(target, dst)
	}
	if !info.IsDir() {
		return copyFile(src, dst, info.Mode())
	}
	if err := os.MkdirAll(dst, info.Mode().Perm()); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := copyDirTree(filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

// copyFile 复制单个文件内容并保留权限位。
func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode.Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// countEntries 统计目录内条目数（含隐藏文件），用于空目录校验。
func countEntries(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	return len(entries), nil
}

// pathDepth 返回清洗后路径的层级深度，用于禁止磁盘根目录的校验。
func pathDepth(p string) int {
	p = strings.TrimSuffix(filepath.Clean(p), string(filepath.Separator))
	return strings.Count(p, string(filepath.Separator))
}

// relocateDataRoot 把源数据根搬迁到 dstParent/<当前数据根目录名>。
// 调用方需已完成所有前置校验；函数保证：
//  1. 同卷优先 rename（原子），跨卷复制后删除；
//  2. 任何中途失败都尽量回滚，源目录保持可用。
//
// 注意：搬迁成功后调用方必须立刻写指针文件；两条语句之间进程被杀的极端窗口
// 由启动日志与指针校验兜底（数据完整保留在新目录，可手工恢复指针）。
func relocateDataRoot(srcRoot, dstParent string) (newRoot string, err error) {
	newRoot = filepath.Join(dstParent, dataRootDirName())

	moved := false
	defer func() {
		if err != nil {
			// 回滚：目标已部分建立则搬回/删除；源目录已不存在则从目标搬回。
			if _, statErr := os.Stat(srcRoot); errors.Is(statErr, os.ErrNotExist) {
				if _, tgtErr := os.Stat(newRoot); tgtErr == nil {
					if rbErr := os.Rename(newRoot, srcRoot); rbErr != nil {
						applog.Error("搬迁失败且自动回滚失败，请手工把目录移回原位",
							"from", newRoot, "to", srcRoot, "error", rbErr.Error())
					}
				}
			} else if _, tgtErr := os.Stat(newRoot); tgtErr == nil && !moved {
				// 源仍在、目标是复制残留：清理目标避免下次被判为非空冲突。
				if rmErr := os.RemoveAll(newRoot); rmErr != nil {
					applog.Error("清理搬迁残留目标目录失败", "target", newRoot, "error", rmErr.Error())
				}
			}
		}
	}()

	if sameVol(srcRoot, dstParent) {
		if err = os.Rename(srcRoot, newRoot); err != nil {
			err = fmt.Errorf("移动工作空间目录失败: %w", err)
			return "", err
		}
		moved = true
	} else {
		if err = copyDirTree(srcRoot, newRoot); err != nil {
			err = fmt.Errorf("复制工作空间目录失败: %w", err)
			return "", err
		}
		moved = true
		if err = os.RemoveAll(srcRoot); err != nil {
			// 复制完成但源删除失败：目标已完整，保留目标，源残留由用户手工清理。
			applog.Error("工作空间已复制到新目录，但删除旧目录失败，请手工清理",
				"old", srcRoot, "error", err.Error())
			err = nil
		}
	}
	return newRoot, nil
}
