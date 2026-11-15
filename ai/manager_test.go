package ai

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

type failingAssistantStore struct{ ConversationStore }

func (failingAssistantStore) StoreUserMessage(context.Context, string, string, string) (string, error) {
	return "user-message", nil
}

func (failingAssistantStore) StoreAssistantMessage(context.Context, string, AgentPrompt, *AgentResponse, error) (string, error) {
	return "", errors.New("db down")
}

type rememberingAgent struct{ RemembersConversations }

func (rememberingAgent) Class() string                       { return "TestAgent" }
func (rememberingAgent) Model() string                       { return "test-model" }
func (rememberingAgent) Instructions(context.Context) string { return "" }

// A failed turn that cannot be stored is logged: the run's own failure is
// what the caller sees, but the lost turn must not vanish silently.
func TestAFailedTurnThatCannotBeStoredIsLogged(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	agent := &rememberingAgent{}
	agent.SetConversation("conversation-1")

	run := &RunContext{InvocationID: "run-1", Agent: agent}
	run.recordStep(&Step{})

	m := NewManager(ManagerConfig{Store: failingAssistantStore{}})
	m.rememberFailedTurn(t.Context(), agent, AgentPrompt{Agent: agent, Prompt: "hi"}, run, errors.New("provider failed"))

	if !strings.Contains(logs.String(), "ai: storing a failed turn") || !strings.Contains(logs.String(), "db down") {
		t.Fatalf("logs = %q", logs.String())
	}
}
