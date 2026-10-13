//go:build !windows

package executor

import (
	"context"
	"os/exec"
)

func cliProbeCommand(ctx context.Context, execPath string, args []string) (*exec.Cmd, error) {
	cmd := exec.CommandContext(ctx, execPath, args...)
	CreateProcessGroup(cmd)
	return cmd, nil
}
