package executor

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"goteams-client/internal/applog"
)

// PromptFilePlaceholder 是启动参数中代表临时 prompt 文件路径的占位符。
// 用于只能通过文件接收长 prompt 的 CLI（如 openclaw 的 --message-file）。
const PromptFilePlaceholder = "{prompt_file}"

// promptFilePermission 是临时 prompt 文件的权限（仅当前用户可读写）。
const promptFilePermission os.FileMode = 0o600

// maxStdoutLineBytes 是单行 stdout 允许进入解码器的最大字节数。
// 超过后丢弃这一行并继续读，避免 Scanner 的长度上限让读取提前结束：
// 读取一端停止后，CLI 会堵在写满的管道上，事件通道永不关闭，会话就一直停在执行中。
const maxStdoutLineBytes = 16 << 20

// OutputIdleTimeout 是没有未完成工具调用时，stdout 允许静默的时间。
// 流式增量会持续写 stdout，所以这 90 秒不是「界面没刷出新消息」，而是「一个字节都没有」。
// 一轮正常结束时终态事件应在数秒内出现；90 秒只覆盖首 token 和工具返回后的空档。
const OutputIdleTimeout = 90 * time.Second

// OutputToolHardTimeout 是单次工具执行的总时限，从工具调用开始计算。
// 编译、安装可以长时间不打印；只要子进程还在就继续等，到点才终止。
const OutputToolHardTimeout = 30 * time.Minute

const outputIdleCheckInterval = 5 * time.Second

// OutputIdleStopMessage 是 stdout 静默超时后终止进程的原因。
const OutputIdleStopMessage = "CLI 长时间没有新输出，已终止挂起进程"

// OutputToolHardStopMessage 是工具执行达到总时限后终止进程的原因。
const OutputToolHardStopMessage = "工具执行超过 30 分钟，已终止"

// PromptFileSpec 描述通过临时文件传递的 prompt。
type PromptFileSpec struct {
	Prefix  string // 临时文件名前缀
	Content string // 文件内容
}

// RunSpec 描述一次 CLI 进程执行。
// prompt 的投递方式由调用方决定：StdinText 走标准输入，PromptFile 走临时文件，
// 也可以直接拼进 Args，运行器不预设策略。
type RunSpec struct {
	ExecPath       string
	Args           []string
	WorkDir        string
	EnvVars        map[string]string
	StdinText      string // 非空时写入子进程 stdin；为空则不占用 stdin
	PromptFile     *PromptFileSpec
	ExecPrefixArgs []string
	Watchdog       WatchdogConfig
}

// RunResult 汇总一次进程执行的收尾信息。
type RunResult struct {
	ExitCode int
	WaitErr  error
	ScanErr  error
	StdinErr error
	Stderr   string
}

// Process 表示一个正在运行的 CLI 进程，可被停止并等待收尾。
type Process struct {
	mu              sync.Mutex
	cmd             *exec.Cmd
	cancel          context.CancelFunc
	pid             int
	stopped         bool
	stopErr         error
	scanErr         error
	lines           chan string
	stderrTail      *DiagnosticTail
	stderrDone      chan struct{}
	stdinErr        chan error
	promptPath      string
	waitOnce        sync.Once
	result          *RunResult
	stdoutDone      chan struct{}
	waitDone        chan struct{}
	lastStdout      atomic.Int64
	stderrBytes     atomic.Int64
	watch           idleWatchState
	wd              watchdog
	wdCfg           WatchdogConfig
	tree            procTree
	activities      chan ExecutorEvent
	rootExited      chan struct{}
	rootOnce        sync.Once
	stdout          io.Closer
	stderr          io.Closer
	stopMu          sync.Mutex
	idleStopMessage string
	stallKilled     bool
	sawProtocol     bool
	startedAt       time.Time
}

