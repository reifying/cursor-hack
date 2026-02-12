package main

import (
	"log/slog"
	"testing"
	"time"
)

func TestParseFlags_DefaultsPrintMode(t *testing.T) {
	cfg := parseFlags([]string{"-p", "hello world"})
	if !cfg.Print {
		t.Error("expected Print=true")
	}
	if cfg.OutputFormat != "stream-json" {
		t.Errorf("OutputFormat = %q, want %q", cfg.OutputFormat, "stream-json")
	}
	if cfg.Log.ConsoleLevel != slog.LevelInfo {
		t.Errorf("ConsoleLevel = %v, want %v", cfg.Log.ConsoleLevel, slog.LevelInfo)
	}
	if cfg.PositionalPrompt != "hello world" {
		t.Errorf("PositionalPrompt = %q, want %q", cfg.PositionalPrompt, "hello world")
	}
}

func TestParseFlags_PrintLongForm(t *testing.T) {
	cfg := parseFlags([]string{"--print", "hello world"})
	if !cfg.Print {
		t.Error("expected Print=true with --print")
	}
	if cfg.OutputFormat != "stream-json" {
		t.Errorf("OutputFormat = %q, want %q", cfg.OutputFormat, "stream-json")
	}
}

func TestParseFlags_DefaultsInteractiveMode(t *testing.T) {
	cfg := parseFlags([]string{})
	if cfg.Print {
		t.Error("expected Print=false")
	}
	if cfg.OutputFormat != "text" {
		t.Errorf("OutputFormat = %q, want %q", cfg.OutputFormat, "text")
	}
	if cfg.Log.ConsoleLevel != slog.LevelWarn {
		t.Errorf("ConsoleLevel = %v, want %v", cfg.Log.ConsoleLevel, slog.LevelWarn)
	}
}

func TestParseFlags_AllFlagsParsed(t *testing.T) {
	cfg := parseFlags([]string{
		"-p",
		"--output-format", "text",
		"--idle-timeout", "120s",
		"--tool-grace", "45s",
		"--tick-interval", "10s",
		"--log-dir", "/tmp/testlogs",
		"--log-level", "debug",
		"--agent-bin", "/usr/local/bin/cursor-agent",
		"--model", "gpt-4",
		"--workspace", "/home/user/project",
		"--force=false",
		"--resume", "sess-existing-123",
		"--prompt-after-hang", "continue",
		"--max-hang-retries", "5",
		"my prompt here",
	})

	if !cfg.Print {
		t.Error("expected Print=true")
	}
	if cfg.OutputFormat != "text" {
		t.Errorf("OutputFormat = %q, want %q", cfg.OutputFormat, "text")
	}
	if cfg.IdleTimeout != 120*time.Second {
		t.Errorf("IdleTimeout = %v, want %v", cfg.IdleTimeout, 120*time.Second)
	}
	if cfg.ToolGrace != 45*time.Second {
		t.Errorf("ToolGrace = %v, want %v", cfg.ToolGrace, 45*time.Second)
	}
	if cfg.TickInterval != 10*time.Second {
		t.Errorf("TickInterval = %v, want %v", cfg.TickInterval, 10*time.Second)
	}
	if cfg.Log.Dir != "/tmp/testlogs" {
		t.Errorf("Log.Dir = %q, want %q", cfg.Log.Dir, "/tmp/testlogs")
	}
	if cfg.Log.ConsoleLevel != slog.LevelDebug {
		t.Errorf("ConsoleLevel = %v, want %v", cfg.Log.ConsoleLevel, slog.LevelDebug)
	}
	if cfg.Log.FileLevel != slog.LevelDebug {
		t.Errorf("FileLevel = %v, want %v", cfg.Log.FileLevel, slog.LevelDebug)
	}
	if cfg.Process.AgentBin != "/usr/local/bin/cursor-agent" {
		t.Errorf("AgentBin = %q, want %q", cfg.Process.AgentBin, "/usr/local/bin/cursor-agent")
	}
	if cfg.Process.Model != "gpt-4" {
		t.Errorf("Model = %q, want %q", cfg.Process.Model, "gpt-4")
	}
	if cfg.Process.Workspace != "/home/user/project" {
		t.Errorf("Workspace = %q, want %q", cfg.Process.Workspace, "/home/user/project")
	}
	if cfg.Process.Force {
		t.Error("expected Force=false")
	}
	if cfg.Process.SessionID != "sess-existing-123" {
		t.Errorf("SessionID = %q, want %q", cfg.Process.SessionID, "sess-existing-123")
	}
	if cfg.PromptAfterHang != "continue" {
		t.Errorf("PromptAfterHang = %q, want %q", cfg.PromptAfterHang, "continue")
	}
	if cfg.MaxHangRetries != 5 {
		t.Errorf("MaxHangRetries = %d, want %d", cfg.MaxHangRetries, 5)
	}
	if cfg.PositionalPrompt != "my prompt here" {
		t.Errorf("PositionalPrompt = %q, want %q", cfg.PositionalPrompt, "my prompt here")
	}
}

