package script

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"btcx/internal/model"
)

// lockedBuffer collects log output from concurrent goroutines safely.
type lockedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (w *lockedBuffer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func (w *lockedBuffer) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

// writeScripts creates a script directory holding files.
func writeScripts(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, src := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

// newRawEngine returns an unloaded engine with a custom budget and captured
// logs.
func newRawEngine(t *testing.T, budget time.Duration, files map[string]string) (*Engine, *lockedBuffer) {
	t.Helper()
	var buf lockedBuffer
	e, err := New(writeScripts(t, files), true, "9.9.9", slog.New(slog.NewTextHandler(&buf, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	e.budget = budget
	return e, &buf
}

// newEngine is newRawEngine for files that must load without warnings.
func newEngine(t *testing.T, budget time.Duration, files map[string]string) (*Engine, *lockedBuffer) {
	t.Helper()
	e, buf := newRawEngine(t, budget, files)
	warns, err := e.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(warns) != 0 {
		t.Fatalf("Load warnings: %v", warns)
	}
	return e, buf
}

func TestSandboxGlobals(t *testing.T) {
	const src = `
local removed = {"os","io","package","require","debug","dofile","loadfile",
  "load","loadstring","print","collectgarbage","coroutine","bit32","xpcall"}
for _, name in ipairs(removed) do
  assert(_G[name] == nil, name .. " is exposed")
end
for _, name in ipairs({"assert","error","getmetatable","ipairs","next","pairs",
  "pcall","rawequal","rawget","rawlen","rawset","select","setmetatable",
  "tonumber","tostring","type"}) do
  assert(type(_G[name]) == "function", name .. " is missing")
end
assert(type(string.format) == "function", "string")
assert(type(table.sort) == "function", "table")
assert(type(math.floor) == "function", "math")
assert(type(table.unpack) == "function", "table.unpack")
assert(type(btcx) == "table", "btcx")
assert(type(btcx.log) == "function", "btcx.log")
assert(btcx.version() == "9.9.9", "btcx.version")
assert(type(_VERSION) == "string", "_VERSION")
`
	e, _ := newEngine(t, time.Second, map[string]string{"sandbox.lua": src})
	if got := e.Warnings(); len(got) != 0 {
		t.Fatalf("sandbox script failed: %v", got)
	}
	if got := e.OnStartup(); len(got) != 0 {
		t.Fatalf("OnStartup: %v", got)
	}
}

func TestForbiddenCallsAreErrors(t *testing.T) {
	for _, name := range []string{"require", "dofile", "load", "loadstring", "loadfile", "collectgarbage"} {
		t.Run(name, func(t *testing.T) {
			e, _ := newRawEngine(t, time.Second, map[string]string{
				"bad.lua": name + `("x")`,
			})
			warns, err := e.Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if len(warns) == 0 {
				t.Fatalf("%s did not produce a warning", name)
			}
			if !strings.Contains(warns[0], name) || !strings.Contains(warns[0], "bad.lua") {
				t.Fatalf("warning %q does not name %s/bad.lua", warns[0], name)
			}
		})
	}
}

func TestOnNewMailEnvelope(t *testing.T) {
	e, buf := newEngine(t, time.Second, map[string]string{"mail.lua": `
function on_new_mail(env)
  btcx.log("info", "mail " .. env.subject .. " from " .. env.from[1].addr ..
    " uid " .. env.uid .. " seen " .. tostring(env.flags.seen) ..
    " attachments " .. tostring(env.has_attachments))
end
`})
	env := model.Envelope{
		Ref:            model.Ref{Mailbox: "INBOX", UIDValidity: 1, UID: 42},
		Subject:        "hello",
		From:           []model.Address{{Name: "A", Addr: "a@example.org"}},
		HasAttachments: false,
	}
	if warns := e.OnNewMail(env); len(warns) != 0 {
		t.Fatalf("OnNewMail: %v", warns)
	}
	want := "mail hello from a@example.org uid 42 seen false attachments false"
	if got := buf.String(); !strings.Contains(got, want) {
		t.Fatalf("log %q does not contain %q", got, want)
	}
}

func TestBeforeSend(t *testing.T) {
	newMsg := func() *model.Message {
		m := &model.Message{Plain: "body"}
		m.Header.Set("Subject", "Hi")
		return m
	}
	cases := []struct {
		name     string
		src      string
		want     bool
		wantWarn bool
	}{
		{"false cancels", `function before_send(m) return false end`, false, false},
		{"true allows", `function before_send(m) return true end`, true, false},
		{"nil allows", `function before_send(m) end`, true, false},
		{"bad type allows", `function before_send(m) return "nope" end`, true, true},
		{"error allows", `function before_send(m) error("nope") end`, true, true},
		{"no hook", ``, true, false},
		{"sees subject", `function before_send(m) return m.subject == "Hi" end`, true, false},
		{"inspects attachments", `function before_send(m) return #m.attachments == 1 end`, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := newEngine(t, time.Second, map[string]string{"send.lua": tc.src})
			m := newMsg()
			m.Attachments = []model.Attachment{{Filename: "x", ContentType: "text/plain", Data: []byte("hi")}}
			allow, warns := e.BeforeSend(m)
			if allow != tc.want {
				t.Fatalf("allow = %v, want %v (warnings %v)", allow, tc.want, warns)
			}
			if tc.wantWarn && len(warns) == 0 {
				t.Fatal("expected a warning")
			}
			if !tc.wantWarn && len(warns) != 0 {
				t.Fatalf("unexpected warnings: %v", warns)
			}
		})
	}
}

func TestSyntaxErrorDisablesOnlyThatFile(t *testing.T) {
	e, buf := newRawEngine(t, time.Second, map[string]string{
		"a_bad.lua":  "function on_startup(\n",
		"b_good.lua": `function on_startup() btcx.log("info", "good ran") end`,
	})
	warns, err := e.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "a_bad.lua") {
		t.Fatalf("warnings = %v, want one naming a_bad.lua", warns)
	}
	if got := e.OnStartup(); len(got) != 0 {
		t.Fatalf("OnStartup: %v", got)
	}
	if got := buf.String(); !strings.Contains(got, "good ran") {
		t.Fatalf("good script did not run: %q", got)
	}
}

func TestBeforeSendMultipleScriptsVeto(t *testing.T) {
	e, _ := newEngine(t, time.Second, map[string]string{
		"1.lua": `function before_send(m) return true end`,
		"2.lua": `function before_send(m) return false end`,
	})
	allow, warns := e.BeforeSend(&model.Message{})
	if allow {
		t.Fatal("a single false must veto the send")
	}
	if len(warns) != 0 {
		t.Fatalf("warnings: %v", warns)
	}
}

func TestInfiniteLoopBudget(t *testing.T) {
	bodies := map[string]string{
		"plain loop": `function on_startup() while true do end end`,
		"loop in pcall": `function on_startup()
  while true do pcall(function() while true do end end) end
end`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			e, buf := newEngine(t, 100*time.Millisecond, map[string]string{
				"loop.lua":  body,
				"alive.lua": `function on_startup() btcx.log("info", "alive") end`,
			})
			start := time.Now()
			warns := e.OnStartup()
			elapsed := time.Since(start)
			if elapsed > 2*time.Second {
				t.Fatalf("hook took %v, budget was 100ms", elapsed)
			}
			if len(warns) != 1 || !strings.Contains(warns[0], "loop.lua") || !strings.Contains(warns[0], "budget") {
				t.Fatalf("warnings = %v, want one budget warning naming loop.lua", warns)
			}
			if !strings.Contains(buf.String(), "alive") {
				t.Fatal("healthy script did not run")
			}
			// The runaway script stays disabled; the engine still works.
			if got := e.OnStartup(); len(got) != 0 {
				t.Fatalf("second OnStartup should not rerun the disabled script: %v", got)
			}
		})
	}
}

func TestMemoryBombBounded(t *testing.T) {
	e, _ := newEngine(t, 20*time.Millisecond, map[string]string{"bomb.lua": `
function on_startup()
  local t = {}
  while true do t[#t+1] = ("x"):rep(1024) end
end
`})
	start := time.Now()
	warns := e.OnStartup()
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("memory bomb took %v", elapsed)
	}
	if len(warns) == 0 {
		t.Fatal("expected a budget warning")
	}
}

