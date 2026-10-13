//go:build windows

package executor

import (
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func startInTreePlatform(cmd *exec.Cmd) (procTree, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		CreateProcessGroup(cmd)
		if startErr := cmd.Start(); startErr != nil {
			return nil, startErr
		}
		return &fallbackWinTree{pid: cmd.Process.Pid}, nil
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		windows.CloseHandle(job)
		CreateProcessGroup(cmd)
		if startErr := cmd.Start(); startErr != nil {
			return nil, startErr
		}
		return &fallbackWinTree{pid: cmd.Process.Pid}, nil
	}

	attr := cmd.SysProcAttr
	if attr == nil {
		attr = &syscall.SysProcAttr{}
		cmd.SysProcAttr = attr
	}
	attr.CreationFlags |= windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_SUSPENDED
	if err = cmd.Start(); err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	pid := cmd.Process.Pid
	handle, openErr := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_INFORMATION, false, uint32(pid))
	if openErr != nil {
		_ = resumeProcess(pid)
		windows.CloseHandle(job)
		return &fallbackWinTree{pid: pid}, nil
	}
	assignErr := windows.AssignProcessToJobObject(job, handle)
	windows.CloseHandle(handle)
	// 恢复失败时进程会一直挂起，不论是否已进 Job 都必须终止并报错。
	if resumeErr := resumeProcess(pid); resumeErr != nil {
		if assignErr == nil {
			_ = windows.TerminateJobObject(job, 1)
		}
		windows.CloseHandle(job)
		_ = TerminateProcessTree(pid)
		return nil, resumeErr
	}
	if assignErr != nil {
		windows.CloseHandle(job)
		return &fallbackWinTree{pid: pid}, nil
	}
	return &jobProcTree{pid: pid, job: job}, nil
}

func rootGone(pid int) bool { return !pidAlive(pid) }

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	var code uint32
	if err = windows.GetExitCodeProcess(handle, &code); err != nil {
		return false
	}
	return code == 259 // STILL_ACTIVE
}

type jobProcTree struct {
	mu  sync.RWMutex
	pid int
	job windows.Handle
}

func (t *jobProcTree) PID() int { return t.pid }

func (t *jobProcTree) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.job == 0 {
		return nil
	}
	err := windows.CloseHandle(t.job)
	t.job = 0
	return err
}

func (t *jobProcTree) Sample() (ProcSample, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	var info jobAccounting
	err := windows.QueryInformationJobObject(
		t.job,
		windows.JobObjectBasicAndIoAccountingInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
		nil,
	)
	if err != nil {
		return ProcSample{IOBytes: -1, Readable: false}, err
	}
	cpu := time.Duration(info.TotalUserTime+info.TotalKernelTime) * 100
	io := int64(info.ReadTransferCount + info.WriteTransferCount)
	return ProcSample{
		PIDs:     map[int]int{t.pid: 0},
		CPU:      cpu,
		IOBytes:  io,
		Readable: true,
	}, nil
}

func (t *jobProcTree) Empty() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.job == 0 {
		return !pidAlive(t.pid)
	}
	var list struct {
		NumberOfAssignedProcesses uint32
		NumberOfProcessIdsInList  uint32
		ProcessIdList             [64]uintptr
	}
	err := windows.QueryInformationJobObject(
		t.job,
		windows.JobObjectBasicProcessIdList,
		uintptr(unsafe.Pointer(&list)),
		uint32(unsafe.Sizeof(list)),
		nil,
	)
	if err != nil {
		return false
	}
	return list.NumberOfAssignedProcesses == 0 && list.NumberOfProcessIdsInList == 0
}

func (t *jobProcTree) Terminate(grace time.Duration) error {
	if grace <= 0 {
		grace = 5 * time.Second
	}
	_ = windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(t.pid))
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if t.Empty() {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.mu.RLock()
	if t.job != 0 {
		_ = windows.TerminateJobObject(t.job, 1)
	}
	t.mu.RUnlock()
	deadline = time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if t.Empty() {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return TerminateProcessTree(t.pid)
}

type fallbackWinTree struct{ pid int }

func (t *fallbackWinTree) PID() int     { return t.pid }
func (t *fallbackWinTree) Close() error { return nil }
func (t *fallbackWinTree) Sample() (ProcSample, error) {
	return ProcSample{PIDs: map[int]int{t.pid: 0}, IOBytes: -1, Readable: false}, nil
}
func (t *fallbackWinTree) Empty() bool                         { return !pidAlive(t.pid) }
func (t *fallbackWinTree) Terminate(grace time.Duration) error { return TerminateProcessTree(t.pid) }

type jobAccounting struct {
	TotalUserTime        int64
	TotalKernelTime      int64
	ThisPeriodUserTime   int64
	ThisPeriodKernelTime int64
	TotalPageFaultCount  uint32
	TotalProcesses       uint32
	ActiveProcesses      uint32
	TotalTerminated      uint32
	ReadOperationCount   uint64
	WriteOperationCount  uint64
	OtherOperationCount  uint64
	ReadTransferCount    uint64
	WriteTransferCount   uint64
	OtherTransferCount   uint64
}

func resumeProcess(pid int) error {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snap)
	var entry windows.ThreadEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err = windows.Thread32First(snap, &entry); err != nil {
		return err
	}
	for {
		if entry.OwnerProcessID == uint32(pid) {
			thread, openErr := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
			if openErr == nil {
				_, _ = windows.ResumeThread(thread)
				windows.CloseHandle(thread)
			}
		}
		if err = windows.Thread32Next(snap, &entry); err != nil {
			break
		}
	}
	return nil
}
