# Assistant (the "Persona" tab, formerly "Chat")

Persona in the "Persona" tab is Claude Code running **headlessly** in `~/.persona/assistant` on the hub machine (`claude -p` + stream-json; see
`docs/integrations/claude-integration.md` ("Headless assistant")). A Codex launcher uses `codex app-server` instead ("Codex" below); a launcher for an assistant-capable Agent runtime (DSH) runs in that runtime ("Agent runtime (DSH)" below). This doc covers what it can do, how it works, and the trade-offs behind the 2026-10-26 capability upgrade.

## Work computers and personal context

Set `machines[].trust` to `"work"` in `hub.json` to keep personal assistant data off that computer (see [config.md](../operations/config.md)).
Ordinary Agents can still run there and receive tasks from personal computers and the assistant. Their task prompts do not receive the automatic knowledge-base project background or previous project outcomes. The task text the user or assistant explicitly supplies is still sent.

Assistant conversations, including guest conversations and the Inbox AI twin, cannot start on a work computer. The manager refuses before preparing the assistant directory or sending instructions, memory locations, connector skills, or the first message. An Inbox twin already associated with a computer later marked `work` receives no further notes and is marked failed; its conversation is not adopted after a restart. Select a personal computer for the twin to start new handling conversations.

Omitting `trust`, or setting it to `"personal"`, preserves the existing task-context and Inbox placement behavior.

The manager verifies the calling machine for `mcp.tools`, `mcp.call`, `assistant.context`, and `assistant.model`. The node's `assistant` flag only requests the assistant tool set. The configured assistant computer and Inbox twin computer can bootstrap sessions before runtime session IDs are available; a known ordinary Agent or host pane does not acquire assistant privileges. A conversation explicitly started by the manager on another personal computer is authorized during startup and then by its recorded machine and session. Runtime tool discovery without a session ID can rediscover the tool schemas on that computer after startup; execution still requires its recorded session. Guest conversations retain their restricted tool whitelist and receive no live context. `assistant.lark` remains restricted to the configured assistant computer.

Changing trust does not erase context already delivered to a computer or terminate an existing process. New personal-data tool calls and Inbox deliveries are refused; ordinary Agent supervision and question / approval cards remain available.

## Why it was weak before 2026-10-26 (from real chat records)

- **Broken confirmation**: CLAUDE.md told it to confirm through AskUserQuestion, but in `-p` that tool immediately reports
  `No such tool available: AskUserQuestion … nobody in this session can answer it`; PreToolUse hooks never get a chance to run.
- **Most time spent finding data**: 33 `cd` / `ls` calls in one chat; `docker inspect` to extract a Lark token; ssh and parsing `herdr agent list` to see Agents.
  The hub already has this data.
- **Slow**: 36–136 seconds per turn, repeated ssh timeouts (exit 124 / 137); cold-starting claude for each chat, and ToolSearch before Persona tools.
- **Reactive only**: even on starting, it didn't know the time, who was waiting, or how many unread items existed.

## Current structure

