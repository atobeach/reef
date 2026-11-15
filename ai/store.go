package ai

import (
	"context"
	"slices"
	"strconv"
	"time"

	"github.com/atobeach/reef/clock"
	"github.com/atobeach/reef/db"
	"github.com/atobeach/reef/ordered"
	"github.com/atobeach/reef/support"
)

// ConversationStore persists conversations and their messages. Ownership is
// the app's, so the participant columns stay null.
type ConversationStore interface {
	StoreConversation(ctx context.Context, title string, id string) (string, error)
	StoreUserMessage(ctx context.Context, conversationID string, agentClass string, content string) (string, error)
	// StoreAssistantMessage records the turn; failure non-nil marks it failed.
	StoreAssistantMessage(ctx context.Context, conversationID string, prompt AgentPrompt, response *AgentResponse, failure error) (string, error)
	LatestConversationMessages(ctx context.Context, conversationID string, limit int) ([]Message, error)
}

// Message statuses, as stored in agent_conversation_messages.status.
const (
	MessageCompleted = "completed"
	MessageFailed    = "failed"
	MessagePaused    = "paused"
)

// DatabaseConversationStore is the ConversationStore over database tables.
// Stored conversations and clients read the message columns' JSON shapes
// and key order back, so they must not drift.
type DatabaseConversationStore struct {
	ConversationsTable string
	MessagesTable      string
	// Encode writes the JSON columns (nil: encoding/json); rows stored in
	// another dialect keep matching only with that dialect's encoder.
	Encode Encoder
}

// NewDatabaseConversationStore uses the agent_conversations tables, its
// JSON columns written by encode (nil: encoding/json).
func NewDatabaseConversationStore(encode Encoder) *DatabaseConversationStore {
	return &DatabaseConversationStore{ConversationsTable: "agent_conversations", MessagesTable: "agent_conversation_messages", Encode: encode}
}

type messageRow struct {
	ID      string `db:"id"`
	Role    string `db:"role"`
	Content string `db:"content"`
	Steps   string `db:"steps"`
	Meta    string `db:"meta"`
	Status  string `db:"status"`
}

// StoreConversation implements ConversationStore.
func (s *DatabaseConversationStore) StoreConversation(ctx context.Context, title string, id string) (string, error) {
	if id == "" {
		id = support.UUID7()
	}

	at := clock.Now(ctx)
	if err := db.Table[struct{}](ctx, s.ConversationsTable).Insert(db.Values{
		"id": id, "participant_type": nil, "participant_id": nil,
		"title": title, "created_at": at, "updated_at": at,
	}); err != nil {
		return "", err
	}

	return id, nil
}

// StoreUserMessage implements ConversationStore.
func (s *DatabaseConversationStore) StoreUserMessage(ctx context.Context, conversationID string, agentClass string, content string) (string, error) {
	id := support.UUID7()
	at := clock.Now(ctx)

	// A user row has no steps, usage or meta: empty lists and objects in
	// the store's encoding.
	none, err := s.Encode.encode([]any{})
	if err != nil {
		return "", err
	}
	empty, err := s.Encode.encode(ordered.NewObject())
	if err != nil {
		return "", err
	}

	if err := db.Table[struct{}](ctx, s.MessagesTable).Insert(db.Values{
		"id": id, "conversation_id": conversationID,
		"participant_type": nil, "participant_id": nil,
		"agent": agentClass, "role": "user", "content": content,
		"attachments": string(none), "steps": string(none), "usage": string(empty), "meta": string(empty),
		"status":     MessageCompleted,
		"created_at": at, "updated_at": at,
	}); err != nil {
		return "", err
	}

	return id, s.touchConversation(ctx, conversationID, at)
}

