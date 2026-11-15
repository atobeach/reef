package ai

import (
	"context"
	"slices"
	"time"

	"github.com/atobeach/reef/jsonschema"
	"github.com/atobeach/reef/ordered"
)

// ToolChoice modes.
const (
	ToolChoiceAuto     = "auto"
	ToolChoiceNone     = "none"
	ToolChoiceRequired = "required"
	ToolChoiceTool     = "tool"
)

// ToolChoice constrains which tool the model may call: a mode, plus the
// tool's name for ToolChoiceTool.
type ToolChoice struct {
	Mode     string
	ToolName string
}

// Options are a run's generation options: the agent's attributes plus
// per-step overrides a middleware sets.
type Options struct {
	MaxSteps    *int
	MaxTokens   *int
	Temperature *float64
	TopP        *float64
	ToolChoice  *ToolChoice
	Agent       Agent
	// ExtraProviderOptions are merged over the agent's own provider options.
	ExtraProviderOptions *ordered.Object
}

// OptionsForAgent is the agent's own generation options (its step limit).
func OptionsForAgent(agent Agent) *Options {
	options := &Options{Agent: agent}
	if m, ok := agent.(HasMaxSteps); ok && m.MaxSteps() > 0 {
		steps := m.MaxSteps()
		options.MaxSteps = &steps
	}

	return options
}

// ProviderOptions is the driver-specific body fields: the agent's options,
// then any extra ones over them.
func (o *Options) ProviderOptions(provider string) *ordered.Object {
	if o == nil {
		return nil
	}

	var options *ordered.Object
	if a, ok := o.Agent.(HasProviderOptions); ok {
		options = a.ProviderOptions(provider)
	}

	if o.ExtraProviderOptions == nil {
		return options
	}

	merged := ordered.NewObject()
	for _, source := range []*ordered.Object{options, o.ExtraProviderOptions} {
		if source == nil {
			continue
		}
		for _, key := range source.Keys() {
			value, _ := source.Get(key)
			merged.Set(key, value)
		}
	}

	return merged
}

func (o *Options) clone() *Options {
	if o == nil {
		return &Options{}
	}

	copied := *o

	return &copied
}

// forStep is the options for one step: a forced tool choice applies to the
// first step only.
func (o *Options) forStep(step int) *Options {
	if o == nil || step == 0 || o.ToolChoice == nil {
		return o
	}

	if o.ToolChoice.Mode != ToolChoiceRequired && o.ToolChoice.Mode != ToolChoiceTool {
		return o
	}

	copied := o.clone()
	copied.ToolChoice = nil

	return copied
}

// PendingStep is the step about to be sent, which step middleware may
// narrow first.
type PendingStep struct {
	Number       int
	IsFinalStep  bool
	Provider     string
	Model        string
	Instructions string
	Messages     []Message
	Tools        []Tool
	Schema       jsonschema.Properties
	Options      *Options
	// Steps are the run's completed steps, with their tool results.
	Steps        []*Step
	Usage        TextUsage
	Timeout      time.Duration
	InvocationID string
}

// IsFirstStep is the run's first step.
func (s PendingStep) IsFirstStep() bool {
	return s.Number == 0
}

// WithModel swaps the model for this step.
func (s PendingStep) WithModel(model string) PendingStep {
	s.Model = model

	return s
}

// WithInstructions swaps the instructions for this step.
func (s PendingStep) WithInstructions(instructions string) PendingStep {
	s.Instructions = instructions

	return s
}

// WithoutTools drops the named tools from this step.
func (s PendingStep) WithoutTools(names ...string) PendingStep {
	kept := make([]Tool, 0, len(s.Tools))
	for _, tool := range s.Tools {
		if !slices.Contains(names, tool.Name()) {
			kept = append(kept, tool)
		}
	}
	s.Tools = kept

	return s
}

// OnlyTools keeps only the named tools.
func (s PendingStep) OnlyTools(names ...string) PendingStep {
	kept := make([]Tool, 0, len(s.Tools))
	for _, tool := range s.Tools {
		if slices.Contains(names, tool.Name()) {
			kept = append(kept, tool)
		}
	}
	s.Tools = kept

	return s
}

// WithToolChoice sets this step's tool choice (nil clears it).
func (s PendingStep) WithToolChoice(choice *ToolChoice) PendingStep {
	options := s.Options.clone()
	options.ToolChoice = choice
	s.Options = options

	return s
}

// ToolNames are the names of the step's tools, in order.
func (s PendingStep) ToolNames() []string {
	names := make([]string, len(s.Tools))
	for i, tool := range s.Tools {
		names[i] = tool.Name()
	}

	return names
}

// StepHandler runs a (possibly narrowed) step against the provider.
type StepHandler func(ctx context.Context, step PendingStep) (*StepResponse, error)

// StepMiddleware wraps each generation step — HasMiddleware's entries.
// Call next to send the step (narrowed or not), or return a response of
// your own to answer it without the provider.
type StepMiddleware interface {
	Handle(ctx context.Context, step PendingStep, next StepHandler) (*StepResponse, error)
}

// ToolCallLimiter is a StepMiddleware that also bounds which of a step's
// tool calls run, since calls made side by side in one response arrive after
// the step middleware ran.
type ToolCallLimiter interface {
	// LimitToolCalls returns, per call, why it must not run (sent to the
	// model as its failed result), or "" to run it.
	LimitToolCalls(step PendingStep, calls []ToolCall) []string
}
