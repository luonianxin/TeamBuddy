package artifactstore

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"goteams-client/internal/applog"
	"goteams-client/internal/redact"
	"goteams-client/internal/workspace"
)

// 留存文件路径只由 (session_uuid, sequence) 推导。
// 建成之后只允许写入一次（临时文件 rename）和删除，禁止移动或改名。
// 自动续跑必须沿用同一个事件序号，否则会覆盖这里的文件。

const (
	inlineMaxBytes   = 256 << 10
	inlineHeadBytes  = 64 << 10
	inlineTailBytes  = 64 << 10
	fileMaxBytes     = 64 << 20
	sessionMaxBytes  = 512 << 20
	totalMaxBytes    = 10 << 30
	artifactKeepDays = 30
)

var (
	uuidRe         = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	truncMarkerRe  = regexp.MustCompile(`\r?\n⟦gt-truncated v1 bytes=(\d+) omitted=(\d+) file=([01])( redacted=1)?( capped=1)?⟧\r?$`)
	errBadArtifact = errors.New("artifact key invalid")
	errMissing     = errors.New("artifact_missing")
)

// TruncMarker 是库里内容末尾的截断标记。
type TruncMarker struct {
	Bytes    int64
	Omitted  int64
	File     bool
	Redacted bool
	Capped   bool
}

// ParseTruncMarker 解析内容最后一行的截断标记。
func ParseTruncMarker(content string) (TruncMarker, bool) {
	match := truncMarkerRe.FindStringSubmatch(content)
	if match == nil {
		return TruncMarker{}, false
	}
	bytes, _ := strconv.ParseInt(match[1], 10, 64)
	omitted, _ := strconv.ParseInt(match[2], 10, 64)
	return TruncMarker{
		Bytes: bytes, Omitted: omitted, File: match[3] == "1",
		Redacted: match[4] != "", Capped: match[5] != "",
	}, true
}

// ArtifactPath 返回留存文件的唯一路径。
func ArtifactPath(root, sessionUUID string, seq int64) (string, error) {
	if !uuidRe.MatchString(sessionUUID) || seq <= 0 {
		return "", errBadArtifact
	}
	return filepath.Join(root, sessionUUID, fmt.Sprintf("%012d.txt", seq)), nil
}

// ArtifactStore 保存超出内联上限的事件全文。
type ArtifactStore struct {
	root    string
	mu      sync.Mutex
	session map[string]int64
	warned  map[string]bool
}

func NewArtifactStore(root string) *ArtifactStore {
	return &ArtifactStore{root: root, session: map[string]int64{}, warned: map[string]bool{}}
}

func Default() *ArtifactStore {
	root, err := workspace.ResolveDataRoot()
	if err != nil || root == "" {
		return nil
	}
	return NewArtifactStore(filepath.Join(root, "session-artifacts"))
}

// Put 在内容超过内联上限时写留存文件，并返回带标记的头尾文本。
func (s *ArtifactStore) Put(sessionUUID string, seq int64, content string) string {
	if s == nil || len(content) <= inlineMaxBytes {
		return content
	}
	full, redacted := redact.Redact(content)
	capped := false
	if len(full) > fileMaxBytes {
		full = trimUTF8(full, fileMaxBytes)
		capped = true
	}
	path, err := ArtifactPath(s.root, sessionUUID, seq)
	ok := false
	if err == nil && s.allow(sessionUUID, int64(len(full))) {
		ok = writeAtomic(path, full) == nil
		if !ok {
			s.mu.Lock()
			s.session[sessionUUID] -= int64(len(full))
			s.mu.Unlock()
		}
	}
	if !ok {
		applog.Warn("[Executor] artifact_write_failed", "session", sessionUUID, "sequence", seq)
	}
	return headTail(full, len(content), ok, redacted, capped)
}

func (s *ArtifactStore) allow(sessionUUID string, n int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session[sessionUUID]+n > sessionMaxBytes {
		if !s.warned[sessionUUID] {
			s.warned[sessionUUID] = true
			applog.Warn("[Executor] artifact_session_capped", "session", sessionUUID)
		}
		return false
	}
	s.session[sessionUUID] += n
	return true
}

// Open 打开留存文件。不存在时返回 errMissing。
func (s *ArtifactStore) Open(sessionUUID string, seq int64) (*os.File, error) {
	if s == nil {
		return nil, errMissing
	}
	path, err := ArtifactPath(s.root, sessionUUID, seq)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errMissing
		}
		return nil, err
	}
	return file, nil
}

func (s *ArtifactStore) RemoveSession(sessionUUID string) error {
	if s == nil || !uuidRe.MatchString(sessionUUID) {
		return nil
	}
	return os.RemoveAll(filepath.Join(s.root, sessionUUID))
}

func (s *ArtifactStore) Sweep(now time.Time, live func(uuid string) (exists, running bool, lastActive time.Time)) {
	if s == nil {
		return
	}
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return
	}
	var total int64
	type item struct {
		name string
		size int64
		at   time.Time
	}
	var items []item
	for _, entry := range entries {
		if !entry.IsDir() || !uuidRe.MatchString(entry.Name()) {
			continue
		}
		exists, running, last := live(entry.Name())
		if running {
			continue
		}
		size := dirSize(filepath.Join(s.root, entry.Name()))
		if !exists || (!last.IsZero() && now.Sub(last) > artifactKeepDays*24*time.Hour) {
			_ = os.RemoveAll(filepath.Join(s.root, entry.Name()))
			continue
		}
		total += size
		items = append(items, item{name: entry.Name(), size: size, at: last})
	}
	if total <= totalMaxBytes {
		return
	}
	// 从最旧的会话开始删，直到低于 90%。
	for total > totalMaxBytes*9/10 && len(items) > 0 {
		oldest := 0
		for i := range items {
			if items[i].at.Before(items[oldest].at) {
				oldest = i
			}
		}
		_ = os.RemoveAll(filepath.Join(s.root, items[oldest].name))
		total -= items[oldest].size
		items = append(items[:oldest], items[oldest+1:]...)
	}
}

func headTail(full string, original int, fileOK, redacted, capped bool) string {
	head := trimUTF8(full, inlineHeadBytes)
	tail := suffixUTF8(full, inlineTailBytes)
	fileBit := "0"
	if fileOK {
		fileBit = "1"
	}
	extra := ""
	if redacted {
		extra += " redacted=1"
	}
	if capped {
		extra += " capped=1"
	}
	omitted := original - len(head) - len(tail)
	if omitted < 0 {
		omitted = 0
	}
	marker := fmt.Sprintf("⟦gt-truncated v1 bytes=%d omitted=%d file=%s%s⟧", len(full), omitted, fileBit, extra)
	if strings.HasSuffix(full, marker) {
		full += "\u200b"
		head = trimUTF8(full, inlineHeadBytes)
		tail = suffixUTF8(full, inlineTailBytes)
	}
	return head + "\n…\n" + tail + "\n" + marker
}

func trimUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}

func suffixUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	start := len(s) - max
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}

func writeAtomic(path, content string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp := filepath.Join(dir, "."+filepath.Base(path)+".tmp")
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func dirSize(root string) int64 {
	var n int64
	_ = filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() {
			n += info.Size()
		}
		return nil
	})
	return n
}

// CopyLimited 把文件按 Range 或全文写给调用方。
func CopyLimited(w io.Writer, file *os.File) error {
	_, err := io.Copy(w, file)
	return err
}
