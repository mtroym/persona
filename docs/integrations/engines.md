# Engines: herdr, Herdr App, Orca, Paseo

**One engine per computer**: Persona sees and drives Claude Code / Codex through an engine. Each computer selects one; Agents shows live sessions only from it.
Sessions elsewhere (other engines, ordinary terminals) appear only as "Not running" history. Default herdr preserves existing machines without an explicit choice.

Exception: **"herdr + Herdr App (side by side)"**. Choose this when both classic herdr and Herdr App operate on one computer (typically local terminal herdr,
plus Herdr App on another computer opening terminals through remote). Sessions merge; boundaries are in "Side by side" below.

On computers with **botmux**, its sessions appear alongside the selected engine ("botmux" below). It is not an engine option and needs no selection.

**Paseo** is terminal-free: it runs Agents through provider SDKs, with node connected through a bridge; see `docs/integrations/paseo.md`.

## Host sets (#2576)

A computer can **enable several engines at once**, any combination of herdr, Herdr App, Orca and Paseo, or none at all, plus a **default engine**:
where sessions started from the phone, and sessions continued from there, go. Each single choice above is a special case of a set: herdr is `{herdr}`, and
"herdr + Herdr App" is `{herdr, herdr-core}` with herdr the default. API and storage: `docs/protocol/protocol.md` "Engines" ("Host sets").
The web is wired: the machine page's Engine section is a checklist with "Make default", New Agent has "Open in", and a session live in
two hosts shows "Also open in …" above the composer. iOS and Android still pick one engine, and the single engine picked keeps working.

- **Only enabled hosts are registered.** An engine that is not checked gets no client, no polling and no event subscription; it is only probed once in
  `node.info` (for "Detected"). An engine that is not installed or not running does not slow the others down.
- **Hosts do not hold each other up.** Each host refreshes in parallel with its own timeout, and its events refresh only itself. A host that is down
  (unreachable, `not_running`) clears only its own sessions; the others are listed and driven as usual. This is the same mechanism as herdr-both
  (`docs/plans/plan-orca-support.md` "Side-by-side engines").
- **Default engine**: new sessions, continuing a session that is not running, renaming a space and the mode menus go to the default engine. Per-pane
  operations (reading the screen, sending, keys, closing) still go to the host the pane belongs to. Changing only the default resets nothing; changing
  the set itself works like switching engines did (live terminal views end, the session list is rebuilt). With Orca the default, a Claude session that is not
  running is continued in Orca, any other agent's is refused (`orca_resume`); with Paseo the default, new and continued sessions go to Paseo.
- **The empty set**: no terminal host. Agents shows transcripts, runtimes (DSH, pi, opencode) and botmux sessions only; starting or continuing a
  session from the phone is refused with "这台电脑没启用终端引擎" (`engine_missing`) and nothing is sent.
- **One session live in two hosts** (say herdr `--resume`d a session that is still open in Orca): it is listed once and owned by one host
  (`node/ownership.go`). The owner is, in order: (1) the pane whose claude process runs it (`~/.claude/sessions/<pid>.json`); (2) the pane whose last
  SessionStart hook named it; (3) the default engine's pane; (4) otherwise herdr, Herdr App, Orca in that order. Messages, keys and interrupts go to
  the owner only, and its state is the only one used; the row's `also_open_in` lists the other hosts that have it open, so clients can warn of the
  conflict (the web does; the apps do not yet). Two panes of the same host, or a live pane and an offline row, behave as before.