| Capability | Implementation |
| --- | --- |
| Ask on the phone | MCP `ask_user` → hub creates a `question` interaction (`tool_name: ask_user`, session is this chat) → existing App question card + push ("Persona Has a Question") → answer returned as tool result. The card waits up to 2 hours (`askUserWait` in `assistant/tools.go`, #9), longer than the Lark nudge's default 60 minutes, so an owner who is away is nudged while it still waits. The turn that asked is blocked on the answer (a message the user sends meanwhile would wait behind it), so when the user writes in the conversation (`UserSpoke`) the question is withdrawn at once: the tool returns "the user wrote a new message instead; it is delivered when you end this turn, so end it", and, with the interaction queue, the card closes as dismissed with `reason: "replied_in_chat"` (Lark: "Replied in the chat"; what the user types next is a message, not a late answer to it). Without the queue (the hub keeps the cards) the question just disappears from the snapshot. The take-back happens when the message is received, before it is sent: if the send then fails, the question is already gone. Only the conversation's own `ask_user` is taken back, not an Agent's card or a permission. The question is not resumable: a manager restart ends it. A card that expires unanswered becomes a to-do waiting for the owner's call (in the to-do list, the live snapshot and the briefing's "Can wait"); the tool says so, and the owner's later decision on it comes to the asking conversation as a new message (`docs/assistant/todos.md`, "Questions nobody answered", #1617) |
| Push | `notify_user` → APNs (arrives with the App closed), tapping opens this chat; no duplicate "completion" push at the end of the same turn. Also sends to Lark if the Lark bot's "Send notifications to Lark too" is on, and to plugin channels with "Push" on (sent by the assistant itself, `docs/extensions/bots.md`, `docs/extensions/channels.md`) |
| Chat in Lark | Private chat with the Lark bot = talking in main chat; answers return to Lark, questions / approvals use Lark card buttons (`docs/extensions/bots.md`) |
| Return on a schedule | `schedule_followup` (`at` once; `every: "30m"` repeats by interval) / `list_followups` / `cancel_followup`; when due, hub sends the prompt (starting with "⏰") into the original chat (`assistant.send` uses `--resume`), or starts a new one on failure |
| Scheduled tasks | One table ("Scheduled tasks" below), default "Morning briefing" on weekdays 09:00 enabled, "Evening review" weekdays 21:30 disabled; normal "completion" push afterward (with the task's `facts` on, as `docs/extensions/bots.md` "Morning and evening briefings" says, Persona computes what waits and what failed, and the push is the counts). When told "every day / on weekday X at…", the assistant creates one with `schedule_routine` (marked "Created by Persona" in the App), deletes with `delete_routine` |
| Run history | Every run (chat started successfully / failed to start / skipped because downtime missed it) is stored in settings `assistant.runs` (latest 200), `GET /api/v1/assistant/runs[?ref=]`; scheduled tasks include `last_status` |
| Due time | Scheduling sets a timer for the next due time (sleeps at most 30 seconds, recalculates on list changes), not 20-second polling. Follow-ups missed during hub downtime run once on return, with lateness stated in the prompt |
| Background tasks / Monitor | The assistant can use Bash `run_in_background` and Monitor to watch events, then end its turn; command completion / events make claude start another turn itself (node considers it working, with normal push on completion). node learns running tasks from stream-json `system/background_tasks_changed`: don't reclaim the process while tasks exist (up to 12 hours); task lists live in `node_sessions.tasks`. personad restart (deployment) kills tasks with the process; on startup node sends these chats an explanation so the assistant decides whether to restart them |
| Tools without approval | The assistant process gets `--allowedTools mcp__persona`: Persona tools (notify_user, schedule_followup, agents_list, kb_*…) no longer request phone approval each time. Actions representing the user (mail_send, phone writes) are confirmed by hub itself; Lark writes are confirmed by a Bash gate ("Lark: skill supplied by the Lark extension" below). Other Bash / Monitor calls still follow the session permission mode |
| Agents | `agents_list` / `agent_read` / `agent_send` / `agent_start`: use hub session state (`decorated`) and node `session.items` / `session.send` / `session.start` directly, not ssh. With `worktree.branch`, `agent_start` first has the computer's engine make the worktree through node `worktree.prepare` / `worktree.create` (the workspace found by `cwd`; herdr's or Orca's own, so Orca and the app list it), then starts the Agent in it, instead of the Agent running `git worktree add` itself (#5325, `assistant/agent_worktree.go`) |
| Answer questions on the user's behalf | `interaction_answer`: when the owner says "answer that question for me, choose A", the assistant answers other Agents' **question-type** interactions. Only `kind: question`, excluding the assistant's own chat; `permission` / `plan` / `screen` are always rejected with an explanation that only the owner can answer in the App (no proxy approvals is a hard boundary). Validate every question: all must be answered, keys must be exact question text, choices must be listed, single-select exactly one, multi-select joined as `A, B` like the App; reject free text (answer in the App or use `agent_send`). Expired, answered, dismissed, cancelled, and nonexistent interactions each return their own error (the latter four consult history). `Resolution.AnsweredBy = "assistant"` accompanies interaction history (`GET /api/v1/interactions/history`) and `interaction_resolved` broadcasts; the App marks "Answered by Persona". On Agent questions, the assistant first decides whether to answer, see "Assistant answers questions first" below. `agents_list`'s waiting rows include `interaction_id` and options for every question, avoiding another lookup (tools and rules all belong to the assistant: `toolInteractionAnswer` in `assistant/tools_apps.go`, tests `assistant/tools_apps_test.go`; the interaction queue also lives in the manager: `interactions.get` returns the interaction or its historical outcome if no longer waiting; `interactions.answer` records validated answers as assistant answers, directly in-process, `assistant/fleet_routes.go`) |
| Interrupt, adopt, report | `agent_interrupt`: stop an Agent's current turn immediately, optionally followed by a new instruction (`agent_send` queues behind the turn); `agent_supervise`: bring an Agent not dispatched by main chat under its supervision (it becomes a main-chat task); the Agents' `report_to_supervisor`: an Agent on a supervised task reports a needed decision, blocker, completion with evidence, or progress into the supervision message. See `docs/assistant/unified-entry.md` ("Supervision", #5112) |
| Archive Agent sessions | `agent_archive` / `agent_unarchive`: when the user asks, archive or restore a coding Agent's session with the same manager-owned, reversible archive the apps use (`setAgentArchive`, `docs/protocol/protocol.md` ("Agent session archives")); nothing is ever deleted. Archive and restore change nothing on the computer and need no confirmation (like `conversation_archive`). `agent_archive` with `close: true` is "Archive and close" (`archiveAndCloseAgentSession`): a busy Agent is refused before anything is asked, then the user confirms on a phone permission card every time (also outside the health space, as `persona_settings`), and a declined card changes nothing. The result states what happened for the assistant to repeat: archived or not, the native archive (`native: done / pending / unsupported`), and the close outcome (`closed`, `not_running`, or `refused` / `offline` / `failed` with the localized reason: the session stays archived and still open). Busy Agents are refused with nothing changed; an Agent awaiting the user's reply is not busy. `agents_list` leaves archived sessions out unless `state` is `archived` (only those) or `all` (marked `archived`). Persona conversations are refused (`conversation_archive`). `assistant/agent_archive_tools.go` |
| Dispatch tasks | `task_plan`: hand Agent work to the user, one task per to-do (`origin` = this chat). Read-only and user-remembered tasks start directly; others become plan cards awaiting "Start" without blocking the turn. Progress appears in the live snapshot's "Tasks started from chat"; completion receipts, answered questions, and direct task messages enter chat activity ([unified-entry.md](unified-entry.md), `assistant/tasks.go`) |
| Telegram | Available only with the Telegram extension (extension market `nwverse/persona-extensions`, #1704, #2057): read-only MCP `telegram_chats` / `telegram_messages` / `telegram_search`; more flexible reads and writes use the bundled telegram skill (Bash `tg …`, i.e. `personad tool --cli telegram`, through hub, writes confirmed on the phone). `tg` is on PATH only while Telegram is logged in (`docs/protocol/connector-protocol.md` ("Command line")) |
| Lark | The open-source version uses official lark-cli ("Lark: lark-cli" below). With a Lark-gateway extension installed, mainly use its skill (extension commands in Bash, both reads and writes), see "Lark: skill supplied by the Lark extension"; MCP `lark_chats` / `lark_messages` / `lark_search` also come from the extension (since #1571 no Lark gateway client in hub, `docs/plans/plan-extensions.md` ("Step 3")). The old read-only `lark_api` and `lark_send` were removed as duplicates of the skill |
| Control Persona itself | `persona_settings` (`list` first, an assistant tool, `assistant/settings_tool.go`: About Me, default launcher, and `main_tasks_quiet` are assistant-owned settings, read / written by it with its own phone confirmation cards; other settings belong to hub, listed through `app.call settings.list`, read / written through `app.tool persona_settings`, with hub confirmation): read / write triage rules (global `inbox.rules` and per-plugin-source `inbox.rules.<plugin>` for sources declaring `triage_rules`, overriding global when present), inbox task-linking rules (`inbox.agent_rules`: which running task a needs-you item belongs to; what the AI twin takes on is inbox policies. Reading or changing it says when the AI twin is off, #5865), About Me, default launcher, "Main chat tasks notify only in main chat" (`assistant.main_tasks_quiet`), per-machine muted Claude config directories (`agents.blocked_claude_dirs`), and add / remove mail accounts. Changes first show a phone confirmation card; added-account passwords / authorization codes are entered on the phone card, never through the model or chat / interaction history. After approval, hub tests IMAP / SMTP and encrypts / saves only if both work. Gmail requires Google sign-in in the App; launchers are written to unified model configuration while retaining existing model and effort |
| Live snapshot | Each message to the assistant starts with `<persona-context>` (not system prompt, see "Live snapshots and prefix caching"): time (phone timezone), online computers, Needs you / Unread / Working Agents, Lark unread and @me, today's upcoming Lark meetings, scheduled follow-ups. Agents and tasks include web links: hub `public_url` (default `http://<hostname>:18660` when unset) plus `/s/<machine>/<session id>`, `/todos?id=<to-do id>`; the assistant uses Markdown links when mentioning them. Omit if hub can't supply the address (#2194). Include only changes since this chat's previous snapshot (time always included). hub gives each source 3 seconds, reuses reads for 45 seconds, and messages wait at most 1.5 seconds for fresh data |
| Faster | When the App displays a new-chat page, `POST /api/v1/assistant/warm` starts a spare node process (`spare`, valid for 10 minutes), used directly by new chats. The assistant gets `ENABLE_TOOL_SEARCH=false`, disabling delayed Persona tool loading |
| Memory | Claude Code auto memory (`~/.claude/projects/<encoded directory>/memory/*.md`); App Settings can view / edit / delete it (node `assistant.memory`). Codex and DSH read / write the same memory ("Codex", "Agent runtime (DSH)" below), so changing launchers doesn't lose it. Health chats have a separate health space and separate memory (`docs/assistant/health.md`) |
| About Me | App edits go to settings `assistant.notes`, override `assistant.notes` in the manager's `manager.json`, and append to CLAUDE.md |
| Phone not in foreground | No phone connected during a `phone_*` call → hub sends a silent push (`content-available`, `persona_wake`) → App connects WS in the background to execute, disconnects after 25 seconds; wait up to 20 seconds. The App proactively disconnects WS on backgrounding, or a suspended socket makes hub think it is online and calls wait 2 minutes. Once disconnected, nothing else in the background reopens it (network recovery, refresh completion, reconnect backoff, `LiveConnection.suspended`, 2026-11-02); only this silent push (`wake()`) connects |

The "their computers" section in the instructions (`~/.persona/assistant/CLAUDE.md`) reflects the assistant's **actual machine** (fixed 2026-10-31; previously recognized only kind local):
- For the assistant's own computer, regardless of kind: "This is the machine you are on; run commands directly."
- `ssh_host` is an ssh alias on the hub machine, so commands `ssh <alias> '<command>'` for other ssh computers are supplied only when the assistant runs locally on hub (kind local).
- Otherwise (assistant on another computer, or a link target without ssh), don't provide unusable commands; explain that direct execution isn't possible and use `agent_start` / `agent_send` for work.

These instructions are written on that computer when the assistant prepares its directory (`assistant.prepare`: new chats, App new-chat prewarming, AI twin; `claudeMD` / `prepare` in `assistant/start.go`),
based on hub facts: `devices.list` (machine kind, ssh alias, home, data directory), knowledge base and other note directories (since 2026-11-04 the knowledge base belongs to the assistant, previously hub `kb.dirs`), `connectors.signed_in` (signed-in connectors, their CLIs, whether the manager's manager.json configures lark-cli), and `plugins.assistant` (plugin instructions and skills). A running claude process
uses what it read at startup; new instructions take effect on its next start: after 20-minute idle reclamation, or rebuilding that node connection, the next message starts a fresh process.

The node keeps the signed-in connector list and CLI mappings from `assistant.prepare` **per assistant directory** (#5831), resolving symlinks so prepare and the running session use the same key.
Preparing another directory, or signing a connector out there, leaves this directory's connector permissions and command wrappers intact. Skill synchronization uses the destination directory's list;
connector CLI calls first identify the assistant process and then check its session directory's list. An unprepared directory has no connector permissions.
The default assistant keeps `assistant-bin/` under the node's data directory; other assistant directories use `assistant-bins/<directory hash>/`, with that directory first on their process PATH.
The node's `skills.list` accepts an optional `cwd`. Without it, the node uses the default assistant directory if it has been prepared; otherwise, if exactly one directory has been prepared, it uses that directory. With no prepared directories or several non-default ones, it still uses the default directory's empty connector list. No new configuration is required.

Only the assistant gets these tools: node's inline-JSON MCP configuration includes `PERSONA_ASSISTANT=1`, `PERSONA_SESSION_ID=<chat id>`, and `personad mcp` carries both to node.
node does not take the flag on the server's word, because every process started with the assistant's environment repeats it. It forwards assistant=true only when
the socket's peer process or one of its ancestors is an assistant process node runs (a chat, the warm spare, a Codex process before its thread exists), with that chat's session id,
or a connected runtime that declared `caps.assistant`, with the session the request names when it can be one of that runtime's assistant chats ([runtimes.md](../integrations/runtimes.md) ("Assistant (optional)")).
Where the system does not tell the peer process (Windows), the request must name a session node runs for the assistant, or an assistant chat of a connected runtime that declared `caps.assistant`;
those ids cannot be guessed, but any process of the same user can read them from the assistant's command lines, so there the check stops only inherited environments. Any other request goes as an Agent's (`node/clicall.go` `mcpAssistantOf`).
hub lists these tools only for assistant=true and forwards calls to the assistant (`assistant/tools.go`). Ordinary Agents in herdr can't see them, even with the assistant's environment;
a program run under the assistant's own process (its Bash tool) counts as the assistant, as for `personad tool`, and so does an Agent in an engine whose server that shell started without detaching it.

## Boundary with hub (2026-11-04, #1446)

hub is only a conduit: the assistant runs as its own process (`personad assistant`), connected by the bus (`docs/protocol/protocol.md` ("Bus")). hub forwards App requests, caches assistant snapshots, delivers pushes and App messages, and doesn't run assistant code:
`go list -deps ./internal/hub` excludes `internal/assistant`; shared code is only message types, method names, error codes, and the assistant route table (`server/internal/assistantwire`), plus small text helpers (`server/internal/textfmt`).

All assistant business logic lives in `server/internal/assistant`, which doesn't import hub: its settings (machine, launcher, timezone, About Me, `main_tasks_quiet`, `GET/PUT /api/v1/assistant`, those `persona_settings` entries); main chat (creation, compaction, retained background from older chats, title "Persona", no renaming); chat creation (directory preparation, CLAUDE.md, prewarming, memory API, `POST /api/v1/assistant/conversations`); launcher and model per message; chats (archive, inter-chat messaging, `conversation*` tools); chat titles; scheduled tasks (scheduler, run history, `schedule_*`, `/api/v1/assistant/tasks` and related routes); dispatched tasks (plan cards, chat activity, receipts, auto-completion decisions, closing finished Agents, "Today" card, `/api/v1/task-plans`, `/api/v1/task-trust`); to-dos (moved from hub 2026-11-04: storage, all `/api/v1/todos…` routes, automatic project links and derived following, herdr markers, decision records; `docs/assistant/todos.md` ("Responsibilities")); Agent management (moved from hub 2026-11-04: stopped-turn triage, auto-continue, wakeups, its cards and answers, four switches, two `session_work` tables, statistics; new Agent tab names, launcher and model for new / resumed Agents, `assistant/agents_*.go`; `docs/assistant/agent-work.md` ("Responsibilities")); all its MCP tools (`ask_user`, `notify_user`, `agents_*`, `task_plan`… including `interaction_answer` using hub's interaction queue and Inbox `inbox_draft`: parameters, rules, and model-visible text belong to the assistant, hub supplies only data, `assistant/tools_apps.go`); message-prefix live snapshots; model configuration (moved from hub 2026-11-05: records for four categories, Persona-added providers and keys, presets, rate-limit pauses, `/api/v1/model-config…` and `/api/v1/launchers…`, every model call, `assistant/models*.go`; `docs/assistant/model-config.md` ("Who manages models")); channels (moved from hub 2026-11-04: the chat half of Lark bot and plugin channels, target chats, turn tracking, answers and questions sent back, commands, push and sync, `/api/v1/channels*`, `assistant/channels*.go`, `feishu*.go`; hub retains only transport, see "Channels" below and `docs/extensions/channels.md` ("Responsibilities")),
Inbox (moved from hub 2026-11-04: items and states, triage, event synthesis, muting, AI twin, item translation and generic translation API, all `/api/v1/inbox…` and `/api/v1/translate*` routes; hub handles only sources, `docs/assistant/inbox.md` ("Responsibilities")),
and knowledge base (moved from hub 2026-11-04: engine, loops, files in the data directory and index tables, `kb_*`, all `/api/v1/kb…` routes; hub handles only sources, `docs/assistant/knowledge.md` ("Responsibilities")).

**The assistant accesses hub through one interface**, `assistant.Hub` (`assistant/hub.go`), implemented by bus client `assistant.BusHub` (`assistant/bus.go`). Every method corresponds to a bus method:

| Method | Parameters → result | How hub answers (`assistantLink` in `hub/assistant_link.go`) |
| --- | --- | --- |
| `manager.hello` | `{id, host?, os?, arch?, deploy?, version?, capabilities}` → `{assistant:{machine_id, machines, standby?}, done_delay_ms, persona_view?, html_views?, models?, notify?, locale?, mutes?, legacy_config?}` (`owner`, `owner_notes` empty; `legacy_config` only to a built-in manager the hub did not start, [managers.md](managers.md) ("Configuration")) | What the hub knows that the assistant needs: `machines` are the computers this manager owns, `assistant.machine_id` the hub's default machine (its own, else the first, `defaultAssistantMachine`) when this manager owns it, else its first own one (`placedFor`; don't use a stored assistant machine absent from the list), `standby` is `isMember` (member hub in a linked group; primary runs scheduled tasks), `done_delay_ms` is hub.json notify completion delay (how long a task Agent must stop to count as stopped) and `notify` its push timings; `persona_view` is hub.json `assistant.persona_view` (cards as persona-view, `docs/operations/config.md`), `html_views` is `assistant.html_views` when enabled (on by default, HTML bridge available); `models.launchers` come from this manager's machines' `machines[].launchers`. `capabilities` names `config.own`: the assistant reads its own sections from its `manager.json` (`docs/operations/config.md` ("manager.json")) and lays them over the answer, as the defaults for its settings: `assistant.owner`, `assistant.notes`, `assistant.machine` (used when it is one of this manager's machines), `assistant.launcher`; `kb` (knowledge storage, `docs/assistant/knowledge.md` ("Responsibilities")); `agents` (triage limits and initial switches, `docs/assistant/agent-work.md`); the models `ai`, `inbox.triage_ai` / `judge_ai`, `kb.ai`, `inbox.agent.machine` (legacy model baseline, `docs/assistant/model-config.md` ("Who manages models")); `hook_timeout_minutes`. An older hub still sends these from its hub.json, and a section of `manager.json` replaces them. The assistant remembers answers; changes send `manager.updated`, and every answer has it read its `manager.json` again |
| `device.call` | `{device, method, params}` → that computer's result | node methods (`session.items`, `session.rename`, `session.send`, `session.start`, `session.retire`, `todo.sync_session_tokens`, `assistant.prepare`, `assistant.send`, `assistant.warm`, `assistant.memory`, `assistant.compact`, `assistant.handoff`, `launchers.list`, `launchers.probe`) forwarded unchanged to its node (`Machine.call`). hub answers `devices.list` and `interactions.list` (`device` is `*`: machines' id, name, kind, ssh alias, home, data directory, online state; phone online / what Agents await, `machinesNow`, `phoneClient`, `pendingInteractions`), `sessions.list` is the App's session list (`decorated`) plus `muted` (sessions with push disabled, which triage won't open cards for); `session.get` is node's listed entry (`Machine.session`). Missing machine returns `assistant.ErrNoDevice` |
| `app.call` | `{app_id, method, params, timeout_ms?}` → application result (`timeout_ms`: assistant wait, also stopped by hub when due, e.g. a model call) | `interactions` (queue in the manager since 2026-11-05, answered directly in-process, `assistant/fleet_routes.go`; `interactions.owner_action` still belongs to hub, redeeming owner actions before channel answers): `interactions.get {interaction_id}` → `{interaction, ok}` (`ok` is false when no longer waiting, `status` is historical resolved / dismissed / expired / cancelled, empty if nonexistent); `interactions.answer {machine_id, session_id, interaction_id, answers}` → `{ok, status}` (answer still-pending questions for that session from `answers`, recorded as assistant answers; `ok` is false if no longer waiting; errors for non-question or wrong session). `connectors`: `connectors.snapshots` (live signed-in connector state: waiting chats, today's meetings, for snapshots and Today cards, `connectorSnapshots`), `connectors.signed_in` (enabled, signed-in connectors, CLIs, manager.json lark-cli configuration); knowledge sources (2026-11-04, `kbSourcesCall` in `extmgr/kb_sources.go`, types from `internal/kb`, `internal/connector`): `connectors.archives {}` → `{archives:[{id, poll_ms?, sources?, media?, history?, history_anchors?}], items:[id]}` (connectors to read now: signed in, knowledge ingestion on, with docs / meetings / calendars), `connectors.changes {id, cursor, limit}` → `{batches, next, more?}`, `connectors.archive_media {id, ref}` → `{media:{mime, data}}` (up to 20 MB), `connectors.items {id}` → `{items}`, `connectors.doc_title {id, url}` → `{title}`; importing older history, read-only, for a connector declaring it (`docs/protocol/connector-protocol.md` "History"): `connectors.history_chats {id}` → `{chats}`, `connectors.history {id, query}` → `{messages?, older?, newer?, more?, unreadable?, pace?}`, `connectors.history_anchors {id, query}` → `{anchors?, next?, more?, pace?}`. `plugins`: `plugins.assistant` (instructions and machine-specific skills; already-installed skills include only name and summary), `plugins.skills_need` (node reports missing skills, include files next time); model-config plugin calls (2026-11-05, `hub/assistant_models.go`, removed when plugin management moved into a manager): `plugins.presets` → `{presets}` (provider presets), `plugins.voice` → `{plugins:[{id, name, models, default, error?}]}`, `plugins.need_triage` → `{names}` (enabled plugins needing information filtering). `settings`: `settings.list` (hub-owned settings readable / writable through `persona_settings`). `inbox` (hub sources, `inboxAppCall` in hub `inbox.go`; items belong to assistant): `inbox.collect {round}` (pull-to-refresh: hub reads sources immediately, delivers via `inbox.collected`, then answers), `inbox.kick` (request a round soon), `inbox.config` → `{rules, agent_rules, agent, lark_account, translate, extensions}` (triage / AI twin config, `lark.account`, requested translation languages, plugin connectors; assistant computes the translation-model fingerprint); `inbox.mail_body {account_id, uid, mailbox, readable?}` → `{from, to, cc, date, subject, text}` (full mail, no read marking; `readable` preserves paragraphs / links for HTML-only translation); `inbox.reply_mail {account_id, uid, mailbox, text, send_id}` → `{already?}` (reply using the receiving account); `inbox.reply {source, reply, send_id}` → `{already?}` (connector / Lark plugin replies or doc-status changes, "Can't reply" if connector absent). The same `send_id` sends only once (App-shared `sent_messages`; the records do not expire); repeats return `already`, interrupted partial sends return `assistant_in_doubt` and aren't resent. `inbox.settle {source, threads}` → `{over}`, `inbox.group_tool {source, call}` → `{result}` (avatars removed, truncated to 8 KB; connector `unknown_method` preserved), `inbox.group_fallback {source, fallback}`, `inbox.group_append {source, append, append_id}` → `{thing}` (repeated `append_id` uses the plugin's previous answer without another write). `app.tool {name, arguments, machine_id, session_id}` → `{content, is_error}`: only settings `persona_settings` remains, handling hub-owned settings (triage rules, muted config directories, mail); assistant-owned entries live in `assistant/settings_tool.go` |
| `notify.request` | `{notification:{id, kind:"assistant", title, body, subtitle, device_id, session_id, asked?}}` → `{ok}` | Push, same per-device settings as Agents (`notifyDone`); `asked` means `notify_user` explicitly requested it, so this chat's turn won't also push "completion" (`notified`). The assistant sends to Lark bot and plugin channels itself (since 2026-11-04, hub no longer uses `botPush` / `channelPush`) |
| `inbox.changed` | `{items:[{id, source, kind, title}]}`, one-way | Items changed and were saved (up to 50 per report): hub sends plugins `inbox.changed` and App `{"type":"inbox"}` (even without items, for triage-state changes). Runs outside the bus read loop (slow plugins don't block other assistant messages); if backlog exceeds 256, skip plugin delivery for that batch but still push to the App |
| `ws.broadcast` | `{message}`, one-way | Live App updates, `message` sent unchanged through `broadcast`: `assistant_schedule`, `assistant_feed`, `task_plan` |
| `manager.snapshot` | `{epoch, seq, at, partial?, sessions:[{machine_id, session_id, title?, archived?, queued?}], interactions:[…], tasks:[…], work:{on, sessions:[{machine_id, session_id, work}]}, profile:{machine_id, main?, launcher, timezone?, keep:[{machine_id, session_id}]}, todos:{links:[{machine_id, session_id, todo_id, title, task?}], quiet:[{machine_id, session_id}]}}` → `{ok, seq}` | hub retains only the newest (within an epoch, non-increasing seq returns `stale_snapshot`). `decorated` fills session titles (main chat "Persona"), archives, queued messages; changes push that machine's session list once. `interactions` are assistant phone questions (`ask_user`, `persona_settings` for its own settings; headless Claude lacks AskUserQuestion), displayed / pushed / expired as normal cards (`waitInteraction`); answers return via `manager.action`, missing entries are withdrawn. `tasks` is `null` if unreadable; model configuration's "Scheduled tasks" purpose reads it. `profile` contains assistant settings needed by hub features (if unreadable, `null`, retaining the previous copy), below. `todos` (`assistantwire.TodoReport`) maps Agents to unfinished to-dos (`task` means chat-dispatched task) and quiet Agents; `decorated` fills `todo`, `quiet`, `withTask` labels cards, `quietSession` suppresses push (`hub/assistant_todos.go`); unreadable means `null` / incomplete snapshot and retains prior state. Changes push that machine's list. `work` is each Agent's latest-turn work state (`docs/assistant/agent-work.md`); `decorated` applies `work` and derived `state` only to sessions still on that turn; unreadable `null` retains prior state. `interactions` also includes triage cards (`tool_name: persona_triage`, asking about that Agent session; if disconnected, hub stores the answer and delivers after reconnect) |

**Computers belong to the assistant** (2026-11-04, [machines.md](../server/machines.md) ("Who connects computers")): the assistant process also manages all computers, with `internal/devices` connecting each (hub no longer does).
Messages with hub live in `server/internal/devicewire`, over the same bus (`docs/protocol/protocol.md` ("Computers (devices.*)")); `assistant/devices.go` hands hub `devices.*` calls and events to it. `BusHub` has only a few calls:
node-owned methods in the table's `device.call` go directly in-process to that computer's node, without hub (errors remain `apierr.ErrOffline`, `rpc.ErrClosed`, node `rpc.Error`); hub-owned methods (`assistantwire.HubDeviceMethods`: `devices.list`, `interactions.list`, `sessions.list`, `session.get`, and channel `session.follow` / `session.unfollow` / `session.send_once`) still ask hub.
node requests `assistant.model` and `assistant.context` are also answered directly in-process (`Service.NodeAnswer`), not forwarded by hub; the manager tells nodes the snapshot's `profile.keep` (above).

**hub accesses the assistant only through** `assistant.Handler` (`assistant/handler.go`; hub's `assistantRemote`, same messages), one message per method: App requests (assistant `assistantwire.Routes`, bus `http.request`, forwarded unchanged; caller role in `X-Persona-Role`, overriding any client-supplied value),
`assistant.tools` (hub lists them alongside its own tools for assistant chats), `assistant.tool`, `assistant.context` (node's pre-message live snapshot), `manager.action` (user answered an assistant question), `assistant.todo_handoff` (`POST /api/v1/sessions` or `…/messages` with `todo_id`: it decides whether the to-do exists, is done, and can be handed over; prepends effective decisions when handing to an Agent), `assistant.todo_link` (link the Agent to the to-do after handoff, taking a title from the initial prompt if absent),
`assistant.start` (hub features starting an assistant chat; AI twin, Lark bot, and channels start their own since 2026-11-04, so hub no longer uses it; `{text, images?, launcher?, permission_mode?, effort?, purpose?, machine_id?, main?, untitled?}` → `{session}`; AI twin uses `untitled`, avoiding assistant-style titles),
`assistant.send_params` (`{machine_id, session_id, params}` → `{params}`: launcher and model for a message to an assistant chat; a new main chat starts empty and its first message includes the old main chat's handoff. Phone and node-generated messages (`assistant.model`) ask it; Lark bot / channels compute internally. For an unlisted machine that isn't main chat, return unchanged; malformed requests return `bad_request`, mapped by hub to 400),
`assistant.rename` (`{machine_id, session_id, params}` → node result: App renames assistant chats, main chat rejects with `bad_request`; Codex changes only the assistant's stored title, while node renames still use its `session.rename`),
`models.usable` (`{purpose}` → `{usable}`: hub plugins ask whether information filtering can be called), `models.launchers` (`{}` → `{launchers: [{name, base_url}]}`: gateway-address launchers for extension `gateway.launchers`, no keys), `models.launcher_http` (`{name, machine?, http?}`: authenticated requests for usage plugins and launcher panels, `docs/integrations/launchers.md` ("Usage"); absent launcher `no_launcher`, absent gateway `no_gateway`), `agents.start_params` (`{machine_id, params, name_from?}` → `{params}`: launcher, model, tab name for App-created Agents; unusable configuration `bad_request`, hub 400), `agents.resume_params` (`{machine_id, session_id, params}` → `{params}`: launcher / model when resuming sessions with unknown launcher), `agents.tab_name` (`{prompt}` → `{name}`),
knowledge base (2026-11-04, `assistant/kb_bus.go`): `kb.context {chat_id}` → `{context}` (AI twin chat context, `null` if absent), `kb.projects {machine_id, cwd}` → `{projects:[{slug, name}]}` (projects whose directories hold cwd on that machine; it and `kb.context` still answer, but nothing calls them now), `kb.forget {source}` → `{chats, people}` (delete source data, indexes, project references, cursors); plugin posts handed to hub (after checking knowledge ingestion is on, forwarded with connector name): `kb.posts_put {connector, sources, posts}` → `{statuses}`, `kb.posts_comments {connector, id, count, comments}` → `{}`, `kb.posts_pending {connector, limit}` → `{posts}`, `kb.archived_chats {connector, sources, source}` → `{chats}`,
one-way `kb.kick {connector?, scope?}` (source changed: run a round after 5 seconds and read that connector's chats, or the items, at that round even before their poll, at most once a minute; a manager before #1562 sends `{}`: only the round), `kb.agent_turn {machine_id, session_id, cwd, title?, line}` (Agent turn completed, append to its cwd project's timeline; hub queues while assistant disconnected and delivers on reconnect),
hub-observed events: `session.user_spoke`, `session.user_stopped` (user stopped an assistant chat in the App: Lark /agent chats stop their dispatched tasks too, #3308), `session.turn_started`, `session.turn_ended`, `sessions.changed` (a machine's session list drives inter-chat messages, automatic project / derived to-do links, herdr markers, Agent titles), `interaction.resolved` (user handled an Agent card; record in task chat, and linked Agent choice questions become decisions), `todos.space_renamed` (hub renamed a workspace), `interaction.raised` (quiet task Agent asks, #1731), `session.seen` (user viewed a session, for triage statistics), `manager.updated` (handshake answers changed); removed on 2026-11-04: `assistant.tasks`, `assistant.card_sent`, `assistant.cards_sent`, `inbox.message`, because Inbox moved into the assistant,
and `manager.resync` (hub has no snapshot or only an incomplete one: just started, database didn't answer).
Inbox (`assistant/inbox_bus.go`): `inbox.collected` (hub hands over a source round: `{epoch, seq, round, sources:[{id, name, off?, extension?, migrated?, group?, meta?, meta_description?, settles?, kinds?, things}], mail?, names}`; assistant synthesizes / triages items then answers; `epoch` identifies this hub run, `seq` the round, discard older already-used rounds; unreadable stored Inbox returns `inbox_unavailable`, retried next round), `inbox.item {id}` → `{item, ok}` (stored JSON for plugin detail slots), `inbox.forget {source}` → `{deleted}`, `inbox.repo_mutes {source, set?, add?, repos?}` → `{repos}` (GitHub settings "Muted repositories"). Snapshots add `inbox: {agents:[{machine_id, session_id}]}` for AI twin chats, suppressing hub completion push (`null` if unreadable, retain previous).
Hub errors cross the bus with codes (`no_device`, `machine_offline`, `node_closed`, `stale_snapshot`, `assistant_in_doubt` for interrupted partial Inbox replies), restored by the assistant to original errors with unchanged decisions. App routes, events, fields, and pushes are unchanged.

**State** uses the assistant's `assistant.Store` (`assistant/store.go`): settings `assistant` (its own profile, below), model config `model_config.*`, `launchers.managed`, `launchers.hidden`, `launchers.zshrc` (`docs/assistant/model-config.md` ("Who manages models")), `assistant.main_handoff`, `assistant.titles`, `assistant.archived`, `assistant.relay`, `assistant.tasks`, `assistant.runs`, `tasks.trust`, Inbox `inbox_mutes` (legacy `assistant.routines` / `assistant.followups` still migrate only on first read), channel `channels`, `channels.state`, `bots.feishu.state` (since 2026-11-04; `bots.feishu` credentials, pairing, and switches still belong to hub); tables `task_plans`, `assistant_feed`, `todos`, `todo_spaces`, `todo_agents`, `todo_decisions`, `inbox_items`.
Knowledge state is files in `~/.persona/data`, the manager's `handles.key` (`~/.persona/managers/assistant/`), and `kb_entities` (`docs/assistant/knowledge.md`).
This is hub's same Postgres, with unchanged keys / tables and no data migration; the assistant connects itself using the same `database_url`. Only it reads / writes these keys and tables; hub doesn't touch them.
Scheduled-task and finished-task Agent-closing loops belong to the assistant (`Service.Run`, beginning only after first hub connection and handshake).

**Assistant settings** (settings `assistant`, exclusively read / written by the assistant since 2026-11-04, `assistant/profile.go`): chat machine, launcher, user timezone (phone `PUT /api/v1/assistant`), About Me, main chat, replaced old main chats, `main_tasks_quiet`.
`assistant.owner` / `notes` / `machine` / `launcher` in the manager's `manager.json` supply the defaults (laid over the hub's handshake answer); JSON matches the old hub-written form without migration. Reads are cached for 5 seconds (only it writes; `manager.updated` triggers immediate reread).
`GET/PUT /api/v1/assistant` also reads and saves `default_permission_mode` in this manager’s settings. Reads identify the answering manager with `manager`. New non-guest conversations without an explicit `permission_mode` inherit it, including new main chats, scheduled new conversations and side chats. An explicit session mode, including `default`, wins. Existing conversations retain their own mode; changing the manager default never edits their settings or history. An unset preference preserves earlier behavior.

To change the default, name the target explicitly with `X-Persona-Manager: <manager id>` or `?manager=<manager id>`, including the built-in `assistant`. Capture that id when opening the settings and use it for every read and save; do not read a later global page selection. Conflicting header and query targets, an omitted target, an unknown target, an invalid mode, an empty string or `null` are refused without saving mixed fields. Omitting the field preserves it; sending `default` restores ordinary approval mode. Existing role checks apply. Web and Android assistant/model configuration pages show the selected assistant and keep requests and responses scoped to it. Native runtime policy remains subject to node version and managed restrictions; a saved preference alone does not prove the next turn accepted it.

The answering manager includes `permission_defaults: {"version":1,"manager":"<manager id>","modes":["default","acceptEdits","plan","auto","bypassPermissions"]}` in its configuration response. This describes its implemented scoped persistence and new-session inheritance; the Hub forwards it without synthesizing capabilities for older managers. Android enables the permission control only for version 1, a matching nonempty owner, and all five supported modes. Absent, partial, mismatched or unknown-version contracts leave it unavailable. Neither a manager identity nor an omitted optional setting proves support. Its save path validates the PUT owner and value, then separately reads GET and validates both again; responses from an earlier settings load cannot replace the current state. Other manager-scoped settings and navigation remain available.

Every snapshot includes `profile`, the subset hub needs. hub retains the latest (`assistantProfile` in `assistant_profile.go`) and persists changes under its own `hub.assistant_profile` key (hub-only):

- Identify phone messages to main chat (`isMainOn`), machine for model configuration's "Main chat" purpose, default launcher (`launcher`: assistant setting, else `manager.json`'s `assistant.launcher`, else claude), AI twin default machine, machine selection for `/api/v1/skills`, launcher tests and usage, and `assistant.lark` only for the assistant machine.
- User timezone (`userLocation`, `hub/usertz.go`): all hub time reads use it.
- `keep`: sessions nodes must list regardless of inactivity (main chat, replaced old main chats, scheduled-task chats, Agents linked to unfinished to-dos, #4510). Since 2026-11-04 the assistant process connects computers ([machines.md](../server/machines.md) ("Who connects computers")):
  on snapshot reporting, its device layer (`internal/devices` `SetKeep`) sends `node.configure keep_sessions` once to changed machines, all machines on the first snapshot; also supplied on node connection (`configureNode`). hub only retains this copy.
- **The first second or two after hub restart, before the assistant reports**: hub uses its persisted copy (`loadAssistantProfile`, before plugins and connectors start). Startup plugins need user timezone;
  an in-memory-only copy means they get hub timezone during these seconds and keep it until their own restart.
  (Knowledge base and Lark bot formerly depended on it too: the knowledge base reordered everything in hub timezone on each restart, then back two minutes later; Lark messages arriving then created a new main chat replacing the original. Since 2026-11-04 both belong to the assistant; knowledge startup reads its timezone directly and the assistant chooses Lark chats.)
  The first startup after moving settings lacks this copy: read once from assistant settings (machine, main chat, timezone in settings `assistant`) and save a hub-owned copy, never reading it again. `keep` is unknown then, so node connections omit `keep_sessions` (nodes retain their prior value).
  With no copy at all (fresh hub): the hub's own machine (else the first), no main chat, hub timezone, launcher `claude` (the hub no longer reads `assistant.machine` / `assistant.launcher`: they are the manager's).

**Channels** (moved from hub 2026-11-04, `docs/extensions/channels.md` ("Responsibilities")): the assistant owns the "chat with Persona" half of Lark bot and plugin channels—target chat, commands, sending into chats, turn tracking,
progress cards / answers, questions / approvals (cards, text) and answers, push / sync, source markers, and `/api/v1/channels*`. hub handles only transport: Lark's developer-platform persistent connection, credentials, pairing, owner checks, plugin `channel` programs.
The bus adds these (`assistantwire` channel types):

| Direction | Method / event | Purpose |
| --- | --- | --- |
| hub → assistant, call | `channels.receive {channel, message, action}`, `feishu.message {event}` → `{}` | Owner message (plugin channel; raw Lark private-chat event). Assistant returns once accepted / deduplicated by id, then processes internally. If disconnected, hub waits up to 60 seconds and retries dropped / timed-out calls; only then tells the owner "Assistant temporarily unreachable". A temporary dependency failure returns `try_again`, and hub redelivers |
| | `feishu.card_action {event, action}` → card callback answer | Owner tapped a bot card (verified by hub), Lark waits 3 seconds: if unfinished after 1.5 seconds, assistant first replies "Processing…", then edits the card on completion. `action` is one-time owner authorization for this card only |
| | `feishu.view {}` → `{target, agents}` | "Send to now" and bound Agent in `GET /api/v1/bots/feishu` |
| hub → assistant, event | `interaction.announced {interaction}`, `interaction.ended {interaction, resolution?}` | New cards (except quiet tasks), ended cards (answers included if answered): Lark cards and text questions |
| | `session.items`, `session.delta` | New items and streaming text for followed chats (`session.follow`): progress cards |
| | `feishu.owner {paired}`, `channels.noted {channel, machine_id, session_id}` | Bot paired / unpaired; channel-origin message entered a chat through App API (voice `voice-…`) |
| Assistant → hub, `device.call` | `session.follow` / `session.unfollow {session_id}`, `session.send_once {…session.send parameters}` | Follow a chat (hub releases all on disconnect, assistant re-follows on reconnect); send with App-shared send-once protection (`sent_messages`) |
| Assistant → hub, `app.call` | `feishu.status`, `feishu.post`, `feishu.text`, `feishu.update_card`, `feishu.react`, `feishu.unreact`, `feishu.messages`, `feishu.resource`, `feishu.save`, `feishu.user_name`, `feishu.configure`, `feishu.test` | Lark transport: send / reply / edit cards and text (owner private chat only), reactions, read messages, download images, save files on the computer, switches and features |
| | `channels.list`, `channels.send`, `channels.typing`, `channels.enable`, `channels.unbind`, `interactions.resolve {interaction_id, resolution, action, channel}` | Plugin transport and channel list (no targets or plugin feature switches); answer cards from channel replies with owner actions issued by hub in this channel (no proxy approvals); reject missing actions or actions from another channel. Credential-bearing binding is hub's route, bypassing assistant |
| Snapshot | `channels: {marks:[{machine_id, session_id, channels}], turns:[{machine_id, session_id, answered?}]}` | Per-chat source channels (session-list `channels`), turns being / just answered in channels (hub suppresses completion push); `null` retains previous |

**Why model it around the bus**: interfaces are wire messages; hub's `assistantLink` is the only implementation, with just encoding / decoding added on the bus. hub no longer computes assistant state (titles, archives, queues, questions display exactly as reported), or understands assistant semantics (tool lists, prompts, question meanings belong to the assistant).
Benefits: clear ownership; assistant crashes / hangs don't bring down hub, and vice versa; independent restart / upgrade; the assistant can run elsewhere, eventually more than one; hub no longer gains state with every assistant feature.

**Extensions, connectors, and bots also belong to the assistant process** (2026-11-05, [extensions.md](../extensions/extensions.md) ("Responsibilities")): `internal/extmgr` runs extensions, connectors, mail, Lark and WeChat bots in-process;
`assistant/extensions.go` hands over hub `extensions.*` calls / events. Assistant `app.call` to `inbox`, `feishu`, `channels`, `connectors`, `plugins` (Inbox / knowledge sources, channel transport, connector CLIs and extension skills) is answered directly in-process, not via hub;
`inbox.changed`, `kb.changed`, `kb.forgotten`, `todo.changed`, `todo.decision` destined for hub first go there for extension subscriptions. Its hub needs (cards, push, model-config queries) use `app.call` app `hub`. Channel messages described above as "hub forwards to assistant" (`channels.receive`, `feishu.message`, `feishu.card_action`) and Inbox `inbox.collected` now arrive from it in-process, with unchanged methods / parameters.

**Still in hub** (move later):

- App rename API (`renameSession`): assistant chats go to `assistant.rename`, others unchanged.
- Assistant-only tools doing hub work: hub-owned `persona_settings` entries (triage rules, muted config directories) return through `app.tool` (mail and source-specific triage rules belong to the manager's extensions; hub forwards `extensions.hub_call settings.*`). Lark tools belong to its connector plugin, never the assistant.

**No longer in hub**: `findAgent` / `machineByName`. The assistant resolves Agents and machines over the bus (`devices.list` / `sessions.list`) and keeps `conversationName` after moving the Lark bot; extmgr keeps its own `machineByName` (`extmgr/machines.go`).
The text helpers have one implementation (`server/internal/textfmt`): the hub aliases `clip`, `oneLine`, `planTitle` (`hub/tools_shared.go`), the assistant also `firstLine`, `ago`, `short`, `agentLabel`, `stateNames` (`assistant/text.go`).

Observable differences:

- When a queued message enters a running chat after its turn ends, immediately push the session list (queued entry disappears), rather than waiting for the next working-state push.
- While assistant is disconnected (hub startup, assistant restart), APIs / tools fail with `503 assistant_offline`, "The assistant is temporarily unreachable, try again later". Lists retain its last snapshot; during the first second or two after hub startup, assistant chats lack titles.
- `ws.broadcast` is re-encoded by the hub: it drops `manager` and any `event_seq` the assistant set (the event log assigns its own; an `extension_operation` skips the log and carries none), so keys go out in alphabetical order rather than in the assistant's field order.
- After settings / main chat moved on 2026-11-04, these also return `503 assistant_offline` while disconnected: `GET/PUT /api/v1/assistant`, prewarming, memory, new chat, compaction, new main chat, phone sends and renames for assistant chats;
  Lark bot / channel main-chat sends and new chats reply "Assistant temporarily unreachable" in-channel; the AI twin item fails with the same error. node-generated messages fall back to the chat's own launcher if it can't ask (same as asking an old hub).
- If Android creates an empty main chat and the first message comes from Lark / a channel, include the old main chat's handoff too (formerly only phone sends did; Lark missed it, leaving background pending until the next phone message).
- After scheduled tasks get their own chats, replaced old main chats stay listed by node too (the assistant's old `keep_sessions` omitted them until node reconnect).
- After Agent management moved to assistant (2026-11-04, `docs/assistant/agent-work.md` ("Responsibilities")): creation waits up to 60 seconds only on hub startup before the assistant has ever connected. If unavailable then, or disconnected after connecting, create / resume returns `503 assistant_offline` (it connects computers, [machines.md](../server/machines.md) ("Who connects computers")). hub stores card answers and delivers after a new snapshot; triage pauses, then the next check within 15 seconds catches turns finished but unread during absence. Triage switches, statistics, `POST /api/v1/tabs/autoname` return 503; lists retain the last work state. In the first second or two after hub restart, triaged sessions lack `work` and display Unread.
- After knowledge base moved (2026-11-04): disconnected assistant means `/api/v1/kb…` 503, `kb_*` failures, delete-source API 503 with no deletion. Archiving waits without loss (its cursors, plugin redelivery next round, Agent timeline lines queued by hub); `docs/assistant/knowledge.md` ("Responsibilities").
- After to-dos moved (2026-11-04): disconnected assistant means to-do APIs and create / send with `todo_id` return 503; automatically project-linked / derived-following Agents gain to-do fields in the next push, not the same one. `docs/assistant/todos.md` ("Responsibilities").
- After Inbox moved (2026-11-04): disconnected assistant means `/api/v1/inbox…` and `/api/v1/translate*` return 503 `assistant_offline` (rejected replies write no receipt, can retry later). hub keeps reading sources on schedule and buffering for assistant (merged, bounded, memory-only); reconnect triggers a complete collection including buffered data, preserving items that slide out of windows like latest 40 mail / latest 30 iMessages.
  Disconnected / timed-out in-flight replies return 502 `assistant_in_doubt`, `delivered: "unknown"`: they may have sent. No receipt is written; retries use the same `send_id`, so hub doesn't resend completed sends.
  Plugin Inbox detail slots can't render then; GitHub Inbox settings (CI, muted repositories) are entirely hidden (providing settings without muted repositories would clear them on save); connector-data deletion reports an Inbox error. `docs/assistant/inbox.md` ("Responsibilities").
  Previously hub collected immediately on startup; now it waits for assistant connection (hub starts its child, usually a second or two).
- After channels moved (2026-11-04): disconnected assistant means `/api/v1/channels*` 503 except hub-owned `POST …/bind`; Lark bot / plugin receivers keep receiving, owner messages wait up to 60 seconds before "Assistant temporarily unreachable" (plugins: "Not sent: the assistant is temporarily unreachable, try again later"),
  and Lark buttons get the same toast. Lark bot settings (`/api/v1/bots/feishu*`) still work except target / bound Agent. Questions appear only in the App during absence and catch up in channels on return; first startup seconds lack session `channels` (completion push waits up to 10 extra seconds for the first report). The assistant sends the successful "Paired" card with help; hub sends a short version if absent. Images / files handed to assistant total at most 40 MB per message (bus limit, formerly 50 MB per file). `docs/extensions/channels.md` ("When the assistant is away").

## Process (2026-11-04, #1446)

`personad assistant` is the assistant: it reads hub.json (`~/.persona/hub.json`, same environment as hub: `PERSONA_DATABASE_URL`, `PERSONA_LISTEN`) and its own sections from `~/.persona/managers/assistant/manager.json` (`docs/operations/config.md` ("manager.json")), connects to the same Postgres and hub `/api/v1/bus` with a bus token,
handshakes, reports a full snapshot, then starts scheduled tasks. On disconnect it reconnects (1 second, doubling to 1 minute), reporting a full snapshot on every connection.

- **Default (no user changes)**: `personad hub` generates a fresh assistant token with `createBusToken`, starts `personad assistant` as a child from its own binary, and writes the token to stdin (not argv, invisible to `ps`);
  a hub run as `persona-hub` starts `persona-manager assistant` beside it instead, when that is the same build (`version -v`), else the `personad` beside it (`hub.managerBinary`, [../server/server.md](../server/server.md) ("Programs"));
  `PERSONA_ASSISTANT_HUB` is hub's local address. Child output joins hub stderr logs with `assistant:` prefix. On exit, hub restarts it with backoff from 1 second to 1 minute, resetting to 1 second after a run of at least 1 minute.
  On hub shutdown, SIGTERM first, kill after 10 seconds. If hub disappears (including crashes), stdin closes and the assistant exits too, leaving no unreachable orphan. No systemd, Docker image, or `deploy.sh` changes needed.
- **Run elsewhere**: set `"assistant": {"external": true}` in hub.json to stop hub spawning it (`docs/operations/config.md`); on the machine where it will run, generate a token with `personad hub assistant-token`,
  then `PERSONA_ASSISTANT_TOKEN=<token> PERSONA_ASSISTANT_HUB=<hub address> personad assistant`. It must reach hub's Postgres.
  The knowledge base follows it, in that computer's data directory (copy the hub machine's old `data/` into its data directory and `managers/assistant/handles.key` and `secret.key` into its `managers/assistant/`, before the manager first starts there, to retain them; [managers.md](managers.md) ("The built-in manager's own files")); `docs/assistant/knowledge.md` ("Responsibilities").
  So does its configuration: `~/.persona/managers/assistant/manager.json` on that computer. The connector, extension, bot, Lark and Inbox sections move into it once from the hub.json there; the owner, notes, Agents, `kb`, `ai`, the Inbox's models and a card's wait come from the hub's `hub.json`, which the hub hands over at the handshake for it to take over once (that computer's own `hub.json` copies of these are not used) ([managers.md](managers.md) ("Configuration"), `docs/operations/config.md` ("manager.json")).
- Disconnected-assistant hub behavior is at the end of the previous section. In child mode the hub changes the token at each startup, so an assistant elsewhere needs `external`; a service of its own on the hub's machine is bound once instead (`assistant.mode` `service`, [managers.md](managers.md) ("The built-in manager as its own service")).
- It also manages all computers (2026-11-04, [machines.md](../server/machines.md) ("Who connects computers")): after receiving handshake `devices`, connect hub.json machines (hub-local `node --stdio` is now its child),
  and a second WebSocket `GET /api/v1/bus/relay` with the same token receives Persona Link and other-hub node streams forwarded by hub. On shutdown, hand online machines to the successor (warm start), then disconnect.
  While absent, computers show offline and machine APIs return `503 assistant_offline`; on return it reconnects them itself.

- **Other managers** (2026-11-06): managers created in the App run the same program as `personad manager run --dir <its directory>`. Identity, hub address, and token live in `manager.json` in that directory;
  the knowledge base, `secret.key`, and extensions also live there, with its own database (a Postgres of its own that install makes in Docker, never the hub's). The handshake reports its own id. Installation, directories, database, and revocation: [managers.md](managers.md) ("A second manager").

## Lark: skill supplied by the Lark extension (2026-10-30)

> History: this route began with hub directly connecting the Lark gateway, with an unchanged upstream skill copy (`SKILL.md`, `references/`, command scripts) and an upstream sync script in the repo.
> On 2026-11-04 (#1468), the gate became a generic "command gate". Since #1571 / 3c (#1520), the copy, sync script, and hub-generated gate are removed; skills, instructions, gate rules, and credentials come only from extensions.
> Since 2026-11-05 (#1317), credentials don't enter the assistant process either ("Credentials" below).

Requested: the assistant must not be limited to MCP `lark_*` tools (too restrictive), but directly read / write with the gateway's bundled skill; copy just the skill, not the entire upstream repo.

The current mechanisms are generic; specific commands, routes, and variables are in each extension's instructions (such as the Lark extension):

- **Delivery to the assistant**: extension contribution `assistant` (`docs/extensions/extensions.md` ("Assistant")) supplies a skill directory and instructions. Skills **aren't in personad** (upstream redistribution isn't allowed, `docs/operations/hub-update.md` ("Taking skills out of the binary"));
  node places them on the assistant machine and links them into `~/.persona/assistant/.claude/skills/` at each process start. User skills of the same name in `~/.persona/skills/` take precedence.
  Gated commands get a one-line wrapper in the assistant directory's command bin (the default assistant uses `~/.persona/assistant-bin/<command>`), first on the process PATH. Without the skill there is no command, and assistant CLAUDE.md mentions only MCP `lark_*`.
- **Credentials**: hub gives them only to the assistant node (RPC `assistant.lark`), from extension `assistant.credentials`. They stay in node's local proxy (`node/gateproxy.go`);
  the assistant gets only a local-only address that still requires write confirmation. `env` / `printenv` can't expose credentials.
- **Gate** (`node/larkgate.go`): the assistant uses separate hooks (plugin `assistant-hooks`, or `assistant-settings.json` for older Claude Code, `docs/integrations/claude-integration.md` ("Hooks")), with an extra Bash PreToolUse hook (herdr Agents unaffected).
  It reads the whole command:
  - With only gated commands (and output-only commands such as jq / head / grep / echo / cd / `timeout N`), check each: reads allowed directly; self-only actions (mark read, complete own to-dos) allowed;
    other writes (send / reply / recall / forward, reactions, docs, schedules, approvals, mail, group settings…) show a phone "Lark" approval card with action, chat name (extension `assistant.summary`),
    method, path, and body to send (reads `@file`). Denial returns user feedback as the reason; "Always allow in this session" remembers this route type with ids stripped.
  - A write that went out through the proxy (the API answered 2xx) is kept in the sent-as-user ledger (`docs/extensions/connectors.md` "Sent as the user"): the node tells the manager (`lark.sent`) how the gate let it through. Not in it: lark-cli writes and gates without `auth`.
  - A health conversation's writes are confirmed each time, rules' allow included (`docs/assistant/health.md` "Outbound"), at both entrances: `larkCheck` turns an allow into a card, and the proxy address opened for a process started in the health space
    lets a write through only by taking the grant that card left (a conversation's own: a new Codex conversation's addresses are given its id once its thread starts, and take nothing before). Both decide the space from a record made when
    the process is started, not from the directory a hook reports (the model's `cd` moves it); a hook directory that is a health space's counts too, so a mix-up can only ask more. Reads go as everywhere.
  - Credential variables, `/proc/*/environ`, direct gateway curl, or flags exposing headers such as `-v` / `--trace` / `-w`: reject directly.
  - Other commands (piped to python, writes outside /tmp, `$(…)`) bypass this gate and follow ordinary Bash approval in the permission mode; but any gated write inside is rejected with instructions to send it separately:
    the proxy allows only the exact call read and confirmed by the gate, so whole-command approval still can't send it (before #1317, the whole command showed a card).
  - Rules are configuration: `connector_gate` in the manager's manager.json, same format as other connector writes; `connector` is `lark`, `action` is "METHOD path" (`*` one path segment, `**` all remaining,
    `"*"` any call), `match` against top-level body fields, `decision` is `allow|confirm|deny`. They precede extension rules; first match wins.
    For example, stop confirming sends: `{"connector":"lark","action":"POST /conversations/*/messages","decision":"allow"}`.
    Legacy `lark_gate` (`{method, path, body?, action}`) still works after `connector_gate` Lark rules; hub combines both for node (`connector.LarkRules`, 2026-11-03 migration M5).

## Lark: lark-cli (2026-11-03, L1 of #273)

A hub without a Lark gateway can still let the assistant read / write Lark through official developer-platform lark-cli (`@larksuite/cli`); plan and later steps: `docs/plans/plan-lark-cli.md`.
This step covers only the assistant; Inbox, knowledge base, and `/agent` still require the Lark gateway.

- **Enable**: `lark.cli` in the manager's manager.json (`docs/operations/config.md` ("manager.json")), lark-cli installed on the assistant machine; the user runs `lark-cli auth login --domain all` once.
  A gateway can coexist; `lark.prefer` sets prompt precedence.
- **Delivery**: before process startup, node asks hub for configuration (RPC `assistant.lark` adds `cli`, `prefer`; CLI-only doesn't query the gateway; hub no longer supplies gateway tokens anyway),
  writes a one-line `lark-cli` wrapper in that assistant directory's command bin, invoking configured lark-cli (resolve nonabsolute paths in a login shell) with configured `--profile`,
  and adds its directory to PATH (npm lark-cli uses `env node`, so node must be findable). No wrapper if `lark.cli` absent or lark-cli not found.
  Credentials remain in its own `~/.lark-cli`; hub / node don't touch them.
- **Instructions**: no skill copied into the assistant directory. CLAUDE.md (`prompts/assistant/claude_md.md`) adds a section directing `lark-cli skills read lark-<domain>` first for bundled,
  version-matched domain instructions, with parameters from `--help` / `lark-cli schema`. CLI-only explains no unread / Moments and no `lark_*` MCP tools; find actionable messages with
  `im +messages-search --is-at-me / --chat-type p2p`. With both sources, divide according to `lark.prefer` (goldens `assistant_claude_md.lark_cli`, `.both`).
  Send / reply through the app bot (`--as bot`): user identity requires app permission `im:message.send_as_user`, absent in the default app (measured 2026-11-04); other reads / writes use user identity.
  In CLI-only setups, a leftover gateway skill (copied by old personad) doesn't count: no gateway is available, so CLAUDE.md must not mention its commands
  (encountered deploying to the hub machine 2026-11-03: instructions recommended gateway commands then said no gateway existed).
- **Gate** (`node/larkcligate.go`, integrated with the `ol` gate in the same `larkCheck`, also used by Codex): lark-cli knows write commands; each `--help` prints
  `Risk: read / write / high-risk-write`. node asks once on first seeing a command (cached per command, about 50 ms):
  - `read` allowed directly; `write`, `high-risk-write`, and unknown risk show a phone "Lark" card explaining actor (`--as bot`: "as the bot", otherwise "as you"), action (send, reply, react, recall…), and full command;
    high / unknown risk is separately marked. Denial returns user feedback; "Always allow in this session" remembers the command.
  - Output-only allowed directly: `--help`, `--version`, `--dry-run`, `schema`, `skills`, `whoami`, `doctor`, `auth status / check / scopes / list`, `event list / schema / status`;
    `lark-cli api` uses HTTP methods: GET / HEAD allowed, others confirmed (method, path, `--data` on card).
  - Reject `auth login / logout` (user handles login), `config`, `profile`, `update`, custom `--profile`, or access to `~/.lark-cli/`.
  - Unparseable commands containing lark-cli (loops, `$(…)`, python pipelines): if any call isn't a known read, confirm the whole command on the phone regardless of permission mode.
  - Match manager.json rules first: `connector_gate` `{"connector":"lark","action":"CLI /im/+messages-send","decision":"allow"}` (legacy `lark_gate`
    `{"method":"CLI","path":"/im/+messages-send","action":"allow"}` also works).
- **Not done**: runtime assistant (DSH, pi) prompts and extension-gate tool declarations still only know the gateway; settings-page lark-cli status and missing permissions (L2).

## Extension MCP servers (2026-11-01, Claude assistant only)

Extensions can contribute `mcp_servers` (`docs/extensions/extensions.md` ("MCP servers")): when the named CLI is installed on the assistant computer, node attaches it as another MCP server for the **Claude assistant**
(`mcp__<id>__*`, `node/assistant_mcp.go`; not Codex / runtime assistants). Commands read their own configuration; Persona neither reads nor passes credentials. Listed read-only tools are allowed directly,
named denied tools are never callable; others request phone permission according to chat mode. For example, a Lark extension attaches upstream personal-Agent control tools for scheduling personal Agents and inspecting pending requests
(approvals only forwarded to the user, never approved on their behalf; Herdr / Orca tools overlapping Persona `mcp__persona__agents_*` are disabled). Specific tools / switches are in extension instructions.

## Always allow in this session (2026-11-06, #2202, #5867)

Tapping "Always allow in this session" on an assistant permission card remains effective throughout the conversation, including process restarts and scheduled follow-ups. The card explains the scope before approval: Claude file tools grant that tool in the file's directory; Bash grants **all commands in this conversation**, not just the displayed command. Other Claude tools grant all calls to that tool. Codex command and file approvals retain their narrower command/path scopes; runtime-provided labels describe that runtime's scope.

- Node supplies `always_keys` on assistant cards (Agents-page sessions keep their existing behavior): Claude `tool:<tool name>` for a whole tool, or a directory rule such as `tool:Read(//workspace/project/**)` for file tools; Codex `codex-command:<command>`, `codex-edit:<file>` / `codex-root:<directory>`, and `codex-permissions:<permission>`; Lark `lark:<route>` with IDs stripped. Whole-command gate confirmations are not remembered.
- The manager stores approved keys in Postgres `assistant_always` by conversation ID and key SHA-256. Every requested key must match to auto-allow a later card. Directory keys are exact matches; a subdirectory can prompt separately. Only explicit permission grants are remembered; denials, one-time allows, and unknown keys still prompt. Automatically replayed answers do not write grants back, so an in-flight answer cannot resurrect a deleted grant. A remembered grant also answers the covered cards already waiting in the conversation (parallel tool calls produce several). Such an answer carries `remembered: true` (`proto.Resolution`), so the node can tell it from the answer to a card: the sent-as-user ledger records a Lark write let through this way as `conversation_always` (`docs/extensions/connectors.md` "Sent as the user").
- For assistant conversations, Claude no longer receives `addRules … destination: session`; Codex command/file approvals are one-shot and extended permissions use turn scope; the Lark gate consults the manager instead of keeping its own grants. The manager remains the authority across processes. Agents-page approvals are unchanged.

On the web, open the conversation's **Session info → Always allowed in this conversation** to view and revoke individual grants. Each row shows the tool and approval time. The existing table stores only a key hash, so exact historical directories/commands cannot be reconstructed; the UI says the detailed scope is unavailable. No migration or wider replacement grant is required. iOS and Mac show the same list in **Session Info** and ask before revoking (`AlwaysGrantsSection`); a hub without these routes or a device that may not use them shows no section there. Android shows it in the Persona tab's **Session info** for the open conversation, also asking before revoking (`AlwaysGrants.kt`, `AssistantManagement.alwaysGrants` / `revokeAlwaysGrant`; #890); a hub without the routes or a device that may not use them shows only the conversation there.

The full-access API is relayed by the hub to the conversation's manager:

- `GET /api/v1/assistant/conversations/{id}/always` → `{"grants":[{"key_hash","tool_name","created_at"}]}`; an empty list is `[]`.
- `DELETE /api/v1/assistant/conversations/{id}/always/{hash}` → `{"ok":true,"restart_required":false}`. Deletion is idempotent and scoped to that conversation. Extension tokens and answer-only devices cannot use these endpoints.

Revocation first deletes the persisted grant, then calls node `assistant.permissions.reset` to clear cached gate rules and terminate the conversation's existing Claude/Codex process group and wait for exit. This also removes session rules installed before this change and Codex turn permissions. **Revoking stops the current response**; send another message to resume the same transcript. Work already authorized or completed cannot be undone. Unrelated stored grants remain effective when the conversation resumes; explicit bypass modes and configured allow rules are separate settings.

If the node is offline, too old, fails to acknowledge the reset, or the conversation belongs to an external runtime without a permission-reset protocol, deletion still succeeds with `restart_required: true`. The web retains a warning: **revocation takes effect after the conversation restarts**. Restart the conversation's process in that runtime before continuing; merely reopening the web page does not clear process permissions. Retrying the DELETE also retries the reset even when the row is already gone.

## Password card (#1137)

When the main chat needs a password only the user knows (typically a computer's sudo password), the Agent never asks for it in the chat. The assistant-only tool `secret_run` (`server/internal/assistant/secret_run.go`, description in `prompts/mcp/assistant/secret_run.md`) proposes the exact commands; the user reads them in the Persona app (iPhone, Mac, Vision Pro, Android or the web), types the password into the card and taps Run. The target computer's node runs exactly those commands, giving the password to the one that runs `sudo -S`. The Agent gets exit codes and output that never held the password, never the password.

- **Checks before the card** (code, not prompt): the machine resolves by id or name (`machine` must be given), and its node answers `secret.run {"check":true}` (an offline computer, a node without the `secret_run` capability and a Windows node fail here). `purpose` is one line of at most 300 visible characters: no control, format (zero-width, bidirectional), line or paragraph separator, private-use or unassigned characters. 1 to 5 commands, each one line of at most 1000 characters of printable ASCII (`proto.SecretRunCommandProblem`): anything else could make a command look other than what runs (a zero-width space in a file name, a no-break space inside a word, a look-alike letter), and the error names the command and the character. A command that calls `sudo` (where a command starts: `proto.SecretRunSudoOf`) must pass `-S` (`sudo -S -k -p ''`, as the node has no terminal for sudo to ask on), call it once (stdin has the password once) and not after a pipe (sudo would read the pipe). `timeout_seconds` covers the whole run: 300 by default, at most 1800.
- **The card** is an ordinary `permission` interaction, so every client and safeguard already handles it: `tool_name: "secret_run"`, `tool_input {machine, machine_name, purpose, commands, delivery}`, a `summary` that repeats the computer, the purpose and every command after `$ ` (for clients that don't know the tool), one `secret` field `password`, and a 10-minute expiry. `machine_name` (and the summary) carries the computer's id too, as `Mac mini (macmini-2)`, when another computer of the manager has the same name or that name as its id or ssh alias. A card with fields has no quick approve anywhere. Only the tool makes this card: one from a node (a hook, a runtime, a daemon, a Codex list) or an extension (`fleet.ask`) with `tool_name: "secret_run"` is refused before it reaches the clients (`reservedCard`), since the clients show it as the trusted card without an Agent's name.
- **Who can fill it in**: only a paired app's answer from the card's own form: the iPhone, the Mac, Vision Pro, Android and the web show the password field and Run. The watch shows the commands and offers only Decline: the hub treats it as an answer-only device and refuses `values` from it. The clients keep the password only in the card's own state, never stored or restored (no drafts, storage, saved state or password-manager saving), and clear it on Run, on Decline and when the card goes or another takes its place. These answers run nothing and give `refused`: one the assistant wrote (`answered_by`), a remembered "Always allow", one without the password, one carrying the history's `<redacted>` marker (a replay), and a password with a control character (sudo would take only its first line). Lark and plugin channels can only decline such a card (`channelResolve`).
- **The run** (`node/secretrun.go`): the manager sends the password to the node in the `secret.run` params, in memory over the existing connection. Each command runs with `/bin/sh -c` in the home directory with `PATH=/usr/bin:/bin:/usr/sbin:/sbin` (a `sudo` earlier in the user's own PATH is not what runs; other tools go by their absolute path), in its own session and process group, stdout and stderr read together. A command that runs one `sudo -S` gets the password and a newline on its stdin, then the pipe closed; every other command gets an empty stdin (`/dev/null`). Where a sudoers rule says `NOPASSWD`, sudo does not read stdin and the command it runs gets the password instead. The run stops at the first command that exits non-zero, cannot start or runs out of time. At the deadline the command's group gets SIGTERM (sudo passes it on to the command it runs as root, which the node cannot signal), 5 seconds to exit, then SIGKILL until the group is empty (`osproc.SweepGroup`); a root command that ignores SIGTERM can outlive the run, and the result tells the Agent to check its state before asking for a new card. The node masks the password in the output as it is read (holding back only the last `len(password)-1` bytes, which may begin an occurrence the next read completes), keeps the last 256 KiB of the masked output and returns the last 16 KiB. A command whose output held the password returns none of it (`output_withheld`): where a masked password sits (`/Users/***/`) could give a weak one away. The manager checks the output again and gives the Agent the last 4000 characters of each.
- **Result**: one JSON object, `{status, machine, message, results}`. `status` is `ran`, `failed`, `declined` (Decline, or the card closed in an app before its deadline, as "Hand back to computer" does), `expired` (not answered by the deadline) or `refused`; `results` (`command`, `exit_code`, `timed_out`, `output`, `truncated`, `output_withheld`) come only with `ran` and `failed`, and the commands after the first failure are absent. Errors before the card are plain-text tool errors.
- **One use**: the first answer claims the card; a second gets the existing "already answered" reply and runs nothing. The password lives only in the waiting tool call and the node's handler. A hub, manager or node restart drops it: the interrupted call returns an error, and the Agent has to ask again with a new card. The tool call comes over the node connection of the computer the conversation runs on (`mcp.call`): when that connection ends, the call ends with it, the card is withdrawn and nothing runs. The node is not told when the tool call stops waiting; it stops the commands at the run's timeout.
- **For every card with secret fields** (adding a mail account too): `interaction.resolved` / `interaction.ended` (the channels, the task feeds) carry the history's redacted answer; values for keys a card does not declare are dropped when it is answered; an answer to a card with a secret field is never held for redelivery (`settleAnswer`); and the hub's operation receipt for `resolve` (`client_ops.digest`) covers the answer as the hub reads it (`proto.Resolution`, field names in any case) with each value replaced by its key, and one fixed body for any that is not an answer, since the sha256 of a short password can be guessed offline.

Threat model and limits:

- Protects against the password reaching the database (interaction history, operation receipts, task feeds), logs, the Agent's transcript, environment, files, process arguments or tool result, channel messages and push payloads.
- Does not protect against a process that attacks Persona as the same user on the hub, the manager's or the target machine (it can read process memory, or use the hub token as an app, #813). An Agent with full shell access on the hub machine is in that class if it sets out to attack Persona: the card stops accidental exposure, not a hostile Agent.
- A command on the card that reveals the password in another form (`base64`, a file, a remote host) shows that form: only the literal password is found. The user's check is the card, which shows every command verbatim, and nothing else runs.
- Go strings cannot be wiped. The password slices the tool call and the node's handler hold (the bytes handed to the node, the decoded password, the node's request params and its output buffers) are zeroed when the run ends; request bodies, RPC frames, the JSON copies on the way and `exec`'s own copy buffers are left to the garbage collector. The password crosses the existing TLS / ssh connections in plaintext; end-to-end encryption to the target node is a follow-up.

## Assistant answers questions first (2026-11-05, #3029)

With over ten Agents running, most mid-task questions concern internal choices the user can't answer (technical approach, default definitions, reruns, branch choice). Agent questions therefore reach the assistant first: answer what it can, send only red-line and goal decisions to the user. `agents.delegate` in the manager's `manager.json` (**off by default**, enable with `enabled: true`; examples in `docs/operations/config.md`).

- **Eligible questions**: `kind: question`, including hook AskUserQuestion, Codex questions, and triage "Your decision" cards (`persona_triage`, also `waitInteraction` → `announce`). Permissions, plan approvals, terminal dialogs, forms, assistant `ask_user`, and questions within assistant chats go directly to users; triage cards requiring authorization links or risky actions such as push / deployment aren't answered either. Don't wait if assistant disconnected.
- **Answer only real questions, once per question** (2026-11-06): Agent requests (AskUserQuestion, Codex) already await input; triage cards are inferred from stopped-turn text. Delegate only if a question contains `?` / `？` or at least two Agent-authored options (not manager defaults like "Okay, do as you said" / "Leave it for now") (`asksForInput`). Declarative stops ("Waiting for runner recovery and 0.14.0 release") get only fallback choices; formerly the assistant selected "Okay", the Agent repeated itself, another card appeared, and delegation repeated—one board Agent answered three times in a minute. Remember delegated questions per session (compare after removing spaces / punctuation, or bigram similarity ≥ 0.8; `delegated` in `session_work_events`, reloaded on restart): the same Agent repeating within 24 hours goes to the user, recording `delegate_repeat`.
- **Flow** (`assistant/fleet_delegate.go`; queue in manager since 2026-11-05, #3246): manager `announce` first reports the card to hub with `delegating: true` (visible / answerable in the App, no push or bot card), then evaluates in the background for up to `timeout_seconds` (default 3 minutes). Assistant answer → same `interaction_answer` path (`answerQuestion` → `resolve`, `answered_by: "assistant"` + `reason`), with reason / answers in history and `interaction_resolved`. User handoff, failure, or timeout → remove `delegating`, attach `delegate` context, and use the normal push / bot / channel / quiet-task path; hub appends "Assistant suggests: X" to push body (`hub/push_payload.go`). If the user answers during evaluation, first answer wins and the assistant result is discarded.
- **Decision** (`assistant/delegate.go`, prompt `prompts/agents/delegate.md`, separate model-config category "Question delegation", `agent.delegate`: before selection uses the information-filtering provider and Opus 5.5; change on that Settings › Model Configuration page, `docs/assistant/model-config.md`): one model call reads user messages / final Agent text from the latest three turns, its to-do (title, notes, decisions), cwd project, question, and choices. Prompt rules: answer internal choices with a short reason; send irreversible actions (deploy, release, merge, delete data, force-push, external messages), socially visible actions (MR comments, reviewers, approvals / tickets, contacting people, creating groups, touching others' MRs), and goal changes to the user, supplying "what this is, why ask now, recommended answer, cost of not choosing, why you must decide". Default deadline 24 hours, or earlier card expiry. Two hard checks follow: every question answered using listed choices (same `interaction_answer`, no free text); selected labels / descriptions containing `riskyWords` (push, deploy, merge, send messages…) are handed to the user as recommendations, except short "don't do it" choices such as "Don't merge yet" / "Don't send". User `rules` can broaden internal choices but never red lines.
- **Why one model call, not a persistent Claude chat**: handle questions from over ten Agents concurrently within 3 minutes; headless chat startup alone takes a minute or two and its tools create permission cards. Single-call input / output is easier to test and inspect in goldens. Insufficient context goes to the user (uncertainty handoff is a prompt rule).
- **Why not an extension**: it attaches to hub `announce` and interaction `resolve`, both core flows. No "intercept new interaction" extension point exists; extraction first needs one (manifest validation, permission / consent to answer for users, process protocol, timeout fallback), not a smaller change.
- **Audit and changing answers**: triage answers sent to Agents explain "The assistant answered for me under my rules (reason: …); I may change my answer later" (`prompts/agents/answer.md`); task chat activity says "Assistant answered: X, because Y". Don't record delegation as to-do decisions (those are the user's). To change: `POST /api/v1/interactions/{id}/reanswer {answers}` (hub forwards to manager; card-answer-only devices can't use it). The Agent has already acted, so send the new answer as a message (`prompts/agents/reanswer.md`, including original assistant answer), broadcast `interaction_reanswered`, and record the user answer in task history / decisions.
- **Statistics**: each evaluation writes `session_work_events` (`delegate_answered` / `delegate_asked` / `delegate_failed`), shown with triage statistics.
- **Client UI**: web has "Assistant is looking", the `delegate` context and "Change answer", plus the delegated answers in **Session info** (#544, `docs/web/web.md` "Questions the assistant sees first"); iOS / Mac have the same (`docs/apple/ios.md`, "Questions the assistant sees first"). **Not done**: Android. Questions being delegated during manager restart aren't delegated again or catch-up pushed (cards remain in the App).

## Main chat, knowledge base, and context flow (2026-10-30)

Prompts live in `server/internal/prompts/assistant/` (`docs/assistant/prompts.md`): CLAUDE.md is `claude_md.md`, snapshots `snapshot.md` (main-chat sentence in `context.md`), due-task messages `task_run.md`, default morning briefing / evening review `routine_*.md`.


- **Main chat**: the first chat becomes main chat (assistant settings `assistant.main`), opened by default in the Persona tab. Snapshot begins "This is the main chat"; prompts require continuing prior work.
  node offline lists only the latest 80 sessions within 7 days; on a busy machine, a main chat quiet for two days was crowded out, removing "Main chat" from the sidebar (2026-11-01).
  The assistant reports these sessions in profile `keep`; its device layer (hub before 2026-11-04) supplies `node.configure` with `keep_sessions` on node connection and changes, listing them regardless of age.
  Handoffs from other pages (Agents long-press "Hand to Persona", knowledge "Ask Persona") enter main chat as `<persona-context>`.
- **Knowledge base** (`docs/assistant/knowledge.md`): `kb_search` / `kb_list` / `kb_get` / `kb_update`; snapshots include key projects / chats; prompts require writing conclusions back
  (project progress to timelines, code directories to `dirs` for Agent-project matching, read project profiles before dispatch).
- **AI twin**: item-processing prompts include the chat and its top 3 project profiles; afterward write new conclusions back to projects.
- Assistant and AI twin default launchers: `assistant.launcher` / `inbox.agent.launcher` in the manager's `manager.json` (local specifics in `CLAUDE.local.md`).
- The launcher in Settings › Model Configuration › Main chat (stored `model_config.assistant`, legacy settings `assistant.launcher` compatible; removed from Settings › Assistant since #1291) controls main and new chats: used for hub main-chat sends and when the App doesn't specify a launcher.
  If node sees a mismatch with the running process, it uses the new launcher with `--resume` on the same chat (fixed 2026-11-01: previously only new chats changed, main chat retained the initial launcher,
  appearing ineffective). Every main-chat message follows this setting, not the previous turn's launcher: #1355 briefly preserved the running launcher, so a follow-up changing it prevented returning (#1518).
  But a chat can resume only with its original Agent type. If settings select another type (Codex main chat → Claude launcher, vice versa, or into / out of a runtime), node uses the chat's launcher; if its remembered launcher is also the wrong type (overwritten by a failed start), use same-type `claude` / `codex`, ignoring the hub model for this message (`assistantResumeLauncher` in `node/assistant_agent.go`).
  Previously node rejected only while the process lived, then tried `claude --resume <thread>` for exited Codex threads: every message returned `No conversation found`, main chat showed "Session ended", messages stayed "Couldn't confirm delivery" (2026-11-05). To change Agent type, create a new main chat.
  `schedule_followup` returns to the original chat using the same rules (main chat follows settings, other chats keep their own), not the Agents category (`sendModel` in `assistant/start.go`, hub asks `assistant.send_params`). Scheduled-task chats (morning briefing, etc.) also use the "Main chat" category, following settings per message and follow-up (#1544). The top-bar "Launcher" menu changes only this chat's next message; opening a launcher also allows selecting a model from its model list (the `/model` list),
  sent as message `model`, which hub uses for the execution fingerprint and node process restart; unselected means the launcher default.
- **Don't switch while busy** (#1554): while a process answers or has background tasks, changed launcher / model messages still go to that running process (session state records the actual one);
  restart with the new selection on the first idle message. Previously it rejected with "Model configuration changed; wait until the current task ends", trapping users when the assistant kept creating background tasks.
  node's post-restart "Background tasks were lost" message also first asks hub (`assistant.model`, forwarded to assistant `assistant.send_params`, same as phone sends), not starting from the session's last model.

## Permission mode and effort changes (2026-11-06, #5947)

For a running Claude conversation, the node matches each `set_permission_mode` or `apply_flag_settings` request to its
`control_response.request_id`. Writing the request to stdin does not mean Claude accepted it: `session.modes` waits for
the matching response before confirming the change. The same check runs before sending a message when a new or resumed
process needs its settings restored. This applies to both the Claude CLI and the Agent SDK bridge.

- Accepted fields update the process, the session list, and the persisted conversation settings. If a request changes
  both mode and effort, they are applied in that order; a successful mode change remains applied if effort is refused.
- A refused field returns to its previous value. The HTTP request fails with `409` and code `assistant_control_failed`,
  with a localized explanation containing Claude's original reason and the original reason in `detail`. Existing clients
  show the failure and restore their optimistic selection; no configuration is needed. An environment override such as
  `CLAUDE_CODE_EFFORT_LEVEL` or a model that cannot use the requested effort can cause a refusal.
- Unknown or duplicate response IDs cannot confirm another change. A process exit, a failed write, or an unconfirmed
  request also reports failure instead of claiming that the setting was applied.
  If the outcome is uncertain, the node stops the child process so a late acknowledgment cannot leave it running with
  a setting the app reported as failed; the next message resumes the conversation with its recorded settings.
- Settings chosen while the conversation has no running process are kept for its next start. The existing
  `bypassPermissions` restart rule remains: a process not started with it cannot switch to it through a control request;
  a busy process finishes its work before the restart. Ultracode remains available only in Agent sessions.

## Workspace mentions (2026-11-06, #5594)

In the assistant's composer (web), `@` also lists **workspaces**: the discovered workspaces of `GET /api/v1/workspaces` (host spaces, current session folders and folders bound on the workspace board, on every computer; see [workspace-board.md](../web/workspace-board.md), "Workspaces and status"). The list is read each time the menu opens, works whether or not the board is turned on, shows one row per computer and folder, and leaves out host spaces without a folder. Agents' composers don't offer it: an Agent already works in one folder.

- **Picking one** adds a "Workspace" chip. On send it goes in front of the message like a session quote, as `<persona-context kind="workspace" title="…" url="" machine="<machine id>" cwd="<folder>" branch="…">` with a short human-readable body.
- **The manager fills it in** (`assistant/workspace_mention.go`): in `SendParams` for messages to the assistant's conversations and in `POST /api/v1/assistant/conversations` for a new one's first message (guest conversations excepted), each workspace block among the context blocks at the start of the message is replaced by `prompts/assistant/workspace_mention.md`. That block says to use that machine / cwd for `task_plan` and `agent_start` and when answering unless the message says otherwise, and lists the computer (and whether it is offline), folder, branch, the knowledge-base projects for the folder (`ProjectsForWork`) and the latest 5 Agents working in it or below (`sessions.list`, waited on for at most 3 s; the assistant's own conversations left out).
- Blocks further into the message (inside quoted text) are never taken for mentions, and a block naming a computer the hub doesn't know stays as the client wrote it. The transcript keeps the filled-in block, so the chat's context row shows what the assistant was given.
- **Not yet**: iOS / Mac (`PromptComposer.swift`) and Android composers. They only need to list the same endpoint and write the same tag; the manager side already handles their messages.

## Pitfalls

- Models call ask_user **in parallel** with other tools (measured: ask_user and schedule_followup in the same millisecond), so ask_user can't be the sole "confirm before acting" gate;
  genuinely side-effecting tools (`lark_send`, `mail_send`) request approval themselves in hub.
- Don't catch up very late scheduled tasks: after a morning of hub downtime, no noon morning briefing (skip if over 30 minutes late, record only).
- Scheduled tasks, follow-ups, and run history count as loaded only after successful database reads. Failures never fall back to the two default tasks; related APIs return 503, tools fail, the scheduler skips the round, and nothing is written;
  retry reading on the next call. Previously failure still marked loading complete, and the next save overwrote real tasks / follow-ups (review finding 2026-11-02, `TestScheduleReadFailureNeverOverwrites`).
- Silent pushes are system-rate-limited and don't reach force-quit Apps; failed wakeups tell the model to ask the user to open the App.
- Snapshots formerly used `--append-system-prompt`, assuming restarts refreshed them. Claude Code resume actually retains the system prompt from the chat's first start (next section),
  so the assistant kept seeing the first day's time / unread state. Now every message carries a snapshot.
- Go zero time `0001-01-01T00:00:00Z` appears in `last_run` / `next_run`; App `Date.realDate` treats it as absent.
- **Unattended runs must not block on approval** (measured 2026-10-30): default assistant permission mode asked phone approval even for `notify_user`, `schedule_followup`;
  nighttime follow-ups and Monitor events stopped there, pushing only "Approval needed". Persona tools are now preallowed (table above).
- **systemd restarting hub (compatibility unit `personad`) kills the whole cgroup**: assistant claude, background commands, and Monitor disappear before node can clean up, so running task lists
  must be persisted continuously (`node_sessions.tasks`), not only on shutdown.
- **Failed process startup swallows messages** (2026-11-05): node wrote a new process's stdin and immediately told hub "sent", while the launcher had already exited (e.g. `personad launch opus: no persona node`),
  losing the message. The App stayed "Delivering", logs only contained a zsh theme-plugin notice with `error`. A first send to a new process now waits for its first output line (up to `assistantStartWait`, 8 seconds):
  early exit reports "<launcher> didn't start; message not delivered: <last stderr lines>", and App shows retryable send failure. No output after 8 seconds counts as started (slow SessionStart hooks).
  The process stores stderr tail with ANSI and blank lines removed (`watchAssistantStderr`).
  Exit before writing formerly returned a pipe error (`send to claude: write |1: file already closed`); now wait for exit and drained stderr, reporting the same "Didn't start (exited after N seconds): …".
  For shell command-not-found (exit 127 / `command not found`, usually Claude Code auto-update reinstall removes `claude` for about 2.5 seconds),
  restart after `assistantStartRetryDelay` (2 seconds), up to `assistantStartRetries` (3), invisibly to users; other startup failures aren't retried (#2581, `docs/lessons/server.md`).
- **Another process replaces node's socket file** (2026-11-05): another node listened on the same path and didn't unlink on exit (`SetUnlinkOnClose(false)`), leaving an unserved socket;
  every `personad launch <Persona-managed launcher>` and hook got `connection refused`. The watchdog once checked only existence; now every 15 seconds it checks ownership (`os.SameFile`),
  dials if different, and recreates only when unanswered (`sockGone`). A responding newer node keeps the socket; don't take it back.
- In Chinese locales, `.hour(.twoDigits(amPM: .omitted))` is 12-hour (17:00 appears "05:00"); always use `AssistantScheduleText.hm` (fixed `HH:mm`).

## Live snapshots and prefix caching (2026-11-01)

**Why snapshots can't be in system prompts**: prompt caching matches prefixes. A request consists of system prompt (including tool definitions), project context (CLAUDE.md, memory),
then the whole chat. Any earlier byte change prevents reading the later history cache, requiring recomputation at cache-write price and speed. Assistant processes are reclaimed after 20 idle minutes, then restarted with
`--resume`, so only startup-invariant content belongs at the front. Snapshots (time, unread, meetings) always change and must accompany messages at the chat's end.

**Reality is more complicated** (Claude Code 2.1.314 measured on hub machine, same launcher):

- Resumed Claude Code chats **don't use** new `--append-system-prompt`; they retain the system prompt from the first request until compaction (documentation "System prompt flags in
  resumed conversations"; after changing text, the model still answered the first secret phrase and read the whole cache). Old snapshots therefore didn't invalidate cache: they **stayed the first day's snapshot**.
- What really breaks post-restart cache reads is **changed tool definitions**: copy a real 275k-token assistant chat (new session id, temporary directory, same exported Persona definitions),
  then restart; results below. Schema key order doesn't matter; removing one MCP tool rewrites everything. Persona definitions come from prompt files (deployment changes them)
  and phone-reported `phone_*` (the last connected device and its permissions), so node now compares tool lists at each start, logging
  "tools changed since its last start" on changes and cache read / rewrite counts for the first request after startup (`assistant_context.go`, `readAssistant`).
- Idle over an hour (cache lifetime) always rewrites; only smaller context helps then, see "Compaction" below.

| Main-chat copy (275k tokens) | First token | cache_read | cache_creation |
| --- | --- | --- | --- |
| Cold start (expired cache) | 9.8 s | 0 | 275,373 |
| Restart, unchanged | 7.0 s | 275,327 | 91 |
| Restart, changed `--append-system-prompt` text | 5.2 s | 275,418 | 46 |
| Restart, reordered tool schema keys | 7.7 s | 275,464 | 46 |
| Restart, one MCP tool removed | 8.5 s | 0 | 275,385 |
| `/compact` (warm cache) | 35 s to compact | 275,393 → 3,507 (compacted history) | |
| Cold start after compaction | 5.3 s | 0 | 36,195 |
| Another restart after compaction | 4.1 s | 32,886 | 3,354 |

Pre-change production records (first transcript reply after over 20 minutes idle): the long chat begun 10-28 had five 39–59-minute idle periods on 10-29–10-30 (cache still valid),
with `cache_read` = 0, rewriting 300k–370k tokens and taking 10–320 seconds for the first response segment, while other equally idle runs read cache. Every idle period over an hour rewrote fully, the last 510k tokens.
Current main chat's first message on 11-01 after 46 idle hours: 254k rewritten, 12.6 seconds.

**Implementation**:

- Before each message, node requests a hub snapshot (`assistant.context`, parallel with process startup), compares sections (unindented header plus indented lines) against the last sent snapshot for this chat,
  and includes only changes and time in `<persona-context>` (`prompts/assistant/turn_context.md`; old nodes / history use `<persona-context>`, readers recognize both, #3182) as the first text block. Prior content is remembered in node memory,
  cleared after compaction (`compact_boundary`; Codex `contextCompaction`), so the next message is full; node restart also sends one full snapshot. App, web, titles, and knowledge base remove it in
  transcript parsing (`transcript.ContextRe`), hiding it from users. Scheduled tasks, Lark `/agent`, and post-restart lost-background-task explanations all use `assistant.send`
  and include it too.
- No `--append-system-prompt` remains; Codex `developerInstructions` contains only memory (changes with its index; resumed handling not measured).
- hub snapshot: reuse data for 45 seconds; refresh stale data in the background, wait at most 1.5 seconds per message, then use the prior snapshot if within 30 minutes. Time is always current; ended items removed.

## Compaction (2026-11-01)

Long assistant chats can grow to hundreds of thousands of tokens. After the prompt cache expires, restarting rewrites the whole context; compacting while the cache is warm keeps that restart affordable. The persistent main conversation also supervises many tasks, so frequent compaction loses useful details. It has more room than short-lived conversations.

- **Scope (2026-11-07, #6481)**: the assistant manager sends `is_main` when starting the main conversation and on subsequent messages, including scheduled wake-ups and node-requested model settings. Only the current main session on its configured machine gets the larger limits; ordinary conversations, scheduled-task conversations, Lark `/agent`, guests, and Inbox twins retain ordinary limits. The directory and model purpose are not a main-chat marker.
- **Automatic compaction**: the main conversation targets `CLAUDE_CODE_AUTO_COMPACT_WINDOW=400000`; other conversations target `200000`. The node pins the per-start value in both the process environment and merged Claude settings, preserving launcher settings and hooks. CLI and Agent SDK starts share this policy. Confirmed previously in `-p` stream-json: compaction before the next message emits `system/status` `compacting` → `compact_boundary`, `trigger: auto`; that turn waits for the summary.
- **Backend capacity**: take the smaller of the target and the selected model's declared context window / explicit `CLAUDE_CODE_MAX_CONTEXT_TOKENS` (including launcher environment and settings). A launcher's `AUTO_COMPACT_WINDOW` is policy, not proof of backend capacity. Pin `MAX_CONTEXT_TOKENS` to that capacity too, so Claude Code's internal minimum does not silently retain a 200k ceiling on a verified larger backend. If main-chat capacity is unknown, conservatively retain 200k rather than advertise 400k to an unverified backend. No backend window is enlarged by this change.
- **Idle compaction**: Claude Code's own idle compaction runs only interactively, not `-p`. Before reclaiming an idle process, node sends `/compact` at 300k tokens for the main conversation, 150k for others, while the cache is warm. When backend capacity lowers the automatic window, the idle threshold is capped at 75% of that window (a 200k backend remains 200k / 150k). Don't compact this process again; reclaim normally next idle. This isn't a chat turn: App state unchanged, no push; transcript `/compact` displays "Compact context".
- **Codex**: the main conversation targets `-c model_auto_compact_token_limit=400000`, others `200000`, with the same declared-capacity clamp. Launcher `model_context_window` arguments and config declarations also cap the target; conservatively use the smallest declaration across profiles. Do not raise Codex's `model_context_window`. `thread/compact/start` exists; idle compaction is still not integrated.
- **Rolling upgrades and activation**: older nodes ignore `is_main`; older hubs omit it and new nodes use ordinary limits. Update the hub's assistant manager **and the resident node's personad binary**. The new limits apply on the next assistant-process start; an idle existing process is resumed with the new policy on its next marked message, while active background tasks are not killed. A hub-only deploy or a download-channel publication without installing the node update is not enough. Publishing and deployment remain separate, approval-gated operations.

### Impossible gateway usage (2026-11-06, #3300)

Claude Code estimates context from the last reply's `usage`. Some gateways report zero for every reply in a turn, then total all request inputs in the last reply (85k-token chat reported 1.7M):
before the next message, Claude Code thinks the window is exceeded and auto-compacts—successful compaction needlessly loses context; a failed summary returns "Prompt is too long · automatic compaction failed", repeated forever on every message.

- **Impossible usage** (`transcript.PlausibleContext`): request input over 1M (largest context window), or over transcript bytes by more than 200k (at least roughly one byte per token, with 200k allowance for system prompt, tools, memory).
  Exclude such values from context usage (`ContextTokens`) and node `p.context`, so they don't trigger idle compaction.
- **Repair** (`node/assistant_usage.go`): after each claude turn `result`, inspect transcript (whole file on the process's first check, then from the previous position), replacing impossible `input_tokens` / `cache_*_input_tokens` in-place with 0
  (pad with spaces, preserving size, inode, reader offsets; subagent rows untouched), first saving `<transcript>.usage-backup-<time>` beside it. Values remain in process memory, so exit after repair (don't exit with background tasks; still repair for next startup).
- **Retry**: if the turn was "Prompt is too long", continue after repair with `assistant/usage_retry` (only a `<persona-context>` block, no App bubble); at most once per chat per 10 minutes. Don't retry without impossible usage (genuinely too long, retain error).
- Measurement (Claude Code 2.1.318, transcript copy + fake API): unchanged resume attempted compaction (`pre_tokens` 1.7M), empty summary returned "Prompt is too long"; zeroed usage skipped compaction and sent directly.
  Previously undelivered user messages remain in transcript; Claude Code combines them with the new message into one user turn (synthetic error replies excluded), so retry needn't include their text again.

### Manual compaction (2026-11-04, #1727)

User selects "Compact context" in main-chat menu: `POST /api/v1/assistant/conversations/{id}/compact` → node `assistant.compact` (`node/assistant_compact.go`).

- Same path as idle compaction: write `/compact` into an existing process, or start with `--resume` (chat's previous launcher / model); Codex uses `thread/compact/start`; runtime chats unsupported.
  Answering or already compacting returns 409, not queued: users can't see when a `/compact` queued behind a turn begins.
- Progress lives in node session state (hub Postgres), sent with session lists as `compaction`: `running` → `done` on `compact_boundary` (Codex `thread/compacted` / `contextCompaction`);
  if turn `result` arrives first without a boundary, `failed` with Claude Code's original error (e.g. "Not enough messages to compact" for short chats);
  10 minutes without result, process exit, or node restart means `failed` (`reapAssistants` checks each minute). Automatic compaction triggered within a turn doesn't change it.
- Not a chat turn: no Working state, push, or unarchive. Transcript `/compact` and boundary remain "Compact context" / "Context compacted" notices;
  clients draw one divider per compaction: "Content above compacted · Manual · Nk tokens before compaction".

### New main chat (2026-11-04, #1727)

A long-used main chat accumulates large context; users can replace it: `POST /api/v1/assistant/main/new` (assistant route, `assistant/start.go`).

1. Reject if the old main chat is answering or compacting (409 `busy`); App supplies its observed main-chat id (`expect`), rejecting if another device already changed it (409 `main_changed`) rather than replacing the new one again.
2. Carry background (`carry: summary`, default): node `assistant.handoff` extracts the latest compaction summary from the old transcript (Claude `isCompactSummary`, Codex `compacted`, up to 12k characters)
   plus subsequent text messages (up to 12, 6000 characters, `<persona-context>` removed), stored in settings `assistant.main_handoff`.
3. Clear `assistant.main`, add old id to `assistant.former_main` (up to 10, in profile `keep`, so hub ensures node always lists them),
   rename "Old main chat · Through M/D" (user-assigned, not overridden by autonaming), archive—all in one assistant operation.
4. The next main-chat message creates a new session (App `POST /api/v1/assistant/conversations` with `main:true`, Lark bot / plugin main-chat sends created by assistant with `main`, all via `startMain`),
   with `assistant/main_handoff.md` prepending background as `<persona-context kind="context" title="接上旧主对话">` (`title` means "Continue old main chat"; App context tag), then clearing it;
   `carry: none` still includes the old main-chat id, allowing `conversation_read`.
   Android first creates an empty `prompt`, then sends separately: bind background to the new session id (`for`) and include it with the first `POST …/messages` or Lark / channel message, all through `assistant.send_params`, not as a separate turn.

Why:

- **Don't create immediately**: without a user's first message, we'd have to invent one, making the first message machine-authored. Deferring matches the existing "no main chat yet" flow all clients support.
- **Carry compaction summary, not knowledge base**: people, groups, projects, memory, live snapshots already exist for every assistant chat (CLAUDE.md, tools, message snapshots); carrying them duplicates content.
  What's lost is chat-specific conclusions and ongoing work, exactly the compaction summary. Without compaction, carry only recent messages; confirmation suggests compacting first.
- **Don't delete old chats**: archive only removes from lists; sending again automatically restores them (`unarchiveOnSend`).

iOS / macOS (#1762): avatar top-bar menu (same Mac menu, also sidebar main-chat context menu) adds "Compact context" / "New main chat…", shown only with hub features `assistant_compact` / `assistant_main_new`.
Status above input (`MainConversationActionsView.swift`): "Compacting context…", `done` shows "Context compacted" for 4 seconds, `failed` shows dismissible "Compaction failed: …";
show `done` / `failed` only if this page observed `running` transition or completion is recent (within 1 / 10 minutes), never ancient results on opening (`MainConversationActions.shown`, same as web).
New-chat confirmation is a form sheet, "Carry the old chat's summary" on by default; success returns to empty main-chat page, and the first message starts it via the no-main-chat flow. Divider wording matches web / Android.

## Scheduled tasks (unified into one table 2026-10-31)

Previously two systems: routines (daily time) and follow-ups (one-shot wakeups from `schedule_followup`), in separate settings;
App "Scheduled tasks" listed only routine switches. For "check every 30 minutes", the assistant scheduled the next follow-up at each turn's end:
removed from the list when the turn began, so an hour blocked on unanswered Bash approval left the page empty, impossible to inspect / edit / stop.

Now one `Task` table (`assistant/schedule.go`, settings `assistant.tasks`), three `repeat` types. Model input retains its runtime role but carries persisted message-source metadata: `once` is `source.kind="followup"`, `daily` / `interval` are `source.kind="patrol"`. The same source accompanies an existing-chat run, reuse of a task's own chat, and a new-chat fallback. Clients retain the legacy `injected` detail but exclude these messages from user bubbles and user-only navigation ([protocol.md](../protocol/protocol.md), "Message source").

- `once`: one-shot follow-up, disabled after running and retained one day (hidden in App) to link "schedule the next one", then removed;
- `daily`: daily / selected weekday time; over 30 minutes late skips with a record;
- `interval`: every N minutes (5 minutes–7 days), `at` is next run, advanced on original cadence; missed during downtime runs once on return, then counts from now.

`until` is optional on a repeating task. It is stored and compared in UTC and shown in the user's time zone. One-off tasks ignore it, and saving a one-off clears it. Omitting `until` on update keeps the stored time; `null` clears it. Once that time is reached the task is turned off and a skipped run is recorded instead of running.

**Wait for main-chat setup** (#4253, `assistant/schedule_ready.go`, design `docs/assistant/onboarding.md` 4.4): due runs check readiness without probing / tokens; if a required step (computer, Claude Code, model) is blocked, skip with "Computer or model not connected yet; skipped", no push / retry. Check only what's needed: follow-ups to another computer don't await assistant computer; tasks using another launcher don't await main-chat Claude / model. "Run now" isn't blocked (user must see actual errors), nor one-shot tasks (reminders, assistant follow-ups): they disable on due, so skipping loses them; send normally, push failures, retry once. If a user-action setup step (no computer, not logged in, invalid key…) causes a skip, store `routines_waiting` in `assistant.readiness` (not sleeping / offline computers; wakeup isn't news), clear before the first full-ready run (`mode: full`), and add a main-chat activity `notice`. Default morning briefing stays enabled.

Readiness skips carry the optional persisted `not_ready` flag (#1857). The failure-streak counter ignores them, including temporary sleep / offline skips: they do not increase or reset the streak, and cannot trigger its notification. Other failures and skips still count as before. Existing history without the flag is not reclassified from its localized error text; the ready-again notice above is unchanged.

**Failure and retry**: node accepts `assistant.send` before model output, so runs ending in API errors (quota, removed model, upstream 503) formerly counted successful.
`watchScheduledRun` now follows the turn, reads final text, records API errors as failed, pushes "Scheduled task failed: …", and retries after 3 minutes once
(`Retried`, no further push on repeat failure). Retry uses the task **then** in the table, not the startup copy (before #2313, users followed failure push and disabled / deleted it,
yet it silently reran the old prompt 3 minutes later): deleted tasks don't retry, disabled repeating tasks don't retry, once follow-ups disabled on due still retry, edited prompts use new text.

### Push after running (#2396)

Every run completes a turn in a chat; hub pushes "Finished" for every turn (`notifyFinished` in `assistant/fleet_push.go`). Tasks like morning briefings always have results to read, so that's fine;
but watchers like "Tell me when new mail arrives" also push "New mail · No new mail" when nothing happens. Prompt edits can't fix it: hub pushes "Done" even if the assistant says nothing.

Each task therefore has `push`:

- `""` (every run, default): push each run's answer as before.
- `"asked"` (only when it explicitly notifies): only `notify_user` pushes reach the phone; no news means no push. Run messages explain this (`task_run.md`).
  Errors (`TurnError`), model-truncated answers, blockers (⏳), "Scheduled task failed: …", and user-answer cards still push normally.

**Implemented per chat, not per turn.** `asked` tasks always use their own chat (`ownConversationIfQuiet`: selecting `asked` clears `session_id`,
including follow-ups no longer entering their originating chat, such as main chat). Snapshot tasks include `push` (`TaskView.Push`); if hub recognizes an `asked` task's own chat
(`run_session_id`) and a normal completion (`assistantwire.NormalFinish`, based on pushed text: final work explanation or last answer), suppress "Finished"
but still finish through `settle`. Channel forwarding (Lark, plugin sync and task pushes) skips under the same `quietTaskTurn` rule.

Cost: user-authored messages in that chat also don't push on completion. People generally don't chat in task-owned chats; if they do, they're likely viewing the App.

Boundaries (`assistantwire.QuietTaskFinish`, shared by hub / channel forwarding):

- **Only Claude Code and headless Codex chats** (session `agent` empty or `codex`): must distinguish "nothing happened" from failure. Claude errors appear in final text,
  recognized by hub (truncation); headless Codex failures report session `turn_error` from node (#2571, enabled since #2932). Runtime failures (`agent` is runtime id) aren't in session state,
  so runtime asked tasks still push "Finished" rather than swallowing failures as no news.
- **Main chat is never quiet**: if a user makes a task chat main chat, normal pushes resume and the next task run creates another chat.
  If main-chat identity is uncertain (assistant settings read failure, hub hasn't received profile), treat it as possibly main: push / forward normally and don't reuse it for the task.
- **Other tasks don't enter quiet chats**: `schedule_followup` from an asked-task chat runs in its own chat; existing tasks targeting it also move to their own on running (they require every-run push).
- Decide after `settle`, not as "already pushed": background-pending turns remain suspended, their wakeup turn is judged separately, errors push normally.
- Run-message explanations ("It's time again…", "This task only notifies when you use notify_user…") are stripped when the assistant renews with the whole message (`stripScheduleHeader`),
  preserving task identity, `push`, and prompt.
- On hub restart before snapshots, or the first run before a new chat id is reported, hub doesn't know chat ownership and pushes normally.
  After receiving a snapshot, assistant disconnection still uses the last copy.

Why not suppress only "this run's turn": three per-turn designs were reviewed (input `quiet`, node tracking whether all inputs were quiet and completion successful,
reported to hub); each had races swallowing user pushes: Claude processes queued messages after background turns; compaction completion reuses prior results;
failed Codex steer becoming start is claimed by another input's `turn/started`; old push callbacks read new-turn markers if hub misses intermediate Working.
Correctness requires correlating every input / completion id in core node message paths. Per-chat decisions avoid those races and have a fixed answer.

App `PUT /api/v1/assistant/tasks/{id}` omitting `push` or `facts` preserves it (old Apps); old `PUT /api/v1/assistant/routines` always preserves both.
`null`, non-string, unknown values return 400. `schedule_routine` / `schedule_followup` accept optional `push` (`every` / `asked`); omission defaults new tasks to every run,
preserves existing values. `schedule_routine` with `id` / `push` can change only this field on any task. Web, iOS / Mac, Android editors include the switch (#2570, #2676: shown with hub `task_push` or an already-asked task; "Only when it notifies" hides "Continue original chat", list marks "Push only on notification"; tasks without prior `push` omit it on save for old hubs).

Each task can separately set **launcher (model), permission mode, effort** (empty = assistant default, unchanged), editable only in the App, not assistant tools
(no self-escalation). `bypassPermissions` requires startup `--permission-mode` (control requests can't switch): node doesn't use a spare on new startup,
and restarts existing processes in another mode (`node/assistant.go`). Persona's mode-menu "Allow Everything" works likewise (#2416):
stop idle processes immediately, resume next message; busy processes wait until the turn ends. App mode persists in hub session state over old transcript values. "Allow Everything" first confirms, then marks the picker red with consequences.
Tasks continuing original chats (`session_id`) also switch those chats to these settings.

**Same task, same chat** (requested 2026-11-01: "Run the same scheduled task in the same session"): tasks without `session_id`
(App morning briefing / checks, `schedule_routine`) formerly created new chats each time, unable to compare "with yesterday" or "is last time's item still there?". Now:

- First run creates a chat and stores `run_machine_id` / `run_session_id` / `run_launcher` with task settings, surviving hub restart;
  later runs `assistant.send` into it. node keeps these listed like main chat (`keptSessions`), even weekly tasks, so they open and busy state is knowable.
- Continued messages say "⏰ Title (scheduled task)", lateness, and "This is still this task's own chat; previous results are above; explain changes from last time",
  then the task prompt (possibly edited / compacted, included every time). Knowledge instructions and "No need to schedule the next run" appear only initially (`Continued` in `task_run.md`).
- Failed resume creates and records a new task chat: missing chat (Claude's 30-day transcript cleanup, never written: hub `resume_only`; if neither process nor transcript exists, node errors rather than silently creating blank history under the old id),
  offline machine, send failure, task / assistant launcher type change (claude ↔ codex can't resume), assistant machine change. Log the reason.
- If due again while the previous run is running or awaiting approval / answer, **skip**, recording "Previous run still running; skipped", not queuing in the same chat
  (would answer twice, the second seeing half-finished work). One-shot follow-ups still send (skip loses them); App "Run now" returns 409 with reason when busy.
  Only one simultaneous send per task (`schedule.sending`), preventing due / Run now overlap.
- Chat-scheduled tasks (`session_id`) still send to their originating chat first; failures fall back to a task-owned chat, recorded and reused.
- App Scheduled tasks "Chat" section says "Runs in the same chat", opening it in Persona in both layouts / shells. "Start a new chat next time"
  (`POST /api/v1/assistant/tasks/{id}/fresh`) clears the stored chat for large context / restart; old chat remains in Chats. Never-run tasks say "A new chat starts on the first run".

Deployment: update hub and node together (`resume_only` is new; old node ignores it and creates a new chat under a missing old id).

**A turn schedules its successor** ("Finally use schedule_followup with at=30m and the same prompt"): hub recognizes the task from run history;
same title or prompt doesn't create another. One-shot relative-time (`30m`) renewal becomes a 30-minute `interval`; later identical requests update the prompt without changing time,
and don't re-enable tasks disabled in the App. Copied leading "⏰ … (your scheduled follow-up)" lines are removed to prevent growth.
`interval` messages say "It will run again on schedule; no need to arrange the next run", and assistant instructions recommend `every`.

Migration: first startup without `assistant.tasks` moves `assistant.routines` / `assistant.followups` (ids, times, prior statuses unchanged),
then clears old follow-ups so an older hub can't rerun them; thereafter reads only the new table. Failed Postgres reads save nothing (formerly defaults overwrote).
Legacy App `routines` / `followups` APIs still read / write this table (`docs/protocol/protocol.md`).

## Chat titles (2026-11-01)

Requested: "Generate titles after starting new chats; standardize as ‘MMDD | Type | Topic’." Implemented as follows:

- Title = `MMDD | Type | Topic`. MMDD is the chat's start date in **user timezone** (`Hub.userLocation()`; hub runs UTC, but a 00:30 start uses the user's local date).
  Types: Feature / Design / Fix / Optimization / Release / Exploration / Docs / Research / Review / Scheduled; topic ≤ 14 characters, no project / repo names. Example: `1101 | 修复 | 搜索框和顶栏重叠` ("1101 | Fix | Search and top bar overlap").
- **When generated**: new hub chats (App `POST /api/v1/assistant/conversations`) are recorded "pending title". After the first turn (working → done / idle),
  the assistant uses the "Chat title" purpose model in the background (`assistant.title`, Model Configuration › Information processing), reading first message / reply to produce "Type | Topic". No interruption, not delegated to the assistant chat itself, replies don't wait.
  Budget (#1532): 45 s per call, 2 minutes per naming, `max_tokens` 1024. The 2–3 s first measured no longer holds: with a thinking model behind the gateway a title-sized call measured p50 6 s, p90 21.6 s, and its thinking spends the same `max_tokens` (at 200, replies stopped after 188–192 thinking and 4–8 output tokens: half a topic that still parsed). The old 15 s cut-off left about a fifth of new chats on the fallback title.
  Scheduled-task chats immediately get `MMDD | Scheduled | Task name`, no model call, retained as the task reuses its chat (previous section).
  AI twin chats immediately get `MMDD | Inbox | Item title` (#3264; formerly untitled, sidebar rows began "This is an Inbox item; you…"); Inbox is hub-only, not a model-selectable type.
- **Validation** (`parseTitle`): take first line, tolerate quotes, fullwidth pipes, redundant model dates; invalid type / long topic retries once with "Previous result didn't match format";
  second valid type with long topic truncates to 14 characters; otherwise fallback `MMDD | Exploration | First 14 characters of first message` (tags / Markdown stripped). Same fallback without model config.
  Each way an answer goes wrong gets its own one more call within the naming's time: no answer (timed out, connection failed, gateway 5xx: `ai.Transient`), a reply stopped at `max_tokens` (half a title even when it parses: twice the room; `ai.Reply.Truncated`) and the broken format above.
  When the naming ends for want of an answer (or the first exchange couldn't be read) the fallback is only a stand-in (`tries` in the record): the next turn's end names the chat again from its first turn (`after_seq: 0`, not the newest), three namings at most; a user or assistant rename meanwhile stays. A model that isn't set up or turns the request down (4xx, refusal) answers the same next time: that fallback is final.
- **Storage**: assistant store (hub settings `assistant.titles`: session id → title, author, start time, `tries` for a stand-in), snapshot to hub, replaces App session-list `title` (`decorated`).
  No transcript writes; same for Claude / Codex, no node changes.
- **Once only, user wins**: App Rename (drawer long-press chat › Rename…, `POST /api/v1/sessions/{mid}/{sid}/rename`) records user-authored titles, never overwritten;
  even before the first turn ends. Codex renames are stored only in hub (node would write Claude custom-title lines into Codex rollout).
  A Claude rename goes to the node first and is stored once it succeeds. Before Claude has written the transcript (the Feishu /agent renames its conversation
  right after starting it), the node keeps the title and writes the custom-title line when the transcript appears (#5866).
- No backfill for old chats lacking pending-title records, except old AI twin chats gain `MMDD | Inbox | …` while backfilling origins ("Chat origins" below).
- **Main chat has no title** (#1313): App session-list `title` is always "Persona" (`mainTitle`, settings `assistant.main`),
  not node's first-message title (headless transcripts lack Claude ai-title), no `MMDD | Type | Topic` (discard pending-title on first-turn completion without model call),
  App rename returns 400 "Main chat has no title to rename" (`conversation_rename` already rejects). Top bars show the avatar: iOS / Mac / Android avatar name (default "Persona"), web "Persona".
- App drawer rows draw `MMDD | Type | ` in secondary color, topic primary, truncating topic end in narrow space (`ConversationTitle`); other titles unchanged.

## Guest conversations (2026-11-06, #5195)

A conversation an extension opens to answer **someone else** for the user (for example a Lark extension's opt-in auto replies to @-mentions). Someone else's message goes into it and its answer goes out as the user, so it must have none of the assistant's powers.
An ordinary extension conversation is an assistant conversation (the assistant directory, its CLAUDE.md and memory, every `mcp__persona` tool allowed up front, `agent_start` / `agent_send` asking the phone only in the health space), where only the prompt stands between the message and those tools; hence this kind.

- **Starting one**: `POST /api/v1/assistant/conversations` with `guest: {"tools": ["lark_agent_reply"]}`. `tools` are 1–8 **connector** tools (an extension's; the assistant's own, the phone's, Cua's and ego's are refused with 400). It can't be combined with `main` or `space`, and `report_tasks` is ignored (it hands nothing out).
  Conversations without `guest` are exactly as before. An extension first asks `GET /api/v1/assistant/capabilities` (`{"guest_conversations": true}`): an older hub would ignore `guest` and start an ordinary conversation, so on 404 the extension starts none.
- **Directory**: the hub has the node's `assistant.guest_prepare` write `.persona/guest` with only a guest CLAUDE.md / AGENTS.md (`prompts/assistant/guest_claude_md.md`), leaving the assistant's connectors, CLIs and skills alone. A node that doesn't know the method (too old) gets a 400 and nothing starts.
- **Node** (`node/assistant_guest.go`): Claude Code CLI only (Codex, the Agent SDK and agent runtimes such as DSH are refused: their built-in tools can't be taken away), never the warm spare process.
  Every process it runs in (start, resume, compaction, model switch) has `--tools ""` (no built-in tools), `--strict-mcp-config` (only Persona's own MCP server, none of the user's or the extensions'), and `--allowedTools` naming only its tools.
  The guest flag and tools live in its session state (`sessionState.Guest`) and follow a fork; a later message naming another launcher or runtime is ignored and it runs on what it started on. It gets no live snapshot and no gate proxy addresses. The node relays its MCP calls with `guest: true`.
- **Hub** (`assistant/guest.go`, setting `assistant.guests`, kept 30 days): `mcp.tools` lists and `mcp.call` runs only its tools; `assistant.context` gives it an empty snapshot; `SendParams` drops a launcher, model, runtime or permission mode picked for a message.
  While the record can't be read, every tool call is refused (the user's own conversations keep their snapshot and picks: the node enforces those for a guest itself), and a conversation the node calls a guest but the hub doesn't know gets no tools.

## Chat origins (#3264, 2026-11-06)

Feedback: many AI twin chats flooded sidebar "Recent", hiding user chats; automatically created chats need origin labels and default collapse, while remaining openable.
Each assistant chat records origin (`assistant/origins.go`, settings `assistant.origins`), snapshot to hub session `origin` / `origin_ref`; values, backfill, client-sidebar rules: `docs/protocol/protocol.md` ("Assistant chat origins").

- **Recording**: every creation path uses `start()`, with caller origin: App `POST /api/v1/assistant/conversations` is `user` (extension tokens `extension`, `lark.agent` model `lark_agent`;
  extension identity from hub-forwarded `X-Persona-Extension`, discarding client headers), channel `/new` `channel`, AI twin `inbox`, scheduled task `task`, `conversation_new` `spawned`; main chat always `user`.
- **Backfill**: each machine list (`SessionsChanged`, node's raw list with first-message titles) triggers background backfill for assistant chats lacking origin. Evidence order: AI twin session recorded in Inbox items,
  task-owned chat (`run_session_id`, runs not sent to another chat), hub-assigned title (Scheduled / Inbox), first-message prefix (Inbox, `⏰` task); if title insufficient but hub-created (pending / automatic title),
  read first message to recognize forwarded-chat and `/agent` `<persona-context>` (node titles strip these blocks). Otherwise `user`. Creation-time records always override inferred backfill, never changed by it.
- **Rerun**: `POST /api/v1/assistant/origins/backfill` immediately processes the assistant machine list; `redo` reevaluates inferred records, but never changes a recognized origin back to `user` (hub-decorated list titles are no longer first messages).

## Managing its own chats (2026-11-01)

Requested: "Can the Persona assistant manage its own chat history—rename title, delete (archive), new, send messages to sessions / threads?"
Assistant MCP tools (`assistant/conversations.go`, descriptions `prompts/mcp/assistant/conversation*.md`), identical for Claude / Codex:

| Tool | Purpose |
| --- | --- |
| `conversations_list` | Sidebar chats: id, title, last activity, main / current / running / Codex flags, last text; `query` filters title / last text; archived excluded by default (`include_archived`) |
| `conversation_read` | Read another chat's latest N turns (default 2, max 10), same `agent_read` truncation (`transcriptText`: one line per tool call, only failed results, at most the final 40000 bytes, starting at a UTF-8 rune boundary; the earlier-part-cut marker is added separately) |
| `conversation_rename` | Set title with `kind` + `topic`; hub formats `MMDD \| Type \| Topic` (start date, previous section). User titles stay unless explicitly requested (`user_asked`, then user-owned); assistant titles marked `assistant`, no autonaming override, App user renames still win. Main chat has no title to rename |
| `conversation_archive` / `conversation_unarchive` | Archive / unarchive, below |
| `conversation_new` | Start a chat with an opening message, return id immediately without waiting; normal auto-title after first turn, excluding opening source context |
| `conversation_send` | Send to another chat, not itself |

- **Archive only hides**: hub settings `assistant.archived` (session id → machine, time, actor), session list `archived: true`;
  `GET /api/v1/assistant` excludes by default, includes with `?archived=1`. No transcript deletion / hard delete. Main chat can't archive (API 400, tool error); replace through "New main chat" ("Compaction" above).
  - **Archive itself**: allowed ("We're done, archive this" in a side chat is natural). Finish the turn normally; an open App chat isn't closed, only hidden from lists;
    new user messages restore it (`unarchiveOnSend` in `sendMessage`), as does another chat's `conversation_send`.
  - node lists only recent sessions: old archived chats can disappear from Archived (as they do from normal lists), but archive records remain and `conversation_unarchive` can retrieve by id.
- **Inter-chat messages** (`conversation_send`, `conversation_new` opening):
  - Prepend `<persona-context kind="context" title="来自对话「…」">` (`title`: "From chat ‘…’", `prompts/assistant/relay.md`), explaining it's not user-authored, source chat, hop count;
    if a reply is needed, use `conversation_send`; do not reply merely to acknowledge receipt. `Item.source.kind="conversation"` retains the stable sender id and title, separately from the runtime role and native peer `from`. Clients show a source message rather than a user bubble; the relay's instructions remain inspectable as Context. Explicit source metadata is persisted in a separate `message-source` context, not a selectable Context card. Full canonical legacy relay contexts remain recognizable; user-pasted templates are protected at the entry point.
  - **Queue while running**: if target is running or already queued, persist in hub settings `assistant.relay` (survives restart), displayed above the input like typed queues
    (session `queued`). After its turn ends (no longer working), hub sends one at a time. Don't write into running processes: Codex merges through `turn/steer`, not queueing.
  - **Loop prevention**: each chat tracks the current message's hops from the user's input (user 0, relay +1, expires after 1 hour); don't send beyond 3 hops.
    Per chat, at most 3 sends to the same target and 10 total within 10 minutes (memory only, reset on hub restart). User messages reset hops to 0.
  - Sending to archived chats restores them.
- **App**: swipe left to Archive in sidebar (not main chat; also long-press). Sidebar changes `ScrollView` + `LazyVStack` to `List`, removing insets, separators, backgrounds
  with unchanged appearance, enabling system swipes. With drawer open, a left drag starting on a **swipeable chat row** goes to the row (`SwipeRowMark`; `DrawerPan` makes row gestures wait for it to fail);
  other left drags still close the drawer. Bottom "Archived (N)" if nonzero, also in Recent ⋯; opens `ArchivedConversationsView`, tap to open,
  swipe / long-press Unarchive. Mac shares code (two-finger trackpad swipe).
- Calls remain tool cards with localized Chinese source names (`ToolPresentation`: New chat, Send to chat, Rename chat, Archive chat…).
- Measurement (2026-11-01, isolated test hub on hub machine: fake HOME, separate port / database, copied launcher pointing to fake Claude config): assistant performed new → list → rename → send → archive → list including archived → unarchive → read, all 8 succeeded. Both new-chat messages included source and hop 1; it answered only "OK", no relay reply. First-turn untitled chats were formerly "assistant" in lists / source context (directory-name title), now "New chat".

## Codex (2026-11-01, `codex app-server`)

Selecting a Codex launcher in Settings › Assistant (or top-bar menu / scheduled tasks) replaces `claude -p` with Codex's persistent IDE JSON-RPC service,
`codex app-server` (stdio, one JSON line per message; protocol from `codex app-server generate-ts [--experimental]`, codex-cli 0.159.3 on hub machine).
Code: `server/internal/node/assistant_codex.go`; `assistant_agent.go` chooses Claude / Codex from launcher-probed agent (`launcherProbeCached`). Assistant-capable runtime launchers go to that runtime (next section); other types (internal Codex variants…) reject with explanation.

**Startup**: node runs the launcher function in interactive zsh, replacing `codex` with `codex <launcher arguments> app-server <Persona -c>` (`codexShell`,
including `command codex …`), retaining environment, `-c model_provider=…`, `-m`. One app-server per chat, one thread inside;
thread id is session id (assigned by Codex, known only after new-chat `thread/start`), rollout is history (`docs/integrations/codex.md`), App displays normally.
Handshake: `initialize` (experimentalApi) → `skills/extraRoots/set` → `thread/start` / `thread/resume`.
Snapshots accompany each message as Claude does (first `turn/start` / `turn/steer` input); `developerInstructions` holds only memory.

| Capability | Claude (`claude -p`) | Codex (`codex app-server`) |
| --- | --- | --- |
| Streaming | `stream_event` text_delta | `item/agentMessage/delta` → same `session.delta` |
| Continue / after restart | `--resume <id>` | Reuse live process, otherwise start a new one with `thread/resume` (verified after hub / node restart) |
| Stop | Kill process, next message `--resume` | `turn/interrupt`, retain process, then `thread/backgroundTerminals/clean`; interrupt alone doesn't stop commands (measured `sleep 120` remained backgrounded) |
| Interject during reply | Queued message absorbed mid-turn | `turn/steer` into current turn (same effect, verified within its reply); start a turn if it just ended |
| Persona MCP | `--mcp-config` + `--allowedTools mcp__persona` | `-c mcp_servers.persona.*`; Codex asks via `mcpServer/elicitation/request` (`codex_approval_kind: mcp_tool_call`); node always allows compatibility server `persona`. `tool_timeout_sec` one day (default 60 seconds times out ask_user phone waits) |
| ask_user | Blocking MCP | Same tool. MCP starts before thread id exists; environment carries `proc-…` key, replaced by node with session id on call (`resolveAssistantKey`) |
| Native request_user_input | — | `item/tool/requestUserInput` → phone question card, answers keyed by question id |
| Command / file permissions | PermissionRequest hook → phone | `item/commandExecution/requestApproval` / `item/fileChange/requestApproval` / `item/permissions/requestApproval` → same phone `ask` |
| Lark write confirmation | Bash PreToolUse (`larkgate.go`) | When command approvals are enabled, `larkCheck` first strips `/bin/zsh -lc '…'`: reads allowed, writes show Lark card, token access denied. Full access skips command approval callbacks; connector proxies and gated CLI wrappers still enforce business confirmations. Codex decline has no reason; phone feedback / gate explanation reaches model through `turn/steer`, shown as "(Not executed)…" |
| Permission mode | `--permission-mode` / control requests | Each `turn/start` uses `codexPolicy`: Default / Accept edits = `untrusted` + workspace-write with network; Auto = `on-request` + workspace-write without network, escalation for sandbox escape / network passes gate; Plan = `untrusted` + read-only; Allow Everything = `never` + danger-full-access. The same mapping applies to `thread/start` and `thread/resume`. Switches apply at the next `turn/start`; an active turn keeps its existing policy. Persisted per-session choices win over older rollout settings, including historical `untrusted` + danger-full-access turns. No machine defaults or other session settings are rewritten. Managed policy restrictions can still reject native settings and are returned as errors |
| Effort | `--effort` / control | `turn/start` `effort`, low…max unchanged, gateway support depends on model |
| Images | base64 image blocks | Files in `~/.persona/uploads/<date>/`, `localImage`; rollout path becomes App thumbnail |
| Instructions | `CLAUDE.md` | `AGENTS.md` (both written identically by `assistant.prepare`); also user `~/.codex/AGENTS.md` |
| Extension skills | `.claude/skills/<name>` | Same directory as extra skill root (same format, visible in `skills/list`); gated commands on PATH, credentials in node proxy (previous environment tokens required `shell_environment_policy.ignore_default_excludes=true`, or Codex stripped `*TOKEN*` from command environments) |
| Memory | Assistant-specific Claude auto memory | **Same files** (chosen 2026-11-01): `developerInstructions` gives directory, one-file-per-entry + `MEMORY.md` format, current index (`codexMemoryPrompt`); directory added to sandbox `writableRoots`. apply_patch and memory-only reads (cat / head / rg…, `readsOnlyMemory`) don't ask, but Plan writes do. Disable Codex's machine-wide rollout-derived `~/.codex/memories` for assistant (`features.memories=false`) |
| Background / Monitor | `background_tasks_changed`, claude wakes on completion | Read `thread/backgroundTerminals/list` after every turn, record tasks and retain process, report lost tasks after restart; **completion doesn't wake chat**, no Monitor |
| Prewarmed spare | Yes | Yes, pre-start thread reused (measured 50 ms return) |

User-visible differences:

- **More prompts in Default**: Codex `untrusted` allows only its own read-only command list; even `timeout 20 head …` asks on the phone (Claude file reads don't). Use Auto if too many.
- Background completion doesn't wake chat; use `schedule_followup` to revisit.
- Denial reasons appear as "(Not executed)…" messages (native Codex denial carries no text).
- User `~/.codex/config.toml` MCP servers, `hooks.json`, `AGENTS.md` load too, like Claude user settings; other MCP calls need phone approval outside Allow Everything.

Measurement (2026-11-01, isolated hub: fake HOME, separate port / database, real Codex launcher and Lark gateway): new chat (snapshot, `agents_list` no approval), resume,
ask_user answer, `ol GET /me` allowed, `ol POST …/messages` Lark card denied (model stopped without retry, no send),
Allow Everything commands without prompts, same-turn interjection, immediate interrupt stopping `sleep`, context retained after hub + node restart, effort / mode restored, streaming around 120 ms per segment,
image color recognition, prewarmed spare. Memory follow-up that day: preset "preferred tea" read in Default without a card;
"peanut allergy" created a file / updated index, memory API listed it, new chat answered from index without file read, no writes to `~/.codex/memories`. Unit tests: `assistant_codex_test.go`, fake app-server replaying actual frames for handshake, turn/start / steer / interrupt,
approvals / gate, request_user_input.

## Agent runtime (DSH) (2026-11-02, issue #282)

Selecting a launcher declaring assistant capability (currently DSH plugin `dsh`, `launchers.list` `assistant: true`) runs chats in that runtime:
node `server/internal/node/assistant_runtime.go` drives `session.start` / `session.send` / `session.interrupt`,
with live snapshot (`context`), permission mode (`mode`), memory directory per message. Content, streaming, permissions, and questions use existing runtime paths (`docs/integrations/runtimes.md` ("Assistant")).
This layer contains no runtime-specific code; any runtime declaring `caps.assistant` and implementing the contract can serve as assistant.

Same as Claude / Codex: live snapshots, `ask_user` / Persona tools (assistant chats only), all permissions / questions go to phone first, permission modes, shared memory
(first message includes directory, format, index via `prompts/assistant/runtime_instructions.md`), assistant instruction files / skills (`.agents/skills/`),
AI twin / Lark `/agent` / scheduled tasks (all `assistant.send`).

Differences:

- **Lark**: credentials never enter runtime. Model uses extension-gate declared tools (`proto.CommandGate.Tool`, arguments are command words); node applies `larkCheck`, then
  runs skill scripts locally, credentials only in node proxy. Direct shell commands can't obtain credentials or bypass gates.
- **Model / effort** selected in runtime (DSH Web GUI); model configuration doesn't select a specific model for these launchers. **Prewarming** unnecessary (persistent runtime).
- Closing stops only the current turn, leaving the chat in runtime. Changing between runtime and Claude / Codex requires a new chat; another launcher specified on send still goes to the original runtime (`assistantResumeLauncher`).
- Disconnected runtime (`dsh web` not running) returns `runtime_away` without sending anything.

DSH details (permission preset mapping, persistent `dsh web`): `docs/integrations/dsh.md` ("Assistant"); end-to-end `integrations/dsh/e2e/assistant.mjs`.

## Profile and avatar (2026-10-30)

- Persona tab top-right avatar opens `AssistantProfileView`: identity / machine, Online / Working, three "Up Next" items,
  proactive work (scheduled tasks, run history), knowing you (About Me, memory), settings (Skills, Run on, Working directory; model in Settings › Model Configuration › Main chat, #1291), accessible data. Settings › Assistant opens the same page.
- Scheduled tasks: "Up Next" timeline (large times on the left, like Reminders), "Scheduled tasks" (daily / interval, natural rules "Every weekday at 9:00 AM", "Every 30 minutes"),
  "One-time follow-ups"; rows show icon, next run, nondefault execution ("claude-fast · Auto · Low effort", Allow Everything red), last result, switch; swipe delete, long-press Run now,
  recent runs. Shared editor: repeat (Once / Daily / Interval), prompt, execution (model, permission, effort), Continue original chat (toggle off for new chats each run),
  Run now, history.
- Task-created chats live on Scheduled tasks (#2838, same web #2824 rules, `AssistantTaskChats`): iOS / Mac Recent excludes them (except the open chat,
  which keeps a row; pinned unchanged). Each task shows up to 3 links like "Today 09:03". Identify by `run_session_id` / `last_session_id`
  and `/assistant/runs` `session_id`, excluding task `session_id` (continued originating chats are user-owned). History retains 200 runs; older chats match hub titles
  `MMDD | Scheduled | Task name` (mutual prefixes accepted for truncated names; Scheduled alone isn't enough, since model can classify user chats that way). Reread tasks / runs on sidebar appearance and chat-count changes.
- Avatar is SwiftUI (`AssistantAvatar`), not bitmap. Default since 2026-11-03 is **Mochi** (white waving dumpling in the App icon, icon colors independent of theme; `docs/apple/persona.md` ("Built-in avatars"));
  the alternatives below follow theme accent. Four earlier candidates (`-tab assistant -openAssistantPage avatars` compares):
  **Visor** (default before 2026-11-03, now optional: accent disc + dark mask, two glowing eyes, upper-right spark echoing App icon; working eyes scan side to side),
  **Circuit** (B2 selected from tech-style icon candidates 2026-10-31, made optional not default 11-01: on a dark accent circle, traces from three left pads
  flow into a two-eyed core, representing computer Agents converging in Persona; working signals flow along traces),
  Spark (App-icon spark in a dark circle, too similar to the icon and button-like small), Halo (broken HUD ring + glowing core, cool but cold, details blur at 28pt).
  Not beside messages (full-width assistant replies would lose a column), nor notifications (requires Notification Service Extension + communication notification, separate work).
- Since 2026-10-31 avatar / name can change (Settings › Appearance › Avatar, `docs/apple/persona.md`; avatar colors used only with "Match Avatar" theme). Visor is default, Circuit next and selectable. Both animate by top-bar state:
  idle blinks, thinking looks up with thought bubble, working Visor scans / Circuit signals flow along three traces, Needs you red breathing outer ring, offline gray.

## Android entry points (2026-11-02)

Android management lives in Settings › Assistant (`docs/android/android.md` ("Assistant settings")): Settings "Assistant › Persona", Persona title-menu "Assistant settings", top-right avatar, and drawer "Profile" all open it. Content: Run on (machine, working directory, machine switch; launcher / model in Model Configuration › Main chat, #1291), data location, knowing you (About Me, memory), timezone (phone default), accessible data; proactive work (scheduled tasks, follow-ups, run history) is separate, added here after integration.

Unlike iOS, Android can switch the assistant machine. Warn "Memory and chat history stay on the old computer and won't follow", and send `"main":"-"` in the same `PUT /api/v1/assistant` to clear main chat. hub doesn't change `main` on machine changes; without clearing, the App combines old `main` with the new machine and seeks nonexistent history.

## Verification

Server tests in `docs/operations/testing.md` cover `nextRun` / `parseWhen`, expired-task skipping, ask_user round trips, agents_list, assistant-only tools.
Added 2026-10-30: scheduling across hub restart (`TestScheduleAcrossHubRestart`: no rerun, missed follow-ups run once with lateness, very late routines skipped, history retained),
`schedule_routine`, retaining processes with background tasks, explaining lost tasks after restart; App `AssistantScheduleTests`, UI `AssistantProfileTests`
(avatar → profile, scheduled list / details, Run now for tasks / follow-ups). After 2026-10-31 table unification: `assistant/schedule_test.go` (lossless / nonduplicating migration,
self-renewal becomes one `interval`, interval cadence, permission validation, old APIs), App `AssistantRoutinesModelTests`, UI `testRepeatingTaskAndItsPermissionMode`
(check appears in list, Allow Everything confirmation, hub save).

### Capability measurements (2026-10-30, production hub, early morning while user inactive; test chats start `【Persona 能力测试，可忽略】`—"Persona capability test, safe to ignore")

| Capability | Before fix | Evidence (UTC) |
| --- | --- | --- |
| Request "Remind me in 3 minutes" in chat | Partial: created and ran on time, but both `schedule_followup` and due `notify_user` needed phone approval; blocked without a tap | 18:45:15 schedule_followup → approval wait; due 18:50:05 notify_user → approval wait again |
| Follow-up punctuality | 21 seconds late (20-second polling + process resume) | Due 18:49:31.3, entered chat 18:49:52.6 |
| Request "Do this daily at a time" | No scheduled-task creation tool; one-shot follow-ups had to relay (as with the user's project checks) | `list_followups` said "the user edits these in the app's settings" |
| Scheduled tasks / follow-ups after hub restart | Yes: Postgres storage, 18:48:07 restart still triggered follow-up at 18:49; routines record ran before execution, no duplicates; over 30 minutes late skip (formerly invisible) | journalctl 18:48:07–10 restart; unit skip / no-duplicate tests |
| Monitor | Partial: live-process event woke assistant in 0.3 seconds; Monitor and notify_user needed approval, hub restart silently lost Monitor | 18:47:33.423 ALERT write → 18:47:33.719 assistant receives → 18:47:44.1 notify_user |
| Background (`run_in_background`) | Partial: 2-minute command immediately woke assistant on completion; over-20-minute commands killed with idle reclamation, also on restart, so assistant waited forever | 18:47:14 command ends → 18:47:14.79 assistant receives → 18:47:33 notify_user; 23-minute command killed by 18:48 restart, no further chat activity |
| phone_* while locked | Yes: silent push woke App, result in 2.8 seconds (02:54 local) | 18:54:42.9 approval → 18:54:45.7 result |
| Phone push | Yes, notify_user uses APNs even with App closed; hub formerly didn't record successful pushes, preventing latency measurement, now records "APNs acceptance time" | — |

Most latency is the model (10–19 seconds from event to notify_user); hub / node stays within 1 second.

### Retest after fixes (2026-10-30, after e50f972 deployment, production hub)

| Capability | Result | Event → assistant handles → push (APNs accepted) |
| --- | --- | --- |
| "Remind me in 3 minutes" | Works, no approval throughout | Due 19:39:40.29, ran 19:39:40.30 (0.01 seconds late) → 19:39:47.4 notify_user → APNs 175 ms |
| "Do this daily at HH:MM" | Works (`schedule_routine`) | Due 19:43:00, ran 19:43:00.01 → new chat 19:43:01.96 → 19:43:07.9 notify_user → APNs 313 ms; run recorded |
| Monitor | Works (Monitor itself still needs one permission-mode approval) | 19:37:23.855 write → 19:37:24.167 assistant receives (0.3 seconds) → 19:37:30.9 notify_user → APNs 313 ms |
| 2-minute background task | Works, no approval throughout | 19:38:43 command ends → 19:38:43.85 assistant receives → 19:38:55.5 notify_user → APNs 173 ms |
| 23-minute background task (beyond old 20-minute reclamation) | Works: process stayed unreclaimed through 23 idle minutes | 19:36:51 start → 19:59:51 command ends → 19:59:51.74 assistant receives → 20:00:09.9 notify_user → APNs 233 ms |
| Phone tools while locked | Works, no approval | Silent push APNs 259 ms, phone connects 900 ms later, tool result in 3.2 seconds |
| Explain lost background tasks after hub restart | Unit tested (`TestRecoverAssistantTasksAfterRestart`); production verification awaits a deployment with tasks running (log `background tasks lost in the restart`) | — |

Background / locked App: APNs alerts and silent wakes don't require foreground. Phone stayed locked during tests (after 03:00 local), phone tools returned in 3.2 seconds. hub can't measure when the notification actually appears on the phone.

End-to-end (2026-10-26): new chat answered "Who is waiting for me, what meetings remain today?" within 10 seconds using snapshot + `agents_list` + `lark_api calendar/week`;
ask_user displayed as a native App question card and returned the answer; `schedule_followup 2m` triggered on time and used `notify_user`.

Real-Lark tests of the extension-supplied skill (2026-10-30) are in the extension's own instructions.
