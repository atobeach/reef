package ai_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/atobeach/reef/ai"
	"github.com/atobeach/reef/events"
	"github.com/atobeach/reef/jsonschema"
	"github.com/atobeach/reef/panics"
)

// panicTool has a bug: it panics instead of answering, once its context
// ends when it waits.
type panicTool struct{ waits bool }

func (panicTool) Name() string        { return "Explode" }
func (panicTool) Description() string { return "Has a bug." }
func (panicTool) Schema() jsonschema.Properties {
	return jsonschema.Properties{}
}

func (t panicTool) Handle(ctx context.Context, _ ai.Request) (string, error) {
	if t.waits {
		<-ctx.Done()
	}

	panic("tool bug")
}

// instructionsBug is an agent whose instructions panic, before any step.
type instructionsBug struct{ testAgent }

func (instructionsBug) Instructions(context.Context) string { panic("instructions bug") }

// rememberingAgent stores its turns in conversation-1.
type rememberingAgent struct {
	testAgent
	ai.RemembersConversations
}

func remembering(agent testAgent) *rememberingAgent {
	remembered := &rememberingAgent{testAgent: agent}
	remembered.ContinueConversation("conversation-1")

	return remembered
}

// panickingStore is a conversation store with a bug: storing a message
// panics.
type panickingStore struct{ ai.ConversationStore }

func (panickingStore) LatestConversationMessages(context.Context, string, int) ([]ai.Message, error) {
	return nil, nil
}

func (panickingStore) StoreUserMessage(context.Context, string, string, string) (string, error) {
	panic("store bug")
}

// explodingGateway is a provider whose every step calls the Explode tool.
type explodingGateway struct{}

func (explodingGateway) GenerateStep(context.Context, ai.ProviderConfig, ai.PendingStep) (*ai.StepResponse, error) {
	return &ai.StepResponse{ToolCalls: []ai.ToolCall{{ID: "call_1", Name: "Explode"}}, FinishReason: ai.FinishToolCalls}, nil
}

func (g explodingGateway) StreamStep(ctx context.Context, _ string, provider ai.ProviderConfig, step ai.PendingStep, _ func(ai.StreamEvent)) (*ai.StepResponse, error) {
	return g.GenerateStep(ctx, provider, step)
}

// answeringGateway is a provider that answers every step with text.
type answeringGateway struct{}

func (answeringGateway) GenerateStep(context.Context, ai.ProviderConfig, ai.PendingStep) (*ai.StepResponse, error) {
	return &ai.StepResponse{Text: "Hi", FinishReason: ai.FinishStop}, nil
}

func (g answeringGateway) StreamStep(ctx context.Context, _ string, provider ai.ProviderConfig, step ai.PendingStep, _ func(ai.StreamEvent)) (*ai.StepResponse, error) {
	return g.GenerateStep(ctx, provider, step)
}

// runLogs captures the default logger's JSON lines for the test; line is
// the first with msg, nil when there is none.
func runLogs(t *testing.T) (line func(msg string) map[string]any) {
	t.Helper()

	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	return func(msg string) map[string]any {
		for raw := range bytes.SplitSeq(bytes.TrimSpace(logs.Bytes()), []byte("\n")) {
			var line map[string]any
			if err := json.Unmarshal(raw, &line); err == nil && line["msg"] == msg {
				return line
			}
		}

		return nil
	}
}

// lifecycle is a dispatcher that keeps the run's end events.
type lifecycle struct {
	dispatcher *events.Dispatcher
	failed     []error
	succeeded  int
}

func newLifecycle() *lifecycle {
	l := &lifecycle{dispatcher: events.NewDispatcher(nil)}
	events.Listen(l.dispatcher, func(_ context.Context, event ai.AgentFailed) error {
		l.failed = append(l.failed, event.Err)

		return nil
	})
	events.Listen(l.dispatcher, func(context.Context, ai.AgentStreamed) error {
		l.succeeded++

		return nil
	})
	events.Listen(l.dispatcher, func(context.Context, ai.AgentPrompted) error {
		l.succeeded++

		return nil
	})

	return l
}

func panicManager(gateway ai.Gateway, dispatcher *events.Dispatcher, store ai.ConversationStore) *ai.Manager {
	return ai.NewManager(ai.ManagerConfig{
		Providers: map[string]ai.ProviderConfig{"openrouter": {Name: "openrouter"}},
		Default:   "openrouter",
		Gateway:   gateway,
		Events:    dispatcher,
		Store:     store,
	})
}