- **Performance** (2026-11-06, one Mac with herdr 0.9.5 and Orca 1.4.250, `go test -tags enginehostslive -run EngineHostsLive ./internal/node/ -v`,
  40 rounds each, Orca refreshed once a second meanwhile as its watcher does; three runs, a typical one below, the other two of the same order):

  | | herdr only | herdr + Orca |
  | --- | --- | --- |
  | herdr send | p50 0.5 ms · p95 1.7 ms · max 2.5 ms | p50 0.5 ms · p95 1.1 ms · max 3.3 ms |
  | refresh after a herdr event (herdr only) | p50 82 ms · p95 205 ms · max 267 ms | p50 92 ms · p95 320 ms · max 430 ms |
  | refresh of every host | p50 62 ms · p95 125 ms · max 179 ms | p50 106 ms · p95 519 ms · max 1104 ms |

  In two of the three runs the herdr + Orca send had one max of about 170 ms (herdr alone had one of 149 ms in one run); p50 did not move.
  The refresh of every host is slower because it now includes Orca, the heaviest host. herdr's own refresh gains about 10 ms at p50 and about
  0.1 s at p95: the session merge after it (`setAgentsAt`: process lookups, session files) covers Orca's agents too.

  A herdr send waits for nothing of Orca's. Measured again for #5816 on fakes (2026-11-07,
  `PERSONA_BENCH_ENGINEHOSTS=1 go test -run EngineHostsLatencyOnFakes ./internal/node/ -v`: a fake herdr, a fake Orca whose process scan
  takes 170 ms as three `ps` runs do on a Mac, 400 rounds each), a herdr send is p95 0.1 ms with and without Orca. herdr itself answers
  about 0.1 s late now and then with nothing else asking it (`docs/lessons/agents.md`).

  The merge asks only the hosts that answered the refresh what their panes run; another host's pane keeps the pid its own refresh found.
  Only the owner of a session live in two hosts still asks both, so it does not change with the host that refreshed. When the merge asked
  every host, herdr's refresh ran Orca's process scan whenever an Orca pane was due for a look (p95 174 ms on the fakes, 1.6 ms now), and
  Orca's poll asked herdr about every herdr pane due for one.

"Engine" below means this choice. APIs, storage, and node switching: `docs/protocol/protocol.md` ("Engines"); design / trade-offs: `docs/plans/plan-orca-support.md`.

## Engines

| Engine | App name | Where Claude runs | Detection |
| --- | --- | --- | --- |
| `herdr` | herdr | Run `herdr` in a terminal, then `claude` / `codex` in its panes (node starts it on demand if absent; see "When herdr is not running") | Connectable herdr socket or installed `herdr` command |
| `herdr-core` | Herdr App | Terminals opened in Herdr desktop app (herdr-core behind it) | Responsive herdr-core socket |
| `herdr-both` | herdr + Herdr App | Both above | Both runtimes detected |
| `orca` | Orca | Terminals and native Claude chats (structured sessions) in Orca desktop app, or its remote runtime launched here over SSH by Orca on another computer | Orca desktop / remote runtime running, with a Persona-compatible version |
| `paseo` | Paseo | Paseo Agents (desktop app, `paseo` command; Claude Code, Codex, ACP all count) | Paseo daemon running; address in its home's `paseo.pid` responds |

herdr and Herdr App are from the same developer but separate: neither sees the other's sessions. If using only one, select it;
if both are used and should appear on the phone, choose "herdr + Herdr App (side by side)."

## Switching

1. **During pairing**: after QR / manual pairing, asks "Which engine does this computer use?" (the hub machine). Each option says "Available" or "Not detected."
   Select one; herdr is default (or previous choice), confirmation switches; no Skip.
2. **Anytime later**: **Settings › Machines › computer › Engine**, select another. Choices are per computer; one can use herdr, another Orca.
3. **Offline computers** can change too, taking effect when connected.

