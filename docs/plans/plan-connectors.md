# Plan: connectors (GitHub and iMessage first)

`Status`: in progress. Telegram has its own plan in `docs/archive/extensions/plan-telegram.md` (#533; an extension `since` 2026-11-05 in `nwverse/persona-extensions`, #1704, #2057). User, 2026-11-01: "Assign a lane to write the `connector` plan first, starting with GitHub and iMessage"; implementation began 2026-11-02: framework (C1), then GitHub, then iMessage (assigned to the terminal lane, #449),
with Lark and `mail` migration (M0–M6) later. Use conservative defaults and keep switches for the two undecided points ("Decisions needed from the user", 2 and 7).
After the user approves, follow "Implementation steps" and check completed steps; once everything is done, merge the design into `docs/extensions/connectors.md` (new), `docs/assistant/knowledge.md`, `docs/assistant/inbox.md`, then delete this plan.
2026-11-03: migration M0–M6 complete; current structure is in part I of `docs/extensions/connectors.md`, together with v2 design and progress (closed out 2026-11-03: V1, V3–V6 complete, V2 and V7 rejected). This plan remains as process history and for unfinished steps (I5, later Telegram steps).

## Background: separate integrations today

| Pipeline | Lark | Moments | Mail |
| --- | --- | --- | --- |
| Knowledge-base archive | `larkSource` (`kb.Source` + `kb.Backfiller`, then in the hub's kb.go) | `momentsSource` (same, empty `Older`) | `mailSource` is a shell; real archiving is a separate `hub.kbMail` loop, cursor in `mail-cursors.json`, calling `kb.Engine.Ingest` |
| Documents / meetings / events | `larkItems` (`kb.ItemSource`) | — | — |
| `Inbox` | `collectLark`, `collectDocs` (`inbox.go`, `inbox_docs.go`) | `collectMoments` (`inbox_moments.go`) | `collectMail` (`inbox.go`) |
| `Inbox` items | Hardcoded `Source: "lark"`, separate `Lark` / `Doc` / `Moment` blocks | Same | `Source: "mail"`, `Mail` block |
| `Reply` | `replyInbox` switches according to which of `Moment` / `Doc` / `Lark` / `Mail` is nonempty | Same | Same |
| AI read tools | `lark_chats` / `lark_messages` / `lark_search` (names hardcoded in `assistantTools(withLark, withMail)`), Lark gateway skill | Lark gateway skill | `mail_*` (`mail/mcp.go`) |
| Write `gate` | Node Bash `gate` (`node/larkgate.go`, hub.json `lark_gate` rules) | Same | `hub.approve` (called by `mail_send` itself) |
| Live updates | Lark gateway event WebSocket (`lark_events.go`) wakes `Inbox` and archive | Polled with Lark | Polling (`Inbox` 3 minutes, archive 6 minutes) |
| App links | `FeishuLinks.route` → native documents / Moments, `LinkSheet` web for everything else | Same | — |
| Settings › Connectors | `FeishuConnectorView` (`GET /api/v1/lark/status`) | — | `Mail` page |

Each new source needs its own implementation in six or seven places, plus another branch in every `switch`. Clients also hardcode `sources`: iOS `InboxItem.isLark`, Android `source == "lark"`,
**everything other than `lark` displays as `mail`**. Simply inserting `source: "github"` therefore makes old Apps render it as `mail` and route "Send" to a `mail` reply.

## Goals

Each source implements one **`connector`**, providing capabilities as needed, all optional. The hub discovers what it implements and automatically attaches it to the knowledge base, `Inbox`, assistant (MCP tools) and App.
Adding a source no longer `changes` these pipelines.

Out of scope: source-specific browsing pages (`docs/extensions/feishu.md` ("Do not build another Lark") applies equally to GitHub and Messages); changing existing `sources`' behavior.

## I. `Connector` interface

Code lives in `server/internal/connector` (interfaces, `Registry`, generic `gate` and status), with one subpackage per built-in `connector` (`connector/github`, `connector/imessage`).
During migration, Lark, Moments and `mail` initially stay in `hub` behind adapters to avoid moving too much at once. `connector` depends on `kb` data types (`SourceChat`, `SourceMessage`, `Item`),
not `hub`; hub owns the `Registry`.

The core is identity only:

```go
type Connector interface {
    ID() string   // "lark" "mail" "github" "imessage": config key, MCP tool prefix, InboxItem.Source
    Name() string // Human-readable: Lark / Mail / GitHub / Messages
}
```

Other capabilities are discovered through interface assertions (the same approach as optional capabilities in `host`):

### 1. `Accounts` and health (`Accounts`)

```go
type Status struct {
    State     string    // ok | not_configured | not_signed_in | no_permission | rate_limited | error | disabled (turned off by the user, #533)
    Account   string    // Displayed account name (Lark name, GitHub login, "Messages on this Mac")
    Error     string    // Human-readable sentence ("Lark gateway is not signed in", "Full Disk Access is missing")
    LastSync  time.Time
    LastEvent time.Time
}
type Accounts interface {
    Status(ctx context.Context) Status
}
// Connectors requiring user-provided credentials also implement:
type Credentials interface {
    Fields() []CredentialField           // Required fields (GitHub: token, optional base_url)
    Verify(ctx context.Context, v map[string]string) (account string, err error) // Try before storing
}
```

- Credentials live on the hub: AES-GCM encryption with `secret.key` (the same master key as launcher keys and `mail` credentials, `internal/secretkey`), in a new Postgres table
  `connector_accounts(connector, account, config jsonb, secret_enc text, created_at, updated_at)`; check every worktree before selecting a migration number.
  Plaintext exists only in memory and is never returned by an API (only "configured" and the account name). A hub.json `<connector>.token_file` (0600) is also available for people who do not want to use the App.
- States such as `not_signed_in` appear consistently in Settings › Connectors, the `Inbox`'s top `error` bar, and assistant snapshots (it will not call tools when "GitHub is not signed in").
  In the App (`ConnectorText.state` / `tone`), only `error` says "Something went wrong"; red dots are only for `error` and `no_permission`. Disabled ("Off"), unconfigured and unsigned-in states have grey dots;
  states unknown to the App (added by a newer hub) say "Unknown status" with a grey dot, not an error.
  Programs run by the hub (Telegram), on hubs before #669, cannot list capabilities while off and never started (empty list; capabilities self-reported by the program; `since` #669 the hub lists static declarations first):
  the App still offers a toggle, does not say "configure in hub.json", and polls every second after enabling until the program starts and reports `login` (at most 15 seconds).
- Lark's `lark.ErrNotLoggedIn` → `not_signed_in`; keep `/api/v1/lark/status` for old Apps, reading the same `Status`.

### 2. `Conversations` (`Conversations`) and backfill (`Backfill`)

Increments have two natural units, both supported:

```go
// Per-conversation cursor (Lark, Moments): today's kb.Source + kb.Backfiller, moved unchanged.
type ChatSource interface {
    Chats(ctx, limit) ([]kb.SourceChat, error)
    Messages(ctx, chat kb.SourceChat, after string, limit int) (msgs []kb.SourceMessage, newer string, more bool, err error)
}
type Backfiller interface {
    Older(ctx, chat kb.SourceChat, before string, limit int) ([]kb.SourceMessage, string, bool, error)
}

// Per-account cursor (mail UID, iMessage ROWID, GitHub updated_at): new messages from all conversations since the cursor in one call.
type StreamSource interface {
    Changes(ctx, cursor string, limit int) (batches []ChatBatch, next string, more bool, err error)
}
type ChatBatch struct {
    Chat     kb.SourceChat
    Messages []kb.SourceMessage // Oldest first
}

type Attachments interface { Download(ctx, media, dst string) error } // Extract Download from today's kb.Source
```

- A `connector` may expose several conversation `sources` (Lark `connector` = Lark conversations + Moments posts), hence `ChatSources() []ChatSource` / `Streams() []StreamSource`.
  Each source has its own `Name()` (`lark`, `moments`), the knowledge-base id prefix (`chats/lark-…`, `chats/moments-…`), which **must not change during migration**.
- Stream cursors live in the knowledge-base directory at `cursors/<connector>-<stream>.json` (migrate today's `mail-cursors.json`, read the old file for one version); per-conversation cursors stay in each conversation's `meta.json`.
- `kb.Engine.Archive` remains unchanged for `ChatSource`; for `StreamSource`, call `Changes` then `Ingest` each batch. `Backfill` uses only `Backfiller` for both, retaining relevance-based conversation selection.
- Existing behavior to watch: `archiveChat` skips messages "not newer than `LastAt`". Late iCloud-synced older messages and edited GitHub comments can predate `LastAt`;
  the stream path must insert them into the appropriate daily file by `date`, like `Backfill` (`AppendMessages` already splits by day; missing pieces are correct ordering within a day and dedup by id).

### 3. `Items` (`Items`)

Documents, tasks, PRs and calendar events. Move today's `kb.ItemSource` (`Items` + `DocTitle`) unchanged; expand `Item.Kind` beyond fixed doc / meeting / event values to connector-owned kinds
(`pr`, `issue`), in knowledge-base directories `items/<kind>/` (existing `docs/`, `meetings/`, `events/` stay, with unchanged `kind`). Tagging and project association use the same `TagItems`.
Add optional `Links() []LinkPattern`: matching links in messages become items (today `docLink` in `archive.go` is a hardcoded Lark-document regex).

### 4. `Events` (`Events`)

```go
type Events interface {
    // Watch runs until ctx ends, calling changed on updates (scope: chats | inbox | items); the hub restarts errors with backoff.
    Watch(ctx context.Context, changed func(scope string)) error
}
type Poller interface {
    NextPoll() time.Duration // Without live push: the hub polls at this interval (GitHub updates it from X-Poll-Interval each time)
}
```

`changed` is today's `kickInbox` / `h.kb.kick` (debounced). Lark's event WebSocket and iMessage's "`chat.db-wal` `changed`" are `Watch`; `mail` and GitHub are `Poller`.

### 5. `Inbox` (`Inbox`)

The `connector` decides "related to me, needs me", producing a common `InboxItem`:

```go
type Inbox interface {
    // Collect returns things changed this round; open finds the unresolved item for a thread (today's inbox.openFor).
    Collect(ctx context.Context, open func(thread string) *InboxItem) ([]*InboxItem, error)
    // Reply sends user-confirmed text back: a Lark message, email reply, GitHub comment.
    Reply(ctx context.Context, it InboxItem, text string) error
}
```

- `InboxItem.Source` = `connector` `ID()`, no longer hardcoded. Existing `lark` / `mail` values and `Lark` / `Mail` / `Doc` / `Moment` blocks **stay unchanged** (old App and database data depend on them).
- New connectors share a new block `Connector *InboxConnector`: `{thread, label (`kind` label: "Review requested", "@me", "CI failed"), icon, url (opens in-App),
  `open_url` (system-handled schemes such as `sms:`), `reply_to` (one sentence saying where the reply goes), can_reply}`. The App renders only this block without knowing the source.
- Lifecycle (done, `done_by=replied/read`, 3-day / 8-day retention, classification, AI twin, translation) is hub-generic, outside connectors.
- Classification and AI twin prompts select the "`kind` line" by `kind` (`prompts/inbox/item.md`); new-kind descriptions go in `prompts/inbox/kinds/<connector>.md`, not concatenated in Go.
- **Backward compatibility**: `GET /api/v1/inbox` returns only `lark` / `mail` by default; other `sources` require `?sources=all`. Only new Apps/watches send it, so old Apps never render PRs as mail.
  `{"type":"inbox"}` pushes remain unchanged.

### 6. Read tools (`ReadTools`)

```go
type ReadTools interface {
    Tools() []string                                                   // Tool names must start with "<id>_"
    Call(ctx context.Context, name string, args json.RawMessage) (string, error)
}
```

- Descriptions and schemas live in `server/internal/prompts/mcp/<connector>/<tool name>.md` (unchanged format from `docs/assistant/prompts.md` ("MCP tools")), read by `prompts.Tool(name)`.
- The hub tool list comes from `Registry`: only `ok` connectors expose tools (today's two booleans in `assistantTools(withLark, withMail)` are a special case);
  `callTool` / `runAssistantTool` dispatch by prefix. `mcp_tools.*` goldens must remain byte-for-byte identical across migration.
- Read-only tools need no approval under `--allowedTools mcp__persona` (like today's `lark_*`); writes do not belong here.

### 7. Writes (`Actions`)

```go
type Action struct {
    Name    string // "github_comment", "github_review", "imessage_send"
    Visible string // Who sees it ("all participants in example-org/example-repo#12"): required on the card
}
type Actions interface {
    Actions() []string
    // Describe turns parameters into phone-card text (conversation, target, full outgoing text); must work offline (otherwise list raw parameters).
    Describe(ctx context.Context, name string, args json.RawMessage) (Action, string)
    Do(ctx context.Context, name string, args json.RawMessage) (string, error)
}
```

- **One `gate`** (`connector/gate.go`): hub receives a write → checks rules → `allow` runs directly, `deny` refuses with a `reason` returned to the `model`, `confirm` (default)
  uses existing `hub.approve` (`IxPermission` card, 15-minute expiry, "Always `allow` in this session"). Rules are hub.json `connector_gate: [{connector, action, match?, decision}]`,
  first `match` wins, same form as `lark_gate`; behavior of migrated `mail_send` stays unchanged.
- The `Inbox`'s "Send" button itself is confirmation, so `Reply` shows no extra card (as with current Lark / `mail`); the AI twin may only draft (`inbox_draft`), with direct sending still forbidden by its prompt.
- Lark writes remain its gateway skill + node Bash `gate` (read/write is known only from the command line), not moved; unify only rule storage and card appearance.

### 8. `Links` (`Links`)

- Hub side: the `LinkPattern` under "`Items`" above records links from messages as items and associates them with conversations and projects.
- App side: all links already `open` in-App (`LinkSheet` web cards); new connectors **get no native pages**. `GET /api/v1/connectors` includes recognized hostnames
  (e.g. self-hosted GitHub Enterprise domains live only in config, never in the App), which the App uses for card icons and labels such as "Open in GitHub".
  `FeishuLinks` stays unchanged.

### Hub wiring

```text
Registry ──┬─ ChatSources / Streams / Backfiller / Attachments → kb.Engine (archive, backfill, compression, tagging unchanged)
           ├─ Items / LinkPattern                              → kb.CollectItems / TagItems
           ├─ Events / Poller                                  → wake kbLoop, inboxLoop
           ├─ Inbox                                            → collectInbox, replyInbox
           ├─ ReadTools / Actions                              → assistant and Agent MCP tools, Actions gated first
           └─ Accounts / Credentials                           → GET /api/v1/connectors, Settings › Connectors, snapshots
```

New API `GET /api/v1/connectors` → `{connectors:[{id, name, status, account, error, last_sync, capabilities:[…], hosts:[…], credential_fields:[…]}]}`,
`PUT /api/v1/connectors/{id}/credentials`, `DELETE …/credentials`. Add `connectors` to `features`; old Apps omit new rows when they do not see this feature.

## II. Three forms

### Built-in (Go, in the hub)

Lark, Moments, `mail`, GitHub. Implement the interfaces directly, release with `personad`, with prompts in this `repo` enforced by goldens.

### External process (reuse the plugin mechanism, code in another `repo`)

Add `kind: "connector"` to plugins in `docs/extensions/extensions.md`: the hub still starts the process, supplies `PERSONA_PLUGIN_SOCKET`, checks health and restarts with backoff;
remote plugins that only connect to the socket (`ssh -R`) still work. The plugin exposes over the socket:

| Path | Purpose |
| --- | --- |
| `GET /manifest` | `{id, name, capabilities, actions:[{name, visible}], hosts}`: the hub registers capabilities from it; **tools not declared in actions are treated as something other than read-only** (below) |
| `POST /mcp` | MCP (streamable HTTP, JSON-RPC `tools/list`, `tools/call`): AI tools. The hub is an MCP client, prefixes tools with `<id>_` and forwards them to the assistant |
| `GET /status` | `Status` |
| `GET /sync/chats`, `/sync/messages?chat=&after=&limit=`, `/sync/older?…`, `/sync/changes?cursor=&limit=` | `Conversations` / backfill / streams (JSON mapping one-to-one to Go interfaces) |
| `GET /sync/items`, `GET /sync/inbox` | `Items`; `Inbox` candidates (hub owns `open`, merging, lifecycle) |
| `GET /events` | SSE: one `message` per line such as `{"scope":"chats"}`; if absent, poll at manifest `poll_seconds` |
| `GET /media?path=` | Attachments |
| `POST /actions/{name}` | Writes: called by the hub **only after its `gate` allows them** |

**Assessment: feasible, recommended.**

- MCP alone is insufficient: it has resources + subscriptions but no cursor or backfill conventions, and most existing MCP servers do not implement them; the knowledge base needs "increments `since` last time".
  MCP therefore handles only AI tools, with a small REST extension for increments (`/sync/*` above); each does its own simple job.
- The `gate` cannot trust plugins: MCP's `readOnlyHint` is only a hint. The hub directly allows only tools **not** listed in manifest actions with `annotations.readOnlyHint == true`;
  everything else is treated as a gated write. Plugins cannot obtain or bypass user confirmation (App requests never reach plugins; only the hub can call them).
- Benefit: an existing MCP server (e.g. a service's official server) can attach unchanged as a read-tools-only `connector`; add `/sync/*` later for `Inbox` / knowledge base.
- Cost: a new MCP client in the hub (two methods, `tools/list`, `tools/call`, hundreds of lines); external `connector` tool descriptions are outside this `repo`'s `prompts/mcp/` and its goldens,
  an explicit exception to "all prompts live in `prompts/`" (only for code outside this `repo`).
- No first external `connector` exists yet; **specify this protocol now, implement when one actually exists** ("Decisions needed from the user").
- **First-party subprocess form (done, #533)**: connectors in this `repo` with dependencies too large for `personad` (Telegram's gotd) use `connector/serve` (program side)
  and `connector/proc` (hub side): the hub starts the program and speaks the hub↔node JSON-lines RPC over stdin / stdout, mapping each `Connector` capability to a method.
  The program reports a manifest at startup (id, name, capabilities, `Capabler`); encrypted storage is handled by reverse `secrets.*` calls to the hub (bound to this `connector`). This does not conflict with the third-party HTTP + MCP design above.

### Third form: node as a local `connector` (data on Mac)

Some data exists only on a computer (iMessage `chat.db`, future local notes, photos…), inaccessible to the hub. The node already connects to the hub over ssh with bidirectional RPC,
so it reads local data while the hub registers a `connector` (`connector/imessage/remote.go`: an RPC-forwarding adapter).

New hub → node methods (old nodes return `unknown method`, treated as no local connectors):

| method | params → result |
| --- | --- |
| `connector.list` | → `{connectors:[{id, status, error}]}`: what this machine provides and whether it works (e.g. missing Full Disk Access is `no_permission`) |
| `connector.changes` | `{id, stream, cursor, limit}` → `{batches, next, more}` |
| `connector.older` | `{id, chat, before, limit}` → `{messages, before, more}` |
| `connector.media` | `{id, path, offset}` → chunked reads (like `file.read`, 20 MB maximum per item) |
| `connector.action` | `{id, name, args, approval}` → `{result}`: sent only after hub gating; `approval` is the card id (for node logging) |
| `credential.gh_token` | `{host}` → `{token}`: runs `gh auth token` for GitHub `from_machine` ("IV · Credentials"); returned only to the hub, never logged |

node → hub event: `connector.changed {id, scope}`.

A `connector` uses only **one** machine (configured as `imessage.machine`), not several Macs (iCloud data is the same; reading twice only duplicates it).

## III. Migration: move Lark, Moments and `mail` to new interfaces (refactor only)

One PR per step, with the same checks before merging:

- **All existing tests pass**: `./scripts/test/test-server.sh` (hub machine, real Postgres), `go test ./...`, App `PersonaTests` and `Inbox` / knowledge-base UI tests.
- **Byte-identical goldens**: prompts, MCP tool lists (`mcp_tools.*`, `mail_tools`).
- **New comparison tests** (write in M0, baseline generated with old code): fake Lark gateway + fake `mail` (synthetic data), one archive + `Inbox` collection round,
  saving results (knowledge-base directory tree and every file's content, `InboxItem` JSON) as goldens; every subsequent step must match.
- **Real-data comparison** (read-only, print numbers only): `personad dev connector-diff` (DEBUG builds / tests only), using real Lark gateway and `mail` on the hub machine;
  run old and new code once each into two temporary directories (no touching `~/.persona/data`, database writes or marking read), comparing each conversation's line count/content hash and `Inbox` item id / `kind` / thread ownership.
  Print only "`match` / conversation N differs, by N lines", not content. Delete temporary directories afterwards.

| Step | Work | Acceptance (in addition to above) |
| --- | --- | --- |
| M0 | Comparison tests and `connector-diff`; baseline from current master | Commit synthetic baseline; real comparison of current code against itself matches |
| M1 ✓ | (#529) Add `Chats{ChatSources() []kb.Source}`, `Items{ItemSource()}` to `connector`; Lark `connector` (hub/lark_connector.go) wraps `larkSource`, `momentsSource`, `larkItems`; `kbSources()` / `kbCollectItems()` use Registry. Tools dispatch only to connectors that actually provide them (`Registry.Offers`), so `lark` does not `handle` `lark_*` before M4; `/api/v1/connectors` omits `lark` / `mail` before M6. **Departure from plan**: Lark event WebSocket is not wrapped as `connector` `Events`; it also pushes the App's Messages tab, refreshes personal Agents and starts /agent immediately, so remains in the hub. Original plan: add `connector` and `Registry`; Lark `connector` wraps `larkSource` + `momentsSource` + `larkItems` + event WebSocket; `kb.Engine.Sources` comes from `Registry` | Knowledge-base id prefixes unchanged; real comparison matches |
| M2 ✓ | (#544) `Mail` `connector` (`extmgr/mail_connector.go`) implements `connector.Archive`; add optional `Poller` (`mail` 6 minutes), remove `kbMail`; only cursor-file location `changes` in the baseline. Original plan: `mail` implements `StreamSource` (UID cursor), remove shell `mailSource` and `kbMail` loop; move `mail-cursors.json` to `cursors/`, reading old file compatibly | Same `mail` batch produces identical conversations/lines; no cursor regression (no duplicate archive) |
| M3 ✓ | (#548) Lark/mail items keep their own blocks and use hub-internal `ownInbox{collectInbox, replyInbox}` (hub/inbox_own.go, implemented by their connectors); `collectInbox` collects in `Registry` order, replace the `replyInbox` `switch` with ownership-based `connector` dispatch; add optional `connector.InboxReplier{ReplyItem}` (`Reply` signature unchanged, coordinated with terminal lane); queue each reply in the hub until archive completes. Original plan: wrap `collectLark` / `collectDocs` / `collectMoments` / `collectMail` as `Inbox`; replace the `replyInbox` `switch` with `Reply`; add `?sources=` | `Inbox` comparison matches; old Apps without `sources` see the same list |
| M4 ✓ | (#557) Lark `connector` provides `lark_chats/messages/search` (assistant only); `mail` provides `mail_accounts/search/read` and gated `mail_send` (same card; optional `AgentTools`, `Validator`). Tool lists concatenate phone, Agent-exposed connectors, assistant-owned tools, assistant-only connectors; three goldens unchanged; `lark_agent_reply` remains in hub. Declined `mail` now gives the `model` the generic "The user declined mail_send." Original plan: register `lark_*`, `mail_*` from `Registry` and dispatch `callTool` by prefix | Three tool-list goldens byte-identical; `TestEveryMCPToolIsUsed` passes |
| M5 ✓ | (#605) Since M4, `mail_send` is gated by `Registry.Call` through `connector_gate`; move decision + approval into `connector/gate.go` (`gate`). `connector:"lark"` rules in `connector_gate` govern assistant Lark writes; `action` is "method path" (`"POST /conversations/*/messages"`, `"CLI /im/+messages-send"`, `"*"`), `match` matches body. Hub combines these before old `lark_gate` (`connector.LarkRules`) and passes them to node through `assistant.lark`; node/protocol unchanged. Both original node/hub card types and text unchanged. Original plan: move `mail_send` approval and `lark_gate` rule reading into `connector/gate.go`, add `connector_gate` | Card text unchanged (code untouched, no screenshots needed); old `lark_gate` config works (unit tests) |
| M6 ✓ | (#609) `GET /api/v1/connectors?all=1` includes Lark/mail (without it, still omitted: pre-M6 Apps would duplicate their rows). App Settings › Connectors draws the section from this list; `lark` / `mail` retain original Lark/mail rows (same titles, icons, trailing text and destinations, status still from their APIs); old hubs omitting them retain original rendering. `Status` gains `guide` (`Status.Guide`, supplied by Messages when permissions are missing), shown on generic `connector` `error` pages. Original plan: `GET /api/v1/connectors`; use it for new settings rows, keep mail/Lark rows unchanged | `/api/v1/lark/status` remains; dark/light settings screenshots `match` before, except added rows |

After merging and deploying M1–M6, watch hub logs for a day (archive lines, `Inbox` items and classifications roughly comparable to the day before), then add new sources.

## IV. GitHub `connector` (first new source, built-in)

### `Inbox`: what "needs me"

Poll `GET /notifications` (unread only, without `participating`, using `reason`); one notification thread = one thing (`github:<thread id>:<updated_at>`).
Later activity merges into the unresolved item; activity after completion opens a new item (same rule as Lark conversations).

| Situation | Detection | Default classification |
| --- | --- | --- |
| PR waiting for my review | `reason = review_requested`, and I remain in requested reviewers (for team requests, check my team membership) | Needs you |
| @ me | `mention`; `team_mention` defaults to FYI | Needs you |
| New `comment` / review on my PR | `author` + `subject.type = PullRequest`, latest entry not mine | Model decides ("please change this" needs you, "LGTM" is FYI) |
| CI failed on my PR | `ci_activity` (`Actions` run I triggered finished), then check the commit's check runs for failure; also check head commits of my `open` PRs (CI triggered by someone else's push sends no notification) | Needs you |
| Issue assigned to me | `assign` | Needs you |
| Other (`subscribed`, `state_change`, `comment`, etc.) | Not in `Inbox`; knowledge base only, if the thread is already there | — |

- Replied on GitHub (latest `comment` is mine and later than this item) → `done_by=replied`; PR merged / closed, or review request withdrawn → `done_by=read` (items not needing you are simply completed).
- **Done is recorded only in Persona, not written back as read on GitHub** (same as Lark/mail; possible change under "Decisions needed from the user").
- `Reply`: `comment` on PR / `issue` (`POST /repos/{o}/{r}/issues/{n}/comments`); `reply_to` says "Comment on `example-org/example-repo`#12 (visible to everyone with repository access)".
- App row: GitHub logo (`Theme.Brand.github`, official asset to add), label ("Review requested", "@me", "CI failed", "Assigned to you"), PR / `issue` title,
  second line ✦ summary, third "repository #number · latest commenter: content". Opens generic detail: context (`Context` from hub: description, recent comments / reviews, failed check names)
  plus "Open in GitHub" (in-App web card) and actions (AI draft reply / hand to AI / done). **No** PR browser, diff page or repository page.

### Knowledge base

- **`Conversations`**: one per PR / `issue`, `chats/github-<owner>-<repo>-<number>` (`kind: pr | issue`), description first, then `issue` comments, reviews ("`alice` approved", "bob requested `changes`: …"),
  inline comments (with file:line), chronologically ordered. Bots (`[bot]` accounts, CI comments) are marked as bots and excluded from real people by `HasPeople`.
- **What is collected**: only `threads` "related to me" (seen in notifications + search `involves:@me` updated in the last 30 days), not entire repositories. Cursor uses thread `updated_at`
  + `since` per `comment` type (`StreamSource`); backfill reads all comments on first fetch (like Moments, no `Older` needed).
- **Repositories link to projects**: project `dirs` (`<machine>:<path>`) are code directories; node `git.status` gains optional `remotes` (skip old nodes without it).
  The hub matches `github.com/<owner>/<repo>` (or configured GHE `host`) to projects, storing `repos` in project `meta.json`. PR conversation compression gets the project directly (no guessing),
  with timeline entries "GitHub · PR #number · one sentence". Unmatched repositories still use content-based `model` association.
- PR / `issue` links in messages (Lark, `mail`) become items (`LinkPattern`) associated with that conversation's project.

### AI tools

| Tool | Read / write | Purpose |
| --- | --- | --- |
| `github_search` | Read | Search issues / PRs (GitHub syntax, default `involves:@me` suggested in description), search code |
| `github_read` | Read | PR / `issue` description, state, reviewers, comments and reviews (truncated, newest first) |
| `github_diff` | Read | PR changed-file list + selected file patch (truncated per file; oversized files get statistics only) |
| `github_checks` | Read | Commit / PR checks; last N KB of failed job logs (`Actions` logs API) |
| `github_comment` | Write, gated | Comment on PR / `issue`; card states repository, number, title, full text, "visible to everyone with repository access" |
| `github_review` | Write, gated | approve / request `changes` / `comment`; approval card gets special emphasis |

Descriptions in `prompts/mcp/github/`; add a "with GitHub" tool-list golden.

### `Events`: polling

The hub has no public entry point, so cannot receive webhooks. `GET /notifications` sends the previous `Last-Modified` verbatim as `If-Modified-Since`;
304s do not count against rate limits. Request intervals follow `X-Poll-Interval` (usually 60 seconds, longer when GitHub is busy; mandatory), updating `Poller.NextPoll` each time.
Fetch corresponding PRs / issues / check runs only for new notifications (at most 30 `threads` per round, rest next round). Below 10% `X-RateLimit-Remaining`, status becomes `rate_limited`
and only notification polling remains. That is the hour's core quota (`X-RateLimit-Resource: core`, or no such header); the search API's
per-minute quota is kept apart and below 10% only pauses the knowledge-base archive (`Changes`) until it resets.

### Conversation page (#1055)

User request: "Make the github & Telegram UI richer, based on the Lark conversation page and GitHub thread page." Opening an `Inbox` PR / `issue`
now shows a native GitHub thread page rather than hub-collected plain text (`apple/Persona/Views/Inbox/ConnectorThread/GitHubThreadView.swift`).
It shares the `connector` `threads` capability with Telegram (`docs/extensions/connectors.md`); the App selects this page for `ThreadChat.Kind` `pr` / issue.

- **Page**:
  - Header card: repository · #number, title, state (`open` green, merged purple, closed PR red, closed `issue` purple, draft grey), `author` avatar, `head → base`, labels (their own background color, primary text color so light labels remain readable), "Open in GitHub".
  - Description: Markdown, treated as the first comment.
  - Timeline (oldest first):
    - comments;
    - reviews with colored states: approval green, `changes` requested red, a state-colored stripe at the card's left;
    - inline comments: `file:line` with the diff's last 6 lines above; replies show a quote, not repeated diffs;
    - CI checks: consecutive checks merge into one card, e.g. "3 checks · 1 failed", with the first failure's log tail (last 40 lines) expanded by default;
    - grey event rows: labels added, review requested, title `changed`, merged, closed, force-pushed…
  - Earlier activity: "Show earlier activity" between description and timeline, like GitHub's Load more.
  - `Reply`: bottom composer; long-press "Quote reply" turns a `comment` into a Markdown quote (no @, to avoid notifying its `author`).
    Sending uses `github_comment` (accepts both `{chat, text}` and the assistant's `{repo, number, body}`). User sends `comment` directly, without gate/card (#1190); assistant calls remain gated.
  - "AI draft reply" and "Hand to AI" stay in the `Inbox` top bar. CI-failure notifications that are not PRs/issues still show hub-collected text.
- **Data** (`server/internal/connector/github/thread.go`):
  - Each read fetches `issue`, comments, events; PRs additionally fetch PR, reviews, inline comments; `open` PRs additionally fetch head-commit check runs. Each list capped at 5 pages × 100 entries.
  - Merge chronologically, paginate in memory; cursor is the oldest entry's id from the previous page.
- **Cache**:
  - Keep each API response and ETag in memory (maximum 400, least recently used evicted), send `If-None-Match` next time; 304s do not count against rate limits.
  - Re-reading an API within 15 seconds (paging backward) makes no request.
  - Failed-job logs are read once per job id.
  - Posting a comment/review clears that PR/issue's cache so refresh shows it.
- **Avatars**: media ref `avatar:<login>:<v>` (`v` is the version in GitHub's avatar URL); hub fetches 96-pixel images from GitHub's avatar domain without a token.
  After a hub restart, a previously unseen `login` first requires `/users/<login>`.

### Credentials

**Decided (user, 2026-11-01): read a machine's `gh` login.** Original request: "Can GitHub automatically read authorization from `gh` cli on a selected machine?"

- **Config**: hub.json `"github": {"auth": {"from_machine": "<machine id>", "host": "github.com"}}` (`host` optional, default github.com; self-hosted GitHub Enterprise hostname only in config).
- **Get token**: new node RPC `credential.gh_token {host}` → `{token}`: noninteractive `gh auth token [-h <host>]` on that machine.
  - Hub only: hub → node RPC over their ssh channel; no REST endpoint; hook / MCP `node.sock` does not recognize it, so assistant/Agents cannot obtain it.
  - `gh` `login` in `~/.config/gh/hosts.yml` is readable over ssh; keychain `login` is not because ssh cannot unlock it. Report "This machine's `gh` `login` is in the keychain and cannot be read over ssh",
    with two alternatives: `gh auth login --insecure-storage` on that machine, or manual paste. Old nodes report "`personad` on <machine> is too old".
- **Token only in hub memory**: no database, logs, responses or errors (redact token-shaped strings in errors); not in assistant/Agent environment variables either.
  - On 401, fetch again once (the machine may have just logged in again); another 401 gives `not_signed_in`: "`gh` `login` on <machine> has expired; run `gh auth login` there".
  - Machine offline: use the last token in memory; after hub restart, if still offline and no token cached, state is "GitHub not authorized (<machine> offline)"; fetch automatically on reconnection.
- **App**: Settings › Connectors › GitHub says "`gh` `login` from <machine> · <account>" (account from `GET /user`).
- **Fallback: manually pasted token** (encrypted on hub, "I. `Accounts` and health"). Used without `from_machine`; if both are configured, `from_machine` wins.
- **Explain permissions**: `gh` tokens are powerful (measured scopes `gist`, `read:org`, `repo`, `workflow`: can push code and change CI). The `connector` only calls read APIs;
  its only writes are gated `github_comment` and `github_review`; no git pushes, `workflow` `changes` or reuse by other connectors.
- **Main-session measurement (2026-11-01, no token printed)**: `gh` on two Macs was logged into github.com as the same account, with tokens in `hosts.yml`, readable through noninteractive ssh `gh auth token`
  (40 characters); a third Mac's `login` had expired (which machine is recorded in the lane report). `/notifications` with this token returned 200,
  `X-Accepted-OAuth-Scopes` was `notifications, repo` (`repo` suffices), `X-Poll-Interval` 60; search `review-requested:@me` also returned 200.

Why the two earlier token methods were dropped: notifications supports only classic PAT and OAuth tokens (not fine-grained PAT or GitHub App tokens); `gh` yields an OAuth token,
and `repo` is sufficient, so users need not generate another token.

## V. iMessage `connector` (second, local to Mac)

### Findings (2026-11-01, checked file existence and row counts only, no content read)

- Three Macs connected to the hub have `~/Library/Messages/chat.db`, all on macOS 26. Two have identical `message`, conversation and `attachment` counts (same Apple ID with iCloud Messages sync);
  the third has far fewer, with its latest `message` over half a month old. Machine identities are in the lane report, not the repo.
- Processes started over ssh can read `chat.db` on all three: `sshd-keygen-wrapper` already has Full Disk Access ("Remote Login › Allow full disk access for remote users"),
  see the TCC lesson in `docs/lessons/server.md`.
- Roughly half the messages have empty `text` and only `attributedBody`; without typedstream parsing, half are lost.
- `date` is nanoseconds `since` 2001-01-01 (seconds on older systems; distinguish by magnitude). Conversation services are iMessage, SMS and RCS, mostly SMS (verification codes and notifications).

### Full Disk Access (TCC)

`chat.db` is TCC-protected; permission belongs to the **responsible process**, not the binary reading the file:

- For `ssh … personad node --stdio` started by the hub, the responsible process is `sshd-keygen-wrapper`. Granting it Full Disk Access grants **every ssh session on this Mac**,
  not just personad. These Macs already do this (enabled earlier for Desktop and Documents).
- Granting `personad` itself does not help: ssh children still belong to sshd; `personad`'s binary also `changes` on every deployment with ad-hoc signing, invalidating permission.
- The smallest, most stable approach is a **separate helper**: `personad messages-helper`, run by the user's LaunchAgent (started by `launchd`, responsible for itself),
  exposing only "read `chat.db` / contacts, fetch attachments" on a local unix socket for the node. Only the helper gets permission; signing must be stable (Developer ID, recognized by team ID, preserving permissions across versions),
  so it releases separately rather than with every `personad` deployment.
- Recommendation: **use existing ssh permission in phase one** (already enabled, zero setup; `connector.list` reports `no_permission` and guidance on failure),
  defer the helper to sending (which needs Automation permission, also granted in a graphical session). User decision needed.

### Reading `chat.db`

Hand to AI (#528, user 2026-11-03: "iMessage text can be given to AI, like Lark"): node `connector.changes` returns new messages incrementally by `ROWID` (first fetch starts 30 days back), `connector.search` supports assistant search;
by default, expose only conversations with a contact-book member or in which I have spoken (exclude unsaved short-code senders such as codes, banks and delivery services). Redact verification codes in AI text, not in App-only viewing.
Hub side (`connector/imessage/persona.go`): with "Share with Persona" enabled (hub setting `imessage.ai`, on by default), implement `Archive` (knowledge-base conversations `imessage-<conversation hash>`, people `imessage-<number hash>`),
`Inbox` (check recent conversations every 2 minutes: unread enters `Inbox`, direct chats need you, groups are FYI; reading elsewhere or my reply completes it; "`Reply` in Messages" is an `sms:` link, Persona does not send),
`ReadTools` (`imessage_chats` / `imessage_read` / `imessage_search`). Disabled means none are exposed; stored data is deleted separately: `DELETE /api/v1/imessage/data` (only while disabled) calls `kb.Engine.ForgetSource("imessage")`
to remove conversations, people, project references and their timeline entries, cursors (projects marked for rewriting, summaries rebuilt from remaining data), then index rows and `Inbox` items. Deleted conversations under compression cannot be written back.

Implemented (#449, `server/internal/connector/imessage`, node `connector.*` RPC in `docs/protocol/protocol.md`): merge conversations by person (multiple iMessage / SMS / RCS `chat`s for one number form one conversation; groups use `guid`),
with hashed ids; contact matching uses the last 10 phone digits, then last 7 if unique. On missing permission, `connector.list` gives guidance by node execution `model`: ssh requires "Remote Login › Allow full disk access for remote users";
launchd-resident node (`scripts/deploy/node-launchd.sh`) requires permission for `personad` itself (deployment replaces the binary, possibly requiring reauthorization; the helper step fixes this).
Fixtures are `testdata/home` (synthetic `chat.db` and contacts, macOS 26 schemas; generated by `blobs.swift` + `make.py` in `testdata/gen`); real data is read only by `TestRealMessagesCounts`, printing counts.

- Read-only `open`: `sqlite3 -readonly -json` (macOS `/usr/bin/sqlite3`; `personad` cross-compiles with `CGO_ENABLED=0`, no SQLite driver;
  fetch `attributedBody` with `hex()`). No `immutable=1` (would miss new WAL messages). Do not copy or cache the whole database.
- Tables used: `message` (`ROWID`, `guid`, `text`, `attributedBody`, `handle_id`, `is_from_me`, `date`, `date_read`, `is_read`, `cache_has_attachments`,
  `associated_message_type` / `associated_message_guid` (tapbacks), `thread_originator_guid` (inline replies), `date_edited`, `date_retracted`, `item_type` (group events)),
  `chat` (`guid`, `chat_identifier`, `display_name`, `style`, `service_name`), `handle`, `chat_message_join`, `chat_handle_join`, `attachment`, `message_attachment_join`.
- **Incremental cursor: `ROWID`**. Monotonically increasing; late iCloud messages get new ROWIDs, so none are missed. `Archive` into daily files by `date`
  ("Existing behavior to watch" above). `date` cannot be the cursor: late old messages would be skipped. Phase one does not track edits/recalls (append-only archive, like Lark);
  later, if needed, inspect `changed` `date_edited` / `date_retracted` in the last 7 days each round.
- **typedstream** (`attributedBody`): `NSArchiver` streamtyped format (starts `\x04\x0bstreamtyped`), containing `NSAttributedString`; its first `NSString` is the body.
  Write a minimal decoder (`connector/imessage/typedstream.go`): read class chains and objects, extract first string (length prefix: one byte below 0x80;
  2 bytes after `0x81`, 4 after `0x82`, little-endian), plus `__kIMMentionConfirmedMention` (mentions) and links in attributes.
  Unparseable messages get one line `[无法解析的消息]` (unable to parse message) and a counter (shown in status); **never print or log raw data**.
- Fixtures: a small Swift script on Mac (`NSArchiver` archiving `NSAttributedString`, synthetic body `示例消息` (example message)) generates blobs (plain text, long text, mentions, links, emoji),
  committed to `testdata/` with a synthetic `chat.db` containing only used tables; generator scripts also committed.
- Tapbacks (`associated_message_type` 2000–3006) are not messages and are skipped in phase one; inline replies include the start of the referenced message (`回复「…」：`, "Reply to ‘…’: "); group events (rename, added people) become system lines.

### Attachments

- Phase one **records only a placeholder line**: `[图片]` (image), `[文件 <name> · <size>]` (file, name · size), `[语音]` (voice); no file copies.
- Phase two (user approval needed): images copied through `connector.media` into hub `attachments/<date>/` (HEIC converted to JPEG on node with `sips`), maximum 20 MB each, deleted after 30 days like Lark;
  files, voice and video remain placeholders. `attachment.filename` is a path under `~/Library/Messages/Attachments/…`; `allow` only that directory (prevent induced reads elsewhere).

### Group and contact names

`handle.id` contains only phone / email, and groups may lack names. Two options:

- **Mac contacts**: `~/Library/Application Support/AddressBook/Sources/*/AddressBook-v22.abcddb` on the same Mac (also SQLite, same Full Disk Access),
  `match` names after normalizing phone numbers (remove spaces, `+86`). Works offline, same Apple ID as Messages.
- **Phone `phone_contacts`**: existing tool, but needs the phone online (or woken by silent push), unsuitable for dozens of bulk lookups.
- Recommend Mac contacts first, then one `phone_contacts` lookup for unmatched numbers during compression, cached on the knowledge-base person (`people/imessage-<number hash>`).
  Unnamed groups use joined member names ("Alice, Bob and 2 others").

### Identity and ids

- Conversation id is a hash of `chat.guid` (`imessage-<first 12 sha256 characters>`), **not** phone / `chat_identifier`: no numbers in directory names, logs or URLs. Same for people.
- Numbers live only in `meta.json` (the hub's knowledge base, already a place for personal data); logs contain only ids.

### `Inbox`: what "needs me"

- Direct messages from someone else that I have not answered (latest entry not mine); group messages mentioning me. One unresolved item per conversation, same rules as Lark.
- Read on iPhone / Mac (`is_read`) and not Needs you → `done_by=read`; my reply → `done_by=replied`.
- Replies in phase one **are not sent from Mac**: App "Send" becomes "`Reply` in Messages", opening `sms:<number>&body=<draft>` (system Messages, prefilled draft, user taps send); no reliable group-chat URL, only "Open Messages".
  The hub supplies `open_url` (numbers stay out of App logs).
- Phase two: `imessage_send` (AppleScript `tell application "Messages"`), **gated**, card states recipient (name + last four phone digits), full text;
  requires Automation permission (control Messages), granted once in a graphical session, so done with the helper.

### `Events`

Node watches `chat.db-wal` writes with kqueue (no database polling), debounces 3 seconds then pushes `connector.changed`; additionally checks `max(ROWID)` every 5 minutes as fallback.

## VI. App side

Follow "do not build another source App": no GitHub browsing pages (repository list, PR page, diff page). **Messages is an exception** (user 2026-11-02: "Start with the smallest usable App version: conversation list and `message` viewing"):
messages are not shared with AI by default (`imessage.model` off); users can only view them in the App, hence a read-only conversation list/viewer through the hub reading the Mac's `chat.db`, without copying messages to the hub.
Implemented (#449): Settings › Connectors › Messages (which Mac, status, step-by-step Full Disk Access guidance according to its node execution `model`, "Check again") › `Conversations` (newest first, unread counts, search names/numbers) › conversation
(like Messages: timestamps after an hour or day change, sender names when group speakers change, SMS / iMessage `changes` marked, replies include referenced openings, descriptive `attachment` placeholders, edits/recalls/unreadable messages explained);
`Views/Messages/`, DEBUG `-openConnectorMessages YES [-openMessagesChats YES]`, `-openMessagesChat <conversation id>`, mock `MOCK_IMESSAGE=denied`, UI tests `MessagesConnectorTests`. Otherwise only three areas:

1. **`Inbox` items**: generic `InboxConnector` block renders rows/details (as described under GitHub/iMessage above); filters add GitHub/Messages from `GET /api/v1/connectors`;
   watch Needs you also displays them (label + title + summary). App list requests include `?sources=all`. Android/web excluded this round; without the parameter they receive no new sources.
2. **In-App links**: GitHub links already use `LinkSheet` web cards, unchanged; card title bars show the GitHub logo according to `hosts`. iMessage has only `sms:`, no links.
3. **Settings › Connectors status**: below existing "This iPhone / `Mail` / Lark", add rows from `/api/v1/connectors`:
   - GitHub: state, account, pasted token (instructions for methods A / B, `SecureField`, only "Configured" after saving), `base_url`, last sync, remaining rate limit.
   - Messages: source Mac, permissions (step-by-step setup when missing), conversations to collect (contacts only / all), exclusions, retention days.
   - DEBUG entries `openConnectorGitHub`, `openConnectorMessages` (like `openConnectorFeishu`; names cannot clash with Swift identifiers and must be added to `release.sh`).
   - Mock hub (`scripts/mock/mock_hub.py`) adds two connectors and synthetic items; use it for UI tests and dark/light screenshots.

## VII. Privacy

- **Synthetic data only in the `repo`**: no real messages, PR content, people, repositories, phone numbers, emails or GHE domains in tests, goldens, screenshots or commit messages.
  GitHub uses `example-org/example-repo`, `alice`; Messages uses reserved numbers such as `+1 555 0100` and "Example `message`". Run `scripts/privacy_check.py` as usual,
  adding rules for realistic phone-number strings and `github.com/<non-example organization>`.
- **Logs**: no `message` bodies, numbers or tokens; only `connector`, id, counts, `error` types. `connector-diff` prints numbers only.
- **GitHub**: encrypted token on hub, never returned to clients; collect only related `threads`; all writes gated.
- **iMessage is the most sensitive personal data**, off by default; enabling requires settings for each item:
  - **Knowledge-base scope**: default to direct/group chats with **people in contacts** (automatically exclude short-code verification, bank, delivery and marketing senders); optionally All or Allowlist only.
    hub.json / settings `imessage: {machine, mode: contacts|all|allowlist, include: [], exclude: [], text_days, attachments: false, model: false}` (`model` off by default, decision 7);
    exclusions by conversation (`Inbox` detail "Stop collecting this conversation" adds it too); **adding an exclusion immediately deletes archived content**.
  - **Verification codes**: even in included conversations, replace code-like digits (4–8 digits near `验证码` (verification code) or `code`) with "••••" before storing.
  - **Retention**: text follows `kb.text_days` (365 days) by default; may be shorter independently (recommend 90 days, see decisions); attachments not stored in phase one.
  - **Model access**: compression, tagging, classification and AI twin send text to the hub-configured `model` (`kb.ai` / `ai`, possibly third-party). With `model: false`, archive and search only,
    no compression, `Inbox` `model` classification (rule fallback), or snapshots.
  - Reads are read-only: no marking read, deleting messages or writing `chat.db`.

## VIII. Implementation steps and acceptance

| Step | Contents | Acceptance |
| --- | --- | --- |
| P0 | This plan | User approval; decisions resolved |
| C1 ✓ | Framework (#414): `connector` (`Connector`, optional `Accounts` / `ReadTools` / `Actions` / `Events` / `Hosts`, `Registry`, `connector_gate`); hub owns `Registry`, `GET /api/v1/connectors`, `connectors` feature; `connector` tools assistant-only, prefix dispatch, gated writes defaulting to phone cards; `Events` wakes `Inbox` / knowledge base. Add `Inbox`, `StreamSource`, credentials, `Poller` with their first consumer (G1–G4) | No connectors registered: MCP golden byte-identical; fake-connector unit tests (tools only from `ok`, writes wait for cards, `allow` / `deny`, `Watch` backoff/restart) |
| M0–M6 | Migration (section III) | Each step: existing tests + byte-identical goldens + synthetic and real comparisons; comparable logs for a day after deployment |
| G1 ✓ | GitHub (#421): `credential.gh_token` RPC, `from_machine` token in memory, ~~paste fallback~~ (defer until someone actually does not use `gh`), status, `/api/v1/connectors`, GitHub settings row | "`gh` `login` from <machine> · account"; cached token while offline, "not authorized (<machine> offline)" after hub restart, automatic recovery on connect; refetch once on 401; old node reports too old; grep confirms no token in database/logs/responses/assistant environment; fake pasted token errors; dark/light settings screenshots |
| G2 ✓ | Read tools `github_search/read/diff/checks` | Goldens; mock GitHub synthetic-JSON unit tests; read a user's own PR with their real token on hub, inspect counts/truncation only |
| G3 ✓ | Notification polling + `Inbox` (four situations) + `?sources=all` + generic App row/detail | Synthetic notification tests for every `reason`, merge / new item, `done_by`; fake-server counts verify 304 and `X-Poll-Interval`; old App list unchanged; new App screenshots |
| G4 ✓ | Knowledge base (#476): PR / `issue` conversations (`connector.Archive`, cursor `cursors/<id>.json`). Defer project matching through `remotes` and link items (G4b): on 2026-11-02 none of 40 active hub projects had `dirs`, so even matching could not attach; links already appear in compression context. Wait for widespread project `dirs` | Synthetic archive golden; real PR archive line/comment counts `match`, numbers only |
| G5 ✓ | Writes (#480; live test awaits user in their own test `repo`): gated `github_comment`, `github_review`; `Inbox` Send = `comment` | `confirm` / `allow` / `deny` tests; user tests a `comment` in their own test `repo` (card screenshots only synthetic test-repo content) |
| I0 | Confirm permissions and scale on selected Mac (read-only counts, partly done) | Report identifies machine, permissions, `message` volume |
| I1 | Node: `connector.list/changes`, typedstream, `ROWID`, kqueue (#449 first implemented `connector.list` / `connector.chats` / `connector.older`, typedstream, Mac contact-name matching for App conversation/message viewing; defer `changes`/kqueue to knowledge-base ingestion) | Synthetic `chat.db`/blob tests, including decode failure; old hub ↔ new node and new hub ↔ old node do not `error` |
| I2 | Hub: remote adapter, archive (contacts only), names, code redaction, delete on exclusion (#449 initially implemented hub.json `imessage.machine`, status/permission guidance `GET /api/v1/imessage`, two read-only App APIs, no storage/model; archive awaits AI-switch decision) | One real-Mac round prints only conversations, lines, exclusions, decode failures; user spot-checks in App |
| I3 | `Inbox` + `sms:` reply + Messages settings row (#449 initially implemented row and conversation/message viewing; `Inbox` awaits AI-switch decision, using G3 `InboxThing.OpenURL` and generic block `open_url` button) | Synthetic unit tests, mock screenshots; real-device `Reply` in Messages has a prefilled draft |
| I4 | Attachments (images, optional) | Copies only inside Attachments; 30-day cleanup |
| I5 | Helper (LaunchAgent + own permission) + gated `imessage_send` | Reads after disabling ssh Full Disk Access; sending-card screenshot; denial sends nothing |
| X1 | External-process `connector` (`kind: connector`, MCP client, `/sync/*`) | Wait for first external `connector`; run a synthetic example plugin end to end |

GitHub and migration can run in parallel (G1/G2 depend only on M1/M4); iMessage depends on M1, M2 (stream archive) and M3 (`Inbox` interface).

## Decisions needed from the user

1. ~~**How to provide a GitHub token**~~: decided 2026-11-01—read `gh` `login` from `github.auth.from_machine`, keep only in hub memory; manual paste as fallback ("IV · Credentials").
2. **github.com or company-hosted GHE** (or both)? Self-hosted addresses only in config.
   Interim default (main session, 2026-11-02): github.com first; keep configurable `github.auth.host`, no GHE implementation yet.
3. **GitHub knowledge-base scope**: only related `threads` (recommended), or every PR / `issue` in specified repositories?
4. **Write completion back as read on GitHub?** Lark/mail currently do not (recommend consistency).
5. **Which Mac for iMessage**: one always-on Mac from the pair with matching data (recommended); do not read the other.
6. **Permissions**: use existing ssh Full Disk Access in phase one (recommended; every ssh session gets it, already the case), or build the independent helper immediately and disable ssh's permission?
7. **iMessage scope**: contacts only (recommended) / all / allowlist; include SMS (codes, notifications)? Text retention (recommend 90 days)?
   **May `message` text be sent to a `model`** (compression, classification, AI twin; otherwise archive/search only)?
   Interim default (main session, 2026-11-02): local reads on the Mac node; **`message` text is not shared with AI by default**, viewed only by the user in the App; `imessage.model` toggle defaults off.
8. **Contact names**: Mac contacts first, phone fallback (recommended), or only `phone_contacts`?
9. **iMessage replies in phase one**: `sms:` opens system Messages for you to send (recommended), or send directly from Mac (Automation permission, gated)?
10. **External-process connectors**: approve "MCP for tools + `/sync/*` for increments", implementing only with the first external `connector`?
11. May **Android/web** omit the new `sources` this round?

## What the user needs to do

- GitHub: set `github.auth.from_machine` in hub.json (choose an always-on machine with valid `gh` `login`); keep `gh` logged in,
  rerun `gh auth login` there after expiry (store `login` in `hosts.yml`, not only keychain). Set `host` for self-hosted GHE. If not using `gh`, paste a token in the App.
- iMessage: keep the selected Mac signed into Messages with iCloud Messages sync enabled; `confirm` "Remote Login › Allow full disk access for remote users" stays on in phase one;
  when implementing the helper, grant it Full Disk Access + control of Messages once through the Mac's graphical UI.
- Live write tests (G5, I5) use your own test repository / own phone number, with synthetic text only.
