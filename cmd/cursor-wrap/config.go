package main

import (
	"bufio"
	"flag"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"cursor-wrap/internal/logger"
	"cursor-wrap/internal/process"
)

// Config holds all configuration for the wrapper.
type Config struct {
	// Mode
	Print        bool   // -p: non-interactive, single prompt
	OutputFormat string // "stream-json" or "text"

	// Hang detection
	IdleTimeout  time.Duration
	ToolGrace    time.Duration
	TickInterval time.Duration

	// Logging
	Log logger.LogConfig

	// Process
	Process process.Config

	// Prompt input
	PositionalPrompt string        // trailing arg, if any
	PromptReader     *bufio.Reader // wraps os.Stdin
}

// parseFlags uses the stdlib flag package to parse CLI flags and trailing
// args into a Config. Everything after "--" is captured as ExtraFlags
// for pass-through to cursor-agent. The last non-flag argument (if any)
// is treated as the positional prompt. Mode-dependent defaults (output
// format, console log level) are applied after flag parsing based on
// whether -p was set.
func parseFlags(args []string) Config {
	fs := flag.NewFlagSet("cursor-wrap", flag.ExitOnError)

	// Mode flags — register both -p and --print pointing to the same variable.
	var printMode bool
	fs.BoolVar(&printMode, "p", false, "Non-interactive mode: single prompt, exit after")
	fs.BoolVar(&printMode, "print", false, "Non-interactive mode: single prompt, exit after")
	outputFormat := fs.String("output-format", "", "Output format: stream-json | text")

	// Hang detection flags
	idleTimeout := fs.Duration("idle-timeout", 60*time.Second, "Max silence with no open tool calls")
	toolGrace := fs.Duration("tool-grace", 30*time.Second, "Extra time beyond a tool's declared timeout")
	tickInterval := fs.Duration("tick-interval", 5*time.Second, "How often to check for hangs")

	// Logging flags
	logDir := fs.String("log-dir", "", "Directory for session log files")
	logLevel := fs.String("log-level", "", "Console log level: debug|info|warn|error")

	// Process flags
	agentBin := fs.String("agent-bin", "", "Path to cursor-agent binary")
	model := fs.String("model", "", "Model to pass to cursor-agent")
	workspace := fs.String("workspace", "", "Workspace directory for cursor-agent")
	force := fs.Bool("force", true, "Pass --force to cursor-agent")

	// Split args at "--" separator before parsing. Everything after "--"
	// goes to cursor-agent as ExtraFlags.
	wrapperArgs, extraFlags := splitAtSeparator(args)

	fs.Parse(wrapperArgs)

	// Remaining args after flag parsing: the positional prompt.
	remaining := fs.Args()
	var positionalPrompt string
	if len(remaining) > 0 {
		positionalPrompt = strings.Join(remaining, " ")
	}

	// Resolve agent-bin default.
	agentBinResolved := *agentBin
	if agentBinResolved == "" {
		if p, err := exec.LookPath("cursor-agent"); err == nil {
			agentBinResolved = p
		} else {
			agentBinResolved = "cursor-agent"
		}
	}

	// Resolve log-dir default.
	logDirResolved := *logDir
	if logDirResolved == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "."
		}
		logDirResolved = filepath.Join(home, ".cursor-wrap", "logs")
	}

	// Apply mode-dependent defaults.
	resolvedOutputFormat := *outputFormat
	if resolvedOutputFormat == "" {
		if printMode {
			resolvedOutputFormat = "stream-json"
		} else {
			resolvedOutputFormat = "text"
		}
	}

	resolvedConsoleLevel := parseLogLevel(*logLevel)
	if *logLevel == "" {
		if printMode {
			resolvedConsoleLevel = slog.LevelInfo
		} else {
			resolvedConsoleLevel = slog.LevelWarn
		}
	}

	return Config{
		Print:        printMode,
		OutputFormat: resolvedOutputFormat,
		IdleTimeout:  *idleTimeout,
		ToolGrace:    *toolGrace,
		TickInterval: *tickInterval,
		Log: logger.LogConfig{
			Dir:          logDirResolved,
			ConsoleLevel: resolvedConsoleLevel,
			FileLevel:    slog.LevelDebug,
		},
		Process: process.Config{
			AgentBin:   agentBinResolved,
			Model:      *model,
			Workspace:  *workspace,
			ExtraFlags: extraFlags,
			Force:      *force,
		},
		PositionalPrompt: positionalPrompt,
		PromptReader:     bufio.NewReader(os.Stdin),
	}
}

// splitAtSeparator splits args at the first "--" separator.
// Returns (before, after). If no "--" is found, after is nil.
func splitAtSeparator(args []string) (before, after []string) {
	for i, arg := range args {
		if arg == "--" {
			return args[:i], args[i+1:]
		}
	}
	return args, nil
}

// parseLogLevel maps a log level string to slog.Level.
// Returns slog.LevelInfo for unrecognized values.
func parseLogLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