// run is one way of running an agent, returning the run's error.
type run func(t *testing.T, m *ai.Manager, agent ai.Agent) error

// streamed runs the agent streamed under deadline (zero for none). A
// failure in the run reaches its Catch callbacks, one storing the completed
// turn doesn't: catches says which this run's is, and the test fails
// unless Catch got what it should.
func streamed(deadline time.Duration, catches bool) run {
	return func(t *testing.T, m *ai.Manager, agent ai.Agent) error {
		t.Helper()

		streamable, err := m.Stream(context.Background(), agent, "Go")
		if err != nil {
			t.Fatal(err)
		}
		streamable.Deadline = deadline

		var caught error
		streamable.Catch(func(_ context.Context, err error) { caught = err })

		err = streamable.Each(context.Background(), func(ai.StreamEvent) {})
		if catches && !errors.Is(caught, err) || !catches && caught != nil {
			t.Fatalf("Catch got %v; the run failed with %v", caught, err)
		}

		return err
	}
}

func prompted(_ *testing.T, m *ai.Manager, agent ai.Agent) error {
	_, err := m.Prompt(context.Background(), agent, "Go")

	return err
}

// A panic anywhere in a run (a tool, the agent's own instructions) fails it
// like a returned error: AgentFailed (and a streamed run's Catch) see it,
// and it is logged with its stack, even when it comes after the turn's
// deadline.
func TestAPanicInARunFailsIt(t *testing.T) {
	cases := []struct {
		name     string
		agent    ai.Agent
		gateway  ai.Gateway
		run      run
		value    string
		function string
	}{
		{"a tool, streamed", testAgent{tools: []ai.Tool{panicTool{}}}, explodingGateway{}, streamed(0, true), "tool bug", "ai_test.panicTool.Handle"},
		{"a tool, prompted", testAgent{tools: []ai.Tool{panicTool{}}}, explodingGateway{}, prompted, "tool bug", "ai_test.panicTool.Handle"},
		{"a tool after the deadline, streamed", testAgent{tools: []ai.Tool{panicTool{waits: true}}}, explodingGateway{}, streamed(20*time.Millisecond, true), "tool bug", "ai_test.panicTool.Handle"},
		{"the agent's instructions, streamed", instructionsBug{}, answeringGateway{}, streamed(0, true), "instructions bug", "ai_test.instructionsBug.Instructions"},
		{"the agent's instructions, prompted", instructionsBug{}, answeringGateway{}, prompted, "instructions bug", "ai_test.instructionsBug.Instructions"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logged := runLogs(t)
			lifecycle := newLifecycle()

			err := tc.run(t, panicManager(tc.gateway, lifecycle.dispatcher, nil), tc.agent)

			recovered, ok := errors.AsType[*panics.Error](err)
			if !ok || recovered.Value != tc.value || err.Error() != "ai: run panicked: "+tc.value {
				t.Fatalf("err = %v, want the panic %q", err, tc.value)
			}
			if _, deadline := errors.AsType[*ai.TurnDeadlineError](err); deadline {
				t.Fatalf("err = %v, a panic is not the deadline's doing", err)
			}
			if len(lifecycle.failed) != 1 || !errors.Is(lifecycle.failed[0], err) || lifecycle.succeeded != 0 {
				t.Fatalf("AgentFailed with %v (and %d successes), want once with %v", lifecycle.failed, lifecycle.succeeded, err)
			}

			line := logged("ai: run panicked")
			if stack, _ := line["stack"].(string); line["error"] != tc.value || !strings.Contains(stack, tc.function+"\n") {
				t.Fatalf("logged %v, want the panic with a stack naming %s", line, tc.function)
			}
		})
	}
}

