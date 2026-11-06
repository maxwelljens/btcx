---
name: btcx-dev
description: Development workflow, coding standards, and verification checklist for the btcx mail user agent. Use before writing, reviewing, or refactoring any code in this repository.
---

# btcx development workflow

Read `AGENTS.md` at the repository root first — it is authoritative. This
skill distills the day-to-day loop.

## Where code goes

Dependency direction (never violate): `ui -> app -> {mailparse, compose,
imapx, smtpx, script, editor, config} -> model`.

- `internal/model` — shared domain types. Standard library ONLY.
- Pure logic lives below `app`; `ui` (gotk4) stays thin and dumb.
- Consumer-defined interfaces, 1–3 methods. No global mutable state.
- New dependency? Justify it in the commit body (size, maintenance, license
  must be permissive). Update `AGENTS.md` if architecture changes.

## The loop

1. Write the failing test first (table-driven is the norm).
2. Implement the minimum that passes.
3. Refactor while green.
4. Verify before finishing:

```sh
gofmt -l .              # must print nothing
golangci-lint run ./...  # must be clean (.golangci.yml: standard + gosec)
go vet ./...             # clean
go test ./...            # pass
go test -race ./...      # pass (at least before merge)
```

When touching parsers (mailparse, render, sanitize), also run the local
fuzz targets for ~10–30s each.

## Style rules

- Wrapped errors with context: `fmt.Errorf("imapx: fetch %s: %w", ref, err)`.
  Never swallow errors; never panic in library code.
- `log/slog` for logging. Never log passwords, tokens, or full bodies at
  info level.
- Context on every network/long-running call; all goroutines cancellable.
- GTK widgets are touched only on the GTK main loop (marshal with
  `glib.IdleAdd` equivalents). UI-adjacent logic goes in `app` so it can be
  unit-tested headless.
- Tests assert behaviour, not implementation details. Use real fixtures
  (raw RFC 5322 bytes) for parser tests.

## Commits

Scoped Commits format:

```
<type>(<scope>): <imperative summary>

<body: why, not just what>
```

Types: feat, fix, security, test, refactor, docs, build, chore. Scope =
package name. Security-relevant changes use `security`. One coherent change
per commit; squash fixups before merging. `master` stays green.
