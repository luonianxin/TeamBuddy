//go:build windows

package workspace

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"
)

var (
	modKernel32     = syscall.NewLazyDLL("kernel32.dll")
	procCreateMutex = modKernel32.NewProc("CreateMutexW")
	procCloseHandle = modKernel32.NewProc("CloseHandle")
	errMutexExists  = errors.New("mutex already exists")
)

const errorAlreadyExists = syscall.Errno(183)

type heldMutex struct {
	handle uintptr
}

func (m *heldMutex) release() {
	if m == nil || m.handle == 0 {
		return
	}
	procCloseHandle.Call(m.handle)
	m.handle = 0
}

func createNamedMutex(name string) (*heldMutex, error) {
	mutexName, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	ret, _, lastErr := procCreateMutex.Call(0, 0, uintptr(unsafe.Pointer(mutexName)))
	if ret == 0 {
		return nil, fmt.Errorf("创建 Mutex 失败: %w", lastErr)
	}
	if errno, ok := lastErr.(syscall.Errno); ok && errno == errorAlreadyExists {
		procCloseHandle.Call(ret)
		return nil, errMutexExists
	}
	return &heldMutex{handle: ret}, nil
}

func acquireMigrationLock(string) (func(), error) {
	held, err := createNamedMutex(`Global\GoTeams_Client_DataRootMigration`)
	if err != nil {
		if errors.Is(err, errMutexExists) {
			return nil, fmt.Errorf("数据目录复制正在进行")
		}
		return nil, err
	}
	return held.release, nil
}

func holdLiveInstanceLock(string) (func(), error) {
	held, err := createNamedMutex(`Global\GoTeams_Client_SingleInstance`)
	if err != nil {
		if errors.Is(err, errMutexExists) {
			return nil, fmt.Errorf("旧客户端仍在运行，拒绝复制数据目录")
		}
		return nil, err
	}
	return held.release, nil
}
