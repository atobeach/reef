package ai

import (
	"context"
	"strconv"

	"github.com/atobeach/reef/ordered"
	"github.com/atobeach/reef/support"
)

// Part is one AG-UI protocol event, in its wire key order.
type Part = *ordered.Object

// AgentUserInteractionProtocol maps a run's stream events to AG-UI parts
// (https://docs.ag-ui.com/concepts/events). Clients parse these frames, so
// their keys, order and values must not drift.
type AgentUserInteractionProtocol struct {
	ThreadID string
	RunID    string
	// Encode writes each frame and the tool arguments and results inside it
	// (nil: encoding/json); a client comparing stored bytes needs the
	// encoder they were stored with.
	Encode Encoder

	started  bool
	errored  bool
	finished bool
	step     int
	response *StreamableAgentResponse
}

// NewAgentUserInteractionProtocol builds the protocol, its frames written by
// encode (nil: encoding/json); empty ids default to the conversation id /
// the invocation id when the run begins.
func NewAgentUserInteractionProtocol(threadID string, runID string, encode Encoder) *AgentUserInteractionProtocol {
	return &AgentUserInteractionProtocol{ThreadID: threadID, RunID: runID, Encode: encode}
}

// Errored reports whether an error part already reached the client.
func (p *AgentUserInteractionProtocol) Errored() bool {
	return p.errored
}

// Parts iterates the response, handing every part to yield. An error from
// the run is returned after the parts that preceded it.
func (p *AgentUserInteractionProtocol) Parts(ctx context.Context, response *StreamableAgentResponse, yield func(Part)) error {
	p.started, p.errored, p.finished, p.step = false, false, false, 0
	p.response = response
	if p.RunID == "" {
		p.RunID = response.InvocationID
	}

	var usage *TextUsage
	var reason *string
	var provider, model *string

	err := response.Each(ctx, func(event StreamEvent) {
		if p.finished {
			return
		}

		switch e := event.(type) {
		case StreamStart:
			provider, model = new(e.Provider), new(e.Model)
			if p.started {
				yield(p.stepFinishedPart())
				yield(p.startNextStepPart())
			} else {
				p.beginRunParts(yield)
			}

			return
		case ErrorEvent:
			p.errored = true
			p.finished = true
		case StreamEnd:
			total := TextUsage{}
			if usage != nil {
				total = *usage
			}
			total = total.Add(e.Usage)
			usage = &total
			reason = new(e.Reason)

			return
		}

		for _, part := range p.mapEvent(event) {
			p.YieldPart(part, yield)
		}
	})
	if err != nil {
		return err
	}

	if p.started && !p.errored && !p.finished {
		yield(p.stepFinishedPart())
		yield(p.runFinishedPart(completionAttributes(usage, reason, provider, model)))
	}

	return nil
}

// YieldPart yields the part, preceded by the run's opening parts when the
// run has not begun yet.
func (p *AgentUserInteractionProtocol) YieldPart(value Part, yield func(Part)) {
	if !p.started {
		p.beginRunParts(yield)
	}

	yield(value)
}

func (p *AgentUserInteractionProtocol) beginRunParts(yield func(Part)) {
	p.started = true

	if p.ThreadID == "" {
		if p.response != nil && p.response.ConversationID != "" {
			p.ThreadID = p.response.ConversationID
		} else {
			p.ThreadID = support.UUID7()
		}
	}

	yield(NewPart("type", "RUN_STARTED", "threadId", p.ThreadID, "runId", p.RunID))
	yield(p.startNextStepPart())
}

func (p *AgentUserInteractionProtocol) startNextStepPart() Part {
	p.step++

	return NewPart("type", "STEP_STARTED", "stepName", strconv.Itoa(p.step))
}

func (p *AgentUserInteractionProtocol) stepFinishedPart() Part {
	return NewPart("type", "STEP_FINISHED", "stepName", strconv.Itoa(p.step))
}

func (p *AgentUserInteractionProtocol) runFinishedPart(attributes *ordered.Object) Part {
	finished := NewPart("type", "RUN_FINISHED", "threadId", p.ThreadID, "runId", p.RunID)
	if p.response != nil {
		if p.response.AssistantMessageID != "" {
			finished.Set("messageId", p.response.AssistantMessageID)
		}
		if p.response.UserMessageID != "" {
			finished.Set("userMessageId", p.response.UserMessageID)
		}
	}

	for _, key := range attributes.Keys() {
		value, _ := attributes.Get(key)
		finished.Set(key, value)
	}

	return finished
}

func completionAttributes(usage *TextUsage, reason *string, provider *string, model *string) *ordered.Object {
	attributes := ordered.NewObject()

	if usage != nil {
		reported := ordered.NewObject()
		if provider != nil {
			reported.Set("provider", *provider)
		}
		if model != nil {
			reported.Set("model", *model)
		}
		reported.Set("inputTokens", usage.InputTokens)
		reported.Set("outputTokens", usage.OutputTokens)
		reported.Set("totalTokens", usage.TotalTokens())
		if usage.Reasoning != nil {
			reported.Set("reasoningTokens", *usage.Reasoning)
		}
		if usage.CacheRead != nil {
			reported.Set("cachedInputTokens", *usage.CacheRead)
		}
		attributes.Set("usage", []any{reported})
	}

	if reason != nil {
		attributes.Set("metadata", NewPart("finishReason", *reason))
	}

	return attributes
}

