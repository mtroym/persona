# Script commands: `personad ask` / `personad notify`

Let scripts, CI, long-running jobs, and programs that don't run in herdr reach the phone directly:

- `personad ask "question"`: blocks until someone answers on the phone; the answer becomes the exit code and stdout.
- `personad notify "title" [body]`: sends a notification, best effort by default.

Inspired by the commands of the same name in moshi-hook. Like `personad hook`, both commands only talk to the local node (`~/.persona/node.sock`, 0600, same system user).
They never touch the hub token and need no network configuration: if node is connected to the hub they work; if not, they follow the exit codes below.

## Commands and exit codes

```
personad ask "question" [--options a,b,c] [--timeout 10m] [--require-remote]
personad notify "title" [body] [--silent] [--error] [--strict]
```

`ask`'s flags can go before or after the question; if the question starts with `-`, put it after `--`. `--timeout` takes seconds (`90`) or a duration (`10m`); the default is 30 minutes (same as hook), the maximum 24 hours.
`--options` is comma-separated, so an option can't contain a comma. Empty and duplicate items are dropped; at most 12 items, each at most 100 characters.

| Exit code | `ask` | `notify` |
| --- | --- | --- |
| 0 | Approved (no options), or an option was picked (printed to stdout, one line) | Always 0 (best effort); with `--strict`, it means at least one push device or WebSocket connection was selected |
| 1 | Denied; or "Hand back to computer" was tapped on the phone and there's no one to ask in the terminal | Only with `--strict`: the hub did not accept it (no node, hub not connected, hub too old), or no recipient was selected: `muted` (session muted), `no_device` (no reachable push device or capable WebSocket connection), `filtered` (all reachable recipients declined by notification preferences or desktop suppression) |
| 2 | Usage error (no question, too many options, invalid `--timeout`…) | Usage error (no title, more than two arguments…). This is a mistake in the script, so it is not covered by "best effort" |
| 3 | Timed out, nobody answered | - |
| 4 | The phone can't be reached: no node, node not connected to the hub, hub didn't accept it | - |
| 130 | Ctrl-C / SIGTERM; the card on the phone is withdrawn too | - |

- On exit code 4 or "Hand back to computer", if stdin is a real terminal (not `/dev/null` or a pipe) and `--require-remote` is not set, it falls back to asking in the terminal: a numbered list of options (type the number or the option text),
  or `[y/N]` without options. EOF counts as deny. A timeout or a denial is an answer, so neither falls back to the terminal.
- Without `--strict`, a failure only writes one line to stderr, `Not sent: <reason>` (localized), and the exit code stays 0, so a `set -e` script is never broken by a notification.
- The "confirmation" of `--strict` means it was handed to push or a capable WebSocket connection (`{"sent":true}`), not that the phone displayed it: notification delivery has no receipt anyway (see `../protocol/protocol.md` ("Persistent connection notifications")).

## Path

```
personad ask / notify
  └─ node.sock  {"kind":"ask"|"notify","input":{…},"pane":"…","attempt":"…"}   one request / one reply per connection, one JSON line each
       └─ node (node/scriptcli.go)
            ├─ ask    → hub request interaction.create (same path as hook) → blocks until answered / expired
            └─ notify → hub request notify (deliverNotify in hub/notify_api.go, shared with POST /api/v1/notify)
```

node's replies:

- `ask`: `{"status":"answered|denied|dismissed|timeout|unavailable|invalid","answer":"…","error":"…","retry":true}`.
  `retry` means node is exiting (hub or node restart); the command reconnects to the next node with the same `attempt`, for at most `nodeclient.ResumeWait` (2 minutes), exactly like hook.
- `notify`: `{"sent":true|false,"reason":"muted|no_device|filtered","error":"…"}` (the reason describes hub-side suppression).

An old node doesn't know `ask` / `notify` (replies `{"error":"unknown kind"}`), and the command treats that as unreachable. With a new node and an old hub, the `notify` request fails with unknown method and is likewise treated as not sent.

### What `ask` looks like on clients