// Listeners observe a run and must never fail it: one that panics is
// logged with its stack, and the run completes.
func TestAListenerThatPanicsNeverFailsTheRun(t *testing.T) {
	cases := []struct {
		name   string
		listen func(d *events.Dispatcher)
		run    run
	}{
		{"on StreamingAgent", func(d *events.Dispatcher) {
			events.Listen(d, func(context.Context, ai.StreamingAgent) error { panic("listener bug") })
		}, streamed(0, true)},
		{"on AgentStreamed", func(d *events.Dispatcher) {
			events.Listen(d, func(context.Context, ai.AgentStreamed) error { panic("listener bug") })
		}, streamed(0, true)},
		{"on PromptingAgent", func(d *events.Dispatcher) {
			events.Listen(d, func(context.Context, ai.PromptingAgent) error { panic("listener bug") })
		}, prompted},
		{"on AgentPrompted", func(d *events.Dispatcher) {
			events.Listen(d, func(context.Context, ai.AgentPrompted) error { panic("listener bug") })
		}, prompted},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logged := runLogs(t)
			lifecycle := newLifecycle()
			tc.listen(lifecycle.dispatcher)

			if err := tc.run(t, panicManager(answeringGateway{}, lifecycle.dispatcher, nil), testAgent{}); err != nil {
				t.Fatalf("err = %v, want the run to complete", err)
			}
			if len(lifecycle.failed) != 0 {
				t.Fatalf("AgentFailed with %v", lifecycle.failed)
			}

			line := logged("ai: event listener failed")
			if stack, _ := line["stack"].(string); line["error"] != "listener bug" || !strings.Contains(stack, "ai_test.TestAListenerThatPanicsNeverFailsTheRun.") {
				t.Fatalf("logged %v, want the listener's panic with its stack", line)
			}
		})
	}
}

// Storing a completed turn is part of the run: a store that panics fails
// it, and AgentFailed ends it instead of AgentStreamed or AgentPrompted.
func TestAStoreThatPanicsOnACompletedTurnFailsTheRun(t *testing.T) {
	for name, run := range map[string]run{"streamed": streamed(0, false), "prompted": prompted} {
		t.Run(name, func(t *testing.T) {
			runLogs(t)
			lifecycle := newLifecycle()

			err := run(t, panicManager(answeringGateway{}, lifecycle.dispatcher, panickingStore{}), remembering(testAgent{}))

			if recovered, ok := errors.AsType[*panics.Error](err); !ok || recovered.Value != "store bug" {
				t.Fatalf("err = %v, want the store's panic", err)
			}
			if len(lifecycle.failed) != 1 || !errors.Is(lifecycle.failed[0], err) || lifecycle.succeeded != 0 {
				t.Fatalf("AgentFailed with %v (and %d successes), want once with %v", lifecycle.failed, lifecycle.succeeded, err)
			}
		})
	}
}

// Storing a failed turn never raises over the failure: a store that panics
// there is logged with its stack, and the run's own error is the one
// returned.
func TestAStoreThatPanicsOnAFailedTurnKeepsTheRunsError(t *testing.T) {
	for name, run := range map[string]run{"streamed": streamed(0, true), "prompted": prompted} {
		t.Run(name, func(t *testing.T) {
			logged := runLogs(t)
			lifecycle := newLifecycle()

			err := run(t, panicManager(explodingGateway{}, lifecycle.dispatcher, panickingStore{}), remembering(testAgent{tools: []ai.Tool{panicTool{}}}))

			if err == nil || err.Error() != "ai: run panicked: tool bug" || len(lifecycle.failed) != 1 {
				t.Fatalf("err = %v, AgentFailed with %v; want the tool's panic once", err, lifecycle.failed)
			}

			line := logged("ai: storing a failed turn")
			if stack, _ := line["stack"].(string); line["error"] != "store bug" || !strings.Contains(stack, "ai_test.panickingStore.StoreUserMessage\n") {
				t.Fatalf("logged %v, want the store's panic with its stack", line)
			}
		})
	}
}

// A Then callback that panics fails the run like one that errs; a Catch
// callback that panics leaves the run's own error, and the callbacks after
// it still run.
func TestAStreamsCallbacksThatPanicAreItsFailures(t *testing.T) {
	runLogs(t)

	completed := ai.NewStreamableAgentResponse("inv-1", ai.Meta{}, ai.StreamEvents(nil))
	completed.Then(func(context.Context, *ai.AgentResponse) error { panic("then bug") })

	err := completed.Each(context.Background(), func(ai.StreamEvent) {})
	if recovered, ok := errors.AsType[*panics.Error](err); !ok || recovered.Value != "then bug" {
		t.Fatalf("err = %v, want the Then callback's panic", err)
	}

	cause := errors.New("provider down")
	failed := ai.NewStreamableAgentResponse("inv-2", ai.Meta{}, ai.StreamEvents(cause))
	var caught error
	failed.Catch(func(context.Context, error) { panic("catch bug") })
	failed.Catch(func(_ context.Context, err error) { caught = err })

	if err := failed.Each(context.Background(), func(ai.StreamEvent) {}); !errors.Is(err, cause) || !errors.Is(caught, cause) {
		t.Fatalf("err = %v, the next Catch got %v; want the run's own error in both", err, caught)
	}
}
