package localserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"goteams-client/internal/applog"
	"goteams-client/internal/executor"
	"goteams-client/internal/i18n"
	builtinskills "goteams-client/internal/skills"
	"goteams-client/internal/taskruntime"
	"goteams-client/internal/workflow"
)

type wechatTask struct {
	UUID            string
	ShortID         int64
	Title           string
	Status          string
	Mode            string
	Tool            string
	CurrentStepUUID string
	SessionJSON     string
}

func (w *wechatRemote) ensureAliases(ctx context.Context) error {
	_, err := w.db.ExecContext(ctx, `INSERT OR IGNORE INTO gt_remote_task_aliases(task_uuid, created_at)
		SELECT t.uuid, ? FROM gt_tasks t
		WHERE NOT EXISTS (SELECT 1 FROM gt_remote_task_aliases a WHERE a.task_uuid=t.uuid)
		ORDER BY t.created_at, t.rowid`, time.Now().UnixMilli())
	return err
}

func (w *wechatRemote) taskByShortID(ctx context.Context, shortID int64) (wechatTask, error) {
	var task wechatTask
	err := w.db.QueryRowContext(ctx, `SELECT t.uuid, a.short_id, t.title, t.status, t.execution_mode,
		t.execution_tool, t.current_step_uuid, t.vibe_coding_session_json
		FROM gt_remote_task_aliases a JOIN gt_tasks t ON t.uuid=a.task_uuid WHERE a.short_id=?`, shortID).
		Scan(&task.UUID, &task.ShortID, &task.Title, &task.Status, &task.Mode, &task.Tool, &task.CurrentStepUUID, &task.SessionJSON)
	return task, err
}

func (w *wechatRemote) taskByUUID(ctx context.Context, taskUUID string) (wechatTask, error) {
	var task wechatTask
	err := w.db.QueryRowContext(ctx, `SELECT t.uuid, a.short_id, t.title, t.status, t.execution_mode,
		t.execution_tool, t.current_step_uuid, t.vibe_coding_session_json
		FROM gt_remote_task_aliases a JOIN gt_tasks t ON t.uuid=a.task_uuid WHERE t.uuid=?`, taskUUID).
		Scan(&task.UUID, &task.ShortID, &task.Title, &task.Status, &task.Mode, &task.Tool, &task.CurrentStepUUID, &task.SessionJSON)
	return task, err
}

func (w *wechatRemote) listTasks(ctx context.Context, status string) (string, error) {
	if err := w.ensureAliases(ctx); err != nil {
		return "", err
	}
	query := `SELECT a.short_id, t.title FROM gt_remote_task_aliases a JOIN gt_tasks t ON t.uuid=a.task_uuid`
	args := []any{}
	if status == "not_done" {
		query += ` WHERE t.status != 'done'`
	} else if status != "" {
		query += ` WHERE t.status=?`
		args = append(args, status)
	}
	query += ` ORDER BY a.short_id`
	rows, err := w.db.QueryContext(ctx, query, args...)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var id int64
		var title string
		if err = rows.Scan(&id, &title); err != nil {
			return "", err
		}
		lines = append(lines, fmt.Sprintf("@%d %s", id, title))
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	labelKey := map[string]string{"active": "remote_wechat_status_running", "done": "remote_wechat_status_done", "blocked": "remote_wechat_status_blocked", "not_done": "remote_wechat_status_not_done", "": "remote_wechat_status_all"}[status]
	params := i18n.Params{"Count": len(lines), "Status": w.tr(ctx, labelKey, nil), "Tasks": strings.Join(lines, "\n\n"), "Help": w.tr(ctx, "remote_wechat_help", nil)}
	if len(lines) == 0 {
		return w.tr(ctx, "remote_wechat_empty_tasks", params), nil
	}
	return w.tr(ctx, "remote_wechat_list", params), nil
}

func (w *wechatRemote) latestFinal(ctx context.Context, task wechatTask) (string, error) {
	var result string
	var err error
	if task.Mode == taskruntime.ExecutionModeVibeCoding {
		err = w.db.QueryRowContext(ctx, `SELECT final_result FROM gt_task_progress WHERE task_uuid=?
			AND execution_mode='vibe_coding' AND record_type<>'user_question' AND final_result<>''
			AND activity_kind<>'assignment' AND uuid<>?
			ORDER BY created_at DESC, rowid DESC LIMIT 1`, task.UUID, vibeCodingAssignmentProgressUUID(task.UUID)).Scan(&result)
	} else {
		err = w.db.QueryRowContext(ctx, `SELECT final_result FROM gt_cli_sessions WHERE task_uuid=?
			AND status IN ('success','failed','stopped','interrupted') AND final_result<>''
			ORDER BY created_at DESC, rowid DESC LIMIT 1`, task.UUID).Scan(&result)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return w.tr(ctx, "remote_wechat_last_none", nil), nil
	}
	return strings.TrimSpace(result), err
}

func (w *wechatRemote) processInbox(ctx context.Context) error {
	w.dispatchMu.RLock()
	defer w.dispatchMu.RUnlock()
	if w.pendingFinish != nil {
		return w.flushPendingFinish(ctx)
	}
	if _, err := w.readState(ctx); errors.Is(err, sql.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	var id, content string
	err := w.db.QueryRowContext(ctx, `SELECT message_id, text_content FROM gt_remote_wechat_inbox
		WHERE status='pending' ORDER BY received_at, rowid LIMIT 1`).Scan(&id, &content)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	// Mark first so a restart cannot dispatch the same remote command twice.
	result, err := w.db.ExecContext(ctx, `UPDATE gt_remote_wechat_inbox SET status='processing', updated_at=?
		WHERE message_id=? AND status='pending'`, time.Now().UnixMilli(), id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return err
	}
	if len([]rune(content)) > 16000 {
		w.pendingFinish = &wechatPendingFinish{id: id, status: "done", reply: w.tr(ctx, "remote_wechat_message_too_long", nil)}
		return w.flushPendingFinish(ctx)
	}
	reply, taskUUID, sessionUUID, dispatchErr := w.handleMessage(ctx, id, content)
	status := "done"
	if errors.Is(dispatchErr, errCodexOutcomeUnknown) {
		status = "uncertain"
		reply = w.tr(ctx, "remote_wechat_dispatch_uncertain", nil)
		dispatchErr = nil
	}
	if dispatchErr != nil {
		reply = w.tr(ctx, "remote_wechat_processing_failed", nil)
	}
	if sessionUUID != "" {
		status = "dispatched"
	}
	w.pendingFinish = &wechatPendingFinish{id: id, status: status, taskUUID: taskUUID, sessionUUID: sessionUUID, reply: reply}
	return w.flushPendingFinish(ctx)
}

func (w *wechatRemote) flushPendingFinish(ctx context.Context) error {
	pending := w.pendingFinish
	if err := w.finishProcessing(ctx, pending.id, pending.status, pending.taskUUID, pending.sessionUUID, pending.reply); err != nil {
		return err
	}
	w.pendingFinish = nil
	return nil
}

func (w *wechatRemote) finishProcessing(ctx context.Context, id, status, taskUUID, sessionUUID, reply string) error {
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE gt_remote_wechat_inbox SET status=?, task_uuid=?, session_uuid=?, updated_at=?
		WHERE message_id=? AND status='processing'`, status, taskUUID, sessionUUID, time.Now().UnixMilli(), id)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated == 0 {
		return nil
	}
	if reply != "" {
		if err := w.enqueueTx(ctx, tx, "reply:"+id, taskUUID, reply); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (w *wechatRemote) finishDispatched(ctx context.Context, id, taskUUID, reply string) error {
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE gt_remote_wechat_inbox SET status='done', updated_at=?
		WHERE message_id=? AND status='dispatched'`, time.Now().UnixMilli(), id)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated == 0 {
		return nil
	}
	if err := w.enqueueTx(ctx, tx, "result:"+id, taskUUID, reply); err != nil {
		return err
	}
	return tx.Commit()
}

func (w *wechatRemote) handleMessage(ctx context.Context, messageID, raw string) (reply, taskUUID, sessionUUID string, err error) {
	content := strings.TrimSpace(raw)
	command, params := content, ""
	if split := strings.IndexByte(content, ' '); split >= 0 {
		command, params = content[:split], strings.TrimSpace(content[split+1:])
	}
	// /help 命令显示所有可用命令
	if command == "/help" || command == "/h" || command == "/H" {
		reply = w.tr(ctx, "remote_wechat_help_full", nil)
		return reply, "", "", nil
	}
	// 计划 §五 / §八 B4：本地快路径指令（共享 SkillSurface 业务实现）。
	// 覆盖 /task /t /T、/exit /q /退出、/c /cancel /取消、/r /resume /继续、
	// /d /done /完成；其余与 SkillSurface 共用的命令见 handleFastPathCommand。
	if handled, r := w.handleFastPathCommand(ctx, command, params); handled {
		return r, "", "", nil
	}
	if err = w.ensureAliases(ctx); err != nil {
		return
	}
	// S-WECHAT-SESSION: 阶段 2 - 二次确认：state 中任意 pending_xxx_task_uuid 非空时
	// 优先判断当前消息是否为确认关键词（/c /r /d 共用 yes / y / 是 / 对，/c 额外支持 取消）。
	// - 确认 → 命中对应动作立即执行并返回；
	// - 不是确认 → 清空 state 中所有 pending 字段，让后续指令判断、意图判断、
	//   任务处理和 CLI 兜底按正常流程进行，避免在二次确认期间阻断其他操作。
	var state wechatState
	readStateOnce := func() {
		s, readErr := w.readState(ctx)
		if readErr == nil {
			state = s
		}
	}
	readStateOnce()
	if handled, reply, rhTask, rhSession, rhErr := w.handlePendingConfirmations(ctx, &state, content); handled {
		return reply, rhTask, rhSession, rhErr
	}
	var task wechatTask
	if strings.HasPrefix(content, "@") {
		payload := content[1:]
		// 单独输入 @ 列出非完成的任务
		if payload == "" || strings.TrimSpace(payload) == "" {
			if w.skillSurface == nil {
				reply, err = w.listTasks(ctx, "not_done")
				return reply, "", "", err
			}
			out, listErr := w.skillSurface.WechatListTasks(w.wechatCtx(ctx), "not_done")
			return out, "", "", listErr
		}
		idText, instruction := payload, ""
		if split := strings.IndexFunc(payload, unicode.IsSpace); split >= 0 {
			idText, instruction = payload[:split], strings.TrimSpace(payload[split:])
		}
		shortID, parseErr := strconv.ParseInt(idText, 10, 64)
		if parseErr != nil || shortID < 1 {
			return w.tr(ctx, "remote_wechat_id_invalid", i18n.Params{"Help": w.tr(ctx, "remote_wechat_help", nil)}), "", "", nil
		}
		task, err = w.taskByShortID(ctx, shortID)
		if errors.Is(err, sql.ErrNoRows) {
			return w.tr(ctx, "remote_wechat_task_missing", i18n.Params{"Help": w.tr(ctx, "remote_wechat_help", nil)}), "", "", nil
		}
		if err != nil {
			return
		}
		if _, err = w.db.ExecContext(ctx, `UPDATE gt_remote_wechat_state SET bound_task_uuid=?,
			pending_cancel_task_uuid='', pending_resume_task_uuid='', pending_done_task_uuid='',
			updated_at=? WHERE id=1`, task.UUID, time.Now().UnixMilli()); err != nil {
			return
		}
		if instruction == "" {
			last, lastErr := w.latestFinal(ctx, task)
			if lastErr != nil {
				err = lastErr
				return
			}
			return w.tr(ctx, "remote_wechat_switch", i18n.Params{"Task": task.Title, "Result": last}), task.UUID, "", nil
		}
		content = instruction
	} else {
		// S-NL-CLI: 先在 GO 端做轻量级自然语言识别
		// 简单管理类查询直接处理，避免每次都启动 CLI 进程
		// 已绑定任务（boundTaskUUID != ""）时只允许第 1 轮严格匹配生效，
		// 第 2 / 3 轮宽松匹配在 matchNaturalLanguageIntent 内部跳过，避免影响任务对话。
		if matched, intent, intentParam := w.matchNaturalLanguageIntent(content, state.BoundTaskUUID); matched {
			// 「强制 / 直接 / -f / force」关键词：仅影响需要二次确认的意图（目前只有 nlIntentCancelTask）；
			// 其它意图（resume / complete / pause / stop）已走 changeTaskStatusByShortID 直写，无确认语义。
			force := containsForceKeyword(content)
			return w.handleNaturalLanguageIntent(ctx, state, intent, intentParam, force)
		}
		// S-WECHAT-SESSION: 已绑定任务时，消息直接转发到任务，不进入 CLI 兜底。
		// 只有在没有绑定任务且无法被自然语言识别时，才走 CLI 兜底处理。
		if state.BoundTaskUUID == "" {
			// S-NL-CLI: 无法识别为已知管理操作时，检查是否配置了 CLI 兜底处理。
			// 区分三种情况：已配置并启用 → 走 CLI；已配置但关闭 → 引导开启开关；未配置 → 引导配置。
			cliConfig := w.readChannelCLIConfig(ctx, "wechat")
			if cliConfig.CLICType != "" && cliConfig.Enabled {
				// 有 CLI 配置时，自动创建一个独立任务并交给 CLI 处理，确保工作留痕
				cliTaskUUID, cliSessionUUID, cliErr := w.dispatchToCLI(ctx, messageID, content, state.BoundTaskUUID, cliConfig.CLICType, cliConfig.CLIModel)
				if cliErr != nil {
					applog.Warn("微信消息转发CLI失败", "message_id", messageID, "error", cliErr.Error())
					return w.tr(ctx, "remote_wechat_processing_failed", i18n.Params{
						"Detail": w.localizedDetail(ctx, cliErr.Error()),
						"Help":   w.tr(ctx, "remote_wechat_help", nil),
					}), "", "", nil
				}
				if cliSessionUUID != "" {
					// 兜底任务已创建并启动，告知用户任务编号与处理的 CLI
					if aliasErr := w.ensureAliases(ctx); aliasErr != nil {
						applog.Warn("兜底任务建立 short_id 失败", "task_uuid", cliTaskUUID, "error", aliasErr.Error())
					}
					var shortID int64
					_ = w.db.QueryRowContext(ctx, `SELECT short_id FROM gt_remote_task_aliases WHERE task_uuid=?`, cliTaskUUID).Scan(&shortID)
					ackReply := w.tr(ctx, "remote_wechat_cli_fallback_ack", i18n.Params{"ID": shortID, "CLI": cliConfig.CLICType})
					return ackReply, cliTaskUUID, cliSessionUUID, nil
				}
				// 兼容：返回空 session 时也按兜底处理流程向用户回执
				return "", state.BoundTaskUUID, "", nil
			}
			// S-NL-CLI-fix: 无 CLI 配置时，引导用户在通道页面配置 CLI 作为兜底处理。
			// CLI 配置与连接状态无关，可随时在远程通道页面配置后由通道消息触发使用。
			if cliConfig.CLICType != "" {
				// 已配置但总开关关闭：明确告诉用户去开启开关，而不是建议去配置。
				return w.tr(ctx, "remote_wechat_cli_disabled_for_dispatch", i18n.Params{"Help": w.tr(ctx, "remote_wechat_help", nil)}), "", "", nil
			}
			return w.tr(ctx, "remote_wechat_cli_unconfigured_for_dispatch", i18n.Params{"Help": w.tr(ctx, "remote_wechat_help", nil)}), "", "", nil
		}
		// 已绑定任务：直接加载任务上下文，进入与正常消息相同的派发流程
		task, err = w.taskByUUID(ctx, state.BoundTaskUUID)
		if errors.Is(err, sql.ErrNoRows) {
			// 绑定任务已不存在：清除绑定与全部 pending，然后再次检查是否需要 CLI 兜底
			_, _ = w.db.ExecContext(ctx, `UPDATE gt_remote_wechat_state SET bound_task_uuid='',
				pending_cancel_task_uuid='', pending_resume_task_uuid='', pending_done_task_uuid='' WHERE id=1`)
			readStateOnce()
			if err != nil {
				return
			}
			cliConfig := w.readChannelCLIConfig(ctx, "wechat")
			if cliConfig.CLICType != "" && cliConfig.Enabled {
				cliTaskUUID, cliSessionUUID, cliErr := w.dispatchToCLI(ctx, messageID, content, "", cliConfig.CLICType, cliConfig.CLIModel)
				if cliErr != nil {
					applog.Warn("微信消息转发CLI失败", "message_id", messageID, "error", cliErr.Error())
					return w.tr(ctx, "remote_wechat_processing_failed", i18n.Params{
						"Detail": w.localizedDetail(ctx, cliErr.Error()),
						"Help":   w.tr(ctx, "remote_wechat_help", nil),
					}), "", "", nil
				}
				if cliSessionUUID != "" {
					if aliasErr := w.ensureAliases(ctx); aliasErr != nil {
						applog.Warn("兜底任务建立 short_id 失败", "task_uuid", cliTaskUUID, "error", aliasErr.Error())
					}
					var shortID int64
					_ = w.db.QueryRowContext(ctx, `SELECT short_id FROM gt_remote_task_aliases WHERE task_uuid=?`, cliTaskUUID).Scan(&shortID)
					ackReply := w.tr(ctx, "remote_wechat_cli_fallback_ack", i18n.Params{"ID": shortID, "CLI": cliConfig.CLICType})
					return ackReply, cliTaskUUID, cliSessionUUID, nil
				}
				return "", "", "", nil
			}
			return w.tr(ctx, "remote_wechat_task_missing", i18n.Params{"Help": w.tr(ctx, "remote_wechat_help", nil)}), "", "", nil
		}
		if err != nil {
			return
		}
	}
	if block, blockErr := remoteExecutionBlock(ctx, w.db, task.UUID); blockErr != nil {
		err = blockErr
		return
	} else if block == "uncertain" {
		return w.tr(ctx, "remote_wechat_uncertain_blocked", nil), task.UUID, "", nil
	}
	busy, busyErr := w.taskBusy(ctx, task.UUID)
	if busyErr != nil {
		err = busyErr
		return
	}
	if busy {
		return w.tr(ctx, "remote_wechat_busy", i18n.Params{"Task": task.Title}), task.UUID, "", nil
	}
	if err = w.markProcessingTask(ctx, messageID, task.UUID); err != nil {
		return "", task.UUID, "", err
	}
	sessionUUID, err = w.dispatchTask(ctx, task, messageID, content)
	if errors.Is(err, errCodexOutcomeUnknown) {
		applog.Warn("微信指令下发结果不确定", "task_uuid", task.UUID, "category", "uncertain", "stage", "codex_turn_start")
		return "", task.UUID, "", err
	}
	if err != nil {
		return w.dispatchErrorReply(ctx, task, err), task.UUID, "", nil
	}
	return w.tr(ctx, "remote_wechat_send_ack", i18n.Params{"Task": task.Title, "Message": content}), task.UUID, sessionUUID, nil
}