// StartProcess 启动 CLI 进程并开始按行读取 stdout。
// Lines 通道由调用方消费；通道关闭后再调用 WaitFor 获取收尾结果。
func StartProcess(parent context.Context, spec RunSpec) (*Process, error) {
	innerCtx, cancel := context.WithCancel(parent)

	args := append([]string{}, spec.ExecPrefixArgs...)
	args = append(args, spec.Args...)
	exePath, prefix, unwrapErr := unwrapCommand(spec.ExecPath, args)
	if unwrapErr != nil {
		cancel()
		return nil, unwrapErr
	}
	if len(prefix) > 0 {
		args = append(prefix, args...)
	}
	spec.ExecPath = exePath
	promptPath := ""
	if spec.PromptFile != nil {
		path, err := writePromptFile(spec.PromptFile)
		if err != nil {
			cancel()
			return nil, err
		}
		promptPath = path
		for index, arg := range args {
			args[index] = strings.ReplaceAll(arg, PromptFilePlaceholder, path)
		}
	}

	cmd := exec.CommandContext(innerCtx, spec.ExecPath, args...)
	var tree atomic.Value
	// 上下文取消也走进程树终止，不能只杀根进程留下编译器或包管理器。
	// tree 用 atomic：Start 内部就可能调用 Cancel，和 startInTree 的写入并发。
	cmd.Cancel = func() error {
		if value := tree.Load(); value != nil {
			return value.(procTree).Terminate(5 * time.Second)
		}
		if cmd.Process != nil {
			return TerminateProcessTree(cmd.Process.Pid)
		}
		return nil
	}
	if spec.WorkDir != "" {
		cmd.Dir = spec.WorkDir
	}
	cmd.Env = mergeProcessEnv(spec.EnvVars, spec.WorkDir)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		removePromptFile(promptPath)
		return nil, fmt.Errorf("创建 stdout 管道失败: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		removePromptFile(promptPath)
		return nil, fmt.Errorf("创建 stderr 管道失败: %w", err)
	}

	stdinErrCh := make(chan error, 1)
	var stdin io.WriteCloser
	if spec.StdinText != "" {
		stdin, err = cmd.StdinPipe()
		if err != nil {
			cancel()
			removePromptFile(promptPath)
			return nil, fmt.Errorf("创建 stdin 管道失败: %w", err)
		}
	}

	cfg := spec.Watchdog.normalized()
	if spec.Watchdog.Mode == "" && spec.Watchdog.StallAction == "" {
		cfg = DefaultWatchdogConfig(false)
	}
	started, err := startInTree(cmd)
	if err != nil {
		cancel()
		removePromptFile(promptPath)
		return nil, err
	}
	tree.Store(started)

	proc := &Process{
		cmd:        cmd,
		cancel:     cancel,
		pid:        cmd.Process.Pid,
		lines:      make(chan string, 1024),
		stderrTail: &DiagnosticTail{},
		stderrDone: make(chan struct{}),
		stdoutDone: make(chan struct{}),
		waitDone:   make(chan struct{}),
		stdinErr:   stdinErrCh,
		promptPath: promptPath,
		wd:         newWatchdog(cfg),
		wdCfg:      cfg,
		tree:       started,
		activities: make(chan ExecutorEvent, 32),
		rootExited: make(chan struct{}),
		stdout:     stdout,
		stderr:     stderr,
		startedAt:  time.Now(),
	}
	proc.lastStdout.Store(time.Now().UnixMilli())
	rememberLiveGroup(proc.pid, spec.ExecPath)

	if stdin != nil {
		prompt := spec.StdinText
		go func() {
			_, writeErr := io.WriteString(stdin, prompt)
			closeErr := stdin.Close()
			if writeErr == nil {
				writeErr = closeErr
			}
			stdinErrCh <- writeErr
		}()
	} else {
		stdinErrCh <- nil
	}

	// stderr 只保留尾部，进程非零退出时作为错误详情回传。
	// 必须一直读到 EOF：中途停下会让 CLI 堵在 stderr 管道上，进程无法退出。
	go func() {
		defer close(proc.stderrDone)
		buf := make([]byte, 32*1024)
		for {
			n, err := stderr.Read(buf)
			if n > 0 {
				proc.stderrBytes.Add(int64(n))
				proc.stderrTail.Append(buf[:n])
			}
			if err != nil {
				// 主动关闭读端是为了摆脱脱离进程组的孙进程，不当成失败。
				if !errors.Is(err, io.EOF) && !errors.Is(err, os.ErrClosed) {
					proc.stderrTail.AppendLine("读取 stderr 失败: " + err.Error())
				}
				return
			}
		}
	}()

	go func() {
		defer close(proc.stdoutDone)
		defer close(proc.lines)
		reader := &stdoutActivityReader{r: stdout, touch: func() {
			proc.lastStdout.Store(time.Now().UnixMilli())
		}}
		if err := readStdoutLines(reader, maxStdoutLineBytes, proc.lines); err != nil {
			proc.mu.Lock()
			proc.scanErr = err
			proc.mu.Unlock()
		}
	}()
	go proc.watchStdoutIdle()
	go proc.watchRootExit()

	return proc, nil
}

