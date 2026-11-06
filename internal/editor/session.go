package editor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"btcx/internal/config"
)

// maxStderr caps the editor output kept for error reporting.
const maxStderr = 64 << 10

// Session is a running external editor.
type Session struct {
	cmd    *exec.Cmd
	ctx    context.Context
	stderr *cappedBuffer
	done   chan struct{}
	err    error // set before done is closed
}

// ExitError reports an editor that started but exited unsuccessfully.
type ExitError struct {
	Code   int    // process exit code; -1 when killed by a signal
	Stderr string // captured stderr, capped at 64 KiB
	Err    error  // underlying *exec.ExitError
}

func (e *ExitError) Error() string {
	if s := strings.TrimSpace(e.Stderr); s != "" {
		return fmt.Sprintf("editor: exited with code %d: %s", e.Code, s)
	}
	return fmt.Sprintf("editor: exited with code %d", e.Code)
}

// Unwrap returns the underlying process error.
func (e *ExitError) Unwrap() error { return e.Err }

// Start launches the editor on the draft without a shell. It returns once the
// process has started; Wait blocks for it to exit.
func Start(ctx context.Context, template string, d *Draft) (*Session, error) {
	if d == nil {
		return nil, errors.New("editor: nil draft")
	}
	argv, err := config.SplitCommand(template)
	if err != nil {
		return nil, fmt.Errorf("editor: split command: %w", err)
	}
	if len(argv) == 0 {
		return nil, errors.New("editor: empty editor command")
	}
	argv = buildArgs(argv, d.path)

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdout = io.Discard
	errBuf := &cappedBuffer{}
	cmd.Stderr = errBuf
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("editor: start %s: %w", argv[0], err)
	}
	s := &Session{cmd: cmd, ctx: ctx, stderr: errBuf, done: make(chan struct{})}
	go func() {
		s.err = s.wait()
		close(s.done)
	}()
	return s, nil
}

// buildArgs substitutes the draft path into a split template. The path stays a
// single argv element even when the template was written unquoted.
func buildArgs(argv []string, path string) []string {
	out := make([]string, 0, len(argv)+1)
	replaced := false
	for _, a := range argv {
		if strings.Contains(a, "{file}") {
			replaced = true
			a = strings.ReplaceAll(a, "{file}", path)
		}
		out = append(out, a)
	}
	if !replaced {
		out = append(out, path)
	}
	return out
}

// Wait blocks until the editor exits and reports the outcome. When ctx is
// cancelled the process is killed and the error wraps ctx.Err().
func (s *Session) Wait() error {
	<-s.done
	return s.err
}

// Done is closed when the editor process has exited.
func (s *Session) Done() <-chan struct{} { return s.done }

// wait collects the exit status once the process is gone.
func (s *Session) wait() error {
	err := s.cmd.Wait()
	if err == nil {
		return nil
	}
	if ctxErr := s.ctx.Err(); ctxErr != nil {
		return fmt.Errorf("editor: %w", ctxErr)
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return &ExitError{Code: exit.ExitCode(), Stderr: s.stderr.String(), Err: err}
	}
	return fmt.Errorf("editor: wait: %w", err)
}

// cappedBuffer keeps the first maxStderr bytes written to it and discards the
// rest, so a chatty editor cannot exhaust memory.
type cappedBuffer struct{ bytes.Buffer }

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if n := maxStderr - b.Len(); n > 0 {
		b.Buffer.Write(p[:min(n, len(p))])
	}
	return len(p), nil
}
