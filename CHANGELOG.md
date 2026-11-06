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
  validation, unknown-key warnings and shell-free `password_command`
  resolution.
- RFC 5322/MIME parser with hostile-input caps (depth, parts, decoded size),
  attachment and inline-image extraction, RFC 2231/2047 filenames, and fuzz
  coverage.
- Message composer producing correct MIME trees (plain, HTML,
  multipart/alternative/related/mixed), reply/forward threading and quoting,
  header-injection rejection, and round-trip tests.
- HTML sanitization and Pango markup rendering for safe mail display, with
  remote-content blocking by default and fuzz coverage.
- IMAP client (TLS-only, UID operations, IDLE new-mail watching) and SMTP
  submission client (TLS-before-AUTH, typed recipient errors), both tested
  against in-process servers.
- Sandboxed Lua scripting with hook budgets, capped builtins and a curated
  `btcx.*` API.
- External-editor integration with secure draft files and shell-free
  command execution.
- End-to-end security pipeline test suite (hostile mail through
  parse → sanitize → render).
