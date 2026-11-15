package ai

import (
	"context"
	"log/slog"
	"time"

	"github.com/atobeach/reef/events"
	"github.com/atobeach/reef/panics"
	"github.com/atobeach/reef/support"
)

// AgentPrompt is one prompt to one agent, as lifecycle events carry it.
type AgentPrompt struct {
	Agent    Agent
	Prompt   string
	Provider string
	Model    string
	Timeout  time.Duration
}

// PromptingAgent fires before a non-streamed prompt runs.
type PromptingAgent struct {
	InvocationID string
	Prompt       AgentPrompt
}

// StreamingAgent fires when a streamed prompt starts producing.
type StreamingAgent struct {
	InvocationID string
	Prompt       AgentPrompt
}

// AgentPrompted fires after a non-streamed prompt completed.
type AgentPrompted struct {
	InvocationID string
	Prompt       AgentPrompt
	Response     *AgentResponse
}

// AgentStreamed fires after a streamed prompt completed (after the
// conversation was stored, so the response carries its ids).
type AgentStreamed struct {
	InvocationID string
	Prompt       AgentPrompt
	Response     *AgentResponse
}

// AgentFailed fires when a prompt or stream failed.
type AgentFailed struct {
	InvocationID string
	Prompt       AgentPrompt
	Err          error
}

// InvokingTool fires before a tool handles a call.
type InvokingTool struct {
	InvocationID     string
	ToolInvocationID string
	Agent            Agent
	Tool             Tool
	Arguments        Request
}

// ToolInvoked fires after a tool handled a call. Time is the handler's own
// duration in milliseconds.
type ToolInvoked struct {
	InvocationID     string
	ToolInvocationID string
	Agent            Agent
	Tool             Tool
	Arguments        Request
	Result           string
	Time             float64
}

// ToolFailed fires when a tool returned an error.
type ToolFailed struct {
	InvocationID     string
	ToolInvocationID string
	Agent            Agent
	Tool             Tool
	Arguments        Request
	Err              error
	Time             float64
}

// StepCompleted fires after a provider step returned.
type StepCompleted struct {
	InvocationID string
	StepNumber   int
	Agent        Agent
	Model        string
	Response     *StepResponse
	Time         float64
}

// StepFailed fires when a provider step failed.
type StepFailed struct {
	InvocationID string
	StepNumber   int
	Agent        Agent
	Model        string
	Err          error
	Time         float64
}

// RunContext is one run's identity, its recorded steps (what a failed turn
// is stored from) and its lifecycle events.
type RunContext struct {
	InvocationID string
	Agent        Agent
	Provider     string
	Model        string
	Events       *events.Dispatcher

	steps []*Step
}

func (r *RunContext) dispatch(ctx context.Context, event any) {
	if r == nil || r.Events == nil {
		return
	}

	// Listeners (analytics) must never fail the run they observe: an error
	// is logged, a panic too, with its stack, and the run carries on.
	defer func() {
		if recovered := recover(); recovered != nil {
			p := panics.New(recovered)
			slog.ErrorContext(ctx, "ai: event listener failed", "error", p, "stack", p.Stack())
		}
	}()

	if err := r.Events.Dispatch(ctx, event); err != nil {
		slog.WarnContext(ctx, "ai: event listener failed", "error", err)
	}
}

// settle ends a run where it can end, given what recover returned there:
// a panic becomes the run's error, as a returned one is, and a run that
// failed dispatches AgentFailed, so listeners can release what they hold
// for it.
//
//	defer func() { err = run.settle(ctx, prompt, err, recover()) }()
func (r *RunContext) settle(ctx context.Context, prompt AgentPrompt, err error, recovered any) error {
	if recovered != nil {
		err = panicked(ctx, recovered)
	}

	if err != nil {
		r.dispatch(ctx, AgentFailed{InvocationID: r.InvocationID, Prompt: prompt, Err: err})
	}

	return err
}

func (r *RunContext) recordStep(step *Step) {
	if r != nil {
		r.steps = append(r.steps, step)
	}
}

// recordToolResult appends a result to the latest step (the loop replaces
// the list with the same results once every call has run).
func (r *RunContext) recordToolResult(result ToolResult) {
	if r == nil || len(r.steps) == 0 {
		return
	}

	last := r.steps[len(r.steps)-1]
	last.ToolResults = append(last.ToolResults, result)
}

// recordedResponse is what the run completed so far.
func (r *RunContext) recordedResponse() *AgentResponse {
	response := &AgentResponse{InvocationID: r.InvocationID, Steps: r.steps}
	for _, step := range r.steps {
		response.Usage = response.Usage.Add(step.Usage)
	}

	if len(r.steps) > 0 {
		last := r.steps[len(r.steps)-1]
		response.Text = last.Text
		response.Meta = last.Meta
	} else {
		response.Meta = Meta{Provider: new(r.Provider), Model: new(r.Model)}
	}

	return response
}

func (r *RunContext) stepCompleted(ctx context.Context, step PendingStep, response *StepResponse, elapsed time.Duration) {
	if r == nil {
		return
	}

	r.dispatch(ctx, StepCompleted{InvocationID: r.InvocationID, StepNumber: step.Number, Agent: r.Agent, Model: step.Model, Response: response, Time: milliseconds(elapsed)})
}

func (r *RunContext) stepFailed(ctx context.Context, step PendingStep, err error, elapsed time.Duration) {
	if r == nil {
		return
	}

	r.dispatch(ctx, StepFailed{InvocationID: r.InvocationID, StepNumber: step.Number, Agent: r.Agent, Model: step.Model, Err: err, Time: milliseconds(elapsed)})
}

// executeTool runs one tool call under its own invocation id, dispatching
// InvokingTool before it and ToolInvoked or ToolFailed after.
func (r *RunContext) executeTool(ctx context.Context, tool Tool, call ToolCall) (string, error) {
	toolInvocationID := support.UUID7()
	request := Request{Arguments: call.Arguments, ToolCallID: call.ID, ToolInvocationID: toolInvocationID}
	if request.Arguments == nil {
		request = NewRequest(nil)
		request.ToolCallID, request.ToolInvocationID = call.ID, toolInvocationID
	}

	invocationID := ""
	var agent Agent
	if r != nil {
		invocationID, agent = r.InvocationID, r.Agent
	}

	r.dispatch(ctx, InvokingTool{InvocationID: invocationID, ToolInvocationID: toolInvocationID, Agent: agent, Tool: tool, Arguments: request})

	started := time.Now()
	result, err := tool.Handle(ctx, request)
	if err != nil {
		r.dispatch(ctx, ToolFailed{InvocationID: invocationID, ToolInvocationID: toolInvocationID, Agent: agent, Tool: tool, Arguments: request, Err: err, Time: milliseconds(time.Since(started))})

		return "", err
	}

	r.dispatch(ctx, ToolInvoked{InvocationID: invocationID, ToolInvocationID: toolInvocationID, Agent: agent, Tool: tool, Arguments: request, Result: result, Time: milliseconds(time.Since(started))})

	return result, nil
}

func milliseconds(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000
}
