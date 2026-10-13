// Package mcpbridge exposes the existing local task-skill API as four MCP tools.
// The process speaks JSON-RPC on stdout and does not start the desktop client.
package mcpbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"goteams-client/internal/workspace"
)

const (
	skillClientHeader = "X-GoTeams-Skill-Client"
	skillClientValue  = "teamsboard-v1"
	maxBodyBytes      = 2 << 20
	maxContentRunes   = 64 * 1024
	maxThreadRunes    = 256
	connectTimeout    = 5 * time.Second
	requestTimeout    = 15 * time.Second
)

// Bridge forwards MCP tool calls to the local task-skill HTTP API.
type Bridge struct {
	version     string
	runtimePath func() string
	client      *http.Client
	process     context.Context
}

// New returns a bridge that reads the stable runtime.json used by the skill scripts.
func New(version string) *Bridge {
	if strings.TrimSpace(version) == "" {
		version = "dev"
	}
	return &Bridge{
		version:     version,
		runtimePath: defaultRuntimePath,
		client:      newHTTPClient(requestTimeout),
	}
}

func defaultRuntimePath() string {
	return filepath.Join(workspace.DefaultDataRoot(), "runtime", "runtime.json")
}

func newHTTPClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: connectTimeout}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:       nil,
			DialContext: dialer.DialContext,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// Run serves MCP on stdio until the client disconnects or the process is signalled.
// Diagnostics go to stderr. Stdout is reserved for JSON-RPC.
func Run(ctx context.Context, version string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sig)
	go func() {
		select {
		case <-sig:
			cancel()
		case <-ctx.Done():
			return
		}
		<-sig
		os.Exit(1)
	}()
	bridge := New(version)
	bridge.process = ctx
	return bridge.serve(ctx, &mcp.StdioTransport{})
}

func requestContext(handler, process context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(handler)
	if process == nil {
		return ctx, cancel
	}
	stop := context.AfterFunc(process, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}
func (b *Bridge) serve(ctx context.Context, transport mcp.Transport) error {
	server := mcp.NewServer(&mcp.Implementation{Name: "teamsboard", Version: b.version}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "teamsboard_get_task",
		Description: "Read one local TeamsBoard Vibe Coding task and its history.",
	}, b.getTask)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "teamsboard_bind_session",
		Description: "Bind the current native session to one local TeamsBoard Vibe Coding task.",
	}, b.bindSession)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "teamsboard_update_status",
		Description: "Update the status of one local TeamsBoard Vibe Coding task.",
	}, b.updateStatus)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "teamsboard_report_activity",
		Description: "Report one user message or final reply to a local TeamsBoard Vibe Coding task.",
	}, b.reportActivity)
	return server.Run(ctx, transport)
}

type getInput struct {
	TaskUUID string `json:"task_uuid" jsonschema:"TeamsBoard task UUID"`
	Language string `json:"language,omitempty" jsonschema:"optional zh-CN or en-US"`
}

type bindInput struct {
	TaskUUID       string `json:"task_uuid" jsonschema:"TeamsBoard task UUID"`
	ExecutionIndex *int   `json:"execution_index" jsonschema:"execution index from the launch prompt"`
	Type           string `json:"type" jsonschema:"tool type from the launch prompt"`
	ThreadID       string `json:"thread_id" jsonschema:"current native session ID"`
	Language       string `json:"language,omitempty" jsonschema:"optional zh-CN or en-US"`
}

type statusInput struct {
	TaskUUID       string `json:"task_uuid" jsonschema:"TeamsBoard task UUID"`
	ExecutionIndex *int   `json:"execution_index" jsonschema:"execution index from the launch prompt"`
	Status         string `json:"status" jsonschema:"pending, in_progress, blocked, or done"`
	Tool           string `json:"tool" jsonschema:"tool type from the launch prompt"`
	ThreadID       string `json:"thread_id" jsonschema:"bound native session ID"`
	Language       string `json:"language,omitempty" jsonschema:"optional zh-CN or en-US"`
}

