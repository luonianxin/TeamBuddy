package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// NewCLIProbeCommand 只供版本/模型发现使用，不改变业务 CLI 执行器。
// 取消探测时清理进程树，并限制子进程保留输出管道造成的等待。
func NewCLIProbeCommand(ctx context.Context, execPath string, args ...string) (*exec.Cmd, error) {
	cmd, err := cliProbeCommand(ctx, execPath, args)
	if err != nil {
		return nil, err
	}
	cmd.Cancel = func() error { return TerminateProcessTree(cmd.Process.Pid) }
	cmd.WaitDelay = 2 * time.Second
	// 发现操作不能触发 CodeBuddy 在安装目录中自动更新自身。
	cmd.Env = os.Environ()
	dirs := persistedPathDirs()
	if runtime.GOOS == "windows" {
		dirs = append([]string{filepath.Dir(execPath)}, dirs...)
	}
	if len(dirs) > 0 {
		// npm 垫片还需找到 node；不能只发现新目录中的 .cmd 而沿用旧 PATH。
		cmd.Env = append(cmd.Env, "PATH="+strings.Join(dirs, string(os.PathListSeparator))+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	name := strings.TrimSuffix(strings.ToLower(filepath.Base(execPath)), strings.ToLower(filepath.Ext(execPath)))
	if name == "codebuddy" || name == "cbc" {
		cmd.Env = append(cmd.Env, "DISABLE_AUTOUPDATER=1")
	}
	return cmd, nil
}

const maxCLIProbeOutput = 4 << 20

type probeOutput struct {
	buffer bytes.Buffer
	cancel context.CancelFunc
}

func (b *probeOutput) Write(p []byte) (int, error) {
	if len(p) > maxCLIProbeOutput-b.buffer.Len() {
		b.cancel()
		return 0, fmt.Errorf("CLI 探测输出超过大小限制")
	}
	return b.buffer.Write(p)
}

func runCLIProbe(ctx context.Context, execPath string, combined bool, args ...string) ([]byte, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd, err := NewCLIProbeCommand(ctx, execPath, args...)
	if err != nil {
		return nil, err
	}
	output := probeOutput{cancel: cancel}
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	if combined {
		cmd.Stderr = &output
	}
	err = cmd.Run()
	return output.buffer.Bytes(), err
}
