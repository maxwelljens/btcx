// Package script runs user-supplied Lua hooks in a sandbox.
//
// # Sandbox
//
// Every script gets a fresh lua.State holding only the safe subset of the Lua
// base library (assert, error, getmetatable, ipairs, next, pairs, pcall,
// rawequal, rawget, rawlen, rawset, select, setmetatable, tonumber, tostring,
// type) plus the string, table and math libraries. Everything that can reach
// the host is removed: os, io, package, require, debug, dofile, loadfile,
// load, loadstring, print, collectgarbage and xpcall. setmetatable is kept
// because nothing above it is privileged: a script can only damage its own
// isolated state, which is discarded on error.
//
// The string library is capped per state: string.rep fails before allocating
// a result above 8 MiB, and string.format fails on a format string above
// 64 KiB or a width/precision above 10000. Everything else in string, table
// and math allocates only in proportion to arguments already in memory.
//
// Scripts talk to btcx through a single global table:
//
//	btcx.log(level, msg)  -- level is "debug", "info", "warn" or "error";
//	                      -- anything else logs at info level. msg is coerced
//	                      -- to a string and truncated to 8 KiB.
//	btcx.version()        -- the btcx version string.
//
// # Hooks
//
// A script may define any of:
//
//	on_startup()
//	on_new_mail(envelope)
//	before_send(message) -> bool
//	after_send(message, sent, err_msg)
//
// envelope and message are plain Lua tables with string/number/bool/array
// fields (see values.go). before_send returning false cancels the send; any
// other return value allows it.
//
// # Isolation and ordering
//
// Each *.lua file is loaded and run in its own state, so scripts cannot see
// or overwrite each other's globals: a later file does not override a hook an
// earlier file defined. All hooks from all live scripts run, in filename
// order, and every script's before_send has a veto. Within one file the last
// definition of a hook name wins, as in Lua.
//
// A syntax error or an error at file scope disables that file only; the other
// scripts still load. A runtime error in a hook disables that script; its
// remaining hooks are skipped and the error is reported as a warning. Hooks
// are called under a mutex, so the engine is safe for concurrent use.
//
// # Execution budget
//
// Every chunk and hook call runs in its own goroutine with an
// instruction-count debug hook (lua.SetDebugHook with lua.MaskCount) that
// checks the deadline every 1000 VM instructions. When the budget is spent
// the hook calls runtime.Goexit, abandoning the goroutine and that script's
// state. pcall cannot catch it, so a script cannot dodge the budget by
// wrapping its loops in pcall. The script is disabled and a warning is
// returned; btcx itself keeps working.
//
// The budget cannot interrupt a single library call, so the string library
// is capped before any script runs: string.rep refuses results above 8 MiB
// and string.format refuses format strings above 64 KiB or with a width or
// precision above 10000. Those are the only single-call allocators reachable
// from a script; every other library call allocates in proportion to
// arguments that are already in memory.
//
// shortcut: the caps are per call, so a loop can still fill memory until the
// budget stops it; tighten them if hooks ever run without a budget.
package script

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Shopify/go-lua"

	"btcx/internal/model"
)

const (
	// defaultBudget is how long one chunk or hook call may run.
	defaultBudget = 2 * time.Second
	// hookInterval is the number of VM instructions between budget checks.
	hookInterval = 1000
	// maxWarnings bounds the warnings kept for the UI.
	maxWarnings = 100
	// maxLogMessage bounds what a script may push into one log record.
	maxLogMessage = 8 << 10
	// maxStringBytes caps what string.rep may build in one call.
	maxStringBytes = 8 << 20
	// maxFormatBytes caps the format string string.format accepts.
	maxFormatBytes = 64 << 10
	// maxFormatField caps one directive's width or precision.
	maxFormatField = 10000
)

// errBudget is reported when a script exhausts its execution budget.
var errBudget = errors.New("execution budget exceeded")

