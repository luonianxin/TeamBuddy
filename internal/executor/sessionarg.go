package executor

import "strings"

// SessionArg 构造带值的会话参数。
// equals 为真时使用 --name=value，避免可选值参数把以 - 开头的值当成另一个旗标。
// 值以 - 开头、含空白或引号时返回空，调用方不得把这样的 ID 放进命令行。
func SessionArg(equals bool, name, id string) []string {
	id = strings.TrimSpace(id)
	if id == "" || strings.HasPrefix(id, "-") || strings.ContainsAny(id, " \t\r\n\"") {
		return nil
	}
	if equals {
		return []string{name + "=" + id}
	}
	return []string{name, id}
}

// SupportsPresetSession 表示首轮可以由调用方指定会话 ID。
func SupportsPresetSession(cliType string) bool {
	switch cliType {
	case CLITypeClaude, CLITypeQoder, CLITypeQoderCN, CLITypePi, CLITypeCopilot:
		return true
	default:
		return false
	}
}
