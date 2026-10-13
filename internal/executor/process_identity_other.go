//go:build !darwin && !windows

package executor

// Unsupported desktop platforms cannot safely identify a stale process.
func processBirthID(pid int) string { return "" }
