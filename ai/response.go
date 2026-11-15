package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/atobeach/reef/ordered"
	"github.com/atobeach/reef/panics"
)

// AgentResponse is a completed run, prompted or streamed (a streamed run's
// Events are set).
type AgentResponse struct {
	InvocationID string
	Text         string
	Usage        TextUsage
	Meta         Meta
	Steps        []*Step
	Messages     []Message
	ToolCalls    []ToolCall
	ToolResults  []ToolResult
	Reasoning    string
	// Structured is the decoded structured output of a HasStructuredOutput
	// agent.
	Structured *ordered.Object

	// Events are a streamed run's events, in order (nil when not streamed).
	Events []StreamEvent

	ConversationID     string
	UserMessageID      string
	AssistantMessageID string
}

// Streamed reports whether the run was streamed.
func (r *AgentResponse) Streamed() bool {
	return r.Events != nil
}

// Decode unmarshals the structured output into dst with encoding/json.
func (r *AgentResponse) Decode(dst any) error {
	if r.Structured == nil {
		return errors.New("ai: response has no structured output")
	}

	encoded, err := json.Marshal(r.Structured)
	if err != nil {
		return fmt.Errorf("ai: encoding the structured output: %w", err)
	}

	if err := json.Unmarshal(encoded, dst); err != nil {
		return fmt.Errorf("ai: decoding the structured output into %T: %w", dst, err)
	}

	return nil
}

// streamedResponse is a finished stream's response.
func streamedResponse(invocationID string, events []StreamEvent, meta Meta) *AgentResponse {
	response := &AgentResponse{
		InvocationID: invocationID,
		Text:         CombineText(events),
		Usage:        CombineUsage(events),
		Meta:         meta,
		Reasoning:    CombineReasoning(events),
		Events:       events,
	}

	var citations []Citation
	for _, event := range events {
		switch e := event.(type) {
		case ToolCallEvent:
			response.ToolCalls = append(response.ToolCalls, e.ToolCall)
		case ToolResultEvent:
			if !e.Preliminary {
				response.ToolResults = append(response.ToolResults, e.ToolResult)
			}
		case CitationEvent:
			citations = append(citations, e.Citation)
		case StreamEnd:
			response.Steps = e.Steps
		}
	}
	response.Meta.Citations = citations

	return response
}

// StreamableAgentResponse is a run that starts when iterated. Then
// callbacks run once it completes (storing the conversation, analytics);
// Catch callbacks when it fails.
type StreamableAgentResponse struct {
	InvocationID string
	// ConversationID is known before iteration for a remembered agent.
	ConversationID     string
	UserMessageID      string
	AssistantMessageID string
	// Deadline bounds the whole run, zero for none. Catch callbacks get the
	// caller's context, so a turn cut off by it is still stored.
	Deadline time.Duration

	meta       Meta
	generator  func(ctx context.Context, emit func(StreamEvent)) error
	thens      []func(ctx context.Context, response *AgentResponse) error
	catches    []func(ctx context.Context, err error)
	hasYielded bool
	completed  *AgentResponse
}

// NewStreamableAgentResponse wraps an event generator, for protocols and
// tests that stream events they already have.
func NewStreamableAgentResponse(invocationID string, meta Meta, generator func(ctx context.Context, emit func(StreamEvent)) error) *StreamableAgentResponse {
	return &StreamableAgentResponse{InvocationID: invocationID, meta: meta, generator: generator}
}

// StreamEvents is a generator over a fixed list of events, then err.
func StreamEvents(err error, events ...StreamEvent) func(ctx context.Context, emit func(StreamEvent)) error {
	return func(_ context.Context, emit func(StreamEvent)) error {
		for _, event := range events {
			emit(event)
		}

		return err
	}
}

// Then registers a callback for the completed response.
func (s *StreamableAgentResponse) Then(callback func(ctx context.Context, response *AgentResponse) error) *StreamableAgentResponse {
	s.thens = append(s.thens, callback)

	return s
}

// Catch registers a callback for a failed run.
func (s *StreamableAgentResponse) Catch(callback func(ctx context.Context, err error)) *StreamableAgentResponse {
	s.catches = append(s.catches, callback)

	return s
}

// HasYielded reports whether any event reached the consumer.
func (s *StreamableAgentResponse) HasYielded() bool {
	return s.hasYielded
}

// Response is the completed response (nil until the run finished).
func (s *StreamableAgentResponse) Response() *AgentResponse {
	return s.completed
}

// Each runs the stream, handing each event to fn as it arrives. A failure
// runs the Catch callbacks and is returned; success runs the Then callbacks,
// and the first that fails (or panics) fails the run.
func (s *StreamableAgentResponse) Each(ctx context.Context, fn func(event StreamEvent)) error {
	var events []StreamEvent

	runCtx := ctx
	if s.Deadline > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeoutCause(ctx, s.Deadline, &TurnDeadlineError{Deadline: s.Deadline})
		defer cancel()
	}

	err := s.generator(runCtx, func(event StreamEvent) {
		events = append(events, event)
		s.hasYielded = true
		fn(event)
	})
	if err != nil && runCtx.Err() != nil && ctx.Err() == nil && !isPanic(err) {
		// Whatever the cut-off step reported is the deadline's doing; a
		// panic is a bug whenever it happened.
		err = context.Cause(runCtx)
	}
	if err != nil {
		catches := s.catches
		s.catches = nil
		for _, callback := range catches {
			// A callback that panics is logged with its stack; the run's own
			// failure is the one returned, and the rest still run.
			_ = recovering(ctx, func() error {
				callback(ctx, err)

				return nil
			})
		}

		return err
	}

	if events == nil {
		events = []StreamEvent{}
	}

	meta := s.meta
	for _, event := range events {
		if start, ok := event.(StreamStart); ok {
			meta.Model = new(start.Model)
		}
	}

	response := streamedResponse(s.InvocationID, events, meta)
	response.ConversationID = s.ConversationID
	response.UserMessageID = s.UserMessageID
	response.AssistantMessageID = s.AssistantMessageID

	for _, callback := range s.thens {
		if err := recovering(ctx, func() error { return callback(ctx, response) }); err != nil {
			return err
		}
	}

	s.ConversationID = response.ConversationID
	s.UserMessageID = response.UserMessageID
	s.AssistantMessageID = response.AssistantMessageID
	s.completed = response

	return nil
}

// recovering runs one of the run's Then or Catch callbacks (storing the
// turn, analytics): a panic in it is its error, as one in the run is.
func recovering(ctx context.Context, fn func() error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = panicked(ctx, recovered)
		}
	}()

	return fn()
}

// isPanic reports a run that failed by panicking.
func isPanic(err error) bool {
	_, ok := errors.AsType[*panics.Error](err)

	return ok
}
