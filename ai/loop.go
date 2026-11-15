package ai

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/atobeach/reef/clock"
	"github.com/atobeach/reef/jsonschema"
	"github.com/atobeach/reef/ordered"
	"github.com/atobeach/reef/panics"
)

// defaultMaxStepsCeiling caps a step budget derived from the tool count.
const defaultMaxStepsCeiling = 25

// loopRequest is one run through the text generation loop.
type loopRequest struct {
	Provider     ProviderConfig
	Gateway      Gateway
	Model        string
	Instructions string
	Messages     []Message
	Tools        []Tool
	Schema       jsonschema.Properties
	Options      *Options
	Timeout      time.Duration
	Run          *RunContext
}

// TextResponse is the loop's non-streamed result.
type TextResponse struct {
	Text        string
	Usage       TextUsage
	Meta        Meta
	Steps       []*Step
	Messages    []Message
	ToolCalls   []ToolCall
	ToolResults []ToolResult
	Reasoning   string
	Structured  *ordered.Object
}

// loopState is what a run carries from step to step.
type loopState struct {
	messages []Message
	steps    []*Step
	usage    TextUsage
}

// stepOutcome is one step as the middleware let it go out and as it came
// back. A step a middleware answered itself was never attempted.
type stepOutcome struct {
	response  *StepResponse
	prepared  PendingStep
	attempted bool
	started   time.Time
}

// resolveMaxSteps is the agent's explicit step budget, else 1.5x the tool
// count capped at 25, else 5 — plus one for an agent that repairs tool
// calls, so a repaired call still leaves a step to answer in.
func resolveMaxSteps(options *Options, tools []Tool) int {
	if options != nil && options.MaxSteps != nil {
		return max(1, *options.MaxSteps)
	}

	steps := 5
	if len(tools) > 0 {
		steps = min(int(math.Round(float64(len(tools))*1.5)), defaultMaxStepsCeiling)
	}

	if options != nil && agentRepairs(options.Agent) {
		steps++
	}

	return steps
}

// runStep sends one step through the agent's middleware, remembering the
// step as it actually went out: the tools a narrowed step still offers are
// the ones its calls execute against.
func (r loopRequest) runStep(ctx context.Context, middleware []StepMiddleware, pending PendingStep, send StepHandler) (stepOutcome, error) {
	outcome := stepOutcome{prepared: pending, started: time.Now()}

	next := StepHandler(func(ctx context.Context, step PendingStep) (*StepResponse, error) {
		outcome.prepared, outcome.attempted, outcome.started = step, true, time.Now()

		response, err := send(ctx, step)
		if err != nil {
			r.Run.stepFailed(ctx, step, err, time.Since(outcome.started))
		}

		return response, err
	})

	for i := len(middleware) - 1; i >= 0; i-- {
		pipe, inner := middleware[i], next
		next = func(ctx context.Context, step PendingStep) (*StepResponse, error) {
			return pipe.Handle(ctx, step, inner)
		}
	}

	response, err := next(ctx, pending)
	outcome.response = response

	return outcome, err
}

func (r loopRequest) pending(number int, maxSteps int, state *loopState) PendingStep {
	invocationID := ""
	if r.Run != nil {
		invocationID = r.Run.InvocationID
	}

	return PendingStep{
		Number:       number,
		IsFinalStep:  number+1 >= maxSteps,
		Provider:     r.Provider.Name,
		Model:        r.Model,
		Instructions: r.Instructions,
		Messages:     append([]Message(nil), state.messages...),
		Tools:        r.Tools,
		Schema:       r.Schema,
		Options:      r.Options.forStep(number),
		Steps:        append([]*Step(nil), state.steps...),
		Usage:        state.usage,
		Timeout:      r.Timeout,
		InvocationID: invocationID,
	}
}

