# Streaming

- [Introduction](#introduction)
- [Streaming Responses](#streaming-responses)
    - [Iterating Events](#iterating-events)
    - [Then and Catch Callbacks](#then-and-catch-callbacks)
    - [Run Deadlines](#run-deadlines)
- [Streaming to the Browser](#streaming-to-the-browser)
    - [Writing the Response](#writing-the-response)
    - [Frames](#frames)
    - [Errors Mid-Stream](#errors-mid-stream)
- [Stream Events](#stream-events)
    - [Available Events](#available-events)
    - [Combining Events](#combining-events)
- [Stream Errors](#stream-errors)
- [Streaming Events You Already Have](#streaming-events-you-already-have)

## Introduction

A streamed run hands you each piece of the model's answer as it arrives: text deltas, reasoning, tool calls and their results. reef emits these as typed `ai.StreamEvent` values, and the `ai.AgentUserInteractionProtocol` turns them into [AG-UI](https://docs.ag-ui.com/concepts/events) server-sent events for the browser. A chat interface typically streams every turn this way. This page assumes you have read the [AI overview](ai.md).

## Streaming Responses

To stream an agent, call the manager's `Stream` method. It returns an `*ai.StreamableAgentResponse`:

```go
streamable, err := manager.Stream(ctx, agents.TravelWriter{}, "Write a blurb for Porto.")
if err != nil {
	return err
}
```

`Stream` resolves the agent's provider and model and loads its history and tools straight away, so a misconfigured agent fails here. That happens before the run starts, so a panic in the agent's `Messages` or `Tools` reaches your code as a panic (in a controller, the kernel's [recovery](errors.md#panics)) rather than as the run's error. Nothing is sent to the provider until you iterate the response. Agents with [structured output](ai-agents.md#structured-output) cannot be streamed.

A `StreamableAgentResponse` carries:

| Field | Description |
|-------|-------------|
| `InvocationID` | The run's id. |
| `ConversationID` | The conversation a [remembered](ai-agents.md#remembering-conversations) agent continues, known before you iterate. |
| `UserMessageID`, `AssistantMessageID` | The stored message ids, set once the run completed. |
| `Deadline` | The run's overall [deadline](#run-deadlines); zero is unbounded. |

### Iterating Events

`Each` runs the stream, handing every event to your function as it arrives:

```go
err := streamable.Each(ctx, func(event ai.StreamEvent) {
	switch e := event.(type) {
	case ai.TextDelta:
		fmt.Print(e.Delta)
	case ai.ToolCallEvent:
		fmt.Println("calling", e.ToolCall.Name)
	case ai.StreamEnd:
		fmt.Println(e.Usage.TotalTokens())
	}
})
```

`Each` returns once the run has finished: `nil` after a completed run, or the run's error. Once `Each` has returned `nil`, `Response` returns the run as an `*ai.AgentResponse` (`nil` until then), with every event on its `Events` field. `HasYielded` reports whether any event reached your function, so you can tell a run that failed before anything was shown from one that failed partway through.

Your function runs inside the run, so a panic in it fails the run like a panic in a tool (see [Returning Results](ai-tools.md#returning-results)): `Each` returns `ai: run panicked: <value>` and the `Catch` callbacks run.

A streamable response runs once, so iterate it once.

### Then and Catch Callbacks

`Then` registers a callback for the completed response, and `Catch` one for a failed run. Both return the response, so you may chain them:

```go
streamable.
	Then(func(ctx context.Context, response *ai.AgentResponse) error {
		// response.Text, response.Events, response.Usage...
		return nil
	}).
	Catch(func(ctx context.Context, err error) {
		// ...
	})
```

`Then` callbacks run in the order they were registered, after the last event and before `Each` returns. The manager registers its own first: storing a remembered turn, then dispatching `AgentStreamed`. A `Then` callback that returns an error, or panics, stops the rest and becomes `Each`'s error (a panic as `ai: run panicked: <value>`, logged with its stack); the `Catch` callbacks don't run for it, and `Response` stays `nil`. `Catch` callbacks run when the run itself fails, before `Each` returns that error; one that panics is logged with its stack, and the rest still run.

Both receive the context you passed to `Each`, not the deadline-bound one the run used, so a callback can still write to the database after the deadline has passed.

### Run Deadlines

The agent's [timeout](ai-agents.md#timeouts) bounds each provider request on its own. A model that keeps calling tools step after step is bounded only by its step budget, so you may also bound the whole run:

```go
streamable.Deadline = 3 * time.Minute
```

The deadline covers every step and tool call. Past it, the loop starts no further step, and the run fails with an `*ai.TurnDeadlineError` (`The run exceeded its 3m0s deadline.`), whatever the cut-off step itself reported. A run that panicked keeps its panic as the error, deadline or not, since a panic is a bug whenever it happens. The deadline's error unwraps to `context.DeadlineExceeded`, so a request canceled by the deadline is never mistaken for a provider outage:

```go
if errors.Is(err, context.DeadlineExceeded) {
	// The turn ran out of time.
}
```

Its `Deadline` field holds the deadline that passed.

> [!NOTE]
> The controller below runs each chat turn on `context.WithoutCancel(c.Context())`, so a browser refresh mid-turn still finishes and stores the turn. With the client's cancellation gone, `Deadline` is what bounds the turn.

## Streaming to the Browser

### Writing the Response

`ai.NewAgentUserInteractionProtocol` builds the AG-UI protocol for one run. Its `Parts` method iterates the response and hands you each AG-UI part; `EncodeSSE` frames a part as a server-sent event. Write the frames from a [streamed response](responses.md). `Concierge` is the example agent from [Agents](ai-agents.md#defining-agents):

```go
package controllers

import (
	"context"
	"time"

	"github.com/atobeach/reef/ai"
	"github.com/atobeach/reef/web"
	"example.com/flights/app/ai/agents"
)

// ConciergeController streams one concierge turn as AG-UI server-sent events.
type ConciergeController struct {
	AI *ai.Manager
}

// Invoke handles POST /api/concierge.
func (ctl *ConciergeController) Invoke(c *web.Context) (*web.Response, error) {
	// The turn finishes (and is stored) even if the browser goes away.
	ctx := context.WithoutCancel(c.Context())

	agent := (&agents.Concierge{Timezone: "Europe/London"}).Continue(c.String("conversation_id"))

	streamable, err := ctl.AI.Stream(ctx, agent, c.String("message"))
	if err != nil {
		return nil, err
	}
	streamable.Deadline = 3 * time.Minute

	protocol := ai.NewAgentUserInteractionProtocol("", "", nil)

	res := web.Stream("text/event-stream", func(s *web.StreamWriter) error {
		emit := func(part ai.Part) {
			// A write to a client that left fails; the turn carries on.
			if _, err := s.Write([]byte(protocol.EncodeSSE(part))); err == nil {
				s.Flush()
			}
		}

		if err := protocol.Parts(ctx, streamable, emit); err != nil && !protocol.Errored() {
			protocol.YieldPart(ai.NewPart(
				"type", "RUN_ERROR",
				"message", "The response stream was interrupted. Please try again.",
			), emit)
		}

		return nil
	})

	for name, value := range ai.ProtocolHeaders() {
		res.WithHeader(name, value)
	}

	return res, nil
}
```

The turn outlives the handler, so it runs on `c.Context()`, the request's context as bound so far, rather than on `c` (see [The Request Is a Context](requests.md#the-request-is-a-context)), with the client's cancellation stripped by `context.WithoutCancel`.

`NewAgentUserInteractionProtocol(threadID, runID, encode)` sets the protocol's fields:

| Field | Description |
|-------|-------------|
| `ThreadID` | The AG-UI thread. When empty, it becomes the response's conversation id when the run begins, or a new UUIDv7 if there is none. |
| `RunID` | The AG-UI run. When empty, it becomes the run's invocation id. |
| `Encode` | The [encoder](ai.md#json-encoding) for each frame, and for the tool arguments and results carried inside one as strings (`nil`: `encoding/json`, which replaces invalid UTF-8 rather than failing). |

`ai.ProtocolHeaders` returns the stream's response headers: `Content-Type: text/event-stream; charset=utf-8` and `Cache-Control: no-cache, no-transform, private`. `web.Stream` already sets `X-Accel-Buffering: no`, so proxies pass each frame through as it is flushed.

The protocol's methods are:

| Method | Description |
|--------|-------------|
| `Parts(ctx, response, yield)` | Iterate the response, handing every part to `yield`. An error from the run is returned after the parts that preceded it. |
| `YieldPart(part, yield)` | Yield a part of your own, preceded by the run's opening parts if nothing has been sent yet. |
| `EncodeSSE(part)` | The part as `data: <json>\n\n`. A part the encoder refuses is framed as `data: false` rather than corrupting the stream. |
| `Errored()` | Whether an error part already reached the client. |

An `ai.Part` is an `*ordered.Object`, so its keys stay in wire order. `ai.NewPart` builds one from alternating key, value pairs; keys must be strings:

```go
part := ai.NewPart("type", "CUSTOM", "name", "fare_changed", "value", ai.NewPart("flightId", id))
```

reef writes no end-of-stream marker. If your client waits for one, such as `data: [DONE]`, write it yourself after the last part.

### Frames

A turn that calls one tool and then answers produces these parts:

```text
RUN_STARTED
STEP_STARTED          stepName "1"
TOOL_CALL_START
TOOL_CALL_ARGS
TOOL_CALL_END
TOOL_CALL_RESULT
STEP_FINISHED         stepName "1"
STEP_STARTED          stepName "2"
TEXT_MESSAGE_START
TEXT_MESSAGE_CONTENT  (one per text delta)
TEXT_MESSAGE_END
STEP_FINISHED         stepName "2"
RUN_FINISHED
```

Each stream event maps to parts as follows. The frontend parses these frames, so their keys, order and values are fixed:

| Event | Parts (keys in order) |
|-------|-----------------------|
| The first `StreamStart` | `RUN_STARTED` (`threadId`, `runId`), then `STEP_STARTED` (`stepName`) |
| Each later `StreamStart` | `STEP_FINISHED`, then `STEP_STARTED` for the next step |
| `TextStart` | `TEXT_MESSAGE_START` (`messageId`, `role: "assistant"`) |
| `TextDelta` | `TEXT_MESSAGE_CONTENT` (`messageId`, `delta`) |
| `TextEnd` | `TEXT_MESSAGE_END` (`messageId`) |
| `ReasoningStart` | `REASONING_START` (`messageId`), then `REASONING_MESSAGE_START` (`messageId`, `role: "reasoning"`) |
| `ReasoningDelta` | `REASONING_MESSAGE_CONTENT` (`messageId`, `delta`) |
| `ReasoningEnd` | `REASONING_MESSAGE_END`, then `REASONING_END` (`messageId`) |
| `ToolCallEvent` | `TOOL_CALL_START` (`toolCallId`, `toolCallName`), `TOOL_CALL_ARGS` (`toolCallId`, `delta`: the arguments as a JSON string, `{}` when empty), `TOOL_CALL_END` (`toolCallId`) |
| `ToolResultEvent` | `TOOL_CALL_RESULT` (`messageId`, `toolCallId`, `content`, `role: "tool"`, and `metadata` when unsuccessful) |
| A preliminary `ToolResultEvent` | `ACTIVITY_SNAPSHOT` (`messageId`, `activityType: "TOOL_OUTPUT"`, `content`: `toolName`, `output`) |
| `CitationEvent` | `CUSTOM` (`name: "citation"`, `value`: `url`, and `title` when known) |
| `ErrorEvent` | `RUN_ERROR` (`message`, `code`) |
| `StreamEnd` | None; its usage and finish reason go on `RUN_FINISHED`. |

A `TOOL_CALL_RESULT`'s `messageId` is the result's provider id when it has one, else the event's id. Its `content` is the tool's result (encoded as JSON when it isn't a string); for an unsuccessful call it is the error message, or `The tool call failed.`, and `metadata` repeats it as `error`, adding `denied: true` for a denied call.

When the run completes, the protocol closes the last step and sends `RUN_FINISHED`: `threadId`, `runId`, `messageId` and `userMessageId` (the stored message ids of a remembered turn), `usage` (a one-item list with `provider`, `model`, `inputTokens`, `outputTokens`, `totalTokens`, and `reasoningTokens` and `cachedInputTokens` when reported), and `metadata` with the run's `finishReason`. It is sent after the `Then` callbacks ran, which is why the stored ids are already known.

### Errors Mid-Stream

When the provider reports an error inside the stream, the protocol sends `RUN_ERROR` with the provider's message and code, ignores any later events, and `Errored` turns `true`. `Parts` then returns the run's `*ai.StreamError`. The client has already been told, so don't send a second error.

Any other failure (a provider that refused the request, a tool that returned an error or panicked, the run's deadline) sends nothing: `Parts` returns the error and the client hears nothing more. Answer it with a `RUN_ERROR` of your own through `YieldPart`, as the controller above does, which also opens the run first if the failure came before any part. You may word an outage differently from a broken reply:

```go
if ai.IsFailoverable(err) {
	p.YieldPart(ai.NewPart(
		"type", "RUN_ERROR",
		"message", "The assistant is temporarily unavailable. Please try again in a few minutes.",
		"code", "provider_unavailable",
	), yield)

	return
}

p.YieldPart(ai.NewPart("type", "RUN_ERROR", "message", "The response stream was interrupted. Please try again."), yield)
```

## Stream Events

### Available Events

`ai.StreamEvent` is one of the types below. Every event carries its own `ID` (a UUIDv7), the run's `InvocationID`, and a `Timestamp` in Unix seconds from the context's clock.

| Event | Fields | Description |
|-------|--------|-------------|
| `ai.StreamStart` | `Provider`, `Model` | A provider step began. |
| `ai.ReasoningStart` | `ReasoningID` | A block of visible reasoning opened. |
| `ai.ReasoningDelta` | `ReasoningID`, `Delta` | A chunk of reasoning. |
| `ai.ReasoningEnd` | `ReasoningID` | The reasoning block closed. |
| `ai.TextStart` | `MessageID` | The step's text began. |
| `ai.TextDelta` | `MessageID`, `Delta` | A chunk of text. |
| `ai.TextEnd` | `MessageID` | The step's text ended. |
| `ai.CitationEvent` | `MessageID`, `Citation` | A URL citation attached to the text. |
| `ai.ToolCallEvent` | `ToolCall` | A completed tool call the model made. |
| `ai.ToolResultEvent` | `ToolResult`, `Successful`, `Error`, `Denied`, `Preliminary` | A tool call's outcome. |
| `ai.ErrorEvent` | `Type`, `Message`, `Recoverable` | A provider error reported inside the stream. |
| `ai.StreamEnd` | `Reason`, `Usage`, `Steps` | The run ended. |

Within one step, the events arrive in this order: `StreamStart`; any reasoning block (closed as soon as text or tool calls begin); the text with its citations; then one `ToolCallEvent` per call, assembled from the provider's fragments in the order the calls began, and only when the step finished with `tool_calls`. Once the loop has run the calls, a `ToolResultEvent` follows for each. The run closes with a single `StreamEnd`, whose `Reason` is the final step's finish reason, `Usage` the run's summed usage, and `Steps` every completed step.

`ToolResultEvent.Error` is the unsuccessful call's error message (`nil` on success). `Preliminary` marks a partial result shown while a tool is still working; reef's own loop never emits one, but the protocol renders it. The OpenRouter gateway never sets `ErrorEvent.Recoverable`.

You can't define event types of your own: the interface is closed to these twelve.

### Combining Events

Three functions summarize a list of events, such as `response.Events`:

| Function | Returns |
|----------|---------|
| `ai.CombineText(events)` | Every `TextDelta`, concatenated. |
| `ai.CombineReasoning(events)` | Every `ReasoningDelta`, concatenated. |
| `ai.CombineUsage(events)` | The usage of every `StreamEnd`, summed. |

```go
text := ai.CombineText(response.Events)
```

A completed stream's `AgentResponse` is built the same way: its `Text`, `Reasoning` and `Usage` come from these functions, its `ToolCalls` and `ToolResults` from the tool events (preliminary results left out), `Steps` from the `StreamEnd`, `Meta.Model` from the last `StreamStart`, and `Meta.Citations` from the `CitationEvent`s.

## Stream Errors

A provider may fail a step after the stream has begun, with an `error` chunk or a choice that finishes with `error`. The gateway emits an `ErrorEvent` for it and the run fails with an `*ai.StreamError`, whose `Event` is that `ErrorEvent`:

| Provider failure | `ErrorEvent.Type` | `ErrorEvent.Message` |
|------------------|-------------------|----------------------|
| An `error` chunk | Its `code`, else `unknown_error` | Its `message`, else `Unknown error` |
| A choice finishing with `error` | Its error's `code`, else `provider_error` | Its error's `message`, else `An upstream provider error occurred.` |

A `StreamError`'s message is the event's message; without an event it reads `The provider ended the stream without completing the step.` `ai.IsStreamError` reports whether an error is (or wraps) one. Since the client already saw the error as a `RUN_ERROR`, you may report every other failure to error tracking but not these:

```go
if err := p.Parts(ctx, response, emit); err != nil {
	if !ai.IsStreamError(err) {
		report(ctx, err)
	}
}
```

Streamed steps are requested with `"stream": true` and `"stream_options": {"include_usage": true}`. The stream ends at `data: [DONE]` or the end of the body; lines that aren't JSON, such as keep-alive comments and OpenRouter's processing notices, are skipped.

## Streaming Events You Already Have

`ai.NewStreamableAgentResponse` wraps any event generator in a streamable response, so the AG-UI protocol (or anything else that consumes one) can stream events that did not come from a live run. `ai.StreamEvents(err, events...)` is a generator over a fixed list of events, followed by `err`:

```go
streamable := ai.NewStreamableAgentResponse("run-1", ai.Meta{}, ai.StreamEvents(nil,
	ai.StreamStart{ID: "e1", InvocationID: "run-1", Provider: "openrouter", Model: "openai/gpt-test"},
	ai.TextStart{ID: "e2", InvocationID: "run-1", MessageID: "m1"},
	ai.TextDelta{ID: "e3", InvocationID: "run-1", MessageID: "m1", Delta: "Hello"},
	ai.TextEnd{ID: "e4", InvocationID: "run-1", MessageID: "m1"},
	ai.StreamEnd{ID: "e5", InvocationID: "run-1", Reason: "stop"},
))

protocol := ai.NewAgentUserInteractionProtocol("thread-1", "", nil)

var frames []string
err := protocol.Parts(ctx, streamable, func(part ai.Part) {
	frames = append(frames, protocol.EncodeSSE(part))
})
```

A generator is a `func(ctx context.Context, emit func(ai.StreamEvent)) error`: call `emit` for each event and return the run's error, if any. The `ai.Meta` you pass is the response's starting metadata.
