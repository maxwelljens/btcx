# AGENTS.md — btcx development guide

btcx is a lean, production-grade mail user agent (MUA) for Linux, written in
Go with a gotk4 (GTK4) front end and optional Lua scripting. It targets the
user who likes Thunderbird's competence but not its bulk: full-featured mail
handling, zero bloat, no calendar, no contacts, no WebKit.

This file is the authoritative development guide for humans and AI agents
working on the codebase. Read it before making changes.

## Core principles

1. **Lean but powerful.** Every feature must earn its place. Prefer the
   standard library or one small, well-maintained dependency over a big one.
   No feature that a serious MUA does not need.
2. **Production-grade.** No hacks, no TODO-strewn shortcuts, no untested
   code paths. Stability and correctness are features.
3. **Secure by default.** Mail is hostile input. Assume every message,
   server response, filename, and config value is attacker-controlled until
   proven otherwise. See the Security section below; it is not optional.
4. **Tested relentlessly.** Every package has unit tests; parsers have fuzz
   targets; the end-to-end pipeline has integration tests. `go test ./...`
   must always pass on `master`.
5. **Simple and straightforward.** Clear package boundaries, plain Go,
   table-driven tests, wrapped errors, no clever metaprogramming.

## Architecture

```
cmd/btcx/                main entry point: flags, wiring, GTK application
internal/
  model/                 domain types (Message, Header, Address, Envelope,
                         Mailbox, Flags, Ref). Standard library ONLY.
                         The contract between all other packages.
  config/                TOML configuration (~/.config/btcx/*.toml),
                         validation, secure defaults
  mailparse/             RFC 5322 / MIME parsing: raw bytes -> model.Message
  compose/               model.Message -> raw RFC 5322 bytes (plain, HTML,
                         multipart/alternative, attachments, replies)
  sanitize/              HTML sanitization for untrusted mail bodies
  render/                sanitized HTML -> Pango markup for display,
                         HTML -> plain text fallback
  imapx/                 IMAP client: folders, envelopes, bodies, flags,
                         search, IDLE watching
  smtpx/                 SMTP submission: TLS, auth, send
  script/                Lua sandbox (go-lua): hooks and safe API
  editor/                external editor integration (nvim, vim, $EDITOR),
                         secure temp files, built-in editor buffer logic
  contract/              app<->ui boundary: view models + Controller/Events
                         interfaces (compiles independently of both sides)
  ui/                    gotk4 widgets and views (thin; no business logic)
  app/                   presenters/controllers wiring model + services + ui
  buildinfo/             version/commit build metadata (ldflags)
```

Dependency direction: `ui -> app -> {mailparse, compose, imapx, smtpx,
script, editor, config} -> model`. `model` imports nothing outside the
standard library. `ui` must stay thin: anything testable without GTK belongs
in `app` or below. Never import `ui` from a non-UI package.

## Development workflow

1. **Understand the change.** Check this file, the package's doc comment,
   and existing tests. Use the `btcx-dev` and `btcx-security` skills.
2. **Write the test first** for the behaviour you are about to implement
   (red), then implement (green), then refactor. Table-driven tests are the
   norm.
3. **Keep changes scoped.** One coherent change per commit. Do not mix
   refactors with behaviour changes.
4. **Verify before you finish:**

   ```sh
   gofmt -l .            # must print nothing
   go vet ./...          # must be clean
   go test ./...         # must pass
   go test -race ./...   # must pass (run at least before merging)
   go test -fuzz=FuzzParse -fuzztime=30s ./internal/mailparse  # when touching parsers
   ```

5. **Commit** with a Scoped Commits style message:

   ```
   <type>(<scope>): <imperative summary>

   <body: why, not just what>
   ```

   Types: `feat`, `fix`, `security`, `test`, `refactor`, `docs`, `build`,
   `chore`. Scope is the package name, e.g. `fix(mailparse): decode RFC 2231
   filenames`. Security-relevant commits use the `security` type.

## Coding standards

- Go idiom first: `gofmt`-clean, `go vet`-clean, exported identifiers
  documented, unexported by default. Follow standard Go review comments.
- Errors: return wrapped errors with context (`fmt.Errorf("imapx: fetch %s: %w",
  ref, err)`). Never swallow errors silently; never `panic` in library code
  (panics are allowed only for programmer errors during startup wiring).
- No global mutable state. Pass dependencies explicitly; interfaces are
  defined by the consumer, kept small (1-3 methods).
- Concurrency: goroutines only where the domain needs them (network watches,
  UI main loop marshalling). Always provide cancellation via `context`.
  All shared state is mutex-protected or channel-owned. UI widgets are
  touched only on the GTK main loop (use `glib.IdleAdd`-style marshalling).
- Logging: use `log/slog`. Never log passwords, auth tokens, or full message
  bodies at info level. Debug logging of headers must redact
  `Authorization`-like fields.
- Keep `internal/model` stdlib-only. Adding any dependency requires a note
  in the commit body justifying it (size, maintenance status, license).
- Licenses of dependencies must be permissive (MIT/BSD/Apache/ISC).

## Security rules (mandatory for every change)