func (w *wechatRemote) dispatchErrorReply(ctx context.Context, task wechatTask, err error) string {
	if errors.Is(err, errCodexBusy) {
		applog.Warn("微信指令下发被活动执行拦截", "task_uuid", task.UUID, "category", "busy", "stage", "execution")
		return w.tr(ctx, "remote_wechat_busy", i18n.Params{"Task": task.Title})
	}
	key, category, stage := "remote_wechat_dispatch_failed", "runtime", "dispatch"
	var failure *wechatDispatchFailure
	if errors.As(err, &failure) {
		key, category, stage = failure.key, failure.category, failure.stage
	}
	applog.Warn("微信指令下发失败", "task_uuid", task.UUID, "category", category, "stage", stage)
	return w.tr(ctx, key, nil)
}

func (w *wechatRemote) taskBusy(ctx context.Context, taskUUID string) (bool, error) {
	var active int
	err := w.db.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM gt_cli_sessions WHERE task_uuid=? AND status IN ('created','running','waiting_input','stop_requested'))
		+ (SELECT COUNT(*) FROM gt_task_progress WHERE task_uuid=? AND dispatch_status='routing_pending')
		+ (SELECT COUNT(*) FROM gt_remote_wechat_inbox WHERE task_uuid=? AND status IN ('dispatched','uncertain'))`, taskUUID, taskUUID, taskUUID).Scan(&active)
	return active > 0, err
}

func (w *wechatRemote) markProcessingTask(ctx context.Context, messageID, taskUUID string) error {
	result, err := w.db.ExecContext(ctx, `UPDATE gt_remote_wechat_inbox SET task_uuid=?, updated_at=?
		WHERE message_id=? AND status='processing'`, taskUUID, time.Now().UnixMilli(), messageID)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated != 1 {
		return errors.New("微信指令已取消")
	}
	return nil
}

func (w *wechatRemote) dispatchTask(ctx context.Context, task wechatTask, messageID, content string) (string, error) {
	if task.Mode == taskruntime.ExecutionModeVibeCoding {
		if task.Tool != taskruntime.ExecutionToolCodex {
			return "", wechatConfigFailure("execution_mode", "remote_wechat_config_mode")
		}
		return w.dispatchCodex(ctx, task, messageID, content)
	}
	if task.Mode != taskruntime.ExecutionModeCLI && task.Mode != taskruntime.ExecutionModePipeline &&
		task.Mode != taskruntime.ExecutionModeExpertGroup {
		return "", wechatConfigFailure("execution_mode", "remote_wechat_config_mode")
	}
	if err := w.validateDispatchWorkspace(ctx, task.UUID); err != nil {
		return "", err
	}
	if task.Mode == taskruntime.ExecutionModeExpertGroup {
		var stepUUID string
		err := w.db.QueryRowContext(ctx, `SELECT leader_task_step_uuid FROM gt_task_expert_group_snapshots WHERE task_uuid=?`,
			task.UUID).Scan(&stepUUID)
		if errors.Is(err, sql.ErrNoRows) || err == nil && stepUUID == "" {
			return "", wechatConfigFailure("expert_group", "remote_wechat_config_expert")
		}
		if err != nil {
			return "", err
		}
		if err := w.validateDispatchStep(ctx, task.UUID, stepUUID); err != nil {
			return "", err
		}
		if w.orchestrator == nil {
			return "", errors.New("任务执行服务不可用")
		}
		return w.orchestrator.StartExpertMessage(ctx, task.UUID, "", content, content, messageID)
	}
	if task.CurrentStepUUID == "" {
		if task.Mode != taskruntime.ExecutionModePipeline {
			return "", wechatConfigFailure("step", "remote_wechat_config_step")
		}
		stepUUID, err := w.preparePipelineMessage(ctx, task)
		if err != nil {
			return "", err
		}
		task.CurrentStepUUID = stepUUID
	}
	if err := w.validateDispatchStep(ctx, task.UUID, task.CurrentStepUUID); err != nil {
		return "", err
	}
	if w.orchestrator == nil {
		return "", errors.New("任务执行服务不可用")
	}
	var parentUUID, parentStatus string
	err := w.db.QueryRowContext(ctx, `SELECT uuid, status FROM gt_cli_sessions WHERE task_uuid=? AND step_uuid=?
		ORDER BY created_at DESC, rowid DESC LIMIT 1`, task.UUID, task.CurrentStepUUID).Scan(&parentUUID, &parentStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return w.orchestrator.RunStep(ctx, workflow.RunStepOptions{TaskUUID: task.UUID, StepUUID: task.CurrentStepUUID,
			UserPrompt: content, DisplayPrompt: content, RecordType: "user_question", RequestID: messageID})
	}
	if err != nil {
		return "", err
	}
	if !workflow.IsTerminalStatus(parentStatus) {
		return "", errors.New("任务仍在执行")
	}
	return w.orchestrator.ContinueConversation(ctx, workflow.ContinueConversationOptions{
		ParentSessionUUID: parentUUID, Prompt: content, DisplayPrompt: content, RequestID: messageID})
}

func (w *wechatRemote) collectResults(ctx context.Context) error {
	rows, err := w.db.QueryContext(ctx, `SELECT message_id, task_uuid, session_uuid FROM gt_remote_wechat_inbox
		WHERE status='dispatched' AND session_uuid NOT LIKE 'codex:%' AND next_recovery_at<=?
		ORDER BY next_recovery_at, received_at LIMIT 16`, time.Now().UnixMilli())
	if err != nil {
		return err
	}
	defer rows.Close()
	type item struct{ id, task, session string }
	var pending []item
	for rows.Next() {
		var v item
		if err = rows.Scan(&v.id, &v.task, &v.session); err != nil {
			return err
		}
		pending = append(pending, v)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	for _, v := range pending {
		var status, final string
		err = w.db.QueryRowContext(ctx, `SELECT status, final_result FROM gt_cli_sessions WHERE uuid=?`, v.session).Scan(&status, &final)
		if errors.Is(err, sql.ErrNoRows) {
			if err := w.finishDispatched(ctx, v.id, v.task, w.tr(ctx, "remote_wechat_result_unavailable", nil)); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if !workflow.IsTerminalStatus(status) {
			if err := w.deferCLIResult(ctx, v.id); err != nil {
				return err
			}
			continue
		}
		var mode, title string
		err = w.db.QueryRowContext(ctx, `SELECT execution_mode,title FROM gt_tasks WHERE uuid=?`, v.task).Scan(&mode, &title)
		if errors.Is(err, sql.ErrNoRows) {
			title = w.tr(ctx, "remote_wechat_task_deleted_title", nil)
		} else if err != nil {
			return err
		}
		if mode == taskruntime.ExecutionModeExpertGroup {
			var chain string
			if err = w.db.QueryRowContext(ctx, `SELECT expert_chain_uuid FROM gt_cli_sessions WHERE uuid=?`, v.session).Scan(&chain); err != nil {
				return err
			}
			busy, busyErr := w.expertChainBusy(ctx, v.task, chain)
			if busyErr != nil {
				return busyErr
			}
			if busy {
				if err := w.deferCLIResult(ctx, v.id); err != nil {
					return err
				}
				continue
			}
			err = w.db.QueryRowContext(ctx, `SELECT final_result FROM gt_cli_sessions WHERE task_uuid=?
				AND expert_chain_uuid=? AND status IN ('success','failed','stopped','interrupted')
				ORDER BY expert_chain_round DESC,created_at DESC,rowid DESC LIMIT 1`, v.task, chain).Scan(&final)
			if err != nil {
				return err
			}
		}
		var shortID int64
		_ = w.db.QueryRowContext(ctx, `SELECT short_id FROM gt_remote_task_aliases WHERE task_uuid=?`, v.task).Scan(&shortID)
		body := w.tr(ctx, "remote_wechat_result", i18n.Params{"ID": shortID, "Task": title, "Result": strings.TrimSpace(final)})
		if err = w.finishDispatched(ctx, v.id, v.task, body); err != nil {
			return err
		}
	}
	return nil
}

func (w *wechatRemote) deferCLIResult(ctx context.Context, messageID string) error {
	_, err := w.db.ExecContext(ctx, `UPDATE gt_remote_wechat_inbox SET next_recovery_at=?
		WHERE message_id=? AND status='dispatched'`, time.Now().Add(5*time.Second).UnixMilli(), messageID)
	return err
}

func (w *wechatRemote) expertChainBusy(ctx context.Context, taskUUID, chain string) (bool, error) {
	var active int
	err := w.db.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM gt_cli_sessions WHERE task_uuid=? AND expert_chain_uuid=?
			AND status IN ('created','running','waiting_input','stop_requested'))
		+ (SELECT COUNT(*) FROM gt_task_progress p JOIN gt_cli_sessions s ON s.uuid=p.session_uuid
			WHERE s.task_uuid=? AND s.expert_chain_uuid=? AND p.dispatch_status='routing_pending')`, taskUUID, chain, taskUUID, chain).Scan(&active)
	return active > 0, err
}

