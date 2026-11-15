package ai

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/atobeach/reef/clock"
)

// streamChunk is one Chat Completions stream chunk.
type streamChunk struct {
	Error   json.RawMessage  `json:"error"`
	Model   *string          `json:"model"`
	Choices []streamChoice   `json:"choices"`
	Usage   *completionUsage `json:"usage"`
}

type streamChoice struct {
	Delta        streamDelta     `json:"delta"`
	FinishReason *string         `json:"finish_reason"`
	Error        json.RawMessage `json:"error"`
}

type streamDelta struct {
	Content          *string            `json:"content"`
	Reasoning        *string            `json:"reasoning"`
	ReasoningDetails []reasoningDetail  `json:"reasoning_details"`
	ToolCalls        *[]toolCallDelta   `json:"tool_calls"`
	Annotations      []streamAnnotation `json:"annotations"`
}

// toolCallDelta is a fragment of a tool call: the first fragment for an
// index carries its id and name, every one a piece of the arguments.
type toolCallDelta struct {
	Index    int     `json:"index"`
	ID       *string `json:"id"`
	Function struct {
		Name      *string `json:"name"`
		Arguments *string `json:"arguments"`
	} `json:"function"`
}

type streamAnnotation struct {
	Type        string `json:"type"`
	URLCitation struct {
		URL        string   `json:"url"`
		Title      *string  `json:"title"`
		StartIndex *float64 `json:"start_index"`
		EndIndex   *float64 `json:"end_index"`
	} `json:"url_citation"`
}

type pendingToolCall struct {
	id        string
	name      string
	arguments strings.Builder
}

// textStream turns one streamed step's chunks into stream events: a
// StreamStart on the first choice, reasoning and text blocks opened and
// closed around their deltas, citations as they arrive, and the tool calls
// (assembled from their fragments) once the step finishes with tool_calls.
type textStream struct {
	ctx          context.Context
	invocationID string
	provider     ProviderConfig
	emit         func(StreamEvent)

	messageID     string
	reasoningID   string
	model         string
	streamStarted bool
	textStarted   bool
	text          strings.Builder
	pending       map[int]*pendingToolCall
	order         []int
	usage         TextUsage
	finishReason  string
	failed        bool
}

// processTextStream reads one streamed step. A nil response with a nil
// error is a step the provider failed inside the stream (the ErrorEvent is
// already emitted).
func processTextStream(ctx context.Context, invocationID string, provider ProviderConfig, model string, body io.Reader, emit func(StreamEvent)) (*StepResponse, error) {
	s := &textStream{
		ctx:          ctx,
		invocationID: invocationID,
		provider:     provider,
		emit:         emit,
		messageID:    eventID(),
		model:        model,
		pending:      map[int]*pendingToolCall{},
	}

	if err := parseServerSentEvents(body, s.handle); err != nil {
		return nil, err
	}

	if s.failed {
		return nil, nil
	}

	return s.finish(), nil
}

func (s *textStream) now() int64 { return clock.Now(s.ctx).Unix() }

// handle reads one chunk; false stops the stream.
func (s *textStream) handle(raw []byte) bool {
	var chunk streamChunk
	if json.Unmarshal(raw, &chunk) != nil {
		return true
	}

	if failure, failed := decodeCompletionError(chunk.Error); failed {
		s.fail(firstText("unknown_error", failure.Code), firstText("Unknown error", failure.Message))

		return false
	}

	// The usage arrives on a final chunk without choices.
	if len(chunk.Choices) > 0 {
		choice := chunk.Choices[0]
		if choice.FinishReason != nil && *choice.FinishReason == "error" {
			failure, _ := decodeCompletionError(choice.Error)
			s.fail(firstText("provider_error", failure.Code), firstText("An upstream provider error occurred.", failure.Message))

			return false
		}

		s.handleChoice(chunk.Model, choice)
	}

	if chunk.Usage != nil {
		s.usage = chunk.Usage.textUsage()
	}

	return true
}

func (s *textStream) fail(kind string, message string) {
	s.emit(ErrorEvent{ID: eventID(), InvocationID: s.invocationID, Type: kind, Message: message, Timestamp: s.now()})
	s.failed = true
}

func (s *textStream) handleChoice(model *string, choice streamChoice) {
	if !s.streamStarted {
		s.streamStarted = true
		if model != nil {
			s.model = *model
		}
		s.emit(StreamStart{ID: eventID(), InvocationID: s.invocationID, Provider: s.provider.Name, Model: s.model, Timestamp: s.now()})
	}

	delta := choice.Delta
	s.reason(delta)
	s.write(delta)
	s.collectToolCalls(delta.ToolCalls)
	s.cite(delta.Annotations)

	if choice.FinishReason != nil {
		s.finishReason = *choice.FinishReason
	}
}

