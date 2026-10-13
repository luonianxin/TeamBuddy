package executor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"goteams-client/internal/applog"
)

// liveGroup 记录编排器拉起后尚未回收的进程组，供下次启动补杀。
type liveGroup struct {
	PID     int    `json:"pid"`
	Comm    string `json:"comm"`
	Started string `json:"started,omitempty"`
}

var (
	liveGroupMu   sync.Mutex
	liveGroupDir  string
	liveGroupList []liveGroup
)

// SetLiveGroupDir 设置存活进程组清单目录。启动时先调用 ReapStaleGroups。
func SetLiveGroupDir(dir string) {
	liveGroupMu.Lock()
	liveGroupDir = dir
	liveGroupMu.Unlock()
}

// ReapStaleGroups 杀掉上次进程退出后留下的进程组。命令名对不上的 pid 跳过，避免误杀复用的 pid。
func ReapStaleGroups(dir string) {
	SetLiveGroupDir(dir)
	path := filepath.Join(dir, "live-pgids.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var groups []liveGroup
	if json.Unmarshal(data, &groups) != nil {
		return
	}
	remaining := make([]liveGroup, 0)
	for _, group := range groups {
		if group.PID <= 0 || !pidAlive(group.PID) {
			continue
		}
		// Old records lack a birth identity. Never kill a reused PID just because
		// another node/CLI process happens to have the same executable name.
		if group.Started == "" || processBirthID(group.PID) != group.Started || !processCommandMatches(group.PID, group.Comm) {
			continue
		}
		if err := TerminateProcessTree(group.PID); err != nil {
			applog.Warn("[Executor] stale process cleanup failed", "pid", group.PID, "error", err)
			remaining = append(remaining, group)
		}
	}
	liveGroupMu.Lock()
	liveGroupList = remaining
	writeLiveGroupsLocked()
	liveGroupMu.Unlock()
}

func rememberLiveGroup(pid int, execPath string) {
	if pid <= 0 {
		return
	}
	liveGroupMu.Lock()
	defer liveGroupMu.Unlock()
	liveGroupList = append(liveGroupList, liveGroup{PID: pid, Comm: filepath.Base(execPath), Started: processBirthID(pid)})
	writeLiveGroupsLocked()
}

func forgetLiveGroup(pid int) {
	liveGroupMu.Lock()
	defer liveGroupMu.Unlock()
	next := liveGroupList[:0]
	for _, group := range liveGroupList {
		if group.PID != pid {
			next = append(next, group)
		}
	}
	liveGroupList = next
	writeLiveGroupsLocked()
}

func writeLiveGroupsLocked() {
	if liveGroupDir == "" {
		return
	}
	if err := os.MkdirAll(liveGroupDir, 0o700); err != nil {
		return
	}
	data, err := json.Marshal(liveGroupList)
	if err != nil {
		return
	}
	path := filepath.Join(liveGroupDir, "live-pgids.json")
	tmp, err := os.CreateTemp(liveGroupDir, ".live-pgids-*")
	if err != nil {
		applog.Warn("[Executor] cannot persist process registry", "error", err)
		return
	}
	defer os.Remove(tmp.Name())
	_, writeErr := tmp.Write(data)
	closeErr := tmp.Close()
	if writeErr == nil && closeErr == nil {
		err = os.Rename(tmp.Name(), path)
	} else if writeErr != nil {
		err = writeErr
	} else {
		err = closeErr
	}
	if err != nil {
		applog.Warn("[Executor] cannot persist process registry", "error", err)
	}
}

func processCommandMatches(pid int, comm string) bool {
	if comm == "" {
		return false
	}
	// 读不到命令行时不杀，避免 pid 复用误伤。
	text := processCommandLine(pid)
	if text == "" {
		return false
	}
	return strings.Contains(strings.ToLower(text), strings.ToLower(comm))
}