// removedGlobals are deleted from the base library after opening it. os, io,
// package, require, debug, coroutine and bit32 are never opened at all; they
// are listed here so the sandbox stays closed if that changes.
var removedGlobals = []string{
	"collectgarbage", "coroutine", "debug", "dofile", "io", "load", "loadfile",
	"loadstring", "os", "package", "print", "require", "xpcall", "bit32",
}

// Engine loads Lua scripts and dispatches btcx hooks to them.
type Engine struct {
	mu       sync.Mutex
	dir      string
	enabled  bool
	version  string
	log      *slog.Logger
	budget   time.Duration
	scripts  []*script
	warnings []string
}

// script is one loaded file: its own Lua state, or none if it is disabled.
type script struct {
	name   string
	state  *lua.State
	failed bool // runtime error or budget overrun; never called again
}

// hookResult is one hook's return value, tagged with the script that produced
// it so callers can name the culprit in warnings.
type hookResult struct {
	script *script
	value  interface{}
}

// New returns an engine for the Lua files in dir. When enabled is false the
// engine loads nothing and every hook is a no-op. version is what
// btcx.version() returns. A nil logger falls back to slog.Default.
func New(dir string, enabled bool, version string, logger *slog.Logger) (*Engine, error) {
	if logger == nil {
		logger = slog.Default()
	}
	return &Engine{dir: dir, enabled: enabled, version: version, log: logger, budget: defaultBudget}, nil
}

// Load reads and compiles every *.lua file in the script directory, in
// filename order, replacing anything loaded before. A missing directory is
// not an error. A file that fails to compile or fails at file scope is
// disabled and reported as a warning; the remaining files still load.
func (e *Engine) Load() ([]string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.scripts = nil
	if !e.enabled {
		return nil, nil
	}
	entries, err := os.ReadDir(e.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("script: read %s: %w", e.dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, ent := range entries {
		if !ent.IsDir() && filepath.Ext(ent.Name()) == ".lua" {
			names = append(names, ent.Name())
		}
	}
	sort.Strings(names)

	var warns []string
	for _, name := range names {
		path := filepath.Join(e.dir, name)
		src, err := os.ReadFile(path)
		if err != nil {
			warns = append(warns, e.record(fmt.Sprintf("script %s: %v", name, err)))
			continue
		}
		s := &script{name: name}
		s.state = e.newState(s)
		if err := s.state.Load(bytes.NewReader(src), "@"+path, "t"); err != nil {
			warns = append(warns, e.record(fmt.Sprintf("script %s: %v", name, err)))
			continue
		}
		if err := e.run(s, func(l *lua.State) error { return l.ProtectedCall(0, 0, 0) }); err != nil {
			warns = append(warns, e.record(fmt.Sprintf("script %s: %v", name, err)))
			continue
		}
		e.scripts = append(e.scripts, s)
	}
	return warns, nil
}

// OnStartup runs every script's on_startup hook.
func (e *Engine) OnStartup() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.enabled {
		return nil
	}
	_, warns := e.call("on_startup", nil)
	return warns
}

// OnNewMail runs every script's on_new_mail hook with the new envelope.
func (e *Engine) OnNewMail(env model.Envelope) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.enabled {
		return nil
	}
	_, warns := e.call("on_new_mail", func(l *lua.State) int { return pushEnvelope(l, env) })
	return warns
}

// BeforeSend runs every script's before_send hook. It reports whether the
// send may proceed: any hook returning false vetoes it, and a hook that
// returns a non-boolean is ignored with a warning.
func (e *Engine) BeforeSend(m *model.Message) (bool, []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.enabled {
		return true, nil
	}
	results, warns := e.call("before_send", func(l *lua.State) int { return pushMessage(l, m) })
	allow := true
	for _, r := range results {
		switch v := r.value.(type) {
		case bool:
			if !v {
				allow = false
			}
		case nil:
		default:
			warns = append(warns, e.record(fmt.Sprintf("script %s: before_send returned %T, want boolean", r.script.name, r.value)))
		}
	}
	return allow, warns
}

