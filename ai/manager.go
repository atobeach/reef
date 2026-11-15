package ai

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/atobeach/reef/events"
	"github.com/atobeach/reef/jsonschema"
	"github.com/atobeach/reef/panics"
	"github.com/atobeach/reef/support"
)

// ManagerConfig configures the agent runtime.
type ManagerConfig struct {
	// Providers are `ai.providers`, keyed by name.
	Providers map[string]ProviderConfig
	// Default is `ai.default`, for agents without a provider.
	Default string
	// Gateway sends the steps (OpenRouterGateway for any OpenAI-compatible
	// provider).
	Gateway Gateway
	// Store is the ConversationStore (nil: conversations are not remembered).
	Store ConversationStore
	// Events receives the agent and tool lifecycle events (nil drops them).
	Events *events.Dispatcher
}

// Manager runs agents, prompted or streamed: it resolves an agent's provider
// and model, runs the loop, and remembers conversations through the store.
type Manager struct {
	config ManagerConfig
}

// NewManager builds the agent runtime.
func NewManager(config ManagerConfig) *Manager {
	return &Manager{config: config}
}

// resolve picks the agent's provider config and model.
func (m *Manager) resolve(agent Agent) (ProviderConfig, string, error) {
	name := m.config.Default
	if p, ok := agent.(HasProvider); ok && p.Provider() != "" {
		name = p.Provider()
	}

	provider, ok := m.config.Providers[name]
	if !ok {
		return ProviderConfig{}, "", fmt.Errorf("ai: provider [%s] is not configured", name)
	}

	if provider.Name == "" {
		provider.Name = name
	}
	if provider.Driver == "" {
		provider.Driver = name
	}

	model := agent.Model()
	if model == "" {
		return ProviderConfig{}, "", fmt.Errorf("ai: agent %s names no model", agent.Class())
	}

	return provider, model, nil
}

// messagesFor is the history plus the prompt as a user message.
func (m *Manager) messagesFor(ctx context.Context, agent Agent, prompt string) ([]Message, error) {
	var messages []Message
	if c, ok := agent.(Conversational); ok {
		history, err := c.Messages(ctx, m.config.Store)
		if err != nil {
			return nil, err
		}
		messages = append(messages, history...)
	}

	return append(messages, UserMessage{Content: prompt}), nil
}

func (m *Manager) remembers(agent Agent) (Rememberer, bool) {
	r, ok := agent.(Rememberer)
	if !ok || m.config.Store == nil || r.CurrentConversation() == "" {
		return nil, false
	}

	return r, true
}

// Prompt runs the agent without streaming. A HasStructuredOutput agent's
// decoded output is on response.Structured. A panic anywhere in the run
// (the agent's history, instructions or tools, the loop, storing the turn)
// fails it like a returned error.
func (m *Manager) Prompt(ctx context.Context, agent Agent, prompt string) (response *AgentResponse, err error) {
	provider, model, err := m.resolve(agent)
	if err != nil {
		return nil, err
	}

	invocationID := support.UUID7()
	agentPrompt := AgentPrompt{Agent: agent, Prompt: prompt, Provider: provider.Name, Model: model, Timeout: agentTimeout(agent)}
	run := &RunContext{InvocationID: invocationID, Agent: agent, Provider: provider.Name, Model: model, Events: m.config.Events}

	run.dispatch(ctx, PromptingAgent{InvocationID: invocationID, Prompt: agentPrompt})
	defer func() { err = run.settle(ctx, agentPrompt, err, recover()) }()

	messages, err := m.messagesFor(ctx, agent, prompt)
	if err != nil {
		return nil, err
	}

	var schema jsonschema.Properties
	if s, ok := agent.(HasStructuredOutput); ok {
		schema = s.Schema()
	}

	request := loopRequest{
		Provider: provider, Gateway: m.config.Gateway, Model: model,
		Instructions: agent.Instructions(ctx),
		Messages:     messages,
		Tools:        agentTools(ctx, agent),
		Schema:       schema,
		Options:      OptionsForAgent(agent),
		Timeout:      agentPrompt.Timeout,
		Run:          run,
	}

	result, err := request.generate(ctx)
	if err != nil {
		if r, ok := m.remembers(agent); ok {
			m.rememberFailedTurn(ctx, r, agentPrompt, run, err)
		}

		return nil, err
	}

	return m.completePrompt(ctx, run, agentPrompt, agentResponse(invocationID, result))
}

// completePrompt remembers a finished prompted turn and ends the run. Every
// started run ends in AgentPrompted or AgentFailed (Prompt's settle), so
// listeners can release per-run state.
func (m *Manager) completePrompt(ctx context.Context, run *RunContext, agentPrompt AgentPrompt, response *AgentResponse) (*AgentResponse, error) {
	if r, ok := m.remembers(agentPrompt.Agent); ok {
		if err := m.rememberTurn(ctx, r, agentPrompt, response); err != nil {
			return nil, err
		}
	}

	run.dispatch(ctx, AgentPrompted{InvocationID: run.InvocationID, Prompt: agentPrompt, Response: response})

	return response, nil
}

