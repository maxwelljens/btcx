# btcx — lean but powerful mail user agent

btcx is a modern, production-grade mail user agent for Linux. It does mail —
composing, reading, searching, watching — and nothing else. No calendar, no
contacts, no feed reader, no WebKit. Thunderbird's competence without the
bulk.

## Highlights

- **Modern UI** (GTK4 via gotk4): folder sidebar, fast message list, reader
  pane with safe HTML rendering, compose window.
- **Compose and read** plain-text and HTML mail with attachments and inline
  images.
- **Safe HTML**: every remote body is sanitized; remote images (tracking
  pixels) are blocked by default.
- **Built-in editor** or your own: nvim, vim, helix, whatever `$VISUAL`
  points at.
- **New-mail watching** via IMAP IDLE — the list updates live.
- **Plain-text configuration** in `~/.config/btcx/*.toml`.
- **Lua scripting** in a sandboxed environment for automation hooks.
- **Static-ish deployment**: one binary; the only shared libraries are the
  GTK4 stack your desktop already ships.

## Requirements

- Linux with GTK4 (runtime). Build needs Go 1.24+, a C toolchain, and GTK4
  development headers (`libgtk-4-dev` on Debian/Ubuntu, `gtk4-devel` on
  Fedora).

## Building

```sh
git clone <repo> btcx && cd btcx
go build -o btcx ./cmd/btcx
```

With version metadata:

```sh
go build -ldflags "-X btcx/internal/buildinfo.version=v0.1.0 \
  -X btcx/internal/buildinfo.commit=$(git rev-parse --short HEAD) \
  -X btcx/internal/buildinfo.date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -o btcx ./cmd/btcx
```

## Configuration

Two TOML files under `~/.config/btcx/` (see `docs/*.example`):

- `config.toml` — theme, external editor, watching, remote-content policy.
- `accounts/<name>.toml` — IMAP/SMTP servers, credentials, folders,
  signature. Passwords come from the file (keep it `chmod 600`) or from a
  `password_command` such as `pass show mail/example.org` — btcx runs it
  without a shell.

## Testing

```sh
go test ./...          # unit + integration (in-process IMAP/SMTP servers)
go test -race ./...
go test ./internal/pipeline/...   # hostile-mail security pipeline
```

## Design

See `AGENTS.md` for architecture, development workflow, and the security
rules every change must satisfy.

## License

MIT
