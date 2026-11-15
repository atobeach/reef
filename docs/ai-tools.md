# Tools

- [Introduction](#introduction)
- [Defining Tools](#defining-tools)
    - [Tool Schemas](#tool-schemas)
    - [Offering Tools to an Agent](#offering-tools-to-an-agent)
- [Reading Arguments](#reading-arguments)
    - [Available Methods](#available-methods)
- [Returning Results](#returning-results)
    - [Reporting Failures to the Model](#reporting-failures-to-the-model)
- [Tool Choice](#tool-choice)
- [Repairing Tool Calls](#repairing-tool-calls)
- [Limiting Tool Calls](#limiting-tool-calls)
- [Tool Calls and Results](#tool-calls-and-results)
- [Tool Events](#tool-events)
- [Testing Tools](#testing-tools)

## Introduction

Tools give an agent functions it may call while it answers: searching flights, looking up an airport, switching the user's currency. The model decides when to call a tool and with which arguments; reef runs the tool and sends its result back for the model's next step. This page assumes you have read the [AI overview](ai.md).

## Defining Tools

A tool implements the `ai.Tool` interface:

```go
type Tool interface {
	Name() string
	Description() string
	Schema() jsonschema.Properties
	Handle(ctx context.Context, req Request) (string, error)
}
```

`Name` is what the model calls the tool, and what stored tool calls name, so treat it as stable. `Description` tells the model when to use it. `Schema` describes its arguments, and `Handle` runs a call. Here is the currency tool the `Concierge` agent on the [Agents](ai-agents.md#defining-agents) page uses. It accepts a fixed list of currencies:

```go
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/atobeach/reef/ai"
	"github.com/atobeach/reef/jsonschema"
)

// SwitchCurrency switches the currency prices are shown in.
type SwitchCurrency struct {
	Supported []string
}

// Name implements ai.Tool.
func (*SwitchCurrency) Name() string { return "SwitchCurrency" }

// Description implements ai.Tool.
func (*SwitchCurrency) Description() string {
	return "Switch the currency prices are shown in, after the user agrees to it."
}

// Schema implements ai.Tool.
func (*SwitchCurrency) Schema() jsonschema.Properties {
	return jsonschema.Properties{
		{Name: "currency", Type: jsonschema.String().Description(`The 3-letter currency code to switch to, e.g. "EUR".`).Required()},
	}
}

// Handle implements ai.Tool.
func (t *SwitchCurrency) Handle(ctx context.Context, req ai.Request) (string, error) {
	requested := strings.ToUpper(strings.TrimSpace(req.String("currency")))

	if !slices.Contains(t.Supported, requested) {
		return encode(map[string]any{
			"currency": requested,
			"error":    fmt.Sprintf("Prices cannot be shown in %s right now. Offer EUR, USD or GBP instead.", requested),
		})
	}

	return encode(map[string]any{"switched": true, "currency": requested})
}

func encode(payload map[string]any) (string, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encoding the tool result: %w", err)
	}

	return string(encoded), nil
}
```

A tool is a plain value holding whatever it depends on. Build it with its dependencies where you build your other services and hand it to the agent.

`Handle` receives the run's context, so it may read the clock and anything bound to the context, and it is canceled with the run.

### Tool Schemas

`Schema` returns [JSON Schema](json-schema.md) properties, built with the `jsonschema` package:

```go
func (*SearchFlights) Schema() jsonschema.Properties {
	return jsonschema.Properties{
		{Name: "origin", Type: jsonschema.String().Description("The IATA code of the departure airport").Required()},
		{Name: "destination", Type: jsonschema.String().Description("The IATA code of the arrival airport").Required()},
		{Name: "max_price", Type: jsonschema.Number().Nullable()},
		{Name: "limit", Type: jsonschema.Integer().Min(1).Max(20)},
		{Name: "cabins", Type: jsonschema.Array().Items(jsonschema.String()).Nullable()},
	}
}
```

reef offers each tool to the model as a `function` whose `parameters` are an object of these properties, with a `required` list and `additionalProperties: false`, every nested object closed the same way. A tool without parameters returns an empty `Properties` and is still sent `"properties": {}` and `"required": []`. Strict mode is off, so the model may still send arguments that drift from the schema; the [request readers](#reading-arguments) are lenient for that reason.

### Offering Tools to an Agent

An agent offers tools by implementing `ai.HasTools`. `Tools` is called once per run, with the run's context:

```go
func (a *Concierge) Tools(context.Context) []ai.Tool {
	return []ai.Tool{
		&tools.SearchFlights{},
		&tools.SwitchCurrency{Supported: []string{"EUR", "GBP", "USD"}},
	}
}
```

The order you return is the order the model sees. Whenever a step offers tools, reef sends `tool_choice: "auto"` unless the step [chooses otherwise](#tool-choice). A [middleware](ai-agents.md#narrowing-a-step) may withdraw tools for a single step.

## Reading Arguments

`Handle` receives an `ai.Request`: the model's arguments, with typed readers that forgive the ways a model's JSON drifts from its schema. An absent or `null` key reads as the zero value, a number sent as a numeric string (`"1.5"`) reads as that number, and a number read as a string reads as its JSON spelling:

```go
func (*SearchFlights) Handle(ctx context.Context, req ai.Request) (string, error) {
	origin, destination := req.String("origin"), req.String("destination")
	maxPrice := req.Float("max_price")

	limit := 10
	if req.Filled("limit") {
		limit = req.Integer("limit")
	}

	for _, cabin := range req.Array("cabins") {
		// ...
	}

	// ...
}
```

A request also carries:

| Field | Description |
|-------|-------------|
| `Arguments` | The decoded arguments as an `*ordered.Object`, in the model's key order. Arguments that aren't a JSON object read as empty. |
| `ToolCallID` | The id of the call being answered. |
| `ToolInvocationID` | A fresh UUIDv7 for this execution, shared with its [tool events](#tool-events). |

### Available Methods

#### Get

`Get` returns the raw argument at a key, `nil` when absent. Values are what `ordered.Decode` produces: `string`, `json.Number`, `bool`, `nil`, `[]any` or `*ordered.Object`.

```go
raw := req.Get("filters")
```

#### Has

`Has` reports whether a key is present, even when its value is `null`:

```go
if req.Has("max_price") {
	// ...
}
```

#### Filled

`Filled` reports whether a key is present and neither `null` nor a blank string. A boolean, number, list or object always counts as filled:

```go
if req.Filled("origin") {
	// ...
}
```

#### String

`String` returns the argument as a string: a string as it is, a number as its JSON spelling (`2.7` reads as `"2.7"`), a boolean as `"true"` or `"false"`, and `""` for `null`, a list or an object:

```go
currency := req.String("currency")
```

#### Float

`Float` returns a number, or a decimal numeric string, as a `float64` (see `support.IsNumeric`: an optional sign, digits, fraction and exponent, with no hex, `Inf` or `NaN`). Anything else reads as `0`, and so does a value that isn't finite, so `"Infinity"` or `"NaN"` never reach a price cap or a coordinate:

```go
maxPrice := req.Float("max_price")
```

#### Integer

`Integer` returns the argument as an `int`, read like `Float` with any fraction truncated (`2.7` reads as `2`):

```go
adults := req.Integer("adults")
```

#### Array

`Array` returns the argument as a list. A list is returned as it is, an object as its values in order, `null` (or an absent key) as an empty list, and a scalar as a one-item list:

```go
for _, code := range req.Array("airports") {
	// ...
}
```

#### NewRequest

`ai.NewRequest` builds a request from decoded arguments; `nil` means no arguments. The loop builds requests for you, so you'll mostly use it, and `RequestFromJSON` below, in tests:

```go
req := ai.NewRequest(arguments)
```

#### RequestFromJSON

`ai.RequestFromJSON` builds a request from a JSON object literal:

```go
req := ai.RequestFromJSON(`{"currency":"eur"}`)
```

## Returning Results

`Handle` returns the result as a string, which reef sends back to the model as the content of the call's `tool` message. Most tools return JSON, so the model (and, for a streamed run, the client rendering the result) can read its fields.

> [!WARNING]
> An error returned from `Handle` fails the whole run, not just the call. Return an error only when the run cannot go on, such as a database that is down.

A tool that panics fails the run the same way. The run recovers the panic, logs `ai: run panicked` with its `stack`, and fails with `ai: run panicked: <value>`, an error wrapping the [`*panics.Error`](errors.md#recovered-panics) that carries the stack. `AgentFailed` fires, a [remembered](ai-agents.md#failed-turns) turn is stored as failed with the steps it completed, and in a streamed run `Parts` returns the error like any other failure, so the client gets the `RUN_ERROR` you [answer failures with](ai-streaming.md#errors-mid-stream).

The same holds for a panic elsewhere in the run: a step middleware, the agent's `Instructions`, the provider's answer being read, storing the finished turn, or the function you pass to `Each`. A prompted run also recovers a panic in the agent's `Messages` and `Tools`; `Stream` calls those before the run starts, so there the panic reaches your code as it is. An event listener is the exception: it observes the run and can never fail it, so its panic is logged with its stack (`ai: event listener failed`) and the run carries on.

### Reporting Failures to the Model

A failure the model can recover from belongs in the result. `SwitchCurrency` above answers an unsupported currency with an `error` key, and the model offers the user another currency. A search tool works the same way: dates the airline refuses, or an airport it can't find, come back as a JSON payload explaining the problem, and only an error the model can do nothing about (the database, say) is returned as an error.

## Tool Choice

An `ai.ToolChoice` constrains which tool the model may call on a step. Its `Mode` is one of:

| Constant | Value | Description |
|----------|-------|-------------|
| `ai.ToolChoiceAuto` | `auto` | The model decides (the default whenever tools are offered). |
| `ai.ToolChoiceNone` | `none` | The model may not call a tool. |
| `ai.ToolChoiceRequired` | `required` | The model must call some tool. |
| `ai.ToolChoiceTool` | `tool` | The model must call the tool named by `ToolName`. |

A [middleware](ai-agents.md#middleware) sets the choice for one step with `WithToolChoice`. This one makes the first step call `DestinationBriefing`:

```go
// ForceBriefing makes the first step call DestinationBriefing.
type ForceBriefing struct{}

func (ForceBriefing) Handle(ctx context.Context, step ai.PendingStep, next ai.StepHandler) (*ai.StepResponse, error) {
	if step.IsFirstStep() && slices.Contains(step.ToolNames(), "DestinationBriefing") {
		step = step.OnlyTools("DestinationBriefing").
			WithToolChoice(&ai.ToolChoice{Mode: ai.ToolChoiceTool, ToolName: "DestinationBriefing"})
	}

	return next(ctx, step)
}
```

`ToolChoiceTool` is sent as `{"type": "function", "function": {"name": "<ToolName>"}}`; the other modes are sent as their string.

## Repairing Tool Calls

A model sometimes calls a tool the step didn't offer: one it invented, or one a middleware withdrew. By default that fails the run with an `*ai.NoSuchToolError`, whose `Name` is the tool the model asked for (`Model tried to call unavailable tool 'BookFlight'.`), before any of that step's calls run.

An agent that implements `ai.RepairsToolCalls` and returns `true` answers such a call instead, with a failed result the model can recover from:

```go
func (a *Concierge) RepairToolCalls() bool { return true }
```

The result reads `Tool 'BookFlight' does not exist. Available tools: SearchFlights, SwitchCurrency.` (or `Available tools: none.`), and the run's derived [step budget](ai-agents.md#step-limits) grows by one so the model still has a step to answer in.

## Limiting Tool Calls

A step middleware only sees a step before the provider answers it, so the calls a model makes side by side in one answer are out of its reach. To bound those, a middleware also implements `ai.ToolCallLimiter`:

```go
type ToolCallLimiter interface {
	// LimitToolCalls returns, per call, why it must not run (sent to the
	// model as its failed result), or "" to run it.
	LimitToolCalls(step PendingStep, calls []ToolCall) []string
}
```

The loop asks every limiter among the agent's middleware about the calls about to run (calls to tools the step offers, on any step but the final one). A refused call never runs: it is answered with your refusal as a failed result, so the model knows. When several limiters refuse the same call, the first in the middleware list wins. A verdict list shorter than the calls lets the remaining calls run.

```go
// LimitToolCalls implements ai.ToolCallLimiter.
func (OneSearchPerTurn) LimitToolCalls(_ ai.PendingStep, calls []ai.ToolCall) []string {
	refusals := make([]string, len(calls))
	searched := false

	for i, call := range calls {
		if call.Name != "SearchFlights" {
			continue
		}
		if searched {
			refusals[i] = "This call was not run: SearchFlights runs once per response. Use the result you have."
		}
		searched = true
	}

	return refusals
}
```

The type must also be a `StepMiddleware` the agent lists in `Middleware`. A fuller guard might narrow each step's tools from what the turn already ran, and limit one answer to three calls with each side-effecting tool at most once.

## Tool Calls and Results

An `ai.ToolCall` is one call the model asked for:

| Field | Description |
|-------|-------------|
| `ID` | The call's id. |
| `Name` | The tool's name. |
| `Arguments` | The arguments, in the model's key order (empty for `{}`), so they re-encode to the bytes stored rows and resent history already hold. |
| `ResultID` | The id the provider pairs the call and its result by, when it sent one. |

An `ai.ToolResult` is a call's outcome:

| Field / Method | Description |
|----------------|-------------|
| `ID`, `Name`, `Arguments`, `ResultID` | Copied from the call. |
| `Result` | What the tool returned: a `string` for every executed call, or any decoded JSON value when rebuilt from a stored row. |
| `Denied` | The call was not approved before the conversation continued. |
| `Failed` | The call did not produce its own result: it was refused, repaired or cut off by the step budget. |
| `Successful()` | The call ran and produced its own result: neither denied nor failed. |
| `ErrorMessage()` | Why an unsuccessful call failed (its `Result`, when that is a string), `nil` otherwise. |

When a result goes back to the model, a string is sent as it is and `nil` as `""`. Any other value (a result rebuilt from a stored row) is encoded as JSON with slashes and non-ASCII characters left unescaped, so the model reads it as written.

## Tool Events

Every executed call dispatches [events](ai.md#events) carrying the tool, its `Request` and the call's `ToolInvocationID`:

| Event | When |
|-------|------|
| `ai.InvokingTool` | Before `Handle` runs. |
| `ai.ToolInvoked` | After `Handle` returned a result (`Result`, and `Time`, the handler's duration in milliseconds). |
| `ai.ToolFailed` | After `Handle` returned an error (`Err`, `Time`). |

Calls that never run (refused, repaired or cut off by the step budget) dispatch no tool events. A tool that panics dispatches `InvokingTool` and neither of the others: the run's `AgentFailed` carries the panic.

## Testing Tools

Since a tool is a plain value, you may test it by calling `Handle` with `ai.RequestFromJSON`:

```go
func TestSwitchCurrencyRefusesAnUnsupportedCurrency(t *testing.T) {
	tool := &tools.SwitchCurrency{Supported: []string{"EUR", "GBP"}}

	result, err := tool.Handle(t.Context(), ai.RequestFromJSON(`{"currency":"xyz"}`))
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(result, "cannot be shown in XYZ") {
		t.Fatalf("result = %s", result)
	}
}
```

To test a tool inside a real run, script a step that calls it with `aitest.ToolCall` and assert on what reached the client or the database. See [Testing](ai.md#testing) and [mocking](mocking.md).
