//go:build windows

package executor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func cliProbeCommand(ctx context.Context, execPath string, args []string) (*exec.Cmd, error) {
	ext := strings.ToLower(filepath.Ext(execPath))
	if ext != ".cmd" && ext != ".bat" {
		cmd := exec.CommandContext(ctx, execPath, args...)
		CreateProcessGroup(cmd)
		return cmd, nil
	}
	// CreateProcess 不能直接启动批处理文件。这里只有固定的发现参数，
	// 不允许把 prompt、换行、变量展开或 shell 操作符带入 cmd.exe。
	if strings.ContainsAny(execPath, "\"%\r\n\x00") {
		return nil, fmt.Errorf("CLI 探测脚本路径无效")
	}
	quoted := []string{"\"" + execPath + "\""}
	for _, arg := range args {
		if strings.ContainsAny(arg, "\"%\r\n\x00&|<>^!") {
			return nil, fmt.Errorf("CLI 探测参数无效")
		}
		quoted = append(quoted, "\""+arg+"\"")
	}
	commandProcessor := strings.TrimSpace(os.Getenv("ComSpec"))
	if !filepath.IsAbs(commandProcessor) {
		root := os.Getenv("SystemRoot")
		if root == "" {
			root = `C:\Windows`
		}
		commandProcessor = filepath.Join(root, "System32", "cmd.exe")
	}
	cmd := exec.CommandContext(ctx, commandProcessor)
	CreateProcessGroup(cmd)
	cmd.SysProcAttr.CmdLine = "\"" + commandProcessor + "\" /d /s /v:off /c \"" + strings.Join(quoted, " ") + "\""
	return cmd, nil
}