func agentResponse(invocationID string, result *TextResponse) *AgentResponse {
	return &AgentResponse{
		InvocationID: invocationID,
		Text:         result.Text,
		Usage:        result.Usage,
		Meta:         result.Meta,
		Steps:        result.Steps,
		Messages:     result.Messages,
		ToolCalls:    result.ToolCalls,
		ToolResults:  result.ToolResults,
		Reasoning:    result.Reasoning,
		Structured:   result.Structured,
	}
}

// Stream runs the agent streaming: nothing runs until the response is
// iterated (Each, or a protocol writing it to the client).
func (m *Manager) Stream(ctx context.Context, agent Agent, prompt string) (*StreamableAgentResponse, error) {
	if _, ok := agent.(HasStructuredOutput); ok {
		return nil, errors.New("ai: streaming structured output is not currently supported")
	}

	provider, model, err := m.resolve(agent)
	if err != nil {
		return nil, err
	}

	invocationID := support.UUID7()
	agentPrompt := AgentPrompt{Agent: agent, Prompt: prompt, Provider: provider.Name, Model: model, Timeout: agentTimeout(agent)}

	messages, err := m.messagesFor(ctx, agent, prompt)
	if err != nil {
		return nil, err
	}
	tools := agentTools(ctx, agent)

	run := &RunContext{InvocationID: invocationID, Agent: agent, Provider: provider.Name, Model: model, Events: m.config.Events}

	streamable := &StreamableAgentResponse{
		InvocationID: invocationID,
		meta:         Meta{Provider: new(provider.Name), Model: new(model)},
	}

	// A panic anywhere in the run (the agent's instructions, a tool, the
	// provider's parser, emit) fails it like a returned error.
	streamable.generator = func(ctx context.Context, emit func(StreamEvent)) (err error) {
		defer func() { err = run.settle(ctx, agentPrompt, err, recover()) }()

		run.dispatch(ctx, StreamingAgent{InvocationID: invocationID, Prompt: agentPrompt})

		request := loopRequest{
			Provider: provider, Gateway: m.config.Gateway, Model: model,
			Instructions: agent.Instructions(ctx),
			Messages:     messages,
			Tools:        tools,
			Options:      OptionsForAgent(agent),
			Timeout:      agentPrompt.Timeout,
			Run:          run,
		}

		return request.stream(ctx, invocationID, emit)
	}

	if r, ok := m.remembers(agent); ok {
		m.rememberStream(streamable, r, agentPrompt, run)
	}

	streamable.Then(func(ctx context.Context, response *AgentResponse) error {
		run.dispatch(ctx, AgentStreamed{InvocationID: invocationID, Prompt: agentPrompt, Response: response})

		return nil
	})

	return streamable, nil
}

// rememberStream stores the streamed turn once it completes, or the failed
// turn when the stream fails.
func (m *Manager) rememberStream(streamable *StreamableAgentResponse, r Rememberer, agentPrompt AgentPrompt, run *RunContext) {
	streamable.ConversationID = r.CurrentConversation()

	streamable.Catch(func(ctx context.Context, err error) {
		m.rememberFailedTurn(ctx, r, agentPrompt, run, err)
	})

	streamable.Then(func(ctx context.Context, response *AgentResponse) (err error) {
		// A failure here, a panicking store's too, is the run's end:
		// AgentStreamed never follows.
		defer func() { err = run.settle(ctx, agentPrompt, err, recover()) }()

		return m.rememberTurn(ctx, r, agentPrompt, response)
	})
}

// rememberTurn stores a completed turn: the prompt as the user message, then
// the assistant turn.
func (m *Manager) rememberTurn(ctx context.Context, r Rememberer, prompt AgentPrompt, response *AgentResponse) error {
	conversationID := r.CurrentConversation()

	userMessageID, err := m.config.Store.StoreUserMessage(ctx, conversationID, prompt.Agent.Class(), prompt.Prompt)
	if err != nil {
		return err
	}

	assistantMessageID, err := m.config.Store.StoreAssistantMessage(ctx, conversationID, prompt, response, nil)
	if err != nil {
		return err
	}

	response.ConversationID = conversationID
	response.UserMessageID = userMessageID
	response.AssistantMessageID = assistantMessageID

	return nil
}

// rememberFailedTurn stores a run that died after at least one step as a
// failed turn, keeping the tools it already ran. Its storage errors are
// reported (StepFailed) or logged, never raised over the original failure,
// and so is a store that panics.
func (m *Manager) rememberFailedTurn(ctx context.Context, r Rememberer, prompt AgentPrompt, run *RunContext, cause error) {
	response := run.recordedResponse()
	if len(response.Steps) == 0 {
		return
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			p := panics.New(recovered)
			slog.ErrorContext(ctx, "ai: storing a failed turn", "conversation", r.CurrentConversation(), "error", p, "stack", p.Stack())
		}
	}()

	conversationID := r.CurrentConversation()
	if _, err := m.config.Store.StoreUserMessage(ctx, conversationID, prompt.Agent.Class(), prompt.Prompt); err != nil {
		run.dispatch(ctx, StepFailed{InvocationID: run.InvocationID, Agent: run.Agent, Err: err})

		return
	}

	if _, err := m.config.Store.StoreAssistantMessage(ctx, conversationID, prompt, response, cause); err != nil {
		slog.ErrorContext(ctx, "ai: storing a failed turn", "conversation", conversationID, "error", err)
	}
}
