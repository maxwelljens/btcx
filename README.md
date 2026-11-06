# btcx — lean but powerful mail user agent

btcx is a modern, production-grade mail user agent for Linux. It does mail —
composing, reading, searching, watching — and nothing else. No calendar, no
contacts, no feed reader. Thunderbird's competence without the bulk. HTML
mail renders for real in a hardened WebKitGTK view: JavaScript off, zero
network requests, links open in your browser.

## Highlights

- **Guided first run**: launch with no configuration and btcx writes
  commented example config files and opens a setup wizard — detect your
  servers, test the connection, create the account, done. Hand-editing the
  files works too, always with an example at hand.
- **Modern UI** (GTK4 via gotk4): folder sidebar, fast message list, reader
  pane with safe HTML rendering, compose window.
- **Compose and read** plain-text and HTML mail with attachments and inline
  images.
- **HTML mail rendered for real**: newsletters and formatted mail display
  with full layout and inline images in a hardened WebKitGTK view —
  JavaScript and plugins off, every image served as an inlined data URI
  (the view never touches the network), remote images still blocked by
  default until you opt in, links open in your external browser.
- **Safe HTML**: every remote body is sanitized before display; remote
  images (tracking pixels) are blocked by default.
- **Built-in editor** or your own: nvim, vim, helix, whatever `$VISUAL`
  points at.
- **New-mail watching** via IMAP IDLE — the list updates live.
- **Plain-text configuration** in `~/.config/btcx/*.toml`.
- **Lua scripting** in a sandboxed environment for automation hooks.
- **Static-ish deployment**: one binary; the only shared libraries are the
  GTK4 stack your desktop already ships.

## Requirements

- Linux with GTK4 and **WebKitGTK 6** (the GTK4 flavour — e.g.
  `libwebkitgtk-6.0-dev` on Debian/Ubuntu, `webkitgtk6-devel` on Fedora).
  Note `webkit2gtk-4.1` will NOT work: it links GTK3.
- Build: Go 1.24+, a C toolchain, and the GTK4 development headers
  (`libgtk-4-dev` on Debian/Ubuntu, `gtk4-devel` on Fedora).

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

## First run

Launch `btcx` with no configuration and:

1. btcx creates `~/.config/btcx/` with a fully commented `config.toml`,
   `accounts/example.toml.example` (a complete account-setup walkthrough
   including real Gmail/Outlook/Fastmail/Yahoo settings and both password
   options) and `scripts/example.lua.example`.
2. A setup window walks you through creating an account: your address,
   incoming (IMAP) and outgoing (SMTP) servers — with a "Detect settings"
   guess based on your domain — and a password (stored in the file, or
   supplied by a command such as `pass`).
3. "Test connection" verifies IMAP login and SMTP submission live before
   anything is written; "Create account" writes `~/.config/btcx/accounts/
   <name>.toml` (mode 600) and connects.

Prefer editing files? Everything the wizard writes is plain commented
TOML; open the config folder from the welcome screen, copy the example,
edit, and press "Reload configuration".

## Configuration

Everything lives under `~/.config/btcx/` (or `$XDG_CONFIG_HOME/btcx`):

- `config.toml` — theme, external editor, watching, remote-content policy,
  scripting, logging.
- `accounts/<name>.toml` — IMAP/SMTP servers, credentials, folders,
  signature. Passwords come from the file (keep it `chmod 600`) or from a
  `password_command` such as `pass show mail/example.org` — btcx runs it
  without a shell.

The example files are generated from the templates in
`internal/config/templates/` on first launch and never overwritten.

## Testing

```sh
gofmt -l .                    # must print nothing
golangci-lint run ./...       # standard linters + gosec (config: .golangci.yml)
go test ./...                 # unit + integration (in-process IMAP/SMTP servers)
go test -race ./...
go test ./internal/pipeline/...   # hostile-mail security pipeline
```

## Design

See `AGENTS.md` for architecture, development workflow, and the security
rules every change must satisfy.

## License

MIT
