# Agents

- [Introduction](#introduction)
- [Defining Agents](#defining-agents)
    - [The Class Name](#the-class-name)
- [Agent Configuration](#agent-configuration)
    - [Providers and Models](#providers-and-models)
    - [Timeouts](#timeouts)
    - [Step Limits](#step-limits)
    - [Provider Options](#provider-options)
    - [Generation Options](#generation-options)
- [Conversation Context](#conversation-context)
    - [Messages](#messages)
    - [Remembering Conversations](#remembering-conversations)
    - [Failed Turns](#failed-turns)
    - [The Conversation Store](#the-conversation-store)
    - [Extending the Store](#extending-the-store)
- [Structured Output](#structured-output)
    - [Typed Output](#typed-output)
    - [Hand-Written Schemas](#hand-written-schemas)
    - [How the Answer Is Read](#how-the-answer-is-read)
- [Middleware](#middleware)
    - [Narrowing a Step](#narrowing-a-step)
    - [Setting Generation Options](#setting-generation-options)
    - [Answering a Step Yourself](#answering-a-step-yourself)
    - [Pending Steps](#pending-steps)
- [The Agent Loop](#the-agent-loop)
    - [Steps](#steps)
    - [Finish Reasons](#finish-reasons)

## Introduction

This page covers everything an agent can be configured with: its provider and model, history, structured output and middleware, plus how reef runs the tool-call loop. Start with the [AI overview](ai.md) if you haven't configured the manager yet.

## Defining Agents

An agent is any type with three methods, the `ai.Agent` interface:

```go
type Agent interface {
	Class() string
	Model() string
	Instructions(ctx context.Context) string
}
```

Every other capability is an optional interface the agent implements. Here is an example chat agent using most of them. `Concierge`, its tools (`SearchFlights`, `SwitchCurrency`) and its middleware (`LogSteps`, `OneSearchPerTurn`) are illustrations written for these docs (imported from `example.com/flights`) and appear throughout the AI pages:

```go
package agents

import (
	"context"
	"time"

	"github.com/atobeach/reef/ai"
	"github.com/atobeach/reef/ordered"
	aimiddleware "example.com/flights/app/ai/middleware"
	"example.com/flights/app/ai/tools"
)

// Concierge is the flight-booking chat agent.
type Concierge struct {
	ai.RemembersConversations

	Timezone string
}

// Continue resumes an existing conversation.
func (a *Concierge) Continue(conversationID string) *Concierge {
	a.ContinueConversation(conversationID)

	return a
}

func (a *Concierge) Class() string    { return `App\Ai\Agents\Concierge` }
func (a *Concierge) Provider() string { return "openrouter" }
func (a *Concierge) Model() string    { return "openai/gpt-5" }

func (a *Concierge) Instructions(ctx context.Context) string {
	return "You are a friendly flight-booking assistant. The user's timezone is " + a.Timezone + "."
}

func (a *Concierge) Tools(context.Context) []ai.Tool {
	return []ai.Tool{
		&tools.SearchFlights{},
		&tools.SwitchCurrency{Supported: []string{"EUR", "GBP", "USD"}},
	}
}

func (a *Concierge) MaxSteps() int          { return 8 }
func (a *Concierge) Timeout() time.Duration { return 90 * time.Second }
func (a *Concierge) RepairToolCalls() bool  { return true }

func (a *Concierge) Middleware() []ai.StepMiddleware {
	return []ai.StepMiddleware{aimiddleware.LogSteps{}, aimiddleware.OneSearchPerTurn{}}
}

func (a *Concierge) ProviderOptions(provider string) *ordered.Object {
	if provider != "openrouter" {
		return nil
	}

	reasoning := ordered.NewObject()
	reasoning.Set("effort", "low")

	options := ordered.NewObject()
	options.Set("reasoning", reasoning)

	return options
}
```

reef finds each capability by type assertion when the agent runs, so a method whose signature slipped (a `MaxSteps() int64`) is silently skipped. Declare the ones an agent means to implement beside its type, on the receiver it is run with, and a slip fails the build:

```go
var _ ai.HasMaxSteps = (*Concierge)(nil)
```

An agent is a plain value, so you build one per prompt with whatever it needs (the user's timezone, a config value, tool constructors) as fields. `Instructions` and `Tools` receive the run's context, so they may read the clock (`clock.Now(ctx)`) or anything else your middleware bound to it.

### The Class Name

`Class` is the agent's stable identifier. The conversation store writes it to `agent_conversation_messages.agent`, and instrumentation reports `ai.ClassBasename(agent.Class())`, the segment after the last backslash. Once rows and analytics carry a class name, changing it orphans them, so pick one once and keep it.

Any string works as a class name (`"concierge"` is fine). The examples on these pages use PHP-style names such as `App\Ai\Agents\Concierge` because that is what laravel/ai stores in the same column: if your conversations table is shared with, or inherited from, a Laravel application, use the PHP class names so both sides agree on which agent wrote each message. `ai.ClassBasename` turns either form into a short name for logs and analytics.

## Agent Configuration

### Providers and Models

An agent that implements `ai.HasProvider` runs on the provider its `Provider` method names; one that doesn't, or returns `""`, runs on the manager's `Default`. `Model` is the provider's model id, and may be computed per instance (a chat agent might return a configured override when one is set). An agent whose `Model` returns `""` fails with `ai: agent <Class> names no model`.

### Timeouts

Each provider request is bounded by the agent's timeout: 60 seconds, unless the agent implements `ai.HasTimeout` and returns a positive duration:

```go
func (a *Concierge) Timeout() time.Duration { return 90 * time.Second }
```

The timeout applies to every step on its own, not to the whole run. To bound a streamed run end to end, set its [deadline](ai-streaming.md#run-deadlines); to bound a prompted run, pass a context with a deadline.

### Step Limits

A run is a series of steps: one model round trip each. The loop stops when the model answers without calling a tool, or when the step budget runs out. An agent sets its budget by implementing `ai.HasMaxSteps`:

```go
func (a *Concierge) MaxSteps() int { return 8 }
```

Without a positive `MaxSteps`, reef derives the budget: 5 steps for an agent without tools, otherwise one and a half times the number of tools (rounded) and never more than 25. An agent that [repairs tool calls](ai-tools.md#repairing-tool-calls) gets one more step on top of a derived budget, so a repaired call still leaves a step to answer in.

> [!WARNING]
> Tool calls made on the final step are never run: each is answered with `The agent reached its maximum number of steps without running this tool call.` and the run ends with whatever text that step produced. Leave room in the budget for the model to answer after its last tool round. A middleware may check `step.IsFinalStep` and switch tool use off for that step.

### Provider Options

An agent that implements `ai.HasProviderOptions` adds keys to the request body. The method receives the provider's `Driver` (which defaults to the provider's name), so you may send different options to different providers, and returns an `*ordered.Object`, or `nil` for none:

```go
func (a *Concierge) ProviderOptions(provider string) *ordered.Object {
	if provider != "openrouter" {
		return nil
	}

	reasoning := ordered.NewObject()
	reasoning.Set("effort", "low")

	options := ordered.NewObject()
	options.Set("reasoning", reasoning)

	return options
}
```

These keys are merged into the body last, after `model`, `messages`, `tool_choice`, `tools`, `response_format`, `max_tokens`, `temperature` and `top_p`, so a key you return replaces the value reef would have sent.

### Generation Options

`ai.Options` are a run's generation options:

| Field | Description |
|-------|-------------|
| `MaxSteps` | The agent's explicit step budget. |
| `MaxTokens` | Sent as `max_tokens`. |
| `Temperature` | Sent as `temperature`. |
| `TopP` | Sent as `top_p`. |
| `ToolChoice` | Sent as `tool_choice` (see [Tool Choice](ai-tools.md#tool-choice)). |
| `Agent` | The agent the options belong to. |
| `ExtraProviderOptions` | Body keys merged over the agent's own provider options. |

`ai.OptionsForAgent(agent)` builds an agent's own options: the agent, and its step budget when it implements `HasMaxSteps`. Agents don't declare sampling options; a [middleware sets them](#setting-generation-options) on the step it sends. `Options.ProviderOptions(provider)` returns the body keys a step adds: the agent's `ProviderOptions`, then `ExtraProviderOptions` over them (a key in both takes the extra value).

## Conversation Context

### Messages

An agent that implements `ai.Conversational` returns its history, which the manager sends before the prompt:

```go
// Planner sends a fixed history.
type Planner struct{ Notes []string }

func (Planner) Class() string                       { return `App\Ai\Agents\Planner` }
func (Planner) Model() string                       { return "openai/gpt-5" }
func (Planner) Instructions(context.Context) string { return "You plan multi-city itineraries." }

// Messages implements ai.Conversational.
func (p Planner) Messages(ctx context.Context, _ ai.ConversationStore) ([]ai.Message, error) {
	messages := make([]ai.Message, 0, len(p.Notes)*2)
	for _, note := range p.Notes {
		messages = append(messages,
			ai.UserMessage{Content: note},
			ai.AssistantMessage{Content: "Noted."},
		)
	}

	return messages, nil
}
```

The second argument is the manager's `Store` (`nil` when none is configured). An `ai.Message` is one of three types, and only these three:

| Type | Fields | Sent as |
|------|--------|---------|
| `ai.UserMessage` | `Content` | A `user` message. |
| `ai.AssistantMessage` | `Content`, `ToolCalls` | An `assistant` message; blank content is left out. |
| `ai.ToolResultMessage` | `Results` | One `tool` message per result, answering the previous assistant message's calls. |

The instructions go first as the `system` message (left out when blank), then the history, then the prompt as a `UserMessage`.

If the history holds an assistant message whose tool calls were never answered (a turn that paused for approval, say), reef answers each of those calls before sending, with a denied result reading `This tool call was not executed because it was not approved before the conversation continued.` The history therefore always replays cleanly.

### Remembering Conversations

Most agents don't build their history by hand. Embed `ai.RemembersConversations` and call `ContinueConversation` with the conversation's id before prompting:

```go
conversationID, err := store.StoreConversation(ctx, "Weekend in Lisbon", "")
if err != nil {
	return err
}

agent := (&agents.Concierge{Timezone: "Europe/London"}).Continue(conversationID)

response, err := manager.Prompt(ctx, agent, message)
if err != nil {
	return err
}

response.ConversationID     // conversationID
response.UserMessageID      // the stored user message
response.AssistantMessageID // the stored assistant message
```

The embedded type loads the conversation's latest 100 messages from the store, oldest first, as the run's history. When the run completes, the manager stores the prompt as a user message and the run as an assistant message, and puts their ids on the response. For a streamed run, the conversation id is on the `StreamableAgentResponse` before you iterate it, and the message ids once it completes.

A conversation is only remembered when the manager has a `Store` and the agent's conversation id isn't empty. Otherwise the agent runs without history and nothing is stored.

> [!NOTE]
> The manager never creates the conversation itself. Create the row first, with the store's `StoreConversation` (an empty id gets a new UUIDv7) or an insert of your own. Insert your own row when you want to record who owns the conversation.

`RemembersConversations` provides four methods:

| Method | Description |
|--------|-------------|
| `ContinueConversation(id)` | Continue the conversation with this id. |
| `CurrentConversation()` | The conversation being continued, `""` when none. |
| `SetConversation(id)` | Record the conversation a turn is stored under. |
| `Messages(ctx, store)` | The latest 100 messages of the current conversation (implements `ai.Conversational`). |

The manager recognizes a remembering agent through the `ai.Rememberer` interface (`CurrentConversation` and `SetConversation`), which embedding `RemembersConversations` satisfies. Because the embedded type has pointer methods, use the agent through a pointer (`&agents.Concierge{...}`).

### Failed Turns

When a remembered run fails after completing at least one step, the manager stores the turn anyway: the prompt as the user message, and the completed steps (with any tool results they already have) as an assistant message whose status is `failed` and whose `meta` carries the error message under `error`. The model therefore sees, on the next turn, which tools already ran. A run that fails before any step completes stores nothing.

Storing a failed turn never hides the run's own error. If the user message can't be stored, an `ai.StepFailed` [event](ai.md#events) carries the storage error (with `StepNumber` 0 and no `Model`) and nothing more is stored. If the assistant message can't be stored, the failure is logged (`ai: storing a failed turn`), and so is a store that panics, with its `stack`. Either way, the run's error is what you get back.

When a failed turn is loaded back, any of its tool calls without a recorded result is answered with `This tool call was interrupted before a result was recorded, so it may or may not have run.`, so the model doesn't assume the call never happened.

### The Conversation Store

The manager talks to storage through the `ai.ConversationStore` interface:

```go
type ConversationStore interface {
	StoreConversation(ctx context.Context, title string, id string) (string, error)
	StoreUserMessage(ctx context.Context, conversationID string, agentClass string, content string) (string, error)
	// StoreAssistantMessage records the turn; failure non-nil marks it failed.
	StoreAssistantMessage(ctx context.Context, conversationID string, prompt AgentPrompt, response *AgentResponse, failure error) (string, error)
	LatestConversationMessages(ctx context.Context, conversationID string, limit int) ([]Message, error)
}
```

reef's implementation is `ai.DatabaseConversationStore`, which stores conversations through the context's database connection (see [database](database.md)). `ai.NewDatabaseConversationStore(encode)` builds one over the `agent_conversations` and `agent_conversation_messages` tables, writing its JSON columns with `encode` (`nil`: `encoding/json`). Its fields are:

| Field | Description |
|-------|-------------|
| `ConversationsTable` | The conversations table (`agent_conversations`). |
| `MessagesTable` | The messages table (`agent_conversation_messages`). |
| `Encode` | The [encoder](ai.md#json-encoding) for the JSON columns. |

reef ships no migration for these tables; your application creates them. These are the columns the store reads and writes (your tables may carry more of their own):

```sql
create table agent_conversations (
    id varchar(36) not null primary key,
    title varchar(255) not null,
    participant_type varchar(255),
    participant_id bigint,
    created_at timestamp(0) without time zone,
    updated_at timestamp(0) without time zone
);

create table agent_conversation_messages (
    id varchar(36) not null primary key,
    conversation_id varchar(36) not null,
    participant_type varchar(255),
    participant_id bigint,
    agent varchar(255) not null,
    role varchar(25) not null,
    content text not null,
    attachments text not null,
    steps text not null,
    usage text not null,
    meta text not null,
    status varchar(25) not null default 'completed',
    created_at timestamp(0) without time zone,
    updated_at timestamp(0) without time zone
);
```

The store's behavior, method by method:

- `StoreConversation(ctx, title, id)` inserts a conversation and returns its id (a new UUIDv7 when `id` is `""`).
- `StoreUserMessage(ctx, conversationID, agentClass, content)` inserts a `user` row with empty `attachments`, `steps`, `usage` and `meta`, and returns its id.
- `StoreAssistantMessage(ctx, conversationID, prompt, response, failure)` inserts an `assistant` row holding the response's text, steps, usage and meta, with status `completed`, or `failed` (and `meta.error`) when `failure` isn't `nil`.
- `LatestConversationMessages(ctx, conversationID, limit)` loads the newest `limit` rows, oldest first, rebuilding each assistant row step by step so every tool result answers the message that made its call.
- `UpdateMessage(ctx, id, values)` writes `values` onto the stored message with that id, for stores that [stamp their own columns](#extending-the-store).

Message ids are UUIDv7s, so they sort in the order they were written; `LatestConversationMessages` reads the newest by id. `StoreUserMessage` and `StoreAssistantMessage` both touch the conversation's `updated_at`. Timestamps come from the context's clock (`clock.Now(ctx)`), and like every time reef's [database layer](database.md) binds, they are written in UTC at the whole second, as `timestamp(0)` columns hold them. The participant columns are always `NULL`: ownership is your application's to record.

A message's `status` is one of the `ai.MessageCompleted` (`completed`), `ai.MessageFailed` (`failed`) and `ai.MessagePaused` (`paused`) constants. reef writes the first two.

The JSON columns keep a fixed shape and key order, which existing conversations and the frontend that reads them depend on:

| Column | Shape |
|--------|-------|
| `steps` | A list with one entry per step: `content`, `tool_calls` (each `id`, `name`, `arguments`, `result_id`, then the answering `result`, plus `denied` and `failed` when true), `reasoning`, `replay_blocks` and `provider_tool_calls` (both empty). |
| `usage` | `input_tokens`, `output_tokens`, `cache_read_input_tokens`, `cache_write_input_tokens`, `reasoning_tokens`. |
| `meta` | `provider`, `model`, `citations` (each `url`, `title`, `start_index`, `end_index`), and `error` for a failed turn. |

These are the tables and row format of the Laravel AI SDK. Write the JSON columns with `phpjson.Encode` when your rows were first written by a PHP application (see [compatibility](compatibility.md)).

### Extending the Store

To store more than reef does, embed `*ai.DatabaseConversationStore` and override the method you need. This store stamps a column of the application's own (the UI parts its frontend renders) onto each assistant row after reef wrote it:

```go
package conversations

import (
	"context"
	"fmt"

	"example.com/flights/app/models"
	"github.com/atobeach/reef/ai"
	"github.com/atobeach/reef/db"
	"github.com/atobeach/reef/phpjson"
)

// Store stamps the app's own columns onto every assistant row.
type Store struct {
	*ai.DatabaseConversationStore
}

// NewStore builds the store over the agent_conversations tables.
func NewStore() *Store {
	return &Store{DatabaseConversationStore: ai.NewDatabaseConversationStore(phpjson.Encode)}
}

// StoreAssistantMessage stores the row, then stamps the app's own column.
func (s *Store) StoreAssistantMessage(ctx context.Context, conversationID string, prompt ai.AgentPrompt, response *ai.AgentResponse, failure error) (string, error) {
	id, err := s.DatabaseConversationStore.StoreAssistantMessage(ctx, conversationID, prompt, response, failure)
	if err != nil {
		return "", fmt.Errorf("storing the assistant message: %w", err)
	}

	if err := s.UpdateMessage(ctx, id, db.Values{models.AgentConversationMessageColumns.Parts: "[]"}); err != nil {
		return id, fmt.Errorf("stamping message %s: %w", id, err)
	}

	return id, nil
}
```

Then pass your store as the manager's `Store`.

## Structured Output

### Typed Output

When an agent's answer has a Go type, describe it as a struct and embed `ai.StructuredOutput[T]`. The embedded type's `Schema` method implements `ai.HasStructuredOutput` with `jsonschema.For[T]()`, and the embedding binds the agent to `T`, so `ai.PromptStructured` can only decode the type the model was asked for.

For example, here is a `SummariseFareRules` agent which turns an airline's fare rules into a short summary:

```go
package agents

import (
	"context"

	"github.com/atobeach/reef/ai"
)

// SummariseFareRules turns a fare's rules into three highlights, one or two
// restrictions and the change fee, if any.
type SummariseFareRules struct {
	ai.StructuredOutput[FareSummary]
}

// FareSummary is the agent's structured output, and the source of its
// schema.
type FareSummary struct {
	Highlights   []string `json:"highlights" description:"Exactly 3 things a traveller should know" items:"A short, specific fact under 100 chars"`
	Restrictions []string `json:"restrictions" description:"1-2 restrictions of the fare" items:"A restriction under 100 chars"`
	ChangeFee    *string  `json:"changeFee" description:"The fee to change the flight, or null if changes are free"`
}

// Class implements ai.Agent.
func (SummariseFareRules) Class() string { return `App\Ai\Agents\SummariseFareRules` }

// Provider implements ai.HasProvider.
func (SummariseFareRules) Provider() string { return "openrouter" }

// Model implements ai.Agent.
func (SummariseFareRules) Model() string { return "openai/gpt-5" }

// Instructions implements ai.Agent.
func (SummariseFareRules) Instructions(context.Context) string {
	return "You summarise an airline's fare rules for travellers in plain language."
}
```

```go
summary, err := ai.PromptStructured(ctx, manager, agents.SummariseFareRules{}, fareRules)
if err != nil {
	return err
}

summary.Highlights // []string
```

`PromptStructured[T]` accepts an `ai.StructuredAgent[T]`: an agent that embeds `StructuredOutput[T]`, which is how Go infers `T` from the agent alone. It prompts without streaming and decodes the answer with `encoding/json`; a field of the wrong type fails the decode. Its errors are wrapped with the agent's short name, such as `prompting SummariseFareRules: ...`. See [JSON Schema](json-schema.md#deriving-a-schema-from-a-go-type) for the struct tags `For` reads.

### Hand-Written Schemas

You may also write the schema yourself by implementing `ai.HasStructuredOutput`, then read the answer from `response.Structured` or decode it with `response.Decode`:

```go
// Schema implements ai.HasStructuredOutput.
func (ScoreReview) Schema() jsonschema.Properties {
	return jsonschema.Properties{
		{Name: "feedback", Type: jsonschema.String().Required()},
		{Name: "score", Type: jsonschema.Integer().Min(1).Max(10).Required()},
	}
}
```

```go
response, err := manager.Prompt(ctx, agents.ScoreReview{}, review)
if err != nil {
	return err
}

var score struct {
	Feedback string `json:"feedback"`
	Score    int    `json:"score"`
}

err = response.Decode(&score)
```

Structured output is only available through `Prompt` and `PromptStructured`. Streaming a structured agent fails with `ai: streaming structured output is not currently supported`.

### How the Answer Is Read

reef asks for JSON with a `response_format` of type `json_schema`, named `schema_definition`, whose schema is [`jsonschema.ObjectSchema`](json-schema.md#object-schemas) of your properties (every object closed to additional properties), with `strict` off.

The final step's text is then decoded as a JSON object. If it isn't one, reef looks inside the first fenced code block (models wrap their JSON in a `json` fence despite the response format). A top-level list becomes an object keyed `"0"`, `"1"`, and so on. Anything else decodes as an empty object.

> [!WARNING]
> An answer that isn't JSON at all is read as an empty object rather than an error, and a field the model left out decodes as its zero value. `PromptStructured` then returns a zero-valued `T` with a `nil` error. Check the fields you rely on before using them.

## Middleware

Agent middleware wraps every generation step of a run, prompted or streamed. A middleware implements `ai.StepMiddleware`:

```go
type StepMiddleware interface {
	Handle(ctx context.Context, step PendingStep, next StepHandler) (*StepResponse, error)
}
```

`next` is an `ai.StepHandler`, a `func(ctx context.Context, step PendingStep) (*StepResponse, error)` that sends the step on. Call it to send the step (as it is, or narrowed), or return a response of your own to answer the step without the provider:

```go
package middleware

import (
	"context"
	"log/slog"

	"github.com/atobeach/reef/ai"
)

// LogSteps logs every step the agent sends.
type LogSteps struct{}

// Handle implements ai.StepMiddleware.
func (LogSteps) Handle(ctx context.Context, step ai.PendingStep, next ai.StepHandler) (*ai.StepResponse, error) {
	slog.InfoContext(ctx, "agent step", "step", step.Number, "model", step.Model, "tools", step.ToolNames())

	response, err := next(ctx, step)
	if err != nil {
		return nil, err
	}

	slog.InfoContext(ctx, "agent step finished", "step", step.Number, "reason", response.FinishReason)

	return response, nil
}
```

An agent lists its middleware through `ai.HasMiddleware`. The first entry is the outermost: it sees the step first and the response last.

```go
func (a *Concierge) Middleware() []ai.StepMiddleware {
	return []ai.StepMiddleware{aimiddleware.LogSteps{}, aimiddleware.OneSearchPerTurn{}}
}
```

A middleware only sees a step before the provider answers it. To bound the tool calls a model makes side by side in one answer, a middleware also implements `ai.ToolCallLimiter`; see [Limiting Tool Calls](ai-tools.md#limiting-tool-calls).

### Narrowing a Step

`ai.PendingStep`'s methods return a changed copy of the step, so you may chain them and pass the result to `next`:

| Method | Description |
|--------|-------------|
| `IsFirstStep()` | Whether this is the run's first step. |
| `ToolNames()` | The names of the step's tools, in order. |
| `WithModel(model)` | Use another model for this step. |
| `WithInstructions(instructions)` | Use other instructions for this step. |
| `WithoutTools(names...)` | Drop the named tools from this step. |
| `OnlyTools(names...)` | Keep only the named tools. |
| `WithToolChoice(choice)` | Set this step's [tool choice](ai-tools.md#tool-choice) (`nil` clears it). |

A middleware often decides from the steps the run has already completed. This one withdraws `SearchFlights` once it has returned a result:

```go
// OneSearchPerTurn withdraws SearchFlights once it has run.
type OneSearchPerTurn struct{}

// Handle implements ai.StepMiddleware.
func (OneSearchPerTurn) Handle(ctx context.Context, step ai.PendingStep, next ai.StepHandler) (*ai.StepResponse, error) {
	for _, completed := range step.Steps {
		for _, result := range completed.ToolResults {
			if result.Name == "SearchFlights" && result.Successful() {
				step = step.WithoutTools("SearchFlights")
			}
		}
	}

	return next(ctx, step)
}
```

And this one answers the final step on a smaller model with tool use switched off:

```go
// CheapFinalStep answers the last step on a smaller model, with no tools.
type CheapFinalStep struct{}

func (CheapFinalStep) Handle(ctx context.Context, step ai.PendingStep, next ai.StepHandler) (*ai.StepResponse, error) {
	if !step.IsFinalStep {
		return next(ctx, step)
	}

	return next(ctx, step.
		WithModel("openai/gpt-5-mini").
		WithToolChoice(&ai.ToolChoice{Mode: ai.ToolChoiceNone}))
}
```

The tools a narrowed step still offers are the ones its tool calls run against. If the model calls a tool you withdrew anyway, the call is treated as a call to an unknown tool (see [Repairing Tool Calls](ai-tools.md#repairing-tool-calls)). An agent whose middleware withdraws tools should therefore repair tool calls, as `Concierge` does.

### Setting Generation Options

A step's `Options` are shared with the rest of the run, so copy them before changing a field:

```go
// Deterministic pins the sampling options.
type Deterministic struct{}

func (Deterministic) Handle(ctx context.Context, step ai.PendingStep, next ai.StepHandler) (*ai.StepResponse, error) {
	options := *step.Options
	options.Temperature = new(0.0)
	options.MaxTokens = new(1024)
	step.Options = &options

	return next(ctx, step)
}
```

### Answering a Step Yourself

A middleware may return a `*ai.StepResponse` without calling `next`. The provider is never called, and the loop treats the response like any other: its tool calls run when its `FinishReason` is `ai.FinishToolCalls`.

```go
// Canned answers a greeting without the provider.
type Canned struct{}

func (Canned) Handle(ctx context.Context, step ai.PendingStep, next ai.StepHandler) (*ai.StepResponse, error) {
	if last, ok := step.Messages[len(step.Messages)-1].(ai.UserMessage); ok && last.Content == "ping" {
		return &ai.StepResponse{Text: "pong", FinishReason: ai.FinishStop}, nil
	}

	return next(ctx, step)
}
```

In a streamed run, a response the provider never streamed is described to the client as events: a `StreamStart`, then `TextStart`, `TextDelta` and `TextEnd` when the text isn't blank, then a `ToolCallEvent` per tool call. `StepCompleted` is not dispatched for it.

Returning `nil, nil` is an error: a prompted run fails with a `ResponseError` reading `Agent middleware must return the next step result or a StepResponse.`, and a streamed run with a `StreamError`.

### Pending Steps

An `ai.PendingStep` is the step about to be sent:

| Field | Description |
|-------|-------------|
| `Number` | The step's number, from zero. |
| `IsFinalStep` | Whether this is the last step the budget allows. |
| `Provider`, `Model` | The provider's name and the model. |
| `Instructions` | The system prompt. |
| `Messages` | The history so far: the conversation, the prompt, and this run's earlier steps (a copy). |
| `Tools` | The tools offered. |
| `Schema` | The structured-output schema, if any. |
| `Options` | The [generation options](#generation-options). |
| `Steps` | The run's completed steps, with their tool results. |
| `Usage` | The usage summed over the completed steps. |
| `Timeout` | The step's timeout. |
| `InvocationID` | The run's id. |

## The Agent Loop

Every run, prompted or streamed, is the same loop:

1. Send a step: the instructions, the history and prompt, the step's tools, and the schema of a structured agent.
2. If the step finished with `tool_calls`, run each call in the order the model made them, one at a time, against the step's tools.
3. Append the model's message and the tool results to the history, and send the next step.

The loop ends when a step makes no tool calls, or when the [step budget](#step-limits) is spent. Tool calls only run when the step's finish reason is `tool_calls`.

### Steps

Each completed round trip is an `*ai.Step` on the response:

| Field | Description |
|-------|-------------|
| `Text` | The step's text. |
| `ToolCalls` | The tool calls the model made. |
| `ToolResults` | Their results. |
| `FinishReason` | Why the step ended. |
| `Usage` | The step's token usage. |
| `Meta` | The provider and model it ran on. |
| `Reasoning` | The step's visible reasoning. |

What a gateway or middleware returns for one step is an `*ai.StepResponse`: the same fields without `ToolResults`, plus `Structured`, the decoded output of a structured step.

A prompted run's loop produces an `*ai.TextResponse` (`Text`, `Usage`, `Meta`, `Steps`, `Messages`, `ToolCalls`, `ToolResults`, `Reasoning`, `Structured`), which `Prompt` copies into the `AgentResponse` it returns. Its `Reasoning` joins the steps' non-blank reasoning with a blank line.

The manager tracks each run in an `*ai.RunContext`: its `InvocationID`, `Agent`, `Provider`, `Model`, the `Events` dispatcher, and the steps it has completed (what a [failed turn](#failed-turns) is stored from). You never build one yourself.

### Finish Reasons

`ai.FinishReason` is why a step ended:

| Constant | Value |
|----------|-------|
| `ai.FinishStop` | `stop` |
| `ai.FinishToolCalls` | `tool_calls` |
| `ai.FinishLength` | `length` |
| `ai.FinishContentFilter` | `content_filter` |
| `ai.FinishContinue` | `continue` |
| `ai.FinishUnknown` | `unknown` (any reason the provider sent that isn't one of the above) |

The OpenRouter gateway never reports `continue`. A custom gateway or middleware may return it to keep the loop going even though the step made no tool calls.