// AfterSend runs every script's after_send hook once a send has finished.
func (e *Engine) AfterSend(m *model.Message, sent bool, errMsg string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.enabled {
		return nil
	}
	_, warns := e.call("after_send", func(l *lua.State) int {
		pushMessage(l, m)
		l.PushBoolean(sent)
		l.PushString(errMsg)
		return 3
	})
	return warns
}

// Warnings returns the accumulated warnings, oldest first, at most 100.
func (e *Engine) Warnings() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.warnings...)
}

// call runs hook name in every live script, in load order.
func (e *Engine) call(name string, push func(*lua.State) int) ([]hookResult, []string) {
	var (
		results []hookResult
		warns   []string
	)
	for _, s := range e.scripts {
		if s.failed {
			continue
		}
		value, called, err := e.hookCall(s, name, push)
		if err != nil {
			s.failed = true
			warns = append(warns, e.record(fmt.Sprintf("script %s: %s: %v", s.name, name, err)))
			continue
		}
		if called {
			results = append(results, hookResult{s, value})
		}
	}
	return results, warns
}

// hookCall invokes the script's global function name, if it is a function,
// and returns its first result.
func (e *Engine) hookCall(s *script, name string, push func(*lua.State) int) (interface{}, bool, error) {
	l := s.state
	l.SetTop(0)
	l.Global(name)
	if !l.IsFunction(-1) {
		l.SetTop(0)
		return nil, false, nil
	}
	argCount := 0
	if push != nil {
		argCount = push(l)
	}
	var out interface{}
	err := e.run(s, func(l *lua.State) error {
		if err := l.ProtectedCall(argCount, 1, 0); err != nil {
			return err
		}
		out = l.ToValue(-1)
		return nil
	})
	return out, true, err
}

// run executes f against the script's state in a dedicated goroutine under an
// instruction-count hook. On budget expiry the goroutine is killed with
// runtime.Goexit and the state is left unusable, so the caller must disable
// the script.
func (e *Engine) run(s *script, f func(*lua.State) error) error {
	deadline := time.Now().Add(e.budget)
	type outcome struct {
		err      error
		timedOut bool
	}
	done := make(chan outcome, 1)
	go func() {
		var o outcome
		defer func() {
			// A Go panic from inside the VM must not take btcx down.
			if p := recover(); p != nil {
				o.err = fmt.Errorf("panic: %v", p)
			}
			done <- o
		}()
		lua.SetDebugHook(s.state, func(*lua.State, lua.Debug) {
			if time.Now().After(deadline) {
				o.timedOut = true
				runtime.Goexit()
			}
		}, lua.MaskCount, hookInterval)
		o.err = f(s.state)
	}()
	o := <-done
	if o.timedOut {
		return errBudget
	}
	return o.err
}

// newState builds the sandboxed state for one script.
func (e *Engine) newState(s *script) *lua.State {
	l := lua.NewState()
	lua.BaseOpen(l)
	for _, lib := range []lua.RegistryFunction{
		{Name: "string", Function: lua.StringOpen},
		{Name: "table", Function: lua.TableOpen},
		{Name: "math", Function: lua.MathOpen},
	} {
		lua.Require(l, lib.Name, lib.Function, true)
		l.Pop(1)
	}
	hardenString(l)
	for _, name := range removedGlobals {
		l.PushNil()
		l.SetGlobal(name)
	}
	l.NewTable()
	l.PushGoFunction(e.luaLog(s))
	l.SetField(-2, "log")
	l.PushGoFunction(func(l *lua.State) int {
		l.PushString(e.version)
		return 1
	})
	l.SetField(-2, "version")
	l.SetGlobal("btcx")
	return l
}

