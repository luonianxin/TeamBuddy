//go:build windows

package executor

import (
	"fmt"
	"path/filepath"
	"strings"
)

func validateWrappedArgs(path string, args []string) error {
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".cmd" && ext != ".bat" {
		return nil
	}
	total := len(path)
	for _, arg := range args {
		total += len(arg) + 1
		if strings.ContainsAny(arg, "&|<>^%!\r\n\"") {
			return fmt.Errorf("启动参数含有 cmd 特殊字符，已拒绝执行 %s", filepath.Base(path))
		}
	}
	if total > 7000 {
		return fmt.Errorf("命令行超过 cmd 长度上限，已拒绝执行 %s", filepath.Base(path))
	}
	return nil
}
