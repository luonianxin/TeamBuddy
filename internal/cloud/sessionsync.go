package cloud

import (
	"context"
	"fmt"
	"net/http"

	"goteams-client/internal/applog"
)

// PipelineTaskSessionSync is the step-level session projection pushed to the cloud:
// one row per step carrying the step's full conversation JSON and the aggregated run
// result. The cloud upserts by (admin_id, pipeline_task_id, step_key) and guards
// against out-of-order pushes via client_updated_at.
type PipelineTaskSessionSync struct {
	LocalTaskUUID      string                          `json:"local_task_uuid"`
	ExecutionIndex     int                             `json:"execution_index"`
	StepKey            string                          `json:"step_key"`
	StepName           string                          `json:"step_name"`
	Status             string                          `json:"status"`
	CliType            string                          `json:"cli_type"`
	ModelName          string                          `json:"model_name"`
	PromptSnapshot     string                          `json:"prompt_snapshot"`
	ResultStatus       string                          `json:"result_status"`
	FinalResult        string                          `json:"final_result"`
	ExitCode           int                             `json:"exit_code"`
	ErrorCode          string                          `json:"error_code"`
	ErrorMessage       string                          `json:"error_message"`
	InputTokens        int64                           `json:"input_tokens"`
	OutputTokens       int64                           `json:"output_tokens"`
	TotalTokens        int64                           `json:"total_tokens"`
	ConversationRounds int                             `json:"conversation_rounds"`
	Conversation       PipelineTaskConversationContent `json:"conversation"`
	// Best-effort execution statistics aggregated over the step's sessions.
	ToolCallCount   int   `json:"tool_call_count"`
	FilesChanged    int   `json:"files_changed"`
	LinesAdded      int   `json:"lines_added"`
	LinesDeleted    int   `json:"lines_deleted"`
	StartedAt       int64 `json:"started_at"`
	FinishedAt      int64 `json:"finished_at"`
	DurationMs      int64 `json:"duration_ms"`
	CreatedAt       int64 `json:"created_at"`
	ClientUpdatedAt int64 `json:"client_updated_at"`
}

// PushPipelineTaskSession acknowledges only a valid accepted/duplicate response.
// HTTP 200 with a business error, an empty body, or malformed JSON remains pending.
func (c *Client) PushPipelineTaskSession(ctx context.Context, payload PipelineTaskSessionSync) error {
	var result struct {
		Result string `json:"result"`
	}
	err := c.do(ctx, http.MethodPost, "/api/client/pipeline-tasks/sessions", payload, &result)
	if err != nil {
		applog.Error("[CloudSync] 推送步骤会话失败", "云端地址", c.BaseURL(), "task_uuid", payload.LocalTaskUUID, "step_key", payload.StepKey, "error", err.Error())
		return fmt.Errorf("请求云端失败: %w", err)
	}
	if result.Result != "accepted" && result.Result != "duplicate" {
		return fmt.Errorf("session upload was not accepted: %s", result.Result)
	}
	return nil
}