// stdoutActivityReader 在每次真正读到字节时刷新静默计时。
type stdoutActivityReader struct {
	r     io.Reader
	touch func()
}

func (r *stdoutActivityReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if n > 0 && r.touch != nil {
		r.touch()
	}
	return n, err
}

// ObserveEvent 在解码器出口更新工具状态，不依赖 WebSocket 或数据库消费速度。
func (p *Process) ObserveEvent(event ExecutorEvent) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped {
		return
	}
	now := time.Now()
	if event.Type != EventStart && event.Type != EventActivity && event.Type != EventHeartbeat {
		p.sawProtocol = true
	}
	if p.watch.observe(event, now) {
		p.lastStdout.Store(now.UnixMilli())
	}
	p.wd.ObserveEvent(event, now)
}

// ObserveHeartbeat 把不展示的协议事件记成活动，不进入事件通道。
func (p *Process) ObserveHeartbeat() {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	p.sawProtocol = true
	p.wd.ObserveHeartbeat(now)
}

func (p *Process) IdleStopped() bool { return p.IdleStopMessage() != "" }

// IdleStopMessage 只在成功终止进程树后提供失败原因，不把终止请求当作成功。
func (p *Process) IdleStopMessage() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.idleStopMessage
}

func (p *Process) watchStdoutIdle() {
	mode := p.wdCfg.Mode
	switch mode {
	case WatchdogModeLegacy:
		p.watchStdoutIdleLegacy()
	case WatchdogModeShadow:
		go p.watchStdoutIdleLegacy()
		p.watchStdoutIdleNew(true)
	default:
		p.watchStdoutIdleNew(false)
	}
}

func (p *Process) watchStdoutIdleLegacy() {
	ticker := time.NewTicker(outputIdleCheckInterval)
	defer ticker.Stop()
	stopFailures := 0
	for {
		select {
		case <-p.waitDone:
			return
		case <-ticker.C:
			if p.Stopped() {
				return
			}
			if p.idleStopReason(time.Now()) == "" {
				continue
			}
			if err := p.stopProcess(true); err != nil {
				stopFailures++
				applog.Warn("[Executor] 挂起进程终止失败，未标记会话完成", "pid", p.Pid(), "attempt", stopFailures, "error", err)
				// 不取消根进程、不提前路由；最多重试三次，之后允许用户手动重试停止。
				if stopFailures >= 3 {
					return
				}
				continue
			}
			if p.Stopped() {
				return
			}
		}
	}
}

