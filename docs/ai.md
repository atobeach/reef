# AI

- [Introduction](#introduction)
- [Configuration](#configuration)
    - [Providers](#providers)
    - [Custom Base URLs and Headers](#custom-base-urls-and-headers)
    - [The Gateway](#the-gateway)
    - [JSON Encoding](#json-encoding)
- [Agents](#agents)
    - [Prompting](#prompting)
    - [Conversation Context](#conversation-context)
    - [Structured Output](#structured-output)
    - [Streaming](#streaming)
    - [Tools](#tools)
    - [Middleware](#middleware)
    - [Agent Configuration](#agent-configuration)
- [Responses](#responses)
    - [Token Usage](#token-usage)
    - [Provider Metadata](#provider-metadata)
- [Provider Errors](#provider-errors)
    - [Failover](#failover)
- [Testing](#testing)
- [Events](#events)

## Introduction

The `github.com/atobeach/reef/ai` package runs AI agents. An agent is a small Go type that holds the instructions, tools, conversation context and output schema for one job (a flight-booking concierge, a fare-rules summariser, a destination writer). You configure it once and prompt it wherever you need it.

reef sends every request through one OpenAI-compatible Chat Completions API (OpenRouter by default, or any endpoint with the same shape). Each prompt runs a tool-call loop: the model may call your tools, reef runs them and sends the results back, and the loop goes on until the model answers. A run can be prompted, returning the finished response, or streamed, emitting typed events you can forward to the browser as [AG-UI](https://docs.ag-ui.com/concepts/events) server-sent events. Conversations can be remembered in the `agent_conversations` and `agent_conversation_messages` tables.

The AI documentation is split over several pages:

- This page: configuration, prompting, responses, errors, testing and events.
- [Agents](ai-agents.md): agent configuration, conversations, structured output, middleware and the agent loop.
- [Tools](ai-tools.md): defining tools, reading the model's arguments, and bounding which calls run.
- [Streaming](ai-streaming.md): streamed runs, stream events and the AG-UI protocol.
- [JSON Schema](json-schema.md): the schema builder that tools and structured output use.

> [!NOTE]
> The package covers text agents only. There are no image, audio, transcription, embedding, reranking, file or vector store APIs.

## Configuration

Your application builds one `ai.Manager` at boot and hands it to whatever prompts agents: controllers, jobs and commands take it as a field. `ai.NewManager` accepts an `ai.ManagerConfig`:

```go
import (
	"github.com/atobeach/reef/ai"
	"github.com/atobeach/reef/env"
	"github.com/atobeach/reef/httpclient"
)

manager := ai.NewManager(ai.ManagerConfig{
	Providers: map[string]ai.ProviderConfig{
		"openrouter": {
			Name:   "openrouter",
			Driver: "openrouter",
			Key:    env.Get("OPENROUTER_API_KEY", ""),
			URL:    env.Get("OPENROUTER_URL", ""),
		},
	},
	Default: "openrouter",
	Gateway: ai.OpenRouterGateway{HTTP: httpclient.New},
	Store:   ai.NewDatabaseConversationStore(nil),
	Events:  dispatcher,
})
```

The `ManagerConfig` fields are:

| Field | Description |
|-------|-------------|
| `Providers` | The configured providers, keyed by name. |
| `Default` | The provider an agent runs on when it names none. |
| `Gateway` | What sends each generation step to a provider. Use `ai.OpenRouterGateway`. |
| `Store` | The `ai.ConversationStore` that remembers conversations. When `nil`, nothing is remembered. |
| `Events` | The `*events.Dispatcher` that receives the [lifecycle events](#events). When `nil`, they are dropped. |

A typical reef application builds the manager in its `bootstrap` package, keeps it on the container as `a.AI`, and passes it to the chat controller, the tools and the jobs that use it.

### Providers

Each `ai.ProviderConfig` describes one provider:

| Field | Description |
|-------|-------------|
| `Name` | What stream events and stored message metadata report as the provider. Defaults to the map key. |
| `Driver` | The name passed to an agent's [`ProviderOptions`](ai-agents.md#provider-options). Defaults to the map key. |
| `Key` | The API key, sent as `Authorization: Bearer <key>`. |
| `URL` | Overrides the base URL (`https://openrouter.ai/api/v1`). |
| `HTTPReferer` | Sent as OpenRouter's `HTTP-Referer` attribution header when set. |
| `Title` | Sent as OpenRouter's `X-OpenRouter-Title` attribution header when set. |
| `Headers` | Extra headers for every request. They are applied last, so they win over the ones above. |

An agent picks its provider through `ai.HasProvider`; one that doesn't (or returns `""`) runs on `Default`. Prompting an agent whose provider isn't configured fails with `ai: provider [name] is not configured`.

### Custom Base URLs and Headers

Any OpenAI-compatible endpoint works. Point `URL` at it (a trailing slash is trimmed) and add whatever headers it needs:

```go
Providers: map[string]ai.ProviderConfig{
	"openrouter": {
		Key:         openRouterKey,
		HTTPReferer: "https://example.com",
		Title:       "Flights",
	},
	"local": {
		Driver:  "openrouter",
		URL:     "http://127.0.0.1:4000/v1",
		Headers: map[string]string{"X-Team": "search"},
	},
},
```

Every request is a `POST` to `<URL>/chat/completions`.

### The Gateway

The `ai.Gateway` interface sends one generation step to a provider. reef ships one implementation, `ai.OpenRouterGateway`, which speaks the Chat Completions API:

| Field | Description |
|-------|-------------|
| `HTTP` | Builds the `*httpclient.Client` for a connector. Pass your application's client factory (such as `a.HTTP`), so a test's mock answers every model call. `httpclient.New` works too. |
| `Encode` | The [encoder](#json-encoding) for request bodies (`nil`: `encoding/json`). |

The gateway connects with a 10 second connect timeout and bounds each step with the agent's [timeout](ai-agents.md#timeouts) (60 seconds by default). Its requests go through [reef's HTTP client](http-client.md) as two request types: `ai.ChatCompletion` for a plain step and `ai.StreamChatCompletion` (which embeds it) for a streamed one, so a test can script and assert on the two apart. Both implement `httpclient.Request` (`Method` is `POST`, `Endpoint` is `chat/completions`), `httpclient.WithBody` (`Body`, the step's request body) and `httpclient.WithHeaders` (`Headers`). You will only meet them in tests:

```go
httpclienttest.AssertSentTimes[ai.StreamChatCompletion](t, tc.HTTP, 2)
```

The manager calls `GenerateStep` for each step of a prompted run and `StreamStep` for each step of a streamed one. If you need another transport, implement `ai.Gateway` yourself:

```go
type Gateway interface {
	// GenerateStep runs the step without streaming.
	GenerateStep(ctx context.Context, provider ProviderConfig, step PendingStep) (*StepResponse, error)
	// StreamStep runs the step streaming, emitting its events as they arrive.
	// A nil response with a nil error means the provider ended the stream
	// with an error event (already emitted).
	StreamStep(ctx context.Context, invocationID string, provider ProviderConfig, step PendingStep, emit func(StreamEvent)) (*StepResponse, error)
}
```

### JSON Encoding

Three parts of the package write JSON: the gateway (request bodies, plus the tool arguments and structured tool results re-encoded inside them), the conversation store (its JSON columns) and the AG-UI protocol (its frames). Each takes an `ai.Encoder`, a `func(value any) ([]byte, error)`. A `nil` encoder is `encoding/json`.

You only need another encoder when bytes already exist in another dialect. If you're moving an existing Laravel application to reef and its conversations were stored by laravel/ai, keep writing PHP's JSON dialect: pass `phpjson.Encode` to the gateway and the store, and history you resend reaches the model in exactly the bytes it was stored in:

```go
Gateway: ai.OpenRouterGateway{HTTP: a.HTTP, Encode: phpjson.Encode},
```

See [compatibility](compatibility.md) for the `phpjson` dialect.

## Agents

An agent is any type that implements `ai.Agent`:

```go
package agents

import "context"

// TravelWriter writes short destination blurbs.
type TravelWriter struct{}

// Class implements ai.Agent.
func (TravelWriter) Class() string { return `App\Ai\Agents\TravelWriter` }

// Model implements ai.Agent.
func (TravelWriter) Model() string { return "openai/gpt-5" }

// Instructions implements ai.Agent.
func (TravelWriter) Instructions(context.Context) string {
	return "You write warm, accurate, two-sentence destination blurbs for travellers."
}
```

`Class` is the agent's stable identifier: it is stored on every conversation message and reported by instrumentation, so it must never change once rows carry it. Any string works; these docs use PHP-style class names such as `App\Ai\Agents\TravelWriter`, the names laravel/ai writes, so a conversations table shared with a Laravel application reads the same on both sides (see [the class name](ai-agents.md#the-class-name)). `Model` is the provider's model id. `Instructions` is the system prompt, evaluated once per run.

Everything else an agent can do (tools, history, middleware, a step limit, a timeout) it opts into by implementing one more small interface. reef has no `make:agent` generator: write the type by hand, usually in an `app/ai/agents` package of your own.

`TravelWriter`, `Concierge`, `SummariseFareRules` and `ScoreReview` on these pages, and the `SearchFlights` and `SwitchCurrency` tools and `LogSteps` and `OneSearchPerTurn` middleware they use, are illustrations written for these docs, introduced in [Agents](ai-agents.md#defining-agents).

### Prompting

To prompt an agent, pass it to the manager's `Prompt` method along with the prompt:

```go
response, err := manager.Prompt(ctx, agents.TravelWriter{}, "Write a blurb for Lisbon.")
if err != nil {
	return err
}

fmt.Println(response.Text)
```

`Prompt` runs the whole tool-call loop without streaming and returns an `*ai.AgentResponse` once the model has answered. See [Responses](#responses) for what it holds.

A panic inside the run (in the agent's history, instructions or tools, a tool's `Handle`, a step middleware, or storing the turn) never escapes `Prompt`: it comes back as an error reading `ai: run panicked: <value>`, logged with its stack. See [Returning Results](ai-tools.md#returning-results).

### Conversation Context

An agent that implements `ai.Conversational` sends its history before the prompt. Most agents embed `ai.RemembersConversations` instead, which loads the conversation's latest 100 messages from the store and stores each new turn:

```go
type Concierge struct {
	ai.RemembersConversations

	Timezone string
}

agent := &agents.Concierge{Timezone: "Europe/London"}
agent.ContinueConversation(conversationID)

response, err := manager.Prompt(ctx, agent, "What about an earlier flight?")
```

The [agents documentation](ai-agents.md#conversation-context) covers history, the conversation store and failed turns.

### Structured Output

When you want a typed answer rather than prose, describe it as a Go struct and embed `ai.StructuredOutput[T]` in the agent. `ai.PromptStructured` prompts the agent and decodes the answer into `T`:

```go
// FareSummary is the agent's structured output, and the source of its schema.
type FareSummary struct {
	Highlights   []string `json:"highlights" description:"Exactly 3 things a traveller should know" items:"A short, specific fact under 100 chars"`
	Restrictions []string `json:"restrictions" description:"1-2 restrictions of the fare" items:"A restriction under 100 chars"`
	ChangeFee    *string  `json:"changeFee" description:"The fee to change the flight, or null if changes are free"`
}

type SummariseFareRules struct {
	ai.StructuredOutput[FareSummary]
}

summary, err := ai.PromptStructured(ctx, manager, agents.SummariseFareRules{}, fareRules)
```

The schema the model is asked to follow is derived from the struct, so the two cannot drift. See [Structured Output](ai-agents.md#structured-output) for the details and [JSON Schema](json-schema.md#deriving-a-schema-from-a-go-type) for the struct tags.

### Streaming

`Stream` returns an `*ai.StreamableAgentResponse` that runs when you iterate it, handing you each event as it arrives:

```go
streamable, err := manager.Stream(ctx, agents.TravelWriter{}, "Write a blurb for Porto.")
if err != nil {
	return err
}

err = streamable.Each(ctx, func(event ai.StreamEvent) {
	if delta, ok := event.(ai.TextDelta); ok {
		fmt.Print(delta.Delta)
	}
})
```

To stream a run to the browser, wrap it in the AG-UI protocol. See the [streaming documentation](ai-streaming.md).

### Tools

Tools give an agent functions it may call. A tool implements `ai.Tool`, and an agent offers its tools by implementing `ai.HasTools`:

```go
func (a *Concierge) Tools(context.Context) []ai.Tool {
	return []ai.Tool{
		&tools.SearchFlights{},
		&tools.SwitchCurrency{Supported: []string{"EUR", "GBP", "USD"}},
	}
}
```

See the [tools documentation](ai-tools.md).

### Middleware

Agent middleware wraps every generation step of a run, so you may log, narrow or answer a step before it reaches the provider. An agent lists its middleware by implementing `ai.HasMiddleware`:

```go
func (a *Concierge) Middleware() []ai.StepMiddleware {
	return []ai.StepMiddleware{aimiddleware.LogSteps{}, aimiddleware.OneSearchPerTurn{}}
}
```

See [Middleware](ai-agents.md#middleware).

### Agent Configuration

Where Laravel configures an agent with attributes, a reef agent implements small interfaces. Each is optional:

| Interface | Method | Description |
|-----------|--------|-------------|
| `ai.HasProvider` | `Provider() string` | The configured provider to run on (else `Default`). |
| `ai.HasTools` | `Tools(ctx) []ai.Tool` | The tools the model may call. |
| `ai.HasMiddleware` | `Middleware() []ai.StepMiddleware` | Middleware wrapping each step. |
| `ai.HasStructuredOutput` | `Schema() jsonschema.Properties` | The JSON the model must answer with. |
| `ai.HasMaxSteps` | `MaxSteps() int` | The step budget. |
| `ai.HasTimeout` | `Timeout() time.Duration` | The per-step provider timeout (default 60 seconds). |
| `ai.HasProviderOptions` | `ProviderOptions(provider string) *ordered.Object` | Extra keys merged into the request body. |
| `ai.RepairsToolCalls` | `RepairToolCalls() bool` | Answer calls to unknown tools with an error result instead of failing the run. |
| `ai.Conversational` | `Messages(ctx, store) ([]ai.Message, error)` | History sent before the prompt. |

The [agents documentation](ai-agents.md#agent-configuration) describes each one.

## Responses

Both `Prompt` and a completed stream produce an `*ai.AgentResponse`:

| Field | Description |
|-------|-------------|
| `InvocationID` | The run's id (a UUIDv7), shared by its events. |
| `Text` | The final answer. Prompted: the last step's text. Streamed: every text delta of the run, concatenated. |
| `Usage` | The run's summed [token usage](#token-usage). |
| `Meta` | The [provider, model and citations](#provider-metadata). |
| `Steps` | Every model round trip, as `*ai.Step`. |
| `Messages` | The messages the run added (`ai.AssistantMessage` and `ai.ToolResultMessage`), for a prompted run. |
| `ToolCalls` | Every tool call the model made. |
| `ToolResults` | Every tool call's result. |
| `Reasoning` | The model's visible reasoning, when it sent any. |
| `Structured` | The decoded structured output of a structured agent, as an `*ordered.Object`. |
| `Events` | A streamed run's events, in order (`nil` when the run was not streamed). |
| `ConversationID`, `UserMessageID`, `AssistantMessageID` | The stored conversation and message ids of a [remembered](ai-agents.md#remembering-conversations) turn. |

`Streamed` reports whether the run was streamed, and `Decode` unmarshals the structured output into a value of your own with `encoding/json`:

```go
response, err := manager.Prompt(ctx, agents.ScoreReview{}, review)
if err != nil {
	return err
}

var score struct {
	Feedback string `json:"feedback"`
	Score    int    `json:"score"`
}

if err := response.Decode(&score); err != nil {
	return err
}
```

`Decode` fails with `ai: response has no structured output` when the response carries none.

### Token Usage

`ai.TextUsage` is a step's token usage, or a run's sum:

| Field / Method | Description |
|----------------|-------------|
| `InputTokens`, `OutputTokens` | Prompt and completion tokens. |
| `CacheRead`, `CacheWrite`, `Reasoning` | Cached input, cache-write and reasoning tokens. Each is `nil` when the provider did not report it. |
| `Add(other)` | The sum of two usages. A nullable count stays `nil` only when both are `nil`. |
| `TotalTokens()` | Input plus output. |
| `UncachedInputTokens()` | Input minus cache reads and writes. |

```go
usage := response.Usage

slog.InfoContext(ctx, "agent usage", "total", usage.TotalTokens(), "uncached", usage.UncachedInputTokens())
```

### Provider Metadata

`ai.Meta` holds the `Provider` and `Model` a response came from (both `*string`) and its `Citations`. Each `ai.Citation` is a URL citation the provider attached to the text: `URL`, plus optional `Title`, `StartIndex` and `EndIndex`. Citations are read from streamed steps.

Set these optional fields with Go's `new`:

```go
meta := ai.Meta{Provider: new("openrouter"), Model: new("openai/gpt-5")}
```

## Provider Errors

A failed provider call returns one of these errors, which you may inspect with `errors.As`:

| Error | When |
|-------|------|
| `*ai.ProviderError` | The provider is out of credits, rate limited, overloaded or unreachable: every turn fails the same way until it recovers. |
| `*ai.RequestError` | Any other failed status. `Status` is the HTTP status and `Body` the provider's body. |
| `*ai.ResponseError` | The provider answered with something unusable. `Message` is what the run reports; `Err` (when set, and what `Unwrap` returns) is why, such as a decode failure. |
| `*ai.StreamError` | The provider failed a step inside a stream. See [Stream Errors](ai-streaming.md#stream-errors). |
| `*ai.NoSuchToolError` | The model called a tool the step did not offer. See [Repairing Tool Calls](ai-tools.md#repairing-tool-calls). |
| `*ai.TurnDeadlineError` | A streamed run passed its overall deadline. See [Run Deadlines](ai-streaming.md#run-deadlines). |

A `ProviderError`'s `Kind` is one of the `ai.ProviderErrorKind` constants, chosen from the answer:

| Kind | Value | Cause | Message |
|------|-------|-------|---------|
| `ai.InsufficientCredits` | `insufficient_credits` | HTTP 402 | `AI provider [openrouter] has insufficient credits or quota.` |
| `ai.RateLimited` | `rate_limited` | HTTP 429 | `Application rate limited by AI provider [openrouter].` |
| `ai.ProviderOverloaded` | `overloaded` | HTTP 502, 503, 504, or Cloudflare's 520, 522 and 524 | `AI provider [openrouter] is overloaded.` |
| `ai.ProviderConnection` | `connection` | The request got no answer: the provider was unreachable, or the step's timeout passed first | `Could not connect to AI provider [openrouter].` |

`Provider` names the provider and `Status` the HTTP status (zero for a connection failure); `Err`, which `Unwrap` returns, is the transport error. A canceled context is never turned into a `ProviderError`: it comes back as `context.Canceled`.

> [!NOTE]
> A `RequestError`'s message carries the status only (`HTTP request returned status code 400`). The provider's body can echo the prompt, and error messages end up in error tracking, so the body stays on the `Body` field.

OpenRouter can also answer `200` with an `error` object in place of a completion. That fails the step with a `ResponseError` whose message is `OpenRouter Error: [<type or code>] <message>`. An answer that is not a completion at all fails with `OpenRouter Error: [unknown] Unknown OpenRouter error.`, its decode error on `Err`.

### Failover

reef does not retry or switch providers by itself. `ai.IsFailoverable` reports whether an error is (or wraps) a `ProviderError`, so you can tell an outage from a broken reply and say so. A chat can show "temporarily unavailable" for these instead of "try again":

```go
if ai.IsFailoverable(err) {
	// Every turn fails this way until the provider recovers.
}

var providerErr *ai.ProviderError
if errors.As(err, &providerErr) && providerErr.Kind == ai.InsufficientCredits {
	// Top up the account.
}
```

## Testing

reef does not fake agents themselves. Instead, the `github.com/atobeach/reef/ai/aitest` package scripts the provider: it answers the gateway's `ChatCompletion` and `StreamChatCompletion` requests from your test's HTTP mock with the exact bodies a model would send. Your test then runs the real gateway, loop, tools and protocol without a live model call:

```go
mock := httpclient.NewMock(t, map[string]any{})
aitest.ScriptCompletions(mock,
	aitest.Completion(`{"highlights":["One cabin bag included."],"restrictions":["Non-refundable."],"changeFee":null}`),
)

manager := ai.NewManager(ai.ManagerConfig{
	Providers: map[string]ai.ProviderConfig{"openrouter": {Key: "test-key"}},
	Default:   "openrouter",
	Gateway: ai.OpenRouterGateway{HTTP: func(c httpclient.Connector) *httpclient.Client {
		return httpclient.New(c).WithMock(mock)
	}},
})

summary, err := ai.PromptStructured(t.Context(), manager, agents.SummariseFareRules{}, "fare rules")
```

In a feature test whose test case builds the manager on its mock client (`tc.HTTP` here; see [a full test case](testing.md#a-full-test-case)), you only script the steps. A streamed turn that calls a tool and then answers looks like this:

```go
aitest.Script(tc.HTTP,
	aitest.Stream(aitest.ToolCall("call-1", "SearchFlights", `{"origin":"LHR","destination":"JFK"}`)),
	aitest.Stream(aitest.Text("Here are three flights.")),
)
```

See [mocking](mocking.md) for every `aitest` helper, and [Testing Tools](ai-tools.md#testing-tools) for calling a tool directly.

## Events

When `ManagerConfig.Events` is set, the manager dispatches these [events](events.md) during a run:

| Event | When |
|-------|------|
| `ai.PromptingAgent` | Before a prompted run starts. |
| `ai.StreamingAgent` | When a streamed run starts producing (on iteration). |
| `ai.StepCompleted` | After a provider step returned. |
| `ai.StepFailed` | When a provider step failed, or when storing a failed turn's user message failed (then `StepNumber` is 0 and `Model` is empty). |
| `ai.InvokingTool` | Before a tool handles a call. |
| `ai.ToolInvoked` | After a tool handled a call. |
| `ai.ToolFailed` | When a tool returned an error. |
| `ai.AgentPrompted` | After a prompted run completed (and its turn was stored). |
| `ai.AgentStreamed` | After a streamed run completed (and its turn was stored, so the response carries its ids). |
| `ai.AgentFailed` | When a run failed, a panic in it included. |

Every run that starts ends in exactly one of `AgentPrompted`, `AgentStreamed` or `AgentFailed`, so a listener that keeps per-run state can always release it. You may listen for them like any other event:

```go
events.Listen(d, func(ctx context.Context, e ai.AgentPrompted) error {
	slog.InfoContext(ctx, "agent prompted",
		"agent", ai.ClassBasename(e.Prompt.Agent.Class()),
		"model", e.Prompt.Model,
		"tokens", e.Response.Usage.TotalTokens(),
	)

	return nil
})
```

Listeners observe the run and can never fail it: a listener's error is logged as a warning (`ai: event listener failed`), a panic as an error with its `stack`, and the run carries on. A listener is a good place to send these events to your LLM analytics.

The event payloads are:

| Event | Fields |
|-------|--------|
| `PromptingAgent`, `StreamingAgent` | `InvocationID`, `Prompt` |
| `AgentPrompted`, `AgentStreamed` | `InvocationID`, `Prompt`, `Response *ai.AgentResponse` |
| `AgentFailed` | `InvocationID`, `Prompt`, `Err` |
| `StepCompleted` | `InvocationID`, `StepNumber`, `Agent`, `Model`, `Response *ai.StepResponse`, `Time` |
| `StepFailed` | `InvocationID`, `StepNumber`, `Agent`, `Model`, `Err`, `Time` |
| `InvokingTool` | `InvocationID`, `ToolInvocationID`, `Agent`, `Tool`, `Arguments ai.Request` |
| `ToolInvoked` | `InvocationID`, `ToolInvocationID`, `Agent`, `Tool`, `Arguments`, `Result string`, `Time` |
| `ToolFailed` | `InvocationID`, `ToolInvocationID`, `Agent`, `Tool`, `Arguments`, `Err`, `Time` |

`Time` is the step's or the tool handler's own duration in milliseconds. `StepNumber` counts from zero. `StepCompleted` only fires for steps that reached the provider, not for steps a middleware answered itself.

`Prompt` is an `ai.AgentPrompt`, one prompt to one agent:

| Field | Description |
|-------|-------------|
| `Agent` | The agent. |
| `Prompt` | The prompt text. |
| `Provider` | The provider's name. |
| `Model` | The model. |
| `Timeout` | The per-step timeout the run uses. |

`ai.ClassBasename` returns the last backslash-separated segment of a `Class` (`App\Ai\Agents\Concierge` is `Concierge`; a class without a backslash is itself), the short name analytics report.