// stream runs step after step, emitting each provider event, executing the
// step's tool calls and emitting their results, until a step makes no tool
// call or the budget runs out; then the closing StreamEnd. Its panics are
// recovered by the run around it (Manager.Stream's generator).
func (r loopRequest) stream(ctx context.Context, invocationID string, emit func(StreamEvent)) error {
	middleware := agentMiddleware(r.Options.Agent)
	maxSteps := resolveMaxSteps(r.Options, r.Tools)
	state := &loopState{messages: settleAbandonedToolCalls(r.Messages)}
	reason := FinishStop

	for number := range maxSteps {
		// A run past its deadline must not start another billed step.
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}

		outcome, err := r.streamStep(ctx, invocationID, middleware, r.pending(number, maxSteps, state), emit)
		if err != nil {
			return err
		}
		reason = outcome.response.FinishReason

		results, err := r.completeStep(ctx, state, outcome)
		if err != nil {
			return err
		}

		for _, toolResult := range results {
			emit(ToolResultEvent{
				ID: eventID(), InvocationID: invocationID,
				ToolResult: toolResult,
				Successful: toolResult.Successful(),
				Error:      toolResult.ErrorMessage(),
				Timestamp:  clock.Now(ctx).Unix(),
			})
		}

		if len(results) == 0 && reason != FinishContinue {
			break
		}
	}

	emit(StreamEnd{
		ID: eventID(), InvocationID: invocationID,
		Reason:    string(reason),
		Usage:     state.usage,
		Steps:     state.steps,
		Timestamp: clock.Now(ctx).Unix(),
	})

	return nil
}

// streamStep streams one step. A step the provider ended with an error
// event fails the run with a StreamError carrying that event.
func (r loopRequest) streamStep(ctx context.Context, invocationID string, middleware []StepMiddleware, pending PendingStep, emit func(StreamEvent)) (stepOutcome, error) {
	var reasoning strings.Builder
	var lastError *ErrorEvent

	outcome, err := r.runStep(ctx, middleware, pending, func(ctx context.Context, step PendingStep) (*StepResponse, error) {
		return r.Gateway.StreamStep(ctx, invocationID, r.Provider, step, func(event StreamEvent) {
			switch e := event.(type) {
			case ErrorEvent:
				lastError = &e
			case ReasoningDelta:
				reasoning.WriteString(e.Delta)
			}
			emit(event)
		})
	})
	if err != nil {
		return outcome, err
	}

	result := outcome.response
	if result == nil {
		streamErr := &StreamError{Event: lastError}
		r.Run.stepFailed(ctx, outcome.prepared, streamErr, time.Since(outcome.started))

		return outcome, streamErr
	}

	// A middleware that answered the step itself streamed nothing, so its
	// response is described as events for the client.
	if !outcome.attempted {
		eventsFor(ctx, invocationID, r.Provider, outcome.prepared.Model, result, emit)
	}

	if result.Reasoning == "" {
		result.Reasoning = reasoning.String()
	}

	return outcome, nil
}

// generate is the same loop without streaming, returning the final
// response. It recovers its own panics, so a prompted run that panicked is
// stored as failed with the steps it completed, as any failed loop is.
func (r loopRequest) generate(ctx context.Context) (response *TextResponse, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			response, err = nil, panicked(ctx, recovered)
		}
	}()

	middleware := agentMiddleware(r.Options.Agent)
	maxSteps := resolveMaxSteps(r.Options, r.Tools)
	state := &loopState{messages: settleAbandonedToolCalls(r.Messages)}
	history := len(state.messages)
	var last *StepResponse

	for number := range maxSteps {
		outcome, err := r.runStep(ctx, middleware, r.pending(number, maxSteps, state), func(ctx context.Context, step PendingStep) (*StepResponse, error) {
			return r.Gateway.GenerateStep(ctx, r.Provider, step)
		})
		if err != nil {
			return nil, err
		}
		if outcome.response == nil {
			return nil, &ResponseError{Message: "Agent middleware must return the next step result or a StepResponse."}
		}
		last = outcome.response

		results, err := r.completeStep(ctx, state, outcome)
		if err != nil {
			return nil, err
		}

		if len(results) == 0 && last.FinishReason != FinishContinue {
			break
		}
	}

	return buildFinalResponse(state.steps, state.messages[history:], last), nil
}

