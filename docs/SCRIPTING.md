# btcx Lua scripting

btcx can run user Lua scripts to automate and customise mail handling.
Scripts live in the script directory (`script_dir` in `config.toml`, default
`~/.config/btcx/scripts/`), one or more `*.lua` files, executed in filename
order.

Scripting is **sandboxed**: scripts cannot run shell commands, read or write
files, load modules, or reach the Lua debug facilities. Each script file
runs in its own isolated Lua state with a bounded execution budget.

## Hooks

Define any of these global functions; undefined hooks are simply skipped:

```lua
-- Runs once when btcx starts.
function on_startup()
  btcx.log("info", "hello from btcx " .. btcx.version())
end

-- Runs for each newly arrived message.
function on_new_mail(envelope)
  if envelope.subject:lower():find("newsletter") then
    btcx.log("debug", "newsletter from " .. tostring(envelope.from[1] and envelope.from[1].addr or "?"))
  end
end

-- Runs before a message is sent. Return false to cancel the send.
function before_send(message)
  if message.subject == "" then
    btcx.log("warn", "refusing to send without a subject")
    return false
  end
  return true
end

-- Runs after a send attempt. sent is a boolean, err_msg empty on success.
function after_send(message, sent, err_msg)
  btcx.log("info", "send result: " .. tostring(sent))
end
```

## Data passed to hooks

- `envelope` (on_new_mail): `message_id`, `subject`, `date` (RFC 3339
  string), `mailbox`, `uid`, `uid_validity`, `size`, `has_attachments`,
  `from`, `to`, `cc` (arrays of `{name, addr}`), and `flags` (table of
  booleans: `seen`, `answered`, `flagged`, `deleted`, `draft`, `recent`).
- `message` (before_send/after_send): `subject`, `plain`, `html`, `from`,
  `to`, `cc` (address arrays) and `attachments` (array of `{filename,
  content_type, content_id, size, inline}`).

All fields are informational: mutating the tables does not change the
message btcx sends.

## btcx API

| Function | Description |
|---|---|
| `btcx.log(level, msg)` | Log `"debug"`/`"info"`/`"warn"`/`"error"`. `msg` is truncated at 8 KiB. |
| `btcx.version()` | btcx version string. |

`print` is redirected to `btcx.log("info", ...)`.

## Available Lua

`string`, `table`, `math`, and safe `base` functions (`pcall`, `ipairs`,
`pairs`, `tonumber`, `tostring`, `type`, `error`, `assert`, `select`,
`setmetatable`, `next`, raw accessors). Everything else (`os`, `io`,
`require`, `load`, `debug`, ...) is absent.

## Limits

- Each hook call has an instruction budget (default 2 seconds of VM work);
  exceeding it aborts the call and **disables that script** for the session,
  with a warning in the UI. A broken script can never hang btcx or block
  mail processing.
- Single built-in calls that would allocate more than 8 MiB (e.g.
  `string.rep`) raise an error instead.
- Errors in scripts are captured as warnings; they never crash btcx.

## Example: notify on important mail

```lua
function on_new_mail(envelope)
  local from = envelope.from[1] and envelope.from[1].addr or ""
  if from:match("@example%.org$") then
    btcx.log("warn", "mail from example.org: " .. envelope.subject)
  end
end
```
