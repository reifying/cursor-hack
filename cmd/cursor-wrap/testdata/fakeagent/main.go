// fakeagent is a test helper that simulates cursor-agent output.
// It reads its scenario from the FAKE_AGENT_SCENARIO environment variable
// and emits synthetic JSONL to stdout.
//
// It logs args to stderr for test verification (e.g. --resume detection).
package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

func main() {
	// Read prompt from stdin (cursor-agent behavior: reads to EOF).
	prompt, _ := io.ReadAll(os.Stdin)

	// Log args to stderr for test verification.
	fmt.Fprintf(os.Stderr, "fake-agent args: %s\n", strings.Join(os.Args[1:], " "))
	fmt.Fprintf(os.Stderr, "fake-agent prompt: %s\n", string(prompt))

	scenario := os.Getenv("FAKE_AGENT_SCENARIO")

	// For multi-turn scenarios, detect if this is a resumed invocation.
	isResume := false
	for _, arg := range os.Args[1:] {
		if arg == "--resume" {
			isResume = true
			break
		}
	}

	switch scenario {
	case "normal":
		emitNormal()
	case "idle_hang":
		emitIdleHang()
	case "tool_timeout_hang":
		emitToolTimeoutHang()
	case "with_tool":
		emitWithTool()
	case "multi_turn":
		if isResume {
			emitNormal() // Second turn: normal completion
		} else {
			emitNormal() // First turn: normal completion
		}
	case "hang_then_normal":
		if isResume {
			emitNormal() // Second turn: completes normally
		} else {
			emitIdleHang() // First turn: hangs
		}
	case "slow_normal":
		emitSlowNormal()
	default:
		fmt.Fprintf(os.Stderr, "unknown scenario: %s\n", scenario)
		os.Exit(1)
	}
}

// emitNormal outputs a complete event sequence including a tool call and exits.
// Matches the task spec: system/init → user → thinking → assistant →
// tool_call/started → tool_call/completed → assistant(final) → result.
func emitNormal() {
	lines := []string{
		`{"type":"system","subtype":"init","session_id":"test-session-id","model":"test-model","cwd":"/tmp","permissionMode":"auto"}`,
		`{"type":"user","message":{"content":[{"type":"text","text":"test prompt"}]}}`,
		`{"type":"thinking","subtype":"delta","text":"Let me think about this."}`,
		`{"type":"thinking","subtype":"completed"}`,
		`{"type":"assistant","model_call_id":"mc_1","message":{"content":[{"type":"text","text":"Here is my response."}]}}`,
		`{"type":"tool_call","subtype":"started","call_id":"call_1","model_call_id":"mc_1","timestamp_ms":1000,"tool_call":{"shellToolCall":{"args":{"command":"echo test","timeout":120000}}}}`,
		`{"type":"tool_call","subtype":"completed","call_id":"call_1","model_call_id":"mc_1","timestamp_ms":1100,"tool_call":{"shellToolCall":{"args":{"command":"echo test","timeout":120000},"result":{"success":{"exitCode":0,"stdout":"test\n","stderr":"","executionTime":100}}}}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Final answer."}]}}`,
		`{"type":"result","subtype":"success","duration_ms":1000,"is_error":false,"session_id":"test-session-id","request_id":"req_1"}`,
	}
	for _, line := range lines {
		fmt.Println(line)
	}
}

// emitIdleHang outputs a few events then goes silent (hangs).
func emitIdleHang() {
	lines := []string{
		`{"type":"system","subtype":"init","session_id":"test-session-id","model":"test-model","cwd":"/tmp","permissionMode":"auto"}`,
		`{"type":"user","message":{"content":[{"type":"text","text":"test prompt"}]}}`,
		`{"type":"thinking","subtype":"delta","text":"Let me think about this."}`,
		`{"type":"thinking","subtype":"completed"}`,
	}
	for _, line := range lines {
		fmt.Println(line)
	}
	// Hang — the wrapper should detect idle timeout and kill us.
	// Use a long sleep instead of select{} to avoid Go's deadlock detector.
	time.Sleep(10 * time.Minute)
}

// emitToolTimeoutHang emits a tool_call/started with a short timeout, then hangs.
func emitToolTimeoutHang() {
	lines := []string{
		`{"type":"system","subtype":"init","session_id":"test-session-id","model":"test-model","cwd":"/tmp","permissionMode":"auto"}`,
		`{"type":"user","message":{"content":[{"type":"text","text":"test prompt"}]}}`,
		`{"type":"thinking","subtype":"delta","text":"I'll run a command."}`,
		`{"type":"thinking","subtype":"completed"}`,
		`{"type":"assistant","model_call_id":"mc_1","message":{"content":[{"type":"text","text":"Running command."}]}}`,
		`{"type":"tool_call","subtype":"started","call_id":"call_1","model_call_id":"mc_1","timestamp_ms":1000,"tool_call":{"shellToolCall":{"args":{"command":"sleep 999","timeout":1000}}}}`,
	}
	for _, line := range lines {
		fmt.Println(line)
	}
	// Hang — tool timeout (1000ms) + grace (1s) should trigger.
	time.Sleep(10 * time.Minute)
}

// emitWithTool outputs a sequence with a tool call for text format testing.
func emitWithTool() {
	lines := []string{
		`{"type":"system","subtype":"init","session_id":"test-session-id","model":"test-model","cwd":"/tmp","permissionMode":"auto"}`,
		`{"type":"user","message":{"content":[{"type":"text","text":"test prompt"}]}}`,
		`{"type":"thinking","subtype":"delta","text":"I'll help with that."}`,
		`{"type":"thinking","subtype":"completed"}`,
		`{"type":"assistant","model_call_id":"mc_1","message":{"content":[{"type":"text","text":"I'll run a command for you."}]}}`,
		`{"type":"tool_call","subtype":"started","call_id":"call_1","model_call_id":"mc_1","timestamp_ms":1000,"tool_call":{"shellToolCall":{"args":{"command":"echo hello","timeout":120000}}}}`,
		`{"type":"tool_call","subtype":"completed","call_id":"call_1","model_call_id":"mc_1","timestamp_ms":1200,"tool_call":{"shellToolCall":{"args":{"command":"echo hello","timeout":120000},"result":{"success":{"exitCode":0,"stdout":"hello\n","stderr":"","executionTime":200}}}}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"The command completed successfully."}]}}`,
		`{"type":"result","subtype":"success","duration_ms":2000,"is_error":false,"session_id":"test-session-id","request_id":"req_1"}`,
	}
	for _, line := range lines {
		fmt.Println(line)
	}
}

// emitSlowNormal outputs events with delays to give time for signal testing.
func emitSlowNormal() {
	fmt.Println(`{"type":"system","subtype":"init","session_id":"test-session-id","model":"test-model","cwd":"/tmp","permissionMode":"auto"}`)
	time.Sleep(200 * time.Millisecond)
	fmt.Println(`{"type":"user","message":{"content":[{"type":"text","text":"test prompt"}]}}`)
	time.Sleep(200 * time.Millisecond)
	fmt.Println(`{"type":"thinking","subtype":"delta","text":"Let me think about this."}`)
	// Sleep long enough for the test to send a signal.
	time.Sleep(30 * time.Second)
	fmt.Println(`{"type":"result","subtype":"success","duration_ms":5000,"is_error":false,"session_id":"test-session-id","request_id":"req_1"}`)
}
