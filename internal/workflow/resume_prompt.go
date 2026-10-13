package workflow

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"goteams-client/internal/applog"
	"goteams-client/internal/executor"
	"goteams-client/internal/i18n"
	"goteams-client/internal/storage/log"
)

type resumeTool struct {
	ID      string
	Name    string
	Args    string
	Kind    string
	Tail    string
	Started time.Time
}

func noteResumeTool(tools map[string]*resumeTool, event executor.ExecutorEvent) {
	switch event.Type {
	case executor.EventToolCall:
		id := strings.TrimSpace(event.ToolUseID)
		if id == "" {
			// 没有调用 ID 就无法证明结果对应哪次操作，保留到本轮终态，禁止自动重放。
			id = fmt.Sprintf("anonymous:%d", len(tools))
		}
		if _, ok := tools[id]; ok {
			return
		}
		name, args := splitToolContent(event.Content)
		tools[id] = &resumeTool{
			ID: id, Name: name, Args: args, Kind: event.ToolKind, Started: time.Now(),
		}
	case executor.EventToolResult:
		id := strings.TrimSpace(event.ToolUseID)
		if id == "" {
			return
		}
		tool := tools[id]
		if tool == nil {
			return
		}
		if text := strings.TrimSpace(event.Content); text != "" {
			tool.Tail = lastLines(text, 20)
		}
		if !event.ToolResultPartial {
			delete(tools, id)
		}
	}
}

func splitToolContent(content string) (string, string) {
	content = strings.TrimSpace(content)
	name, args, _ := strings.Cut(content, " ")
	return name, strings.TrimSpace(args)
}

func lastLines(text string, n int) string {
	lines := strings.Split(text, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func resumeEnabled() bool {
	return os.Getenv("GOTEAMS_RESUME_AUTO") != "0"
}

func renderResumePrompt(locale string, tools map[string]*resumeTool) string {
	var b strings.Builder
	if len(tools) == 0 {
		b.WriteString("（无）")
	}
	for _, tool := range tools {
		args := truncate(tool.Args, 200)
		tail := strings.TrimSpace(tool.Tail)
		if tail == "" {
			tail = "无输出"
		}
		elapsed := time.Since(tool.Started).Round(time.Second)
		risk := ""
		if riskyCommand(tool.Name + " " + tool.Args) {
			risk = "，可能有外部副作用"
		}
		fmt.Fprintf(&b, "- %s %s（已运行 %s%s）\n  最后输出：%s\n", tool.Name, args, elapsed, risk, truncate(tail, 500))
	}
	return i18n.FormatWithLocale(locale, "cli_resume_prompt", i18n.Params{"Tools": strings.TrimRight(b.String(), "\n")})
}

func riskyCommand(text string) bool {
	lower := strings.ToLower(text)
	for _, prefix := range []string{
		"git push", "rm ", "rm\t", "mv ", "npm publish", "docker push",
		"kubectl", "terraform apply", "migrate", "deploy", "release",
	} {
		if strings.Contains(lower, prefix) {
			return true
		}
	}
	return false
}

func unattendedExecution(mode string) bool {
	switch mode {
	case "pipeline", "expert_group":
		return true
	default:
		return false
	}
}

// workspaceFingerprint 只用于续跑日志，不回滚工作区。
func (o *Orchestrator) emitInterruptedTools(sessionUUID string, seq *int, tools map[string]*resumeTool, attempt int) {
	if o == nil || len(tools) == 0 {
		return
	}
	store := log.NewEventStore(o.db)
	for _, tool := range tools {
		if strings.TrimSpace(tool.ID) == "" {
			continue
		}
		*seq++
		event := executor.ExecutorEvent{
			Type:                  executor.EventToolResult,
			ToolUseID:             tool.ID,
			ToolKind:              tool.Kind,
			Content:               tool.Tail,
			ToolResultInterrupted: true,
			InterruptedAttempt:    attempt,
			Timestamp:             nowMillis(),
		}
		if o.eventBroadcaster != nil {
			o.eventBroadcaster(sessionUUID, *seq, event)
		}
		payload, persist, err := marshalSessionEvent(event)
		if err != nil || !persist {
			continue
		}
		if appendErr := store.Append(log.EventLogEntry{
			SessionUUID: sessionUUID,
			Sequence:    *seq,
			EventType:   event.Type,
			Payload:     string(payload),
		}); appendErr != nil {
			applog.Warn("[Orchestrator] 写入中断工具结果失败", "session", sessionUUID, "error", appendErr)
		}
	}
}

func workspaceFingerprint(dir string) string {
	if strings.TrimSpace(dir) == "" {
		return ""
	}
	head, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	status, err := exec.Command("git", "-C", dir, "status", "--porcelain=v1", "-z").Output()
	if err != nil {
		return strings.TrimSpace(string(head))
	}
	return strings.TrimSpace(string(head)) + ":" + fmt.Sprintf("%d", len(status))
}
