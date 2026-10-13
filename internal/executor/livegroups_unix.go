//go:build !windows

package executor

import (
	"context"
	"os/exec"
	"strconv"
	"time"
)

func processCommandLine(pid int) string {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil {
		return ""
	}
	return string(out)
}
