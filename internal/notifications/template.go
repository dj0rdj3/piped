// Package notifications manages reusable webhook targets and completion delivery.
package notifications

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

type Event struct {
	Host, Job, Status, Output string
	Secrets                   map[string]string
}

// Render substitutes once: values containing dollar signs never become templates.
// JSON values are escaped as string contents; placeholders belong inside quotes.
func Render(body, contentType string, event Event) (string, error) {
	// A valid JSON template can contain dollar placeholders only inside strings.
	// Validate before expansion so empty/numeric values cannot hide a misplaced
	// placeholder or complete a partial JSON escape sequence.
	if contentType == "application/json" && !json.Valid([]byte(body)) {
		return "", invalid("provide valid JSON with template variables inside strings")
	}
	var result strings.Builder
	for index := 0; index < len(body); {
		if body[index] != '$' {
			result.WriteByte(body[index])
			index++
			continue
		}
		index++
		if index < len(body) && body[index] == '$' {
			result.WriteByte('$')
			index++
			continue
		}
		start := index
		index = templateNameEnd(body, index)
		token := strings.ToLower(body[start:index])
		var value string
		switch token {
		case "host":
			value = event.Host
		case "job":
			value = event.Job
		case "status":
			value = event.Status
		case "output":
			value = event.Output
			if index < len(body) && body[index] == ':' {
				index++
				start = index
				for index < len(body) && body[index] >= '0' && body[index] <= '9' {
					index++
				}
				count, err := strconv.Atoi(body[start:index])
				if err != nil || count < 1 || count > 1000 {
					return "", invalid("output line count must be between 1 and 1000")
				}
				value = tailLines(value, count)
			}
		default:
			var exists bool
			value, exists = lookupSecret(event.Secrets, token)
			if !exists {
				return "", invalid(fmt.Sprintf("unknown template variable $%s; use an event variable, a named secret, or $$", token))
			}
		}
		value = strings.ToValidUTF8(value, "\uFFFD")
		if contentType == "application/json" {
			encoded, _ := json.Marshal(value)
			value = string(encoded[1 : len(encoded)-1])
		}
		result.WriteString(value)
		if result.Len() > 1024*1024 {
			return "", invalid("rendered notification exceeds 1 MiB")
		}
	}
	if result.Len() > 1024*1024 {
		return "", invalid("rendered notification exceeds 1 MiB")
	}
	rendered := result.String()
	if !utf8.ValidString(rendered) {
		return "", invalid("body must be valid UTF-8")
	}
	if contentType == "application/json" && !json.Valid([]byte(rendered)) {
		return "", invalid("provide valid JSON with template variables inside strings")
	}
	return rendered, nil
}

func tailLines(output string, count int) string {
	output = strings.ReplaceAll(output, "\r\n", "\n")
	output = strings.TrimSuffix(output, "\n")
	for index := len(output) - 1; index >= 0; index-- {
		if output[index] == '\n' {
			count--
			if count == 0 {
				return output[index+1:]
			}
		}
	}
	return output
}
