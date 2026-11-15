package ai

import (
	"context"
	"strings"
)

// stepToolResults runs the step's tool calls against the tools it was sent
// with, or settles those that can't run: on the final step, an unknown tool
// under repair, or a limiter's refusal. An unknown tool without repair fails
// the run.
func (r loopRequest) stepToolResults(ctx context.Context, result *StepResponse, prepared PendingStep) ([]ToolResult, error) {
	if result.FinishReason != FinishToolCalls || len(result.ToolCalls) == 0 {
		return nil, nil
	}

	repairs := prepared.repairsToolCalls()

	tools := make([]Tool, len(result.ToolCalls))
	var runnable []ToolCall
	for i, call := range result.ToolCalls {
		tool := findTool(call.Name, prepared.Tools)
		if tool == nil && !prepared.IsFinalStep && !repairs {
			return nil, &NoSuchToolError{Name: call.Name}
		}
		tools[i] = tool
		if tool != nil && !prepared.IsFinalStep {
			runnable = append(runnable, call)
		}
	}

	refusals := r.toolCallRefusals(prepared, runnable)

	results := make([]ToolResult, 0, len(result.ToolCalls))
	for i, call := range result.ToolCalls {
		refusal := ""
		if tools[i] != nil && !prepared.IsFinalStep {
			refusal, refusals = refusals[0], refusals[1:]
		}

		toolResult, err := r.answerToolCall(ctx, prepared, call, tools[i], refusal)
		if err != nil {
			return nil, err
		}
		r.Run.recordToolResult(toolResult)
		results = append(results, toolResult)
	}

	return results, nil
}

// answerToolCall is the result for one call: settled without running when
// its tool is missing, the step is final or a limiter refused it; the
// tool's own output otherwise.
func (r loopRequest) answerToolCall(ctx context.Context, prepared PendingStep, call ToolCall, tool Tool, refusal string) (ToolResult, error) {
	result := ToolResult{
		ID:        call.ID,
		Name:      call.Name,
		Arguments: call.Arguments,
		ResultID:  call.ResultID,
		Failed:    true,
	}

	switch {
	case tool == nil && prepared.repairsToolCalls():
		result.Result = "Tool '" + call.Name + "' does not exist. Available tools: " + availableToolNames(prepared.Tools) + "."
	case prepared.IsFinalStep:
		result.Result = "The agent reached its maximum number of steps without running this tool call."
	case refusal != "":
		result.Result = refusal
	default:
		output, err := r.Run.executeTool(ctx, tool, call)
		if err != nil {
			return ToolResult{}, err
		}
		result.Result, result.Failed = output, false
	}

	return result, nil
}

// toolCallRefusals asks the agent's ToolCallLimiter middleware about the
// calls about to run: one entry per call, "" for a call that runs. The
// first limiter to refuse a call wins.
func (r loopRequest) toolCallRefusals(prepared PendingStep, calls []ToolCall) []string {
	refusals := make([]string, len(calls))

	for _, middleware := range agentMiddleware(r.Options.Agent) {
		limiter, ok := middleware.(ToolCallLimiter)
		if !ok {
			continue
		}

		verdicts := limiter.LimitToolCalls(prepared, calls)
		for i := range refusals {
			if refusals[i] == "" && i < len(verdicts) {
				refusals[i] = verdicts[i]
			}
		}
	}

	return refusals
}

func (s PendingStep) repairsToolCalls() bool {
	return s.Options != nil && agentRepairs(s.Options.Agent)
}

func findTool(name string, tools []Tool) Tool {
	for _, tool := range tools {
		if tool.Name() == name {
			return tool
		}
	}

	return nil
}

func availableToolNames(tools []Tool) string {
	if len(tools) == 0 {
		return "none"
	}

	names := make([]string, len(tools))
	for i, tool := range tools {
		names[i] = tool.Name()
	}

	return strings.Join(names, ", ")
}

// settleAbandonedToolCalls answers tool calls an earlier paused (approval)
// turn left unanswered, so the history stays replayable.
func settleAbandonedToolCalls(messages []Message) []Message {
	resolved := map[string]bool{}
	for _, message := range messages {
		if answer, ok := message.(ToolResultMessage); ok {
			for _, result := range answer.Results {
				resolved[result.ID] = true
			}
		}
	}

	out := make([]Message, 0, len(messages))
	for i := 0; i < len(messages); i++ {
		message := messages[i]
		out = append(out, message)

		assistant, ok := message.(AssistantMessage)
		if !ok {
			continue
		}

		var placeholders []ToolResult
		for _, call := range assistant.ToolCalls {
			if !resolved[call.ID] {
				placeholders = append(placeholders, ToolResult{
					ID: call.ID, Name: call.Name, Arguments: call.Arguments,
					Result:   "This tool call was not executed because it was not approved before the conversation continued.",
					ResultID: call.ResultID,
					Denied:   true,
				})
			}
		}

		if len(placeholders) == 0 {
			continue
		}

		// Merge into the answer that follows, if any, so one assistant
		// message is answered by one tool message.
		if i+1 < len(messages) {
			if next, ok := messages[i+1].(ToolResultMessage); ok {
				out = append(out, ToolResultMessage{Results: append(append([]ToolResult{}, next.Results...), placeholders...)})
				i++

				continue
			}
		}

		out = append(out, ToolResultMessage{Results: placeholders})
	}

	return out
}