// dispatchToCLI 将消息分派为独立的兜底任务
// 微信通道收到无法识别为已知指令的复杂消息时，自动创建一个 CLI 任务并执行：
// 1. 用消息内容作为任务标题（前 30 字），确保工作留痕
// 2. 工作区使用通道配置的专属目录（默认 ~/goteams/wechat_workspace）
// 3. 走与正常任务一致的 RunStep 流程，自动在「所有任务」中可见
// 返回 (taskUUID, sessionUUID, error)：
//   - taskUUID：自动创建的兜底任务 UUID，用于更新 inbox 跟踪
//   - sessionUUID：RunStep 启动的 CLI 会话 UUID，可被 collectResults 收集结果
func (w *wechatRemote) dispatchToCLI(ctx context.Context, messageID, message, taskUUID, cliType, cliModel string) (string, string, error) {
	if w.orchestrator == nil {
		return "", "", errors.New("任务执行服务不可用")
	}

	// 解析 CLI 可执行文件路径，让用户先在客户端配置 CLI
	execPath, execErr := executor.ResolveCLIExecutable(cliType)
	if execErr != nil || execPath == "" {
		applog.Warn("CLI 兜底失败：未找到可执行文件", "cli_type", cliType, "error", execErr)
		return "", "", fmt.Errorf("未找到 %s CLI，请先在客户端配置正确的 CLI 类型", cliType)
	}
	if _, ok := workflow.NewAdapter(cliType); !ok {
		applog.Warn("CLI 兜底失败：不支持的 CLI 类型", "cli_type", cliType)
		return "", "", fmt.Errorf("不支持的CLI类型: %s", cliType)
	}

	// 确定专属工作区：优先使用通道配置，缺省时落到 ~/goteams/wechat_workspace
	cliConfig := w.readChannelCLIConfig(ctx, "wechat")
	workDir := strings.TrimSpace(cliConfig.Workspace)
	if workDir == "" {
		home, _ := os.UserHomeDir()
		workDir = filepath.Join(home, "goteams", "wechat_workspace")
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		applog.Warn("创建微信工作区失败", "path", workDir, "error", err.Error())
		return "", "", fmt.Errorf("创建微信工作区失败: %w", err)
	}

	// 获取任务上下文（若已绑定）让 CLI 知道当前关联的任务
	var taskTitle, taskStatus string
	if taskUUID != "" {
		_ = w.db.QueryRowContext(ctx, `SELECT title, status FROM gt_tasks WHERE uuid=?`, taskUUID).Scan(&taskTitle, &taskStatus)
	}

	// 构造任务标题：消息前 30 字（截断加省略号），保证留痕可读
	title := strings.TrimSpace(message)
	if runes := []rune(title); len(runes) > 30 {
		title = string(runes[:30]) + "…"
	}
	if title == "" {
		title = "微信指令"
	}

	// 创建任务（service.Create 会自动建 CLI 直接执行步骤并写入 current_step_uuid）
	taskService := taskruntime.NewService(w.db, w.taskRoot)
	newTaskUUID, err := taskService.Create(ctx, taskruntime.CreateInput{
		SourceType:     "local",
		Title:          title,
		Content:        message,
		WorkDirs:       []string{workDir},
		Status:         "active",
		ExecutionMode:  taskruntime.ExecutionModeCLI,
		ExecutionTool:  cliType,
		ExecutionModel: cliModel,
	})
	if err != nil {
		applog.Warn("微信兜底任务创建失败", "cli_type", cliType, "error", err.Error())
		return "", "", fmt.Errorf("创建兜底任务失败: %w", err)
	}

	// 读取 service.Create 自动建立的 step，作为 RunStep 的入口
	var stepUUID string
	if err = w.db.QueryRowContext(ctx, `SELECT current_step_uuid FROM gt_tasks WHERE uuid=?`, newTaskUUID).Scan(&stepUUID); err != nil {
		applog.Warn("读取新建任务 step 失败", "task_uuid", newTaskUUID, "error", err.Error())
		return "", "", fmt.Errorf("读取任务步骤失败: %w", err)
	}
	if stepUUID == "" {
		return "", "", errors.New("新建任务缺少初始步骤")
	}

	// 构造发给 CLI 的 prompt：通过 markdown 本地路径让 CLI 加载 teamsboard-im-bot-cli Skill，
	// 由 Skill 自身描述 API 文档与处理流程，避免与 SKILL.md 内容漂移。
	// Skill 未安装时 prompt 仍按 Skill 名称引导，CLI 会按自身加载机制发现该 Skill。
	userPrompt := message
	displayPrompt := message
	if taskUUID != "" {
		userPrompt = w.buildCLISkillPrompt(ctx, cliType, taskUUID, taskTitle, taskStatus, message)
	}

	// 走正常 RunStep 流程：创建 user_question + initial_run progress，绑定 gt_cli_sessions
	sessionUUID, err := w.orchestrator.RunStep(ctx, workflow.RunStepOptions{
		TaskUUID:      newTaskUUID,
		StepUUID:      stepUUID,
		UserPrompt:    userPrompt,
		DisplayPrompt: displayPrompt,
		RecordType:    "user_question",
		RequestID:     messageID,
		CLIType:       cliType,
		Model:         cliModel,
		WorkDir:       workDir,
	})
	if err != nil {
		applog.Warn("微信兜底任务执行失败", "task_uuid", newTaskUUID, "cli_type", cliType, "error", err.Error())
		return "", "", fmt.Errorf("启动兜底任务失败: %w", err)
	}

	applog.Info("微信兜底任务已创建并启动",
		"task_uuid", newTaskUUID,
		"step_uuid", stepUUID,
		"session_uuid", sessionUUID,
		"cli_type", cliType,
		"cli_model", cliModel,
		"exec_path", execPath,
		"work_dir", workDir,
		"message_id", messageID,
	)
	return newTaskUUID, sessionUUID, nil
}

// buildCLISkillPrompt 通过 markdown 本地路径让 CLI 加载 teamsboard-im-bot-cli Skill，
// 仅注入任务上下文（UUID/标题/状态/用户消息），由 Skill 自身描述 API 文档与处理流程。
// Skill 未安装时 prompt 仍按 Skill 名称引导，CLI 会按自身加载机制发现该 Skill。
// 完整模板见 i18n key localserver_cli_skill_prompt（zh-CN / en-US）。
func (w *wechatRemote) buildCLISkillPrompt(ctx context.Context, cliType, taskUUID, taskTitle, taskStatus, message string) string {
	linkPath := w.imBotCLISkillPath(cliType)
	if linkPath == "" {
		// Skill 未安装或 cliType 未知，按名称引导 CLI 加载，由 CLI 自行决定如何处理。
		return i18n.FormatWithLocale(w.wechatLocale(ctx), "localserver_cli_skill_prompt", i18n.Params{
			"SkillPath":  builtinskills.ImBotCLISkillName,
			"TaskUUID":   taskUUID,
			"TaskTitle":  taskTitle,
			"TaskStatus": taskStatus,
			"Message":    message,
		})
	}
	return i18n.FormatWithLocale(w.wechatLocale(ctx), "localserver_cli_skill_prompt", i18n.Params{
		"SkillPath":  encodeMarkdownLocalPath(linkPath),
		"TaskUUID":   taskUUID,
		"TaskTitle":  taskTitle,
		"TaskStatus": taskStatus,
		"Message":    message,
	})
}

// wechatLocale 读取当前微信通道用户的 locale；读取失败时回退 zh-CN。
func (w *wechatRemote) wechatLocale(ctx context.Context) string {
	state, err := w.readState(ctx)
	if err != nil || strings.TrimSpace(state.Locale) == "" {
		return "zh-CN"
	}
	return state.Locale
}

// 自然语言意图枚举
const (
	nlIntentListTasks    = "list_tasks"        // 列出所有任务
	nlIntentListActive   = "list_active_tasks" // 列出进行中任务
	nlIntentListDone     = "list_done_tasks"   // 列出已完成任务
	nlIntentListBlocked  = "list_blocked"      // 列出阻塞任务
	nlIntentTaskDetail   = "task_detail"       // 查看任务详情
	nlIntentHelp         = "help"              // 帮助
	nlIntentCurrentTask  = "current_task"      // 查看当前绑定任务
	nlIntentSwitchTask   = "switch_task"       // 切换/进入任务
	nlIntentPauseTask    = "pause_task"        // 暂停任务
	nlIntentResumeTask   = "resume_task"       // 恢复任务
	nlIntentStopTask     = "stop_task"         // 直接停止任务（无确认）
	nlIntentCancelTask   = "cancel_task"       // 取消执行当前任务（带二次确认，对应 /c）
	nlIntentExitTask     = "exit_task"         // 退出当前任务（清绑定 + 列任务，对应 /exit）
	nlIntentCompleteTask = "complete_task"     // 完成任务
	nlIntentTaskSummary  = "task_summary"      // 任务最新结果
	// 计划 §五 / §六：本地快路径意图（命中后调 SkillSurface.WechatFormatXxx）
	nlIntentListTeamWork     = "list_team_work"     // /tt
	nlIntentListProjects     = "list_projects"      // /pro
	nlIntentListPipelines    = "list_pipelines"     // /pls
	nlIntentListExpertGroups = "list_expert_groups" // /egs
	nlIntentListCLIs         = "list_clis"          // /clis
	nlIntentListVibeTools    = "list_vibe_tools"    // /vcl
)

// containsForceKeyword 判断用户消息是否包含「强制 / 直接 / -f / force」等跳过二次确认的关键词。
//
// 仅命中纯短语或显式 -f/-force 才算「强制」；普通动词里的「直接」字样（如"直接说"）
// 不应误命中——所以这里用短语匹配，要求至少 2 个汉字或一个完整英文短语。
//
// 用途：让 nlIntentCancelTask 等需要二次确认的意图，自然语言下含"强制"时直接走 SkillSurface。
// 其它意图（resume/complete/pause/stop）已经走 changeTaskStatusByShortID 直写，force 对它们无影响。
func containsForceKeyword(content string) bool {
	lower := strings.ToLower(content)
	// 显式 flag 形式 -f / -force / --force：作为独立 token 出现，避免与短 ID 拼合。
	for _, token := range strings.Fields(lower) {
		if token == "-f" || token == "-force" || token == "--force" {
			return true
		}
	}
	// 中文短语：要求至少 2 个汉字连续出现，避免「直接说」之类误命中。
	for _, p := range []string{"强制", "直接取消", "直接执行", "直接完成", "直接恢复", "跳过确认", "无需确认"} {
		if strings.Contains(content, p) {
			return true
		}
	}
	// 英文短语。
	for _, kw := range []string{"force", "directly", "skip confirm", "no confirm", "without confirm"} {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// matchNaturalLanguageIntent 两轮匹配 + 验证
// 第 1 轮：严格精确匹配（明确指令 + 标准形式 + 纯 ID）
// 第 2 轮：意图关键词提取（提取核心动词 + 限定词 + 数字）
// 任何一轮匹配成功后还会做参数校验，不合法则降级到下一轮
// 两轮都不匹配才返回 false，让调用方走 CLI 兜底
//
// boundTaskUUID 不为空时表示已绑定到具体任务：为了不打扰与任务的正常交谈，
// 只允许第 1 轮严格精确匹配生效，第 2 轮关键词提取直接跳过，
// 让非严格匹配内容回落到任务派发链路。
//
// 进入意图匹配前先做长度过滤：中文 15 字 / 英文 60 字符内的短指令才识别；
// 含任务 ID 时适度放宽到中文 30 字 / 英文 120 字符。
// 超出限制的复杂内容直接走 CLI 兜底，避免把任务原文误判为"列出所有任务"等简单指令。
func (w *wechatRemote) matchNaturalLanguageIntent(content string, boundTaskUUID string) (matched bool, intent string, param string) {
	content = strings.TrimSpace(content)
	if content == "" {
		return false, "", ""
	}
	// 长度过滤：长任务内容（>30字 或 >120字符）直接走 CLI 兜底，避免被误判为简单指令
	hasID := extractTaskID(content) != ""
	if !withinIntentLengthLimit(content, hasID) {
		applog.Debug("自然语言匹配：内容超出意图匹配长度限制，走CLI兜底",
			"rune_count", len([]rune(content)),
			"byte_count", len(content),
			"has_id", hasID,
		)
		return false, "", ""
	}
	lower := strings.ToLower(content)

	// 第 1 轮：严格精确匹配
	if rIntent, rParam, ok := w.matchRound1Strict(lower, content); ok {
		if w.validateMatch(rIntent, rParam) {
			return true, rIntent, rParam
		}
		applog.Debug("自然语言匹配第1轮未通过验证，降级到第2轮", "intent", rIntent, "param", rParam, "content", content)
	}

	// 已绑定到任务时不再走宽松匹配，避免对话内容被误识别为管理类指令。
	if boundTaskUUID != "" {
		return false, "", ""
	}

	// 第 2 轮：意图关键词提取
	if rIntent, rParam, ok := w.matchRound2Keywords(lower, content); ok {
		if w.validateMatch(rIntent, rParam) {
			return true, rIntent, rParam
		}
		applog.Debug("自然语言匹配第2轮未通过验证，跳过自然语言识别", "intent", rIntent, "param", rParam, "content", content)
	}

	return false, "", ""
}

// validateMatch 验证匹配结果参数是否合法
// 带 ID 的意图必须能解析为合法正整数，否则视为错误匹配
func (w *wechatRemote) validateMatch(intent, param string) bool {
	switch intent {
	case nlIntentTaskDetail, nlIntentSwitchTask, nlIntentPauseTask,
		nlIntentResumeTask, nlIntentStopTask, nlIntentCompleteTask, nlIntentCancelTask:
		if param == "" {
			return true
		}
		n, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(param), "号"), 10, 64)
		return err == nil && n > 0
	}
	return true
}

