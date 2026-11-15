package ai

import (
	"encoding/json"

	"github.com/atobeach/reef/ordered"
	"github.com/atobeach/reef/support"
)

// FinishReason is why a provider step ended.
type FinishReason string

// The finish reasons a provider step can end with.
const (
	FinishStop          FinishReason = "stop"
	FinishToolCalls     FinishReason = "tool_calls"
	FinishLength        FinishReason = "length"
	FinishContentFilter FinishReason = "content_filter"
	FinishContinue      FinishReason = "continue"
	FinishUnknown       FinishReason = "unknown"
)

// ToolCall is one function call a model asked for. Arguments keep the
// model's key order (empty for `{}`), so they re-encode to the bytes stored
// rows and resent history already hold.
type ToolCall struct {
	ID        string
	Name      string
	Arguments *ordered.Object
	ResultID  *string
}

// ToolResult is a tool call's outcome. Result is what the tool returned
// (a string for every executed tool; any decoded JSON value when rebuilt
// from a stored row).
type ToolResult struct {
	ID        string
	Name      string
	Arguments *ordered.Object
	Result    any
	ResultID  *string
	Denied    bool
	Failed    bool
}

// Successful is a tool call that ran and produced its own result.
func (r ToolResult) Successful() bool {
	return !r.Denied && !r.Failed
}

// ErrorMessage is why an unsuccessful call failed, nil otherwise.
func (r ToolResult) ErrorMessage() *string {
	if r.Successful() {
		return nil
	}

	if text, ok := r.Result.(string); ok {
		return &text
	}

	return nil
}

// text is the result as sent back to a provider: a string as it is, null
// as "", anything else as JSON in encode's dialect.
func (r ToolResult) text(encode Encoder) string {
	switch v := r.Result.(type) {
	case string:
		return v
	case nil:
		return ""
	}

	encoded, err := encode.encode(r.Result)
	if err != nil {
		return ""
	}

	// Slashes and non-ASCII unescaped, so structured results reach the
	// model readable.
	return unescapeSlashesAndUnicode(string(encoded))
}

// TextUsage is a step's (or a run's summed) token usage; the cache and
// reasoning counts are nil when the provider did not report them.
type TextUsage struct {
	InputTokens  int
	OutputTokens int
	CacheRead    *int
	CacheWrite   *int
	Reasoning    *int
}

// Add sums two usages, keeping a nullable count null only when both are.
func (u TextUsage) Add(other TextUsage) TextUsage {
	return TextUsage{
		InputTokens:  u.InputTokens + other.InputTokens,
		OutputTokens: u.OutputTokens + other.OutputTokens,
		CacheRead:    sumNullable(u.CacheRead, other.CacheRead),
		CacheWrite:   sumNullable(u.CacheWrite, other.CacheWrite),
		Reasoning:    sumNullable(u.Reasoning, other.Reasoning),
	}
}

// TotalTokens is input plus output.
func (u TextUsage) TotalTokens() int {
	return u.InputTokens + u.OutputTokens
}

// UncachedInputTokens is the input minus cache reads and writes.
func (u TextUsage) UncachedInputTokens() int {
	return u.InputTokens - support.Deref(u.CacheRead) - support.Deref(u.CacheWrite)
}

// stored is the usage as a message row's `usage` column holds it.
func (u TextUsage) stored() *ordered.Object {
	o := ordered.NewObject()
	o.Set("input_tokens", u.InputTokens)
	o.Set("output_tokens", u.OutputTokens)
	o.Set("cache_read_input_tokens", nullableInt(u.CacheRead))
	o.Set("cache_write_input_tokens", nullableInt(u.CacheWrite))
	o.Set("reasoning_tokens", nullableInt(u.Reasoning))

	return o
}

// Citation is a URL citation a provider attached to its text.
type Citation struct {
	URL        string
	Title      *string
	StartIndex *int
	EndIndex   *int
}

// Meta is the provider, model and citations a response came with.
type Meta struct {
	Provider  *string
	Model     *string
	Citations []Citation
}

// stored is the meta as a message row's `meta` column holds it.
func (m Meta) stored() *ordered.Object {
	o := ordered.NewObject()
	o.Set("provider", nullableString(m.Provider))
	o.Set("model", nullableString(m.Model))

	citations := make([]any, len(m.Citations))
	for i, citation := range m.Citations {
		c := ordered.NewObject()
		c.Set("url", citation.URL)
		c.Set("title", nullableString(citation.Title))
		c.Set("start_index", nullableInt(citation.StartIndex))
		c.Set("end_index", nullableInt(citation.EndIndex))
		citations[i] = c
	}
	o.Set("citations", citations)

	return o
}

// Step is one model round trip of a run.
type Step struct {
	Text         string
	ToolCalls    []ToolCall
	ToolResults  []ToolResult
	FinishReason FinishReason
	Usage        TextUsage
	Meta         Meta
	Reasoning    string
}

// StepResponse is what one provider step returned.
type StepResponse struct {
	Text         string
	ToolCalls    []ToolCall
	FinishReason FinishReason
	Usage        TextUsage
	Meta         Meta
	Reasoning    string
	// Structured is the decoded structured output (structured steps only).
	Structured *ordered.Object
}

func sumNullable(a, b *int) *int {
	if a == nil && b == nil {
		return nil
	}

	sum := support.Deref(a) + support.Deref(b)

	return &sum
}

func nullableInt(p *int) any {
	if p == nil {
		return nil
	}

	return *p
}

func nullableString(p *string) any {
	if p == nil {
		return nil
	}

	return *p
}

// Encoder writes a value as JSON. A nil Encoder is encoding/json; an app
// whose stored rows, resent history or clients already hold another
// dialect passes its own (reef/phpjson's Encode, say).
type Encoder func(value any) ([]byte, error)

func (e Encoder) encode(value any) ([]byte, error) {
	if e == nil {
		return json.Marshal(value)
	}

	return e(value)
}
