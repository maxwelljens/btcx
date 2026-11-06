// Package editor integrates an external text editor into btcx and manages the
// secure temporary file a composed message is written to while the user edits
// it.
//
// Editors are launched without a shell: the configured command template is
// split into argv (config.SplitCommand) and the draft path is substituted as a
// single argument, so message-derived text can never gain shell semantics.
// Drafts live in a private directory (0700) created with a file mode of 0600;
// Cleanup removes the directory and everything in it.
//
// TUI editors need a terminal, which a GUI process does not provide. Use a
// terminal wrapper in the template:
//
//	editor = "alacritty -e nvim {file}"
//
// When the template has no {file} placeholder the path is appended as the last
// argument.
package editor

import (
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

// DetectEditor returns the external editor command template to use: override
// if non-empty, else $VISUAL, else $EDITOR, else the first of "nvim", "vim",
// "vi" found on PATH. It returns "" when none is available; the caller shows
// an error. The error return is reserved for future detection failures.
func DetectEditor(override string) (string, error) {
	if s := strings.TrimSpace(override); s != "" {
		return s, nil
	}
	for _, env := range []string{"VISUAL", "EDITOR"} {
		if s := strings.TrimSpace(os.Getenv(env)); s != "" {
			return s, nil
		}
	}
	for _, name := range []string{"nvim", "vim", "vi"} {
		if _, err := exec.LookPath(name); err == nil {
			return name, nil
		}
	}
	return "", nil
}

// Draft is a temporary composition file with a guaranteed lifecycle: the file
// and its private directory stay until Cleanup is called.
type Draft struct {
	dir  string
	path string
}

// watchInterval is a knob so tests need not wait the production half second.
var watchInterval = 500 * time.Millisecond

// NewDraft creates a private temporary directory containing a draft file named
// from suggestedBase and writes initialContent when it is non-empty.
func NewDraft(suggestedBase, initialContent string) (*Draft, error) {
	dir, err := os.MkdirTemp("", "btcx-draft-*")
	if err != nil {
		return nil, fmt.Errorf("editor: create draft dir: %w", err)
	}
	path := filepath.Join(dir, draftName(suggestedBase))
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		if initialContent != "" {
			_, err = f.WriteString(initialContent)
		}
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		return nil, fmt.Errorf("editor: write draft: %w", errors.Join(err, os.RemoveAll(dir)))
	}
	return &Draft{dir: dir, path: path}, nil
}

// draftName turns untrusted suggested base text (typically a message subject)
// into a single safe file name ending in ".eml".
func draftName(base string) string {
	name := filepath.Base(base)
	name = strings.NewReplacer("\r", "", "\n", "", "\x00", "", "/", "", `\`, "").Replace(name)
	name = strings.TrimSpace(name)
	name = strings.TrimLeft(name, ".-")
	if name == "" {
		name = "draft"
	}
	return name + ".eml"
}

// Path returns the absolute path of the draft file.
func (d *Draft) Path() string { return d.path }

// Read returns the current draft content.
func (d *Draft) Read() (string, error) {
	b, err := os.ReadFile(d.path)
	if err != nil {
		return "", fmt.Errorf("editor: read draft: %w", err)
	}
	return string(b), nil
}

// Cleanup removes the draft file and its private directory. It is idempotent.
func (d *Draft) Cleanup() error {
	if err := os.RemoveAll(d.dir); err != nil {
		return fmt.Errorf("editor: cleanup draft: %w", err)
	}
	return nil
}

// Watch polls the draft for modifications and calls onChange after each
// change. onChange runs on a polling goroutine and must marshal to the UI
// thread itself. The returned stop terminates the poller and waits for it.
//
// shortcut: polling every watchInterval, not inotify; upgrade if a draft ever
// grows large enough that a 500 ms stat is noticeable.
func (d *Draft) Watch(ctx context.Context, onChange func()) (stop func(), err error) {
	fi, err := os.Stat(d.path)
	if err != nil {
		return nil, fmt.Errorf("editor: watch draft: %w", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(watchInterval)
		defer t.Stop()
		last := fi
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				fi, err := os.Stat(d.path)
				if err != nil {
					continue // editor may replace the file; keep polling
				}
				if fi.Size() != last.Size() || !fi.ModTime().Equal(last.ModTime()) {
					last = fi
					onChange()
				}
			}
		}
	}()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
	return stop, nil
}
