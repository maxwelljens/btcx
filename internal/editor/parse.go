package editor

import (
	"fmt"
	"strings"

	"btcx/internal/model"
)

// maxHeaderLine bounds a single draft header line; a longer line is rejected
// instead of being folded into the message.
const maxHeaderLine = 64 << 10

// ParseDraft converts draft text into a message skeleton. Header lines run
// until the first blank line; continuation lines starting with space or tab are
// folded into the previous value. Only To, Cc, Bcc and Subject are kept, and
// CR, LF and NUL are stripped from their values, so a draft can never inject
// fields into the message btcx later composes.
func ParseDraft(text string) (*model.Message, error) {
	msg := &model.Message{Header: model.NewHeader()}

	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(text, "\n")

	fields := make([]model.Field, 0, 8)
	body := len(lines)
headers:
	for i, line := range lines {
		if line == "" {
			body = i + 1
			break headers
		}
		if len(line) > maxHeaderLine {
			return nil, fmt.Errorf("editor: draft header line %d too long (%d bytes)", i+1, len(line))
		}
		if line[0] == ' ' || line[0] == '\t' {
			if len(fields) == 0 {
				return nil, fmt.Errorf("editor: draft line %d: continuation without a header", i+1)
			}
			fields[len(fields)-1].Value += " " + strings.TrimSpace(line)
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("editor: draft line %d: malformed header", i+1)
		}
		fields = append(fields, model.Field{Key: key, Value: value})
	}

	for _, f := range fields {
		switch strings.ToLower(f.Key) {
		case "to", "cc", "bcc", "subject":
			msg.Header.Set(f.Key, cleanValue(f.Value))
		}
	}
	if body < len(lines) {
		msg.Plain = strings.TrimSuffix(strings.Join(lines[body:], "\n"), "\n")
	}
	return msg, nil
}

// cleanValue trims a header value and drops characters that could start a new
// field downstream.
func cleanValue(v string) string {
	v = strings.TrimSpace(v)
	return strings.NewReplacer("\r", "", "\n", "", "\x00", "").Replace(v)
}