iOS / Android have both entry points (Android Machines is under Agents: **List settings › Machine monitoring › computer**, or tree computer-header dashboard button, then **Engine and connection › Engine**). Web selects directly in Machines (**sidebar Machines › computer**, Engine section, #2071). Choice is stored on hub, not phone, and matches across phones, web, and Android.

## After switching

- Agents immediately shows new-engine sessions; old-engine sessions become Not running, with history retained.
- Live terminal / phone takeover disconnects; reopen in a new-engine session.
- No running Claude in any engine is closed or modified.

## Capabilities by engine

| Capability | herdr | Herdr App | Orca | Paseo |
| --- | --- | --- | --- | --- |
| List sessions, show state (Needs you / Working / Unread) | Yes | Yes | Yes (polls each second) | Yes (Paseo pushes changes) |
| Send messages (multiline as one message) | Yes | Yes | Yes | Yes (while working, injects into current turn) |
| Keys (Enter, Esc, arrows…), interrupt | Yes | Yes | Yes | Interrupt only |
| Answer dialogs (permissions, options) | Yes | Yes (screen-based) | Yes (screen-based) | Yes (cards, also answerable in Paseo app) |
| Close sessions from phone | Yes | Yes | Yes | Backend supports archiving in Paseo; app button currently only for terminal sessions |
| Create / continue Not running sessions from phone | Yes | No | Yes (create; continue a Not running Claude session in a new terminal, #5548) | Yes (each Paseo provider is a launcher) |
| Create a worktree from a workspace ([terminal.md](terminal.md), "23. New worktree…") | Yes (herdr 0.9 creates it; older versions fall back to git) | No | Yes (Orca creates it under the original workspace and owns the checkout directory) | No |
| Rename spaces / tabs | Yes | No | No | No (backend supports session names; app button currently only for terminal sessions) |
| Live terminal, phone takeover | Yes | No | No | No (no terminal) |
| Colored screen reads, draft detection | Yes | No (plain text, dialogs only) | No (plain text, dialogs only) | No (no terminal) |
| Change permission mode / thinking effort | Yes | Supported, untested | Supported, untested | Yes (end-to-end tested) |

Windows: herdr has native Windows support since 0.9.5; except live terminal, capabilities match above. Orca works too. See [windows.md](../operations/windows.md), "herdr."

Orca's native Claude chats (structured, no terminal) connect through node's bridge: list, state, send text / images, interrupt, close,
answer approvals / questions, change permission / thinking (no plan mode); no screen reads, keys, or live terminal. Chats appear only after their first message;
Codex chats not listed in this version. See `docs/plans/plan-orca-support.md` ("Structured sessions").

Unsupported actions explain failure instead of partially acting. Herdr App rejects sending without entering anything when Claude is not foregrounded (suspended / exited), and Orca does when its pane is still
there with Claude not in front, saying "Claude is not in foreground (possibly suspended); nothing sent," or asking to reopen on the computer.

Orca New Agent creates terminals inside already-open Orca workspaces or subdirectories, starting in the selected directory. Unopened Git-repository directories attempt automatic registration; any other directory gets a terminal in the first local Orca workspace that `cd`s there (refused only when Orca has no workspace open). It never takes over an existing terminal.
Resume of a running session returns it. A Claude session whose agent exited is continued like on herdr (#5548): a new terminal in the Orca workspace holding its directory (the same rules as New Agent), `cd` there, the launcher that started it with `--resume`, then the message. Never a second writer: a session a live claude still has open (Claude's desktop app, another terminal, suspended) is refused (`open_elsewhere`), and so is one whose last pane is still open (its agent may be suspended there; close the pane to resume elsewhere). A session an Orca native chat owns is refused too. Codex, TRAEX and pi sessions are not resumed in Orca. Orca's own `terminal create --agent claude --resume` is not used: it exists only in some Orca builds' CLI, and it starts Orca's claude command, not the session's launcher.

Selecting `herdr-both` attaches both herdr and herdr-core hosts:

- Agents merges sessions; spaces / tabs, state, screen reads, messages, keys, closing route to the pane's own host, as when selected alone.
- **Phone create / continue Not running / space rename use classic herdr** (complete capabilities). Continuing an exited Herdr App session from phone
  opens a new herdr pane with `--resume` (same Claude session ID, continuous history, different window). herdr-core live terminal / phone takeover remain unavailable.
- Neither slows the other: snapshots fetched concurrently with separate timeouts; herdr-core events refresh only herdr-core,
  herdr events only herdr; a failed side clears only its own online state.
- Available only when both herdr and herdr-core are detected (can preselect offline, effective on reconnect; if only one exists,
  it works normally while the other list is empty).
- Orca next to herdr goes through "Host sets" above; there are no more combination options.

## botmux

[botmux](https://github.com/deepcoldy/botmux) maps each Lark topic to a Claude Code / Codex / compatible CLI session in its own tmux. It is not an engine:
when `botmux` is on the computer's PATH, node adds a botmux host alongside the selected engine host (`server/internal/node/hostbotmux.go`).
Typical use: work Agents in botmux, personal Agents in herdr, viewed together.

- **Listing and state**: each refresh runs `botmux observe --include-raw` (all sessions from all daemons, reused within 5 seconds, immediately reread after sending; 0.3–0.9 seconds for about 500 local sessions; occasional observe failures retain the previous list).
  Lists Claude Code, Codex, and the compatible Codex fork (`cli.id: claude-code / codex / traex`), with identical state rules: running sessions use botmux state (dialog `tuiPromptActive` or botmux Needs you → Needs you; `working` / `starting` / `analyzing` → Working;
  others → Done). Suspended (`not_running`, not closed, active within 7 days) appear Not running. For Claude Code, botmux session ID is the Claude ID used to start it, so transcript handling is unchanged.
  For Codex / the compatible fork, observe omits CLI session ID (stored in botmux database), but every prompt starts with `<session_id><botmux-session-id></session_id>`.
  Node searches rollout beginnings in the same working directory (first 512 KB, stopping each file after finding the marker). Matching rollout is the thread (newest-written among forks),
  listed by thread ID and read as rollout history (`docs/integrations/codex.md`); title prefers thread name, otherwise botmux topic title (`botmuxThreads`).
  Without matching rollout, omit: newly opened sessions before initial prompt enters rollout (next refresh finds them), or custom bot-specific `CODEX_HOME` / `TRAE_HOME` directories unknown to node
  (Codex reads known homes, compatible fork only `~/.trae/cli`). `codex-app` (botmux drives Codex through app-server, without session-ID prompts) and other CLIs omitted.
  Space is cwd (like Herdr App terminals outside spaces; trim trailing `/` to avoid duplicates), named by final directory component. If it matches parent case-insensitively, include parent (`Botmux/botmux`, #1836). Derive only from its own path, not other listed sessions, so name-based pin / mute / group-to-do bindings stay stable.
- **Sending**: same for all three, delivered through botmux dashboard `POST /api/trigger` targeting the session, not terminal input. Botmux queues while busy,
  resumes suspended sessions itself (no separate herdr `claude --resume`, also for suspended beyond 7 days / unlisted), and sends replies back to Lark topics.
  Node gets dashboard token through `botmux dashboard current`, memory-only, connecting only to local 127.0.0.1. Claude receives botmux task tags and an external-event explanation.
- **Unsupported**: screen reads, keys, dialogs (answer in Lark), close, rename, create, live terminal; app hides or explains by capabilities.
- **Why it needs you / open in Lark**: reason from `botmux send --attention` goes into `attention_reason` while Needs you;
  app shows it in list rows / chat top. Raw observe topic link (`feishuThreadLink`, HTTPS only) becomes `external_url` for "Open in Lark."
  Both are raw botmux fields, not stable APIs; omit if absent.
- **Summary (`last_text`)**: when not Working / Needs you and botmux replied to the latest message (`previewBotState: replied`), use its last reply (`previewBotFullText`, otherwise `previewBotText`; cards stored as `[卡片: 标题]`, "card: title", strip wrapper, keep title).
  Agent answers go to Lark through `botmux send`, so transcript often ends only with `BOTMUX_NOTHING_TO_SEND`. While working, unreplied, Needs you (topic reply often explains the wait), or transcript has text over 10 minutes newer than this reply (work continued outside botmux), retain transcript's last text.
  Standalone `BOTMUX_NOTHING_TO_SEND` never counts as text in any session: retain earlier text if alone in a turn. A transcript containing only that still counts as spoken; after over 7 suspended days it is listed Not running.
- **Execution failures**: botmux failure notices (warning-symbol prefix, saying it stopped automatic continuation / recovery / restart / handling) are not questions and do not mean Needs you.
  State follows botmux turn (usually Done); reason in `failure_reason`; app shows a red error with Retry (send "Continue" through botmux) and Open in Lark.
  Agent-issued `botmux send --attention` still means Needs you, even with a warning prefix.
- **Stale attention**: botmux clears attention / failure notices only for messages passing through it (Lark, delivery), not terminal or other channels.
  A newer transcript user message after the attention / failure makes it stale: hide Needs you / reason, use turn state.
  Slash commands, `! cmd`, and other Agent reports do not count. Read only the final 384 KB; if a turn wrote more after that message, it may be invisible, conservatively retaining attention rather than falsely clearing.
- **No duplicates**: botmux sessions using herdr backend already are herdr panes, so botmux host omits them. If botmux Codex also appears in Codex history / desktop lists (same rollout),
  keep only botmux's actionable row, not read-only history. Botmux Codex / compatible-fork sessions do not attach app-server (`thread/resume`), read screens for question / approval cards, or inspect composer before sending;
  like botmux Claude Code, those actions stay in Lark / on computer. Codex suspended beyond 7 days and absent from botmux appears as read-only Codex history; reply in Lark.
- **Same session on both sides**: if also running in an engine pane (previously resumed from phone), the live pane wins and botmux's suspended record yields.
- **Detection**: find `botmux` when node connects and learns whether to list it (next item), and on engine switches: PATH first, then installer path `~/.botmux/bin/botmux` (launchd node has only system PATH). Installing later requires restarting that computer's personad.
- **Disable**: "Also show botmux sessions" (#2575; the web has it in the machine page header as **botmux · On / Off** beside the engine pill, one click flips it, #2842; iOS below Engine in machine details; Android next to Engine in Engine and connection, saved on tap, #2899), default on. Changes immediately send `node.configure {"botmux": …}`, stored in hub settings.
  `"botmux": false` in the machine's `machines[]` hub.json entry is only its unset-setting default (`docs/operations/config.md`; old nodes ignore). Stops `botmux observe` / `botmux dashboard current` (in-flight results discarded); Agents has no `botmux:` panes.
  Hub specifies the setting in `node.configure` on connection; node does not attach botmux or push lists before that (preventing sessions briefly disappearing / returning). Old hubs omit it; attach as before when asked for sessions.
  After disabling, tmux botmux sessions still appear as Not running history. Replying on phone launches engine `claude --resume`, potentially colliding with botmux's instance; reply to these in Lark.
- **To-dos**: attach by session ID as usual; project-wide grouping uses botmux session cwd.
- Live test: `go test -tags botmuxlive -run BotmuxLive ./internal/node/ -v` (read-only listing plus one dry-run delivery: validation / rendering, no dispatch).

## Named herdr sessions (#2196)

herdr can run more than one server per account: besides the default one, named sessions (`herdr --session <name>`, listed by
`herdr session list`), each with its own socket under `<herdr config dir>/sessions/<name>/` and its own ids, so `w4:p1` can be a pane of two
of them at once. A node on the default herdr server drives every **running** named session as part of the herdr engine
(`node/hostherdr_sessions.go`):

- **Ids**: a named session's pane, tab and workspace ids are `@<session>:<id>` (`@build:w4:p1`, `herdr.QualifyID`); the default server's stay
  bare, so stored rows, hooks and clients read as before. No host kind starts with `@`, so the hub and every client take them for herdr panes, and they
  hold no `/` a path could split on. Sessions and their live rows are listed, read, sent to, interrupted, closed, renamed, split, streamed (observe,
  control, the full herdr view) and given resume commands on the session the id names; ids herdr hands back (a split, a new tab, a worktree) are
  qualified again.
- **Showing the session**: a named session's spaces are labelled `<session> · <workspace label>`. Renaming one sets herdr's label without that
  prefix (typed or not), and `space.rename` answers the name the space shows now, which the hub keys pins, mutes and to-do links by.
- **Left alone**: stopped sessions, the `persona-<hash>` sessions of other nodes' data directories (`herdr/session.go`), names that cannot stand in an
  id, and every other session on a node that runs on a session of its own or on a socket `HERDR_SOCKET_PATH` moved. Windows drives the default server only.
- **Pane identity from processes**: hooks, MCP calls, the pi extension (`herdrPaneOf`) and the process scans read `HERDR_SESSION` (or the session
  directory in `HERDR_SOCKET_PATH`) with `HERDR_PANE_ID` (`herdr.EnvSession`, `herdr.PaneOf`), so a hook in a named session's `w4:p1` never binds
  the default server's `w4:p1`. Hooks, MCP calls and the pi extension write panes as the default server sees them (`nodeclient.HookPane`), and the
  node translates them where they come in (`node ownPane`: the node socket, the standby spool, runtime hints); the node's own process scans write
  them as its herdr host does (`node envPane`). The two differ only on a node whose own server is a named session: its own session's panes are bare
  there, and the default server's are `@default:<id>`, which it does not drive.
- **Never in the default server's way**: `herdr session list` runs in the background, again once the list is 5 s old; an id of a session the list
  does not have makes the node ask again (at most once a second) and wait up to 6 s for the answer. Each listing asks the named sessions alongside
  the default server and waits at most 2 s from its start for them (and, the first time, for the list); what a session has not answered by then
  comes from its last answer, with one call to it out at a time. The default server failing is the herdr host failing, as before: while it is down
  the herdr host lists nothing, the named sessions included, and keeps its last list. Each server has its own event stream; the named ones are
  looked at again every 5 s (a session started since gets one, a stopped one's ends), and one that ends is opened again on its own.
- **Pane-less work stays on the default server**: new workspaces, and new sessions started without naming a workspace (a named session's labels
  never match a start's directory name). A start, a new tab or a worktree in a named session's workspace opens there.
- **Focus** following (`herdr.client.focus`) is the default server's: it is the computer's own herdr window. Focusing a named session's pane works
  but brings no echo back. A full herdr view is attached to one server (the session of the pane it opened on): asked to show a pane of another
  session it ends, and opening it again attaches to that session.
- **Known gap**: the iPhone and Android apps rename a space optimistically under the name typed; for a named session's space the name shown
  (`<session> · <name>`) differs, so they show the typed name until the app reloads. The web and the hub use the name `space.rename` answers.

## When herdr is not running

After reboot, launchd / systemd starts node, but no terminal has started classic `herdr` server (#1879). Node starts it on demand:

- **When**: node startup (after hub specifies settings in `node.configure`; old hubs omit them, so assume enabled after 10 seconds), and before phone create / continue Not running.
  Only with herdr or herdr + Herdr App, only classic herdr; herdr-core has its own lifecycle and is untouched. Not from polling,
  only those two occasions.
- **How**: `herdr server` (v0.9.5 headless foreground server), in a separate session (`setsid`) so node restarts do not kill it.
  Linux systemd-user nodes instead use `systemd-run --user --unit=persona-herdr-server` (stopping the node unit kills its whole cgroup).
  Wait for connectable socket (at most 20 seconds) before create / continue.
- **Clean environment**: herdr server passes its environment to every pane and claude. Node uses `env -i` with only `HOME`, `USER`, `LOGNAME`, `TMPDIR`, language,
  `XDG_*`, `HERDR_SOCKET_PATH`, `PERSONA_HOME` (panes find the node socket in that directory), then user's login shell (`$SHELL -l -c 'exec herdr server'`; if launchd has no `SHELL`, read account login shell).
  PATH comes from that shell; no node / launcher `ANTHROPIC_*` or proxy variables. Find herdr on node PATH first, then ask login shell.
- **What a start types** (#744, `node/agentenv.go`): a pane has its engine's environment, not the node's. A herdr server started some other way, or Orca's app, may lack the node's `PERSONA_HOME`, and an engine started from the assistant's Bash tool hands the assistant's own variables to every pane.
  So every command the node types into a pane to start or resume an Agent (Claude, Codex, compatible CLIs, pi; herdr's `resume_argv` too) does two things:
  - It puts `PERSONA_HOME=<the node's data directory>` in front, where the pane's shell takes `VAR=val cmd`.
  - In the sh family, it first runs `unset` on two sets of variables. One set is what the node gives only the assistant's processes: `PERSONA_ASSISTANT`, `PERSONA_SESSION_ID`, `PERSONA_SPACE`, … in both spellings, plus `CLAUDE_CODE_AUTO_COMPACT_WINDOW` and `ENABLE_TOOL_SEARCH`. The other is what a running Claude Code sets for the programs it runs: `CLAUDECODE`, `CLAUDE_CODE_ENTRYPOINT`, `CLAUDE_EFFORT`, ….

  fish gets the directory only. A shell that takes no assignments (csh, nu, pwsh), and Windows, get the command as before. An Agent that should have `CLAUDE_CODE_AUTO_COMPACT_WINDOW` or `ENABLE_TOOL_SEARCH` gets it from its launcher or from Claude Code's settings, not from the pane.
  The generated `mcp.json` (the server's `env`) and the hook commands (`PERSONA_HOME=… personad hook`) name the directory as well, so they work even when the start command could not carry it.
- **Only one**: in-node mutex plus `flock` on herdr configuration directory (another node for the same account); recheck socket after locking.
- **One server per data directory** (#6482): a herdr server's panes reach the node whose `PERSONA_HOME` the server was started with, so two nodes with different data directories on one account must not share a server.
  A node whose data directory is not `~/.persona` uses a herdr session named after it (`persona-<8 hex of the directory's hash>`): socket `<herdr config dir>/sessions/<name>/herdr.sock`, its own saved workspaces and log, the same `config.toml`.
  The node reaches it through that socket and passes `HERDR_SESSION` to the herdr CLI it runs and to the server it starts, so the panes carry it too. The node with the default data directory keeps the default server.
  An explicit `HERDR_SOCKET_PATH` or `HERDR_SESSION` in the node's environment wins (a node started inside a herdr pane has them, so it uses that pane's server; it logs when that is not the data directory's own session); under systemd the server of a session is a unit of its own (`persona-herdr-server-<session>`); Windows keeps the default server. A default server already running with another data directory's `PERSONA_HOME` (started by a node before this) is not detected: restart herdr.
- **Switch**: per machine, "Automatically start herdr when needed" below **Settings › Machines › Engine**. hub.json
  `machines[].herdr_autostart` is only the machine's unset-choice default (omitted means enabled). User choices live in hub settings
  (`herdr_autostart/<machine-id>`), sent through `node.configure` on connect / changes. Once disabled, manual `herdr server stop` stays stopped,
  and node does not restart it. `GET /api/v1/machines/{mid}/herdr` (`{autostart, running, known}`),
  `PUT` (`{"autostart": bool}`); row exists only for herdr / herdr-both. Client UI pending (#1915).
- **Manual start**: `node.info` includes `herdr_running` / `herdr_running_known` (old nodes omit both, distinguishing stopped from
  unknown). `POST /api/v1/machines/{mid}/herdr/start` forwards to node `herdr.server.start`, **independent of autostart**:
  that switch controls node-initiated starts; a user's Start tap explicitly requests it. Failure reasons below (`engine_missing`).
- **Current status**: `node.info` is requested only once on node connection. After reboot, node waits for `node.configure` (after `node.info`) to start herdr,
  so relying on `node.info` leaves stale notices until node reconnects; same after manual Start / computer-side `herdr server stop` (#1916).
  `GET …/herdr` therefore asks `herdr.server.status` live (one socket connection, `{running, known}`), reused for 3 seconds;
  successful `herdr/start` also records result. Old nodes without the method retain `node.info` values and avoid reasking within 3 seconds.
  Android Agents has the notice / Start button (#1916), shown when online and known stopped; tap explicitly starts.
  iOS / Mac too (`HerdrNotice.swift`): poll every 10 seconds while Agents is foregrounded and not covered by chat. One notice per machine atop list, respecting machine filters / hub groups.
  One read failure does not remove existing notices; endpoint 404 stops polling that machine. Start shows "Starting…"; failures show localized known reasons inline (immediate exit / killed, missing, no response, old node, offline, timeout, `HerdrFailure.says`; unknown reasons remain raw). Keep button; raw hub text, status, request ID under long-press / context-menu Copy details, not body.
  After success, use POST result for that machine during this run until hub also reports running (GET may still use connection-time value). Web same (`web/src/lib/herdr-notice.ts`, `views/agents/HerdrNotices.tsx`), below filters in sidebar / mobile Agents section, polling every 10 seconds while visible,
  same rules / localized failures, with raw hub text, status, request ID (`X-Request-Id`) in Copy details button / context menu.
- **Failure**: still `engine_missing`. Missing herdr copy unchanged; installed-but-failed says "Automatic start also failed." Details include exit code / final output lines,
  output in `persona/herdr/persona-herdr-server.log` under the user's cache folder (`~/.cache` on Linux, `~/Library/Caches` on macOS; a folder of the node's own in the temp folder when there is none), also node logs. No autostart on Windows; behavior unchanged.

### When herdr is not installed

New computers often lack herdr (#2847); previously Start only said "herdr not installed," requiring someone to log in and install it. Node now installs it (`node/herdrinstall.go`):

- **What**: official herdr release binary for OS / CPU (linux / macos × x86_64 / aarch64), **version pinned** to match other computers
  (currently 0.8.0, not `latest.json`). Verify hardcoded sha256 before installing to `~/.local/bin/herdr` (no sudo, on node PATH).
  Write temporary file and rename only after verification, avoiding partial binaries on interruption. Windows / other architectures have no package and require manual install. Version upgrades update the version and all four sha256s.
- **When**: always install on user Start. Node-initiated starts (startup, before create / continue) install only if `machines[].herdr_autoinstall: true` in hub.json;
  omitted preserves "Not installed." One install at a time; concurrent requests share it.
- **Slow downloads**: background download up to 15 minutes; Start waits at most 15 seconds. If ready, start and return `running`; otherwise return "Installing herdr 0.8.0… It will start automatically when ready"
  (client shows original text after "Not started:"). Node starts server when done; next Agents poll sees running and removes notice.
  `herdr.server.status` / `GET /api/v1/machines/{mid}/herdr` add `installing`, `install_error` (last failure); client can show Installing, not wired yet.
- **Install failure**: GitHub unreachable, timeout, HTTP error, checksum mismatch all give a localized reason. Network failures say "A proxy may be needed: set node HTTPS_PROXY and restart it"
  (downloads use Go `ProxyFromEnvironment`). Also log reason on node.

### Resuming Persona-started sessions after restart

herdr restores workspaces, tabs, panes after server restart, then **after a client attaches** resumes Agents using built-in commands (`session.resume_agents_on_restore`, default on),
for Claude Code plain `claude --resume <id>`. Persona-created Claude uses launchers (`personad launch <name>`, ~/.zshrc functions),
`--settings`, `--mcp-config`, Persona hooks; plain resume loses all of these, switching gateway to user's subscription and dropping MCP / hooks.

Persona therefore reports its own recovery command for each Claude pane (`resume_argv`, only this pane):
`eval "<Persona's original launch command, --resume <id>>"`. herdr types it into the pane shell during restore, retaining ~/.zshrc launchers.

- User-started terminal claude is not reported; herdr restores normally; user `config.toml` untouched.
- Older Persona sessions / sessions predating node restart (launcher in node session records): report once on refresh if running in herdr.
- Claude changes sessions in a pane (`/clear`): report new ID when Persona SessionStart hook arrives; herdr does not track it automatically (v0.9.5 tested).
- Resume command omits permission / thinking settings; paths with single quotes are not reported (herdr rejects `'` arguments), falling back to built-in restore.
- Node-started headless server has no attached client, so herdr does not resume Agents until someone opens herdr on the computer. Phone Continue on a Not running session still works.
- **Interrupted by restart** (#1917): sessions running before restart appear `interrupted` afterward (`docs/protocol/agent-states.md`), with Continue. Node records the pane and
  launch time (session records in hub settings); if launch predates current node startup and no Agent is alive, it is interrupted. Sessions launched after this node startup do not count,
  nor user-started terminal sessions (no recorded pane). No automatic continuation: restart may interrupt a tool call, so the user decides. Continue uses ordinary
  `session.start {resume}`, launcher `--resume` in a new pane.
- No "Continue all" yet (machine-wide batch start, client half).

## When an engine cannot be selected

- **"Not detected"**: engine is not running on that computer. Start `herdr` once in a terminal; open Herdr App / Orca. After a short wait,
  or Reconnect for that machine, detection refreshes.
- **"herdr + Herdr App" not detected**: both runtimes are not running (herdr started in terminal, Herdr App open with remote connected to this machine).
  If only one is used, select it directly.
- **Orca says "This Orca version is incompatible with Persona"**: Orca / personad versions mismatch; update both.
- **Paseo not detected**: open Paseo desktop app or run `paseo daemon start`. For a custom home, set personad `PASEO_HOME`.
  If available but a connection failure appears below, see `docs/integrations/paseo.md` ("Troubleshooting").
- **Engine selected, no sessions**: verify Claude actually runs in that engine; ordinary-terminal / other-app sessions appear only Not running.
- **Old node on hub machine** (version mismatch in machine info): old node does not understand switching and uses only herdr; update personad on that computer.
