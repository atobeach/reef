package ai

import (
	"context"
	"fmt"

	"github.com/atobeach/reef/jsonschema"
)

// StructuredOutput is embedded by an agent whose answer decodes into T: it
// derives the schema from T and lets PromptStructured decode only T:
//
//	type ExtractTips struct{ ai.StructuredOutput[Tips] }
//
//	tips, err := ai.PromptStructured(ctx, manager, ExtractTips{}, text) // a Tips
type StructuredOutput[T any] struct{}

var _ HasStructuredOutput = StructuredOutput[struct{}]{}

// Schema implements HasStructuredOutput.
func (StructuredOutput[T]) Schema() jsonschema.Properties {
	return jsonschema.For[T]()
}

// answersWith binds the embedding agent to T, so PromptStructured infers T
// from the agent alone.
func (StructuredOutput[T]) answersWith(T) {} //nolint:unused // called by no one; it exists to satisfy StructuredAgent[T]

// StructuredAgent is an agent that embeds StructuredOutput[T].
type StructuredAgent[T any] interface {
	Agent
	HasStructuredOutput
	answersWith(T)
}

// PromptStructured runs agent without streaming and decodes its answer into
// T with encoding/json. A field the model left out reads as T's zero value;
// one of the wrong type fails the decode.
func PromptStructured[T any](ctx context.Context, m *Manager, agent StructuredAgent[T], prompt string) (T, error) {
	var output T

	response, err := m.Prompt(ctx, agent, prompt)
	if err != nil {
		return output, fmt.Errorf("prompting %s: %w", ClassBasename(agent.Class()), err)
	}

	if err := response.Decode(&output); err != nil {
		return output, fmt.Errorf("prompting %s: %w", ClassBasename(agent.Class()), err)
	}

	return output, nil
}
