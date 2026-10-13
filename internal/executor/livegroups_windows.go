//go:build windows

package executor

import "golang.org/x/sys/windows"

// processCommandLine 在 Windows 上返回进程映像全路径，用于补杀前核对。
// 读不到时返回空，调用方不杀，避免 PID 复用误杀无关进程（Windows 上 PID 复用很快）。
func processCommandLine(pid int) string {
	if pid <= 0 {
		return ""
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(handle)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(handle, 0, &buf[0], &size); err != nil {
		return ""
	}
	return windows.UTF16ToString(buf[:size])
}