It reuses existing question and permission card shapes. Cards without a session open directly on supported clients, including the web (#1882); see "Attribution" below.

| Command | `interaction.kind` | Card |
| --- | --- | --- |
| With `--options` | `question`, one `Question` (`header` is `脚本` ("Script"), `multi_select: false`, one `label` per option) | The same question card as AskUserQuestion; the answer is `answers[<original question text>]`, the option or free text (when the card supports "Other") |
| Without `--options` | `permission`, `summary` is the question | Allow / Deny card; the notification buttons can answer it directly |

Both carry `tool_name: "personad_ask"`, `agent: "script"`, `agent_label: "脚本"` ("Script"; this is how runtime cards label "who is asking", `runtimes.md`):
the notification title is "Script Has a Question" / "Script Needs Your Permission", and the card offers no "Always Allow", so it is easy to tell apart from Claude's own permission cards.
`resumable: true`, and the id is derived from `attempt`: when the hub or node restarts, the card on the phone isn't lost, and the reconnected command reattaches to the same card (`../protocol/protocol.md` ("Interactions survive restarts")).
When the command is killed (Ctrl-C), node sends `interaction.cancel` and the card is withdrawn.

### Attribution

The command reads the caller's pane with `nodeclient.HookPane` (`HERDR_PANE_ID`, then herdr-core's `HC_TERMINAL_ID`, Orca's `ORCA_PANE_KEY`) and sends it to node with the request.
node uses the "pane → session" table learned from hooks (`bindPane`) to find the Claude session in that pane:

- Found: the card / notification is attached to that session (`session_id`), the card's subtitle is the session title, tapping the notification opens that session, and `notify` sends nothing when that session is muted.
- Not found (Claude Code never ran in the pane, no hook has fired yet, or it isn't in herdr): `session_id` is empty, the card subtitle is the name of the calling directory, and the notification subtitle is the machine name.
  iOS / Mac / watch (#4059), Android and web (#1882) can still answer: explicitly sessionless cards do not check session-source approval permissions. On the web, Overview and Inbox open `/requests/<id>` with the existing question or permission card; ordinary session cards still require that session's approval capability. New requests notify individually, using the device's existing notification preferences, and opening one uses the same request route.
  Tapping there or in the Inbox opens the card directly (no chat exists to open).
- `ask` also carries the calling directory (`cwd`). `notify` carries no path.

### Preferences and muting

`notify` uses the hub's existing "send notification" path (the same function `deliverNotify` as extensions' `POST /api/v1/notify`): a device that turned off "Needs You" (`prefs.input`) doesn't get it, and a muted session / space sends nothing (`reason: "muted"`).
Delivery goes through APNs (direct or via the push relay) and the persistent connection event of `ws_notify` (`kind: "alert"`). If neither channel selects a recipient, the hub returns `sent: false`: `no_device` when no device has a usable push route and no capable connection is online; `filtered` when reachable recipients exist but all are excluded by "Needs You", manager notification switches, "Phone Muted", or the open-desktop rule. A selection on either channel is enough for `sent: true`; asynchronous APNs or relay failures do not change that reply.

- `--error`: `interruption-level: time-sensitive`, and the title gets a "⚠️ " prefix; the default is `active`.
- `--silent`: no `sound` in `aps`. `notifyRequest` gained an optional `silent` field for this, which the HTTP API also accepts. Through the push relay the phone takes the sound from the sealed `aps`, so it is quiet there too; only the "New message" fallback of a push that can't be opened rings (`../operations/push-relay.md`).
- The title is at most 120 characters, the body at most 1000; anything longer is truncated in node, without an error.
  The push itself is bounded in bytes (3500 encoded), so the hub then cuts the body further, by characters with a trailing "…", until it fits (`fitBody`): 1000 emoji or escaped `<` `>` `&` are still delivered. Only a title too large on its own is refused (`notification is too large`). Extensions' `POST /api/v1/notify` is not cut: it still gets a 400 over the limit.

## Clients

- Notifications: `kind: "alert"` and `aps` already exist; no changes needed.
- Question / permission cards: the shapes already exist; no changes needed. Possible but not required: if the card shows "Script" from `agent_label`, check that iOS's `Interaction.Agent` falls back to `agent_label` for unknown values (runtime cards already do).
- **Not checked on a device / simulator**: "Unverified" below lists what to look at.

## Security and privacy

- node.sock is a 0600 unix socket; only processes of the same system user can connect, which is the same trust boundary as hook and MCP. Any process of this user can pop things up on the phone with `ask` / `notify`, so cards are clearly labeled "Script".
- An `ask` with options is an ordinary question card: the assistant's existing rules for `interaction_answer` apply to it too (the assistant may answer on the user's behalf when allowed, `docs/assistant/assistant.md`), and the command can't see `answered_by` in the answer. To make sure only a human can answer, use the form without options (the assistant can't answer permission cards).
- The question, body, and calling directory name go into the phone's notifications (end-to-end encrypted when going through the push relay, `../operations/push-relay.md`) and interaction history; don't put secrets in them. `notify` carries no path.

## Verification

- Unit tests: `server/internal/scriptcli` (exit codes, flags, terminal fallback, Ctrl-C, `--strict`); `server/internal/node/scriptcli_test.go` (fake hub + fake socket: card shape, attribution, timeout, withdrawal, restart reconnect, `notify` parameters and truncation);
  `server/internal/hub/notify_api_test.go` (`silent`, `notify` from node: muting, preferences, no extension fields, the body cut to fit the push).
- The real `personad` binary was run against a fake node socket to check the exit codes and wire JSON of `ask` / `notify` (without touching a real node or phone).
- Not done: end-to-end against a real hub + phone (by convention, tests don't send real notifications).

Unverified: how "Script" cards actually display on iOS / Android / web; where cards with an empty `session_id` show up in the Agents page / Inbox (the hub handles them by `machine_id + id`; clients not checked);
whether `--silent` (no `sound`) is actually silent on a real device under APNs; notification delivery has no receipt.