// hardenString swaps the string library's single-call allocators for capped
// wrappers. The originals are kept in the registry, which scripts cannot reach.
func hardenString(l *lua.State) {
	l.Global("string")
	for _, lib := range []struct {
		name string
		fn   lua.Function
	}{{"rep", cappedRep}, {"format", cappedFormat}} {
		l.Field(-1, lib.name)
		l.SetField(lua.RegistryIndex, "btcx.string."+lib.name)
		l.PushGoFunction(lib.fn)
		l.SetField(-2, lib.name)
	}
	l.Pop(1)
}

// cappedRep rejects string.rep results larger than maxStringBytes before the
// builtin allocates them.
func cappedRep(l *lua.State) int {
	if s, ok := l.ToString(1); ok {
		if n, ok := l.ToInteger(2); ok && n > 0 {
			sep, _ := l.ToString(3)
			if len(s)+len(sep) > 0 && n > maxStringBytes {
				lua.Errorf(l, "string.rep: result too large")
			}
			size := int64(len(s)) * int64(n)
			if n > 1 {
				size += int64(len(sep)) * int64(n-1)
			}
			if size > maxStringBytes {
				lua.Errorf(l, "string.rep: result too large")
			}
		}
	}
	return callString(l, "rep")
}

// cappedFormat rejects format strings that would make the builtin allocate
// large padding, then defers to it.
func cappedFormat(l *lua.State) int {
	if f, ok := l.ToString(1); ok {
		if len(f) > maxFormatBytes {
			lua.Errorf(l, "string.format: format too long")
		}
		if formatTooWide(f) {
			lua.Errorf(l, "string.format: width or precision too large")
		}
	}
	return callString(l, "format")
}

// callString invokes the original library function kept in the registry with
// the wrapper's arguments.
func callString(l *lua.State, name string) int {
	l.Field(lua.RegistryIndex, "btcx.string."+name)
	args := l.Top() - 1
	for i := 1; i <= args; i++ {
		l.PushValue(i)
	}
	l.Call(args, 1)
	return 1
}

// formatTooWide reports whether f has a directive whose width or precision
// exceeds maxFormatField.
func formatTooWide(f string) bool {
	for i := 0; i < len(f); i++ {
		if f[i] != '%' {
			continue
		}
		i++
		for i < len(f) && strings.IndexByte("-+ #0", f[i]) >= 0 {
			i++
		}
		for field := 0; field < 2; field++ {
			if field == 1 {
				if i >= len(f) || f[i] != '.' {
					break
				}
				i++
			}
			v := 0
			for ; i < len(f) && '0' <= f[i] && f[i] <= '9'; i++ {
				if v <= maxFormatField {
					v = v*10 + int(f[i]-'0')
				}
			}
			if v > maxFormatField {
				return true
			}
		}
	}
	return false
}

// luaLog implements btcx.log(level, msg).
func (e *Engine) luaLog(s *script) lua.Function {
	return func(l *lua.State) int {
		level, _ := l.ToString(1)
		msg, ok := lua.ToStringMeta(l, 2)
		l.Pop(1) // ToStringMeta leaves the string on the stack
		if !ok {
			msg = ""
		}
		if len(msg) > maxLogMessage {
			msg = strings.ToValidUTF8(msg[:maxLogMessage], "") + "[truncated]"
		}
		switch strings.ToLower(level) {
		case "debug":
			e.log.Debug(msg, "script", s.name)
		case "warn":
			e.log.Warn(msg, "script", s.name)
		case "error":
			e.log.Error(msg, "script", s.name)
		default:
			e.log.Info(msg, "script", s.name)
		}
		return 0
	}
}

// record appends a warning for the UI, keeping the newest maxWarnings, and
// returns it so callers can also return it to the immediate caller.
func (e *Engine) record(msg string) string {
	e.warnings = append(e.warnings, msg)
	if len(e.warnings) > maxWarnings {
		e.warnings = e.warnings[len(e.warnings)-maxWarnings:]
	}
	return msg
}
