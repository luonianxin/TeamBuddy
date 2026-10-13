package executor

import (
	"os"
	"runtime"
	"strconv"
	"time"
)

const (
	WatchdogModeLegacy = "legacy"
	WatchdogModeShadow = "shadow"
	WatchdogModeNew    = "new"

	StallActionNotify = "notify"
	StallActionKill   = "kill"

	ToolKindShell = "shell"
)

// WatchdogConfig 控制静默提示和卡死判定。
// 本功能分支默认 mode=new，让直接执行不再把静默当成死亡。
// 方案里的 shadow 观察期仍可通过 GOTEAMS_WATCHDOG_MODE=shadow|legacy 切回。
// 静默、CPU/IO 和连接采样仅用于提示，不能证明进程已死。
type WatchdogConfig struct {
	Mode              string
	PollInterval      time.Duration
	QuietNotice       time.Duration
	LongToolNotice    time.Duration
	SuspectAfter      time.Duration
	StallKillAfter    time.Duration
	StallAction       string
	ProtocolBusyGrace time.Duration
	AbsoluteMaxTurn   time.Duration
	TermGrace         time.Duration
	DrainAfterExit    time.Duration
	CPUEpsilon        time.Duration
	IOEpsilonBytes    int64
	FirstEventTimeout time.Duration
	// ModelConnGrace 是「这次请求曾经连上，之后外部连接又持续缺失」多久后结束本轮。
	// 还没见过已建立连接时不使用它，避免慢握手或请求尚未发出就被当成断线。
	ModelConnGrace time.Duration
}

// DefaultWatchdogConfig 返回一份配置。
// 长时间没有进度时保留进程，由用户决定停止；自动恢复只处理已退出的 CLI。
func DefaultWatchdogConfig(unattended bool) WatchdogConfig {
	first := 45 * time.Second
	if runtime.GOOS == "windows" {
		first = 60 * time.Second
	}
	cfg := WatchdogConfig{
		Mode:              WatchdogModeNew,
		PollInterval:      outputIdleCheckInterval,
		QuietNotice:       OutputIdleTimeout,
		LongToolNotice:    OutputToolHardTimeout,
		SuspectAfter:      3 * time.Minute,
		StallKillAfter:    3 * time.Minute,
		StallAction:       StallActionNotify,
		ProtocolBusyGrace: 10 * time.Minute,
		TermGrace:         5 * time.Second,
		DrainAfterExit:    5 * time.Second,
		CPUEpsilon:        200 * time.Millisecond,
		IOEpsilonBytes:    64 << 10,
		FirstEventTimeout: first,
		ModelConnGrace:    15 * time.Second,
	}
	if unattended {
		cfg.StallKillAfter = 30 * time.Minute
	}
	if runtime.GOOS == "linux" {
		cfg.Mode = WatchdogModeLegacy
	}
	if mode := os.Getenv("GOTEAMS_WATCHDOG_MODE"); mode != "" && runtime.GOOS != "linux" {
		switch mode {
		case WatchdogModeLegacy, WatchdogModeShadow, WatchdogModeNew:
			cfg.Mode = mode
		}
	}
	return cfg
}

func (c WatchdogConfig) normalized() WatchdogConfig {
	if c.Mode == "" {
		c.Mode = WatchdogModeNew
	}
	if c.PollInterval <= 0 {
		c.PollInterval = outputIdleCheckInterval
	}
	if c.QuietNotice <= 0 {
		c.QuietNotice = OutputIdleTimeout
	}
	if c.LongToolNotice <= 0 {
		c.LongToolNotice = OutputToolHardTimeout
	}
	if c.SuspectAfter <= 0 {
		c.SuspectAfter = 3 * time.Minute
	}
	if c.StallKillAfter <= 0 {
		c.StallKillAfter = 30 * time.Minute
	}
	if c.StallAction == "" {
		c.StallAction = StallActionNotify
	}
	if c.ProtocolBusyGrace <= 0 {
		c.ProtocolBusyGrace = 10 * time.Minute
	}
	if c.TermGrace <= 0 {
		c.TermGrace = 5 * time.Second
	}
	if c.DrainAfterExit <= 0 {
		c.DrainAfterExit = 5 * time.Second
	}
	if c.CPUEpsilon <= 0 {
		c.CPUEpsilon = 200 * time.Millisecond
	}
	if c.IOEpsilonBytes <= 0 {
		c.IOEpsilonBytes = 64 << 10
	}
	if c.ModelConnGrace <= 0 {
		c.ModelConnGrace = 15 * time.Second
	}
	return c
}

// WatchInput 是一次看门狗求值的外部信号。
type WatchInput struct {
	Now        time.Time
	LastStdout time.Time
	Sample     ProcSample
	StderrGrew bool
	Paused     bool
	UserStop   bool
	// SawProtocol 为真表示已经收到过协议事件。用于慢启动提示。
	SawProtocol bool
	StartedAt   time.Time
	// ModelConn 只在一次模型请求进行中时有意义。
	// Unknown 表示查不到套接字，调用方应退回原来的静默判定。
	ModelConn ModelConn
}