func TestFailingHookDisablesScript(t *testing.T) {
	e, buf := newEngine(t, time.Second, map[string]string{
		"fail.lua": `
function on_startup() error("boom") end
function on_new_mail(env) btcx.log("info", "fail hook ran") end
`,
		"ok.lua": `function on_startup() btcx.log("info", "ok ran") end`,
	})
	warns := e.OnStartup()
	if len(warns) != 1 || !strings.Contains(warns[0], "boom") || !strings.Contains(warns[0], "fail.lua") {
		t.Fatalf("warnings = %v, want one naming fail.lua and boom", warns)
	}
	if !strings.Contains(buf.String(), "ok ran") {
		t.Fatal("healthy script did not run")
	}
	if got := e.OnNewMail(model.Envelope{}); len(got) != 0 {
		t.Fatalf("disabled script ran again: %v", got)
	}
	if strings.Contains(buf.String(), "fail hook ran") {
		t.Fatal("disabled script's later hook ran")
	}
}

func TestMissingDirectory(t *testing.T) {
	e, err := New(filepath.Join(t.TempDir(), "absent"), true, "1", nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	warns, err := e.Load()
	if err != nil || len(warns) != 0 {
		t.Fatalf("Load = %v, %v; want no warnings, no error", warns, err)
	}
	if got := e.OnStartup(); len(got) != 0 {
		t.Fatalf("OnStartup: %v", got)
	}
}

func TestDeterministicOrder(t *testing.T) {
	e, buf := newEngine(t, time.Second, map[string]string{
		"02_second.lua": `function on_startup() btcx.log("info", "second") end`,
		"01_first.lua":  `function on_startup() btcx.log("info", "first") end`,
	})
	if warns := e.OnStartup(); len(warns) != 0 {
		t.Fatalf("OnStartup: %v", warns)
	}
	got := buf.String()
	first, second := strings.Index(got, "first"), strings.Index(got, "second")
	if first < 0 || second < 0 || first > second {
		t.Fatalf("hooks ran out of filename order: %q", got)
	}
}

func TestDisabledEngine(t *testing.T) {
	dir := writeScripts(t, map[string]string{"s.lua": `function on_startup() btcx.log("info", "ran") end`})
	var buf lockedBuffer
	e, err := New(dir, false, "1", slog.New(slog.NewTextHandler(&buf, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if warns, err := e.Load(); err != nil || len(warns) != 0 {
		t.Fatalf("Load = %v, %v", warns, err)
	}
	if got := e.OnStartup(); len(got) != 0 {
		t.Fatalf("OnStartup: %v", got)
	}
	allow, warns := e.BeforeSend(&model.Message{})
	if !allow || len(warns) != 0 {
		t.Fatalf("BeforeSend = %v, %v", allow, warns)
	}
	if buf.String() != "" {
		t.Fatalf("disabled engine ran a script: %q", buf.String())
	}
}

func TestLogTruncation(t *testing.T) {
	e, buf := newEngine(t, time.Second, map[string]string{"log.lua": `
btcx.log("warn", string.rep("x", 50000))
`})
	if got := e.Warnings(); len(got) != 0 {
		t.Fatalf("warnings: %v", got)
	}
	out := buf.String()
	if len(out) > maxLogMessage+1024 {
		t.Fatalf("log line not truncated: %d bytes", len(out))
	}
	if !strings.Contains(out, "truncated") || !strings.Contains(out, "level=WARN") {
		t.Fatalf("log line = %q", out)
	}
}

func TestDefaultBudget(t *testing.T) {
	e, err := New(t.TempDir(), true, "1", nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if e.budget != 2*time.Second {
		t.Fatalf("budget = %v, want 2s", e.budget)
	}
}

func TestWarningCap(t *testing.T) {
	e, err := New(t.TempDir(), true, "1", nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for i := 0; i < maxWarnings+5; i++ {
		e.record(fmt.Sprintf("w%d", i))
	}
	got := e.Warnings()
	if len(got) != maxWarnings {
		t.Fatalf("kept %d warnings, want %d", len(got), maxWarnings)
	}
	if last := got[len(got)-1]; last != fmt.Sprintf("w%d", maxWarnings+4) {
		t.Fatalf("newest warning = %q", last)
	}
}

// TestConcurrentHooks exercises the engine's mutex under -race.
func TestConcurrentHooks(t *testing.T) {
	e, _ := newEngine(t, time.Second, map[string]string{
		"c.lua": `
function on_startup() btcx.log("info", "start") end
function on_new_mail(env) btcx.log("info", env.subject) end
function after_send(m, sent, err) btcx.log("info", "sent") end
`,
	})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.OnStartup()
			e.OnNewMail(model.Envelope{Subject: "x"})
			e.AfterSend(&model.Message{}, true, "")
			e.Warnings()
		}()
	}
	wg.Wait()
}
