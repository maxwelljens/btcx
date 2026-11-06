---
name: btcx-security
description: Security threat model and mandatory review checklist for btcx. Use when touching mail parsing, HTML rendering, network clients, command execution, file handling, scripting, or when reviewing any change for security concerns.
---

# btcx security review checklist

Mail content, server responses, config values, and filenames are hostile
input. Run this checklist against every change that touches them. The
matching rules live in `AGENTS.md` § Security; this skill is the review
procedure.

## Threat model (who attacks us)

1. **Malicious e-mail** — the primary threat: crafted MIME, header
   injection, HTML/XSS, tracking pixels, decompression/nesting bombs,
   malicious attachments (filenames, path traversal), terminal escape
   sequences in bodies.
2. **Malicious/compromised server** — hostile IMAP/SMTP responses, huge
   literals, response injection, downgrade attempts.
3. **Local attacker** — config file readability, temp file races, editor
   command injection via crafted subject/body written to temp files.
4. **Malicious script** — Lua scripts trying to escape the sandbox.

## Checklist per area

**Parsing (mailparse, model)**
- [ ] No panics on malformed input (fuzz targets exist and pass).
- [ ] Nesting depth, part count, and decoded-size caps enforced.
- [ ] Filenames: RFC 2231/2047 decoded, then `filepath.Base`, separators
      and `..` stripped, empty/absolute rejected before any filesystem use.
- [ ] Header values decoded but preserved; CR/LF cannot leak into new headers.

**HTML (sanitize, render)**
- [ ] Every body passes `sanitize.HTML` before rendering. No `Unsafe()`.
- [ ] Denied: script/iframe/object/embed/form/meta/base/link/svg/math,
      `on*` attrs, `style` attrs, `javascript:`/`data:` URLs (case- and
      whitespace-smuggling tested).
- [ ] Remote images blocked by default; only `cid:` renders inline.
- [ ] Renderer escapes all text; output is well-formed XML (Pango markup).

**Compose**
- [ ] CR/LF/NUL rejected in Subject, addresses, filenames (ValidateHeader).
- [ ] Message-ID minted from crypto/rand, validated charset.
- [ ] No SMTP command smuggling through bodies (dot-stuffing handled by
      the SMTP client; verify tests cover leading "From " and "\r\n.\r\n").

**Network (imapx, smtpx)**
- [ ] TLS verifies certificates + hostnames. `insecure_skip_verify` only
      via explicit config, with a loud startup warning.
- [ ] No plaintext authentication ever (`tls = "none"` rejected in config).
- [ ] All reads bounded (timeouts, size limits); context cancellation works.

**Command execution (editor, config.ResolvePassword)**
- [ ] Executed WITHOUT a shell: argv split with shell-like quoting only.
- [ ] Message-derived data never reaches a shell string.
- [ ] Timeout enforced; stdout/stderr never contain secrets in errors.

**Files**
- [ ] Temp files: `os.CreateTemp`, private dir 0700, files 0600, cleaned up.
- [ ] Attachment save path uses sanitized filename + user-chosen directory.

**Scripting (script)**
- [ ] Sandbox: no os/io/package/debug; no `load`/`dofile`/`loadfile`.
- [ ] Hook execution time-bounded; script failure disables the script with a
      visible error — never crashes btcx or blocks mail processing.

**Secrets**
- [ ] Passwords never logged, never in errors; config with static password
      that is group/world-readable produces a warning.

## Pipeline tests

`internal/pipeline` feeds hostile fixtures through
parse -> sanitize -> render. After any change in those packages, run:

```sh
go test ./internal/pipeline/...
```

Add a fixture whenever a new attack shape is discovered.
