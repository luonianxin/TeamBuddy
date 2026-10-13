package executor

import "strings"

// ExitClass 是一轮 CLI 进程的退出分类。编排器只按这个值决定续跑或终态。
type ExitClass string

const (
	ExitCompleted       ExitClass = "completed"
	ExitUserStopped     ExitClass = "user_stopped"
	ExitLaunchFailed    ExitClass = "launch_failed"
	ExitFatalUpstream   ExitClass = "fatal_upstream"
	ExitFatalCLI        ExitClass = "fatal_cli"
	ExitTransport       ExitClass = "transport"
	ExitCrash           ExitClass = "crash"
	ExitStalled         ExitClass = "stalled"
	ExitNoTerminalClean ExitClass = "no_terminal_clean_exit"
)

// Resumable 表示该分类可以自动续跑一次。
func (c ExitClass) Resumable() bool {
	switch c {
	case ExitTransport, ExitCrash, ExitStalled, ExitNoTerminalClean:
		return true
	default:
		return false
	}
}

// ExitFacts 是收尾分类的输入。MissingTerminal 表示解码器没有收到协议终态，
// 这时 FailureReason 只是诊断，不能当成 fatal_cli。
type ExitFacts struct {
	TerminalSeen    bool
	FailureReason   string
	ExitHint        ExitClass
	MissingTerminal bool
	IdleStop        string
	StallKilled     bool
	UserStopped     bool
	ExitCode        int
	WaitErr         error
	ScanErr         error
	StdinErr        error
	Stderr          string
	SawProtocol     bool
}

// ClassifyExit 按 4.12.4 的优先级给出退出分类。
func ClassifyExit(f ExitFacts) ExitClass {
	if f.UserStopped {
		return ExitUserStopped
	}
	if f.StallKilled {
		return ExitStalled
	}
	if f.ExitHint != "" {
		return f.ExitHint
	}
	// legacy 看门狗杀进程仍按原失败处理，不续跑。
	if strings.TrimSpace(f.IdleStop) != "" {
		return ExitFatalCLI
	}
	if !f.SawProtocol && (f.ExitCode == 127 || f.ExitCode == 126 || f.StdinErr != nil) {
		return ExitLaunchFailed
	}
	if f.TerminalSeen && !f.MissingTerminal {
		if strings.TrimSpace(f.FailureReason) != "" {
			return ExitFatalCLI
		}
		return ExitCompleted
	}
	if looksTransport(f.Stderr) || looksTransport(f.FailureReason) {
		return ExitTransport
	}
	if f.ExitCode == 0 && f.WaitErr == nil {
		return ExitNoTerminalClean
	}
	return ExitCrash
}

func looksTransport(text string) bool {
	lower := strings.ToLower(text)
	for _, needle := range []string{
		"econnreset", "econnrefused", "etimedout",
		"stream disconnected", "connection reset", "connection lost",
		"timed out", "timeout", "broken pipe", "tls handshake",
		"network is unreachable", "temporary failure",
	} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

// LooksFatalUpstream 识别鉴权、计费、模型不存在这类不应续跑的失败。
func LooksFatalUpstream(text string) bool {
	lower := strings.ToLower(text)
	for _, needle := range []string{
		"authentication_failed", "billing_error", "account_on_hold",
		"oauth_org_not_allowed", "model_not_found", "invalid_request",
		"unauthorized", "forbidden", "quota", "context length",
		"insufficient_quota", "rate limit exceeded",
	} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}