type activityInput struct {
	TaskUUID       string `json:"task_uuid" jsonschema:"TeamsBoard task UUID"`
	ExecutionIndex *int   `json:"execution_index" jsonschema:"execution index from the launch prompt"`
	EventID        string `json:"event_id" jsonschema:"UUID for this activity; reuse it when retrying"`
	Type           string `json:"type" jsonschema:"question or result"`
	Content        string `json:"content" jsonschema:"activity text"`
	Tool           string `json:"tool" jsonschema:"tool type from the launch prompt"`
	ThreadID       string `json:"thread_id" jsonschema:"bound native session ID"`
	Language       string `json:"language,omitempty" jsonschema:"optional zh-CN or en-US"`
}

func (b *Bridge) getTask(ctx context.Context, _ *mcp.CallToolRequest, in getInput) (*mcp.CallToolResult, any, error) {
	id, err := taskID(in.TaskUUID)
	if err != nil {
		return toolError("invalid_argument", err.Error())
	}
	if err = languageOK(in.Language); err != nil {
		return toolError("invalid_argument", err.Error())
	}
	return b.forward(ctx, http.MethodGet, id, "", in.Language, nil)
}

func (b *Bridge) bindSession(ctx context.Context, _ *mcp.CallToolRequest, in bindInput) (*mcp.CallToolResult, any, error) {
	id, err := taskID(in.TaskUUID)
	if err != nil {
		return toolError("invalid_argument", err.Error())
	}
	if err = languageOK(in.Language); err != nil || in.ExecutionIndex == nil || *in.ExecutionIndex < 0 {
		return toolError("invalid_argument", "execution_index and language must be valid")
	}
	in.Type = strings.ToLower(strings.TrimSpace(in.Type))
	in.ThreadID = strings.TrimSpace(in.ThreadID)
	if in.Type == "" || in.ThreadID == "" || utf8.RuneCountInString(in.ThreadID) > maxThreadRunes {
		return toolError("invalid_argument", "type and thread_id are required")
	}
	body := map[string]any{"execution_index": *in.ExecutionIndex, "type": in.Type, "thread_id": in.ThreadID}
	return b.forward(ctx, http.MethodPut, id, "/vibe-session", in.Language, body)
}

func (b *Bridge) updateStatus(ctx context.Context, _ *mcp.CallToolRequest, in statusInput) (*mcp.CallToolResult, any, error) {
	id, err := taskID(in.TaskUUID)
	if err != nil {
		return toolError("invalid_argument", err.Error())
	}
	if err = languageOK(in.Language); err != nil || in.ExecutionIndex == nil || *in.ExecutionIndex < 0 {
		return toolError("invalid_argument", "execution_index and language must be valid")
	}
	in.Status = strings.TrimSpace(in.Status)
	switch in.Status {
	case "pending", "in_progress", "blocked", "done":
	default:
		return toolError("invalid_argument", "status is invalid")
	}
	in.Tool = strings.ToLower(strings.TrimSpace(in.Tool))
	in.ThreadID = strings.TrimSpace(in.ThreadID)
	if in.Tool == "" || in.ThreadID == "" || utf8.RuneCountInString(in.ThreadID) > maxThreadRunes {
		return toolError("invalid_argument", "tool and thread_id are required")
	}
	body := map[string]any{
		"execution_index": *in.ExecutionIndex, "status": in.Status, "tool": in.Tool, "thread_id": in.ThreadID,
	}
	return b.forward(ctx, http.MethodPut, id, "/status", in.Language, body)
}