// StoreAssistantMessage implements ConversationStore.
func (s *DatabaseConversationStore) StoreAssistantMessage(ctx context.Context, conversationID string, prompt AgentPrompt, response *AgentResponse, failure error) (string, error) {
	id := support.UUID7()
	at := clock.Now(ctx)

	steps, err := s.Encode.encode(stepsFor(response))
	if err != nil {
		return "", err
	}

	usage, err := s.Encode.encode(response.Usage.stored())
	if err != nil {
		return "", err
	}

	meta := response.Meta.stored()
	status := MessageCompleted
	if failure != nil {
		meta.Set("error", failure.Error())
		status = MessageFailed
	}

	encodedMeta, err := s.Encode.encode(meta)
	if err != nil {
		return "", err
	}

	if err := db.Table[struct{}](ctx, s.MessagesTable).Insert(db.Values{
		"id": id, "conversation_id": conversationID,
		"participant_type": nil, "participant_id": nil,
		"agent": prompt.Agent.Class(), "role": "assistant", "content": response.Text,
		"attachments": "[]", "steps": string(steps), "usage": string(usage), "meta": string(encodedMeta),
		"status":     status,
		"created_at": at, "updated_at": at,
	}); err != nil {
		return "", err
	}

	return id, s.touchConversation(ctx, conversationID, at)
}

// UpdateMessage writes values onto a stored message, for an app that
// stamps its own columns onto the rows this store writes.
func (s *DatabaseConversationStore) UpdateMessage(ctx context.Context, id string, values db.Values) error {
	_, err := db.Table[struct{}](ctx, s.MessagesTable).WhereKey(id).Update(values)

	return err
}

func (s *DatabaseConversationStore) touchConversation(ctx context.Context, conversationID string, at time.Time) error {
	_, err := db.Table[struct{}](ctx, s.ConversationsTable).WhereKey(conversationID).Update(db.Values{"updated_at": at})

	return err
}

// stepsFor is a response's `steps` column: one entry per model round trip,
// each tool call paired with the result that answered it.
func stepsFor(response *AgentResponse) []any {
	if len(response.Steps) > 0 {
		steps := make([]any, len(response.Steps))
		for i, step := range response.Steps {
			steps[i] = storedStep(step.Text, toolCallsFor(step.ToolCalls, step.ToolResults), step.Reasoning)
		}

		return steps
	}

	return []any{storedStep(response.Text, toolCallsFor(response.ToolCalls, response.ToolResults), response.Reasoning)}
}

func storedStep(content string, toolCalls []any, reasoning string) *ordered.Object {
	step := ordered.NewObject()
	step.Set("content", content)
	step.Set("tool_calls", toolCalls)
	step.Set("reasoning", reasoning)
	step.Set("replay_blocks", []any{})
	step.Set("provider_tool_calls", []any{})

	return step
}

// toolCallsFor is a step's stored tool calls: {id, name, arguments,
// result_id} plus the answering result's `result` (and `denied`/`failed`
// when set).
func toolCallsFor(calls []ToolCall, results []ToolResult) []any {
	byID := map[string]ToolResult{}
	for _, result := range results {
		byID[result.ID] = result
	}

	stored := make([]any, len(calls))
	for i, call := range calls {
		entry := ordered.NewObject()
		entry.Set("id", call.ID)
		entry.Set("name", call.Name)
		entry.Set("arguments", argumentsOrEmpty(call.Arguments))
		entry.Set("result_id", nullableString(call.ResultID))

		if result, ok := byID[call.ID]; ok {
			entry.Set("result", result.Result)
			if result.Denied {
				entry.Set("denied", true)
			}
			if result.Failed {
				entry.Set("failed", true)
			}
		}

		stored[i] = entry
	}

	return stored
}

func argumentsOrEmpty(arguments *ordered.Object) *ordered.Object {
	if arguments == nil {
		return ordered.NewObject()
	}

	return arguments
}

// LatestConversationMessages implements ConversationStore: the newest rows,
// oldest first, each assistant row rebuilt step by step so every tool result
// answers the message that made its call.
func (s *DatabaseConversationStore) LatestConversationMessages(ctx context.Context, conversationID string, limit int) ([]Message, error) {
	rows, err := db.Table[messageRow](ctx, s.MessagesTable).
		Where("conversation_id", conversationID).
		Latest("id").
		Limit(limit).
		Select("id", "role", "content", "steps", "meta", "status").
		Get()
	if err != nil {
		return nil, err
	}

	slices.Reverse(rows)

	var messages []Message
	for _, row := range rows {
		if row.Role == "user" {
			messages = append(messages, UserMessage{Content: row.Content})

			continue
		}

		messages = append(messages, assistantTurnFrom(row)...)
	}

	return messages, nil
}

