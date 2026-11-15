package ai_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/atobeach/reef/ai"
	"github.com/atobeach/reef/ai/aitest"
	"github.com/atobeach/reef/httpclient"
	"github.com/atobeach/reef/jsonschema"
	"github.com/atobeach/reef/phpjson"
)

// bareTool is a tool that takes no parameters.
type bareTool struct{}

func (bareTool) Name() string                                       { return "Now" }
func (bareTool) Description() string                                { return "The time." }
func (bareTool) Schema() jsonschema.Properties                      { return nil }
func (bareTool) Handle(context.Context, ai.Request) (string, error) { return "noon", nil }

// The gateway writes bodies with encoding/json unless given an encoder; an
// encoder writes the body and the JSON nested inside it as strings (the
// resent tool call's arguments), while a parameterless tool still lists
// `properties` as an object even under an encoder that writes an empty
// object as a list.
func TestTheBodyEncoderWritesTheBodyAndTheJSONInsideIt(t *testing.T) {
	cases := []struct {
		name   string
		encode ai.Encoder
		want   []string
	}{
		{"encoding/json", nil, []string{
			`"model":"openai/gpt-test"`,
			`"arguments":"{\"city\":\"Paris/Nord\"}"`,
			`"parameters":{"type":"object","properties":{},"required":[],"additionalProperties":false}`,
		}},
		{"phpjson", phpjson.Encode, []string{
			`"model":"openai\/gpt-test"`,
			`"arguments":"{\"city\":\"Paris\\\/Nord\"}"`,
			`"parameters":{"type":"object","properties":{},"required":[],"additionalProperties":false}`,
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mock := httpclient.NewMock(t, map[string]any{})
			aitest.Script(mock,
				aitest.Stream(aitest.ToolCall("call_1", "Echo", `{"city":"Paris/Nord"}`)),
				aitest.Stream(aitest.Text("Done.")),
			)

			m := managerOn(ai.OpenRouterGateway{Encode: c.encode, HTTP: func(conn httpclient.Connector) *httpclient.Client {
				return httpclient.New(conn).WithMock(mock)
			}})

			calls := 0
			if _, _, err := collect(t, m, testAgent{tools: []ai.Tool{echoTool{&calls}, bareTool{}}}, "Go"); err != nil {
				t.Fatal(err)
			}

			resent := aitest.SentRawBodies(mock)[1]
			for _, want := range c.want {
				if !strings.Contains(resent, want) {
					t.Errorf("body lacks %s:\n%s", want, resent)
				}
			}
		})
	}
}

// A non-streamed answer that is not a completion fails the step with
// OpenRouter's generic message, the decode failure kept as its cause.
func TestAnUndecodableCompletionKeepsItsCause(t *testing.T) {
	cases := map[string]httpclient.MockResponse{
		"not JSON":        {Status: 200, Body: []byte(`<html>`)},
		"empty object":    httpclient.JSON(200, map[string]any{}),
		"mistyped fields": httpclient.JSON(200, map[string]any{"choices": "none"}),
	}

	for name, reply := range cases {
		t.Run(name, func(t *testing.T) {
			mock := httpclient.NewMock(t, map[string]any{})
			aitest.ScriptCompletions(mock, reply)

			_, err := manager(t, mock).Prompt(context.Background(), structuredAgent{}, "Paris")

			var responseErr *ai.ResponseError
			if !errors.As(err, &responseErr) || responseErr.Message != "OpenRouter Error: [unknown] Unknown OpenRouter error." {
				t.Fatalf("err = %v", err)
			}
			if responseErr.Err == nil || !strings.Contains(responseErr.Err.Error(), "decoding the completion") {
				t.Fatalf("cause = %v", responseErr.Err)
			}
		})
	}
}

// OpenRouter's error object reads its kind from `type`, else `code` (a
// number or a string), with defaults for what is missing.
func TestACompletionErrorNamesItsKindAndMessage(t *testing.T) {
	cases := []struct {
		name  string
		error map[string]any
		want  string
	}{
		{"numeric code", map[string]any{"code": 429, "message": "Slow down"}, "OpenRouter Error: [429] Slow down"},
		{"type over code", map[string]any{"type": "invalid_request", "code": 400, "message": "Bad"}, "OpenRouter Error: [invalid_request] Bad"},
		{"nothing", map[string]any{}, "OpenRouter Error: [unknown] Unknown OpenRouter error."},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mock := httpclient.NewMock(t, map[string]any{})
			aitest.ScriptCompletions(mock, httpclient.JSON(200, map[string]any{"error": c.error}))

			_, err := manager(t, mock).Prompt(context.Background(), structuredAgent{}, "Paris")
			if err == nil || err.Error() != c.want {
				t.Fatalf("err = %v, want %s", err, c.want)
			}
		})
	}
}