type watchedCall struct {
	started time.Time
	kind    string
}

// watchdog 是纯状态机。Tick 不碰进程，方便压缩时钟测试。
type watchdog struct {
	cfg               WatchdogConfig
	state             string
	lastA             time.Time
	protocolBusyUntil time.Time
	suspectSince      time.Time
	lastSample        ProcSample
	hasSample         bool
	sentQuiet         bool
	sentSuspect       bool
	sentEscalated     bool
	sentSlow          bool
	modelOpen         bool
	modelConnSeen     bool
	modelDownSince    time.Time
	longToolSent      map[string]bool
	tools             map[string]watchedCall
	anon              []watchedCall
	attempt           int
	max               int
}

func newWatchdog(cfg WatchdogConfig) watchdog {
	cfg = cfg.normalized()
	return watchdog{cfg: cfg, state: "active", longToolSent: map[string]bool{}, tools: map[string]watchedCall{}}
}

func (w *watchdog) ObserveHeartbeat(now time.Time) {
	// 心跳说明协议还活着，但不代表这次模型请求已经结束。
	open := w.modelOpen
	w.noteActivity(now)
	w.modelOpen = open
	if open {
		w.state = "model"
	}
}

func (w *watchdog) ObserveEvent(e ExecutorEvent, now time.Time) {
	switch e.Type {
	case EventActivity:
		w.observeActivity(e, now)
	case EventThinking, EventMessage, EventToolCall:
		w.noteActivity(now)
		if e.Type == EventToolCall {
			call := watchedCall{started: now, kind: e.ToolKind}
			id := e.ToolUseID
			if id == "" {
				w.anon = append(w.anon, call)
			} else if _, ok := w.tools[id]; !ok {
				w.tools[id] = call
			}
		}
	case EventToolResult:
		if e.ToolResultPartial {
			w.noteActivity(now)
			return
		}
		w.noteActivity(now)
		if e.ToolUseID != "" {
			delete(w.tools, e.ToolUseID)
		} else if len(w.tools)+len(w.anon) == 1 {
			w.tools = map[string]watchedCall{}
			w.anon = nil
		}
	case EventComplete, EventError:
		w.noteActivity(now)
		w.tools = map[string]watchedCall{}
		w.anon = nil
	}
}

func (w *watchdog) observeActivity(e ExecutorEvent, now time.Time) {
	kind := e.Content
	if e.Activity != nil && e.Activity.Kind != "" {
		kind = e.Activity.Kind
	}
	switch kind {
	case ActivityCallingModel:
		// 请求已经发出。没有 token 只说明模型还没往回写，不是会话结束。
		w.modelOpen = true
		w.modelConnSeen = false
		w.modelDownSince = time.Time{}
		w.suspectSince = time.Time{}
		w.sentQuiet = false
		w.sentSuspect = false
		w.sentEscalated = false
		w.state = "model"
		return
	case ActivityReconnecting, ActivityRetrying, ActivityCompacting:
		w.modelOpen = false
		w.modelConnSeen = false
		w.modelDownSince = time.Time{}
		if e.Activity != nil {
			w.attempt = e.Activity.Attempt
			w.max = e.Activity.Max
		}
		// 最后一次重试不再刷新宽限，交给后续终态。
		if w.max > 0 && w.attempt >= w.max {
			w.state = "retrying"
			return
		}
		grace := w.cfg.ProtocolBusyGrace
		if e.Activity != nil && e.Activity.DelayMs > 0 {
			delay := time.Duration(e.Activity.DelayMs)*time.Millisecond + 2*time.Minute
			if delay > grace {
				grace = delay
			}
		}
		w.protocolBusyUntil = now.Add(grace)
		w.state = "retrying"
		w.suspectSince = time.Time{}
		w.sentSuspect = false
		w.sentEscalated = false
	case ActivityResumed:
		w.protocolBusyUntil = time.Time{}
		w.noteActivity(now)
	default:
		// quiet / long_tool / suspect 是看门狗自己发出的，不能当成新活动。
	}
}

func (w *watchdog) noteActivity(now time.Time) {
	w.lastA = now
	w.state = "active"
	w.suspectSince = time.Time{}
	w.sentQuiet = false
	w.sentSuspect = false
	w.sentEscalated = false
	w.protocolBusyUntil = time.Time{}
	w.modelOpen = false
	w.modelConnSeen = false
	w.modelDownSince = time.Time{}
}

