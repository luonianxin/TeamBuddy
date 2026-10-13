// Package redact 在长输出落库和留存前去掉常见密钥。这是尽力而为，不保证覆盖全部秘密。
package redact

import (
	"regexp"
	"strings"
)

var patterns = []struct {
	name string
	re   *regexp.Regexp
}{
	{"bearer", regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._\-+/=]{16,}`)},
	{"sk", regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}`)},
	{"github", regexp.MustCompile(`\b(?:ghp_|github_pat_)[A-Za-z0-9_]{16,}\b`)},
	{"aws", regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`)},
	{"jwt", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`)},
	{"url", regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^/\s:]+:[^@\s/]+@`)},
	// 不用 (?i)：否则 token_count=12、monkey=1 这类普通输出也会被当成密钥。
	{"env", regexp.MustCompile(`(?m)^([A-Z0-9_]*(?:KEY|TOKEN|SECRET|PASSWORD)[A-Z0-9_]*)\s*=\s*\S+`)},
	{"pem", regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`)},
}

// Redact 替换常见密钥，并报告是否命中。
func Redact(input string) (string, bool) {
	hit := false
	out := input
	for _, pattern := range patterns {
		if !pattern.re.MatchString(out) {
			continue
		}
		hit = true
		name := pattern.name
		out = pattern.re.ReplaceAllStringFunc(out, func(string) string {
			return "[REDACTED:" + name + "]"
		})
	}
	if strings.Contains(out, "[REDACTED:") {
		hit = true
	}
	return out, hit
}