// A completion's tool calls, usage and reasoning details are read off the
// typed envelope; arguments sent as an object rather than a string still
// decode.
func TestACompletionReadsToolCallsUsageAndReasoning(t *testing.T) {
	mock := httpclient.NewMock(t, map[string]any{})
	aitest.ScriptCompletions(mock, httpclient.JSON(200, map[string]any{
		"model": "openai/gpt-test",
		"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{
			"content":           `{"facts":["a"]}`,
			"reasoning_details": []any{map[string]any{"summary": "Thought briefly."}},
		}}},
		"usage": map[string]any{
			"prompt_tokens": 100, "completion_tokens": 20,
			"prompt_tokens_details":     map[string]any{"cached_tokens": 40},
			"completion_tokens_details": map[string]any{"reasoning_tokens": 7},
		},
	}))

	response, err := manager(t, mock).Prompt(context.Background(), structuredAgent{}, "Paris")
	if err != nil {
		t.Fatal(err)
	}

	usage := response.Usage
	if usage.InputTokens != 100 || usage.OutputTokens != 20 || *usage.CacheRead != 40 || usage.CacheWrite != nil || *usage.Reasoning != 7 {
		t.Fatalf("usage = %+v", usage)
	}
	if response.Reasoning != "Thought briefly." {
		t.Fatalf("reasoning = %q", response.Reasoning)
	}
}

// The request's readers are lenient the way a model's JSON needs: absent
// and null read as zero, numeric strings as numbers, numbers as their JSON
// spelling.
func TestRequestReadersAreLenient(t *testing.T) {
	req := ai.RequestFromJSON(`{"n":"12","f":"1.5","whole":3,"frac":7.9,"flag":true,"list":["a"],"object":{"k":"v"},"none":null,"blank":"  ","word":"12abc"}`)

	texts := map[string]string{"n": "12", "whole": "3", "frac": "7.9", "flag": "true", "list": "", "object": "", "none": "", "missing": ""}
	for key, want := range texts {
		if got := req.String(key); got != want {
			t.Errorf("String(%s) = %q, want %q", key, got, want)
		}
	}

	ints := map[string]int{"n": 12, "f": 1, "whole": 3, "frac": 7, "word": 0, "flag": 0, "missing": 0}
	for key, want := range ints {
		if got := req.Integer(key); got != want {
			t.Errorf("Integer(%s) = %d, want %d", key, got, want)
		}
	}

	if req.Float("f") != 1.5 || req.Float("frac") != 7.9 || req.Float("word") != 0 {
		t.Error("Float reads numbers and numeric strings")
	}

	// Only decimal numbers read: what strconv.ParseFloat would also accept
	// (infinities, NaN, hex, underscores) and what overflows read as 0.
	odd := ai.RequestFromJSON(`{"inf":"Infinity","short":"inf","nan":"NaN","hex":"0x1p4","under":"1_0","huge":"1e400","overflow":1e400,"padded":" 2.5 "}`)
	for key, want := range map[string]float64{"inf": 0, "short": 0, "nan": 0, "hex": 0, "under": 0, "huge": 0, "overflow": 0, "padded": 2.5} {
		if got := odd.Float(key); got != want {
			t.Errorf("Float(%s) = %v, want %v", key, got, want)
		}
		if got := odd.Integer(key); got != int(want) {
			t.Errorf("Integer(%s) = %v, want %v", key, got, int(want))
		}
	}

	filled := map[string]bool{"n": true, "flag": true, "list": true, "none": false, "blank": false, "missing": false}
	for key, want := range filled {
		if got := req.Filled(key); got != want {
			t.Errorf("Filled(%s) = %v, want %v", key, got, want)
		}
	}

	if !req.Has("none") || req.Has("missing") {
		t.Error("Has is presence, null included")
	}

	if values := req.Array("object"); len(values) != 1 || values[0] != "v" {
		t.Errorf("Array(object) = %v", values)
	}
	if values := req.Array("whole"); len(values) != 1 || values[0] != json.Number("3") {
		t.Errorf("Array(scalar) = %v", values)
	}
}
