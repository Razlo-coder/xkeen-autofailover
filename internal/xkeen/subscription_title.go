package xkeen

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Happ accepts profile-title either in the HTTP response or in the comment
// preamble of a plain/base64 subscription. A response header takes precedence.
func subscriptionProfileTitle(header string, body []byte) string {
	if title := cleanProfileTitle(header); title != "" {
		return title
	}
	content := strings.TrimPrefix(strings.TrimSpace(string(body)), "\ufeff")
	if decoded, err := decodeShareBase64(content); err == nil && utf8.Valid(decoded) {
		content = string(decoded)
	}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "#") {
			break
		}
		key, value, ok := strings.Cut(strings.TrimPrefix(line, "#"), ":")
		if ok && strings.EqualFold(strings.TrimSpace(key), "profile-title") {
			return cleanProfileTitle(value)
		}
	}
	return ""
}

func cleanProfileTitle(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= len("base64:") && strings.EqualFold(value[:len("base64:")], "base64:") {
		decoded, err := decodeShareBase64(strings.TrimSpace(value[len("base64:"):]))
		if err != nil {
			return ""
		}
		value = strings.TrimSpace(string(decoded))
	}
	if !utf8.ValidString(value) || value == "" || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) > 40 {
		value = string(runes[:40])
	}
	return value
}
