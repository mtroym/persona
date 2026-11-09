# Inbox

The Inbox tab is Persona's information entry point: Lark messages and mail from all email accounts share **one list**, grouped into items. AI triages first; people see only the conclusions.
(Requested direction 2026-10-27: no group-by-group browsing, no second Lark client; see `docs/extensions/feishu.md`.)

## Ownership: the manager owns the Inbox (2026-11-04)

The Inbox belongs to the **manager** (assistant process `personad assistant`; `docs/assistant/assistant.md`, "Boundary with hub"): items and their state (pre-admission triage,
per-source rules and fallback tiers, `triage` status, event grouping, muting, done state, bulk actions), the AI twin (judgment, starting its chat, drafts, message cards), item translation,
and generic App translation (`docs/apple/ios.md`, "Translation"), plus every endpoint under `/api/v1/inbox…`, `/api/v1/translate`, and `/api/v1/translate/mail`
(hub forwards unchanged through `assistantwire.InboxRoutes`, with the same fields as before). Code: `server/internal/assistant/inbox*.go`.
Table `inbox_items` and setting `inbox_mutes` are unchanged; no data migration, and only the manager reads / writes them. All models (triage, judgment, drafting, translation) are called by the manager
through hub's model configuration (`app.call models.*`, centralized on the manager side in `assistant/inbox_hub.go`). Every call has a deadline (`timeout_ms`);
when the manager stops waiting, hub stops too, leaving no queued request. Inbox and translation no longer call models inside hub.

**Sources also belong to the manager** (2026-11-05, moved with connectors; `server/internal/extmgr/inbox.go`, `docs/extensions/extensions.md`, "Ownership"). Source-related mentions of "hub" below now live in the manager, in the same process as the Inbox, without bus handoff. Connectors and plugins collect through `inbox.collect` on a schedule
(every 2 minutes, connector `changed`, Lark events, mail every 3 minutes) and deliver raw collected data to the manager (bus call `inbox.collected`, once per round: each source's items and capabilities,
each mail account's latest page, reading at most 4 accounts concurrently and delivering in account order, skipping failed mail reads. Failed connector reads still appear in the round with empty `things` and a short `read_error` code (see "Triage status" below); the manager retains their existing items. Each round carries the hub run's `epoch` and round `seq`; delayed rounds older than one already used are discarded). Source operations requested by the manager use
`app.call inbox.*` through hub: read a mail's full body (translation needs text preserving paragraphs and links), send replies (mail, connectors, or the Lark plugin),
`settle`, event grouping's `group.tool` / `group.fallback` / `group.append`, read hub triage rules, AI twin configuration, and plugin-provided connectors
(`inbox.config`), and current translation model (`inbox.translate_fingerprint`). On item changes, the manager emits `inbox.changed`; outside the bus read loop, hub
sends plugins the `inbox.changed` event and Apps `{"type":"inbox"}` (one slow plugin reader does not block others). Hub asks the manager for items needed by plugin detail slot `inbox_item`,
items when deleting a source's data, and GitHub Settings' muted repositories (`inbox.item`, `inbox.forget`, `inbox.repo_mutes`).
Hub still enforces its own boundary: reply-only devices (Watch) may send only text, not change document state or send `/agent …` (which starts an Agent when sent as the user).
Hub rejects these before forwarding (403; the manager checks too). Connector settings, including credentials, remain hub APIs; only muted repositories belong to the manager.

**Send once**: replies (mail, connectors, Lark plugin) carry manager-derived `send_id` (use App `client_id` if supplied; otherwise derive from item, text, and changes,
excluding `last`, like Lark `clientId`). Hub stores it in `sent_messages` (the same table as App sends; the records do not expire). Repeated requests return `already` without resending (safe if a reply is lost in transit
or the manager restarts and retries). A previous interrupted send (hub stopped or timed out) returns `assistant_in_doubt`, also without resending. Event grouping's `group.append` similarly carries
`append_id`: hub retains the plugin's response and returns the same event on retry, never appending twice.

**When the manager is disconnected** (assistant restart, or crashed before being restarted):

- Inbox and translation endpoints return `503 assistant_offline`, "Assistant temporarily unavailable; try again later" (including pull-to-refresh, replies, marking done, muting). Replies rejected this way
  are not recorded in receipts (`reply_receipts.go`), so the same reply can send normally after recovery.
- If the connection drops or times out after handing the reply to the manager, it may already have been sent. Return `502 assistant_in_doubt`, `delivered: "unknown"` (the App asks users to check before deciding,
  not "Not sent"). No receipt is written; retry reaches the manager with the same `send_id`, and hub does not resend anything already sent.
- **Collection loses nothing** (since sources and Inbox share a process from 2026-11-05, this now matters only when the assistant Inbox temporarily cannot read stored state). Hub keeps collecting on schedule and buffers for the manager (`held`: latest version per thread / event / mail, retaining items that slide out of the window;
  at most 2000 items per source and 500 mails per mailbox, memory only, lost on hub restart—when the manager reconnects and immediately collects a round). Sources disabled at collection are not retained.
  On manager connection, hub immediately collects a complete round including mail and delivers it with buffered items, including those that slid out of windows such as latest 40 mails / 30 iMessages.
  If delivery fails (no manager response or database unreadable), keep buffering for the next round. Rounds keep their usual pace meanwhile (a Lark event reads chats only, mail every 3 minutes);
  the complete round owed for the failure waits until a round is accepted again. Incompletely written event batches are resubmitted by the plugin next round.
- Plugin detail slots cannot render (the App keeps its original body). GitHub Inbox settings (CI, muted repositories, ignored branches) are hidden as a whole: hub never supplies settings missing muted repositories,
  which would look unmuted and clear everything on save. Saving without `muted_repos` (or with `null`) preserves mutes; only a supplied list, even empty, changes them.
  Deleting connector data still deletes knowledge-base data but reports the Inbox failure; retry deletion completes it.
- Manager items are in the database: manager or hub restarts reload `inbox_items`. Hub restarts launch the manager again, with a full round on connection.
- Connected manager but stored items / mutes unreadable (database unresponsive): Inbox APIs return `503 inbox_unavailable`, submitted rounds are rejected (hub retains them for retry),
  and nothing is written, avoiding overwriting database state with incomplete memory. Retry reading on next use.