// matchRound1Strict 严格精确匹配
// 只处理明确指令和标准指令形式
// 短语同时维护中英文变体，统一经过 ToLower 后再匹配，避免大小写与平台差异。
func (w *wechatRemote) matchRound1Strict(lower, raw string) (intent string, param string, ok bool) {
	switch lower {
	case "帮助", "help", "/help", "怎么用", "使用说明", "可以做什么", "指令说明", "功能说明",
		"usage", "instructions", "how to use", "how to", "command list", "commands", "what can you do", "what can i do":
		return nlIntentHelp, "", true
	case "退出", "退出任务", "退出当前任务", "离开任务", "离开当前任务", "离开", "我要离开", "切出去", "结束对话", "结束当前对话", "不再关注", "取消绑定", "解绑", "清除绑定", "不绑定了",
		"exit", "exit task", "exit current task", "leave task", "leave current task", "leave", "quit", "quit task", "end chat", "end conversation", "end session", "close chat", "stop tracking", "unbind", "disconnect", "detach", "remove binding", "done with this", "all done here":
		return nlIntentExitTask, "", true
	case "取消", "取消任务", "取消当前任务", "取消执行", "取消执行任务", "取消这个任务", "取消那个任务",
		"cancel", "cancel task", "cancel this task", "cancel current task", "cancel that task", "cancel execution", "abort", "abort task", "abort execution", "stop execution":
		return nlIntentCancelTask, "", true
	case "当前任务", "当前绑定", "绑定哪个", "绑定状态", "绑定到哪个", "正在做啥", "现在做啥",
		"current task", "current binding", "bound task", "what is current", "which task", "what task", "what is bound", "what's bound", "which one", "what is selected", "currently":
		return nlIntentCurrentTask, "", true
	case "最新结果", "任务结果", "当前结果", "任务最新", "任务摘要", "任务输出",
		"latest result", "last result", "task result", "current result", "task output", "task summary", "show result", "result please":
		return nlIntentTaskSummary, "", true
	}
	// 计划 §五 / §六：本地快路径意图的精确短语。
	for _, pattern := range []string{
		"我的工作", "团队工作", "看下我的工作", "看看我的工作", "查看我的工作", "我的工作列表", "我有什么工作",
		"my work", "team work", "my tasks cloud", "show my work",
	} {
		if lower == pattern {
			return nlIntentListTeamWork, "", true
		}
	}
	for _, pattern := range []string{
		"本地项目", "项目列表", "列出项目", "查看项目", "所有项目",
		"local projects", "list projects", "show projects", "projects list",
	} {
		if lower == pattern {
			return nlIntentListProjects, "", true
		}
	}
	for _, pattern := range []string{
		"流水线", "流水线列表", "列出流水线", "查看流水线", "所有流水线",
		"pipelines", "list pipelines", "show pipelines", "pipeline list",
	} {
		if lower == pattern {
			return nlIntentListPipelines, "", true
		}
	}
	for _, pattern := range []string{
		"专家团", "专家团列表", "列出专家团", "查看专家团", "所有专家团",
		"expert groups", "list expert groups", "show expert groups", "expert group list",
	} {
		if lower == pattern {
			return nlIntentListExpertGroups, "", true
		}
	}
	for _, pattern := range []string{
		"已安装 cli", "已安装的 cli", "cli 列表", "列出 cli", "查看 cli",
		"installed clis", "list clis", "show clis", "cli list",
	} {
		if lower == pattern {
			return nlIntentListCLIs, "", true
		}
	}
	for _, pattern := range []string{
		"vibe 工具", "vibe coding 工具", "vibe 工具列表", "列出 vibe 工具", "vibe coding tools",
	} {
		if lower == pattern {
			return nlIntentListVibeTools, "", true
		}
	}

	for _, pattern := range []string{"列出所有任务", "列出任务", "查看所有任务", "任务列表", "所有任务", "我的任务",
		"list tasks", "list task", "show tasks", "show all tasks", "tasks", "list all tasks", "view tasks", "view all tasks", "show task list", "all tasks", "my tasks"} {
		if lower == pattern {
			return nlIntentListTasks, "", true
		}
	}

	// 纯 ID 形式（"5 号" / "任务 3" / "Task 4"）
	if ok, idText := matchBareTaskID(raw); ok {
		return nlIntentSwitchTask, idText, true
	}

	// 标准指令形式（带明确动作 + ID）
	if ok, idText := matchTaskIDAfterKeyword(lower, []string{"进入任务", "切换到任务", "切换任务", "绑定任务", "绑定到任务", "选择任务", "到任务", "选任务", "进入",
		"enter task", "switch to task", "switch task", "bind task", "bind to task", "select task", "go to task", "choose task", "pick task", "open task", "enter", "switch"}); ok {
		if idText != "" {
			return nlIntentSwitchTask, idText, true
		}
	}
	if ok, idText := matchTaskIDAfterKeyword(lower, []string{"暂停任务", "暂停",
		"pause task", "pause", "suspend task", "suspend", "hold task", "hold"}); ok {
		if idText != "" {
			return nlIntentPauseTask, idText, true
		}
		return nlIntentPauseTask, "", true
	}
	if ok, idText := matchTaskIDAfterKeyword(lower, []string{"恢复任务", "继续任务", "继续", "恢复",
		"resume task", "continue task", "resume", "continue"}); ok {
		if idText != "" {
			return nlIntentResumeTask, idText, true
		}
		return nlIntentResumeTask, "", true
	}
	if ok, idText := matchTaskIDAfterKeyword(lower, []string{"停止任务", "停止", "终止任务", "终止",
		"stop task", "stop", "abort task", "abort", "terminate task", "terminate", "halt task", "halt", "kill task", "kill"}); ok {
		if idText != "" {
			return nlIntentStopTask, idText, true
		}
		return nlIntentStopTask, "", true
	}
	if ok, idText := matchTaskIDAfterKeyword(lower, []string{"完成任务", "标记完成", "done", "finish",
		"complete task", "mark complete", "mark done", "finish task"}); ok {
		if idText != "" {
			return nlIntentCompleteTask, idText, true
		}
		return nlIntentCompleteTask, "", true
	}

	return "", "", false
}

// matchRound2Keywords 意图关键词提取
// 提取核心动词 + 限定词 + 数字，处理带助词的模糊指令
// 关键词同时维护中英文两个版本，lower 已是小写，避免冗余 ToLower 调用。
func (w *wechatRemote) matchRound2Keywords(lower, raw string) (intent string, param string, ok bool) {
	// 提取限定词
	hasActive := strings.Contains(lower, "进行中") || strings.Contains(lower, "正在") || strings.Contains(lower, "活跃") || strings.Contains(lower, "active") || strings.Contains(lower, "running")
	hasDone := strings.Contains(lower, "已完成") || strings.Contains(lower, "done") || strings.Contains(lower, "finished") || strings.Contains(lower, "complete") || strings.Contains(lower, "completed")
	hasBlocked := strings.Contains(lower, "阻塞") || strings.Contains(lower, "blocked") || strings.Contains(lower, "paused") || strings.Contains(lower, "stuck")
	hasCurrent := strings.Contains(lower, "当前") || strings.Contains(lower, "正在做") || strings.Contains(lower, "现在") || strings.Contains(lower, "绑定哪个") || strings.Contains(lower, "current") || strings.Contains(lower, "bound") || strings.Contains(lower, "active one") || strings.Contains(lower, "selected")

	// 提取动作动词
	hasView := strings.Contains(lower, "查看") || strings.Contains(lower, "看看") || strings.Contains(lower, "看下") || strings.Contains(lower, "look") || strings.Contains(lower, "show") || strings.Contains(lower, "list") || strings.Contains(lower, "view") || strings.Contains(lower, "browse")
	hasEnter := strings.Contains(lower, "进入") || strings.Contains(lower, "切换") || strings.Contains(lower, "绑定") || strings.Contains(lower, "选择") || strings.Contains(lower, "enter") || strings.Contains(lower, "switch") || strings.Contains(lower, "bind") || strings.Contains(lower, "select") || strings.Contains(lower, "choose") || strings.Contains(lower, "open") || strings.Contains(lower, "pick") || strings.Contains(lower, "go to")
	hasComplete := strings.Contains(lower, "完成") || strings.Contains(lower, "complete") || strings.Contains(lower, "mark done") || strings.Contains(lower, "mark complete")
	hasPause := strings.Contains(lower, "暂停") || strings.Contains(lower, "挂起") || strings.Contains(lower, "pause") || strings.Contains(lower, "suspend") || strings.Contains(lower, "hold")
	hasResume := strings.Contains(lower, "恢复") || strings.Contains(lower, "继续") || strings.Contains(lower, "resume") || strings.Contains(lower, "continue")
	hasStop := strings.Contains(lower, "停止") || strings.Contains(lower, "终止") || strings.Contains(lower, "stop") || strings.Contains(lower, "terminate") || strings.Contains(lower, "abort") || strings.Contains(lower, "halt") || strings.Contains(lower, "kill")
	// 退出/离开类：与 /exit、/q 行为一致，命中时直接清绑定 + 列当前进行中任务
	hasExit := strings.Contains(lower, "退出") || strings.Contains(lower, "离开") || strings.Contains(lower, "结束对话") || strings.Contains(lower, "切出去") || strings.Contains(lower, "结束会话") || strings.Contains(lower, "exit") || strings.Contains(lower, "leave") || strings.Contains(lower, "quit") || strings.Contains(lower, "end chat") || strings.Contains(lower, "end conversation") || strings.Contains(lower, "end session") || strings.Contains(lower, "close chat")
	// 解绑类（取消绑定/解绑/解除）：与 hasExit 一致地走向 nlIntentExitTask，保持对外体验统一
	hasUnbind := strings.Contains(lower, "取消绑定") || strings.Contains(lower, "解绑") || strings.Contains(lower, "解除") || strings.Contains(lower, "unbind") || strings.Contains(lower, "disconnect") || strings.Contains(lower, "detach") || strings.Contains(lower, "remove binding")
	// 取消执行类：与 /c 行为一致，需要二次确认
	// 仅匹配明确的「取消任务/取消执行/取消当前/取消这个」短语，避免与"取消绑定"混淆
	// 英文同上：必须包含 task/execution/this/current 等语义词，避免单独 "cancel" 过度匹配
	hasCancel := strings.Contains(lower, "取消任务") || strings.Contains(lower, "取消执行") || strings.Contains(lower, "取消当前") || strings.Contains(lower, "取消这个") || strings.Contains(lower, "取消那个") || strings.Contains(lower, "取消一下") || lower == "取消" ||
		strings.Contains(lower, "cancel task") || strings.Contains(lower, "cancel this task") || strings.Contains(lower, "cancel current task") || strings.Contains(lower, "abort task") || strings.Contains(lower, "cancel execution") || strings.Contains(lower, "abort execution") || strings.Contains(lower, "stop execution")

	// 提取数字 ID
	idText := extractTaskID(raw)
	if idText == "" {
		if matched, t := matchBareTaskID(raw); matched {
			idText = t
		}
	}

	// 复合判断：先看动作动词，再看限定词
	// 优先级：退出 > 解绑 > 取消执行 > 直接动作动词 > 查看/列表。
	// 「取消绑定」同时含"取消"，但 hasUnbind 已在前面命中并指向 ExitTask；这里独立使用 hasCancel 仅针对语义明确的"取消执行"。
	if hasExit {
		return nlIntentExitTask, "", true
	}
	if hasUnbind {
		return nlIntentExitTask, "", true
	}

	if hasComplete && idText != "" {
		return nlIntentCompleteTask, idText, true
	}
	if hasPause && idText != "" {
		return nlIntentPauseTask, idText, true
	}
	if hasResume && idText != "" {
		return nlIntentResumeTask, idText, true
	}
	if hasStop && idText != "" {
		return nlIntentStopTask, idText, true
	}
	if hasEnter && idText != "" {
		return nlIntentSwitchTask, idText, true
	}
	// 取消执行类：与命令 /c 等价，只对当前绑定任务生效；带 ID 的语料（例如"取消任务 5"）
	// 通过 validateMatch 校验后由 handleCancelTask 取用 boundTaskUUID，ID 参数被忽略。
	if hasCancel {
		return nlIntentCancelTask, idText, true
	}

	// 实体短语类（B6 二轮扩展）：我的工作 / 项目 / 流水线 / 专家团 / CLI / Vibe 工具。
	//
	// 必须放在 hasView 分支之前：hasView 只认「查看/看看/列出」这类泛动词，遇到
	// 「看看项目」会一路落到「无 ID、无 all 关键词」的兜底 return false，用户于是
	// 被丢给 CLI 兜底。判定的判别式是**实体关键词**（项目/流水线/expert group...），
	// 不是动词 —— 因此不会吞掉既有「查看任务」语料（任务类在下面 hasView 分支处理）。
	//
	// 零回归保证：凡是被既有分支**已经判过**的语料类型，一律让位 ——
	//   · 退出 / 解绑 / 取消执行（上方已 return）；
	//   · 带任务 ID；
	//   · 任务动作动词（停止/暂停/恢复/完成/进入）；
	//   · 任务限定词（当前 / 进行中 / 已完成 / 阻塞）。
	// 只有这些既有信号全部缺席时，实体关键词才生效 —— 此时旧实现返回 false（走 CLI 兜底），
	// 因此新增识别不会改写任何既有判定，只把原本兜底掉的短句接进快路径。
	// 唯一有意的改判：「查看所有项目」这类同时含实体关键词与「所有」的短句，
	// 旧实现经 containsAllKeyword 误判为「列出所有任务」，现按实体关键词归位（见交付说明）。
	conflictingAction := hasStop || hasPause || hasResume || hasComplete || hasEnter ||
		hasCurrent || hasActive || hasDone || hasBlocked
	if intent, ok := matchEntityListingIntent(lower, conflictingAction, idText); ok {
		return intent, "", true
	}

	// 查看类 + 限定词
	// 仅在显式「所有 / 全部」信号出现时才兜底为 listTasks；其他裸短句（如「查看一下」）
	// 不应被识别为列表查询，让 CLI 兜底或继续派发到任务。
	if hasView {
		if hasCurrent {
			return nlIntentCurrentTask, "", true
		}
		if hasActive {
			return nlIntentListActive, "", true
		}
		if hasBlocked {
			return nlIntentListBlocked, "", true
		}
		if hasDone {
			return nlIntentListDone, "", true
		}
		if idText != "" {
			return nlIntentTaskDetail, idText, true
		}
		if containsAllKeyword(raw) {
			return nlIntentListTasks, "", true
		}
		return "", "", false
	}

	// 列表类动词（列出 / 显示）也必须配合「所有 / 全部 / all」才识别为 listTasks，
	// 否则单说「列出」「显示一下」不应被兜底为列出所有任务。
	// 英文 "show / list / view" 等动词已在 hasView 分支处理；这里只保留中文的兜底入口。
	if strings.Contains(lower, "列出") || strings.Contains(lower, "显示") {
		if hasActive {
			return nlIntentListActive, "", true
		}
		if hasDone {
			return nlIntentListDone, "", true
		}
		if hasBlocked {
			return nlIntentListBlocked, "", true
		}
		if containsAllKeyword(raw) {
			return nlIntentListTasks, "", true
		}
		return "", "", false
	}

	return "", "", false
}

