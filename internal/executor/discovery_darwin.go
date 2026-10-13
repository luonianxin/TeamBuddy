//go:build darwin

package executor

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

var discoveryPath struct {
	sync.RWMutex
	dirs []string
}

func persistedPathDirs() []string {
	discoveryPath.RLock()
	defer discoveryPath.RUnlock()
	return append([]string(nil), discoveryPath.dirs...)
}

// 手动重新检测时读取更新后的 shell PATH，不修改 sidecar 的全局环境。
// 与桌面启动一致，只运行固定命令；shell 启动文件属于当前用户的本机信任边界。
func refreshDiscoveryPath(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, 2750*time.Millisecond)
	defer cancel()
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	shell := os.Getenv("SHELL")
	if !filepath.IsAbs(shell) {
		shell = "/bin/zsh"
	}
	name := filepath.Base(shell)
	modes := [][]string{{"-ilc"}}
	switch name {
	case "zsh", "fish", "sh", "ksh":
	case "bash":
		modes = [][]string{{"-ic"}, {"-lc"}}
	case "csh", "tcsh":
		modes = [][]string{{"-ic"}}
	default:
		return
	}
	const marker = "__GOTEAMS_LOGIN_PATH__="
	const command = "/usr/bin/printf '" + marker + "'; /usr/bin/printenv PATH"
	// 交互与登录启动文件并行读取，共用这一次期限。串行时较慢的 .bashrc 会取消登录 PATH。
	limit := &shellPathOutput{cancel: cancel}
	found := make([][]string, len(modes))
	var wg sync.WaitGroup
	for index, mode := range modes {
		wg.Add(1)
		go func(index int, mode []string) {
			defer wg.Done()
			found[index] = readShellPath(ctx, shell, name, mode, home, command, marker, limit)
		}(index, mode)
	}
	wg.Wait()
	if parent.Err() != nil || limit.overflowed() {
		return
	}
	var paths []string
	for _, part := range found {
		paths = append(paths, part...)
	}
	var dirs []string
	seen := map[string]bool{}
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if len(path) > 4096 || !filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\r\n") || seen[path] {
			continue
		}
		seen[path] = true
		dirs = append(dirs, path)
	}
	if len(dirs) > 0 {
		discoveryPath.Lock()
		discoveryPath.dirs = dirs
		discoveryPath.Unlock()
	}
}

func readShellPath(ctx context.Context, shell string, name string, mode []string, home string, command string, marker string, limit *shellPathOutput) []string {
	if ctx.Err() != nil {
		return nil
	}
	cmd := exec.CommandContext(ctx, shell, append(mode, command)...)
	if name == "csh" || name == "tcsh" {
		cmd.Args[0] = "-" + name
	}
	cmd.Dir = home
	// 探测不需要桌面与云端凭据，使用最小启动环境。
	cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "SHELL=" + shell}
	for _, key := range []string{"USER", "LOGNAME", "TMPDIR", "LANG"} {
		if value, ok := os.LookupEnv(key); ok {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	CreateProcessGroup(cmd)
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 50 * time.Millisecond
	var buffer bytes.Buffer
	output := &shellPathOutputWriter{limit: limit, buffer: &buffer}
	cmd.Stdout, cmd.Stderr = output, output
	err := cmd.Run()
	if cmd.Process != nil {
		// 按本次独立进程组清理 shell 留下的后台进程，避免管道泄漏。
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		deadline := time.Now().Add(50 * time.Millisecond)
		for time.Now().Before(deadline) && syscall.Kill(-cmd.Process.Pid, 0) == nil {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if err != nil {
		return nil
	}
	var paths []string
	for _, line := range strings.Split(buffer.String(), "\n") {
		if index := strings.Index(line, marker); index >= 0 {
			paths = append(paths, strings.Split(strings.TrimSpace(line[index+len(marker):]), ":")...)
		}
	}
	return paths
}

// shellPathOutput 统计同一次刷新里所有 shell 的输出，超过 64 KiB 时取消全部探测。
type shellPathOutput struct {
	mu       sync.Mutex
	total    int
	exceeded bool
	cancel   context.CancelFunc
}

func (b *shellPathOutput) overflowed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.exceeded
}

type shellPathOutputWriter struct {
	limit  *shellPathOutput
	buffer *bytes.Buffer
}

func (w *shellPathOutputWriter) Write(p []byte) (int, error) {
	w.limit.mu.Lock()
	if w.limit.exceeded {
		w.limit.mu.Unlock()
		return 0, io.ErrShortBuffer
	}
	w.limit.total += len(p)
	if w.limit.total > 64*1024 {
		w.limit.exceeded = true
		cancel := w.limit.cancel
		w.limit.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		return 0, io.ErrShortBuffer
	}
	n, err := w.buffer.Write(p)
	w.limit.mu.Unlock()
	return n, err
}