// panicked is a run's recovered panic as its error, logged here with its
// stack: the run's error goes on to callers that log no stack.
func panicked(ctx context.Context, recovered any) error {
	p := panics.New(recovered)
	slog.ErrorContext(ctx, "ai: run panicked", "error", p, "stack", p.Stack())

	return fmt.Errorf("ai: run panicked: %w", p)
}

// completeStep records a step that came back, runs its tool calls and
// appends the exchange to the history the next step is sent with.
func (r loopRequest) completeStep(ctx context.Context, state *loopState, outcome stepOutcome) ([]ToolResult, error) {
	result := outcome.response

	completed := buildStep(result)
	state.steps = append(state.steps, completed)
	r.Run.recordStep(completed)
	if outcome.attempted {
		r.Run.stepCompleted(ctx, outcome.prepared, result, time.Since(outcome.started))
	}
	state.usage = state.usage.Add(result.Usage)

	results, err := r.stepToolResults(ctx, result, outcome.prepared)
	if err != nil {
		return nil, err
	}
	completed.ToolResults = results

	state.messages = append(state.messages, AssistantMessage{Content: result.Text, ToolCalls: result.ToolCalls})
	if len(results) > 0 {
		state.messages = append(state.messages, ToolResultMessage{Results: results})
	}

	return results, nil
}

// eventsFor describes a step response produced without streaming.
func eventsFor(ctx context.Context, invocationID string, provider ProviderConfig, model string, response *StepResponse, emit func(StreamEvent)) {
	now := clock.Now(ctx).Unix()
	emit(StreamStart{ID: eventID(), InvocationID: invocationID, Provider: provider.Name, Model: model, Timestamp: now})

	if strings.TrimSpace(response.Text) != "" {
		messageID := eventID()
		emit(TextStart{ID: eventID(), InvocationID: invocationID, MessageID: messageID, Timestamp: now})
		emit(TextDelta{ID: eventID(), InvocationID: invocationID, MessageID: messageID, Delta: response.Text, Timestamp: now})
		emit(TextEnd{ID: eventID(), InvocationID: invocationID, MessageID: messageID, Timestamp: now})
	}

	for _, call := range response.ToolCalls {
		emit(ToolCallEvent{ID: eventID(), InvocationID: invocationID, ToolCall: call, Timestamp: now})
	}
}

func buildStep(result *StepResponse) *Step {
	return &Step{
		Text:         result.Text,
		ToolCalls:    result.ToolCalls,
		FinishReason: result.FinishReason,
		Usage:        result.Usage,
		Meta:         result.Meta,
		Reasoning:    result.Reasoning,
	}
}

func buildFinalResponse(steps []*Step, newMessages []Message, last *StepResponse) *TextResponse {
	response := &TextResponse{Messages: newMessages, Steps: steps}

	reasoning := make([]string, len(steps))
	for i, step := range steps {
		response.Usage = response.Usage.Add(step.Usage)
		reasoning[i] = step.Reasoning
		response.ToolCalls = append(response.ToolCalls, step.ToolCalls...)
	}
	response.Reasoning = joinReasoning(reasoning)

	for _, message := range newMessages {
		if answer, ok := message.(ToolResultMessage); ok {
			response.ToolResults = append(response.ToolResults, answer.Results...)
		}
	}

	if len(steps) > 0 {
		final := steps[len(steps)-1]
		response.Text = final.Text
		response.Meta = final.Meta
	}

	if last != nil {
		response.Structured = last.Structured
	}

	return response
}
