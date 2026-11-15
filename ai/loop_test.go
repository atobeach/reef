package ai_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/atobeach/reef/ai"
	"github.com/atobeach/reef/ai/aitest"
	"github.com/atobeach/reef/httpclient"
	"github.com/atobeach/reef/jsonschema"
	"github.com/atobeach/reef/ordered"
)

type echoTool struct{ calls *int }

func (echoTool) Name() string        { return "Echo" }
func (echoTool) Description() string { return "Echo the arguments back." }
func (echoTool) Schema() jsonschema.Properties {
	return jsonschema.Properties{
		{Name: "city", Type: jsonschema.String().Description("A city.").Required()},
	}
}

func (t echoTool) Handle(_ context.Context, req ai.Request) (string, error) {
	*t.calls++

	return `{"city":"` + req.String("city") + `"}`, nil
}

type testAgent struct {
	tools   []ai.Tool
	repairs bool
	steps   int
}

func (testAgent) Class() string                          { return `App\Ai\Agents\TestAgent` }
func (testAgent) Instructions(context.Context) string    { return "Be brief." }
func (testAgent) Provider() string                       { return "openrouter" }
func (testAgent) Model() string                          { return "openai/gpt-test" }
func (a testAgent) Tools(context.Context) []ai.Tool      { return a.tools }
func (a testAgent) RepairToolCalls() bool                { return a.repairs }
func (a testAgent) MaxSteps() int                        { return a.steps }
func (testAgent) ProviderOptions(string) *ordered.Object { return nil }

func manager(t *testing.T, mock *httpclient.MockClient) *ai.Manager {
	t.Helper()

	return managerOn(ai.OpenRouterGateway{HTTP: func(c httpclient.Connector) *httpclient.Client {
		return httpclient.New(c).WithMock(mock)
	}})
}

// managerOn is the test agent's runtime over gateway.
func managerOn(gateway ai.Gateway) *ai.Manager {
	return ai.NewManager(ai.ManagerConfig{
		Providers: map[string]ai.ProviderConfig{"openrouter": {Name: "openrouter", Driver: "openrouter", Key: "test-key"}},
		Default:   "openrouter",
		Gateway:   gateway,
	})
}

func collect(t *testing.T, m *ai.Manager, agent ai.Agent, prompt string) ([]ai.StreamEvent, *ai.StreamableAgentResponse, error) {
	t.Helper()

	streamable, err := m.Stream(context.Background(), agent, prompt)
	if err != nil {
		t.Fatal(err)
	}

	var events []ai.StreamEvent
	err = streamable.Each(context.Background(), func(e ai.StreamEvent) { events = append(events, e) })

	return events, streamable, err
}

func TestStreamsAToolLoopAndSendsTheResultBack(t *testing.T) {
	mock := httpclient.NewMock(t, map[string]any{})
	aitest.Script(mock,
		aitest.Stream(aitest.ToolCall("call_1", "Echo", `{"city":"Paris"}`)),
		aitest.Stream(aitest.Text("Paris ", "it is.")),
	)

	calls := 0
	events, streamable, err := collect(t, manager(t, mock), testAgent{tools: []ai.Tool{echoTool{&calls}}}, "Go")
	if err != nil {
		t.Fatal(err)
	}

	if calls != 1 {
		t.Fatalf("tool ran %d times", calls)
	}

	response := streamable.Response()
	if response.Text != "Paris it is." || len(response.Steps) != 2 || len(response.ToolResults) != 1 {
		t.Fatalf("unexpected response: %+v", response)
	}

	var kinds []string
	for _, e := range events {
		kinds = append(kinds, typeName(e))
	}
	want := []string{"StreamStart", "ToolCallEvent", "ToolResultEvent", "StreamStart", "TextStart", "TextDelta", "TextDelta", "TextEnd", "StreamEnd"}
	if !slices.Equal(kinds, want) {
		t.Fatalf("events:\n got %v\nwant %v", kinds, want)
	}

	bodies := aitest.SentRawBodies(mock)
	if len(bodies) != 2 {
		t.Fatalf("sent %d bodies", len(bodies))
	}
	if !strings.Contains(bodies[1], `{"role":"tool","tool_call_id":"call_1","content":"{\"city\":\"Paris\"}"}`) {
		t.Fatalf("second body lacks the tool result: %s", bodies[1])
	}
	if !strings.Contains(bodies[0], `"tool_choice":"auto","tools":[{"type":"function","function":{"name":"Echo","description":"Echo the arguments back.","parameters":{"type":"object","properties":{"city":{"description":"A city.","type":"string"}},"required":["city"],"additionalProperties":false}}}],"stream":true,"stream_options":{"include_usage":true}`) {
		t.Fatalf("first body: %s", bodies[0])
	}
}