func TestParseFlags_SeparatorSplitsFlags(t *testing.T) {
	cfg := parseFlags([]string{
		"-p",
		"--idle-timeout", "30s",
		"--", "--extra-flag", "value",
		"the prompt",
	})

	if !cfg.Print {
		t.Error("expected Print=true")
	}
	if cfg.IdleTimeout != 30*time.Second {
		t.Errorf("IdleTimeout = %v, want %v", cfg.IdleTimeout, 30*time.Second)
	}
	if len(cfg.Process.ExtraFlags) != 3 {
		t.Fatalf("ExtraFlags len = %d, want 3", len(cfg.Process.ExtraFlags))
	}
	if cfg.Process.ExtraFlags[0] != "--extra-flag" {
		t.Errorf("ExtraFlags[0] = %q, want %q", cfg.Process.ExtraFlags[0], "--extra-flag")
	}
	if cfg.Process.ExtraFlags[1] != "value" {
		t.Errorf("ExtraFlags[1] = %q, want %q", cfg.Process.ExtraFlags[1], "value")
	}
	if cfg.Process.ExtraFlags[2] != "the prompt" {
		t.Errorf("ExtraFlags[2] = %q, want %q", cfg.Process.ExtraFlags[2], "the prompt")
	}
	// With --, positional prompt should be empty since everything after -- goes to ExtraFlags.
	if cfg.PositionalPrompt != "" {
		t.Errorf("PositionalPrompt = %q, want empty", cfg.PositionalPrompt)
	}
}

func TestParseFlags_PositionalPromptJoinsMultipleWords(t *testing.T) {
	cfg := parseFlags([]string{"-p", "hello", "world", "from", "test"})
	if cfg.PositionalPrompt != "hello world from test" {
		t.Errorf("PositionalPrompt = %q, want %q", cfg.PositionalPrompt, "hello world from test")
	}
}

func TestParseFlags_NoSeparator_NoExtraFlags(t *testing.T) {
	cfg := parseFlags([]string{"-p", "prompt text"})
	if cfg.Process.ExtraFlags != nil {
		t.Errorf("ExtraFlags = %v, want nil", cfg.Process.ExtraFlags)
	}
}

func TestParseFlags_DefaultHangDetection(t *testing.T) {
	cfg := parseFlags([]string{})
	if cfg.IdleTimeout != 60*time.Second {
		t.Errorf("IdleTimeout = %v, want %v", cfg.IdleTimeout, 60*time.Second)
	}
	if cfg.ToolGrace != 30*time.Second {
		t.Errorf("ToolGrace = %v, want %v", cfg.ToolGrace, 30*time.Second)
	}
	if cfg.TickInterval != 5*time.Second {
		t.Errorf("TickInterval = %v, want %v", cfg.TickInterval, 5*time.Second)
	}
}

func TestParseFlags_DefaultForce(t *testing.T) {
	cfg := parseFlags([]string{})
	if !cfg.Process.Force {
		t.Error("expected Force=true by default")
	}
}

func TestParseFlags_OutputFormatExplicitOverridesDefault(t *testing.T) {
	// In -p mode, default is stream-json. Explicit --output-format text overrides.
	cfg := parseFlags([]string{"-p", "--output-format", "text", "prompt"})
	if cfg.OutputFormat != "text" {
		t.Errorf("OutputFormat = %q, want %q", cfg.OutputFormat, "text")
	}

	// In interactive mode, default is text. Explicit --output-format stream-json overrides.
	cfg = parseFlags([]string{"--output-format", "stream-json"})
	if cfg.OutputFormat != "stream-json" {
		t.Errorf("OutputFormat = %q, want %q", cfg.OutputFormat, "stream-json")
	}
}

func TestParseFlags_LogLevelExplicitOverridesDefault(t *testing.T) {
	// In -p mode, default is info. Explicit --log-level error overrides.
	cfg := parseFlags([]string{"-p", "--log-level", "error", "prompt"})
	if cfg.Log.ConsoleLevel != slog.LevelError {
		t.Errorf("ConsoleLevel = %v, want %v", cfg.Log.ConsoleLevel, slog.LevelError)
	}

	// In interactive mode, default is warn. Explicit --log-level debug overrides.
	cfg = parseFlags([]string{"--log-level", "debug"})
	if cfg.Log.ConsoleLevel != slog.LevelDebug {
		t.Errorf("ConsoleLevel = %v, want %v", cfg.Log.ConsoleLevel, slog.LevelDebug)
	}
}

func TestParseFlags_PromptReaderInitialized(t *testing.T) {
	cfg := parseFlags([]string{})
	if cfg.PromptReader == nil {
		t.Error("PromptReader should be initialized")
	}
}