func (p *Process) watchStdoutIdleNew(shadow bool) {
	ticker := time.NewTicker(p.wdCfg.PollInterval)
	defer ticker.Stop()
	var stderrAt int64
	var state string
	for {
		select {
		case <-p.waitDone:
			return
		case <-ticker.C:
			if p.Stopped() {
				return
			}
			now := time.Now()
			sample := ProcSample{Readable: false, IOBytes: -1}
			if p.tree != nil {
				if got, err := p.tree.Sample(); err == nil {
					sample = got
				}
			}
			stderrNow := p.stderrBytes.Load()
			p.mu.Lock()
			modelOpen := p.wd.modelOpen
			p.mu.Unlock()
			modelConn := ModelConnUnknown
			if modelOpen && sample.Readable {
				modelConn = remoteConnsOpen(sample.PIDs)
			}
			p.mu.Lock()
			input := WatchInput{
				Now:         now,
				LastStdout:  time.UnixMilli(p.lastStdout.Load()),
				Sample:      sample,
				StderrGrew:  stderrNow > stderrAt,
				Paused:      p.watch.paused(),
				UserStop:    p.stopped,
				SawProtocol: p.sawProtocol,
				StartedAt:   p.startedAt,
				ModelConn:   modelConn,
			}
			events, kill := p.wd.Tick(input)
			nextState := p.wd.state
			p.mu.Unlock()
			stderrAt = stderrNow
			if nextState != state {
				applog.Info("[Executor] cli_watchdog_state", "pid", p.Pid(), "state", nextState, "shadow", shadow)
				state = nextState
			}
			for _, event := range events {
				p.pushActivity(event)
			}
			if !kill {
				continue
			}
			if shadow {
				applog.Info("[Executor] cli_watchdog_would_kill_total", "pid", p.Pid(), "old_reason", p.idleStopReason(now), "new_state", nextState)
				continue
			}
			p.mu.Lock()
			p.stallKilled = true
			p.mu.Unlock()
			if err := p.stopProcess(true); err != nil {
				applog.Warn("[Executor] 卡死进程终止失败", "pid", p.Pid(), "error", err)
			}
			return
		}
	}
}

func (p *Process) pushActivity(event ExecutorEvent) {
	select {
	case p.activities <- event:
	default:
	}
}

func (p *Process) watchRootExit() {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-p.waitDone:
			return
		case <-ticker.C:
			if rootGone(p.Pid()) {
				p.rootOnce.Do(func() { close(p.rootExited) })
				return
			}
		}
	}
}

func (p *Process) idleStopReason(now time.Time) string {
	p.mu.Lock()
	if p.stopped || p.watch.paused() {
		p.mu.Unlock()
		return ""
	}
	version, pid := p.watch.version, p.pid
	probe := p.watch.hasSubprocessTool() && !p.watch.hardExpired(now)
	p.mu.Unlock()
	childAlive := probe && hasLiveDescendant(pid)
	p.mu.Lock()
	defer p.mu.Unlock()
	// 慢进程探测期间可能已经收到结果或授权请求，不能用旧快照终止。
	if p.stopped || p.watch.version != version {
		return ""
	}
	return p.watch.reason(now, time.UnixMilli(p.lastStdout.Load()), childAlive)
}

// hasLiveDescendant 报告 root 下面是否还有活着的子进程。
// 查不到进程表时按「还在」处理，避免把正在编译的命令误判成挂起。
func hasLiveDescendant(root int) bool {
	if root <= 0 {
		return false
	}
	parents, err := snapshotProcessParents()
	if err != nil {
		return true
	}
	return hasDescendant(parents, root)
}

func hasDescendant(parents map[int]int, root int) bool {
	if root <= 0 || len(parents) == 0 {
		return false
	}
	children := map[int][]int{}
	for pid, ppid := range parents {
		children[ppid] = append(children[ppid], pid)
	}
	stack := append([]int{}, children[root]...)
	seen := map[int]bool{}
	for len(stack) > 0 {
		pid := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if pid == root || seen[pid] {
			continue
		}
		seen[pid] = true
		stack = append(stack, children[pid]...)
	}
	return len(seen) > 0
}

