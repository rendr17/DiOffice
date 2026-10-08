package events

import (
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"
)

var ErrUnsafeEvent = errors.New("unsafe or unsupported persisted event")

var redactionPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?:gh[pousr]_[A-Za-z0-9_]{20,}|github_pat_[A-Za-z0-9_]{20,}|sk-(?:proj-)?[A-Za-z0-9_-]{20,}|(?:AKIA|ASIA)[A-Z0-9]{16})`),
	regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`),
	regexp.MustCompile(`(?i)\b(?:Bearer|Basic)\s+[A-Za-z0-9._~+/=-]+`),
	regexp.MustCompile(`(?i)\b[\w-]*(?:password|passwd|secret|token|api[_-]?key|authorization|cookie|access[_-]?key)[\w-]*\s*[:=]\s*(?:"[^"\r\n]*"|'[^'\r\n]*'|[^\s,;]+)`),
	regexp.MustCompile(`(?s)-----BEGIN (?:[A-Z ]*PRIVATE KEY)-----.*?-----END (?:[A-Z ]*PRIVATE KEY)-----`),
	regexp.MustCompile(`(?i)(?:s3://[^\s"'<>]+|task-reference-images/[^\s"'<>]+)`),
	regexp.MustCompile(`[A-Za-z]:[\\/][^\s"'<>]+|\\\\[^\s"'<>]+`),
	regexp.MustCompile(`/(?:home|Users|var|tmp|etc|srv|mnt|root|opt|private|internal)/[^\s"'<>]+`),
}
var eventURLPattern = regexp.MustCompile(`https?://[^\s"'<>]+`)

// RedactText filters recognizable credentials, private object references and host paths.
// It cannot recognize an arbitrary secret without a credential pattern or context.
func RedactText(text string) string {
	text = eventURLPattern.ReplaceAllStringFunc(text, func(raw string) string {
		u, err := url.Parse(raw)
		if err != nil || u.User != nil {
			return "[REDACTED]"
		}
		for key := range u.Query() {
			lower := strings.ToLower(key)
			if strings.HasPrefix(lower, "x-amz-") || strings.Contains(lower, "signature") || strings.Contains(lower, "token") || lower == "sig" || lower == "awsaccesskeyid" {
				return "[REDACTED]"
			}
		}
		return raw
	})
	for _, pattern := range redactionPatterns {
		text = pattern.ReplaceAllString(text, "[REDACTED]")
	}
	return text
}

func boundedText(text string, limit int) string {
	text = RedactText(text)
	runes := []rune(text)
	if len(runes) > limit {
		text = string(runes[:limit])
	}
	return text
}

// SanitizeData is also used before persisting newly created task facts. It projects
// only fields in the versioned event contract; the task and its request hash stay intact.
func SanitizeData(eventType string, raw json.RawMessage) (json.RawMessage, error) {
	return sanitizePayload(eventType, raw)
}
