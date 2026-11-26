package platform

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const DefaultCommandOutputLimit = 1 << 20

type CommandRequest struct {
	Tool               string
	Path               string
	Args               []string
	Input              []byte
	Environment        []string
	Timeout            time.Duration
	OutputLimit        int
	SensitiveOutput    bool
	SkipDiagnosticOnOK bool
}

type CommandResult struct {
	Command    []string `json:"command"`
	Stdout     string   `json:"stdout,omitempty"`
	Stderr     string   `json:"stderr,omitempty"`
	ExitCode   int      `json:"exit_code"`
	DurationMS int64    `json:"duration_ms"`
	Truncated  bool     `json:"truncated,omitempty"`
}

type CommandDiagnostic struct {
	Tool       string `json:"tool"`
	ExitCode   int    `json:"exit_code"`
	DurationMS int64  `json:"duration_ms"`
	Truncated  bool   `json:"truncated,omitempty"`
	ErrorCode  string `json:"error_code,omitempty"`
	Stdout     string `json:"stdout,omitempty"`
	Stderr     string `json:"stderr,omitempty"`
}

type CommandError struct {
	Code   string
	Result CommandResult
	Cause  error
}

func (err *CommandError) Error() string {
	detail := strings.TrimSpace(err.Result.Stderr)
	if detail == "" {
		detail = strings.TrimSpace(err.Result.Stdout)
	}
	command := strings.Join(err.Result.Command, " ")
	if command == "" {
		command = "native command"
	}
	if detail != "" {
		return fmt.Sprintf("%s: %v: %s", command, err.Cause, detail)
	}
	return fmt.Sprintf("%s: %v", command, err.Cause)
}

func (err *CommandError) Unwrap() error { return err.Cause }

type NativeRunner interface {
	Run(context.Context, CommandRequest) (CommandResult, error)
}

type OSNativeRunner struct{}

func (OSNativeRunner) Run(parent context.Context, request CommandRequest) (result CommandResult, returnedError error) {
	result = CommandResult{ExitCode: -1}
	defer func() { recordCommandDiagnostic(request, result, returnedError) }()
	path := strings.TrimSpace(request.Path)
	if path != "" && !filepath.IsAbs(path) {
		err := fmt.Errorf("%w: native command path must be absolute", ErrInvalid)
		return result, &CommandError{Code: "invalid_path", Result: result, Cause: err}
	}
	if path == "" {
		var err error
		path, err = exec.LookPath(request.Tool)
		if err != nil {
			cause := fmt.Errorf("%w: %s is missing from PATH", ErrUnavailable, request.Tool)
			return result, &CommandError{Code: "not_found", Result: result, Cause: cause}
		}
	}
	result.Command = append([]string{path}, request.Args...)
	timeout := request.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	limit := request.OutputLimit
	if limit <= 0 {
		limit = DefaultCommandOutputLimit
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	capture := newCommandCapture(limit)
	cmd := exec.CommandContext(ctx, path, request.Args...)
	configureNativeCommand(cmd)
	cmd.Env = append(os.Environ(), request.Environment...)
	cmd.Stdin = bytes.NewReader(request.Input)
	cmd.Stdout = capture.writer(&capture.stdout)
	cmd.Stderr = capture.writer(&capture.stderr)
	started := time.Now()
	err := cmd.Run()
	result.DurationMS = time.Since(started).Milliseconds()
	result.Stdout, result.Stderr, result.Truncated = capture.result()
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	if err == nil {
		return result, nil
	}
	code, cause := "start_failed", err
	switch {
	case errors.Is(parent.Err(), context.Canceled):
		code, cause = "canceled", parent.Err()
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		code, cause = "timeout", ctx.Err()
	default:
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			code = "exit"
		} else if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) {
			code = "unavailable"
			cause = fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
	}
	return result, &CommandError{Code: code, Result: result, Cause: cause}
}

var nativeCommandDiagnostics struct {
	sync.Mutex
	entries []CommandDiagnostic
}

func recordCommandDiagnostic(request CommandRequest, result CommandResult, err error) {
	if err == nil && request.SkipDiagnosticOnOK {
		return
	}
	tool := strings.TrimSpace(request.Tool)
	if tool == "" {
		tool = filepath.Base(request.Path)
	}
	diagnostic := CommandDiagnostic{
		Tool: tool, ExitCode: result.ExitCode, DurationMS: result.DurationMS, Truncated: result.Truncated,
	}
	var commandError *CommandError
	if errors.As(err, &commandError) {
		diagnostic.ErrorCode = commandError.Code
	}
	if !request.SensitiveOutput {
		diagnostic.Stdout, diagnostic.Stderr = diagnosticText(result.Stdout), diagnosticText(result.Stderr)
		if diagnostic.Stdout != result.Stdout || diagnostic.Stderr != result.Stderr {
			diagnostic.Truncated = true
		}
	}
	nativeCommandDiagnostics.Lock()
	defer nativeCommandDiagnostics.Unlock()
	nativeCommandDiagnostics.entries = append(nativeCommandDiagnostics.entries, diagnostic)
	if len(nativeCommandDiagnostics.entries) > 64 {
		nativeCommandDiagnostics.entries = append([]CommandDiagnostic(nil), nativeCommandDiagnostics.entries[len(nativeCommandDiagnostics.entries)-64:]...)
	}
}

func diagnosticText(value string) string {
	const limit = 4 << 10
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "\n[diagnostic output truncated]"
}

func CommandDiagnostics() []CommandDiagnostic {
	nativeCommandDiagnostics.Lock()
	defer nativeCommandDiagnostics.Unlock()
	return append([]CommandDiagnostic(nil), nativeCommandDiagnostics.entries...)
}

type commandCapture struct {
	mu        sync.Mutex
	remaining int
	truncated bool
	stdout    bytes.Buffer
	stderr    bytes.Buffer
}

type commandCaptureWriter struct {
	capture *commandCapture
	target  *bytes.Buffer
}

func newCommandCapture(limit int) *commandCapture {
	return &commandCapture{remaining: limit}
}

func (capture *commandCapture) writer(target *bytes.Buffer) *commandCaptureWriter {
	return &commandCaptureWriter{capture: capture, target: target}
}

func (writer *commandCaptureWriter) Write(data []byte) (int, error) {
	writer.capture.mu.Lock()
	defer writer.capture.mu.Unlock()
	stored := min(len(data), writer.capture.remaining)
	if stored > 0 {
		_, _ = writer.target.Write(data[:stored])
		writer.capture.remaining -= stored
	}
	if stored < len(data) {
		writer.capture.truncated = true
	}
	return len(data), nil
}

func (capture *commandCapture) result() (string, string, bool) {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return capture.stdout.String(), capture.stderr.String(), capture.truncated
}
