package ai

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/atobeach/reef/ordered"
)

// Message is one history entry sent to a provider: a UserMessage, an
// AssistantMessage or a ToolResultMessage.
type Message interface {
	isMessage()
}

// UserMessage is a user turn.
type UserMessage struct {
	Content string
}

// AssistantMessage is a model turn: its text and the tool calls it made.
type AssistantMessage struct {
	Content   string
	ToolCalls []ToolCall
}

// ToolResultMessage answers the previous assistant message's tool calls.
type ToolResultMessage struct {
	Results []ToolResult
}

func (UserMessage) isMessage()       {}
func (AssistantMessage) isMessage()  {}
func (ToolResultMessage) isMessage() {}

// decodeArguments decodes a tool call's argument string: anything that is
// not a JSON object is empty.
func decodeArguments(raw string) *ordered.Object {
	if strings.TrimSpace(raw) == "" {
		return ordered.NewObject()
	}

	decoded, err := ordered.Decode([]byte(raw))
	if err != nil {
		return ordered.NewObject()
	}

	if object, ok := decoded.(*ordered.Object); ok {
		return object
	}

	return ordered.NewObject()
}

// unescapeSlashesAndUnicode writes an encoder's escaped slashes and
// \u-escaped non-ASCII characters as themselves, so a structured tool
// result reaches the model readable whichever encoder wrote it.
func unescapeSlashesAndUnicode(encoded string) string {
	var decoded any
	if err := json.Unmarshal([]byte(encoded), &decoded); err != nil {
		return encoded
	}

	var out strings.Builder
	for i := 0; i < len(encoded); i++ {
		c := encoded[i]
		if c != '\\' || i+1 >= len(encoded) {
			out.WriteByte(c)

			continue
		}

		next := encoded[i+1]
		switch {
		case next == '/':
			out.WriteByte('/')
			i++
		case next == 'u' && i+5 < len(encoded):
			code, err := strconv.ParseUint(encoded[i+2:i+6], 16, 16)
			if err != nil || code < 0x80 {
				out.WriteString(encoded[i : i+6])
				i += 5

				continue
			}

			r := rune(code)
			if code >= 0xD800 && code <= 0xDBFF && i+11 < len(encoded) && encoded[i+6] == '\\' && encoded[i+7] == 'u' {
				low, err := strconv.ParseUint(encoded[i+8:i+12], 16, 16)
				if err == nil && low >= 0xDC00 && low <= 0xDFFF {
					r = rune((code-0xD800)<<10 + (low - 0xDC00) + 0x10000)
					i += 6
				}
			}
			out.WriteRune(r)
			i += 5
		default:
			out.WriteByte(c)
			out.WriteByte(next)
			i++
		}
	}

	return out.String()
}