// readStdoutLines 按 LF 读取 stdout，并一直读到 EOF。
// 超长行会被跳过而不是让整个读取失败退出，这样后续的终态事件和进程退出都还能被观察到。
func readStdoutLines(r io.Reader, maxLine int, lines chan<- string) error {
	reader := bufio.NewReaderSize(r, 64*1024)
	var acc []byte
	skipping := false
	var skipErr error
	for {
		part, err := reader.ReadSlice('\n')
		if len(part) > 0 {
			if skipping {
				// 继续丢掉当前超长行，直到遇到换行。
			} else if maxLine > 0 && len(acc)+len(part) > maxLine {
				skipping = true
				acc = nil
				if skipErr == nil {
					skipErr = fmt.Errorf("单行输出超过 %d 字节，已跳过并继续读取", maxLine)
				}
			} else {
				acc = append(acc, part...)
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if !skipping && len(acc) > 0 {
			lines <- strings.TrimRight(string(acc), "\r\n")
		}
		acc = acc[:0]
		skipping = false
		if err == io.EOF {
			return skipErr
		}
		if err != nil {
			if skipErr != nil {
				return skipErr
			}
			return err
		}
	}
}

// Lines 返回 stdout 的行通道，通道关闭表示输出读取结束。
func (p *Process) Lines() <-chan string {
	return p.lines
}

// Pid 返回进程组根进程 ID。
func (p *Process) Pid() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pid
}

// Stopped 报告进程是否收到过停止请求。
func (p *Process) Stopped() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stopped
}

// Stop 终止进程树并取消上下文。
// 必须先按存活 PID 终止整棵进程树再 cancel：先 cancel 会让根进程先消失，
// 子进程会变成孤儿进程继续运行。
func (p *Process) Stop() error { return p.stopProcess(false) }

func (p *Process) stopProcess(watchdog bool) error {
	return p.stopUsing(watchdog, func(pid int) error {
		if p.tree != nil {
			return p.tree.Terminate(p.wdCfg.TermGrace)
		}
		return TerminateProcessTree(pid)
	})
}

func (p *Process) stopUsing(watchdog bool, terminate func(int) error) error {
	p.stopMu.Lock()
	defer p.stopMu.Unlock()
	select {
	case <-p.waitDone:
		return nil
	default:
	}
	if p.Stopped() {
		return nil
	}
	reason := ""
	if watchdog {
		reason = p.idleStopReason(time.Now())
		p.mu.Lock()
		stalled := p.stallKilled
		p.mu.Unlock()
		if reason == "" && !stalled {
			return nil
		}
		if stalled {
			reason = "CLI 长时间无任何活动（进程树无 CPU/IO、无输出），已终止"
		}
	}
	pid := p.Pid()
	err := terminate(pid)
	p.mu.Lock()
	p.stopErr = err
	if err == nil {
		p.stopped = true
		if watchdog && p.stallKilled {
			reason = "CLI 长时间无任何活动（进程树无 CPU/IO、无输出），已终止"
		}
		p.idleStopMessage = reason
	}
	p.mu.Unlock()
	if err == nil {
		p.cancel()
	}
	return err
}

// WaitFor 等待进程退出并汇总 stdout/stderr/stdin 与退出码信息。
// 必须在 Lines 通道关闭（编码器已消费完输出）之后调用；可重复调用，只有第一次真正等待。
func (p *Process) WaitFor() *RunResult {
	p.waitOnce.Do(func() {
		p.waitStderr()

		waitErr := p.cmd.Wait()
		stdinErr := <-p.stdinErr

		p.mu.Lock()
		scanErr := p.scanErr
		p.mu.Unlock()

		removePromptFile(p.promptPath)
		if p.GroupEmpty() {
			forgetLiveGroup(p.pid)
		}
		if p.tree != nil {
			_ = p.tree.Close()
		}

		// 与停止请求同步，确保终止原因先于最终事件可见。
		p.stopMu.Lock()
		defer p.stopMu.Unlock()
		defer close(p.waitDone)
		defer p.cancel()
		p.result = &RunResult{
			ExitCode: exitCodeOf(waitErr),
			WaitErr:  waitErr,
			ScanErr:  scanErr,
			StdinErr: stdinErr,
			Stderr:   p.stderrTail.String(),
		}
	})
	return p.result
}

