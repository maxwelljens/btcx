---
name: btcx-mail-standards
description: Practical RFC reference for the mail standards btcx implements — RFC 5322 headers, MIME structure, encoded words, IMAP and SMTP behaviour and gotchas. Use when working on mailparse, compose, imapx, smtpx, or debugging protocol/format issues.
---

# Mail standards reference for btcx

Compact, implementation-focused. Verify odd cases against the RFCs
(rfc-editor.org) when behaviour is unclear.

## RFC 5322 message format

- Headers: `Key: value`, folding via CRLF + WSP continuation. Parsing must
  unfold; composing must fold at 78 octets where possible.
- Structured headers we honour: From, To, Cc, Bcc, Reply-To, Subject, Date,
  Message-ID, In-Reply-To, References, MIME-Version, Content-*.
- `Message-ID` format `<local@domain>` — bare token (no brackets) is stored
  in `model.Message.MessageID()`; brackets stripped.
- Threading: `References` is a space-separated chain, oldest first;
  `In-Reply-To` holds one or more IDs. `Re:` prefix added once only.
- Date: RFC 5322 date-time (`mail.ParseDate` in stdlib handles obsolete
  forms; always emit `time.RFC1123Z` format when composing).
- Display names and phrases: RFC 2047 encoded-words (`=?UTF-8?B?...?=`),
  may appear mid-string; decode with `mime.WordDecoder`, encode with
  `mime.QEncoding`/`BEncoding` (or let go-message handle it).

## MIME (RFC 2045–2049, 2231, 6532)

- Content-Type: `type/subtype; param=value`. Parameters may be RFC 2231
  continuations (`filename*0*=`) and charset-tagged (`filename*=utf-8''%20`).
  go-message's `mail` package decodes these; don't hand-roll.
- Content-Transfer-Encoding: 7bit, 8bit, binary, quoted-printable, base64.
- Canonical structure btcx emits:
  - plain only: `text/plain; charset=UTF-8`
  - plain+html: `multipart/alternative` (plain first, html second)
  - attachments: `multipart/mixed` wrapping the above
  - inline images: `multipart/related` (html + `Content-ID` parts) — the
    HTML references them as `<img src="cid:...">` where the cid value has
    NO angle brackets in `model.Attachment.ContentID`.
- `multipart/digest` sub-messages are `message/rfc822` parts — treat as
  attachments (keep raw bytes).
- HTML mail should always include a text/plain alternative when btcx
  generates it (HTML -> text via `sanitize.PlainText`).
- 8-bit headers: RFC 6532 allows UTF-8 raw in headers; prefer RFC 2047
  encoding on output for maximum interoperability.

## IMAP (RFC 3501, 9051 + extensions)

- UIDs are valid only within a UIDVALIDITY epoch — always store the
  `model.Ref{Mailbox, UIDValidity, UID}` triple.
- Fetch envelopes with `ENVELOPE FLAGS RFC822.SIZE BODYSTRUCTURE` (structure
  tells us about attachments cheaply); fetch bodies with `BODY.PEEK[]` so
  reads don't set `\Seen` implicitly. Setting `\Seen` happens via STORE.
- IDLE (RFC 2177) for new-mail watching: send `IDLE`, wait for `* n EXISTS`,
  finish with `DONE`. Re-issue IDLE after ~29 minutes (server timeout).
- APPEND for saving drafts/sent copies. UIDPLUS for atomic move/copy when
  advertised; fall back to COPY + STORE \Deleted + EXPUNGE semantics carefully
  (never expunge messages the user didn't delete).
- Literal sizes from the server are untrusted — enforce caps client-side.

## SMTP submission (RFC 6409, 5321)

- Port 587 STARTTLS, 465 implicit TLS, 25 discouraged.
- EHLO -> AUTH (PLAIN/LOGIN only after TLS is up) -> MAIL FROM -> RCPT TO ->
  DATA. Never send AUTH before TLS.
- Dot-stuffing: lines starting with "." are stuffed by the client library;
  message bodies ending with "\r\n.\r\n" must round-trip (test it).
- 8BITMIME: advertise when sending UTF-8; fall back to quoted-printable.

## Charset handling

- Decode: `github.com/emersion/go-message/charset` (blank import) covers the
  common set; unknown charsets must degrade gracefully, never panic.
- Emit UTF-8 everywhere (charset=UTF-8 in Content-Type).