func (p *AgentUserInteractionProtocol) mapEvent(event StreamEvent) []Part {
	switch e := event.(type) {
	case TextStart:
		return []Part{NewPart("type", "TEXT_MESSAGE_START", "messageId", e.MessageID, "role", "assistant")}
	case TextDelta:
		return []Part{NewPart("type", "TEXT_MESSAGE_CONTENT", "messageId", e.MessageID, "delta", e.Delta)}
	case TextEnd:
		return []Part{NewPart("type", "TEXT_MESSAGE_END", "messageId", e.MessageID)}
	case ReasoningStart:
		return []Part{
			NewPart("type", "REASONING_START", "messageId", e.ReasoningID),
			NewPart("type", "REASONING_MESSAGE_START", "messageId", e.ReasoningID, "role", "reasoning"),
		}
	case ReasoningDelta:
		return []Part{NewPart("type", "REASONING_MESSAGE_CONTENT", "messageId", e.ReasoningID, "delta", e.Delta)}
	case ReasoningEnd:
		return []Part{
			NewPart("type", "REASONING_MESSAGE_END", "messageId", e.ReasoningID),
			NewPart("type", "REASONING_END", "messageId", e.ReasoningID),
		}
	case ToolCallEvent:
		return p.toolCallParts(e.ToolCall)
	case ToolResultEvent:
		if e.Preliminary {
			content := NewPart("toolName", e.ToolResult.Name, "output", e.ToolResult.Result)

			return []Part{NewPart("type", "ACTIVITY_SNAPSHOT", "messageId", e.ToolResult.ID, "activityType", "TOOL_OUTPUT", "content", content)}
		}

		return []Part{p.toolResultPart(e)}
	case ErrorEvent:
		return []Part{NewPart("type", "RUN_ERROR", "message", e.Message, "code", e.Type)}
	case CitationEvent:
		value := NewPart("url", e.Citation.URL)
		if e.Citation.Title != nil {
			value.Set("title", *e.Citation.Title)
		}

		return []Part{NewPart("type", "CUSTOM", "name", "citation", "value", value)}
	}

	return nil
}

func (p *AgentUserInteractionProtocol) toolCallParts(call ToolCall) []Part {
	return []Part{
		NewPart("type", "TOOL_CALL_START", "toolCallId", call.ID, "toolCallName", call.Name),
		NewPart("type", "TOOL_CALL_ARGS", "toolCallId", call.ID, "delta", encodeArguments(call.Arguments, p.Encode)),
		NewPart("type", "TOOL_CALL_END", "toolCallId", call.ID),
	}
}

func (p *AgentUserInteractionProtocol) toolResultPart(e ToolResultEvent) Part {
	content := p.toolResultContent(e)

	messageID := e.ID
	if e.ToolResult.ResultID != nil {
		messageID = *e.ToolResult.ResultID
	}

	result := NewPart("type", "TOOL_CALL_RESULT", "messageId", messageID, "toolCallId", e.ToolResult.ID, "content", content, "role", "tool")
	if !e.Successful {
		metadata := NewPart("error", content)
		if e.Denied {
			metadata.Set("denied", true)
		}
		result.Set("metadata", metadata)
	}

	return result
}

func (p *AgentUserInteractionProtocol) toolResultContent(e ToolResultEvent) string {
	if !e.Successful {
		if e.Error != nil {
			return *e.Error
		}

		return "The tool call failed."
	}

	if text, ok := e.ToolResult.Result.(string); ok {
		return text
	}

	encoded, err := p.Encode.encode(e.ToolResult.Result)
	if err != nil {
		return ""
	}

	return string(encoded)
}

// EncodeSSE frames a part as `data: <json>\n\n`; a part the encoder
// refuses is framed as `false` rather than corrupting the stream.
func (p *AgentUserInteractionProtocol) EncodeSSE(value Part) string {
	encoded, err := p.Encode.encode(value)
	if err != nil {
		encoded = []byte("false")
	}

	return "data: " + string(encoded) + "\n\n"
}

// NewPart builds an ordered AG-UI part from alternating key, value pairs.
func NewPart(pairs ...any) Part {
	o := ordered.NewObject()
	for i := 0; i+1 < len(pairs); i += 2 {
		o.Set(pairs[i].(string), pairs[i+1])
	}

	return o
}

// ProtocolHeaders are the stream's response headers.
func ProtocolHeaders() map[string]string {
	return map[string]string{
		"Cache-Control": "no-cache, no-transform, private",
		"Content-Type":  "text/event-stream; charset=utf-8",
	}
}