Mail content, server responses, and filenames are untrusted input.

1. **TLS.** IMAP/SMTP connections verify certificates and hostnames. There
   is no "accept invalid certs" switch in the UI. A config escape hatch
   (`insecure_skip_verify`) may exist but must log a loud warning at startup
   and be documented as dangerous.
2. **Header injection.** Compose must reject or strip CR/LF in any
   user-supplied header value (subject, addresses, filenames). Address
   formatting goes through `model.Address.String`, which strips CR/LF.
   Message-IDs generated by btcx must be validated (no spaces, no CR/LF).
3. **HTML is hostile.** Every HTML body passes through `sanitize` before
   display. Deny: `<script>`, `<iframe>`, `<object>`, `<embed>`, `<form>`,
   `<meta>`, `<base>`, `<link>`, event handler attributes (`on*`), `style`
   attributes containing `expression`/`url(javascript:)`, `javascript:`
   URLs, and CSS imports. Remote content (http/https images) is blocked by
   default (privacy + tracking); only `cid:` references to message parts may
   render inline.
4. **Command execution.** External editors and `password_command` run
   WITHOUT a shell: split the configured command into argv (shell-like
   quoting rules), substitute the file path as a single argument, and exec
   directly. Never build a shell string from message-derived data. Enforce a
   timeout on `password_command`.
5. **Files.** Temp files for editors/attachments are created with `os.CreateTemp`
   in a private directory (0700), mode 0600, and removed when done.
   Attachment filenames are sanitized (`filepath.Base`, strip separators and
   `..`, reject empty/absolute) before being used anywhere.
6. **Resource exhaustion.** MIME parsing enforces a maximum nesting depth,
   caps decoded attachment memory (larger parts stream to temp files), and
   fuzz tests must stay panic-free. Message list rendering uses virtualized
   lists (GTK ColumnView), never per-message widgets for unbounded lists.
7. **Lua sandbox.** Scripts cannot run shell commands, open files, or load
   modules. Only the curated `btcx.*` API and Lua's safe standard libraries
   (`string`, `table`, `math`, parts of `base`) are available. Hook execution
   is time-bounded; a failing script disables itself with a visible error,
   it never crashes btcx or blocks mail processing.
8. **Secrets.** Passwords come from the config file (warn if it is
   group/world-readable) or from `password_command`. Never write passwords
   to logs, error messages, or crash dumps.
9. **Dependency hygiene.** Run `go mod verify`; prefer updating a small
   dependency over vendoring an old one. Any parser change needs fuzz
   coverage.
10. **Pipeline review.** After changes to mailparse/compose/sanitize/render,
    run the end-to-end security pipeline tests (`internal/pipeline`) which
    feed hostile fixtures through parse -> sanitize -> render.

## Configuration (user-facing contract)

`$XDG_CONFIG_HOME` (default `~/.config`) `btcx/`:

- `config.toml` — general settings: theme, editor command, external editor
  terminal, script directory, remote-content policy, index/watch intervals.
- `accounts/<name>.toml` — one file per account: display name, email address,
  IMAP host/port/user, SMTP host/port/user, auth method, TLS mode, password
  or `password_command`, signature, sent/drafts folders.

All files are TOML. Unknown keys warn (forward compatibility); invalid values
fail validation with a precise message. Sample files live in `docs/`.

## Testing strategy

- **Unit tests** (`*_test.go` alongside each package): table-driven, cover
  happy path, edge cases, and error paths. Aim for high coverage of parsing,
  composing, config, and script APIs. `go test -cover ./...` is the bar.
- **Fuzz tests** (`Fuzz*` in mailparse and render): seed corpora with
  realistic and hostile messages (deeply nested multiparts, header
  injection, encoded-word bombs, malformed MIME). Must never panic or hang.
- **Integration tests**: IMAP and SMTP against in-process test servers
  (go-imap/go-smtp server packages), covering login, list, fetch, store,
  idle, send. Mark slow tests with `testing.Short()` guards where needed.
- **Pipeline tests** (`internal/pipeline`): raw hostile RFC 5322 -> parse ->
  sanitize -> render assertions (no script/remote content survives).
- **UI**: keep logic in `app/` so it is unit-testable; `ui/` gets smoke
  tests that are skipped when no display is available.

## Git conventions

- Branch: `master` is protected and always green. Feature work in
  `feat/<slug>` or `fix/<slug>` branches, merged when `go test -race ./...`
  passes.
- Commits follow the Scoped Commits format shown above. Keep history
  readable: squash fixup commits before merging.
- Release tags: `vMAJOR.MINOR.PATCH` (semver). `buildinfo` embeds version,
  commit, and build date via `-ldflags`.

## Adding a feature (checklist)

1. Write/extend tests for the target packages (red).
2. Implement in the lowest appropriate layer (model -> plumbing -> app -> ui).
3. Update this file and `docs/` if the feature changes user-visible
   behaviour or config keys.
4. Add pipeline/security tests if the feature touches mail content.
5. Run the full verification suite (fmt, vet, test, race, fuzz where
   relevant) and commit with a scoped message.
