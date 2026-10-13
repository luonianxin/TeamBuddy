//go:build darwin

package executor

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// probeRemoteConns 用 lsof 看这些 pid 还有没有已建立的 TCP。
// 请求发出前本来就可能没有连接，调用方不能单凭 Down 结束会话。
// 本地代理（127.0.0.1）也算还连着，避免把走本机转发的模型请求当成断线。
func probeRemoteConns(pids map[int]int) ModelConn {
	list := make([]string, 0, len(pids))
	for pid := range pids {
		if pid > 0 {
			list = append(list, strconv.Itoa(pid))
		}
	}
	if len(list) == 0 {
		return ModelConnDown
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/sbin/lsof",
		"-nP", "-a",
		"-p", strings.Join(list, ","),
		"-iTCP", "-sTCP:ESTABLISHED",
		"-Fn",
	)
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return ModelConnUnknown
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return ModelConnDown
		}
		return ModelConnUnknown
	}
	if bytes.Contains(out, []byte("->")) {
		return ModelConnUp
	}
	return ModelConnDown
}
