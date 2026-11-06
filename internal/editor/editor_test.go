package editor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDetectEditor(t *testing.T) {
	bin := t.TempDir()
	writeFake(t, bin, "nvim")

	t.Setenv("PATH", bin)
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")

	tests := []struct {
		name     string
		override string
		visual   string
		editor   string
		want     string
	}{
		{name: "override beats env", override: "code {file}", visual: "nano", editor: "ed", want: "code {file}"},
		{name: "visual beats editor", visual: "nano", editor: "ed", want: "nano"},
		{name: "editor", editor: "ed", want: "ed"},
		{name: "path fallback", want: "nvim"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("VISUAL", tc.visual)
			t.Setenv("EDITOR", tc.editor)
			got, err := DetectEditor(tc.override)
			if err != nil {
				t.Fatalf("DetectEditor: %v", err)
			}
			if got != tc.want {
				t.Fatalf("DetectEditor = %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("fallback order", func(t *testing.T) {
		dir := t.TempDir()
		writeFake(t, dir, "vim")
		writeFake(t, dir, "vi")
		t.Setenv("PATH", dir)
		if got, _ := DetectEditor(""); got != "vim" {
			t.Fatalf("DetectEditor = %q, want vim", got)
		}
	})

	t.Run("nothing found", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		got, err := DetectEditor("")
		if err != nil || got != "" {
			t.Fatalf("DetectEditor = %q, %v; want empty, nil", got, err)
		}
	})
}

func writeFake(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestDraftName(t *testing.T) {
	tests := []struct{ base, want string }{
		{"hello", "hello.eml"},
		{"my draft", "my draft.eml"},
		{"", "draft.eml"},
		{"..", "draft.eml"},
		{".hidden", "hidden.eml"},
		{"../../etc/passwd", "passwd.eml"},
		{"a\r\nBcc: evil@x", "aBcc: evil@x.eml"},
		{`a"; rm -rf /; echo "`, `; echo ".eml`},
		{"a\\b", "ab.eml"},
	}
	for _, tc := range tests {
		got := draftName(tc.base)
		if got != tc.want {
			t.Fatalf("draftName(%q) = %q, want %q", tc.base, got, tc.want)
		}
		if filepath.Base(got) != got || strings.ContainsAny(got, "\r\n/\\") || strings.HasPrefix(got, ".") {
			t.Fatalf("draftName(%q) = %q is not a safe base name", tc.base, got)
		}
	}
}

func TestNewDraft(t *testing.T) {
	d, err := NewDraft("subject: hi", "To: a@x\n")
	if err != nil {
		t.Fatalf("NewDraft: %v", err)
	}
	defer func() {
		if err := d.Cleanup(); err != nil {
			t.Errorf("Cleanup: %v", err)
		}
	}()

	di, err := os.Stat(filepath.Dir(d.Path()))
	if err != nil {
		t.Fatal(err)
	}
	if perm := di.Mode().Perm(); perm != 0o700 {
		t.Fatalf("draft dir mode = %o, want 700", perm)
	}
	fi, err := os.Stat(d.Path())
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("draft file mode = %o, want 600", perm)
	}
	if filepath.Base(d.Path()) != "subject: hi.eml" {
		t.Fatalf("draft path = %q", d.Path())
	}

	got, err := d.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got != "To: a@x\n" {
		t.Fatalf("Read = %q", got)
	}
}

func TestDraftCleanupIdempotent(t *testing.T) {
	d, err := NewDraft("x", "")
	if err != nil {
		t.Fatal(err)
	}
	path := d.Path()
	if err := d.Cleanup(); err != nil {
		t.Fatalf("first Cleanup: %v", err)
	}
	if err := d.Cleanup(); err != nil {
		t.Fatalf("second Cleanup: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("draft survived cleanup: %v", err)
	}
}

func TestDraftWatch(t *testing.T) {
	old := watchInterval
	watchInterval = 10 * time.Millisecond
	defer func() { watchInterval = old }()

	d, err := NewDraft("w", "one")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Cleanup()

	changes := make(chan struct{}, 8)
	stop, err := d.Watch(context.Background(), func() { changes <- struct{}{} })
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	if err := os.WriteFile(d.Path(), []byte("two, longer"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changes:
	case <-time.After(2 * time.Second):
		t.Fatal("watch did not fire on modification")
	}

	stop()
	select {
	case <-changes:
		t.Fatal("unexpected callback after stop")
	case <-time.After(100 * time.Millisecond):
	}
}
