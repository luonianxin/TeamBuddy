//go:build !windows

package executor

func validateWrappedArgs(path string, args []string) error { return nil }