func writePromptFile(spec *PromptFileSpec) (string, error) {
	prefix := strings.TrimSpace(spec.Prefix)
	if prefix == "" {
		prefix = "goteams-prompt-"
	}
	file, err := os.CreateTemp("", prefix+"*.md")
	if err != nil {
		return "", fmt.Errorf("创建 prompt 临时文件失败: %w", err)
	}
	path := file.Name()
	if _, err := file.WriteString(spec.Content); err != nil {
		file.Close()
		removePromptFile(path)
		return "", fmt.Errorf("写入 prompt 临时文件失败: %w", err)
	}
	if err := file.Close(); err != nil {
		removePromptFile(path)
		return "", fmt.Errorf("关闭 prompt 临时文件失败: %w", err)
	}
	_ = os.Chmod(path, promptFilePermission)
	return path, nil
}

func removePromptFile(path string) {
	if path == "" {
		return
	}
	_ = os.Remove(path)
}

// Activities 返回看门狗发出的活动事件。
func (p *Process) Activities() <-chan ExecutorEvent { return p.activities }

// RootExited 在根进程退出后关闭。
func (p *Process) RootExited() <-chan struct{} { return p.rootExited }

// DrainAfter 是根进程退出后的输出等待预算，不包含消费事件所用时间。
func (p *Process) DrainAfter() time.Duration {
	if p.wdCfg.DrainAfterExit <= 0 {
		return 5 * time.Second
	}
	return p.wdCfg.DrainAfterExit
}

// CloseStdout 关闭 stdout 读端，避免孙进程继承管道导致读取永不结束。
func (p *Process) CloseStdout() {
	if p.stdout != nil {
		_ = p.stdout.Close()
	}
}

// drainStdout preserves buffered protocol output after process-tree cleanup.
// Escaped writers are bounded by bytes and cumulative waiting time; consuming
// an event may involve slow storage, so its duration is excluded from that budget.
func (p *Process) drainStdout(ctx context.Context, consume func(string)) bool {
	const maxDrainBytes = 256 << 20
	remaining := p.DrainAfter()
	bytes := 0
	complete := true
	for {
		if remaining <= 0 || bytes >= maxDrainBytes || ctx.Err() != nil {
			complete = false
			break
		}
		started := time.Now()
		timer := time.NewTimer(remaining)
		select {
		case line, ok := <-p.lines:
			timer.Stop()
			remaining -= time.Since(started)
			if !ok {
				return true
			}
			bytes += len(line)
			consume(line)
		case <-timer.C:
			complete = false
		case <-ctx.Done():
			timer.Stop()
			complete = false
		}
		if !complete {
			break
		}
	}
	p.CloseStdout()
	// The reader may already be waiting to enqueue a line. Drain that finite
	// remainder to avoid leaking it, even when an escaped writer was truncated.
	for line := range p.lines {
		consume(line)
	}
	return complete
}

// CloseStderr 关闭 stderr 读端。setsid 脱离进程组的孙进程杀不到时，否则 WaitFor 会一直等 EOF。
func (p *Process) CloseStderr() {
	if p.stderr != nil {
		_ = p.stderr.Close()
	}
}

// waitStderr 先等 stderr 自然结束。超过排空时间仍不结束就关掉读端，避免永久卡住。
func (p *Process) waitStderr() {
	timer := time.NewTimer(p.DrainAfter())
	defer timer.Stop()
	select {
	case <-p.stderrDone:
	case <-timer.C:
		p.CloseStderr()
		<-p.stderrDone
	}
}

