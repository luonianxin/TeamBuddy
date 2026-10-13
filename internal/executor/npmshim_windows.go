//go:build windows

package executor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func unwrapCommand(path string, args []string) (string, []string, error) {
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".cmd" && ext != ".bat" {
		return path, nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil, err
	}
	shim, parseErr := parseNpmShim(string(data), filepath.Dir(path))
	if parseErr == nil && shim.Kind == "native" && shim.Exe != "" {
		if _, statErr := os.Stat(shim.Exe); statErr == nil {
			return shim.Exe, nil, nil
		}
	}
	if parseErr == nil && shim.Kind == "node" {
		node := filepath.Join(filepath.Dir(path), "node.exe")
		if _, statErr := os.Stat(node); statErr != nil {
			found, lookErr := exec.LookPath("node")
			if lookErr != nil {
				return "", nil, errNodeNotFound
			}
			node = found
		}
		// PATH 上的 node 可能本身是 .cmd 包装（nvs、部分版本管理器），参数仍会经过 cmd.exe。
		if nodeExt := strings.ToLower(filepath.Ext(node)); nodeExt == ".cmd" || nodeExt == ".bat" {
			if err := validateWrappedArgs(node, append(append([]string{}, shim.PrefixArgs...), args...)); err != nil {
				return "", nil, err
			}
		}
		return node, shim.PrefixArgs, nil
	}
	if err := validateWrappedArgs(path, args); err != nil {
		return "", nil, err
	}
	return path, nil, nil
}
