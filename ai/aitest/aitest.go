// Package aitest scripts a fake OpenAI-compatible provider for tests: the
// exact Chat Completions bodies (streamed SSE chunks or plain JSON) a model
// would send, answered from the TestCase's MockClient, so a run exercises
// the real gateway, loop and protocol without a live model call.
//
//	aitest.Script(mock,
//		aitest.Stream(aitest.ToolCall("call_1", "DestinationInfo", `{"location":"Japan"}`)),
//		aitest.Stream(aitest.Text("Japan uses the yen.")),
//	)
package aitest

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/atobeach/reef/httpclient"
)

// Model is the model id the scripted chunks report.
const Model = "openai/gpt-test"

// Chunk is one Chat Completions stream chunk (encoded as JSON).
type Chunk = map[string]any

// Text is a step that streams text, split into the given deltas, then stops.
func Text(deltas ...string) []Chunk {
	chunks := []Chunk{}
	for _, delta := range deltas {
		chunks = append(chunks, delta1(map[string]any{"role": "assistant", "content": delta}, nil))
	}

	return append(chunks, finish("stop"), usageChunk(10, 5))
}

// ToolCall is a step that calls one tool, its arguments streamed in two
// halves as providers do, then finishes with tool_calls.
func ToolCall(id string, name string, arguments string) []Chunk {
	return ToolCalls(Call{ID: id, Name: name, Arguments: arguments})
}

// Call is one scripted tool call.
type Call struct {
	ID        string
	Name      string
	Arguments string
}

// ToolCalls is a step making several tool calls side by side.
func ToolCalls(calls ...Call) []Chunk {
	chunks := []Chunk{}
	for index, call := range calls {
		half := len(call.Arguments) / 2
		chunks = append(chunks,
			delta1(map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{
				"index": index, "id": call.ID, "type": "function",
				"function": map[string]any{"name": call.Name, "arguments": call.Arguments[:half]},
			}}}, nil),
			delta1(map[string]any{"tool_calls": []any{map[string]any{
				"index": index, "function": map[string]any{"arguments": call.Arguments[half:]},
			}}}, nil),
		)
	}

	return append(chunks, finish("tool_calls"), usageChunk(20, 8))
}

// ErrorChunk is a provider error reported inside the stream.
func ErrorChunk(code any, message string) []Chunk {
	return []Chunk{{"error": map[string]any{"code": code, "message": message}}}
}

func delta1(delta map[string]any, finishReason any) Chunk {
	return Chunk{
		"id": "gen-1", "object": "chat.completion.chunk", "created": 1760000000, "model": Model,
		"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finishReason}},
	}
}

func finish(reason string) Chunk {
	return delta1(map[string]any{"role": "assistant", "content": ""}, reason)
}

func usageChunk(prompt int, completion int) Chunk {
	return Chunk{
		"id": "gen-1", "object": "chat.completion.chunk", "created": 1760000000, "model": Model,
		"choices": []any{},
		"usage":   map[string]any{"prompt_tokens": prompt, "completion_tokens": completion, "total_tokens": prompt + completion},
	}
}

// SSE renders chunks as the provider's server-sent-event body.
func SSE(chunks []Chunk) []byte {
	var body strings.Builder
	for _, chunk := range chunks {
		encoded, err := json.Marshal(chunk)
		if err != nil {
			panic(err)
		}
		body.WriteString("data: ")
		body.Write(encoded)
		body.WriteString("\n\n")
	}
	body.WriteString("data: [DONE]\n\n")

	return []byte(body.String())
}

// Stream is a 200 SSE mock reply for one streamed step.
func Stream(chunks []Chunk) httpclient.MockResponse {
	return httpclient.MockResponse{
		Status: http.StatusOK,
		Body:   SSE(chunks),
		Header: http.Header{"Content-Type": {"text/event-stream"}},
	}
}

// Completion is a 200 JSON mock reply for one non-streamed step whose
// message content is text (for structured output, the JSON object).
func Completion(text string) httpclient.MockResponse {
	return httpclient.JSON(http.StatusOK, map[string]any{
		"id": "gen-1", "object": "chat.completion", "model": Model,
		"choices": []any{map[string]any{
			"index": 0, "finish_reason": "stop",
			"message": map[string]any{"role": "assistant", "content": text},
		}},
		"usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 50, "total_tokens": 150},
	})
}

// Status is a failed provider reply (402 credits, 503 overloaded, ...).
func Status(status int, message string) httpclient.MockResponse {
	return httpclient.JSON(status, map[string]any{"error": map[string]any{"code": status, "message": message}})
}

// Script answers the streamed steps in order (the last repeats).
func Script(mock *httpclient.MockClient, steps ...httpclient.MockResponse) {
	mock.Add("StreamChatCompletion", httpclient.Sequence(steps))
}

// ScriptCompletions answers the non-streamed steps in order.
func ScriptCompletions(mock *httpclient.MockClient, steps ...httpclient.MockResponse) {
	mock.Add("ChatCompletion", httpclient.Sequence(steps))
}

// SentBodies are the decoded request bodies of every chat completion sent.
func SentBodies(mock *httpclient.MockClient) []map[string]any {
	var bodies []map[string]any
	for _, sent := range mock.Sent() {
		name := httpclient.Name(sent.Request)
		if name != "StreamChatCompletion" && name != "ChatCompletion" {
			continue
		}

		var body map[string]any
		_ = json.Unmarshal(sent.Body, &body)
		bodies = append(bodies, body)
	}

	return bodies
}

// SentRawBodies are the raw request bodies of every chat completion sent.
func SentRawBodies(mock *httpclient.MockClient) []string {
	var bodies []string
	for _, sent := range mock.Sent() {
		name := httpclient.Name(sent.Request)
		if name == "StreamChatCompletion" || name == "ChatCompletion" {
			bodies = append(bodies, string(sent.Body))
		}
	}

	return bodies
}
