package ai

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/atobeach/reef/ordered"
	"github.com/atobeach/reef/support"
)

// completion is a non-streamed Chat Completions answer.
type completion struct {
	Error   json.RawMessage `json:"error"`
	Model   *string         `json:"model"`
	Choices []struct {
		FinishReason *string           `json:"finish_reason"`
		Message      completionMessage `json:"message"`
	} `json:"choices"`
	Usage *completionUsage `json:"usage"`
}

type completionMessage struct {
	Content          *string              `json:"content"`
	Reasoning        *string              `json:"reasoning"`
	ReasoningDetails []reasoningDetail    `json:"reasoning_details"`
	ToolCalls        []completionToolCall `json:"tool_calls"`
}

// reasoningDetail is one `reasoning_details` entry: models that only
// summarise their reasoning send a summary instead of the text.
type reasoningDetail struct {
	Text    *string `json:"text"`
	Summary *string `json:"summary"`
}

type completionToolCall struct {
	ID       *string `json:"id"`
	Function struct {
		Name *string `json:"name"`
		// Arguments is a JSON object encoded as a string, though some
		// providers send the object itself.
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

// completionUsage is a Chat Completions `usage` object; the cache and
// reasoning counts are absent when the provider does not report them.
type completionUsage struct {
	PromptTokens        *json.Number `json:"prompt_tokens"`
	CompletionTokens    *json.Number `json:"completion_tokens"`
	PromptTokensDetails *struct {
		CachedTokens     *json.Number `json:"cached_tokens"`
		CacheWriteTokens *json.Number `json:"cache_write_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails *struct {
		ReasoningTokens *json.Number `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

// completionError is the `error` object OpenRouter sends in place of a
// completion, in a 200 answer or a stream chunk.
type completionError struct {
	Type    *text `json:"type"`
	Code    *text `json:"code"`
	Message *text `json:"message"`
}

// text is a JSON string, or a number as its literal: providers disagree on
// whether an error code is one or the other.
type text string

// UnmarshalJSON implements json.Unmarshaler.
func (t *text) UnmarshalJSON(raw []byte) error {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		*t = text(s)

		return nil
	}

	var n json.Number
	if err := json.Unmarshal(raw, &n); err != nil {
		return fmt.Errorf("ai: want a string or a number, got %s", raw)
	}
	*t = text(n)

	return nil
}

// unknownOpenRouterError is the message for an answer that is not a
// completion at all.
const unknownOpenRouterError = "OpenRouter Error: [unknown] Unknown OpenRouter error."

// parseCompletion reads a non-streamed Chat Completions answer. OpenRouter
// can answer 200 with an `error` object, which fails the step like a
// failed status would; an answer that does not decode fails it too,
// keeping the decode error as the cause.
func parseCompletion(raw []byte, provider ProviderConfig, structured bool) (*StepResponse, error) {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil || len(members) == 0 {
		if err == nil {
			err = errors.New("an empty answer")
		}

		return nil, &ResponseError{Message: unknownOpenRouterError, Err: fmt.Errorf("decoding the completion: %w", err)}
	}

	var answer completion
	if err := json.Unmarshal(raw, &answer); err != nil {
		return nil, &ResponseError{Message: unknownOpenRouterError, Err: fmt.Errorf("decoding the completion: %w", err)}
	}

	if failure, failed := decodeCompletionError(answer.Error); failed {
		kind := firstText("unknown", failure.Type, failure.Code)
		message := firstText("Unknown OpenRouter error.", failure.Message)

		return nil, &ResponseError{Message: "OpenRouter Error: [" + kind + "] " + message}
	}

	return answer.stepResponse(provider, structured), nil
}

// decodeCompletionError reads a non-null `error` member; failed is false
// when there is none. An error object that does not decode still fails the
// step, with the defaults standing in for its fields.
func decodeCompletionError(raw json.RawMessage) (failure completionError, failed bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return completionError{}, false
	}

	if err := json.Unmarshal(raw, &failure); err != nil {
		return completionError{}, true
	}

	return failure, true
}

// firstText is the first of values that is present, else fallback.
func firstText(fallback string, values ...*text) string {
	for _, value := range values {
		if value != nil {
			return string(*value)
		}
	}

	return fallback
}

func (c completion) stepResponse(provider ProviderConfig, structured bool) *StepResponse {
	var message completionMessage
	finish := ""
	if len(c.Choices) > 0 {
		message = c.Choices[0].Message
		if c.Choices[0].FinishReason != nil {
			finish = *c.Choices[0].FinishReason
		}
	}

	content := ""
	if message.Content != nil {
		content = *message.Content
	}

	model := ""
	if c.Model != nil {
		model = *c.Model
	}

	response := &StepResponse{
		Text:         content,
		ToolCalls:    message.toolCalls(),
		FinishReason: mapFinishReason(finish),
		Usage:        c.Usage.textUsage(),
		Meta:         Meta{Provider: new(provider.Name), Model: &model},
		Reasoning:    message.reasoning(),
	}

	if structured {
		response.Structured = decodeStructuredOutput(content)
	}

	return response
}

func (m completionMessage) toolCalls() []ToolCall {
	var calls []ToolCall
	for _, call := range m.ToolCalls {
		toolCall := ToolCall{
			Name:      support.Deref(call.Function.Name),
			Arguments: decodeArguments(argumentsText(call.Function.Arguments)),
		}
		if call.ID != nil {
			toolCall.ID = *call.ID
			toolCall.ResultID = new(*call.ID)
		}
		calls = append(calls, toolCall)
	}

	return calls
}

// argumentsText is a call's arguments as JSON text: the string a provider
// encoded them as, or the object it sent in its place.
func argumentsText(raw json.RawMessage) string {
	var encoded string
	if err := json.Unmarshal(raw, &encoded); err == nil {
		return encoded
	}
	if len(raw) == 0 || string(raw) == "null" {
		return "{}"
	}

	return string(raw)
}

// reasoning is the message's `reasoning`, or else its `reasoning_details`
// texts.
func (m completionMessage) reasoning() string {
	if m.Reasoning != nil && strings.TrimSpace(*m.Reasoning) != "" {
		return *m.Reasoning
	}

	parts := []string{}
	for _, detail := range m.ReasoningDetails {
		parts = append(parts, detail.text())
	}

	return joinReasoning(parts)
}

// text is the detail's text, or its summary when it has none.
func (d reasoningDetail) text() string {
	if d.Text != nil {
		return *d.Text
	}

	return support.Deref(d.Summary)
}

// textUsage reads the usage; a nil usage (none reported) is all zero.
func (u *completionUsage) textUsage() TextUsage {
	if u == nil {
		return TextUsage{}
	}

	usage := TextUsage{InputTokens: support.Deref(tokens(u.PromptTokens)), OutputTokens: support.Deref(tokens(u.CompletionTokens))}
	if details := u.PromptTokensDetails; details != nil {
		usage.CacheRead = tokens(details.CachedTokens)
		usage.CacheWrite = tokens(details.CacheWriteTokens)
	}
	if details := u.CompletionTokensDetails; details != nil {
		usage.Reasoning = tokens(details.ReasoningTokens)
	}

	return usage
}

// tokens is a reported token count, fractions truncated; nil when absent.
func tokens(n *json.Number) *int {
	if n == nil {
		return nil
	}

	if i, err := n.Int64(); err == nil {
		return new(int(i))
	}
	if f, err := n.Float64(); err == nil {
		return new(int(f))
	}

	return nil
}

func mapFinishReason(reason string) FinishReason {
	switch reason {
	case "stop":
		return FinishStop
	case "tool_calls":
		return FinishToolCalls
	case "length":
		return FinishLength
	case "content_filter":
		return FinishContentFilter
	}

	return FinishUnknown
}

// joinReasoning keeps the non-blank parts, separated by a blank line.
func joinReasoning(parts []string) string {
	kept := []string{}
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			kept = append(kept, part)
		}
	}

	return strings.Join(kept, "\n\n")
}

var jsonCodeFence = regexp.MustCompile("(?is)```(?:json)?\\s*(.*?)\\s*```")

// decodeStructuredOutput is the JSON object in a structured step's text, or
// inside a ```json fence (models add one despite the response format);
// empty otherwise.
func decodeStructuredOutput(text string) *ordered.Object {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ordered.NewObject()
	}

	if object := decodeObject(trimmed); object != nil {
		return object
	}

	if match := jsonCodeFence.FindStringSubmatch(trimmed); match != nil {
		if object := decodeObject(strings.TrimSpace(match[1])); object != nil {
			return object
		}
	}

	return ordered.NewObject()
}

// decodeObject decodes a JSON object, or a list as an object keyed "0", "1",
// …; nil for anything else.
func decodeObject(raw string) *ordered.Object {
	decoded, err := ordered.Decode([]byte(raw))
	if err != nil {
		return nil
	}

	switch v := decoded.(type) {
	case *ordered.Object:
		return v
	case []any:
		object := ordered.NewObject()
		for i, item := range v {
			object.Set(strconv.Itoa(i), item)
		}

		return object
	}

	return nil
}
