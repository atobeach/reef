package ai

import (
	"strings"

	"github.com/atobeach/reef/jsonschema"
	"github.com/atobeach/reef/ordered"
)

// chatCompletionBody is the Chat Completions request body for a step:
// model, messages, then tools, response format, sampling options and the
// agent's provider options, in that key order. encode writes the JSON
// carried inside it as strings (tool-call arguments, structured results).
func chatCompletionBody(provider ProviderConfig, step PendingStep, encode Encoder) *ordered.Object {
	body := ordered.NewObject()
	body.Set("model", step.Model)
	body.Set("messages", mapMessagesToChat(step.Messages, step.Instructions, encode))

	if len(step.Tools) > 0 {
		tools := make([]any, len(step.Tools))
		for i, tool := range step.Tools {
			tools[i] = toolDefinition(tool)
		}

		var choice any = ToolChoiceAuto
		if step.Options != nil && step.Options.ToolChoice != nil {
			choice = mapToolChoice(*step.Options.ToolChoice)
		}
		body.Set("tool_choice", choice)
		body.Set("tools", tools)
	}

	if len(step.Schema) > 0 {
		body.Set("response_format", responseFormat(step.Schema))
	}

	if step.Options != nil {
		if step.Options.MaxTokens != nil {
			body.Set("max_tokens", *step.Options.MaxTokens)
		}
		if step.Options.Temperature != nil {
			body.Set("temperature", *step.Options.Temperature)
		}
		if step.Options.TopP != nil {
			body.Set("top_p", *step.Options.TopP)
		}
	}

	options := step.Options.ProviderOptions(provider.Driver)
	for _, key := range options.Keys() {
		value, _ := options.Get(key)
		body.Set(key, value)
	}

	return body
}

func mapToolChoice(choice ToolChoice) any {
	if choice.Mode != ToolChoiceTool {
		return choice.Mode
	}

	function := ordered.NewObject()
	function.Set("name", choice.ToolName)
	mapped := ordered.NewObject()
	mapped.Set("type", "function")
	mapped.Set("function", function)

	return mapped
}

// toolDefinition is a tool as the model is offered it. Strict mode is off,
// so additionalProperties is the only constraint beyond the schema itself.
func toolDefinition(tool Tool) *ordered.Object {
	parameters := ordered.NewObject()
	parameters.Set("type", "object")

	// A tool without parameters still lists `properties`, as the empty
	// struct: an encoder that writes an empty object as a list (`[]`) still
	// writes struct{}{} as `{}`.
	var properties any = struct{}{}
	var required any = []any{}
	if schema := tool.Schema(); len(schema) > 0 {
		object := jsonschema.ObjectSchema(schema)
		properties, _ = object.Get("properties")
		if names, ok := object.Get("required"); ok {
			required = names
		}
	}
	parameters.Set("properties", properties)
	parameters.Set("required", required)
	parameters.Set("additionalProperties", false)

	function := ordered.NewObject()
	function.Set("name", tool.Name())
	function.Set("description", tool.Description())
	function.Set("parameters", parameters)

	definition := ordered.NewObject()
	definition.Set("type", "function")
	definition.Set("function", function)

	return definition
}

// responseFormat asks for JSON matching the schema — never strict, as no
// agent opts into strict structured output.
func responseFormat(schema jsonschema.Properties) *ordered.Object {
	object := jsonschema.ObjectSchema(schema)

	jsonSchema := ordered.NewObject()
	jsonSchema.Set("name", "schema_definition")
	jsonSchema.Set("schema", object.Except("name"))
	jsonSchema.Set("strict", false)

	format := ordered.NewObject()
	format.Set("type", "json_schema")
	format.Set("json_schema", jsonSchema)

	return format
}

// mapMessagesToChat is the history as Chat Completions messages: the
// instructions as the system message, an assistant message without blank
// content, and one `tool` message per tool result.
func mapMessagesToChat(messages []Message, instructions string, encode Encoder) []any {
	chat := []any{}
	if strings.TrimSpace(instructions) != "" {
		chat = append(chat, chatMessage("system", instructions))
	}

	for _, message := range messages {
		switch m := message.(type) {
		case UserMessage:
			chat = append(chat, chatMessage("user", m.Content))
		case AssistantMessage:
			chat = append(chat, assistantChatMessage(m, encode))
		case ToolResultMessage:
			for _, result := range m.Results {
				msg := ordered.NewObject()
				msg.Set("role", "tool")
				msg.Set("tool_call_id", resultID(result.ID, result.ResultID))
				msg.Set("content", result.text(encode))
				chat = append(chat, msg)
			}
		}
	}

	return chat
}

func chatMessage(role string, content string) *ordered.Object {
	msg := ordered.NewObject()
	msg.Set("role", role)
	msg.Set("content", content)

	return msg
}

func assistantChatMessage(m AssistantMessage, encode Encoder) *ordered.Object {
	msg := ordered.NewObject()
	msg.Set("role", "assistant")
	if strings.TrimSpace(m.Content) != "" {
		msg.Set("content", m.Content)
	}
	if len(m.ToolCalls) > 0 {
		calls := make([]any, len(m.ToolCalls))
		for i, call := range m.ToolCalls {
			calls[i] = serializeToolCall(call, encode)
		}
		msg.Set("tool_calls", calls)
	}

	return msg
}

// serializeToolCall is a tool call as the model made it, its arguments
// re-encoded (`{}` when empty).
func serializeToolCall(call ToolCall, encode Encoder) *ordered.Object {
	arguments := encodeArguments(call.Arguments, encode)

	function := ordered.NewObject()
	function.Set("name", call.Name)
	function.Set("arguments", arguments)

	serialized := ordered.NewObject()
	serialized.Set("id", resultID(call.ID, call.ResultID))
	serialized.Set("type", "function")
	serialized.Set("function", function)

	return serialized
}

// resultID is the id the provider pairs a call and its result by: the
// provider's own result id when it sent one.
func resultID(id string, override *string) string {
	if override != nil {
		return *override
	}

	return id
}

// encodeArguments is a call's arguments as a JSON object string: `{}` when
// there are none (an encoder may write an empty object as a list), or when
// they cannot be encoded.
func encodeArguments(arguments *ordered.Object, encode Encoder) string {
	if arguments.Len() == 0 {
		return "{}"
	}

	encoded, err := encode.encode(arguments)
	if err != nil {
		return "{}"
	}

	return string(encoded)
}
