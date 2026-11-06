package editor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"btcx/internal/config"
)

const helperEnv = "BTCX_EDITOR_HELPER"

// TestHelperProcess is not a real test: it is the child process body the
// Session tests run instead of a real editor.
func TestHelperProcess(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	if len(args) == 0 {
		os.Exit(2)
	}
	switch args[0] {
	case "argv": // argv <outfile> <args...>
		if err := os.WriteFile(args[1], []byte(strings.Join(args[2:], "\n")), 0o600); err != nil {
			os.Exit(1)
		}
	case "append": // append <file>
		f, err := os.OpenFile(args[1], os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			os.Exit(1)
		}
		f.WriteString("edited\n")
		f.Close()
	case "exit": // exit <code>
		os.Stderr.WriteString("helper failed")
		code, _ := strconv.Atoi(args[1])
		os.Exit(code)
	case "sleep": // sleep <duration>
		d, _ := time.ParseDuration(args[1])
		time.Sleep(d)
	}
	os.Exit(0)
}

// helperTemplate builds an editor command that runs this test binary as the
// editor. The binary path is single-quoted so paths with spaces survive
// SplitCommand.
func helperTemplate(mode string, extra ...string) string {
	return strings.Join(append([]string{"'" + os.Args[0] + "'", "-test.run=TestHelperProcess", "--", mode}, extra...), " ")
}

func newHelperDraft(t *testing.T, base string) *Draft {
	t.Helper()
	t.Setenv(helperEnv, "1")
	d, err := NewDraft(base, "To: a@x\n\nbody\n")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Cleanup(); err != nil {
			t.Errorf("Cleanup: %v", err)
		}
	})
	return d
}

func TestBuildArgs(t *testing.T) {
	path := "/tmp/btcx draft/my draft.eml"
	tests := []struct {
		name     string
		template string
		want     []string
	}{
		{
			name:     "placeholder is one argument",
			template: `nvim {file}`,
			want:     []string{"nvim", path},
		},
		{
			name:     "embedded placeholder is one argument",
			template: `code --goto {file}`,
			want:     []string{"code", "--goto", path},
		},
		{
			name:     "no placeholder appends",
			template: "nvim",
			want:     []string{"nvim", path},
		},
		{
			name:     "terminal wrapper with quoted argument",
			template: `alacritty --title "my draft" -e nvim {file}`,
			want:     []string{"alacritty", "--title", "my draft", "-e", "nvim", path},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			argv, err := config.SplitCommand(tc.template)
			if err != nil {
				t.Fatalf("SplitCommand(%q): %v", tc.template, err)
			}
			got := buildArgs(argv, path)
			if len(got) != len(tc.want) {
				t.Fatalf("argv = %q, want %q", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("argv = %q, want %q", got, tc.want)
				}
			}
		})
	}
}

// TestNoShellSemantics proves a template hands literal arguments to the
// program it names, and that message-derived text stays a file name or a
// single argv element.
func TestNoShellSemantics(t *testing.T) {
	argv, err := config.SplitCommand(`sh -c "touch /tmp/pwned" {file}`)
	if err != nil {
		t.Fatalf("SplitCommand: %v", err)
	}
	got := buildArgs(argv, "/tmp/x.eml")
	want := []string{"sh", "-c", "touch /tmp/pwned", "/tmp/x.eml"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("argv = %q, want %q", got, want)
	}

	hostile, err := NewDraft(`a"; rm -rf /; echo "`, "")
	if err != nil {
		t.Fatal(err)
	}
	defer hostile.Cleanup()
	argv, err = config.SplitCommand("nvim {file}")
	if err != nil {
		t.Fatal(err)
	}
	got = buildArgs(argv, hostile.Path())
	if len(got) != 2 || got[1] != hostile.Path() {
		t.Fatalf("argv = %q, want path as single argument", got)
	}
	if strings.ContainsAny(filepath.Base(got[1]), "\r\n/\\") {
		t.Fatalf("unsafe draft name %q", filepath.Base(got[1]))
	}
}

func TestStartWait(t *testing.T) {
	d := newHelperDraft(t, "wait")
	out := filepath.Join(t.TempDir(), "argv.txt")

	s, err := Start(context.Background(), helperTemplate("argv", "'"+out+"'"+" {file}"), d)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := s.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	select {
	case <-s.Done():
	default:
		t.Fatal("Done not closed after Wait")
	}

	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != d.Path() {
		t.Fatalf("child argv = %q, want %q", got, d.Path())
	}
}

func TestStartAppendsPathWithoutPlaceholder(t *testing.T) {
	d := newHelperDraft(t, "nofile")
	out := filepath.Join(t.TempDir(), "argv.txt")

	s, err := Start(context.Background(), helperTemplate("argv", "'"+out+"'"), d)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := s.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if b, err := os.ReadFile(out); err != nil || string(b) != d.Path() {
		t.Fatalf("child argv = %q, %v; want %q", b, err, d.Path())
	}
}

func TestStartQuotedArgumentWithSpaces(t *testing.T) {
	d := newHelperDraft(t, "spaces")
	spaced := filepath.Join(t.TempDir(), "a dir with spaces")
	if err := os.Mkdir(spaced, 0o700); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(spaced, "argv.txt")

	s, err := Start(context.Background(), helperTemplate("argv", "'"+out+"'")+" {file}", d)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := s.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if b, err := os.ReadFile(out); err != nil || string(b) != d.Path() {
		t.Fatalf("child argv = %q, %v; want %q", b, err, d.Path())
	}
}

func TestExitError(t *testing.T) {
	d := newHelperDraft(t, "exit")
	s, err := Start(context.Background(), helperTemplate("exit", "3"), d)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	err = s.Wait()
	var exit *ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("Wait error = %v, want *ExitError", err)
	}
	if exit.Code != 3 {
		t.Fatalf("exit code = %d, want 3", exit.Code)
	}
	if !strings.Contains(exit.Stderr, "helper failed") {
		t.Fatalf("stderr = %q, want captured helper output", exit.Stderr)
	}
}

func TestContextCancelKillsEditor(t *testing.T) {
	d := newHelperDraft(t, "sleep")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s, err := Start(ctx, helperTemplate("sleep", "10s"), d)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	cancel()

	done := make(chan error, 1)
	go func() { done <- s.Wait() }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Wait error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("editor was not killed by context cancellation")
	}
}

func TestStartErrors(t *testing.T) {
	d := newHelperDraft(t, "err")
	if _, err := Start(context.Background(), "   ", d); err == nil {
		t.Fatal("Start with empty command succeeded")
	}
	if _, err := Start(context.Background(), `sh -c "unterminated`, d); err == nil {
		t.Fatal("Start with unterminated quote succeeded")
	}
	if _, err := Start(context.Background(), "/nonexistent/editor {file}", d); err == nil {
		t.Fatal("Start with missing binary succeeded")
	}
	if _, err := Start(context.Background(), "nvim {file}", nil); err == nil {
		t.Fatal("Start with nil draft succeeded")
	}
}

func TestCappedBuffer(t *testing.T) {
	var b cappedBuffer
	n, err := b.Write(make([]byte, maxStderr+100))
	if err != nil || n != maxStderr+100 {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if b.Len() != maxStderr {
		t.Fatalf("buffer length = %d, want %d", b.Len(), maxStderr)
	}
}
