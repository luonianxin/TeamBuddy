//go:build !windows

package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

type heldFileLock struct {
	file *os.File
}

func (h *heldFileLock) release() {
	if h == nil || h.file == nil {
		return
	}
	_ = syscall.Flock(int(h.file.Fd()), syscall.LOCK_UN)
	_ = h.file.Close()
	h.file = nil
}

func acquireMigrationLock(home string) (func(), error) {
	if err := os.MkdirAll(home, 0o755); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(home, migrateLockName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("无法创建数据目录复制锁: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("数据目录复制正在进行: %w", err)
	}
	held := &heldFileLock{file: file}
	return held.release, nil
}

func holdLiveInstanceLock(liveRoot string) (func(), error) {
	path := filepath.Join(liveRoot, "runtime", "instance.lock")
	file, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if os.IsNotExist(err) {
		return func() {}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("无法检查旧客户端实例锁: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("旧客户端仍在运行，拒绝复制数据目录: %w", err)
	}
	held := &heldFileLock{file: file}
	return held.release, nil
}
