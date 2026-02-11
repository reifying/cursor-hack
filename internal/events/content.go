package events

import (
	"encoding/json"
	"fmt"
)

// AssistantMessage extracts the text content from an "assistant" event.
type AssistantMessage struct {
	Text        string // extracted from message.content[0].text
	ModelCallID string // present for mid-turn, absent for final
	IsFinal     bool   // true when model_call_id is absent (final response)
}

// ThinkingDelta extracts the token text from a "thinking"/"delta" event.
type ThinkingDelta struct {
	Text string `json:"text"`
}

// ToolCallInfo extracts tool type and key arguments for display.
// Parsed from the tool_call field of started/completed events.
type ToolCallInfo struct {
	ToolType string // key name: "shellToolCall", "lsToolCall", etc.
	// Shell-specific fields (populated when ToolType == "shellToolCall"):
	Command   string
	TimeoutMS int64
	// LS-specific fields (populated when ToolType == "lsToolCall"):
	Path string
}

// ShellToolResult extracts result fields from a completed shellToolCall.
type ShellToolResult struct {
	ExitCode      int    `json:"exitCode"`
	Stdout        string `json:"stdout"`
	Stderr        string `json:"stderr"`
	ExecutionTime int64  `json:"executionTime"` // ms
}

// ParseAssistantMessage extracts text from an assistant event's raw JSON.
func ParseAssistantMessage(raw []byte) (AssistantMessage, error) {
	// Intermediate structs to navigate the nested JSON.
	var envelope struct {
		Message struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
		ModelCallID json.RawMessage `json:"model_call_id,omitempty"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return AssistantMessage{}, fmt.Errorf("unmarshal assistant event: %w", err)
	}
	if len(envelope.Message.Content) == 0 {
		return AssistantMessage{}, fmt.Errorf("assistant event has no content")
	}

	var modelCallID string
	// model_call_id is absent (null or missing) for final assistant messages.
	if len(envelope.ModelCallID) > 0 && string(envelope.ModelCallID) != "null" {
		if err := json.Unmarshal(envelope.ModelCallID, &modelCallID); err != nil {
			return AssistantMessage{}, fmt.Errorf("unmarshal model_call_id: %w", err)
		}
	}

	return AssistantMessage{
		Text:        envelope.Message.Content[0].Text,
		ModelCallID: modelCallID,
		IsFinal:     modelCallID == "",
	}, nil
}

// ParseToolCallInfo extracts tool type and display-relevant args from
// the tool_call field of a started or completed event.
func ParseToolCallInfo(toolCallJSON json.RawMessage) (ToolCallInfo, error) {
	// The tool_call field is an object with a single key identifying the tool type.
	// e.g. {"shellToolCall": {"args": {...}}} or {"lsToolCall": {"args": {...}}}
	var toolCallMap map[string]json.RawMessage
	if err := json.Unmarshal(toolCallJSON, &toolCallMap); err != nil {
		return ToolCallInfo{}, fmt.Errorf("unmarshal tool_call object: %w", err)
	}

	// Find the single key.
	var toolType string
	var toolData json.RawMessage
	for k, v := range toolCallMap {
		toolType = k
		toolData = v
		break
	}
	if toolType == "" {
		return ToolCallInfo{}, fmt.Errorf("tool_call object has no keys")
	}

	info := ToolCallInfo{ToolType: toolType}

	switch toolType {
	case "shellToolCall":
		var shell struct {
			Args ShellToolArgs `json:"args"`
		}
		if err := json.Unmarshal(toolData, &shell); err != nil {
			return info, fmt.Errorf("unmarshal shellToolCall: %w", err)
		}
		info.Command = shell.Args.Command
		info.TimeoutMS = shell.Args.Timeout
	case "lsToolCall":
		var ls struct {
			Args struct {
				Path string `json:"path"`
			} `json:"args"`
		}
		if err := json.Unmarshal(toolData, &ls); err != nil {
			return info, fmt.Errorf("unmarshal lsToolCall: %w", err)
		}
		info.Path = ls.Args.Path
	}

	return info, nil
}

// ParseShellToolResult extracts the result from a completed shellToolCall.
func ParseShellToolResult(toolCallJSON json.RawMessage) (ShellToolResult, error) {
	var toolCallMap map[string]json.RawMessage
	if err := json.Unmarshal(toolCallJSON, &toolCallMap); err != nil {
		return ShellToolResult{}, fmt.Errorf("unmarshal tool_call object: %w", err)
	}

	shellData, ok := toolCallMap["shellToolCall"]
	if !ok {
		return ShellToolResult{}, fmt.Errorf("tool_call is not a shellToolCall")
	}

	var shell struct {
		Result struct {
			Success ShellToolResult `json:"success"`
		} `json:"result"`
	}
	if err := json.Unmarshal(shellData, &shell); err != nil {
		return ShellToolResult{}, fmt.Errorf("unmarshal shellToolCall result: %w", err)
	}

	return shell.Result.Success, nil
}
