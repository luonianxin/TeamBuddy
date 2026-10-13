package executor

import (
	"errors"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	errShimUnrecognized = errors.New("shim_unrecognized")
	errNodeNotFound     = errors.New("node_not_found")
)

// NpmShim 是从 npm .cmd 包装脚本解析出的真实启动方式。
type NpmShim struct {
	Exe        string
	PrefixArgs []string
	Kind       string
}

var (
	nodeShimLine = regexp.MustCompile(`(?m)^\s*(?:endLocal & goto #_undefined_# 2>NUL \|\| title %COMSPEC% & )?(?:"%_prog%"|node|"%~dp0\\node\.exe")\s+((?:--?[A-Za-z0-9][A-Za-z0-9_=.:-]*\s+)*)"(%dp0%|%~dp0)\\([^"%]+)"\s+%\*\s*$`)
	exeShimLine  = regexp.MustCompile(`(?m)^\s*@?"(%dp0%|%~dp0)\\([^"%]+)"\s+%\*\s*$`)
)

// parseNpmShim 按 cmd-shim 模板解析 .cmd 内容。dir 是 .cmd 所在目录。
func parseNpmShim(content, dir string) (NpmShim, error) {
	if len(content) > 4<<10 {
		return NpmShim{}, errShimUnrecognized
	}
	text := strings.ReplaceAll(content, "\r\n", "\n")
	if !strings.Contains(text, `SET dp0=%~dp0`) && !strings.Contains(text, `%~dp0`) {
		return NpmShim{}, errShimUnrecognized
	}
	matches := nodeShimLine.FindAllStringSubmatch(text, -1)
	if len(matches) > 0 {
		opts := strings.TrimSpace(matches[0][1])
		rel := matches[0][3]
		for _, match := range matches[1:] {
			if strings.TrimSpace(match[1]) != opts || match[3] != rel {
				return NpmShim{}, errShimUnrecognized
			}
		}
		target := filepath.Clean(filepath.Join(dir, filepath.FromSlash(strings.ReplaceAll(rel, `\`, `/`))))
		if !strings.Contains(target, string(filepath.Separator)+"node_modules"+string(filepath.Separator)) {
			return NpmShim{}, errShimUnrecognized
		}
		var prefix []string
		if opts != "" {
			prefix = strings.Fields(opts)
		}
		prefix = append(prefix, target)
		return NpmShim{PrefixArgs: prefix, Kind: "node"}, nil
	}
	exeMatches := exeShimLine.FindAllStringSubmatch(text, -1)
	if len(exeMatches) != 1 {
		return NpmShim{}, errShimUnrecognized
	}
	rel := exeMatches[0][2]
	if !strings.EqualFold(filepath.Ext(rel), ".exe") {
		return NpmShim{}, errShimUnrecognized
	}
	target := filepath.Clean(filepath.Join(dir, filepath.FromSlash(strings.ReplaceAll(rel, `\`, `/`))))
	if !strings.Contains(target, string(filepath.Separator)+"node_modules"+string(filepath.Separator)) {
		return NpmShim{}, errShimUnrecognized
	}
	return NpmShim{Exe: target, Kind: "native"}, nil
}
