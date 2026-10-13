package executor

import (
	"os/exec"
	"time"
)

// ProcSample 是一次进程树采样。IOBytes 为 -1 表示当前平台没有 IO 计数。
type ProcSample struct {
	PIDs     map[int]int
	CPU      time.Duration
	IOBytes  int64
	Readable bool
}

// procTree 在启动时绑定本轮进程树，负责采样、终止和判断是否已清空。
type procTree interface {
	Sample() (ProcSample, error)
	Terminate(grace time.Duration) error
	Empty() bool
	Close() error
	PID() int
}

// startInTree 启动命令并绑定进程树。实现按平台拆分。
func startInTree(cmd *exec.Cmd) (procTree, error) {
	return startInTreePlatform(cmd)
}