// ReapGroup 回收本轮进程组里残留的后代。
func (p *Process) ReapGroup(grace time.Duration) error {
	if p.tree == nil {
		return TerminateProcessTree(p.Pid())
	}
	err := p.tree.Terminate(grace)
	if p.tree.Empty() {
		applog.Info("[Executor] cli_orphan_reaped_total", "pid", p.Pid())
	} else if err == nil {
		err = fmt.Errorf("process tree %d is still alive after cleanup", p.Pid())
	}
	return err
}

// GroupEmpty is conservative when process-tree inspection fails.
func (p *Process) GroupEmpty() bool {
	if p.tree != nil {
		return p.tree.Empty()
	}
	return !pidAlive(p.Pid())
}

// StallKilled 报告新看门狗是否因卡死终止了进程。
func (p *Process) StallKilled() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stallKilled
}

// UserStopped 报告用户停止。看门狗终止不算。
func (p *Process) UserStopped() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stopped && !p.stallKilled && p.idleStopMessage == ""
}

// SawProtocol 报告是否已经收到过协议事件。
func (p *Process) SawProtocol() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sawProtocol
}

func mergeProcessEnv(extra map[string]string, workDir string) []string {
	env := os.Environ()
	if len(extra) == 0 && workDir == "" {
		return nil
	}
	if workDir != "" {
		filtered := env[:0:0]
		for _, item := range env {
			if strings.HasPrefix(item, "PWD=") {
				continue
			}
			filtered = append(filtered, item)
		}
		env = filtered
	}
	for key, value := range extra {
		if workDir != "" && key == "PWD" {
			continue
		}
		env = append(env, key+"="+value)
	}
	if workDir != "" {
		env = append(env, "PWD="+workDir)
	}
	return env
}

// exitCodeOf 从等待错误中解析退出码，无法解析时返回 -1。
func exitCodeOf(waitErr error) int {
	if waitErr == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

// FormatExitCode 返回退出码的可读文本，用于错误详情与提示。
func FormatExitCode(code int) string {
	return strconv.Itoa(code)
}

// FormatExecutionError 统一拼装 CLI 执行失败原因：
// 依次合并协议层错误、协议特有细节、stderr 尾部、stdout 读取错误与退出码。
func FormatExecutionError(displayName, failureReason, stderrText string, extraDetails []string, scanErr, waitErr error) string {
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		displayName = "CLI"
	}
	details := make([]string, 0, 4+len(extraDetails))
	appendDetail := func(detail string) {
		detail = strings.TrimSpace(detail)
		if detail == "" {
			return
		}
		for _, existing := range details {
			if existing == detail {
				return
			}
		}
		details = append(details, detail)
	}

	appendDetail(failureReason)
	for _, extra := range extraDetails {
		appendDetail(extra)
	}
	appendDetail(stderrText)
	if scanErr != nil {
		appendDetail("读取 " + displayName + " 输出失败: " + scanErr.Error())
	}

	exitCode := exitCodeOf(waitErr)
	exitLabel := ""
	if exitCode >= 0 {
		exitLabel = fmt.Sprintf("（退出码 %d）", exitCode)
	}

	if len(details) > 0 {
		return fmt.Sprintf("%s 运行失败%s：%s", displayName, exitLabel, strings.Join(details, "\n"))
	}
	if waitErr != nil && exitCode < 0 {
		reason := strings.TrimSpace(waitErr.Error())
		if reason != "" && !strings.HasPrefix(reason, "exit status") {
			return fmt.Sprintf("%s 运行失败：%s", displayName, reason)
		}
	}
	return fmt.Sprintf(
		"%s 运行失败%s：CLI 未输出错误详情，请检查 %s 登录状态、网络、模型和 CLI 配置",
		displayName,
		exitLabel,
		displayName,
	)
}
