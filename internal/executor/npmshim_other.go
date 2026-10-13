//go:build !windows

package executor

func unwrapCommand(path string, args []string) (string, []string, error) {
	return path, nil, nil
}
