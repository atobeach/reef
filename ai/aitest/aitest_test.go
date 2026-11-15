package aitest_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/atobeach/reef/ai/aitest"
	"github.com/atobeach/reef/httpclient"
)

// StreamChatCompletion and ChatCompletion stand in for reef/ai's requests:
// the mock answers by request name.
type StreamChatCompletion struct{ Prompt string }

func (StreamChatCompletion) Method() string   { return "POST" }
func (StreamChatCompletion) Endpoint() string { return "chat/completions" }
func (r StreamChatCompletion) Body() any      { return map[string]any{"prompt": r.Prompt, "stream": true} }

type ChatCompletion struct{ Prompt string }

func (ChatCompletion) Method() string   { return "POST" }
func (ChatCompletion) Endpoint() string { return "chat/completions" }
func (r ChatCompletion) Body() any      { return map[string]any{"prompt": r.Prompt} }

type unrelated struct{}

func (unrelated) Method() string   { return "GET" }
func (unrelated) Endpoint() string { return "health" }

// events decodes an SSE body into its data payloads.
func events(t *testing.T, body []byte) []string {
	t.Helper()

	var data []string
	for line := range strings.SplitSeq(strings.TrimSpace(string(body)), "\n\n") {
		payload, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			t.Fatalf("not an SSE data line: %q", line)
		}
		data = append(data, payload)
	}

	return data
}

func TestStreamsEndWithTheirFinishReasonUsageAndDone(t *testing.T) {
	cases := map[string]struct {
		chunks []aitest.Chunk
		finish string
	}{
		"text":      {aitest.Text("Hel", "lo"), "stop"},
		"tool call": {aitest.ToolCall("call_1", "Echo", `{"city":"Paris"}`), "tool_calls"},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			data := events(t, aitest.Stream(c.chunks).Body)
			if data[len(data)-1] != "[DONE]" {
				t.Fatalf("last event = %s, want [DONE]", data[len(data)-1])
			}

			var finish, usage map[string]any
			_ = json.Unmarshal([]byte(data[len(data)-3]), &finish)
			_ = json.Unmarshal([]byte(data[len(data)-2]), &usage)

			choice := finish["choices"].([]any)[0].(map[string]any)
			if choice["finish_reason"] != c.finish || usage["usage"] == nil {
				t.Fatalf("finish %v, usage %v", finish, usage)
			}
		})
	}
}

func TestAToolCallStreamsItsArgumentsInTwoHalvesThatRejoin(t *testing.T) {
	var arguments strings.Builder
	for _, chunk := range aitest.ToolCall("call_1", "Echo", `{"city":"Paris"}`)[:2] {
		delta := chunk["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)
		call := delta["tool_calls"].([]any)[0].(map[string]any)
		arguments.WriteString(call["function"].(map[string]any)["arguments"].(string))
	}

	if arguments.String() != `{"city":"Paris"}` {
		t.Fatalf("arguments = %s", arguments.String())
	}
}

func TestScriptsAnswerInOrderAndTheirBodiesAreRecorded(t *testing.T) {
	mock := httpclient.NewMock(t, map[string]any{"unrelated": httpclient.JSON(200, map[string]any{})})
	aitest.Script(mock, aitest.Stream(aitest.Text("one")), aitest.Stream(aitest.Text("two")))
	aitest.ScriptCompletions(mock, aitest.Completion(`{"answer":42}`))
	client := httpclient.New(httpclient.Connector{BaseURL: "http://model.test"}).WithMock(mock)

	var streamed []string
	for _, req := range []httpclient.Request{StreamChatCompletion{"a"}, unrelated{}, StreamChatCompletion{"b"}, StreamChatCompletion{"c"}, ChatCompletion{"d"}} {
		res, err := client.Send(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}

		if _, ok := req.(StreamChatCompletion); ok {
			streamed = append(streamed, string(res.Body()))
		}
	}

	// The last scripted step repeats.
	if !strings.Contains(streamed[0], `"content":"one"`) || !strings.Contains(streamed[1], `"content":"two"`) || streamed[2] != streamed[1] {
		t.Fatalf("streamed = %v", streamed)
	}

	var prompts []any
	for _, body := range aitest.SentBodies(mock) {
		prompts = append(prompts, body["prompt"])
	}

	if raw := aitest.SentRawBodies(mock); len(prompts) != 4 || prompts[0] != "a" || prompts[3] != "d" || len(raw) != 4 || raw[3] != `{"prompt":"d"}` {
		t.Fatalf("prompts = %v, raw = %v", prompts, raw)
	}
}
