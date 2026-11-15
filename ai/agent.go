// Package ai runs agents (instructions, tools, structured output) through a
// streaming tool-call loop against an OpenAI-compatible provider, frames the
// stream as AG-UI parts, and remembers conversations in rows whose format
// must not drift.
//
//	type Concierge struct {
//		ai.RemembersConversations
//		Timezone string
//	}
//
//	func (a *Concierge) Class() string                       { return "concierge" }
//	func (a *Concierge) Provider() string                    { return "openrouter" }
//	func (a *Concierge) Model() string                       { return "openai/gpt-6.1-sol" }
//	func (a *Concierge) Instructions(ctx context.Context) string { return "You are a concierge…" }
//	func (a *Concierge) Tools(ctx context.Context) []ai.Tool { return []ai.Tool{…} }
//
//	agent.ContinueConversation(id)
//	streamable, err := manager.Stream(ctx, agent, "Hi")
package ai

import (
	"context"
	"time"

	"github.com/atobeach/reef/jsonschema"
	"github.com/atobeach/reef/ordered"
)

// Agent is anything that can be prompted. Class is stored on every message
// row and reported to analytics, so it never changes once used.
type Agent interface {
	Class() string
	Model() string
	Instructions(ctx context.Context) string
}

// HasProvider names the configured provider an agent runs on (else the default).
type HasProvider interface {
	Provider() string
}

// HasTools is an agent that offers the model tools.
type HasTools interface {
	Tools(ctx context.Context) []Tool
}

// HasMiddleware is an agent with middleware wrapping each generation step
// of a run.
type HasMiddleware interface {
	Middleware() []StepMiddleware
}

// HasProviderOptions is an agent with extra request body keys merged into
// the provider call. Return nil for none.
type HasProviderOptions interface {
	ProviderOptions(provider string) *ordered.Object
}

// HasStructuredOutput is an agent that answers with JSON matching its
// schema (only through Manager.Prompt; streaming does not support it). An
// agent whose answer has a Go type embeds StructuredOutput[T] instead, which
// derives the schema from T and is read through PromptStructured.
type HasStructuredOutput interface {
	Schema() jsonschema.Properties
}

// HasMaxSteps is an agent with an explicit step budget.
type HasMaxSteps interface {
	MaxSteps() int
}

// RepairsToolCalls is an agent that repairs bad tool calls: a call to an
// unknown (or withdrawn) tool gets an error result instead of failing the
// run, and the step budget grows by one.
type RepairsToolCalls interface {
	RepairToolCalls() bool
}

// HasTimeout is an agent with its own per-step provider timeout.
type HasTimeout interface {
	Timeout() time.Duration
}

// Conversational is an agent with history sent before the prompt.
type Conversational interface {
	Messages(ctx context.Context, store ConversationStore) ([]Message, error)
}

// Rememberer is an agent embedding RemembersConversations.
type Rememberer interface {
	CurrentConversation() string
	SetConversation(id string)
}

// RemembersConversations makes an agent conversational: embed it and call
// ContinueConversation(id) to load that conversation's history and store
// the turn in it.
type RemembersConversations struct {
	conversationID string
}

var (
	_ Conversational = (*RemembersConversations)(nil)
	_ Rememberer     = (*RemembersConversations)(nil)
)

// ContinueConversation makes the next turn continue conversation id.
func (r *RemembersConversations) ContinueConversation(id string) {
	r.conversationID = id
}

// CurrentConversation is the conversation being continued, "" when none.
func (r *RemembersConversations) CurrentConversation() string {
	return r.conversationID
}

// SetConversation records the conversation a new turn was stored under.
func (r *RemembersConversations) SetConversation(id string) {
	r.conversationID = id
}

// Messages loads the latest 100 messages of the current conversation.
func (r *RemembersConversations) Messages(ctx context.Context, store ConversationStore) ([]Message, error) {
	if r.conversationID == "" || store == nil {
		return nil, nil
	}

	return store.LatestConversationMessages(ctx, r.conversationID, 100)
}

// defaultTimeout bounds a provider step for agents without HasTimeout.
const defaultTimeout = 60 * time.Second

func agentTimeout(agent Agent) time.Duration {
	if t, ok := agent.(HasTimeout); ok && t.Timeout() > 0 {
		return t.Timeout()
	}

	return defaultTimeout
}

func agentTools(ctx context.Context, agent Agent) []Tool {
	if t, ok := agent.(HasTools); ok {
		return t.Tools(ctx)
	}

	return nil
}

func agentMiddleware(agent Agent) []StepMiddleware {
	if m, ok := agent.(HasMiddleware); ok {
		return m.Middleware()
	}

	return nil
}

func agentRepairs(agent Agent) bool {
	r, ok := agent.(RepairsToolCalls)

	return ok && r.RepairToolCalls()
}

// ClassBasename is the last backslash-separated segment of an agent's
// Class (`Shop\Agents\Concierge` is "Concierge"; a Class without one is
// itself), the name analytics report.
func ClassBasename(class string) string {
	for i := len(class) - 1; i >= 0; i-- {
		if class[i] == '\\' {
			return class[i+1:]
		}
	}

	return class
}