// matchEntityListingIntent 匹配「实体关键词」驱动的本地快路径列表意图。
//
// 与 hasView 分支的分工：hasView 负责「任务」这一实体（以及带 ID 的详情），
// 本函数负责 我的工作 / 项目 / 流水线 / 专家团 / CLI / Vibe 工具 六类实体。
// 命中条件是实体关键词本身（「项目」即足以表达要看项目列表），动词可有可无；
// 这是与 /pro /pls /egs /clis /vcl /tt 六条快路径命令对齐的自然语言入口。
//
// 返回 false 的保守情形：
//   - 内容里出现了任务 ID（idText != ""）：用户多半在说某个任务，不能改判成项目列表；
//   - 命中停止/暂停/完成/进入/取消这类动作动词：语义是「对某实体做动作」，
//     不是「列出该实体」，交给后续分支或 CLI 兜底；
//   - 同时命中多个实体：语义不明确，宁可回落 hasView / CLI 兜底，不猜。
//
// 英文一律用精确短语或整词（cli / clis）而非裸子串，
// 避免 "client" / "clipboard" 里的 "cli" 以及长句里的 "projects" 被误伤。
func matchEntityListingIntent(lower string, conflictingAction bool, idText string) (string, bool) {
	if idText != "" || conflictingAction {
		return "", false
	}
	// 顺序即优先级：越具体/越长短语的实体先判，避免 "vibe coding tools" 同时命中 "cli"。
	var (
		matched string
		count   int
	)
	record := func(intent string) {
		if matched != intent {
			count++
		}
		matched = intent
	}
	if containsAny(lower, "我的工作", "团队工作", "我的任务云", "my work", "team work") {
		record(nlIntentListTeamWork)
	}
	if containsAny(lower, "项目", "project") {
		record(nlIntentListProjects)
	}
	if containsAny(lower, "流水线", "pipeline") {
		record(nlIntentListPipelines)
	}
	if containsAny(lower, "专家团", "expert group", "expert-group") {
		record(nlIntentListExpertGroups)
	}
	if containsAny(lower, "vibe 工具", "vibe工具", "vibe coding 工具", "vibe tool", "vibe coding tool") {
		record(nlIntentListVibeTools)
	}
	if strings.Contains(lower, "命令行工具") || hasWholeWord(lower, "cli", "clis") {
		record(nlIntentListCLIs)
	}
	if count != 1 {
		return "", false
	}
	return matched, true
}

