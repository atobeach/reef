package ai

import (
	"strings"

	"github.com/atobeach/reef/support"
)

// StreamEvent is one event of a streamed run.
// Every event carries its own uuid7 id and the run's invocation id.
type StreamEvent interface {
	isStreamEvent()
}

// StreamStart opens a provider step.
type StreamStart struct {
	ID           string
	InvocationID string
	Provider     string
	Model        string
	Timestamp    int64
}

// TextStart opens the step's text.
type TextStart struct {
	ID           string
	InvocationID string
	MessageID    string
	Timestamp    int64
}

// TextDelta is a chunk of text.
type TextDelta struct {
	ID           string
	InvocationID string
	MessageID    string
	Delta        string
	Timestamp    int64
}

// TextEnd closes the step's text.
type TextEnd struct {
	ID           string
	InvocationID string
	MessageID    string
	Timestamp    int64
}

// ReasoningStart opens a block of visible reasoning.
type ReasoningStart struct {
	ID           string
	InvocationID string
	ReasoningID  string
	Timestamp    int64
}

// ReasoningDelta is a chunk of reasoning.
type ReasoningDelta struct {
	ID           string
	InvocationID string
	ReasoningID  string
	Delta        string
	Timestamp    int64
}

// ReasoningEnd closes a reasoning block.
type ReasoningEnd struct {
	ID           string
	InvocationID string
	ReasoningID  string
	Timestamp    int64
}

// ToolCallEvent is a completed tool call the model made.
type ToolCallEvent struct {
	ID           string
	InvocationID string
	ToolCall     ToolCall
	Timestamp    int64
}

// ToolResultEvent is a tool call's outcome.
type ToolResultEvent struct {
	ID           string
	InvocationID string
	ToolResult   ToolResult
	Successful   bool
	Error        *string
	Denied       bool
	Preliminary  bool
	Timestamp    int64
}

// ErrorEvent is a provider error reported inside the stream.
type ErrorEvent struct {
	ID           string
	InvocationID string
	Type         string
	Message      string
	Recoverable  bool
	Timestamp    int64
}

// CitationEvent is a URL citation attached to the text.
type CitationEvent struct {
	ID           string
	InvocationID string
	MessageID    string
	Citation     Citation
	Timestamp    int64
}

// StreamEnd closes the run: the final finish reason, the summed usage and
// every completed step.
type StreamEnd struct {
	ID           string
	InvocationID string
	Reason       string
	Usage        TextUsage
	Steps        []*Step
	Timestamp    int64
}

func (StreamStart) isStreamEvent()     {}
func (TextStart) isStreamEvent()       {}
func (TextDelta) isStreamEvent()       {}
func (TextEnd) isStreamEvent()         {}
func (ReasoningStart) isStreamEvent()  {}
func (ReasoningDelta) isStreamEvent()  {}
func (ReasoningEnd) isStreamEvent()    {}
func (ToolCallEvent) isStreamEvent()   {}
func (ToolResultEvent) isStreamEvent() {}
func (ErrorEvent) isStreamEvent()      {}
func (CitationEvent) isStreamEvent()   {}
func (StreamEnd) isStreamEvent()       {}

// CombineText is every text delta, concatenated.
func CombineText(events []StreamEvent) string {
	var text strings.Builder
	for _, event := range events {
		if delta, ok := event.(TextDelta); ok {
			text.WriteString(delta.Delta)
		}
	}

	return text.String()
}

// CombineReasoning is every reasoning delta, concatenated.
func CombineReasoning(events []StreamEvent) string {
	var text strings.Builder
	for _, event := range events {
		if delta, ok := event.(ReasoningDelta); ok {
			text.WriteString(delta.Delta)
		}
	}

	return text.String()
}

// CombineUsage sums the usage of every step's StreamEnd.
func CombineUsage(events []StreamEvent) TextUsage {
	usage := TextUsage{}
	for _, event := range events {
		if end, ok := event.(StreamEnd); ok {
			usage = usage.Add(end.Usage)
		}
	}

	return usage
}

// eventID is a stream event id: a lowercase, monotonic uuid7.
func eventID() string {
	return support.UUID7()
}