- Readable store, but an item's row cannot be written during a collection round: what the round changed in that item (new items, new messages or mail, a mail's text, items filed as
  over, read elsewhere, settled, muted or superseded) is undone to what the row holds, and nothing is announced for it (`inbox_rollback.go`, #91), unless something else moved the item
  meanwhile (that move's own save writes the row). It comes back with the next round, because the sources hand in the same things again: connectors answer `inbox.collect` from their
  memory, mail rounds read each account's newest page, settling connectors are asked every round. Triage, admission and translation undo their failed rows the same way.
- Immediately after restart, before a round lists sources, the manager still knows plugin-provided connectors (`extensions` in `inbox.config`). Their items still have no fallback and must wait for the model;
  unknown sources behave likewise, never defaulting to unimportant.

## One item = one row

| Source | An item is | id |
| --- | --- | --- |
| Lark | Unhandled new messages in one chat (starting at the first new message; later ones merge into it) | `lark:<feedId>:<first message position>` |
| Lark Docs | One document notification-center card (latest mentions / comments / replies to me in one document) | `doc:<notification id>:<update time in milliseconds>` |
| Mail | Unread mail in a thread (same subject after stripping Re: / Fwd: / 回复： [reply] / [list name] means the same thread; when nothing is left after stripping, mail is split further by sender address: one person's subject-less mail is one item, different people's are separate items, #4852) | `mail:<account>:<thread>:<first mail uid>` |

- Subject-less mail used to be one item per account (sender, preview and "sent to me" taken from the latest); since #4852 it is split by sender, so a subject-less back-and-forth among several people also becomes several items.
  The "no subject" item already open before the upgrade (the old thread key): when every unread mail in it on this page has a sender, it is filed (`done_by=superseded`) and the mail goes into each sender's item, each listed once;
  while some mail has no sender, it stays to hold those (the ones with a sender go to their own items); if it is Needs you or the AI twin worked on it (has a draft), it is kept as it is (mail, tier and draft untouched) and handled as usual, and a sender item a round since #4852 opened with only that mail is filed (`superseded`), so the mail is still listed once;
  when all its mail was read elsewhere it counts as `read` as usual. An item reopened this way is handled by the same rule on the next round.
- A chat / thread has only one **unhandled** item at a time. Messages arriving after it is handled open a new item (only messages after the last one at handling time count).
  Exception: Reopen (single `done:false`, bulk `reopen` / Undo) does not check whether a newer item exists for the thread; it reopens the old one anyway, yielding two unhandled items.
  Later messages always merge into the newest (`UpdatedAt`, ties by ID, `threadIndex.openFor`, #3796); the old item stays unchanged.
- Included Lark messages: gateway `unread` scope (Lark Unread, excluding muted chats) + `mentions` scope (mentions of me in muted groups still count).
  Private chat → `dm`, bot / AI private chat → `bot`, group mentions of me / everyone → `mention`, unmuted group → `group`; unread older than 7 days is excluded.
  Only other people's messages count; your own messages never make a chat Needs you.
- Mail: unread messages in each account's latest 40 Inbox messages, within 7 days. The list API reads only the first 2 KB of body (HTML mail often contains nothing but `<style>`),
  so new mails fetch the full body again (IMAP `BODY.PEEK`, without marking read) for preview and AI context (`BodyOf` tracks which mail was read).

## Docs: mentions in sheets, bases, and documents (2026-10-31)

Handled by the Lark plugin (formerly hub `inbox_docs.go`). Mentions / comments to me in Lark documents, sheets, and bases do not enter the IM chat list, only the **document notification center**
(Lark gateway `GET /documents/notifications`). Each document has one card with only its latest activity (`summary`, such as "A colleague mentioned you"), `atMe`, `url`, `type`
(drive object types: 3 sheet, 8 base, 22 docx, 2 legacy document, 16 wiki). Read once per Lark collection.

- Included: `atMe` or kind mention / comment / reply, within 7 days, not muted. Shares and likes do not count. `kind = "doc"`, fallback Needs you, included in the Mentions filter.
- Cards do not identify the row mentioning me, so hub reads the document to find it (wiki links first use `documents/header` to resolve the underlying sheet / base / docx):
  - **Sheets**: on each tab (at most 6), the first nonempty row is the header. A row containing "@<my name>" in any cell is a mentioning row.
    Columns with state-like headers (状态 / 进度 / 进展 / 阶段—status / progress / progress / stage—or status / stage, etc.) are **status columns**. Allowed values come from the cell's dropdown (data validation), or existing column values if no dropdown exists.
  - **Bases**: records on each table (at most 6, each up to 5 pages × 200 records) containing "@<my name>" in text cells or me in a people field (gateway `accounts` provides names and userId).
    **Single-select** fields with state-like names are status columns; choices are their options, with option colors. Real tables often use a Follow-up owner text column containing "@name."
  - **docx**: take 700 characters on either side of mentions of me; include the latest 3 unresolved comments mentioning me (`documents/comments`, or `base/comments` for bases).
  - Order unfinished rows first (state values resembling 完成 / 已解决 / 关闭 / 上线—completed / resolved / closed / live—count as finished), up to 5 rows. Each includes a title (first column / primary field), at most 6 other fields, and status columns.
- Reply destination (`doc.reply`): reply to a comment mentioning me if present; otherwise comment on the base record (`comments/record`), the sheet cell mentioning me
  (`comments/cell`), or the whole docx document.
- **Change status**: `POST /inbox/{id}/doc/status`. For bases, read the record for an `expect` fingerprint, then `base/edit set`. For sheets, read the revision first;
  dropdown cells require `sheet/structure` `choose` (ordinary `sheet/edit` rejects dropdown / link / mention cells with `DOCUMENT_EDIT_UNSUPPORTED`),
  while plain cells use `sheet/edit` with original `base` values. Reread the document afterward to update the item. Changes may use only allowed values for that column.
- AI twin: prompts include these rows, ref / field / allowed values. `inbox_draft` adds `changes:[{ref,field,value}]`, accepting only rows, columns, and values listed for this item.
  Draft cards show one line per change ("Status: Pending review → Completed · Load-test report"). Send changes status first, then posts the comment; each change can be unchecked in the Edit sheet.
- App: replace the row avatar with a document-type tile (sheets green, bases purple, documents blue), type label Mentions / Comment, second line "Base · A colleague mentioned you."
  Below, show the first mentioning row and its status (tap for a menu; selection writes directly into the document), plus "+N rows." Open `InboxDocDetailView`:
  Open base (in-App document reader), one card per row (fields + a menu per status column), and reply destination.
- Tests: row detection, status columns, fingerprints, reply targets, AI-change validation, plus a live change-and-restore test on your own demo document. Since #1571 these live with Lark Inbox in the Lark plugin (formerly hub `inbox_docs_test.go`, TestDocsLive).
  This repository retains `inbox_agent_test.go` / `inbox_golden_test.go` for block types (`assistant/inbox_blocks.go`) and UI `InboxDocTests` (`MOCK_DOCS=1`). Never test on someone else's sheet.

## AI triage

`server/internal/assistant/inbox_triage.go` (manager-owned): every new item, or item with new messages, goes to the small-task model (`inbox.triage` in model configuration, falling back to `ai` in the manager's `manager.json`, default DeepSeek flash), which assigns one of three tiers,
a summary up to 40 characters, and a short reason:

| tier | UI | Meaning |
| --- | --- | --- |
| `action` | Needs you | Requires the user's reply / decision / action or prompt awareness: direct questions, actionable mentions, private requests, approval confirmations, relevant alerts, email needing replies, approaching deadlines |
| `fyi` | FYI | Relevant and worth a glance, no action needed: group progress, CCs, informative notifications |
| `noise` | Collapsed | Marketing, subscriptions, newsletters, automated notices, irrelevant chatter, bot spam |

- Model input is item type (private chat / mention of user / group / bot / mail directly to user / CC or list) plus item text (`context`: at most 20 recent new Lark messages,
  first 3000 characters of the latest mail's full body). Prompts are in `server/internal/prompts/inbox/` (`triage.md`, drafting `draft.md`, judgment `judge.md`, AI twin `agent.md`; `docs/assistant/prompts.md`). Address the owner using `assistant.owner`; `inbox.rules` in the manager's `manager.json` (or assistant-edited settings) contains the user's rules, appended last and overriding defaults.
- **Per-source rules (replacement, 2026-11-04, #1644)**: sources / accounts need different standards; one global rule set cannot cover them. Connector items may include `rules`
  (the user's original words, such as Lark-plugin Triage rules `triage_rules`; `docs/protocol/connector-protocol.md`). The item **uses only its own rules**, with no global `inbox.rules` appended.
  Only items without rules use global ones. Default tier criteria above are unchanged; user rules remain last and authoritative. Rules are stored with the item (not listed), updated from each new version when activity arrives,
  up to 4000 characters. The assistant can change plugin-source rules (`inbox.rules.<plugin>` in `persona_settings`, confirmed on the phone; `docs/assistant/assistant.md`). Built-in GitHub and Messages connectors do not yet have their own rules (the Telegram extension has `triage_rules`).
- When uncertain between action / fyi, choose action. If the model cannot triage (unconfigured, no quota / concurrency full, error, timeout), use fallback rules (private chat / mention of me → action; everyone-only mention without me → fyi, an announcement; group → fyi; direct mail → fyi; others → noise), with reason "Rule-based."
  Connector items use their rule-provided fallback (`tier`). Model-triaged items (empty `tier`) from hub-owned connectors (GitHub, Telegram, Messages) still fall back to noise.
  **Plugin-provided connectors have no fallback**: noise would silently hide items the user must see (below).
- **Scope (requested 2026-11-04)**: "Triage before admission" and "No admission without fallback" below **apply only to plugin-provided connectors** (installed plugins or plugins in the manager's manager.json, not bundled personad connectors;
  currently the Lark extension). Hub-owned mail, GitHub, Telegram, and Messages behave unchanged: new items appear immediately, then triage in the background; timing and fallback (no rule tier means noise) remain as before.
  Identify by plugin ownership (`extensionConnectors`, recorded when hub starts a plugin connector and passed each round as `InboxSource.extension`), not name.
- **Triage before admission (#1644)**: hold new plugin-connector items aside for a collection round (`inbox_admit.go`). After collection, triage in the background; only then write to the database and list them:
  - Model succeeds: use its tier and summary.
  - Model fails, fallback exists: admit by fallback with "Rule-based." **That version is final**, with no retriage on model recovery (only new activity triggers it).
  - Model fails, no fallback: **do not write this round**. Do not store hidden rows (hard to inspect). The connector answers `inbox.collect` from its own memory and resubmits next round, naturally retrying until the model works.
  - Time limit: new items must begin triage within 20 seconds of a batch start (concurrency 4, at most 30 seconds per item). Unreached items wait for the next round (a round with progress immediately triggers another).
    Background triage does not block collection or pull-to-refresh (`{"type":"inbox"}` pushed afterward). Only one batch runs at once; new items arriving during it are not written that round and are resubmitted next round.
  - Lark-plugin items always have a fallback (hub type rules: private / mentions of me → Needs you, groups → FYI, etc.), so **new Lark items are never withheld**; triage merely moves before listing.
    Only items from other plugin connectors without a rule tier are withheld, all reflected in triage status below. Muted new items are still marked done directly.
  - Existing Inbox items with new activity update first, then retriage in the background as before (concurrency 4, at most 90 seconds per item, batches sequential), with fallback on failure.
    When quota is exhausted, untriaged items keep fallback until model recovery. Plugin items without fallback retain their previous tier and retry next round, never turning into noise.
  - Empty text (`context` empty) skips triage and enters directly as before, shown as Needs you.
- **Triage status**: `GET /api/v1/inbox` (and refresh responses) includes `triage`: `{state, reason, waiting, sources?}`. `waiting` counts each plugin source's new items withheld in its latest triage due to failure without fallback,
  or not reaching them (hub-owned sources are never withheld and are excluded). `state` is the most important cause: `unconfigured` (no triage model) > `paused` (no quota / full concurrency) > `error` (model error) >
  `timeout` (too slow) > `behind` (unfinished batch). `reason` is human-readable; `sources: [{source, name, state, reason, waiting, read_error?, read_reason?}]` separates sources.
  `read_error` reports a failed source read in the latest round (`timeout` / `rate_limited` / `failed`; unknown codes become `failed`), and `read_reason` is a human-readable sentence in all three languages.
  Existing items remain; previously collected data still present in a hub-held round is also used. Clear the diagnostic on the next successful read. Memory only: after hub or manager restart, it is unknown until the next round.
  Memory only, recomputed per round for read sources; unread sources retain status until removed. Nothing waiting yields `{"state": "ok", "waiting": 0}`.
  Apps use it for the waiting-for-triage banner below.
- **Waiting-for-triage banner** (#1773, consistent on web, iOS / Mac, Android): if `triage.state` is not `ok` and `waiting > 0`, show a non-dismissible caution-colored (orange) banner at the top of the Inbox list, below search / filters and above the first section.
  It follows list refresh (Inbox pushes, reconnect, pull-to-refresh), disappearing on recovery to `ok`. Older hubs without `triage` show none.
  - Text: with exactly one waiting source (`waiting > 0` in `sources`, with `name`), "{waiting} {name} messages waiting for triage: {reason}"; otherwise "{waiting} messages waiting for triage: {reason}."
    Use hub `reason`, trimmed. If empty, use state defaults: `unconfigured` no triage model configured · `paused` triage model paused · `error` triage model error · `timeout` triage model too slow · `behind` too many new messages, queued.
    Unknown state without reason shows only "{waiting} messages waiting for triage." Chinese labels place no spaces around the source name ("有 2 条GitHub消息在等分拣", meaning 2 GitHub messages waiting for triage).
  - Button: Set Up only for `unconfigured`, opening Settings › Model configuration › Information filtering (`inbox.triage`). Show only if this device can edit settings, otherwise text alone. Other states only explain the cause.
  - All clients use a clock icon. Web `TriageBanner` (`docs/web/web.md`, "Inbox") has Set Up to the text's right. iOS / Mac `InboxStore.triage` uses design-system `NoticeBanner` (`apple/Persona/DesignSystem/Notice.swift`, `Theme.Icon.time`)
    as the first list row (iPhone below filters with Set Up below text; Mac first row in the list column, Set Up on the right). Set Up opens an Information filtering model-edit sheet, hidden in demo mode.
    Android shows Set Up only if it can read `GET /api/v1/model-config/purposes/inbox.triage` (reply-only devices or hubs lacking that purpose show text only; hidden in demo); details in `docs/android/android.md`.
    Normally paired devices behave identically across clients: devices able to save this setting get the button.
  - Screenshots / integration use mock `MOCK_INBOX_TRIAGE=<state>[:<count>[:<source names, comma-separated>[:<reason>]]]` (`unconfigured`, `behind:8:Lark`, `error:7:Lark,Mail,GitHub`, `error:2:GitHub:Model has no quota`).
- First run 2026-10-27: of 51 items, 3 needed you and 29 were noise (all marketing mail). An example summary: "A contact asks whether the Codex code was merged into 1013, awaiting your confirmation."

## Event grouping (2026-11-04, #1680)

One row per chat is too coarse: a group can discuss several things at once, some requiring me and some not. Combining them prevents accurate prioritization and obscures the subject. Connectors declaring `group` (the Lark plugin)
submit new messages from a chat as **a batch awaiting association**. The manager asks the model which item each message belongs to; **one item (event) is one Inbox row** (`server/internal/assistant/inbox_group.go`,
protocol in `docs/protocol/connector-protocol.md`, "Event grouping").

- **One ledger**: message-to-item membership is recorded only in that row's `meta`. For connectors declaring `meta`, their program writes it (`group.append`: one item at a time, submit meta plus messages, return appended meta).
  With only `group`, hub writes events from the batch itself (no meta; `context` is the text of messages assigned to the item; protocol "Two writing modes").
  **Hub does not parse meta**; it stores and passes it unchanged to the model and plugin. Initially, association stays within one chat.
- **Capability negotiation**: hub declares `hub.capabilities: ["inbox_group"]` in plugin `initialize`; connectors declare `group` in the manifest. Those maintaining meta also declare `meta` and `meta_description`
  (prose explaining meta fields; disallowed without `meta`). `group.tool` and `group.fallback` need no declaration; if absent, return `unknown_method`. The model sees unavailable tools; with no fallback, an untriageable batch waits for the next round.
  If either side lacks support, retain one row per chat. Old hubs / plugins do not affect each other (old hubs do not recognize `group`; see protocol "Old hubs").
- **Model behavior** (`inbox.triage`): prompt = generic association rules (`prompts/inbox/group.md`: replies / quotes, same topic, consecutive messages from one person belong together; a long gap plus new topic starts another;
  uncertainty merges into an existing event; at most 3 new events per batch; tier criteria shared with triage in `inbox/tiers.md`) + plugin `meta_description` + source rules (`rules`, otherwise global `inbox.rules`)
  + batch text. Existing events for the chat are preloaded (the no-query result of `events` below), saving one call. Each model response is JSON, either one tool call or a result.
  At most 4 tool calls per batch; at most 2 minutes total, 45 seconds per call.
- **Tools**: `events` (hub-owned: open events in the same chat plus those handled within 3 days, at most 10, `{id, title, summary, tier, version, done, meta}`; `query` filters text in titles, summaries, meta);
  `messages_before`, `messages_around`, `search` (plugin-owned through `group.tool`, avatars / media stripped, at most 8 KB).
- **Result**: `assign` (each new message's event: existing ID or `new:1`…) and `events` (title, summary, tier, reason for each touched event; new events may `continues` a handled one).
  Hub validates: every new message assigned exactly once; existing events must have been queried in this batch and remain unhandled; at most 3 `new:k`; tier must be one of three and title required. **Invalid results mean model unavailable.**
- **Counts**: hub maintains total message `count`, initialized from assigned messages and incremented by newly assigned messages each batch, ignoring plugin `extra.count`. `started_at` is fixed at creation.
- **Inbox writes**: for connectors maintaining `meta`, call `group.append` once per touched event; retry only failed events, 3 attempts total. Write Inbox items using returned `id`, model title / summary / tier, `triaged_by: "model"`,
  treating this version as triaged. If the user handled an event meanwhile, create a new continuation instead. An unchanged `last` is a retransmission, with no store / push (push once per ID + version). Muted events remain directly done.
- **Fallback**: unavailable model (unconfigured, no quota, error, timeout) or invalid result invokes plugin `group.fallback` for structural grouping (reply / quote chains, topics, consecutive messages by one person, time gaps).
  Without `group.fallback`, do not write the batch; wait for the model and resubmit next round, like Inbox items without fallback.
  It receives events under the same rules as `events`. Use plugin `rule_tier`, reason "Rule-based," `triaged_by: "rules"`; do not send this version back to the model. Validate fallback results by the same rules; invalid batches remain unwritten until the next round.
- **One pass per batch**: association runs in the background without delaying collection or pull-to-refresh. One batch per chat at a time (resubmitting the same batch or twice in one round still counts as one); at most 4 chats concurrently, others retry next round.
  Once every batch message belongs to an item, the plugin stops submitting it; unfinished batches resubmit remaining messages.
- **List**: events are ordinary items with added `triaged_by`. `meta` and their chat membership stay on hub, not the list (plugin detail slot `inbox_item` receives meta through `ui.render`, #1681).
- Not implemented: plugin-reported read / done state for chat events (currently users handle them in the App). A chat's pre-grouping row is not merged into events; it remains until handled.

## Moments (2026-11-01)

Relevant Lark Moments activity (formerly Toutiao Circle) also enters the Inbox, via the Lark plugin (formerly hub `inbox_moments.go`): comments on my posts,
replies to my comments, and mentions of me in posts or comments. Likes and new followers are not items. No feed (`docs/extensions/feishu.md`, "Not a second Lark client"); only activity involving me.

- Source: gateway `moments/notifications?category=message`, collected with Lark every 2 minutes (one call, latest 50 notifications); older than 7 days excluded.
- **One post per item** (Thread): new comments / replies on the same post merge into its unhandled item; `count` is notification count. Activity after handling opens a new item.
  Title is the post's opening; `sender` / preview come from the latest comment.
- Context: post (whether mine or someone else's) + latest 15 comments (mine marked Me, reply target marked), explicitly noting company-wide visibility.
- Triage: mentions default to Needs you; comments / replies default to FYI (often encouragement or chat), with normal model judgment.
- **Send** posts a comment (`POST /moments/posts/:id/comments`), replying to the latest relevant comment, or commenting on the post if it mentions me directly.
  Drafting and AI twin prompts warn that it is visible company-wide; say only what can be public.
- If I already replied elsewhere (the latest post comment is mine and newer than the item), mark done (`done_by=replied`). If all notifications were read in Lark and the item is not Needs you, mark `read`.
- App: Moments icon and label (Mentions for mentions of me, also in the Mentions filter). Opening shows post and comments (`LarkMomentsPostView`, `pushed`).
  DEBUG: `-openInboxItem <id>` opens an item for detail screenshots; mock includes `moment:mp1:mn1`.

## Connector: GitHub (2026-11-02)

Connector activity needing me (`docs/plans/plan-connectors.md`) also enters the Inbox. A connector implements `connector.Inbox`, returning candidates (`InboxThing`, one per thread),
which the manager turns into items (`server/internal/assistant/inbox_connectors.go`; hub delivers candidates each round). Rules match Lark chats: one unhandled item per thread, later activity merged with `count` incremented;
after handling, new activity opens another. A thing whose `at` is older than its open item's latest step is an earlier step read again (a connector re-reading after a restart): it is not counted and never moves the item back (#1527), and its read state is not applied to the item (#2149). This errs toward unread: a thread read elsewhere that the connector reports only at an older step stays unread here until its next activity or its end, instead of an unread one turning read.
A new item never takes the id of an existing one (`sha1(thread, last)`): that step already made an item, perhaps handled since. If the connector says an item ended (merged, closed, replied by me), automatically handle it (`done_by` = read / replied); read elsewhere and not Needs you means `read`.
Triage, drafting, AI twin, done state, and retention use hub's generic logic.

- **GitHub** (`server/internal/connector/github/inbox.go`): with no public hub endpoint for webhooks, poll `/notifications` (unread only), following `X-Poll-Interval` (usually 60 seconds),
  with `If-Modified-Since`; 304 fetches no details. Every 10 minutes, fetch unconditionally (reading notifications on GitHub may not change Last-Modified). Fetch PR / issue, comments, and reviews only for changed threads, up to 30 per round.
  Read up to 5 pages of 50 (only the first page sends `If-Modified-Since`). A thing counts as read elsewhere when it is missing from two list reads in a row that would have had its notification: the whole list was read (it ended on a short page),
  or, with 250 or more unread or when a later page fails, its own newest notification is newer than the oldest notification read (the list comes newest first, so it would have been there).
  GitHub pages by offset, not from a snapshot: a notification read, or moved to the top by new activity, while the poll is between pages shifts the rest, so one still unread can be on no page once. A thing missing once makes the next poll read the list in full (no `If-Modified-Since`) to settle it.
  Any notification in the list keeps its thread unread, also one the inbox leaves out (a reason it does not take, a muted repository).
  The time compared is the notification's `updated_at`, kept apart from the thing's time: a CI item the follow-up moved to a later run (GitHub sent no notification of it) is not inferred read while the connector runs, and that run makes a read item unread again (#2149).
  These times are kept in memory only: after a restart the connector rebuilds the item at its notification and may infer that read, which the hub then ignores for an item already at the later run (above).
  With under one tenth of request quota remaining, poll notifications only and show "Request quota nearly exhausted."
  A rate limit (429, or 403 for a secondary limit or a used-up hour) on any GitHub request pauses the background reads, the poll included: until `Retry-After`; without it, until `X-RateLimit-Reset` only when the hour is used up (`X-RateLimit-Remaining: 0`),
  otherwise for a minute, doubling up to an hour while the limit keeps coming back: within one step of the end of the last such pause, whatever was answered in between (a pause GitHub named is not a step).
  A pause also stops the notification pages still to read. The poll waits it out instead of failing, so the watch is not restarted early.
- Included `reason`: PRs requesting my review (`review_requested`), mentions (`mention`), team mentions (`team_mention`), new comments / reviews by others on my PRs / issues (`author`),
  CI results (`ci_activity`), assignments (`assign`). Others (`subscribed`, `comment`, `state_change`…) are excluded. One item per PR / issue (`example-org/example-repo#12`).
- **Noise reduction (2026-11-03, #577)**: the user found it too noisy. A read-only hub count showed all 52 open GitHub items as Needs you, including 42 CI failures on feature / automatic branches (many pushed by Agents using the user's account),
  multiple items per branch, with reruns counted separately. Causes: one item per CI notification; model-only tiers with prompts saying CI failures and review requests generally need the user, so everything became Needs you. Changed to:
  - **Rule-based tiers** (`InboxThing.Tier` + `Why`, item `reason` identifies the rule); only undecidable cases go to the model:
    - Human review requests, human mentions, requested changes (`CHANGES_REQUESTED`), assignments: Needs you.
    - Bot PR review requests, bot mentions / comments on my PRs, approvals, team mentions: FYI.
    - Ordinary human comments on my PRs: model decides (prompt: requests for changes / answers need you).
  - **Group CI failures by (repository, branch)** (thread `ci:<owner>/<repo>@<branch>`, title `<repo name> · <branch> 的 CI 失败` so items of several repositories' `main` differ). Read the branch from notification title (`<workflow> workflow run[, Attempt #N] failed for <branch> branch`;
    these notifications have no URL), merging reruns and later failures:
    - The thing holds the newest failure: notifications come newest first, and one older than the thread's thing is skipped (several notifications per branch; after a restart all unread ones are read again, 30 per poll).
    - GitHub notifies only the account that started a run, so failures from other people's pushes come from the follow-up check instead: the branch's newest failed run, ended at least 3 minutes before (its own notification, about 20 seconds after the run, is the same failure), joins the open item as its latest step.
    - Default branch and my open PRs: Needs you; other branches: FYI; automatic branches (`train/*`, configurable): excluded entirely.
    - Automatically mark handled without deleting (`done_by=read`, explanatory `reason`) when the branch is green again: every workflow's newest finished run on it passed, cancelled and running ones aside ("GitHub: later succeeded";
      a succeeded notification has it checked at once, otherwise Actions runs are checked every 10 minutes), branch deleted, or PR merged / closed.
  - **Configuration** (Settings › Connectors › GitHub › Inbox; `github.inbox` in the manager's manager.json supplies defaults; stored as `connectors.options.github` in settings):
    - CI failures: Off / Default branch only / Include my PRs (default) / All (all Needs you).
    - Muted repositories: exclude all activity and handle existing items.
    - `ignore_branches`: silent branches (`path.Match` patterns, default `["train/*"]`, editable only in manager.json).
  - **Backlog**: after each collection, hub passes open GitHub items to connector `Settle` (`connector.InboxSettler`). Old one-per-notification CI items ("GitHub noise reduction: group CI failures by branch"),
    muted repositories, automatic branches, and CI items when CI is off are marked done, not deleted. Items still in unread notifications are retiered under new rules.
  - Read-only count comparison on real hub data 2026-11-03: Needs you fell from 52 to 13 (42 old CI items handled; remaining 6 human mentions, 2 review requests,
    4 CI failures on default branch / my open PRs, 1 PR comment). Seven feature-branch CI items and one bot-PR review request became FYI.
- Ended: merged / closed → read; latest thread entry is mine → replied; review request withdrawn (I am no longer a reviewer) → replied if I reviewed, otherwise read. Done state **does not write back** GitHub read state.
- Context: title, state, author, requested reviewers, description (1500 characters), latest 8 comments / reviews oldest first (600 characters each). Prompt kind line: `prompts/inbox/kinds/github.md`.
- **Send** = PR / issue comment (`POST /repos/{o}/{r}/issues/{n}/comments`); tapping Send is confirmation. `reply_to` says "Comment on … (visible to everyone with repository access)." CI failures cannot be replied to (Hand to AI only).
- App (only iOS passes `?sources=all`; Mac, Watch, Android do not see this step):
  - Row: avatar of the latest actor, GitHub badge, `label` (Review requested / Mentions / CI failed…), second line "repository#number · who: content."
  - GitHub filter appears only when items exist.
  - Generic `InboxConnectorDetailView`: title, thread, Open in GitHub (in-App web card), hub-read content. No PR page or diff page.
  - With no GitHub items, the UI is unchanged.
  - Mock `MOCK_GITHUB_INBOX=1`; UI tests `InboxConnectorTests`.

## Muting (2026-11-03, connector v2 · V3, #670)

All sources work alike. Long-press an Inbox row for Done / Reopen plus available mute targets: Mute repository (GitHub), Mute chat
(Lark chats, Messages, Telegram), Mute sender (people in Lark groups, mail senders, latest GitHub actor). Names appear below (repositories omit owner; menu labels never wrap).
Muting handles this item and other covered open items in the same source, reason "Muted: <name>," with toast "Muted ‘…’; future items will not enter the Inbox."
The handling chain says "Muted, done" for existing items, newly admitted items and grouped events; `done_by=read` stays unchanged for compatibility.
Muted under More at the Inbox's top right lists all mutes (name, source · repository / chat / sender), with swipe-left or long-press Unmute. Unmuting admits only future items; already handled ones stay handled.

- **APIs**: `GET /api/v1/inbox/mutes` (creation order, oldest first); `POST /api/v1/inbox/mutes {item_id, tag}` mutes the item's value for `tag` (read from the item,
  not supplied by the client; nonexistent item 404, missing tag 400), returning `{mute, settled}` with additionally handled items. `DELETE /api/v1/inbox/mutes/{id}` unmutes.
  Item `mutes` lists allowed mute targets, which the menu renders.

- **Manager** (`assistant/inbox_mutes.go`): mutes live in `inbox_mutes` settings (source + tag + value + name). Each collection handles covered open items, so future
  messages arrive already done. Item mute targets (`mutes`): connector `InboxThing.Tags` (`repo`, `chat`, `sender`); Lark chat (`lark.feed_id`)
  and latest group speaker (`lark.sender_id`); lowercase mail sender address. Lark Docs and Moments items have none.
- **GitHub muted repositories**: formerly GitHub connector setting `muted_repos`, now source `github`, tag `repo` mutes. On manager connection,
  hub migrates old entries through `inbox.repo_mutes` and clears them from GitHub settings. Settings › Connectors › GitHub (and old Apps) still read / write `muted_repos`;
  hub translates through the manager, so both entry points share the same data.

## Policies (2026-11-06, #5747)

A policy says, for a chat, a sender or a repository, how its items are handled, decided in code rather than by a model. It targets exactly what a mute targets (the item's `mutes` options, read off the item, the same for every source) and has two parts, each optional:

- **Tier**: `action` (always Needs you), `fyi`, `noise`, or unset (triage decides).
- **AI twin**: `first` (escalated even when not Needs you, and started before other queued events) or `never` (never escalated; queued items without a processing conversation return to the user on the next handling pass), or unset. Conversations already running or starting continue as before.

Rules:

- **A mute wins**: a muted item is filed whatever its policy says. Among policies the most specific tag wins: `sender`, then `chat`, then `repo` (so a Lark group can be FYI while one person in it is always Needs you; in groups `sender` is the newest message's sender).
- **The tier is applied after triage**, so the model's title, summary and urgency stay: after model triage (`triageOne`), at admission (`admitInbox`), when an event is grouped (`putEvent`), and for a connector's rule tier when the round files it. When the policy changes the tier, the reason reads "Your rule: <name>"; a tier other than Needs you clears `urgent`. If the model fails, the fallback tier is overridden the same way. Items an event's processing conversation is handling keep their tier (the conversation decides).
- **Setting or changing a policy** re-applies it to open, triaged, unhandled items at once (`applied` count). **Deleting one** leaves the tiers it gave; the next triage of new content decides again.
- **The twin's part** is read by `escalate` and the queue (`sortQueued`, `startInboxAgents`). Before starting or adopting a conversation, the queue rechecks the effective policy, including sender/chat/repository precedence. A queued item with no session and a `never` policy returns to its tier's section, clears pending notes and retry metadata, and records the rule in its handling chain. This also applies during backoff or when every conversation slot is occupied, and to queues restored after a restart. A failed release write is rolled back and retried on the next pass without starting a conversation. `inbox.agent.enabled` still gates the twin as a whole; nothing here sends anything without confirmation.
- Stored in the settings key `inbox_policies` (`assistant/inbox_policies.go`); one policy per source + tag + value.

**APIs**: `GET /api/v1/inbox/policies`; `POST /api/v1/inbox/policies {item_id, tag, tier?, twin?}` → `{policy, applied}` (the value is read from the item; a policy for the same target is updated; one left with neither part is removed); `PATCH /api/v1/inbox/policies/{id} {tier?, twin?}` (both as they should be; neither removes it); `DELETE /api/v1/inbox/policies/{id}`. Clients find an item's policy by matching its `source` and `mutes` options against the list.

Free-text triage rules (`inbox.rules`, a source's `triage_rules`) still exist for what is not a chat or a person ("release notifications are FYI"); policies are for the cases where the user knows exactly who or where.

## Events and handling chain (phase 1, 2026-11-05)

Requirements are in the team's design description "Inbox evolution, phase 1." Existing items are **events**: arrival, triage, manager handling, result, user confirmation, and execution all happen on one item,
with a chronological **handling chain**. Additive only: IDs, sources, existing fields / APIs unchanged; only a new Working state. Code: `assistant/inbox_events.go` (chain, sections, relationships),
`inbox_handling.go` (handling chats and suggestion shape). All fields optional; old hubs send none (Apps keep sectioning by `tier`), and old Apps ignore them.

**Added item fields** (included in lists, details, and all item-returning APIs):

| Field | Meaning |
| --- | --- |
| `status` | Manager-derived section for unhandled items: `action` Needs you · `working` Working (new, between Needs you and FYI) · `fyi` FYI · `noise` Collapsed; absent when done. `tier` keeps its meaning, so old Apps show Working under Needs you |
| `urgent` | Urgent: triage (`triage.md` and grouping `group.md` also return `urgent`) says someone is waiting or a deadline is near (action only). Urgent items stay in Needs you while the manager prepares advice, with "AI is preparing a suggestion" on the card |
| `recap` | Manager-written summary: background, progress, blockers; plain text, up to 600 characters |
| `handling` | Bound handling chat: `{owner: "manager", state, machine_id?, session_id?, since, error?}`; `state` is `looking` (AI reviewing) / `agent` (its sub-chat working) / `waiting` (confirmed action sent, awaiting response) / `done` (turn finished with suggestion or conclusion) / `failed` (failure / timeout, returns to Needs you without suggestion) |
| `suggestion` | At most one current suggestion (see "Handling chats and suggestions") |
| `chain` | Chronological handling chain, oldest first, latest 50 entries at most |
| `related` | Cross-atomic relationships: `[{id, title, source}]`; tap to jump to that item, each retaining its own state |

**Section calculation** (`status()`): collapsed stays collapsed (noise never upgrades); reply / Agent suggestion → Needs you; handling chat reviewing / sub-chat working → Working (urgent stays Needs you);
awaiting response → Working; failure → Needs you; otherwise `tier` (FYI / Needs you; untriaged counts as Needs you). A `conclusion` suggestion uses its chosen tier.

**Handling chain** records only key calls, **written by code where they occur**, not model-authored or investigative steps. Each entry is `{id, at, kind, text, detail?, count?, sender?, messages?, link?, edited?}`.
`text` is a manager-written localized line; Apps may also render by `kind`. `link` is the tap destination: `{type: "source", position?}` (Lark: item's chat at that message),
`{type: "source", uid}` (mail: that mail in the item), `{type: "source", url}` (connectors / Docs: in-App opening), `{type: "source"}` (item's source page),
`{type: "session", machine_id, session_id}` (chat / sub-chat), `{type: "item", id}` (another item). Without `link`, tap expands `detail` / `messages` inline.

| kind | Recorded when | Tap destination |
| --- | --- | --- |
| `received` | New messages enter this item, **one entry per batch**: "Received 3 messages · A contact" (mail: "Received 2 emails · …"), with `count`, `sender`; `messages` is this batch (up to 10, 200 characters each; Lark from plugin context lines, omitted if unreadable, preview for one message; mail uses summaries). Replies from others also go here. Messages outside this item are not recorded | First batch message's source position: Lark first batch `start_position`, later batches by chat sequence (count backward by new-message count, never earlier than previous batch's last message); mail first uid; others source page |
| `triaged` | Triage: "Triaged: Needs you" / "Triaged: Needs you (urgent)," `detail` = tier · reason. Same-tier retriage with unchanged urgency is not recorded | Expand inline |
| `handling` | A handling chat takes over this item (once per chat) | That chat |
| `sent` | Persona sends on your behalf (`/reply`, confirmed reply suggestion): "Sent a Lark message" / "Sent an email" / "Posted a comment" | Lark chat tail (where reply lands); replied mail; others source page |
| `agent` | Handling chat opens a sub-chat: "Opened sub-chat: <title>" | That sub-chat |
| `confirmed` / `rejected` / `instructed` | You confirm / reject / say something (`detail` = executed content or your words; edited text has `edited: true`) | Expand inline |
| `self_replied` | You reply directly at the source, outside Persona: "You replied in Lark"; item still becomes done (`done_by=replied`) | Source page |
| `failed` | Handling fails / times out (`detail` = reason) | Expand inline |
| `concluded` | Manager conclusion (`detail` = conclusion) | Expand inline |
| `done` | Handled: "You marked done" / "You ignored it" / "Read elsewhere; done" / "Muted, done" / "Manager reviewed; no action needed." **An added kind outside the contract table**; Apps render unknown kinds from `text` with a generic icon | Expand inline |

- Stored in item JSON (`inbox_items.data`, no new table), IDs `c1`, `c2`… increasing indefinitely; discard oldest beyond 50.
- **Old items** (stored before this phase) have no chain. On read, backfill two entries from existing fields (`received`: start time, count, sender; if triaged, `triaged`: tier / reason), then append normally.
- Record even with AI twin disabled (`inbox.agent.enabled`): arrivals, triage, your own replies, and done state remain.

**Event splitting**:
- **Atomic threads** come from sources: connector `InboxThing.thread` (one Lark chat, mail thread, GitHub PR / issue).
- **Noise stays one item per atomic thread** (`foldNoise`): all noise events newly created by the model in a grouping batch merge into that chat's open noise event, or one new noise event if absent. Noise events do not absorb higher-tier messages
  (those create separate items), so noise is never split by topic, merged with other work, or upgraded.
- **FYI / Needs you** still split by topic ("Event grouping" above).
- **New messages follow the existing event**: while it is being handled (`handling` looking / agent / waiting, or a suggestion awaiting you), new messages record only `received`; tier, title, summary, urgency stay unchanged,
  with no retriage (`TriagedAt` advances directly). Its handling chat decides what to do (below). Inactive items retriage as before.
- **Cross-atomic related items**: phase 1 does not merge them, only records `related` on both sides, at most 10, excluding same-atomic-thread items. Currently populated when a connector says an item grew from another (`InboxThing.parent` points to an item in another source / chat),
  and from `related` submitted by a handling chat with its suggestion (only existing Inbox items are accepted).

## AI twin: handling chats and suggestions (from phase 1, 2026-11-05)

The AI twin becomes a **handling chat**. With `inbox.agent.enabled` on, every Needs you item goes to the manager (no longer filtered by model 2), **one chat per item**,
bound to that item (`handling` on the item; chat source `inbox`, `origin_ref` = item ID). It runs in the manager background. Code: `assistant/inbox_agent.go` (promotion, chat creation, reconciliation),
`inbox_handle.go` (chat notifications, suggestion tool, confirmation API). Actions visible to others **produce suggestions only**; code executes them verbatim after your confirmation. With the switch off, the manager stops automatic promotion, new handling chats, and chat notifications; chain recording continues.

- **Turning the twin off releases unfinished work**: the next handling pass (including collection after a restart) returns open `looking` / `agent` items, queued or running, to Needs you with `handling.state = failed` and the localized reason "The AI twin is off". Queued messages and start retries are discarded, so later messages can be triaged normally. The release is saved and announced through the normal item-change path; a failed save is retried on the next pass.
- Existing completed suggestions remain available. Rejecting, adding instructions, or confirming a handback while the twin is off returns the item to Needs you immediately with the same reason, without waking or starting its handling chat. Confirmed replies already `waiting` on the other side retain that state. Explicitly handed-off conversations keep their separate workflow. Turning off the twin does not terminate processes already running on a computer.

| Step | Who | Purpose |
| --- | --- | --- |
| 1 Triage | `inbox.triage` (small model) | Triage above, also returning `urgent` |
| 2 Promote | Code | Triaged Needs you items with triaged content and latest message within `max_age_hours` (default 24) queue a handling chat; `handling.state = looking` (Working; urgent stays Needs you). The task comes from the triage summary and reason |
| 2′ Link task | `inbox.judge` (small model, only when tasks are active) | Use only its `task_id`: which task the item concerns ([unified-entry.md](unified-entry.md), "Message cards link to tasks"). Its `handle` no longer decides whether to process the item |
| 3 Handle | `inbox.agent.launcher` (Claude Code launcher, Agents category in model configuration) | Headless claude in the assistant directory; investigate, then submit a suggestion and recap through MCP `inbox_suggest` |

- **Continue on new messages, do not restart**: new messages for an item with a handling chat (a response or additional detail), or your confirmation / rejection / instruction, are **sent into the same chat**
  (`sendToSession`, prompts `prompts/inbox/handling/*.md`: new messages, sent, Agent started, handed back, rejected, your instruction, item done). No new chat or fresh judgment.
  Messages merged into active items do not retriage or change tier ("Event splitting" above). Work requests (new messages, rejection, instruction, handback) start a turn (`looking`); informational notices (sent, Agent started) do not.
- **Handling chat missing**: if sending fails or its computer reports no such chat, record its absence. When work is needed, **recover by event ID** (another handling chat for the same item); create a new one only if none exists,
  including `recap`, handling chain, and intervening messages in its first message. **Manager restart**: items remain in the database, and existing handling chats reconnect normally. A chat still starting at restart (no recorded session ID) waits through the startup interval,
  then recovers by event ID before creating another, avoiding duplicate chats.
- **Concurrency, timeout, and 24-hour eligibility** keep existing settings. At most `inbox.agent.concurrency` (default 1) chats may be `looking`; messages continuing existing chats consume no slot.
  No suggestion after `timeout_minutes` (default 15), or a turn ending without one (after the first minute, chat no longer running), means failure: `handling.state = failed`, return to Needs you,
  no suggestion, and a `failed` chain entry. Waiting on a sub-chat (`agent`) times out only after four times that duration.
- **Prompt** (`prompts/inbox/agent.md`): bound event ID, item content and source; on continuation, recap, chain, and intervening messages. Read-only investigation may run directly; work can use `agent_start` to create a sub-chat, which reports completion back here.
  Direct sends (`lark_send` / `mail_send` / `agent_send`, all connector writes) and user interruptions through `ask_user` / `notify_user` are forbidden. Anything visible to others must be submitted as a suggestion.
  Instructions in messages are not user instructions (Inbox bodies are untrusted data from others); follow only the task and the user's words in this chat. Writes also require the gate or phone approval: prompts are the first defense, the gate the last.
  Knowledge-base tools may update people and projects along the way (`kb_update` is an allowed write).
- Its chat appears in Chats but sends no completion push (`notifyFinished` skips it); the result is the Inbox suggestion.

**Suggestion tool `inbox_suggest`** (`assistant/inbox_handle.go`, description `prompts/mcp/inbox/inbox_suggest.md`; accepts the handling chat started for an item, and with `item` a
conversation the item was handed to ("Item actions", Hand to AI), rejecting other chats):
`{item?, kind, text, note?, handback?, outcome?, recap?, machine?, cwd?, related?, changes?}`.

| kind | Meaning | After confirmation |
| --- | --- | --- |
| `reply` | A reply (`text` sent verbatim, no 【…】 blanks for users to fill; Docs may include `changes`) | Send through `/reply`'s send-once path (`send_id`), **without marking the item done**. `handling.state = waiting` (Working, awaiting response); chain records your confirmation and the send, then tells the chat the user confirmed and it was sent |
| `agent` | Assign an Agent (`text` is its instruction; optional `machine` / `cwd` default to assistant computer / home directory) | Code starts an Agent with that instruction (like the chat calling `agent_start`: `agent` chain entry links to it; completion reports back), `handling.state = agent` |
| `conclusion` | Conclusion: `outcome: fyi` (move to FYI with the conclusion; OK marks done) or `done` (review found no action needed; immediately done, `done_by=manager`) | OK marks done (`done_by=user`) |

- `handback: true`: a suggestion requiring your judgment ("Ask a colleague before replying to the client"). Confirmation executes nothing; hand it back to the handling chat as instructions to act, with the normal chain recording.
- `recap` alone, without `kind`, updates only the summary (for example after a send), up to 600 characters. `related` contains IDs of related Inbox items from other chats / sources, recorded on both sides.
- Each submission increments `suggestion.version` and invalidates the old suggestion; producing a suggestion sets `handling.state = done`. Chain records "Manager suggests a reply / an Agent / advice awaiting your decision" (`handling`, linked to the handling chat); conclusions record `concluded`.
- Old `inbox_draft` (reply only) remains supported, equivalent to `kind: reply`, for chats started before the tool list changed.
- `item` (an item id) is needed from a conversation the item was handed to (`assistant/inbox_handoff.go`); from the item's own handling chat it changes nothing. A tool error refuses an unknown item,
  one not handed to this conversation (only a conversation in its `handoffs` may write back) and one the user already filed ("answer in the chat instead"; a filed item is not reopened);
  nothing changes then. Without `item` the tool works as before. A suggestion from a hand-off records where it came from (`suggestion.conversation`), its chain entry links that
  conversation, and the item's `agent` / `handling` stay as they were (no message card, no old-App draft).
- **Task-related items**: for linked tasks (2′), prompts include task progress; a reply suggestion inserts a message card into the chat that started the task (`message`, retaining sent state after sending).

**Confirmation API** `POST /api/v1/inbox/{id}/suggestion`: `{version, action, text?, client_id?}`, `action` = `confirm` / `reject` / `instruct`.

- `confirm`: execute exactly what is shown; supplied `text` replaces suggestion text (edit then confirm; chain `edited: true`, chat learns the changed text).
- `reject`: record rejection, clear the suggestion, and return to the handling chat to reconsider. `instruct` requires `text` (such as "Do not reply this way; ask about timing first"; absent text returns 400), records your words, clears the suggestion, and asks the chat for a new one.
- Responses: `200` item; `409 {"code":"suggestion_stale","error":"建议已更新","item":{…}}` ("Suggestion updated": version is no longer current or no suggestion exists; App shows the notice and redraws from the returned item,
  including the second tap on the same suggestion); `409 inbox_item_done`; `400`; `403`: reply-only devices (Watch) may confirm only replies / conclusions, as text (no Agent creation, handback, rejection, instructions, document-status changes, or `/agent …` sends;
  hub allows the route for Watch, while the manager checks the actual suggestion); `502 assistant_in_doubt` (as `/reply`: it may already have sent; confirming again gives 409); other 502 errors.
- **Execution failure** (send fails / times out, Agent fails to start): `handling.state = failed`, return to Needs you without a suggestion; chain `failed` ("Send did not succeed").
- **Persona's own sends are not your replies**: after a confirmed reply, the next Lark-plugin round reports the chat's latest message as yours (`done: replied`). While the item awaits the other person's response and no new messages arrived (`last` unchanged),
  treat it as Persona's send: do not archive or record You replied in Lark. If the other person replies and you later reply in Lark yourself, mark done as usual (`done_by=replied`), record your reply,
  and tell its active handling chat to stop. Phase 1 ambiguity: if you add another Lark message after sending but before the other person replies, it is also treated as Persona's send.
- **Marked done / dismissed / Send in an old App** (`/reply` still marks done): clear the suggestion and tell an active or suggestion-waiting handling chat the item is done and to stop.

**Old-App compatibility**: reply suggestions also write `agent.draft` / `agent.note` (`agent.state = drafted`). Old Apps still show an AI twin draft, whose Send uses `/reply` and marks done as before.
Processing uses `agent.state = running`, failure `failed`; other suggestions (Agent, conclusion, handback) show no draft on old Apps (`skipped`). Previously stored AI twin drafts become version-1 reply suggestions (`handling.state = done`),
confirmable in new Apps. `/reply`, `/done`, `/draft` unchanged.

- Local model 1 verification: `PERSONA_LIVE_AI='{"base_url":"…","api_key_file":"…","model":"…"}'` runs `TestInboxModelsLive`
  in the manager package: `go test -run InboxModelsLive ./internal/assistant`, on the hub machine, with the key file there.

## Relationships: parents and children (#2128)

An item can have a parent (`parent: {kind, id, by}`), linking an item arriving from a chat, becoming a to-do, and going to several Agents into one thread.
A parent is another Inbox item (`inbox`), a to-do (`todo`), or Agent session (`session`, ID `<machine>/<session>`). Persona stores, lists, and navigates only; it never guesses parents.
To-dos do not store a separate tree. Code: `assistant/inbox_parent.go`.

- **Origin** (`by`): a new event continuing an earlier one from grouping (`continues`), connector `InboxThing.parent` (`connector`), or user-set (`user`).
  Automatic values never overwrite user values; connector values may overwrite `continues`, not vice versa.
- **Set**: `PUT /api/v1/inbox/{id}/parent {kind, id}`; `null` or `{}` removes it (no DELETE, avoiding collision with `DELETE /api/v1/inbox/mutes/{id}`).
  Item parents must exist (404 otherwise); no cycles, at most 8 ancestor levels (409 otherwise). IDs up to 256 characters, no control characters (400). Return updated item.
- **Read a thread**: `GET /api/v1/inbox/tree/{kind}/{id...}` returns `{node, item?, ancestors, children}`. `ancestors` nearest first (last may be a to-do / session without an item,
  or an item no longer in Inbox, with no `item`); `children` are direct child items, oldest start time first. `kind` may be `todo` / `session` to list items growing from it.
- Extension tokens require `inbox.read` for trees, `inbox.write` for parent changes (`extwire/scopes.go`); Watch cannot call either API.
- Client rendering: "Relationships" under "App" below (web `web/src/views/inbox/InboxLine.tsx`, read-only, no parent-edit entry).
- Tests: `inbox_parent_test.go` (`continues`, PUT / removal, depth, precedence, trees).

## Lifecycle

- Hub background collection (`server/internal/extmgr/inbox.go`, delivering data to the manager for item composition): collect Lark chats 3 seconds after real-time Lark events; this round reads chats only. Every 2 minutes, also read document notifications,
  Moments, and other connectors (their events / setting changes also trigger this full round); mail every 3 minutes. Previously every Lark event read all of them (#674). Pull-to-refresh
  (`POST /api/v1/inbox/refresh`) collects everything immediately. Changes write `inbox_items` and push `{"type":"inbox"}`, prompting App refetch; new plugin-connector items store / push only after triage ("AI triage").
- **Done** is recorded only in Persona (`done_at`, `done_by=user`; ignored items use `dismissed`, see "Clean up all"; one replaced by the new thread rule uses `superseded`). It does not change Lark or mail read state. Done items remain listed for 3 days, reopenable at the bottom,
  and are deleted from the database after 8 days (meanwhile preventing old still-unread Lark messages from resurfacing).
- Read elsewhere (Lark / another mail client): mark `read`; non-Needs you items become done (`done_by=read`). Needs you remains open, but unread count becomes zero and me / everyone mention flags clear (no longer unread mentions). Rule-tiered items are retiered using current rules after reading; no longer Needs you means done.
  Already-read open items with remaining unread count or stale rule tiers (read before a rule change) are reconciled next round (#798).
- A reply by me in Lark (latest chat message is mine, newer than the item) handles it (`done_by=replied`).
- Lark collection reads messages without holding a lock. If an item is marked done during the read (manually or in bulk), newly read messages create another item rather than merge into the done one (previously swallowed and invisible, #1105).

## Mark All as Done (2026-11-04, #1105)

The user's "Mark all read." Inbox read means done, recorded only in Persona, **not written back** to Lark, mail, or GitHub notifications, just like single items ("Lifecycle" above). Bulk handling therefore has no source sync / retry.

- **Entry point**: the original Mark All as Done entry has been combined with Dismiss All into Clean up all (#5114, next section). This section retains only request / Undo rules. Scope = **unhandled** items in the current filtered / searched list.
  Waiting Agents are not Inbox items (they await answers, not archiving), so excluded. Disabled with Agent filter or no unhandled items.
- **One request**: `POST /api/v1/inbox/done` (`docs/protocol/protocol.md`), with each listed item's `version`. Hub checks versions and marks within one lock. Items with new messages merged after listing remain unhandled,
  with toast "N items have new messages and remain in the Inbox," then App refetch. Already-done items do not change `done_at` (idempotent); one database write and one Inbox push for the batch.
  `version` is a short hash of `Last` (latest message position / uid / notification), unchanged by triage, reading elsewhere, or marking done.
- **Undo**: update locally first (badges and sidebar counts immediately), then show a toast ("Cleaned up N items" for Clean up all) with Undo after hub responds. Within 5 seconds, Undo reopens `changed` items through the same endpoint.
  Undo rather than prior confirmation matches existing Mark All Seen / swipe-to-done behavior and is smoother. Failure restores everything and shows an error.
- FYI · Mark All Seen, Collapsed · Clear All, and Mac multi-select E use this endpoint too (previously per-item requests), with Undo.
- The App icon has no badge (only tabs / sidebar counts), so no separate update.
- iOS new-shell toasts use a non-touch-intercepting window (`ToastWindow`). Button toasts intercept only near the button, leaving other touches to content below.
- Tests: `inbox_bulk_test.go` (scope, changed-version skip, idempotence, Undo, new item when marked done during message read, concurrency), `InboxBulkTests`, UI `InboxMarkAllTests`
  (mock `/mock/inbox_bulk` records every request), web `inbox.test.ts`.

## Clean up all (2026-11-05, #2609; unified 2026-11-06, #5114)

Extends Mark All as Done above with hub-side filter handling and confirmation for large selections. Each client previously had Mark All as Done and Dismiss All,
which users saw as one action: clear this batch they do not need to handle now. #5114 combines them into Clean up all (English "Clean up all", Japanese "すべて片付ける").

- **Record as dismissed**: Clean up all sends `action=dismiss` (`done_by=dismissed`), since the user did not actually handle these items. The only visible difference is the relationship-chain line
  saying Dismissed rather than Done. No statistics, triage, or other logic distinguishes `user` from `dismissed`, so using dismissed is the least disruptive choice.
- **Compatibility**: hub `/bulk` continues accepting `done` and `dismiss` from shipped iOS / Mac / Android clients, preserving their respective semantics.
  New clients send only `dismiss` for Clean up all. Section Mark All Seen / Clear All remain `done`.

- **Dismiss** means "I do not need to handle this." Like done, Persona-only (`done_at`, `done_by=dismissed`), no source writes, not mute (later messages in the same chat still arrive; use Mute to stop them).
  Side effects match single-item done: one batch write and Inbox push (lists, tab badges, sidebar counts update), queued AI twins do not start (only unhandled items qualify),
  existing drafts remain on the item and survive Undo. Single dismiss: `POST /api/v1/inbox/{id}/done {done:true, dismiss:true}`.
- **API**: `POST /api/v1/inbox/bulk {action: done|dismiss|reopen, items?|ids?, filter?, as_of?}` (`docs/protocol/protocol.md`).
  Clients still pass each listed `id` / `version` (correct for client-only filters such as search / Agent); without IDs, supply `filter` (source / account / section / kind) and `as_of`.
- **Cutoff**: `GET /api/v1/inbox` returns `as_of` (list-read time). Each item tracks when its current content reached hub (`arrived_at`, first listing / persistence;
  reset when `Last` changes; old data backfills `updated_at`). Filter handling leaves items with `arrived_at` after `as_of` unchanged (new arrivals / merged new messages), returning them in `skipped`, `reason=changed`.
  Do not compare message times: Lark / mail messages may predate list viewing but be collected afterward.
- **Undo**: response `ids` are changed items; `undo` is `{action:"reopen", ids}`. Send it unchanged to reopen only those items. Toast's 5-second Undo sends this.
- **Confirmation**: 10 or more items require confirmation ("Clean up N items (scope). New messages arriving later are unaffected; you can undo afterward."); fewer act directly with Undo. Scope / count are from the list at tap time.
- **Entry points (one per client)**: web has Clean up all to the filter row's right (hidden at narrow widths), and one Clean up all item in ⋯ with scope / count below.
  Android top-bar More has one item; iOS More has one item with scope / count below ("All · 12 items", "Lark · Mentions · 3 items", "Search ‘weekly report’ · 2 items").
  Mac has one toolbar button (scope on hover) and one Inbox-menu item, ⇧⌘E while Inbox is active. Apple passes `items` (ID + version) for current filtered / searched unhandled items, not `filter`.
  Section Mark All Seen / Clear All still use `/done`. Watch has only single-item done, no bulk entry, and reply-only devices cannot call `/bulk`; extensions need `inbox.write`.
- **Old hubs**: without `/bulk` (404 / 405), fall back to `/done`. It lacks dismiss, so Clean up all records done (`done_by=user`), still removing items from the list. Reopen also falls back to `/done`.
- Tests: `inbox_bulk_test.go` (`TestInboxBulkFilterAsOf`: post-cutoff items / messages unchanged, dismiss, Undo only changed items; IDs, single dismiss, bad requests),
  `hub/devicetokens_test.go` `TestAnswerRoleOnlyWhatTheWatchDoes` (Watch blocked), `hub/testdata/route_scopes.txt` (`inbox.write` required); web `inbox.test.ts`; Android `InboxBulkTest`, `InboxRepositoryTest`, `InboxAssistantTransportTest`;
  Apple `InboxBulkTests` (responses / `undo`, old-hub fallback, failure rollback, confirmation threshold), UI `InboxMarkAllTests` (Clean up all + returned Undo; confirmation with `MOCK_INBOX_EXTRA`); hub `TestInboxBulkOldClientsDoneAndDismiss` guards old clients' `done` / `dismiss`.

## App

- `InboxStore` (`Persona/App/InboxStore.swift`): list cached locally (`inbox-items`), shown before refresh on cold start; fetch on Inbox events, reconnect, page entry.
  Mark done locally before request; restore on failure. Tab badge = unhandled Needs you count.
- Page (`Persona/Views/Inbox/InboxView.swift`): top filters (All / Lark / each mail account | Mentions / Private / Groups / Mail); search titles, senders,
  body, summary. Sections: Needs you (expanded), Working (always expanded, non-tappable heading, Inbox phase 1; present only with hub `status`, absent on older hubs using `tier`; excluded from badges and Watch Needs you, while urgent `status: action` still counts), FYI (expanded, Mark All Seen to the heading's right), Collapsed (collapsed, Clear All), then Done (collapsed).
  Tap headings to expand / collapse. Row: 40-point avatar (18pt source badge at bottom right: Lark blue, mail neutral gray), title (headline) + type `Chip` (mentions red), time;
  with summary, second line ✦ summary, third small "sender: content" (secondary gray, not tertiary); before triage, just "sender: content." Count badge (normally gray fill, primary-color fill for mentions).
  Separator starts at text column (16 + 40 + 12). Lark avatar palette avoids red / orange that imply Needs you. Full left swipe = done. More contains Clean up all (see "Clean up all" above).
- **Waiting Agents also appear under Needs you**: pending questions, plans, permissions, terminal prompts, including assistant `ask_user`, excluding muted Agents, sort first
  (`InboxAgentRow`: rounded square like Machines session rows, request-type icon on light red fill; Agent title, question second line,
  red "Claude is asking / Plan needs approval" + machine third line. No type chip after the title, which gets full width). Tap opens the Agent chat (assistant in Chats tab). Combined only in the App,
  not stored by hub Inbox; Agents unchanged. Agent filter shows only these, while source filters hide them.
- Opening: Lark chat at the item's first new message (`LarkChatView`, focus = start_position), mail at latest message; Done at top right.
  Visible Lark messages are reported read, including the first screen opened from Inbox (confirmed 2026-10-31; gate in `docs/archive/redesign/ux.md` 3.5 / 3.6). Done itself stays Persona-only.
  Under navigation, `InboxActionBar`: ✦ summary + AI Draft Reply / Hand to AI.
  Relationships below summary (#3071, matching web #2352; `Persona/Views/Inbox/InboxLine.swift`): fetch `GET /api/v1/inbox/tree/inbox/{id}` on open,
  list parent chain first, distant ancestors above (to-do / Agent session / event), then child Next steps. Tapping an event pushes in the current stack,
  a to-do opens details, an Agent opens its chat. Missing ancestor items show "No longer in Inbox" and cannot be tapped. No ancestors / children or old hub without API means nothing drawn.
  Mac puts it below summary in the right action column (`MacInboxActions`); event taps push in the detail column's own stack, returning to the original item.
- **Events (Inbox phase 1, iOS / Mac)**: models in `Shared/InboxEvents.swift` (`handling`, `suggestion`, `chain`, `related`, tolerant per-entry chain decoding;
  unknown `kind` rendered from `text` with a generic icon). An item is an event (`isEvent`) only if it has one of these fields or `recap`; otherwise rows / details remain pixel-identical.
  - Row: suggestions show their kind + first line below the summary (reply / Agent / conclusion / judgment needed), replacing the old AI twin draft card (hub mirrors replies into `agent.draft`; do not render twice).
    Working shows handling state (AI reviewing / Agent working / awaiting reply); urgent items without suggestions show AI is preparing a suggestion; failures show "AI couldn't handle it" (`InboxEventText.mark`).
  - iPhone details use one page (`InboxEventPage`): ✦ summary + View original, state, recap, suggestion card (adjacent buttons: reply Send · Edit · Reject; Agent Start · Edit · Reject;
    conclusion OK · Reject; judgment Confirm · Edit · Reject), handling chain, relationships, related items, then AI Draft Reply (absent with a reply suggestion) and Hand to AI at the bottom.
    Original source pages (Lark chat, mail, plugin-rendered) sit behind View original and Received messages chain entries (`InboxSourceRoute` → `InboxSourcePage`, Lark at link position, mail at link uid).
  - Mac keeps the source on the left rather than opening a separate page. Below summary in right-side `MacInboxActions`: state, recap, suggestion card, chain, related items.
    Chain entries linking only to the source do not navigate (already beside them); position / uid links push within the detail column's own stack. At narrow width with only the top action strip, recap keeps three lines.
  - Edit (`InboxSuggestionEditor`): changing text then Send / Start / Confirm = `confirm` with `text` (omit if unchanged); Say something + Tell AI below = `instruct`.
    One `client_id` per attempt, with the card's displayed `version`. `409 suggestion_stale` shows Suggestion updated and redraws from the returned item, closing the editor. Failure stays in the editor for retry (`InboxSuggestionModel`).
  - Chain initially shows only the latest entry; Expand all (N) reveals all, oldest first. Each row: time · kind icon · text. Taps follow `link` (`InboxChainTarget`):
    source → source page; `session` → chat (manager handling chat in assistant, sub-chat as Agent); `item` → that item; no link → inline `detail` / `messages`.
    Entries with both navigation and messages (Received messages) have a separate expand arrow on the right.
  - Manager-archived items (`done_by: "manager"`) show "Handled by the manager" in detail state. Chain `done` (user marked done / manager found no action needed) uses ✓ and is not tappable.
    FYI conclusions remain under FYI with their card; OK archives them.
  - Watch unchanged: uses `agent.draft` as before.
  - Unit tests `InboxEventsTests` (decode every event state in `PersonaTests/Fixtures/inbox-events.json`, sections, row text, chain targets, suggestion buttons with FakeHub).
- DEBUG: `-tab mail` (Inbox), `-openInboxFirst YES` (first unhandled item), `-openInboxItem <id>` (specific item), `-showInboxAction draft|handoff` (then that sheet),
  `-showInboxSuggestionEdit YES` (suggestion editor on iPhone event page).
- **Plugin-rendered details** (#1681; `docs/extensions/extensions.md`, "Inbox items"): for items from a plugin connector (`source` = connector) contributing `inbox_item`,
  keep the header (title, source, Done, ✦ summary, AI Draft Reply / Hand to AI), replacing the body with the plugin's item View (Lark renders event meta: people, time span, continuation, message chain, reply, View in chat).
  View in chat returns `persona://inbox/<item id>/source`; the App opens its original source page (Lark at the item's first message).
  Hub passes `{id, title, summary, tier, reason, done, meta, extra}`; meta unchanged, unparsed. Sources without a slot, old hubs without it, read-only devices, and rendering failures keep the original body.
  Web implemented (`docs/web/web.md`, "Settings," "UI slots"); iOS / Android in separate PRs.

## Item actions (`Persona/Views/Inbox/InboxActions.swift`)

- **AI Draft Reply**: `POST /api/v1/inbox/{id}/draft {instruction}`. The manager uses the Draft reply model in the user's voice (Lark short and conversational; mail matches the incoming language and formality).
  Do not invent unknown facts or leave 【】 placeholders for the user; omit unknown details. Include `assistant.notes` as About the user. Edit directly in the sheet or enter "Have AI revise: …" to rewrite.
  Send uses `POST /api/v1/inbox/{id}/reply {text}`: the manager asks hub to post in the original Lark chat through its plugin, or reply by mail (Re: subject, In-Reply-To / References,
  quote first 40 lines, using the receiving account), then mark done (`done_by=replied`). Phone and Watch use the same API.
  A reply handles only messages present in the item when sending began. New messages arriving during sending (parallel collection changes `last`) leave the item unhandled; new messages are not archived with it (#1828).
  Lark replies send once: phone / Watch taps or timeout retries send identical text only once (queued per item, `clientId` = UUIDv5(item ID + text)).
  Retrying identical text for an item left unhandled above does not resend. Requests with `client_id` use their own IDs; a new ID is a new reply. Web / Apple do not pass it yet,
  so identical replies to newly arrived messages are treated as retries. Manager `send_id` to hub ("Send once" above) is derived the same way
  (use `client_id` if supplied, otherwise item ID + text + changes, not `last`), so retries after manager restart also do not resend.
  Lark replies go where the other party can see them (`lark.reply_to` / `lark.thread_kind`, recording the last message by someone else and whether it is in a topic during collection):
  private chats post normally; topic items (topic-group topics or topic replies in ordinary groups) reply to that message within its topic (`replyMode:"thread"`);
  group mentions outside topics quote that message; other group items post normally (unclear reply targets would make quotes odd); legacy data without `reply_to` also posts normally.
  Topic groups collect only the latest page of topic roots (P-04 unfinished), so the last message from someone else is the latest topic, not necessarily the one with new replies.
- **Hand to AI**: attach the item as `<persona-context kind="context" title="Lark · Group name">` (hub `context` + feedId / mail uid, letting tools read more and
  reply), plus the requested action (default "Summarize this, tell me what I need to do, and suggest a reply," with two other presets), sent as one message.
  Targets: **Persona (main chat)**, the default (#1194, every client): the item's own manager's main conversation (another manager's item goes to that manager's,
  found by asking it; with none yet, the message starts it), which then opens; **Persona (new chat)** starts a new conversation with the item's manager (iOS / Mac switch to the
  Persona tab); an Agent (latest 8) receives it in its session (navigate there). Marked done by default once the message got there, except for the main chat (below).
  **Write-back to the item** (#1194, web, iOS, Mac and Android): the main-chat target calls `POST /api/v1/inbox/{id}/handoff` `{text, client_id}` on the item's manager (`assistant/inbox_handoff.go`),
  which sends the text after a block naming the item (`prompts/inbox/handoff.md`) to its main conversation through `session.send_once` (the client id is the message id, so the same
  request again sends nothing more), or starts the main conversation with it, and answers `{item, session}`; a work machine is refused, and a failed send records nothing (`apierr.Call`'s status, as for a new conversation).
  The item keeps the hand-off (`handoffs`: `machine_id`, `session_id`, `at`, the latest 5) and its chain gets one `handling` entry per conversation ("Handed to Persona's main chat",
  linked to it). Its processing conversation (`agent`, `handling`) is not touched, so the AI twin's sweep and timeouts never take the main conversation for one of its own.
  That conversation may then write back with `inbox_suggest` and `item` (above). What the user does with its suggestion is told there, wrapped to name the item
  (`prompts/inbox/handoff_note.md`; a Persona notice sent as a plain `session.send` without a message id, a failure only logged, which the hub marks `injected` (`system` / `inbox`)
  so the apps show one line, "Inbox: an update on “title”", not the model's text as a user bubble): reject, instruct, handback, a confirmed reply sent (which files the item as replied:
  nobody there follows the other side's answer) and an Agent started (as that conversation's own task; the chain's `agent` entry links it). A confirmed conclusion files the item as
  usual; a processing conversation still on the item is told it is over.
  "Also mark as done" is off by default for the main chat (it may still write back) and on for the other targets; switching targets follows that default until the user sets the box,
  whose choice then stays. Checked, the item is filed once the hand-off is answered, and a later `inbox_suggest` on it is refused as already filed.
  Not covered: later messages on the item are not forwarded to the main conversation (it reads the source with its tools when asked), and filing the item is not told there.
  **The iPhone's event page** (#2055, `Persona/Views/Inbox/InboxHandOffBar.swift`) has no sheet: a bar pinned to the bottom, like iMessage's, at most two rows high and riding up
  with the keyboard (the page gets its height as a bottom inset). The top row scrolls sideways through one-tap chips: the three presets (Summarize & Suggest Reply, Look Into It,
  Handle It) and Draft Reply / View Draft, which opens AI Draft Reply's sheet. Under them: a target capsule (a system menu: Main Chat, New Chat, the latest 3 Agents, More… with
  the next ones), a one-line "Hand to AI…" field and Send. A chip or a send goes without a confirmation, held for 5 s behind a toast at the bottom (`InboxHandOffModel.undoWindow`):
  Undo takes it back (a typed ask goes back into the field), View sends it at once and opens the conversation; left alone it goes and the toast says where, with View. Sending
  never leaves the page. The last target is remembered (`inbox.handOffTarget`); an Agent no longer listed falls back to Main Chat. "Also mark as done" keeps the targets' defaults.
  The Mac and the iPhone's source pages (whose own reply composer sits at the bottom) keep the sheet, which opens the conversation after sending.
  iOS and Mac (`InboxHandOffModel`) send it with one `client_id` per hand-off. Whenever the answer leaves open whether the text went (no answer or a timeout, the connection lost
  midway, a 5xx without a `code` from something in front of the hub or from the hub unable to keep its receipt, `assistant_in_doubt`, `delivered: unknown` or `yes`, the node lost
  without `delivered: none`, a pending (`202 {"state":"pending"}`) or unknown receipt), the sheet says to check the chat before sending it again, and the same ask sent again is the
  same hand-off: its first text, not one rebuilt from the item (which may have changed since), under the same id. From then on every other answer under that id short of success (all
  but the three below that say nothing about an earlier try) is taken the same way, even one saying nothing went (`delivered: none`): the hub gives back what it kept for that id,
  which may be a relay the phone or a proxy cut off (a bare `internal`), its own refusal (`op_id_reused`) or, after `assistant_in_doubt` (whose receipt it drops, and which may never
  have reached the phone), a new run's answer at the manager. The phone can't tell which of these it got, so none is taken as final, and the sheet adds that changing the instructions
  or reopening the sheet sends it anyway. A 404 / 405 then says to look first instead of falling back to the plain message, and a plain message that may have gone is not sent again
  by the same ask. On any try, the id and first text are also kept through no connection, a cancelled request and `assistant_offline`, which say nothing about an earlier try and show
  their own words (a cancelled request shows nothing). On a first try, any other answer is final for that id, and a failed load of the item, which sent nothing, says the hub can't be
  reached (a timeout or a bare 5xx) or what went wrong. The hub answers what it kept for that id or runs it again, and the manager's `session.send_once` drops a repeat, so the same
  ask sent again from one sheet, with no other ask handed to the main chat in between, hands the item over once (except a hand-off that started the main conversation and whose record
  the manager lost); another ask or a reopened sheet is a new hand-off, and so is the first ask again after another one went to the main chat. Once it went, they open the main
  conversation on the message as sent under that id (its transcript copy carries it and takes the bubble's place; no bubble when the copy is there already) and read the assistant
  settings again, since the hand-off may have started the main conversation. Like every other inbox request from iOS and Mac, it names no manager, so it goes to the default manager.
  Android (#1384, `RealInboxRepository.handOff`) sends it to the item's manager, refuses it while offline, keeps the operation id of a failed attempt so a retry is the
  same hand-off, and takes an answer without `session` as an error rather than sending again; it then opens the session the answer names.
  Every client falls back to plain chat text when the hub or manager answers 404 / 405. On iOS and Mac the fallback goes
  through the main conversation's outbox when it is listed (a failure shows there with Retry); otherwise it is sent or the conversation started from the sheet, which stays open
  with the error when that fails.
- **Done**: row swipe-left, top-right ✓ in details, or alongside Send / Hand to AI.

## Watch

- Home Needs you = waiting Agents (formerly Waiting for you) + Inbox `action` items (`WatchInboxRow`: source / kind, title, AI summary).
  iPhone's Watch summary carries up to 20 items (`WatchSummary.inbox`); while open, Watch fetches `/api/v1/inbox` directly every 8 seconds. New items vibrate.
- Open `WatchInboxItemView`: summary, latest message; AI reply drafts first and displays it, then Send (`/reply`); Done.
- DEBUG: Watch `-openInboxFirst YES` opens the first item.
