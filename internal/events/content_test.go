package events

import (
	"encoding/json"
	"testing"
)

func TestParseAssistantMessage_MidTurn(t *testing.T) {
	data := loadFixture(t, "assistant_mid_turn.json")
	msg, err := ParseAssistantMessage(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantText := "I'll run `sleep 5` in bash, wait for it to finish, then run `sleep 3` in bash."
	if msg.Text != wantText {
		t.Errorf("text = %q, want %q", msg.Text, wantText)
	}
	if msg.IsFinal {
		t.Error("expected IsFinal to be false for mid-turn message")
	}
	if msg.ModelCallID == "" {
		t.Error("expected non-empty ModelCallID for mid-turn message")
	}
}

func TestParseAssistantMessage_Final(t *testing.T) {
	data := loadFixture(t, "assistant_final.json")
	msg, err := ParseAssistantMessage(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantText := "Both commands have finished running: first `sleep 5`, then `sleep 3`, with no other actions taken."
	if msg.Text != wantText {
		t.Errorf("text = %q, want %q", msg.Text, wantText)
	}
	if !msg.IsFinal {
		t.Error("expected IsFinal to be true for final message")
	}
	if msg.ModelCallID != "" {
		t.Errorf("expected empty ModelCallID for final message, got %q", msg.ModelCallID)
	}
}

func TestParseAssistantMessage_InvalidJSON(t *testing.T) {
	_, err := ParseAssistantMessage([]byte(`{not json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestParseAssistantMessage_EmptyContent(t *testing.T) {
	input := `{"type":"assistant","message":{"role":"assistant","content":[]}}`
	_, err := ParseAssistantMessage([]byte(input))
	if err == nil {
		t.Fatal("expected error for empty content")
	}
}

func TestParseToolCallInfo_ShellTool(t *testing.T) {
	data := loadFixture(t, "tool_call_started.json")
	var started ToolCallStarted
	if err := json.Unmarshal(data, &started); err != nil {
		t.Fatalf("unmarshal started: %v", err)
	}

	info, err := ParseToolCallInfo(started.ToolCall)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.ToolType != "shellToolCall" {
		t.Errorf("tool type = %q, want shellToolCall", info.ToolType)
	}
	if info.Command != "sleep 5" {
		t.Errorf("command = %q, want %q", info.Command, "sleep 5")
	}
	if info.TimeoutMS != 10000 {
		t.Errorf("timeout = %d, want %d", info.TimeoutMS, 10000)
	}
}

func TestParseToolCallInfo_LsTool(t *testing.T) {
	toolCall := json.RawMessage(`{"lsToolCall":{"args":{"path":"/some/path","ignore":[],"toolCallId":"call_xxx"}}}`)
	info, err := ParseToolCallInfo(toolCall)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.ToolType != "lsToolCall" {
		t.Errorf("tool type = %q, want lsToolCall", info.ToolType)
	}
	if info.Path != "/some/path" {
		t.Errorf("path = %q, want /some/path", info.Path)
	}
}

func TestParseToolCallInfo_UnknownTool(t *testing.T) {
	toolCall := json.RawMessage(`{"grepToolCall":{"args":{"pattern":"foo"}}}`)
	info, err := ParseToolCallInfo(toolCall)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.ToolType != "grepToolCall" {
		t.Errorf("tool type = %q, want grepToolCall", info.ToolType)
	}
}

func TestParseToolCallInfo_InvalidJSON(t *testing.T) {
	_, err := ParseToolCallInfo(json.RawMessage(`{not json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestParseToolCallInfo_EmptyObject(t *testing.T) {
	_, err := ParseToolCallInfo(json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error for empty tool_call object")
	}
}

func TestParseShellToolResult(t *testing.T) {
	data := loadFixture(t, "tool_call_completed.json")
	var completed ToolCallCompleted
	if err := json.Unmarshal(data, &completed); err != nil {
		t.Fatalf("unmarshal completed: %v", err)
	}

	result, err := ParseShellToolResult(completed.ToolCall)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", result.ExitCode)
	}
	if result.ExecutionTime != 5399 {
		t.Errorf("execution time = %d, want 5399", result.ExecutionTime)
	}
	if result.Stdout != "" {
		t.Errorf("stdout = %q, want empty", result.Stdout)
	}
	if result.Stderr != "" {
		t.Errorf("stderr = %q, want empty", result.Stderr)
	}
}

func TestParseShellToolResult_NonShellTool(t *testing.T) {
	toolCall := json.RawMessage(`{"lsToolCall":{"args":{"path":"/some/path"}}}`)
	_, err := ParseShellToolResult(toolCall)
	if err == nil {
		t.Fatal("expected error for non-shell tool call")
	}
}

func TestParseShellToolResult_InvalidJSON(t *testing.T) {
	_, err := ParseShellToolResult(json.RawMessage(`{not json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestParseShellToolResult_WithOutput(t *testing.T) {
	toolCall := json.RawMessage(`{"shellToolCall":{"args":{"command":"echo hello"},"result":{"success":{"command":"echo hello","workingDirectory":"","exitCode":0,"signal":"","stdout":"hello\n","stderr":"","executionTime":50,"interleavedOutput":""},"isBackground":false}}}`)
	result, err := ParseShellToolResult(toolCall)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", result.ExitCode)
	}
	if result.Stdout != "hello\n" {
		t.Errorf("stdout = %q, want %q", result.Stdout, "hello\n")
	}
	if result.ExecutionTime != 50 {
		t.Errorf("execution time = %d, want 50", result.ExecutionTime)
	}
}

func TestParseShellToolResult_NonZeroExit(t *testing.T) {
	toolCall := json.RawMessage(`{"shellToolCall":{"args":{"command":"false"},"result":{"success":{"command":"false","workingDirectory":"","exitCode":1,"signal":"","stdout":"","stderr":"error msg","executionTime":10,"interleavedOutput":""},"isBackground":false}}}`)
	result, err := ParseShellToolResult(toolCall)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ExitCode != 1 {
		t.Errorf("exit code = %d, want 1", result.ExitCode)
	}
	if result.Stderr != "error msg" {
		t.Errorf("stderr = %q, want %q", result.Stderr, "error msg")
	}
}
