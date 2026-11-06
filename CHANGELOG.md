# Changelog

All notable changes to btcx are documented in this file.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and the project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- Domain model for messages, headers, addresses, envelopes, mailboxes and
  refs (`internal/model`), with RFC 2047 decoding and CR/LF-safe address
  rendering.
- TOML configuration (`~/.config/btcx/config.toml`,
  `~/.config/btcx/accounts/<name>.toml`) with secure defaults, strict
  validation, unknown-key warnings, per-server credentials and shell-free
  `password_command` resolution.
- RFC 5322/MIME parser with hostile-input caps (depth, parts, decoded size),
  attachment and inline-image extraction, RFC 2231/2047 filenames, and fuzz
  coverage.
- Message composer producing correct MIME trees (plain, HTML,
  multipart/alternative/related/mixed), reply/forward threading and quoting,
  header-injection rejection, envelope-only Bcc, and round-trip tests.
- HTML sanitization and Pango markup rendering for safe mail display, with
  remote images blocked by default and fuzz coverage.
- IMAP client (TLS-only, UID operations, IDLE new-mail watching, bounded
  connect phase and LIST) and SMTP submission client (TLS-before-AUTH, typed
  recipient errors, bounded connect phase), tested against in-process servers.
- Sandboxed Lua scripting with hook budgets, capped builtins and a curated
  `btcx.*` API (see `docs/SCRIPTING.md`).
- External-editor integration with secure draft files and shell-free
  command execution.
- GTK4 user interface: folder sidebar, ColumnView message list, reader pane
  with remote-content opt-in bar, inline-image strip, attachment saving,
  compose window with external-editor sessions, keyboard shortcuts and
  theme support (`internal/ui`, `internal/app`, `cmd/btcx`).
- Guided first-run experience: on a fresh launch btcx generates commented
  example configuration (general settings, a full account-setup walkthrough
  with real provider settings and password guidance, and an example Lua
  script) and opens a setup wizard with settings detection, live IMAP/SMTP
  connection testing, one-click account creation and a hand-editing path;
  a welcome empty state keeps the app usable without configuration.
  `Reload` picks up hand-edited account files without a restart.
- End-to-end security pipeline test suite (hostile mail through
  parse → sanitize → render).

### Security

- Robustness against broken servers: a server that crashes mid-fetch while
  generating one message's `BODYSTRUCTURE` (observed with Dovecot answering
  `* BYE Internal error occurred`) no longer bricks the mailbox — envelopes
  are refetched in bounded groups after a reconnect, degrading only the
  affected messages to "no attachment info" and reporting the degradation
  honestly, instead of failing the whole load.
- Remote-image fetches refuse non-public addresses (loopback, RFC 1918,
  CGNAT, link-local/cloud-metadata, unique-local, benchmarking, reserved,
  NAT64), validate the exact resolved IP at dial time (no DNS-rebinding
  window) and ignore environment proxies.
- Parsed-message cache keyed by account and ref (cross-account isolation),
  byte-budgeted with eviction (bounded memory under server-controlled
  content).
- Image decoding refuses pixel bombs (header dimension probe) and
  downscales during decode.
- Lua sandbox caps single-call allocations (`string.rep`, `string.format`).
- Connect-phase timeouts with connection deadlines on IMAP and SMTP against
  stalling servers; IMAP LIST collection capped.
- Stale asynchronous UI results are dropped (account/folder guards) and
  account switches clear the reader, so actions can never target another
  account's mail.
- Inline non-image `Content-ID` parts stay saveable (no hidden
  attachments).
- Forwarded `message/rfc822` parts preserve the original Message-ID and
  Date.