// Tick 求值一次。kill 为真时调用方应终止进程树。
func (w *watchdog) Tick(in WatchInput) (emit []ExecutorEvent, kill bool) {
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	if in.UserStop || in.Paused {
		return nil, false
	}
	if w.state == "retrying" && !w.protocolBusyUntil.IsZero() && now.After(w.protocolBusyUntil) {
		// 宽限结束先回到 active，静默从现在重新计，不直接进入 suspect。
		w.lastA = now
		w.state = "active"
		w.protocolBusyUntil = time.Time{}
		w.suspectSince = time.Time{}
	}
	if w.state == "retrying" && now.Before(w.protocolBusyUntil) {
		return nil, false
	}
	if w.modelOpen {
		w.state = "model"
		switch in.ModelConn {
		case ModelConnUp:
			// 外部连接还在：进程堵在读模型响应上，CPU 和 stdout 都可以是静的。
			w.modelConnSeen = true
			w.modelDownSince = time.Time{}
		case ModelConnDown:
			// 还没见过已建立连接时，可能是握手还没完成，或者这次根本没连上。
			// 这时收掉会把慢请求和正常空档误判成断线，直接执行会变得不可用。
			if !w.modelConnSeen {
				break
			}
			if w.modelDownSince.IsZero() {
				w.modelDownSince = now
			}
			if now.Sub(w.modelDownSince) < w.cfg.ModelConnGrace {
				return emit, false
			}
			w.modelOpen = false
			w.modelConnSeen = false
			w.state = "stalled"
			emit = append(emit, activityEvent(ActivityModelLost, nil))
			if w.cfg.StallAction == StallActionKill {
				return emit, true
			}
			return emit, false
		default:
			// 查不到套接字时不能把「没有连接」当成断线，退回静默判定。
		}
	}
	if !in.SawProtocol && !in.StartedAt.IsZero() && w.cfg.FirstEventTimeout > 0 &&
		now.Sub(in.StartedAt) >= w.cfg.FirstEventTimeout && !w.sentSlow {
		w.sentSlow = true
		emit = append(emit, activityEvent(ActivitySlowLaunch, nil))
	}

	last := in.LastStdout
	if w.lastA.After(last) {
		last = w.lastA
	}
	silent := !last.IsZero() && now.Sub(last) >= w.cfg.QuietNotice
	busy := w.sampleBusy(in)

	for id, tool := range w.tools {
		if w.longToolSent[id] {
			continue
		}
		if now.Sub(tool.started) >= w.cfg.LongToolNotice {
			w.longToolSent[id] = true
			emit = append(emit, activityEvent(ActivityLongTool, nil))
		}
	}
	for i, tool := range w.anon {
		key := anonToolKey(i)
		if w.longToolSent[key] || now.Sub(tool.started) < w.cfg.LongToolNotice {
			continue
		}
		w.longToolSent[key] = true
		emit = append(emit, activityEvent(ActivityLongTool, nil))
	}

	if !silent || busy {
		if busy && silent {
			w.state = "working_silent"
			w.suspectSince = time.Time{}
			if !w.sentQuiet {
				w.sentQuiet = true
				emit = append(emit, activityEvent(ActivityQuiet, nil))
			}
		}
		return emit, false
	}

	// 静默且进程树没有增量。
	if w.suspectSince.IsZero() {
		w.suspectSince = now
	}
	if now.Sub(w.suspectSince) < w.cfg.SuspectAfter && w.state != "suspect" {
		w.state = "working_silent"
		if !w.sentQuiet {
			w.sentQuiet = true
			emit = append(emit, activityEvent(ActivityQuiet, nil))
		}
		return emit, false
	}
	w.state = "suspect"
	if !w.sentSuspect {
		w.sentSuspect = true
		emit = append(emit, activityEvent(ActivitySuspect, nil))
	}
	if now.Sub(w.suspectSince) < w.cfg.StallKillAfter {
		return emit, false
	}
	if w.cfg.StallAction == StallActionKill {
		w.state = "stalled"
		return emit, true
	}
	if !w.sentEscalated {
		w.sentEscalated = true
		info := &ActivityInfo{Kind: ActivitySuspect, Escalated: true}
		emit = append(emit, activityEvent(ActivitySuspect, info))
	}
	return emit, false
}

func (w *watchdog) sampleBusy(in WatchInput) bool {
	if in.StderrGrew {
		return true
	}
	cur := in.Sample
	if !cur.Readable {
		// 进程表不可读时按「还在干活」处理，避免误判卡死。
		return true
	}
	if !w.hasSample {
		w.lastSample = cur
		w.hasSample = true
		return len(cur.PIDs) > 1
	}
	prev := w.lastSample
	w.lastSample = cur
	if cur.CPU-prev.CPU >= w.cfg.CPUEpsilon {
		return true
	}
	if cur.IOBytes >= 0 && prev.IOBytes >= 0 && cur.IOBytes-prev.IOBytes >= w.cfg.IOEpsilonBytes {
		return true
	}
	return pidSetChanged(prev.PIDs, cur.PIDs)
}

func pidSetChanged(prev, cur map[int]int) bool {
	if len(prev) == 0 && len(cur) == 0 {
		return false
	}
	if len(prev) != len(cur) {
		return true
	}
	for pid := range cur {
		if _, ok := prev[pid]; !ok {
			return true
		}
	}
	return false
}

func anonToolKey(index int) string {
	return "anon:" + strconv.Itoa(index)
}

func activityEvent(kind string, info *ActivityInfo) ExecutorEvent {
	if info == nil {
		info = &ActivityInfo{Kind: kind}
	} else if info.Kind == "" {
		info.Kind = kind
	}
	return ExecutorEvent{
		Type:      EventActivity,
		Content:   info.Kind,
		Activity:  info,
		Timestamp: time.Now().UnixMilli(),
	}
}