// containsAny 判断 lower 是否含给定子串中的任意一个（lower 已小写）。
func containsAny(lower string, subs ...string) bool {
	for _, s := range subs {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}

// hasWholeWord 判断 lower（已小写）是否含以空白/标点分隔的整词之一。
// 用于避开 "client" 里的 "cli" 这类子串误命中。
func hasWholeWord(lower string, words ...string) bool {
	for _, tok := range strings.FieldsFunc(lower, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		for _, w := range words {
			if tok == w {
				return true
			}
		}
	}
	return false
}

// containsAllKeyword 判断内容是否含「所有 / 全部 / all」这类"列全"信号。
// nlIntentListTasks 应当只由明确的"列全"语义触发，而不是任何「任务」字眼，
// 否则任务对话里随口提到「任务」就会被误识别为"列出所有任务"。
// 英文部分使用精确短语（如 all tasks / every task），避免子串误伤（例如 small task）。
func containsAllKeyword(raw string) bool {
	lower := strings.ToLower(raw)
	if strings.Contains(raw, "所有") || strings.Contains(raw, "全部") {
		return true
	}
	for _, kw := range []string{"all tasks", "all the tasks", "all of the tasks", "every task", "every tasks", "all my tasks", "all of them", "everything"} {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// withinIntentLengthLimit 判断内容是否在自然语言意图匹配的长度限制内
// 避免长任务内容（如「这个任务的原文是什么？将这个任务的原文发给我。」）被误识别为简单指令。
// 没有参数时严格：中文 ≤ 15 字，英文 ≤ 60 字符；
// 包含任务 ID 时宽松：中文 ≤ 30 字，英文 ≤ 120 字符，因为 ID 本身已大幅降低误判概率。
// 超出限制的复杂内容应直接走 CLI 兜底，由 CLI 通过 skill 进一步处理。
// 区分纯 ASCII（rune == byte）和含中文（rune < byte）两种情况，避免纯英文被中文 15 字限制误伤。
func withinIntentLengthLimit(content string, hasID bool) bool {
	runeCount := len([]rune(content))
	byteCount := len(content)
	maxRune, maxByte := 15, 60
	if hasID {
		maxRune, maxByte = 30, 120
	}
	if runeCount == byteCount {
		// 纯 ASCII（英文/数字/标点），按字节数判断
		return byteCount <= maxByte
	}
	// 含非 ASCII 字符（中文等），rune 与 byte 双重判断
	return runeCount <= maxRune && byteCount <= maxByte
}

// extractTaskID 从消息中提取任务ID数字
// 支持："5 号" / "任务 3" / "完成 4" / "进入 5" / "切换到 3" 等
func extractTaskID(content string) string {
	if strings.HasSuffix(content, "号") {
		rest := strings.TrimSpace(strings.TrimSuffix(content, "号"))
		rest = strings.TrimSpace(strings.TrimPrefix(rest, "任务"))
		rest = strings.TrimSpace(strings.TrimPrefix(rest, "Task"))
		rest = strings.TrimSpace(strings.TrimPrefix(rest, "task"))
		if _, err := strconv.ParseInt(rest, 10, 64); err == nil {
			return rest
		}
	}
	re := regexp.MustCompile(`\d+`)
	match := re.FindString(content)
	if match != "" {
		return match
	}
	return ""
}

// matchTaskIDAfterKeyword 在关键词后提取任务ID
// 例如 "暂停任务 3" / "暂停 3" / "完成 5"
func matchTaskIDAfterKeyword(content string, keywords []string) (bool, string) {
	for _, kw := range keywords {
		if strings.HasPrefix(content, kw) {
			rest := strings.TrimSpace(strings.TrimPrefix(content, kw))
			if rest == "" {
				return true, ""
			}
			// 如果后续是 "任务 X" 形式，去掉"任务"前缀
			if strings.HasPrefix(rest, "任务") {
				rest = strings.TrimSpace(strings.TrimPrefix(rest, "任务"))
			}
			if rest != "" {
				return true, rest
			}
		}
	}
	return false, ""
}

// matchBareTaskIDAfterKeyword "完成 5" 这种
func matchBareTaskIDAfterKeyword(content, keyword string) (bool, string) {
	idx := strings.Index(content, keyword)
	if idx < 0 {
		return false, ""
	}
	rest := strings.TrimSpace(content[idx+len(keyword):])
	if rest == "" {
		return false, ""
	}
	if _, err := strconv.ParseInt(rest, 10, 64); err == nil {
		return true, rest
	}
	// "完成 X 号" / "完成 X"
	rest = strings.TrimSuffix(rest, "号")
	if _, err := strconv.ParseInt(rest, 10, 64); err == nil {
		return true, rest
	}
	return false, ""
}

// matchBareTaskID 纯任务ID形式（"任务 4" / "选 3" / "3 号"）
func matchBareTaskID(content string) (bool, string) {
	content = strings.TrimSpace(content)
	// "任务 X" / "任务X" / "task X"
	for _, prefix := range []string{"任务", "task", "Task"} {
		if strings.HasPrefix(content, prefix) {
			rest := strings.TrimSpace(strings.TrimPrefix(content, prefix))
			if rest == "" {
				return false, ""
			}
			rest = strings.TrimSuffix(rest, "号")
			if _, err := strconv.ParseInt(rest, 10, 64); err == nil {
				return true, rest
			}
			return false, ""
		}
	}
	// "X 号" 形式
	if strings.HasSuffix(content, "号") {
		rest := strings.TrimSpace(strings.TrimSuffix(content, "号"))
		if _, err := strconv.ParseInt(rest, 10, 64); err == nil {
			return true, rest
		}
	}
	return false, ""
}

// handleNaturalLanguageIntent 处理已识别的自然语言意图
// force 表示用户消息中是否含「强制 / 直接 / -f / force」关键词：
//   - 仅影响需要二次确认的意图（目前只有 nlIntentCancelTask）；
//   - 其它意图（resume / complete / pause / stop）已经走 changeTaskStatusByShortID 直写，
//     无确认语义，force 对它们无实际效果。
func (w *wechatRemote) handleNaturalLanguageIntent(ctx context.Context, state wechatState, intent, param string, force bool) (reply, taskUUID, sessionUUID string, err error) {
	switch intent {
	case nlIntentHelp:
		reply = w.tr(ctx, "remote_wechat_help_full", nil)
		return reply, "", "", nil

	case nlIntentListTasks:
		reply, err = w.skillSurface.WechatListTasks(w.wechatCtx(ctx), "")
		return reply, "", "", err

	case nlIntentListActive:
		reply, err = w.skillSurface.WechatListTasks(w.wechatCtx(ctx), "active")
		return reply, "", "", err

	case nlIntentListDone:
		reply, err = w.skillSurface.WechatListTasks(w.wechatCtx(ctx), "done")
		return reply, "", "", err

	case nlIntentListBlocked:
		reply, err = w.skillSurface.WechatListTasks(w.wechatCtx(ctx), "blocked")
		return reply, "", "", err

	case nlIntentListTeamWork, nlIntentListProjects, nlIntentListPipelines,
		nlIntentListExpertGroups, nlIntentListCLIs, nlIntentListVibeTools:
		return w.handleSkillLookupIntent(ctx, intent)

	case nlIntentCurrentTask:
		if state.BoundTaskUUID == "" {
			return w.tr(ctx, "remote_wechat_no_binding", i18n.Params{"Help": w.tr(ctx, "remote_wechat_help", nil)}), "", "", nil
		}
		task, taskErr := w.taskByUUID(ctx, state.BoundTaskUUID)
		if errors.Is(taskErr, sql.ErrNoRows) {
			_, _ = w.db.ExecContext(ctx, `UPDATE gt_remote_wechat_state SET bound_task_uuid='' WHERE id=1`)
			return w.tr(ctx, "remote_wechat_task_missing", i18n.Params{"Help": w.tr(ctx, "remote_wechat_help", nil)}), "", "", nil
		}
		if taskErr != nil {
			err = taskErr
			return
		}
		last, lastErr := w.latestFinal(ctx, task)
		if lastErr != nil {
			err = lastErr
			return
		}
		return w.tr(ctx, "remote_wechat_switch", i18n.Params{"Task": task.Title, "Result": last}), task.UUID, "", nil

	case nlIntentTaskSummary:
		if state.BoundTaskUUID == "" {
			return w.tr(ctx, "remote_wechat_no_binding", i18n.Params{"Help": w.tr(ctx, "remote_wechat_help", nil)}), "", "", nil
		}
		return w.handleTaskSummary(ctx, state.BoundTaskUUID)

	case nlIntentSwitchTask:
		return w.switchTaskByShortID(ctx, param)

	case nlIntentPauseTask:
		return w.changeTaskStatusByShortID(ctx, param, "blocked", "已暂停任务")

	case nlIntentResumeTask:
		return w.changeTaskStatusByShortID(ctx, param, "active", "已恢复任务")

	case nlIntentStopTask:
		return w.changeTaskStatusByShortID(ctx, param, "blocked", "已停止任务")

	case nlIntentCancelTask:
		// 对齐 /c 命令：检测到「强制 / 直接 / -f」关键词时跳过二次确认走 SkillSurface.WechatCancelTask，
		// 否则按 /c 默认行为写 pending_cancel_task_uuid 等下一条消息确认。
		return w.handleCancelTask(ctx, force)

	case nlIntentExitTask:
		// 对齐 /exit、/q 命令：清绑定 + 列出当前进行中任务，体验更友好。
		return w.handleExitTask(ctx)

	case nlIntentCompleteTask:
		return w.changeTaskStatusByShortID(ctx, param, "done", "已完成任务")

	case nlIntentTaskDetail:
		return w.showTaskDetailByShortID(ctx, param)
	}

	return "", "", "", nil
}

// handleTaskSummary 显示任务最新结果
func (w *wechatRemote) handleTaskSummary(ctx context.Context, taskUUID string) (reply, taskUUIDOut, sessionUUID string, err error) {
	task, taskErr := w.taskByUUID(ctx, taskUUID)
	if errors.Is(taskErr, sql.ErrNoRows) {
		return w.tr(ctx, "remote_wechat_task_missing", i18n.Params{"Help": w.tr(ctx, "remote_wechat_help", nil)}), "", "", nil
	}
	if taskErr != nil {
		err = taskErr
		return
	}
	last, lastErr := w.latestFinal(ctx, task)
	if lastErr != nil {
		err = lastErr
		return
	}
	return w.tr(ctx, "remote_wechat_switch", i18n.Params{"Task": task.Title, "Result": last}), task.UUID, "", nil
}

// switchTaskByShortID 切换到指定短ID的任务（同时设置绑定）
func (w *wechatRemote) switchTaskByShortID(ctx context.Context, param string) (reply, taskUUID, sessionUUID string, err error) {
	if param == "" {
		return w.tr(ctx, "remote_wechat_id_invalid", i18n.Params{"Help": w.tr(ctx, "remote_wechat_help", nil)}), "", "", nil
	}
	shortID, parseErr := strconv.ParseInt(param, 10, 64)
	if parseErr != nil || shortID < 1 {
		return w.tr(ctx, "remote_wechat_id_invalid", i18n.Params{"Help": w.tr(ctx, "remote_wechat_help", nil)}), "", "", nil
	}
	if err = w.ensureAliases(ctx); err != nil {
		return
	}
	task, taskErr := w.taskByShortID(ctx, shortID)
	if errors.Is(taskErr, sql.ErrNoRows) {
		return w.tr(ctx, "remote_wechat_task_missing", i18n.Params{"Help": w.tr(ctx, "remote_wechat_help", nil)}), "", "", nil
	}
	if taskErr != nil {
		err = taskErr
		return
	}
	if _, execErr := w.db.ExecContext(ctx, `UPDATE gt_remote_wechat_state SET bound_task_uuid=?, updated_at=? WHERE id=1`, task.UUID, time.Now().UnixMilli()); execErr != nil {
		err = execErr
		return
	}
	return fmt.Sprintf("已切换到任务 @%d: %s", shortID, task.Title), task.UUID, "", nil
}

// showTaskDetailByShortID 显示指定任务详情（不切换绑定）
func (w *wechatRemote) showTaskDetailByShortID(ctx context.Context, param string) (reply, taskUUID, sessionUUID string, err error) {
	if param == "" {
		return w.tr(ctx, "remote_wechat_id_invalid", i18n.Params{"Help": w.tr(ctx, "remote_wechat_help", nil)}), "", "", nil
	}
	shortID, parseErr := strconv.ParseInt(param, 10, 64)
	if parseErr != nil || shortID < 1 {
		return w.tr(ctx, "remote_wechat_id_invalid", i18n.Params{"Help": w.tr(ctx, "remote_wechat_help", nil)}), "", "", nil
	}
	if err = w.ensureAliases(ctx); err != nil {
		return
	}
	task, taskErr := w.taskByShortID(ctx, shortID)
	if errors.Is(taskErr, sql.ErrNoRows) {
		return w.tr(ctx, "remote_wechat_task_missing", i18n.Params{"Help": w.tr(ctx, "remote_wechat_help", nil)}), "", "", nil
	}
	if taskErr != nil {
		err = taskErr
		return
	}
	last, lastErr := w.latestFinal(ctx, task)
	if lastErr != nil {
		err = lastErr
		return
	}
	return w.tr(ctx, "remote_wechat_switch", i18n.Params{"Task": task.Title, "Result": last}), task.UUID, "", nil
}

// changeTaskStatusByShortID 修改指定任务状态
// param 为空时使用当前绑定任务
func (w *wechatRemote) changeTaskStatusByShortID(ctx context.Context, param, targetStatus, successMessage string) (reply, taskUUID, sessionUUID string, err error) {
	if err = w.ensureAliases(ctx); err != nil {
		return
	}
	var task wechatTask
	if param == "" {
		// 没有指定 ID，使用当前绑定任务
		state, readErr := w.readState(ctx)
		if readErr != nil {
			err = readErr
			return
		}
		if state.BoundTaskUUID == "" {
			return w.tr(ctx, "remote_wechat_no_binding", i18n.Params{"Help": w.tr(ctx, "remote_wechat_help", nil)}), "", "", nil
		}
		task, err = w.taskByUUID(ctx, state.BoundTaskUUID)
		if errors.Is(err, sql.ErrNoRows) {
			_, _ = w.db.ExecContext(ctx, `UPDATE gt_remote_wechat_state SET bound_task_uuid='',
				pending_cancel_task_uuid='', pending_resume_task_uuid='', pending_done_task_uuid='' WHERE id=1`)
			return w.tr(ctx, "remote_wechat_task_missing", i18n.Params{"Help": w.tr(ctx, "remote_wechat_help", nil)}), "", "", nil
		}
		if err != nil {
			return
		}
	} else {
		shortID, parseErr := strconv.ParseInt(param, 10, 64)
		if parseErr != nil || shortID < 1 {
			return w.tr(ctx, "remote_wechat_id_invalid", i18n.Params{"Help": w.tr(ctx, "remote_wechat_help", nil)}), "", "", nil
		}
		task, err = w.taskByShortID(ctx, shortID)
		if errors.Is(err, sql.ErrNoRows) {
			return w.tr(ctx, "remote_wechat_task_missing", i18n.Params{"Help": w.tr(ctx, "remote_wechat_help", nil)}), "", "", nil
		}
		if err != nil {
			return
		}
	}
	// 更新任务状态
	now := time.Now().UnixMilli()
	if _, execErr := w.db.ExecContext(ctx, `UPDATE gt_tasks SET status=?, updated_at=? WHERE uuid=?`, targetStatus, now, task.UUID); execErr != nil {
		err = execErr
		return
	}
	// 读取更新后的最新结果
	last, lastErr := w.latestFinal(ctx, task)
	if lastErr != nil {
		// 不阻断主流程
		last = ""
	}
	if last != "" {
		return fmt.Sprintf("%s\n@%d %s\n%s", successMessage, task.ShortID, task.Title, last), task.UUID, "", nil
	}
	return fmt.Sprintf("%s @%d %s", successMessage, task.ShortID, task.Title), task.UUID, "", nil
}

// isCancelConfirm 判断用户输入是否为「二次确认取消」的关键词。
// 仅做归一化大小写和去除首尾空白的等价比较，避免误匹配更长文本。
func isCancelConfirm(content string) bool {
	switch strings.ToLower(strings.TrimSpace(content)) {
	case "yes", "y", "是", "对", "取消":
		return true
	}
	return false
}

// isConfirm 判断用户输入是否为通用的二次确认关键词（yes / y / 是 / 对）。
// 用于 /r 恢复 / /d 完成的二次确认；/c 取消继续使用专门的 isCancelConfirm 兼容 取消 关键词。
func isConfirm(content string) bool {
	switch strings.ToLower(strings.TrimSpace(content)) {
	case "yes", "y", "是", "对":
		return true
	}
	return false
}

// handlePendingConfirmations 处理 state 中所有 pending_xxx_task_uuid 的二次确认。
// 命中任意一个 pending 字段且输入为确认关键词时，立即执行对应动作并返回 handled=true；
// 否则清空所有 pending 字段并返回 handled=false，让后续指令 / 意图 / 任务处理按正常流程进行。
// 多字段同时存在（异常）时按 cancel > resume > done 优先级依次匹配第一个确认项。
// handleSkillLookupIntent 把"我的工作 / 项目 / 流水线 / 专家团 / CLI / Vibe 工具"
// 这一组自然语言意图统一走 SkillSurface 复用（计划 §四）。
func (w *wechatRemote) handleSkillLookupIntent(ctx context.Context, intent string) (reply, taskUUID, sessionUUID string, err error) {
	if w.skillSurface == nil {
		return w.tr(ctx, "remote_wechat_skill_call_failed",
			i18n.Params{"Detail": "skill surface not ready"}), "", "", nil
	}
	execCtx := w.wechatCtx(ctx)
	var (
		out  string
		ferr error
	)
	switch intent {
	case nlIntentListTeamWork:
		out, ferr = w.skillSurface.WechatFormatTeamWork(execCtx, false)
	case nlIntentListProjects:
		out, ferr = w.skillSurface.WechatFormatProjects(execCtx)
	case nlIntentListPipelines:
		out, ferr = w.skillSurface.WechatFormatPipelines(execCtx)
	case nlIntentListExpertGroups:
		out, ferr = w.skillSurface.WechatFormatExpertGroups(execCtx)
	case nlIntentListCLIs:
		out, ferr = w.skillSurface.WechatFormatCLIs(execCtx)
	case nlIntentListVibeTools:
		out, ferr = w.skillSurface.WechatFormatVibeTools(execCtx)
	}
	if ferr != nil {
		return w.wechatSkillFailure(ctx, out, ferr), "", "", nil
	}
	return out, "", "", nil
}

func (w *wechatRemote) handlePendingConfirmations(ctx context.Context, state *wechatState, content string) (handled bool, reply, taskUUID, sessionUUID string, err error) {
	pendingItems := []struct {
		uuid  string
		match func(string) bool
		exec  func(ctx context.Context, taskUUID string) (string, string, string, error)
		clear string
	}{
		{state.PendingCancelTaskUUID, isCancelConfirm, w.executeCancelTask,
			"pending_cancel_task_uuid=''"},
		{state.PendingResumeTaskUUID, isConfirm, w.executeResumeTask,
			"pending_resume_task_uuid=''"},
		{state.PendingDoneTaskUUID, isConfirm, w.executeCompleteTask,
			"pending_done_task_uuid=''"},
	}
	anyPending := false
	for _, p := range pendingItems {
		if p.uuid == "" {
			continue
		}
		anyPending = true
		if p.match(content) {
			reply, taskUUID, sessionUUID, err = p.exec(ctx, p.uuid)
			return true, reply, taskUUID, sessionUUID, err
		}
	}
	if !anyPending {
		return false, "", "", "", nil
	}
	if _, clearErr := w.db.ExecContext(ctx, `UPDATE gt_remote_wechat_state SET
		pending_cancel_task_uuid='',
		pending_resume_task_uuid='',
		pending_done_task_uuid='' WHERE id=1`); clearErr != nil {
		return false, "", "", "", clearErr
	}
	state.PendingCancelTaskUUID = ""
	state.PendingResumeTaskUUID = ""
	state.PendingDoneTaskUUID = ""
	return false, "", "", "", nil
}

// handleExitTask 处理 /exit /q 退出当前任务
// 仅清除微信通道上的绑定，任务本身的状态保持不变，让用户后续可通过 @任务ID 重新进入。
// 列任务收尾与文案统一走 SkillSurface.WechatExitTaskText，避免与 CLI Skill 兜底漂移。
func (w *wechatRemote) handleExitTask(ctx context.Context) (string, string, string, error) {
	if err := w.ensureAliases(ctx); err != nil {
		return "", "", "", err
	}
	state, err := w.readState(ctx)
	if err != nil {
		return "", "", "", err
	}
	execCtx := w.wechatCtx(ctx)
	if state.BoundTaskUUID == "" {
		listText, listErr := w.skillSurface.WechatListTasks(execCtx, "active")
		if listErr != nil {
			return "", "", "", listErr
		}
		return w.tr(ctx, "remote_wechat_no_task_bound", i18n.Params{"Tasks": listText}), "", "", nil
	}
	var title string
	_ = w.db.QueryRowContext(ctx, `SELECT title FROM gt_tasks WHERE uuid=?`, state.BoundTaskUUID).Scan(&title)
	if _, execErr := w.db.ExecContext(ctx, `UPDATE gt_remote_wechat_state SET bound_task_uuid='',
		pending_cancel_task_uuid='', pending_resume_task_uuid='', pending_done_task_uuid='',
		updated_at=? WHERE id=1`, time.Now().UnixMilli()); execErr != nil {
		return "", "", "", execErr
	}
	text, textErr := w.skillSurface.WechatExitTaskText(execCtx, title)
	if textErr != nil {
		return "", "", "", textErr
	}
	return text, "", "", nil
}

// handleCancelTask 处理 /c 取消执行当前任务
// force=true 走直接执行（业务核心在 SkillSurface.WechatCancelTask），
// 否则把待取消的任务写入 pending_cancel_task_uuid，等用户下一条消息确认（yes / y / 是 / 对 / 取消）。
func (w *wechatRemote) handleCancelTask(ctx context.Context, force bool) (string, string, string, error) {
	if err := w.ensureAliases(ctx); err != nil {
		return "", "", "", err
	}
	state, err := w.readState(ctx)
	if err != nil {
		return "", "", "", err
	}
	execCtx := w.wechatCtx(ctx)
	if state.BoundTaskUUID == "" {
		listText, listErr := w.skillSurface.WechatListTasks(execCtx, "active")
		if listErr != nil {
			return "", "", "", listErr
		}
		return w.tr(ctx, "remote_wechat_no_task_bound", i18n.Params{"Tasks": listText}), "", "", nil
	}
	var title string
	err = w.db.QueryRowContext(ctx, `SELECT title FROM gt_tasks WHERE uuid=?`, state.BoundTaskUUID).Scan(&title)
	if errors.Is(err, sql.ErrNoRows) {
		_, _ = w.db.ExecContext(ctx, `UPDATE gt_remote_wechat_state SET bound_task_uuid='',
			pending_cancel_task_uuid='', pending_resume_task_uuid='', pending_done_task_uuid='' WHERE id=1`)
		return w.tr(ctx, "remote_wechat_task_missing", i18n.Params{"Help": w.tr(ctx, "remote_wechat_help", nil)}), "", "", nil
	}
	if err != nil {
		return "", "", "", err
	}
	if force {
		return w.executeCancelTask(ctx, state.BoundTaskUUID)
	}
	if _, execErr := w.db.ExecContext(ctx, `UPDATE gt_remote_wechat_state SET pending_cancel_task_uuid=?, updated_at=? WHERE id=1`, state.BoundTaskUUID, time.Now().UnixMilli()); execErr != nil {
		return "", "", "", execErr
	}
	return w.tr(ctx, "remote_wechat_cancel_confirm", i18n.Params{"Task": title}), state.BoundTaskUUID, "", nil
}

// executeCancelTask 真正执行取消：业务核心下沉到 SkillSurface.WechatCancelTask，
// 本函数只负责清 state 与转发结果，避免与 CLI Skill 兜底在两处各写一套。
func (w *wechatRemote) executeCancelTask(ctx context.Context, taskUUID string) (string, string, string, error) {
	if err := w.ensureAliases(ctx); err != nil {
		return "", "", "", err
	}
	if _, execErr := w.db.ExecContext(ctx, `UPDATE gt_remote_wechat_state SET bound_task_uuid='',
		pending_cancel_task_uuid='', pending_resume_task_uuid='', pending_done_task_uuid='',
		updated_at=? WHERE id=1`, time.Now().UnixMilli()); execErr != nil {
		return "", "", "", execErr
	}
	text, err := w.skillSurface.WechatCancelTask(w.wechatCtx(ctx), taskUUID)
	if err != nil {
		return "", "", "", err
	}
	return text, "", "", nil
}

// handleResumeTask 处理 /r 继续执行当前任务
// force=true 走直接执行（业务核心在 SkillSurface.WechatResumeTask）；
// 否则把待恢复的任务写入 pending_resume_task_uuid，等用户下一条消息确认（yes / y / 是 / 对）。
// 与 nlIntentResumeTask 不同：本命令只作用于绑定任务，忽略 ID 参数；自然语言
// 场景下的「恢复 5」继续走 changeTaskStatusByShortID 不进入确认。
func (w *wechatRemote) handleResumeTask(ctx context.Context, force bool) (string, string, string, error) {
	state, err := w.readState(ctx)
	if err != nil {
		return "", "", "", err
	}
	execCtx := w.wechatCtx(ctx)
	if state.BoundTaskUUID == "" {
		listText, listErr := w.skillSurface.WechatListTasks(execCtx, "active")
		if listErr != nil {
			return "", "", "", listErr
		}
		return w.tr(ctx, "remote_wechat_no_task_bound", i18n.Params{"Tasks": listText}), "", "", nil
	}
	var title string
	err = w.db.QueryRowContext(ctx, `SELECT title FROM gt_tasks WHERE uuid=?`, state.BoundTaskUUID).Scan(&title)
	if errors.Is(err, sql.ErrNoRows) {
		_, _ = w.db.ExecContext(ctx, `UPDATE gt_remote_wechat_state SET bound_task_uuid='',
			pending_cancel_task_uuid='', pending_resume_task_uuid='', pending_done_task_uuid='' WHERE id=1`)
		return w.tr(ctx, "remote_wechat_task_missing", i18n.Params{"Help": w.tr(ctx, "remote_wechat_help", nil)}), "", "", nil
	}
	if err != nil {
		return "", "", "", err
	}
	if force {
		return w.executeResumeTask(ctx, state.BoundTaskUUID)
	}
	if _, execErr := w.db.ExecContext(ctx, `UPDATE gt_remote_wechat_state SET pending_resume_task_uuid=?, updated_at=? WHERE id=1`, state.BoundTaskUUID, time.Now().UnixMilli()); execErr != nil {
		return "", "", "", execErr
	}
	return w.tr(ctx, "remote_wechat_resume_confirm", i18n.Params{"Task": title}), state.BoundTaskUUID, "", nil
}

// executeResumeTask 真正执行恢复：业务核心下沉到 SkillSurface.WechatResumeTask。
func (w *wechatRemote) executeResumeTask(ctx context.Context, taskUUID string) (string, string, string, error) {
	if _, execErr := w.db.ExecContext(ctx, `UPDATE gt_remote_wechat_state SET pending_resume_task_uuid='', updated_at=? WHERE id=1`, time.Now().UnixMilli()); execErr != nil {
		return "", "", "", execErr
	}
	text, err := w.skillSurface.WechatResumeTask(w.wechatCtx(ctx), taskUUID)
	if err != nil {
		return "", "", "", err
	}
	return text, taskUUID, "", nil
}

// handleCompleteTask 处理 /d 标记当前任务为已完成并退出当前会话
// force=true 走直接执行（业务核心在 SkillSurface.WechatCompleteTask）；
// 否则把待完成的任务写入 pending_done_task_uuid，等用户下一条消息确认（yes / y / 是 / 对）。
func (w *wechatRemote) handleCompleteTask(ctx context.Context, force bool) (string, string, string, error) {
	if err := w.ensureAliases(ctx); err != nil {
		return "", "", "", err
	}
	state, err := w.readState(ctx)
	if err != nil {
		return "", "", "", err
	}
	execCtx := w.wechatCtx(ctx)
	if state.BoundTaskUUID == "" {
		listText, listErr := w.skillSurface.WechatListTasks(execCtx, "active")
		if listErr != nil {
			return "", "", "", listErr
		}
		return w.tr(ctx, "remote_wechat_no_task_bound", i18n.Params{"Tasks": listText}), "", "", nil
	}
	var title string
	err = w.db.QueryRowContext(ctx, `SELECT title FROM gt_tasks WHERE uuid=?`, state.BoundTaskUUID).Scan(&title)
	if errors.Is(err, sql.ErrNoRows) {
		_, _ = w.db.ExecContext(ctx, `UPDATE gt_remote_wechat_state SET bound_task_uuid='',
			pending_cancel_task_uuid='', pending_resume_task_uuid='', pending_done_task_uuid='' WHERE id=1`)
		return w.tr(ctx, "remote_wechat_task_missing", i18n.Params{"Help": w.tr(ctx, "remote_wechat_help", nil)}), "", "", nil
	}
	if err != nil {
		return "", "", "", err
	}
	if force {
		return w.executeCompleteTask(ctx, state.BoundTaskUUID)
	}
	if _, execErr := w.db.ExecContext(ctx, `UPDATE gt_remote_wechat_state SET pending_done_task_uuid=?, updated_at=? WHERE id=1`, state.BoundTaskUUID, time.Now().UnixMilli()); execErr != nil {
		return "", "", "", execErr
	}
	return w.tr(ctx, "remote_wechat_done_confirm", i18n.Params{"Task": title}), state.BoundTaskUUID, "", nil
}

// executeCompleteTask 真正执行完成：业务核心下沉到 SkillSurface.WechatCompleteTask。
func (w *wechatRemote) executeCompleteTask(ctx context.Context, taskUUID string) (string, string, string, error) {
	if err := w.ensureAliases(ctx); err != nil {
		return "", "", "", err
	}
	if _, execErr := w.db.ExecContext(ctx, `UPDATE gt_remote_wechat_state SET bound_task_uuid='',
		pending_cancel_task_uuid='', pending_done_task_uuid='', updated_at=? WHERE id=1`, time.Now().UnixMilli()); execErr != nil {
		return "", "", "", execErr
	}
	text, err := w.skillSurface.WechatCompleteTask(w.wechatCtx(ctx), taskUUID)
	if err != nil {
		return "", "", "", err
	}
	return text, "", "", nil
}

// ============================================================================
// 计划 §五 / §八 B4：本地快路径指令。
// 共享 SkillSurface 的实现，避免与 cli-skill 接口的 lookup/write 漂移。
// ============================================================================

// normalizeCommandAlias 将命令别名标准化为内部处理名称。
// 例如："/teamstask" → "/tt", "/projects" → "/pro"
func normalizeCommandAlias(cmd string) string {
	switch cmd {
	case "/teamstask":
		return "/tt"
	case "/projects":
		return "/pro"
	case "/cli-discovery":
		return "/clis"
	case "/cli-models":
		return "/clim"
	case "/pipelines":
		return "/pls"
	case "/expert-groups":
		return "/egs"
	case "/vibecodinglist":
		return "/vcl"
	case "/codex-capability":
		return "/vcl"
	case "/switch-model":
		return "/swm"
	case "/switch-run":
		return "/swr"
	case "/switch-cli":
		return "/swcli"
	case "/task-info":
		return "/ti"
	case "/create-task":
		return "/ct"
	// /ctt 才是「按参考任务创建团队工作」的入口；微信侧一直缺这条快路径，
	// SkillSurface 上 W2 (`createTaskFromTemplate`) 已实现完整业务，
	// 这里只暴露一个别名 + 强制走两个参数的命令。
	case "/create-team-task":
		return "/ctt"
	}
	return cmd
}

// wechatArgs 是 -key=value 形式的参数解析结果。
type wechatArgs map[string]string

// wechatPositional 用于支持 "/ti 1" / "/n 1" 这类"裸数字"参数。
type wechatPositional struct {
	Head string
	Rest string
}

// parseArgs 同时支持 -key=value 与 -flag 形式，并把多余的非 flag 文本放在 rest。
// 解析时把 -key=value、-key value 都识别为同一 key；非 - 开头的 token 视为裸 ID。
func parseArgs(input string) (wechatArgs, string) {
	args := wechatArgs{}
	restTokens := []string{}
	parts := strings.Fields(input)
	for i := 0; i < len(parts); i++ {
		tok := parts[i]
		if !strings.HasPrefix(tok, "-") {
			restTokens = append(restTokens, tok)
			continue
		}
		key := strings.TrimPrefix(tok, "-")
		if eq := strings.IndexByte(key, '='); eq >= 0 {
			args[key[:eq]] = key[eq+1:]
			continue
		}
		if i+1 < len(parts) && !strings.HasPrefix(parts[i+1], "-") {
			args[key] = parts[i+1]
			i++
			continue
		}
		args[key] = "true"
	}
	return args, strings.Join(restTokens, " ")
}

func (a wechatArgs) get(key string) string {
	if v, ok := a[key]; ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func (a wechatArgs) intValue(key string) (int, bool) {
	v := a.get(key)
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

// firstTaskRef 优先取 -id=，其次裸数字（兼容 `/n 1` / `/ti 1`）。
func firstTaskRef(args wechatArgs, rest string) string {
	if v := args.get("id"); v != "" {
		return v
	}
	if rest != "" {
		return strings.TrimSpace(rest)
	}
	return ""
}

// wechatCtx 构造微信快路径专用的 ctx，给 SkillSurface 用。
//
//  1. locale：统一走 i18n.ContextWithLocale。SkillSurface.trFromCtx 读的是
//     i18n.LocaleFromContext，两边必须用同一套 key，否则微信侧恒回落 zh-CN。
//
//  2. 会话：**不经 ctx 注入**，而是由 server.go 用 currentSession() + sessionMatchesConfig()
//     合出 getter、经 wechatRemote.attachAccountSession 透传给 SkillSurface，再由
//     SkillSurface.sessionFromCtx 读取（skillsurface.go:1933-1942）。
//     这是 /tt 可用的**唯一**会话通道：全仓已无任何 ctx 会话读取点（旧的
//     wechatSessionKey 字符串常量已删除），若在这里另写一个 ctx key，就等于把
//     本次刚拆掉的「locale/会话双轨」原样重建一遍 —— 两边互不可见、静默失效。
//     因此 wechatCtx 只负责 locale，会话可用性由 getter 决定。
func (w *wechatRemote) wechatCtx(ctx context.Context) context.Context {
	state, err := w.readState(ctx)
	if err == nil && state.Locale != "" {
		ctx = i18n.ContextWithLocale(ctx, state.Locale)
	}
	return ctx
}

// wechatSessionAvailable 已删除：会话可用性无需在本文件再判一次 ——
// SkillSurface.WechatFormatTeamWork（skillsurface.go:1588-1595）在 getter 返回 nil 时
// 已经产出 localserver_skill_cloud_login_required 文案，再包一层预检查只会把同一
// 判断写两遍。会话通道就是 server.go 注入的 getter，本文件不参与。

// handleFastPathCommand 命中计划 §五的命令表；命中后立即同步返回，不创建任务、不拉 CLI。
func (w *wechatRemote) handleFastPathCommand(ctx context.Context, command, params string) (bool, string) {
	if w.skillSurface == nil {
		return false, ""
	}
	// 标准化命令别名
	command = normalizeCommandAlias(command)
	args, rest := parseArgs(params)
	execCtx := w.wechatCtx(ctx)
	switch command {
	case "/task", "/t", "/T":
		// 任务列表：active(默认/-r) / -a 全部 / -f 已完成 / -e 已阻塞。
		status := "active"
		switch params {
		case "", "-r":
		case "-a":
			status = ""
		case "-f":
			status = "done"
		case "-e":
			status = "blocked"
		default:
			return true, w.tr(ctx, "remote_wechat_params_invalid", i18n.Params{"Help": w.tr(ctx, "remote_wechat_help", nil)})
		}
		out, err := w.skillSurface.WechatListTasks(execCtx, status)
		if err != nil {
			return true, w.wechatSkillFailure(ctx, out, err)
		}
		return true, out
	case "/exit", "/q", "/退出", "退出":
		// 退出当前任务：state 处理 + 列表收尾，业务核心下沉到 SkillSurface。
		reply, _, _, _ := w.handleExitTask(execCtx)
		return true, reply
	case "/c", "/cancel", "/取消", "取消":
		// 取消执行：force=true 跳过二次确认走 SkillSurface.WechatCancelTask，否则写 pending。
		reply, _, _, _ := w.handleCancelTask(execCtx, params == "-f")
		return true, reply
	case "/r", "/resume", "/继续", "继续":
		// 恢复执行：force=true 直接走 SkillSurface.WechatResumeTask，否则写 pending。
		reply, _, _, _ := w.handleResumeTask(execCtx, params == "-f")
		return true, reply
	case "/d", "/done", "/完成", "完成":
		// 标记完成：force=true 直接走 SkillSurface.WechatCompleteTask，否则写 pending。
		reply, _, _, _ := w.handleCompleteTask(execCtx, params == "-f")
		return true, reply
	case "/tt":
		showAll := args.get("a") == "true" || args.get("a") == "1"
		out, err := w.skillSurface.WechatFormatTeamWork(execCtx, showAll)
		if err != nil {
			return true, w.wechatSkillFailure(ctx, out, err)
		}
		return true, out
	case "/pro":
		out, err := w.skillSurface.WechatFormatProjects(execCtx)
		if err != nil {
			return true, w.wechatSkillFailure(ctx, out, err)
		}
		return true, out
	case "/clis":
		out, err := w.skillSurface.WechatFormatCLIs(execCtx)
		if err != nil {
			return true, w.wechatSkillFailure(ctx, out, err)
		}
		return true, out
	case "/clim":
		idx, ok := args.intValue("cid")
		if !ok {
			return true, w.tr(ctx, "remote_wechat_skill_missing_cli_index", nil)
		}
		out, err := w.skillSurface.WechatFormatModels(execCtx, idx)
		if err != nil {
			return true, w.wechatSkillFailure(ctx, out, err)
		}
		return true, out
	case "/pls":
		out, err := w.skillSurface.WechatFormatPipelines(execCtx)
		if err != nil {
			return true, w.wechatSkillFailure(ctx, out, err)
		}
		return true, out
	case "/egs":
		out, err := w.skillSurface.WechatFormatExpertGroups(execCtx)
		if err != nil {
			return true, w.wechatSkillFailure(ctx, out, err)
		}
		return true, out
	case "/vcl":
		out, err := w.skillSurface.WechatFormatVibeTools(execCtx)
		if err != nil {
			return true, w.wechatSkillFailure(ctx, out, err)
		}
		return true, out
	case "/ti":
		ref := firstTaskRef(args, rest)
		if ref == "" {
			return true, w.tr(ctx, "remote_wechat_skill_missing_task_ref", nil)
		}
		out, err := w.skillSurface.WechatFormatTaskDetail(execCtx, ref)
		if err != nil {
			return true, w.wechatSkillFailure(ctx, out, err)
		}
		return true, out
	case "/ct":
		body := createTaskBody{
			Title:       args.get("t"),
			Description: args.get("d"),
			WorkDirs:    splitDirs(args.get("w")),
		}
		if idx, ok := args.intValue("pid"); ok {
			body.ProjectIndex = idx
		}
		for _, tok := range strings.Fields(rest) {
			if n, err := strconv.Atoi(tok); err == nil && n > 0 {
				body.SubprojectIndex = append(body.SubprojectIndex, n)
			}
		}
		if idx, ok := args.intValue("plid"); ok {
			body.PipelineIndex = idx
		}
		if idx, ok := args.intValue("egid"); ok {
			body.ExpertGroupIdx = idx
		}
		if args.get("em") != "" {
			body.ExecutionMode = args.get("em")
		}
		if args.get("et") != "" {
			body.ExecutionTool = args.get("et")
		}
		if args.get("emdl") != "" {
			body.ModelName = args.get("emdl")
		}
		if body.Title == "" {
			return true, w.tr(ctx, "remote_wechat_skill_missing_title", nil)
		}
		// 团队导入/参考任务入口不在微信侧提供：-tw / -wiid 已删除（原先 -tw 经
		// mapTeamWorkType 恒返回空、-wiid 单独赋值只会把请求推进半条团队导入链路）。
		// 团队导入走 /ctt（按参考任务一键创建）；/ct 仍只做普通建任务。
		out, err := w.skillSurface.WechatCreateTask(execCtx, body)
		if err != nil {
			return true, w.wechatSkillFailure(ctx, out, err)
		}
		return true, out
	case "/ctt":
		// /ctt -tid=<本地任务短 id> -ttid=<我的工作 ttid> [-t=<标题>]
		//
		// 按参考本地任务的配置（项目/流水线/关联项目/工作目录/步骤的 CLI 与模型），
		// 创建一个 source_type=cloud 的团队工作。专门解决「/ct 输入参数太多无法在
		// 微信端手动键入」的场景 —— 用户只要拿到 /tt 输出里 `<...>` 的一段 +
		// /ti 里的一条参考任务即可。
		//
		// 注意：参考任务用 `-tid=`（与 /ct/-ttid 区分开），不要复用 firstTaskRef
		// —— 那个 helper 只识别 `-id=`，会把用户传入的 -tid= 误判为「缺参」。
		// -ttid 取的是 ttid（云端 work item 的对外稳定标识），与列表序号解耦
		// —— /tt 输出顺序改变不影响引用。
		templateRef := args.get("tid")
		if templateRef == "" {
			return true, w.tr(ctx, "remote_wechat_skill_missing_template_ref", nil)
		}
		workItemID, ok := firstTeamWorkID(args, rest)
		if !ok {
			return true, w.tr(ctx, "remote_wechat_skill_missing_team_work_index", nil)
		}
		out, err := w.skillSurface.WechatCreateTeamTaskFromTemplate(execCtx, templateRef, workItemID, args.get("t"))
		if err != nil {
			return true, w.wechatSkillFailure(ctx, out, err)
		}
		return true, out
	case "/n":
		ref := firstTaskRef(args, rest)
		if ref == "" {
			return true, w.tr(ctx, "remote_wechat_skill_missing_task_ref", nil)
		}
		out, err := w.skillSurface.WechatNextStepByRef(execCtx, ref)
		if err != nil {
			return true, w.wechatSkillFailure(ctx, out, err)
		}
		return true, out
	case "/swm":
		in := WechatSwitchInput{
			Ref:       firstTaskRef(args, rest),
			Mode:      taskruntime.ExecutionModeCLI,
			ModelName: args.get("emdl"),
		}
		// -mid 的读取必须在 -cid 分支之外：/swm 允许只给 -mid（以任务当前 CLI
		// 解析模型序号），历史实现把它写在 -cid 分支内，单独 -mid 完全不被消费，
		// 最终撞到 ValidateCLITarget 报「模型不能为空」。
		mid, midOK, midErr := parseModelIndexArg(args.get("mid"))
		if midErr != nil {
			return true, w.trSkillErr(ctx, midErr)
		}
		if v := args.get("cid"); v != "" {
			// 兼容 -cid= 直接传 cli_type（值形式）；序号留作 -cid=N 用解析。
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				cliType, modelName, mErr := w.resolveModelFromArgs(execCtx, n, args.get("mid"))
				if mErr != nil {
					return true, w.trSkillErr(ctx, mErr)
				}
				in.Tool = cliType
				if in.ModelName == "" {
					in.ModelName = modelName
				}
			} else {
				in.Tool = strings.ToLower(strings.TrimSpace(v))
			}
		} else if midOK {
			// 独立 -mid：CLI 由任务当前执行态决定（SkillSurface 侧解析，不在这里猜）。
			in.ModelIndex = mid
		} else if in.ModelName == "" {
			// -cid / -mid / -emdl 三者皆缺：硬失败并给出明确缺参文案。
			return true, w.tr(ctx, "remote_wechat_skill_missing_model_index", nil)
		}
		if in.Ref == "" {
			return true, w.tr(ctx, "remote_wechat_skill_missing_task_ref", nil)
		}
		out, err := w.skillSurface.WechatSwitchExecution(execCtx, in)
		if err != nil {
			return true, w.wechatSkillFailure(ctx, out, err)
		}
		return true, out
	case "/swr":
		mode := mapSwitchMode(args.get("t"))
		if mode == "" {
			return true, w.tr(ctx, "remote_wechat_skill_missing_mode", nil)
		}
		in := WechatSwitchInput{
			Ref:             firstTaskRef(args, rest),
			Mode:            mode,
			ModelName:       args.get("emdl"),
			ReplaceExisting: true,
		}
		if idx, ok := args.intValue("plid"); ok {
			in.PipelineIdx = idx
		}
		if idx, ok := args.intValue("egid"); ok {
			in.ExpertGroupIdx = idx
		}
		if idx, ok := args.intValue("vid"); ok {
			in.VibeIdx = idx
		}
		if in.Ref == "" {
			return true, w.tr(ctx, "remote_wechat_skill_missing_task_ref", nil)
		}
		out, err := w.skillSurface.WechatSwitchExecution(execCtx, in)
		if err != nil {
			return true, w.wechatSkillFailure(ctx, out, err)
		}
		return true, out
	case "/swcli":
		in := WechatSwitchInput{
			Ref:       firstTaskRef(args, rest),
			Mode:      taskruntime.ExecutionModeCLI,
			ModelName: args.get("emdl"),
		}
		// /swcli 是「切到某个 CLI 的某个模型」：-cid 与 -mid 都必填（计划 §5.5
		// 缺参硬失败、不默认补全）。此前 -mid 缺失时按 0 传给 resolveModelIndex，
		// 用户看到的是「模型序号无效」这种与真实原因（没给参数）不符的裸错误。
		cidRaw := args.get("cid")
		if cidRaw == "" {
			return true, w.tr(ctx, "remote_wechat_skill_missing_cli_index", nil)
		}
		if cliIdx, cidErr := strconv.Atoi(cidRaw); cidErr == nil && cliIdx > 0 {
			switch {
			case in.ModelName != "":
				// -emdl 是显式模型名替代：只借 CLI 序号取 tool，模型名以显式值为准。
				cliDef, defErr := w.skillSurface.resolveCLIIndex(execCtx, cliIdx)
				if defErr != nil {
					return true, w.trSkillErr(ctx, defErr)
				}
				in.Tool = cliDef.Type
			case args.get("mid") == "":
				return true, w.tr(ctx, "remote_wechat_skill_missing_model_index", nil)
			default:
				cliType, modelName, mErr := w.resolveModelFromArgs(execCtx, cliIdx, args.get("mid"))
				if mErr != nil {
					return true, w.trSkillErr(ctx, mErr)
				}
				in.Tool = cliType
				in.ModelName = modelName
			}
		} else if in.ModelName != "" {
			// -cid= 直接是 cli_type 名称：无法据此解析模型序号，模型名必须显式给出。
			in.Tool = strings.ToLower(strings.TrimSpace(cidRaw))
		} else {
			return true, w.tr(ctx, "remote_wechat_skill_missing_cli_index", nil)
		}
		if in.Ref == "" {
			return true, w.tr(ctx, "remote_wechat_skill_missing_task_ref", nil)
		}
		out, err := w.skillSurface.WechatSwitchExecution(execCtx, in)
		if err != nil {
			return true, w.wechatSkillFailure(ctx, out, err)
		}
		return true, out
	}
	return false, ""
}

// wechatSkillFailure 渲染「SkillSurface 调用失败」时给微信用户的文案。
//
// SkillSurface 的错误返回是双通道：第 1 返回值已经是本地化文案（wechatErrorFromErr），
// 第 2 返回值是原始 error。历史实现在 err 非空时**丢弃第 1 返回值**、改用 err.Error()
// 重新拼装，而 errSkillBadRequest.Error() / coreStatusError.Error() 返回的都是 i18n
// key 本身（如 localserver_skill_cli_required），于是裸键直接展示给用户。
// 这里统一以「已算好的本地化文案」为准，绝不放弃它。
func (w *wechatRemote) wechatSkillFailure(ctx context.Context, localized string, err error) string {
	if err == nil {
		return localized
	}
	if strings.TrimSpace(localized) != "" && !bareI18nKeyPattern.MatchString(strings.TrimSpace(localized)) {
		return localized
	}
	return w.trSkillErr(ctx, err)
}

// bareI18nKeyPattern 识别「整段文本就是一个裸翻译键」——正常用户文案不会长这样。
var bareI18nKeyPattern = regexp.MustCompile(`^(localserver|remote_wechat|common)_[a-z0-9_]+$`)

// localizedDetail 保证填入 {{.Detail}} 的内容不会是一个裸翻译键。
//
// 可读的领域错误（如 dispatchToCLI 的中文提示）原样保留，只把「整段就是一个 key」
// 的情形翻译掉 —— 这样既修掉泄漏，又不改变既有文案模板与可读内容。
func (w *wechatRemote) localizedDetail(ctx context.Context, detail string) string {
	trimmed := strings.TrimSpace(detail)
	if bareI18nKeyPattern.MatchString(trimmed) {
		return w.tr(ctx, trimmed, nil)
	}
	return detail
}

// trSkillErr 把 SkillSurface 返回的错误渲染成给微信用户看的文案。
//
// errSkillBadRequest 的 Error() 返回的是 i18n key 本身（如
// localserver_skill_model_index_invalid），直接塞进 remote_wechat_skill_call_failed
// 会把裸 key 吐给用户。这里按 key 走一次本地化，与 SkillSurface.wechatErrorFromErr
// 同一口径，保持 skill 层「只用 key 表达原因」的约定不被破坏。
func (w *wechatRemote) trSkillErr(ctx context.Context, err error) string {
	if err == nil {
		return ""
	}
	var badRequest *errSkillBadRequest
	if errors.As(err, &badRequest) {
		return w.tr(ctx, badRequest.key, nil)
	}
	// gin-free 核心层的分类错误显式标注 key；coreStatusError.Error() 在该 key 非空时
	// 正是回落到 key 本身（tasks_core.go），不在此翻译同样会泄漏。
	var statusErr *coreStatusError
	if errors.As(err, &statusErr) {
		if statusErr.key != "" {
			return w.tr(ctx, statusErr.key, nil)
		}
		return w.tr(ctx, "localserver_internal_error", nil)
	}
	var invalidInput *errSkillInvalidInput
	if errors.As(err, &invalidInput) {
		if key, _ := i18n.Key(invalidInput.err); key != "" && key != "localserver_internal_error" {
			return w.tr(ctx, key, nil)
		}
		return w.tr(ctx, "localserver_internal_error", nil)
	}
	// 兜底：已是可读文案（如 dispatchToCLI 的中文提示）原样展示；整段就是一个裸键时
	// 再翻译一次，保证任何路径都不会把 key 名呈现给用户。
	detail := strings.TrimSpace(err.Error())
	if bareI18nKeyPattern.MatchString(detail) {
		return w.tr(ctx, detail, nil)
	}
	return w.tr(ctx, "remote_wechat_skill_call_failed", i18n.Params{"Detail": detail})
}

// resolveModelFromArgs 把 -cid / -mid 序号解析为 cli_type / model。
//
// -mid 缺失时按 0 处理是历史缺陷：resolveModelIndex 要求 ≥1，最终抛的是
// 「模型序号无效」这种与真实原因（压根没给参数）不符的裸错误。现在缺参硬失败，
// 返回 errSkillBadRequest(remote_wechat_skill_missing_model_index)，
// 调用方经 wechatErrorFromErr 得到明确的缺参文案。
func (w *wechatRemote) resolveModelFromArgs(ctx context.Context, cliIndex int, modelIndex string) (string, string, error) {
	mid, ok, err := parseModelIndexArg(modelIndex)
	if err != nil {
		return "", "", err
	}
	if !ok {
		return "", "", newSkillBadRequest("remote_wechat_skill_missing_model_index")
	}
	return w.skillSurface.resolveModelIndex(ctx, cliIndex, mid)
}

// parseModelIndexArg 解析 -mid=<N>。
// ok=false 表示参数缺失；err 非 nil 表示参数存在但不合法（非正整数）。
func parseModelIndexArg(raw string) (int, bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 0, false, newSkillBadRequest("localserver_skill_model_index_invalid")
	}
	return n, true, nil
}

func splitDirs(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	for _, sep := range []string{",", ";"} {
		if strings.Contains(raw, sep) {
			parts := strings.Split(raw, sep)
			out := make([]string, 0, len(parts))
			for _, p := range parts {
				if p = strings.TrimSpace(p); p != "" {
					out = append(out, p)
				}
			}
			return out
		}
	}
	return []string{raw}
}

func mapSwitchMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "vcl", "vibe", "vibe_coding":
		return taskruntime.ExecutionModeVibeCoding
	case "cli":
		return taskruntime.ExecutionModeCLI
	case "pls", "pipeline":
		return taskruntime.ExecutionModePipeline
	case "egs", "expert_group":
		return taskruntime.ExecutionModeExpertGroup
	}
	return ""
}

// firstTeamWorkID 读取 `-ttid=<我的工作 ttid>`，未传 / 全空白时返回 ("", false)。
//
// 与显示端（WechatFormatTeamWork）严格同源：-ttid 取的就是 /tt 输出里 `<...>`
// 这一段。ttid 是云端 work item 的对外稳定标识，不会随列表过滤重排漂移，因此
// 不再接受「裸数字当作列表序号」的兜底（之前会让用户在 /tt 输出与 -ttid 之间
// 反复换算，且序号本身就是过滤后位置、含混不稳定）。
func firstTeamWorkID(args wechatArgs, rest string) (string, bool) {
	_ = rest
	if v := strings.TrimSpace(args.get("ttid")); v != "" {
		return v, true
	}
	return "", false
}
