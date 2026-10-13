//go:build !darwin && !windows

package executor

import (
	"os/exec"
	"time"
)

// Linux 暂不支持新的进程树采样。启动仍走现有进程组。
// 采样标记为不可读，看门狗在 Linux 上由配置固定为 legacy。

func startInTreePlatform(cmd *exec.Cmd) (procTree, error) {
	CreateProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &legacyProcTree{pid: cmd.Process.Pid}, nil
}

type legacyProcTree struct{ pid int }

func rootGone(pid int) bool { return !pidAlive(pid) }

func (t *legacyProcTree) PID() int { return t.pid }

func (t *legacyProcTree) Close() error { return nil }

func (t *legacyProcTree) Sample() (ProcSample, error) {
	return ProcSample{IOBytes: -1, Readable: false}, nil
}

func (t *legacyProcTree) Empty() bool { return !pidAlive(t.pid) }

func (t *legacyProcTree) Terminate(grace time.Duration) error {
	if grace <= 0 {
		grace = terminateGracePeriod
	}
	return terminateProcessGroup(t.pid, grace, terminateKillWait)
}