func TestRepairsACallToAnUnknownTool(t *testing.T) {
	mock := httpclient.NewMock(t, map[string]any{})
	aitest.Script(mock,
		aitest.Stream(aitest.ToolCall("call_1", "Nope", `{}`)),
		aitest.Stream(aitest.Text("Sorry.")),
	)

	calls := 0
	_, streamable, err := collect(t, manager(t, mock), testAgent{tools: []ai.Tool{echoTool{&calls}}, repairs: true}, "Go")
	if err != nil {
		t.Fatal(err)
	}

	result := streamable.Response().ToolResults[0]
	if !result.Failed || result.Result != "Tool 'Nope' does not exist. Available tools: Echo." {
		t.Fatalf("repair result: %+v", result)
	}
}

func TestAnUnknownToolFailsTheRunWithoutRepair(t *testing.T) {
	mock := httpclient.NewMock(t, map[string]any{})
	aitest.Script(mock, aitest.Stream(aitest.ToolCall("call_1", "Nope", `{}`)))

	calls := 0
	_, _, err := collect(t, manager(t, mock), testAgent{tools: []ai.Tool{echoTool{&calls}}}, "Go")
	if err == nil || err.Error() != "Model tried to call unavailable tool 'Nope'." {
		t.Fatalf("err = %v", err)
	}
}

func TestTheFinalStepSettlesItsToolCallsUnrun(t *testing.T) {
	mock := httpclient.NewMock(t, map[string]any{})
	aitest.Script(mock, aitest.Stream(aitest.ToolCall("call_1", "Echo", `{"city":"Rome"}`)))

	calls := 0
	_, streamable, err := collect(t, manager(t, mock), testAgent{tools: []ai.Tool{echoTool{&calls}}, steps: 1}, "Go")
	if err != nil {
		t.Fatal(err)
	}

	if calls != 0 || streamable.Response().ToolResults[0].Result != "The agent reached its maximum number of steps without running this tool call." {
		t.Fatalf("calls %d results %+v", calls, streamable.Response().ToolResults)
	}
}

func TestAStreamErrorEventEndsTheRunWithAStreamError(t *testing.T) {
	mock := httpclient.NewMock(t, map[string]any{})
	aitest.Script(mock, aitest.Stream(aitest.ErrorChunk(502, "Provider is overloaded")))

	events, _, err := collect(t, manager(t, mock), testAgent{}, "Go")
	if !ai.IsStreamError(err) || err.Error() != "Provider is overloaded" {
		t.Fatalf("err = %v", err)
	}
	if e, ok := events[0].(ai.ErrorEvent); !ok || e.Type != "502" {
		t.Fatalf("events = %+v", events)
	}
}

func TestProviderStatusesMapToFailoverableErrors(t *testing.T) {
	for status, kind := range map[int]ai.ProviderErrorKind{402: ai.InsufficientCredits, 429: ai.RateLimited, 503: ai.ProviderOverloaded} {
		mock := httpclient.NewMock(t, map[string]any{})
		aitest.Script(mock, aitest.Status(status, "nope"))

		_, _, err := collect(t, manager(t, mock), testAgent{}, "Go")
		if !ai.IsFailoverable(err) {
			t.Fatalf("%d: %v", status, err)
		}
		var providerErr *ai.ProviderError
		if !errors.As(err, &providerErr) || providerErr.Kind != kind {
			t.Fatalf("%d: kind %s", status, providerErr.Kind)
		}
	}
}

type structuredAgent struct{ testAgent }

func (structuredAgent) Schema() jsonschema.Properties {
	return jsonschema.Properties{
		{Name: "facts", Type: jsonschema.Array().Items(jsonschema.String()).Required()},
	}
}

func TestPromptsForStructuredOutput(t *testing.T) {
	mock := httpclient.NewMock(t, map[string]any{})
	aitest.ScriptCompletions(mock, aitest.Completion("```json\n{\"facts\":[\"a\",\"b\"]}\n```"))

	response, err := manager(t, mock).Prompt(context.Background(), structuredAgent{}, "Paris")
	if err != nil {
		t.Fatal(err)
	}

	var decoded struct{ Facts []string }
	if err := response.Decode(&decoded); err != nil || len(decoded.Facts) != 2 {
		t.Fatalf("decoded %+v (%v)", decoded, err)
	}

	body := aitest.SentRawBodies(mock)[0]
	want := `{"model":"openai/gpt-test","messages":[{"role":"system","content":"Be brief."},{"role":"user","content":"Paris"}],"response_format":{"type":"json_schema","json_schema":{"name":"schema_definition","schema":{"additionalProperties":false,"properties":{"facts":{"items":{"type":"string"},"type":"array"}},"type":"object","required":["facts"]},"strict":false}}}`
	if body != want {
		t.Fatalf("body:\n got %s\nwant %s", body, want)
	}
}

type facts struct {
	Facts []string `json:"facts"`
}

type factsAgent struct {
	testAgent
	ai.StructuredOutput[facts]
}