// reason emits the delta's reasoning, opening a block for it, and closes
// the block once the model moves on to text or tool calls.
func (s *textStream) reason(delta streamDelta) {
	reasoning := ""
	if delta.Reasoning != nil {
		reasoning = *delta.Reasoning
	}
	if reasoning == "" {
		reasoning = reasoningText(delta.ReasoningDetails)
	}

	if reasoning != "" {
		if s.reasoningID == "" {
			s.reasoningID = eventID()
			s.emit(ReasoningStart{ID: eventID(), InvocationID: s.invocationID, ReasoningID: s.reasoningID, Timestamp: s.now()})
		}
		s.emit(ReasoningDelta{ID: eventID(), InvocationID: s.invocationID, ReasoningID: s.reasoningID, Delta: reasoning, Timestamp: s.now()})
	}

	hasContent := delta.Content != nil && *delta.Content != ""
	if s.reasoningID != "" && (hasContent || delta.ToolCalls != nil) {
		s.endReasoning()
	}
}

func (s *textStream) endReasoning() {
	s.emit(ReasoningEnd{ID: eventID(), InvocationID: s.invocationID, ReasoningID: s.reasoningID, Timestamp: s.now()})
	s.reasoningID = ""
}

func (s *textStream) write(delta streamDelta) {
	if delta.Content == nil || *delta.Content == "" {
		return
	}

	if !s.textStarted {
		s.textStarted = true
		s.emit(TextStart{ID: eventID(), InvocationID: s.invocationID, MessageID: s.messageID, Timestamp: s.now()})
	}
	s.text.WriteString(*delta.Content)
	s.emit(TextDelta{ID: eventID(), InvocationID: s.invocationID, MessageID: s.messageID, Delta: *delta.Content, Timestamp: s.now()})
}

func (s *textStream) collectToolCalls(calls *[]toolCallDelta) {
	if calls == nil {
		return
	}

	for _, call := range *calls {
		entry, ok := s.pending[call.Index]
		if !ok {
			entry = &pendingToolCall{}
			if call.ID != nil {
				entry.id = *call.ID
			}
			if call.Function.Name != nil {
				entry.name = *call.Function.Name
			}
			s.pending[call.Index] = entry
			s.order = append(s.order, call.Index)
		}
		if call.Function.Arguments != nil {
			entry.arguments.WriteString(*call.Function.Arguments)
		}
	}
}

func (s *textStream) cite(annotations []streamAnnotation) {
	for _, annotation := range annotations {
		if annotation.Type != "url_citation" {
			continue
		}
		citation := Citation{URL: annotation.URLCitation.URL, Title: annotation.URLCitation.Title}
		if annotation.URLCitation.StartIndex != nil {
			citation.StartIndex = new(int(*annotation.URLCitation.StartIndex))
		}
		if annotation.URLCitation.EndIndex != nil {
			citation.EndIndex = new(int(*annotation.URLCitation.EndIndex))
		}
		s.emit(CitationEvent{ID: eventID(), InvocationID: s.invocationID, MessageID: s.messageID, Citation: citation, Timestamp: s.now()})
	}
}

// finish closes the open blocks and emits the assembled tool calls, in the
// order their first fragments arrived. Calls only run when the step
// finished with tool_calls.
func (s *textStream) finish() *StepResponse {
	if s.reasoningID != "" {
		s.endReasoning()
	}

	if s.textStarted {
		s.emit(TextEnd{ID: eventID(), InvocationID: s.invocationID, MessageID: s.messageID, Timestamp: s.now()})
	}

	var toolCalls []ToolCall
	if s.finishReason == "tool_calls" {
		for _, index := range s.order {
			entry := s.pending[index]
			id := entry.id
			call := ToolCall{ID: id, Name: entry.name, Arguments: decodeArguments(entry.arguments.String()), ResultID: &id}
			toolCalls = append(toolCalls, call)
			s.emit(ToolCallEvent{ID: eventID(), InvocationID: s.invocationID, ToolCall: call, Timestamp: s.now()})
		}
	}

	return &StepResponse{
		Text:         s.text.String(),
		ToolCalls:    toolCalls,
		FinishReason: mapFinishReason(s.finishReason),
		Usage:        s.usage,
		Meta:         Meta{Provider: new(s.provider.Name), Model: new(s.model)},
	}
}

// parseServerSentEvents hands each `data:` line's JSON to handle, until
// `[DONE]`, the end of the body, or handle returning false. Lines that are
// not JSON (keep-alive comments, OpenRouter's processing notices) are
// skipped.
func parseServerSentEvents(body io.Reader, handle func(raw []byte) bool) error {
	reader := bufio.NewReader(body)
	for {
		line, err := reader.ReadString('\n')
		if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "data:") {
			data := strings.TrimSpace(trimmed[5:])
			if data == "[DONE]" {
				return nil
			}

			var probe any
			if json.Unmarshal([]byte(data), &probe) == nil && probe != nil && !handle([]byte(data)) {
				return nil
			}
		}

		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}

			return err
		}
	}
}

// reasoningText joins a delta's reasoning_details: each detail's text, or
// its summary when it has none.
func reasoningText(details []reasoningDetail) string {
	var out strings.Builder
	for _, detail := range details {
		out.WriteString(detail.text())
	}

	return out.String()
}