func TestParseFlags_LogDirDefault(t *testing.T) {
	cfg := parseFlags([]string{})
	if cfg.Log.Dir == "" {
		t.Error("Log.Dir should have a default value")
	}
}

func TestParseFlags_PromptAfterHang(t *testing.T) {
	cfg := parseFlags([]string{"--prompt-after-hang", "continue", "hello"})
	if cfg.PromptAfterHang != "continue" {
		t.Errorf("PromptAfterHang = %q, want %q", cfg.PromptAfterHang, "continue")
	}
}

func TestParseFlags_PromptAfterHang_Empty(t *testing.T) {
	cfg := parseFlags([]string{"-p", "hello"})
	if cfg.PromptAfterHang != "" {
		t.Errorf("PromptAfterHang = %q, want empty", cfg.PromptAfterHang)
	}
}

func TestParseFlags_MaxHangRetries(t *testing.T) {
	cfg := parseFlags([]string{"--max-hang-retries", "10", "hello"})
	if cfg.MaxHangRetries != 10 {
		t.Errorf("MaxHangRetries = %d, want %d", cfg.MaxHangRetries, 10)
	}
}

func TestParseFlags_MaxHangRetries_Default(t *testing.T) {
	cfg := parseFlags([]string{})
	if cfg.MaxHangRetries != 3 {
		t.Errorf("MaxHangRetries = %d, want %d", cfg.MaxHangRetries, 3)
	}
}

func TestParseFlags_ResumeFlag(t *testing.T) {
	cfg := parseFlags([]string{"-p", "--resume", "sess-abc-123", "hello"})
	if cfg.Process.SessionID != "sess-abc-123" {
		t.Errorf("Process.SessionID = %q, want %q", cfg.Process.SessionID, "sess-abc-123")
	}
}

func TestParseFlags_ResumeFlag_Empty(t *testing.T) {
	cfg := parseFlags([]string{"-p", "hello"})
	if cfg.Process.SessionID != "" {
		t.Errorf("Process.SessionID = %q, want empty", cfg.Process.SessionID)
	}
}

// --- splitAtSeparator tests ---

func TestSplitAtSeparator_NoSeparator(t *testing.T) {
	before, after := splitAtSeparator([]string{"-p", "hello"})
	if len(before) != 2 || before[0] != "-p" || before[1] != "hello" {
		t.Errorf("before = %v, want [-p hello]", before)
	}
	if after != nil {
		t.Errorf("after = %v, want nil", after)
	}
}

func TestSplitAtSeparator_WithSeparator(t *testing.T) {
	before, after := splitAtSeparator([]string{"-p", "--", "--extra", "val"})
	if len(before) != 1 || before[0] != "-p" {
		t.Errorf("before = %v, want [-p]", before)
	}
	if len(after) != 2 || after[0] != "--extra" || after[1] != "val" {
		t.Errorf("after = %v, want [--extra val]", after)
	}
}

func TestSplitAtSeparator_SeparatorAtStart(t *testing.T) {
	before, after := splitAtSeparator([]string{"--", "all", "extra"})
	if len(before) != 0 {
		t.Errorf("before = %v, want []", before)
	}
	if len(after) != 2 || after[0] != "all" || after[1] != "extra" {
		t.Errorf("after = %v, want [all extra]", after)
	}
}

func TestSplitAtSeparator_SeparatorAtEnd(t *testing.T) {
	before, after := splitAtSeparator([]string{"-p", "--"})
	if len(before) != 1 || before[0] != "-p" {
		t.Errorf("before = %v, want [-p]", before)
	}
	if len(after) != 0 {
		t.Errorf("after = %v, want []", after)
	}
}

func TestSplitAtSeparator_Empty(t *testing.T) {
	before, after := splitAtSeparator([]string{})
	if len(before) != 0 {
		t.Errorf("before = %v, want []", before)
	}
	if after != nil {
		t.Errorf("after = %v, want nil", after)
	}
}

// --- parseLogLevel tests ---

func TestParseLogLevel(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  slog.Level
	}{
		{name: "debug", input: "debug", want: slog.LevelDebug},
		{name: "info", input: "info", want: slog.LevelInfo},
		{name: "warn", input: "warn", want: slog.LevelWarn},
		{name: "warning", input: "warning", want: slog.LevelWarn},
		{name: "error", input: "error", want: slog.LevelError},
		{name: "DEBUG uppercase", input: "DEBUG", want: slog.LevelDebug},
		{name: "Info mixed case", input: "Info", want: slog.LevelInfo},
		{name: "unknown defaults to info", input: "unknown", want: slog.LevelInfo},
		{name: "empty defaults to info", input: "", want: slog.LevelInfo},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseLogLevel(tt.input)
			if got != tt.want {
				t.Errorf("parseLogLevel(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