// The answer's type is the schema's source: the body asks for exactly the
// shape PromptStructured decodes into.
func TestPromptStructuredDecodesIntoTheTypeTheSchemaDescribes(t *testing.T) {
	mock := httpclient.NewMock(t, map[string]any{})
	aitest.ScriptCompletions(mock, aitest.Completion(`{"facts":["a","b"]}`))

	answer, err := ai.PromptStructured(context.Background(), manager(t, mock), factsAgent{}, "Paris")
	if err != nil || !slices.Equal(answer.Facts, []string{"a", "b"}) {
		t.Fatalf("answer %+v (%v)", answer, err)
	}

	body := aitest.SentRawBodies(mock)[0]
	want := `{"model":"openai/gpt-test","messages":[{"role":"system","content":"Be brief."},{"role":"user","content":"Paris"}],"response_format":{"type":"json_schema","json_schema":{"name":"schema_definition","schema":{"additionalProperties":false,"properties":{"facts":{"items":{"type":"string"},"type":"array"}},"type":"object","required":["facts"]},"strict":false}}}`
	if body != want {
		t.Fatalf("body:\n got %s\nwant %s", body, want)
	}
}

func TestPromptStructuredFailsOnAnAnswerOfTheWrongShape(t *testing.T) {
	mock := httpclient.NewMock(t, map[string]any{})
	aitest.ScriptCompletions(mock, aitest.Completion(`{"facts":"just one"}`))

	_, err := ai.PromptStructured(context.Background(), manager(t, mock), factsAgent{}, "Paris")

	var typeErr *json.UnmarshalTypeError
	if !errors.As(err, &typeErr) || !strings.Contains(err.Error(), "prompting TestAgent") {
		t.Fatalf("err = %v", err)
	}
}

func typeName(v any) string {
	return strings.TrimPrefix(fmt.Sprintf("%T", v), "ai.")
}

// slowTool ignores its context and outlives the run's deadline.
type slowTool struct{ delay time.Duration }

func (slowTool) Name() string        { return "Slow" }
func (slowTool) Description() string { return "Takes its time." }
func (slowTool) Schema() jsonschema.Properties {
	return jsonschema.Properties{
		{Name: "city", Type: jsonschema.String()},
	}
}

func (t slowTool) Handle(context.Context, ai.Request) (string, error) {
	time.Sleep(t.delay)

	return `{"done":true}`, nil
}

func TestADeadlineEndsTheRunBeforeAnotherStepStarts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		mock := httpclient.NewMock(t, map[string]any{})
		aitest.Script(mock,
			aitest.Stream(aitest.ToolCall("call_1", "Slow", `{"city":"Paris"}`)),
			aitest.Stream(aitest.Text("Too late.")),
		)

		streamable, err := manager(t, mock).Stream(context.Background(), testAgent{tools: []ai.Tool{slowTool{delay: 80 * time.Millisecond}}}, "Go")
		if err != nil {
			t.Fatal(err)
		}
		streamable.Deadline = 20 * time.Millisecond

		var caught error
		streamable.Catch(func(ctx context.Context, err error) {
			if ctx.Err() != nil {
				t.Error("Catch got the expired run context")
			}
			caught = err
		})

		err = streamable.Each(context.Background(), func(ai.StreamEvent) {})

		var deadline *ai.TurnDeadlineError
		if !errors.As(err, &deadline) || !errors.Is(err, context.DeadlineExceeded) || ai.IsFailoverable(err) {
			t.Fatalf("err = %v", err)
		}
		if !errors.Is(caught, err) {
			t.Fatalf("caught %v", caught)
		}
		if n := len(aitest.SentRawBodies(mock)); n != 1 {
			t.Fatalf("sent %d steps after the deadline passed", n)
		}
	})
}

// waitingGateway is a provider that never answers until the request's
// context ends, as a real HTTP call cut off by its context does.
type waitingGateway struct{ ai.Gateway }

func (waitingGateway) StreamStep(ctx context.Context, _ string, provider ai.ProviderConfig, _ ai.PendingStep, _ func(ai.StreamEvent)) (*ai.StepResponse, error) {
	<-ctx.Done()

	return nil, &ai.ProviderError{Kind: ai.ProviderConnection, Provider: provider.Name, Err: ctx.Err()}
}

func TestADeadlineCutsOffAStepInFlightWithoutReadingAsAnOutage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := managerOn(waitingGateway{})

		streamable, err := m.Stream(context.Background(), testAgent{}, "Go")
		if err != nil {
			t.Fatal(err)
		}
		streamable.Deadline = 20 * time.Millisecond

		err = streamable.Each(context.Background(), func(ai.StreamEvent) {})

		var deadline *ai.TurnDeadlineError
		if !errors.As(err, &deadline) || ai.IsFailoverable(err) || err.Error() != "The run exceeded its 20ms deadline." {
			t.Fatalf("err = %v", err)
		}
	})
}