func (b *Bridge) reportActivity(ctx context.Context, _ *mcp.CallToolRequest, in activityInput) (*mcp.CallToolResult, any, error) {
	id, err := taskID(in.TaskUUID)
	if err != nil {
		return toolError("invalid_argument", err.Error())
	}
	if err = languageOK(in.Language); err != nil || in.ExecutionIndex == nil || *in.ExecutionIndex < 0 {
		return toolError("invalid_argument", "execution_index and language must be valid")
	}
	if _, err = uuid.Parse(strings.TrimSpace(in.EventID)); err != nil {
		return toolError("invalid_argument", "event_id must be a UUID")
	}
	in.Type = strings.TrimSpace(in.Type)
	if in.Type != "question" && in.Type != "result" {
		return toolError("invalid_argument", "type must be question or result")
	}
	in.Content = strings.TrimSpace(in.Content)
	if in.Content == "" || utf8.RuneCountInString(in.Content) > maxContentRunes {
		return toolError("invalid_argument", "content is empty or too long")
	}
	in.Tool = strings.ToLower(strings.TrimSpace(in.Tool))
	in.ThreadID = strings.TrimSpace(in.ThreadID)
	if in.Tool == "" || in.ThreadID == "" || utf8.RuneCountInString(in.ThreadID) > maxThreadRunes {
		return toolError("invalid_argument", "tool and thread_id are required")
	}
	body := map[string]any{
		"execution_index": *in.ExecutionIndex, "event_id": strings.TrimSpace(in.EventID),
		"type": in.Type, "content": in.Content, "tool": in.Tool, "thread_id": in.ThreadID,
	}
	return b.forward(ctx, http.MethodPost, id, "/activities", in.Language, body)
}

func (b *Bridge) forward(ctx context.Context, method, taskID, suffix, language string, body any) (*mcp.CallToolResult, any, error) {
	address, err := readAddress(b.runtimePath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "teamsboard mcp: runtime address unavailable\n")
		return toolError("teamsboard_unavailable", "local TeamsBoard address is unavailable")
	}
	var payload io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return toolError("invalid_argument", "request body is invalid")
		}
		payload = bytes.NewReader(raw)
	}
	reqCtx, stop := requestContext(ctx, b.process)
	defer stop()
	url := "http://" + address + "/api/local/task-skill/tasks/" + taskID + suffix
	req, err := http.NewRequestWithContext(reqCtx, method, url, payload)
	if err != nil {
		return toolError("teamsboard_unavailable", "local request was not sent")
	}
	req.Header.Set(skillClientHeader, skillClientValue)
	if body != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	if language != "" {
		req.Header.Set("X-GoTeams-Lang", language)
	}
	resp, err := b.client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "teamsboard mcp: local request failed\n")
		return toolError("teamsboard_unavailable", "local TeamsBoard did not respond")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return toolError("teamsboard_unavailable", "local response was not read")
	}
	if len(data) > maxBodyBytes {
		return toolError("teamsboard_unavailable", "local response is too large")
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}, nil, nil
	}
	code := statusCode(resp.StatusCode, suffix)
	fmt.Fprintf(os.Stderr, "teamsboard mcp: %s status %d\n", code, resp.StatusCode)
	return toolError(code, "local TeamsBoard rejected the request")
}

func statusCode(status int, suffix string) string {
	switch status {
	case http.StatusBadRequest:
		if suffix == "/vibe-session" {
			return "bind_rejected"
		}
		return "invalid_argument"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusNotFound:
		return "task_not_found"
	case http.StatusConflict:
		return "conflict"
	default:
		return "teamsboard_unavailable"
	}
}

func toolError(code, detail string) (*mcp.CallToolResult, any, error) {
	return nil, nil, fmt.Errorf("%s: %s", code, detail)
}

func taskID(value string) (string, error) {
	id, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		return "", fmt.Errorf("task_uuid must be a UUID")
	}
	return id.String(), nil
}

func languageOK(value string) error {
	switch strings.TrimSpace(value) {
	case "", "zh-CN", "en-US":
		return nil
	default:
		return fmt.Errorf("language must be zh-CN or en-US")
	}
}

func readAddress(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var info struct {
		Address string `json:"address"`
	}
	if err := json.Unmarshal(data, &info); err != nil {
		return "", err
	}
	address := strings.TrimSpace(info.Address)
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return "", err
	}
	if host != "127.0.0.1" && host != "::1" {
		return "", fmt.Errorf("not loopback")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf("invalid port")
	}
	return address, nil
}