func assistantTurnFrom(row messageRow) []Message {
	failed := row.Status == MessageFailed

	var messages []Message
	for _, step := range decodedSteps(row.Steps) {
		var calls []ToolCall
		var results []ToolResult

		for _, raw := range step.toolCalls {
			_, answered := raw.Get("result")
			pending := raw.Has("approval_reason") && !answered
			if !failed && !answered && !pending {
				continue
			}

			call := toolCallFromStored(raw)
			calls = append(calls, call)

			switch {
			case answered:
				results = append(results, toolResultFromStored(raw))
			case failed:
				results = append(results, ToolResult{
					ID: call.ID, Name: call.Name, Arguments: call.Arguments,
					Result: "This tool call was interrupted before a result was recorded, so it may or may not have run.",
				})
			}
		}

		if step.content != "" || len(calls) > 0 {
			messages = append(messages, AssistantMessage{Content: step.content, ToolCalls: calls})
		}

		if len(results) > 0 {
			messages = append(messages, ToolResultMessage{Results: results})
		}
	}

	return messages
}

type decodedStep struct {
	content   string
	toolCalls []*ordered.Object
}

func decodedSteps(raw string) []decodedStep {
	decoded, err := ordered.Decode([]byte(raw))
	if err != nil {
		return nil
	}

	entries := listValues(decoded)
	steps := make([]decodedStep, 0, len(entries))
	for _, entry := range entries {
		object, _ := entry.(*ordered.Object)
		if object == nil {
			continue
		}

		step := decodedStep{}
		if content, ok := object.Get("content"); ok {
			step.content = stringOf(content)
		}

		if calls, ok := object.Get("tool_calls"); ok {
			for _, call := range listValues(calls) {
				if callObject, ok := call.(*ordered.Object); ok {
					step.toolCalls = append(step.toolCalls, callObject)
				}
			}
		}

		steps = append(steps, step)
	}

	return steps
}

// listValues is a stored list's items, in order: a row can hold a list as
// an object keyed "0", "1", ….
func listValues(value any) []any {
	switch v := value.(type) {
	case []any:
		return v
	case *ordered.Object:
		return v.Values()
	}

	return nil
}

func storedArguments(raw *ordered.Object) *ordered.Object {
	value, _ := raw.Get("arguments")
	switch v := value.(type) {
	case *ordered.Object:
		return v
	case []any:
		object := ordered.NewObject()
		for i, item := range v {
			object.Set(strconv.Itoa(i), item)
		}

		return object
	}

	return ordered.NewObject()
}

// storedString is a stored column member as text, "" when absent or null.
func storedString(raw *ordered.Object, key string) string {
	return support.Deref(storedNullableString(raw, key))
}

func storedNullableString(raw *ordered.Object, key string) *string {
	value, ok := raw.Get(key)
	if !ok || value == nil {
		return nil
	}

	text := stringOf(value)

	return &text
}

func toolCallFromStored(raw *ordered.Object) ToolCall {
	return ToolCall{
		ID:        storedString(raw, "id"),
		Name:      storedString(raw, "name"),
		Arguments: storedArguments(raw),
		ResultID:  storedNullableString(raw, "result_id"),
	}
}

func toolResultFromStored(raw *ordered.Object) ToolResult {
	result, _ := raw.Get("result")
	denied, _ := raw.Get("denied")
	failed, _ := raw.Get("failed")

	return ToolResult{
		ID:        storedString(raw, "id"),
		Name:      storedString(raw, "name"),
		Arguments: storedArguments(raw),
		Result:    result,
		ResultID:  storedNullableString(raw, "result_id"),
		Denied:    denied == true,
		Failed:    failed == true,
	}
}
