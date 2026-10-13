package localserver

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"goteams-client/internal/i18n"
	"goteams-client/internal/taskruntime"
)

func (h *TasksHandler) renameTask(c *gin.Context) {
	var body struct {
		Title string `json:"title"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		i18n.LocalServerError(c, http.StatusBadRequest, err)
		return
	}
	taskUUID := c.Param("uuid")
	if orchestrator := h.orchestrator(); orchestrator != nil {
		lease, err := orchestrator.BeginTaskExecutionSwitch(taskUUID)
		if err != nil {
			i18n.LocalServerError(c, http.StatusInternalServerError, err)
			return
		}
		defer lease.Close()
	}
	err := taskruntime.NewService(h.taskDB(), h.taskRoot).Rename(c.Request.Context(), taskUUID, body.Title)
	if err != nil {
		switch {
		case errors.Is(err, taskruntime.ErrInvalidTaskTitle):
			i18n.Error(c, http.StatusBadRequest, "localserver_task_title_invalid", "task_title_invalid")
		case errors.Is(err, taskruntime.ErrNotFound):
			i18n.Error(c, http.StatusNotFound, "localserver_task_not_found", "task_not_found")
		default:
			i18n.LocalServerError(c, http.StatusInternalServerError, err)
		}
		return
	}
	h.pushTaskSync(taskUUID)
	h.notifyTaskChanged(taskUUID)
	c.JSON(http.StatusOK, gin.H{"uuid": taskUUID, "title": strings.TrimSpace(body.Title)})
}
