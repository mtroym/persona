# Extensions

Persona's extension foundation (#1101): the open-source repository holds only general capabilities and **extension points**. Anything that only makes sense for one company or one service is built as an extension: install it and you have it; without it, everything still runs.
The skeleton follows VS Code (manifest + `contributes` + on-demand activation + separate process), permissions follow Chrome (declared explicitly, shown at install time), and from WordPress we borrow only event notifications, not filters.
Design trade-offs, the step plan and what is not done yet are in `docs/plans/plan-extensions.md`. Code: manifest, schema subset, permissions and registry in `server/internal/extension`, process host in `extension/host`,
the program side is the official SDK `sdk/go/ext` (built-in programs use it via `extension/serve`), conformance tests in `extension/conformance`; the wiring lives in the manager (see "Who owns what" below): `server/internal/extmgr/extensions.go`, `extension_routes.go`, `extension_cli.go`, and the admin UI endpoints in `admin_extensions.go`.

Extensions come from three places; the App and the API show them as one list:

| Source | What it is | What you can do |
| --- | --- | --- |
| `builtin` | Ships with personad, manifest compiled into the binary (Telegram connector, WeChat bot, computer control `cua`, browser `ego`) | Toggle, configure, secrets; cannot be uninstalled |
| `hub.json` | `extensions[]` in the manager's `manager.json` (formerly hub.json, hence the source value), written by hand by the owner (the current voice extension), see "Extensions in manager.json" below | Read-only: change it in manager.json or the admin UI |
| `installed` | A package installed on the hub machine with `personad extension install`, or an extension running elsewhere and attached over a socket | Everything: toggle, configure, secrets, upgrade, roll back, uninstall |

A built-in extension with a program of its own (the WeChat bot) is a plugin like any other: its code is in `server/plugins/<id>`, written against the Go SDK (`sdk/go`) and its own packages only, and its program is built, released and bundled with personad (`server/cmd/personad-<id>`). `server/plugins/guard_test.go` reads every file of the plugins, their programs and the SDK, whatever its build constraints: it fails when a plugin or its program uses any other code of this repository, and when a plugin is imported by anything but a program of its own: a directory of `server/cmd/` whose code uses only the SDK and that plugin (`_test.go` files may import one). The hub registers it before it runs from a declaration of its own, held to the plugin's by a test.

Three forms: **declarative only** (just a manifest, no code runs), **process extension** (the hub starts a child process that speaks the process extension protocol on stdin / stdout, and may also serve HTTP on a unix socket),
**remote** (runs elsewhere; the hub attaches to it over a unix socket).

## Who owns what (2026-11-05)

The user's decision: extensions, connectors and chat bots all belong to the **manager** (the assistant's process, `personad assistant`). They move over from the hub as-is, with no design changes; the hub is only the communication center. The routes, fields, events and errors the App sees stay the same, so clients need no changes.

| Part | Code |
| --- | --- |
| Manager side: the extension registry and its tables (`plugins`, `plugin_secrets`, `plugin_audit`, marketplace), install / preview / upgrade / roll back / uninstall / upload, marketplace and auto-upgrade, the process host (`extension/host`, sandbox, egress proxy, health and logs, the log stream during development), config and secrets (only it decrypts them), extension tokens and scope checks, the consumer of every contribution (UI, themes, widgets, shortcuts, personal Agent sources and Agent filters, model providers and quota, cua / ego / imessage, programs on computers, the assistant's skills, the knowledge base and channel parts, messaging `messaging`); the connector framework and all sources (`connector_secrets`, Inbox collection rounds, mail including Lark Mail, GitHub, iMessage, Telegram, lark-cli, each connector's sign-in); the Lark bot's Open Platform long connection and the WeChat bot, pairing, credentials and `/api/v1/bots…` | `server/internal/extmgr/` (files moved from the hub as-is, receiver changed from `Hub` to `Manager`; `manager.go` and `serve.go` are how it runs inside the manager, `machines.go` reaches computers through the manager's own devices, `hubglue.go` covers the few things it still has to ask the hub, and `models_link.go` covers what extensions and model configuration need to ask each other: providers, voice models, quota, presets) |
| Messages shared by both sides | `server/internal/extwire/` (the routes the manager answers, `Routes`; `KeptRoutes`, whose last answer the hub keeps while disconnected; headers added when forwarding, method names, reports, and the route scope table for extension tokens, `RouteScopes`) |
| Hub side: forwarding, the copy it keeps, what its own features need to ask | `server/internal/hub/extensions_link.go`, `extension_calls.go` (calls forwarded by credential), `inbox_routes.go` |
| Wired up in the assistant process | `server/internal/assistant/main.go`, `extensions.go`; the shared key file `server/internal/secretkey/` |

**What the manager does** (`internal/extmgr`; the hub does not import it and it does not import the hub):

- Startup order is the same as the hub's used to be: mail, personal Agents, connector registry, WeChat state migration, bots, the loops (Inbox, Agent sources, keep-awake, leases for programs on computers, auto-upgrade, health), extensions. Extension programs are **child processes of the manager**: a hub restart no longer restarts them, only a manager restart does.
- Reads the extensions, connectors and bots sections (keys unchanged) from its own `manager.json` (`~/.persona/managers/assistant/manager.json` for the built-in manager, copied out of hub.json once on upgrade, `docs/operations/config.md` ("manager.json")) and the same `secret.key` (`internal/secretkey`, no re-encryption; a manager running elsewhere must carry a copy). Table and setting keys are unchanged; no data migration.
- Answers `extwire.Routes`: `/api/v1/extensions…`, `/api/v1/admin/extensions…`, the compatible `/api/v1/plugins…`, the extensions' own HTTP / WebSocket / SSE (`/api/v1/extensions/{id}/…`, plus prefixes contributed by an extension's `api` contribution, e.g. the Lark extension's `/api/v1/lark/…`), `/api/v1/connectors…`, `/api/v1/bots…`, `/api/v1/channels/{id}/bind`, `/api/v1/mail/…`, `/api/v1/imessage…`, `/api/v1/agents…`, `/api/v1/messaging…`, the machines' `…/computer…`, `…/browser…`, `/api/v1/devices/{id}/widgets`.
- Inbox and knowledge base sources, channel transport, and the connector catalog for the assistant are all handed to the assistant directly **in the same process** (previously the hub's `inbox.collected`, `channels.receive`, `feishu.*`, `connectors.*`, `kb.*` over the bus; methods and parameters are unchanged, they just skip the bus). The `inbox.changed` and `kb.changed` the assistant sends to the hub also go to it first (pushed to extensions, and `{"type":"inbox"}` pushed to the App).
- Computers are connected through its own devices: as soon as a node connects, it adds the extensions' Agent filters to `node.configure`, then starts Cua / ego state, extension programs on computers, and connectors (previously done in the hub's `devices.connected`). The node's `plugin.node.*` events and the streams it opens itself (`mx…`) are no longer handed to the hub; it answers `assistant.lark` itself (only to the assistant's machine).
- Reports to the hub with `extensions.report` (the full report each time something changes): extension features (hello, `features` from `/health`), the API prefixes served by extensions and the receipts they declare, Cua / ego state on each machine and whether screen viewing can be enabled, keep-awake while the screen is being viewed, and the avatars of enabled extensions (`looks`). After an extension with avatars is uninstalled it also sends `extensions.looks_gone {ids}`; if the selected avatar no longer has a provider, it switches back to the default (see "Avatars" below).

**What the hub does**:

- Forwards these routes **to the manager as-is**: it opens a stream over `/api/v1/bus/relay` (`extensions.accept`) and runs this one HTTP exchange on it, so SSE and WebSocket are still streams. Who is asking goes in `X-Persona-*` headers (device, role, whether it is the owner using the main token locally, admin UI session, language, address); any of these headers sent by the client are stripped, and the hub's credentials are not forwarded. Extension pages in the admin UI are forwarded only through the admin UI route group (with the admin session header).
- When an extension token hits one of the hub's own routes, the hub asks the manager (`extensions.authorize`: the token's hash, the hub route pattern), and the manager says whose token it is and whether that scope is granted. The table is `extwire.RouteScopes` (`TestRouteScopes` still guards it).
- Keeps the manager's last report (also stored under the hub's own key `hub.extensions_report`, used after a hub restart before the manager reports): the `features` in hello, `computer` / `browser` in the machine list, screen ports. It forwards by the extensions' API prefixes, and for sends that declare receipts, the hub still records the receipts.
- What its own features need, it asks through `extensions.hub_call`: GitHub sign-in (avatar tiers) and the manager's `persona_settings` (mailboxes, each source's own triage rules). The node's MCP tools (`mcp.tools` / `mcp.call`), the extension wording on gate command cards, and `assistant.lark` belong to the manager (`docs/server/machines.md` ("Who handles computer events")): the assistant's `fleet_tools.go` asks the extensions directly in the same process (`extmgr`'s `AgentTools`, `CallTool`, `AssistantSummary`, `AssistantLark`, in the same order as before), bypassing the hub; if the caller stops waiting (the node cancels the call), the card it was waiting on is withdrawn too. Model config belongs to the assistant (`docs/assistant/model-config.md`). What it needs from extensions (providers, the voice extension's model, extensions that need a triage model, quota plugins: `app.call plugins.*`) and what extensions need from it (`models.usable`, `models.launcher_http`, `models.launchers`) are asked directly inside the manager's process, bypassing the hub (`extmgr/models_link.go`, `launcherquota.go`).
- Cards (send mail, Cua apps, `persona_settings`) wait in the assistant's interaction queue, also in the same process (`Options.Ask` → the assistant's `askHub`). When the owner answers in a channel, the assistant answers the card, after first letting the extension clear that owner action (`app.call interactions.owner_action`, in-process). What the manager needs the hub to do goes through the `hub` app of `app.call`: push (`push.localized`, `push.background`) and the language for mail translation (`push.mail_targets`).

**When the manager is away**: these routes return `503 assistant_offline` "助手暂时连不上，稍后再试" ("The assistant can't be reached right now, try again later"). `KeptRoutes` (`GET /api/v1/extensions`, `…/-/ui`, `…/-/themes`, `…/-/prompts`, `…/-/widgets`, `…/-/shortcuts`, `…/-/triage-templates`) return the last answer, so the App still draws extension UI and themes; hello still carries the last reported features. Connector, Cua and ego tools are not in the Agent's tool list during this time (calls get the same message); the hub's other features work normally. When the manager comes back it starts extensions, connectors and bots, runs one full Inbox collection round, and sends the full report again.

Visible differences from before:

- Extension programs no longer restart when the hub restarts (they restart when the manager does).
- While the manager is away, extension endpoints return 503 (before, extensions were there whenever the hub was); extension UI drawn by the App uses the last answer the hub kept.
- Every request with an extension token costs one extra bus round trip (on the same machine, under a millisecond).

## Terms

Three layers aligned with VS Code (#1843):

| Term | Meaning | In VS Code |
| --- | --- | --- |
| Extension | The unit of install, upgrade, authorization and trust: one package, one manifest `persona-extension.json` | extension |
| Extension point | A slot the hub provides, such as `connectors`, `channel` or `ui`; the full list with versions is the table in "Extension points" below | contribution point |
| Contribution | One item an extension attaches to an extension point: one `{id, …}` in the manifest's `contributes.<extension point>[]` | contribution |

Connectors, channels, pages, quota… are all "contributions to some extension point": one extension can contribute to several extension points at once (the extension that connects Lark contributes `api`, `connectors`, `assistant`, `mcp_servers`).
UI text always says "extension" (**Settings › Extensions**, **Extension Market**), the same as the Chinese edition of VS Code.

## Naming and compatibility

This system used to be called "plugin"; #1844 renamed it. Apps already shipped (iOS / Mac / Android / web), extensions already installed, and existing extension packages and marketplaces still use the old names,
so the old extension-system terms **keep working** where listed below. This does not retain previous product-brand names or marketplace filenames; the [Persona-only cutover](../operations/persona-cutover.md) takes precedence. New code, manifests and docs use the new terms.

| Old | New | Compatibility |
| --- | --- | --- |
| Manifest `persona-plugin.json` | `persona-extension.json` | If a package has only the old name, the old one is read; if it has both, the new one wins. Source trees keep only `persona-extension.json` (#2424): `personad extension pack` and the extension repositories' packaging scripts write the same manifest (with `tags` removed, see "Manifest") under both names in the package, so old hubs can still install it |
| `engines.plugin_protocol` | `engines.extension_protocol` | Both accepted (if both are written they must match). The manifest the hub stores and sends to the App still says `plugin_protocol`, so an old hub can still read it after a rollback |
| API `/api/v1/plugins/…` | `/api/v1/extensions/…` | The hub rewrites old paths to new ones for handling; answers use the old field names (`plugins`, `plugin`, `plugin_name`, and `plugins` for the marketplace count). New paths use `extensions`, `extension`, `extension_name` |
| `/api/v1/admin/plugins…` (including upload `…/upload`) | `/api/v1/admin/extensions…` | Same as above |
| Request body `{market, plugin}` (preview from a marketplace) | `{market, extension}` | Both accepted |
| Command `personad plugin …` | `personad extension …`, short form `personad ext …` | `plugin` still works; the CLI calls the new path first and falls back to the old path when the hub is still an old version (not restarted yet) |
| Marketplace list key `plugins` | `persona-extensions.json`, list key `extensions` | GitHub, git and HTTPS-directory marketplaces use only the canonical index filename. Both list keys are accepted; an explicitly registered full JSON URL remains its exact address |
| Env vars `PERSONA_PLUGIN_*` | `PERSONA_EXTENSION_*` | Both extension-system spellings are accepted; only the canonical product prefix is supported |
| manager.json `plugins` | `extensions` | Both read; if the same id appears in both, the one in `extensions` is used |
| Directory `~/.persona/plugins/` | `~/.persona/extensions/` | At startup the hub moves the old directory over whole and leaves a link `plugins → extensions` in its place (manual `ssh -R` to `~/.persona/plugins/<id>.sock` and rollbacks to an old hub still find it). If it can't be moved or both exist, the old directory stays in use and a log line is written; startup is not blocked |
| features `plugins` / `plugin_manager` / `plugin_ui_slots` / `plugin_auto_upgrade` | `extensions` / `extension_manager` / `extension_ui_slots` / `extension_auto_upgrade`, plus `extension_market_spec` | The old ones are still reported. A new client that sees `extensions` uses the new paths; otherwise (old hub) it uses the old paths |
| WebSocket events `plugin_ui {plugin, …}`, `plugin_upgrade {plugin, …}` | `extension_ui {extension, …}`, `extension_upgrade {extension, …}` | During the compatibility period both are sent (old first); push payloads carry both `plugin_upgrade` and `extension_upgrade` keys |
| Protocol `initialize`'s `plugin` | `extension` | The hub sends both keys; the SDK accepts both |
| Go packages `internal/plugin`, `plugin/host`, `plugin/serve`, `plugin/conformance` | `internal/extension/…` | — |
| Database tables `plugins`, `plugin_secrets`, `plugin_audit`, `plugin_markets`, setting key `plugin_upgrades`, push `thread-id` `plugin_upgrade`, model purpose `plugin.<id>`, hub↔node `plugin_skills`, `~/.persona/plugin-skills/`, slot name `plugin_page`, icon name `plugin`, error codes `plugin_failed` / `invalid_plugin` / `no_plugins` | **Unchanged** | Identities in persistence and internal protocols (same approach as Previous-brand → Persona, `docs/operations/rename.md`); changing them would need a data migration, and node and hub would fall out of sync |

**Marketplace move**: after a marketplace repository is renamed or moved (for example, `<org>/<old repository>` → `<org>/<new repository>`), extensions installed from it have the source `market:<market id>/<extension id>`, and the market id must not change.
Use `personad extension market set-spec <market id> <new URL>` (API `PUT /api/v1/extensions/-/markets/{id}` `{spec}`) to change the URL in place. It first reads the new URL once with the market's existing token; if that fails, nothing changes.

**How long it stays**: the compatibility layer stays at least until **2027-05-05** (six months, covering the 90-day lifetime of released TestFlight builds and old Android versions), and until the hub logs show **30 consecutive days with no calls** to the old API.
The hub counts every call to an old path per route (`legacyCalls` in `extension_compat.go`; on the first call each day it logs the previous day's counts as one line, `extensions: old plugin API calls since …`; step 7 wires this into the admin UI).
That decides whether it can be removed. Removal gets its own issue: drop the items in the table above one by one, and rewrite stored manifests to `extension_protocol`.
The `persona-plugin.json` generated in packages follows the same deadline: after 2027-05-05, and once nobody runs a pre-rename hub anymore, `personad extension pack` and the package.sh packaging scripts of the two extension repositories stop generating it.
Removing `tags` from packages and indexes must wait longer, until the marketplace is no longer used by hubs from before `tags` was added (also its own issue).

## Extension points

What an extension contributes where is written in the manifest's `contributes`: keys are extension points, values are lists of contributions, and each item has an `id` (globally unique within that extension point; a clash with another extension or with the hub itself means it can't be installed).

| Extension point | Namespace (method prefix) | Fields per item | Requirements | Wired to |
| --- | --- | --- | --- | --- |
| `connectors` (version 2) | `connector.` | The manifest fields of the connector protocol: `id` (`^[a-z][a-z0-9]*$`), `name`, `capabilities`, `hosts`, `agent_tools`, `unlisted`, `confirm_always?` (globs of write-operation names; `connector_gate`'s `allow` is also always clamped to confirm-on-phone, `deny` unchanged), `poll_seconds`, `kinds`, plus `kind_descriptions?` (kind → one line "Type: …" shown to the model, used by Inbox triage, drafting and the AI twin), `archive_sources?` (other names the archive may also go under), `cli?` (a command line on the assistant's PATH, see `docs/protocol/connector-protocol.md` ("Command line")), `sends_as_user?` (the connector sends messages in the user's name by itself, outside the write gate; see "Sent as the user: `sent.record`" below) | Process speaks the protocol | The connector registry (**Settings › Connectors**, Inbox, knowledge base, the assistant's tools); methods in `docs/protocol/connector-protocol.md`, including optional `catalog` source discovery/read calls and recipes (`docs/extensions/subscriptions.md`) |
| `voice` (version 1) | — | `id`, `name?` | `runtime.http` | The App's voice chat; contract under "Voice" below |
| `computer` (version 1) | — | `id`, `name?` | — | The assistant's computer-control tools and the toggle on the machine page (only the built-in `cua`), see [computer-use.md](computer-use.md) |
| `browser` (version 1) | — | `id`, `name?` | — | The assistant's browser tools (`browser_run` / `browser_look`) and **Browser** on the machine page (only the built-in `ego`), see [plan-ego-browser.md](../plans/plan-ego-browser.md) |
| `quota` (version 1) | `quota.` | `id`, `name`, `hosts?` (gateway host names, may start with `*.`) | Process speaks the protocol, permission `launcher_keys` | **Quota** on the launcher page, see "Quota" below |
| `providers` (version 1) | `provider.` | `id`, `name`, `description?`, `kind?` (`launcher` / `api`), `api?` (`messages` / `responses`), `base_url`, `model?`, `small_model?`, `models?: [{id, name?}]`, `dynamic_models?`, `key_hint?` | Declarative is enough; `dynamic_models` needs the process to speak the protocol | **From Extension** in **Settings › Model Providers**, see "Model providers" below |
| `api` (version 1) | `api.` | `id`, `prefix` (one path segment), `features?`, `events?`, `receipts?` (`[{method, path, kind}]`: writes that need send receipts) | `runtime.http`, `on_startup`; with `events`, the process must speak the protocol | A block of App-facing API `/api/v1/<prefix>/…`, see "App API" below |
| `assistant` (version 1) | `assistant.` | `id`, `skills?` (directories in the package, each with `SKILL.md`), `instructions?` (an `.md` in the package), `gate?` (a `.json` in the package: the gate for one command in a skill) | These files exist in the package; `gate` requires `skills`, and when credentials are needed the process speaks the protocol | The assistant's skills, instructions and command gates, see "Assistant" below |
| `mcp_servers` (version 1) | `mcp.` | `id` (`a-z0-9_-`, cannot be `persona` or the legacy server name), `command`, `env?`, `args?`, `requires?`, `allow?`, `deny?` | Declarative is enough; the command is installed on the computer that runs the assistant | One more MCP server for the Claude assistant (tool names `mcp__<id>__*`), see "MCP servers" below |
| `ui` (version 6) | `ui.` | `id`, `title`, `placement` (slot, see "UI slots": `extension_page` (old name `plugin_page`) / `launcher_section` / `nav_page` / `overview_card` / `capsule_card` / `machine_section` / `inbox_item` / `widget`), `hosts?` (only for `launcher_section`: gateway host names, may start with `*.`; omitted means any launcher, the extension decides), `icon?` (only `nav_page`), `opens?` (only `overview_card`, `capsule_card` and `widget`), `connector?` (only `inbox_item`, and required: the id of a connector contributed by the same extension), `families` / `refresh_minutes?` (only `widget`; `families` is required, see "System-level placements") | Process speaks the protocol; `launcher_section` needs permission `launcher_keys` | A page in the extension's details, a section on the launcher page, main navigation, overview, machine details, the details of an Inbox item, widgets and watch faces outside the App; see "Declarative UI", "UI slots", "System-level placements" below |
| `agent_sources` (version 1) | `agentsrc.` | `id` (`a-z0-9_-`, also the source segment of session ids `<id>:<sessionKey>`), `name`, `providers?` (`a-z0-9_`, e.g. `grok_bot`) | Process speaks the protocol, `on_startup` | Personal Agents on the Agents page (the hub has no sources of its own; all come from extensions), see "Personal Agent sources" below |
| `channel` (version 1) | `channel.` | `id` (`^[a-z][a-z0-9_]{0,31}$`, cannot be 8 hex digits), `name`, `kind` (`im`), `connector?` (binding and status come from this connector of the same extension), `bind?` (`qr` / `code` / `fields`), `description?`, `capabilities` (receive images / files / voice, `format`, `max_text`, `asks`, `push`, `typing`, `progress`, `threads`) | Process speaks the protocol; `on_startup` when there is no `connector` | A channel for talking to Persona: the hub runs the shared pipeline (into the chat, tracking the turn, sending the answer back, questions), and the extension only talks to the IM; see [channels.md](channels.md) |
| `events` (version 1) | `event.` | `id`, `subscribe` (a list of event names, see "Events") | Process speaks the protocol, `on_startup` | Only tells the extension what happened on the hub, see "Events" below |
| `shortcuts` (version 1) | `shortcut.` | `id`, `title` (1–40 characters), `description?` (≤ 200 characters), `params?` (≤ 8 `{name, title, kind: text / number / toggle / choice, choices?, required?}`), `ui?` (a full page from the same extension) | Process speaks the protocol | Actions in Shortcuts and Siri (iPhone, Mac, Apple Watch), see "System-level placements" below |
| `skills` (version 1) | — | `id`, `dir` (a directory in the package with `SKILL.md`) | Declarative is enough | The assistant's skills, delivered the same way as `assistant.skills`, see "Declarative contributions" below |
| `prompts` (version 1) | — | `id`, `kind`: `instructions` (`file`: an `.md` in the package) / `quick` (`title`, `text`) | Declarative is enough | Instructions appended to the assistant's CLAUDE.md; quick prompts in the App's input box; see "Declarative contributions" |
| `themes` (version 1) | — | `id`, `name`, `light` / `dark`: `#RRGGBB` for the role colors `accent` (required), `positive`, `caution`, `danger`, `ai` | Declarative is enough | One more option in **Settings › Appearance › Theme Color**, see "Declarative contributions" |
| `looks` (version 1) | — | `id` (`^[a-z0-9][a-z0-9-]{0,31}$`), `name`, `names?`, `subtitle?`, `subtitles?`, `avatar` (`sizes`: PNG, `svg?`, `safe?`, `dark?`), `colors?`, `dot?`, `app_icon_fallback` (id of a built-in avatar) | Declarative is enough; the package has the images it names | One more **Avatar** in settings: Persona's name, picture, colors and status dot, plus the App icon where it can be changed; see "Avatars" below |
| `triage_templates` (version 1) | — | `id`, `title` (≤ 40 characters), `text` (≤ 4000 characters) | Declarative is enough | Templates to pick from when filling in Inbox triage rules (`triage_rules`), see "Declarative contributions" |
| `agent_filters` (version 1) | — | `id`, `title?` (≤ 40 characters), `when?` (name of a boolean field in the config), `agents?` (`claude` / `codex`), `originators?`, `added_dirs?`, `first_message_prefixes?` (at least one kind) | Declarative is enough | Which Agent sessions on your computers stay out of Agents, see "Agent filters" below |
| `tunnels` (version 1) | — | `id`, `name` (1–40 characters, the row title in App settings) | Pure declaration; `configuration` must have a required `string` field `target_host`; `target_port`, if present, must be `integer` | When the phone cannot reach the hub address, connect through the App's built-in tunnel core; see "In-App tunnels" below |
| `messaging` (version 1, #2138) | — | `id` (`^[a-z][a-z0-9_]{0,31}$`, also the source id in API paths and events), `name`, `icon?`, `capabilities?`, `composer_modes?`, `settings_ui?` (an `extension_page` from the same extension), `links?` | `runtime.http`, with `on_startup` in `activation_events` | The App's **Messages** entry: conversations, chats and sending all go through `/api/v1/messaging/<id>/…`, drawn by the App with shared native messaging components; see [extension-messaging.md](extension-messaging.md) |
| `error_reports` (version 1, #5533) | `error_reports.` | `id`, `sources?` (`hub` / `manager` / `web` / `android` / `ios` / `mac` / `watch` / `vision`; omitted means all) | Process speaks the protocol, `on_startup`, permission `error_reports` | The hub's and the devices' errors and crashes, scrubbed, given to the program as `error_reports.deliver`; see "Error reports" below |
| `auth_relay` (version 1, #5644) | `auth_relay.` | `id`, `name?`, `hosts` (exact lowercase host names, 1–16), `paths` (exact paths, 1–16; a trailing `/` is ignored) | Process speaks the protocol, permission `auth_relay`; **starts off** at install and when an upgrade adds it, whatever `off_by_default` says | Authorization links the user's Agents stop at, on these hosts and paths and printed by the Agent's own command, handed to the program as `auth_relay.complete` to complete with the account it is signed in with; see [auth-relay.md](../operations/auth-relay.md) |

Extension points the hub doesn't know: the extension still installs (it may be written for a newer hub), that item has no effect, and the preview and list show a warning. When adding extension points later (knowledge base sources, link recognition…), add a row to this table.

## Manifest `persona-extension.json`

Lives at the root of the extension package. Example (a connector for a fictional notes service):

```json
{
  "manifest_version": 1,
  "id": "example-notes",
  "name": "Example Notes",
  "version": "1.2.0",
  "description": "Syncs Example Notes into the knowledge base and gives the assistant a search tool.",
  "publisher": "example",
  "homepage": "https://example.com/persona-extension",
  "tags": ["notes", "knowledge base"],
  "engines": {"persona": ">=202611050000", "extension_protocol": 1},
  "runtime": {
    "command": {"linux-amd64": "bin/example-notes-linux-amd64", "darwin-arm64": "bin/example-notes-darwin-arm64"},
    "args": ["serve"]
  },
  "contributes": {
    "connectors": [
      {"id": "examplenotes", "name": "Example Notes", "capabilities": ["status", "tools", "archive", "login"],
       "hosts": ["notes.example.com"], "poll_seconds": 600}
    ]
  },
  "configuration": {
    "type": "object",
    "properties": {
      "base_url": {"type": "string", "title": "Server URL", "format": "uri", "default": "https://notes.example.com"},
      "folders": {"type": "array", "title": "Folders to sync", "items": {"type": "string"}},
      "include_shared": {"type": "boolean", "title": "Include notes shared with me", "default": false}
    },
    "required": ["base_url"]
  },
  "secrets": [
    {"name": "api_key", "title": "API Key", "description": "Generate one in Example Notes under Settings › Developer", "required": true}
  ],
  "permissions": {"network": ["notes.example.com"]}
}
```

| Field | Required | Rules |
| --- | --- | --- |
| `manifest_version` | Yes | `1` |
| `id` | Yes | `^[a-z0-9][a-z0-9_-]{0,31}$`; also the path in `/api/v1/extensions/<id>/…` |
| `name` | Yes | 1–40 characters |
| `version` | Yes | `MAJOR.MINOR.PATCH` (may carry `-pre`, not `+build`); upgrades only go up |
| `description`, `publisher`, `homepage` | No | `homepage` must be https |
| `tags` | No | Tags shown and searched in the marketplace (#2424, previously in the extension repository's `market.json`): at most 16, each 1–24 characters. Written only in the source manifest: the manifests inside packages and marketplace indexes drop it (both SDKs also drop it when answering `manifest`, since the program embeds the source manifest), and the tags go in the index entry's `tags` (see "Extension marketplace"), because hubs from before this field read manifests strictly and refuse to install when they see `tags` |
| `engines.persona` | No | A range of hub build numbers, only `>=N`, `<N` and combinations; if not satisfied it can't be installed, and installed ones are marked `incompatible`. Development builds (build number 0) are not checked; a warning is given |
| `engines.extension_protocol` | Yes when there is `runtime` or `node` | `1` |
| `runtime` | No | Without it, the extension is declarative only. `command`: a relative path in the package, or `{"<GOOS>-<GOARCH>": path}`; if this hub's platform is missing it can't be installed. Must not be absolute or escape the package (`..` and links pointing outside the package are both checked), and must be executable; `personad` is allowed only for built-in extensions. `args`; `env` (must not start with `PERSONA_` / `PERSONA_`, nor be the dynamic loader's `LD_*` / `DYLD_*`, which would reach the sandbox launcher outside the sandbox; secrets don't go here); `rpc`: `stdio` (default) / `none`; `http`: serve HTTP on `PERSONA_EXTENSION_SOCKET`. `rpc: none` without `http` can't be installed |
| `node` | No | A program that runs on computers you pick (#1830, `docs/plans/plan-extensions.md` ("Seven")). `command`: per platform only, `{"darwin-<arch>" \| "linux-<arch>": path}`, same path rules as `runtime`, cannot be `personad`; the platforms listed are the computers it can run on, and every platform's program must be in the package and executable (the hub does not check its own platform). `machines`: the name of the config field that picks computers; this field must be an array of `format: machine`, its `items` must set `x-os`, and only to systems that `command` has a program for (to limit it to one computer, set `maxItems`). `args`, `env`, `rpc`, `http` as in `runtime`; `keep_running`: `lease` (default, stops if the hub hasn't renewed for half an hour) / `always`; `secrets`: names of secrets sent to the computer (must be in `secrets`), the rest stay on the hub only; `permissions`: `network` (host names), `files`, `macos_privacy`, see "Permissions". You can have `node` without `runtime` (if the extension points you contribute don't need a program on the hub). How it runs: see "Programs that run on computers" |
| `activation_events` | No | `on_startup` / `on_call` / `on_http`, see "When it runs"; with `runtime` the default is `on_call`; `rpc: none` allows only `on_startup` |
| `contributes` | Yes (except packs) | See "Extension points", at least one item; contributions to an extension point exported by another extension are written `"<extension id>.<extension point id>"`, see "Dependencies, exports and packs" |
| `off_by_default` | No | Contributions that start off when installed (`["ui/github"]`, #2829), see "Turning contributions on and off". Cannot list connectors or channels bound to connectors. Hubs from before this field read manifests strictly, so extensions with it can't be installed there and their marketplace entries are skipped (with a warning): extensions using it need a new hub anyway |
| `depends`, `exports`, `extension_pack` | No | Extensions depended on, extension points opened to other extensions, members of a pack; see "Dependencies, exports and packs" |
| `configuration` | No | JSON Schema for the config, limited to the subset below |
| `secrets` | No | `[{name, title, description?, required?}]`, `name` matches `^[a-z][a-z0-9_]{0,31}$`, at most 32 |
| `settings_check` | No | Boolean: the program checks a change of its settings or secrets (`settings.check`) before the hub saves it; see "Settings the user fills in" |
| `permissions` | No | See "Permissions" |
| `tools` | No | Command-line programs it needs (#4507); the hub installs them in the extension's own directory and puts them on its `PATH`, see "Command-line programs (tools)"; requires `runtime` |
| `auth` | No | `{title, description?, page?}`: the extension needs a sign-in (#4507); the program implements `auth.*`, see "Sign-in (auth)"; requires a `runtime` that speaks the protocol |

Manifests are at most 256 KB and are read strictly: an unknown field at any level means it can't be installed (a misspelled `permission` is not silently ignored).

When the hub build is below the lower bound in `engines.persona`, the extension list and details in Settings, marketplace, upgrade notices and installation errors
first say “This extension needs a newer hub. Update the hub first,” followed by the required range and current build in parentheses for troubleshooting (#3598).
Messages follow the request language (Chinese, English or Japanese); the CLI always uses the default Chinese, and logs do not change with the UI language.
At or above a `<N` upper bound, the message explains the incompatibility without telling the user to update the hub. Version checks, installation restrictions and the
`incompatible` status of installed extensions are unchanged; development builds still only produce a warning.

### Configuration: the form subset

The root is `{"type": "object", "properties": {…}, "required": [...]}`. Every client can draw it as a form, with fields in the order the properties appear in the file (the hub keeps the order when parsing,
and the API returns the ordered `form.fields`). Anything outside the subset can't be installed, which guarantees every extension's config can be edited in the App.

| Type | Allowed keywords | Form |
| --- | --- | --- |
| `string` | `title`, `description`, `default`, `enum` (with labels: `oneOf: [{"const", "title"}]`), `format` (`uri` / `email` / `hostname` / `machine`), `x-os` (only with `machine`: a list of `darwin` / `linux` / `windows`), `pattern`, `minLength`, `maxLength` | Text field; with `enum`, a picker |
| `integer`, `number` | Descriptive keywords, `enum`, `minimum`, `maximum` | Number field |
| `boolean` | Descriptive keywords | Toggle |
| `array` | `items` must be `string` (may have `enum`), `minItems`, `maxItems`, `uniqueItems` | Editable list; with `enum` in `items`, multi-select; with `format: machine` in `items`, one toggle per machine (below) |
| `object` | Top level only, containing only the scalars and arrays above; may have its own `required` | A group |

**Machines (`format: machine`)**: the value is the id of a machine on this hub.
- A list of these values (`{"type": "array", "items": {"type": "string", "format": "machine", "x-os": ["darwin"]}}`) is drawn on the App's and web's settings page as **one toggle per machine**: machine name, online or not, plus Cua status for computer-control extensions (those contributing `computer`). `x-os` lists only machines on those systems.
- The toggles **take effect immediately**, changing only this one item without waiting for Save; other drafts in the form are kept. Values that match no machine are still listed and can be turned off.
- When the list has `x-confirm` (one sentence), turning on a machine's toggle first asks that sentence and only saves once confirmed; turning off doesn't ask. The App and web both show a dialog: title **Turn On “<machine name>”?**, body is the sentence, buttons **Cancel** and **Turn On** (the App uses `alert`, the web uses `Dialog`, #1830).
- When the form has only these toggles, the form no longer says "…takes effect immediately, no need to save" underneath (the field's own description covers it, and there's nothing to save); if there are other items, it still says which take effect immediately and that the rest need Save. The `cua` extension's **Don't Auto-Lock While On** uses it (#1507).
- When the hub loads an extension, any of these values written as a machine name or host name (ignoring case, spaces, `-`, `_`, and only when exactly one machine matches) is replaced with that machine's id and saved back (`extension_machines.go`).

**Conventional settings**: names the hub recognizes; when an extension declares one, it gets extra uses.
- `triage_rules` (a top-level `string`; `maxLength` of at most 4000 is recommended, since the hub uses only the first 4000 characters): this source's Inbox triage rules, in the user's own words. The extension attaches it as `rules` when it hands over items (`docs/protocol/connector-protocol.md`),
  and the hub uses it instead of the global `inbox.rules` when triaging this source's items (`docs/assistant/inbox.md` ("AI triage")). An extension that declares it gets an item
  `inbox.rules.<extension id>` in the assistant's `persona_settings`: reading is free; changing it pops a confirmation on the phone (before → after), and only once approved is it written through the same config-change path (validated against the schema, only this item changed, extension restarted);
  extensions configured in the manager's manager.json are read-only.

Extensions that use the Inbox **must** have `triage_rules`; see "General rules for the Inbox" below.

### General rules for the Inbox (#1730)

Every extension that uses the Inbox (its connector declares `inbox`) must follow these three rules; extensions that don't use the Inbox are unaffected. How each extension does it (which kinds to hand over promptly, how often a round runs, e.g. the Lark extension's
`immediate_kinds`, `period_minutes`) is the extension's own setting and not part of the protocol.

1. **Wake-up** (already existed): when something changes, the connector sends `connector.changed {scope: "inbox"}`; the hub also pulls periodically per `poll_seconds` (`docs/protocol/connector-protocol.md`).
2. **Its own triage rules**: the settings must have a top-level `triage_rules` (`string`, "Conventional settings" above), attached as `rules` when handing over items.
   **Checked on install and upgrade** (at the preview step: CLI, App, admin UI, browser upload, marketplace, including auto-upgrades of marketplace extensions): without it, it can't be installed ("settings have no triage_rules … can't be installed").
   Already installed ones are not rechecked and run as usual; rolling back to a previously installed version is not checked either. Built-in extensions (the WeChat bot and so on) are exempt.
3. **Its own fallback (optional)**: add `fallback` to the connector's `capabilities`, promising every Inbox item carries a fallback tier `extra.rule_tier` (`action` / `fyi` / `noise`):
   when the model can't triage, the item enters the Inbox by that tier (`docs/assistant/inbox.md` ("AI triage")); the model still triages first. **Without `fallback`, a working triage model is required** (**Message triage** in **Settings › Models**, `inbox.triage`):
   - **Enabling**: when turning it on (`PUT /api/v1/extensions/{id}/-/enabled`, likewise in the admin UI and the toggle on the channels page), and on install (installed means on) and upgrade while on (including auto-upgrade),
     if **Message triage** can't be called (resolution yields no URL, model or key: not set there, not in the manager's `manager.json` `ai` / `inbox.triage_ai`, and no keyed provider that can be picked automatically),
     it returns **409 `needs_triage_model`**: "Set up “Message triage” in Settings › Models first, then turn on this extension". A refused install or upgrade doesn't use up the preview; once the model is set, try again with the same preview.
     When an auto-upgrade is refused, the extension records an "Upgrade failed" with that sentence as the reason.
   - **Changing the model**: while such an extension is on, changing **Message triage** to something that can't be called (`PATCH /api/v1/model-config/purposes/inbox.triage`) returns **422 `triage_model_in_use`** (not 409: the App treats a 409 on model settings as "changed elsewhere" and would hide the reason),
     with `extensions` listing which extensions, and `error` asking to disable them first. Removing your own choice and falling back to a `manager.json` config (`ai` / `inbox.triage_ai`) or automatic pick that still works doesn't count as clearing it, and saves normally.
   - **Extensions in manager.json** have no toggle, so the hub can't block them: they start as usual, but while the model can't be called **their Inbox items are not accepted**; in **Settings › Extensions** the status is `needs_setup`,
     and `needs_setup` says "Needs a triage model"; it recovers once the model works. (manager.json can currently configure only HTTP extensions, not connectors; this rule is implemented ahead of time.)
   - At runtime, per #1644: if the model can't triage and the item has no fallback, it does not enter the Inbox, and the `triage` in `GET /api/v1/inbox` says how many are waiting.

**Old hubs**: hubs before #1680 don't know `fallback`, so a connector declaring it fails to register entirely; #1680 hubs drop it and log a line, treating it as having no fallback (so a model is needed).
Extension versions that declare `fallback` put a hub build number that includes #1730 in `engines.persona` (`">=YYYYMMDDHHMM"`; the build number is the UTC time of the build).

Not supported: `$ref`, `allOf` / `anyOf`, `if` / `then`, arrays of objects, deeper nesting, `additionalProperties` (extra keys are never accepted). Values are at most 64 KB.
`default` and `enum` values are type-checked too. Property names like `password` / `token` / `secret` / `api_key` get a warning: config is stored and shown in plain text, so these belong in `secrets`.

### Permissions

Following Chrome: declared explicitly, shown in full at install time, and an upgrade that asks for more needs consent again. The vocabulary is closed; an unknown permission means it can't be installed.

| Permission | How to write it | Meaning | How the hub enforces it |
| --- | --- | --- | --- |
| Run a program (implied by `runtime`) | — | Runs on the hub machine as the hub's system user | Extensions installed on the hub run in a **sandbox** by default (can only read and write their own package and data directory, with memory / CPU limits); off by default for ones from a trusted marketplace, see "Sandbox" below |
| `network` | A list of host names, may start with `*.` (matches only subdomains, not the name itself), lowercase, no scheme, port or path; IP addresses must be listed literally | Which places it will reach | Sandboxed extensions can only go out through the hub's egress proxy, which lets only these hosts through, see "Egress" below; unsandboxed ones are still only shown and logged |
| `hub` | A list of scopes, see "Hub API: scopes" below | Which parts of the hub API it uses (read the knowledge base, drive Agents…) | The token the extension gets opens only routes in these scopes; anything else returns 403 `permission_denied` |
| `hub_api` (legacy form) | `true` | Gets the hub's address and full token (`PERSONA_HUB_URL`, `PERSONA_HUB_TOKEN`) and can do everything the hub's owner can | Without acceptance these two variables are not given; the install page labels it "Full access (legacy form)" and shows a warning. If `hub` is also written, `hub` wins |
| `storage` | `{keys: [...], patterns: [...], queues?: true, blobs?: true}` | Keep bounded, installation-owned business data in the manager's Postgres database | Default-deny stdio callbacks check declared logical keys, actual grants, installation epoch and the host-created process attempt; no files, SQL, credentials, hub scopes or extra network authority. See "Installation-owned business storage" below |
| `launcher_keys` | `true` | Uses a launcher's key to reach its gateway (required for `quota` contributions and `ui` with `launcher_section`) | The extension never sees the key: the hub or that computer sends the request for it, and only to the launcher's own gateway; without acceptance, it isn't asked |
| `gh_login` | `true` | Reads the GitHub token from gh's sign-in on the computer you pick (#2830, used for moving the GitHub connector out of the hub) | The hub gives it only to extensions that asked for it and were approved at install (`connector.credentials.gh_token`); the token lives only in the extension's memory |
| `usage_gateways` | `true` | Reads the account list of the gateways among the usage sources (no keys) (#4576): which accounts, status and quota the account-pool gateways connected under hub admin › usage sources have | Given only to extensions that asked for it and were approved at install (`usage.gateway_accounts`, "Quota" below); returns what the manager last read, with no admin key and no gateway address |
| `agent_runtimes` | `true` | Its program is an agent runtime that registers its agents with this machine's Persona node, so they show up in Agents (`docs/integrations/runtimes.md`); needs `runtime` | In a sandbox, the hub opens one socket in the extension's run directory (`PERSONA_RUNTIME_SOCKET`) whose every connection it joins to the node's `runtime.sock`, dialed then (a restarted node is still reached); it keeps at most 16 connections open at once (128 across all extensions' agent-runtime and source-runtime sockets) and closes any beyond that right away (a runtime needs one); nothing else of the home directory becomes visible. Without the permission there is no such socket. Unsandboxed ones reach `runtime.sock` as before. Older hubs don't know it (the install fails) |
| `error_reports` | `true` | Receives the error and crash reports of the hub and the user's devices (#5533); needed by `contributes.error_reports` | Given only to extensions that asked for it and were approved at install; reports are scrubbed by the manager before any extension sees them, see "Error reports" below |
| `auth_relay` | `true` | Completes sign-in links for your Agents (#5644); needed by `contributes.auth_relay` | Given only links its contributions name, from your own computers, printed by the Agent's own command in its last turn; the contribution still starts off until you turn it on, see [auth-relay.md](../operations/auth-relay.md) |
| Sends messages as you (implied by `contributes.connectors[].sends_as_user`) | — | A connector of this extension sends messages in your name on its own, without the hub's confirmation card (#5278); shown with the connector's name | Not written in `permissions`; shown on install, and an upgrade that makes another connector send as you needs your consent again (it is part of the digest you agree to). It lets the connector report its sends with `sent.record`; it does not check the sends themselves |
| Take back messages it sent in your name (implied by `contributes.connectors[].capabilities` having `recall`) | — | A connector of this extension can take back a message it sent in your name, when you ask for that in the App and confirm (#5278); shown with the connector's name | Not written in `permissions`; **shown on install and in its details, but not asked for**: it is not in the digest you agree to and an upgrade that adds it does not ask again, because the hub asks the program only after you asked for that recall, and it gives the program nothing it can start itself. See "Taking back what it sent: `recall`" below |
| Shown in the App (implied by `ui` with `nav_page` / `overview_card` / `machine_section` / `inbox_item`) | — | Shows its own UI in the main navigation, the overview, machine details and Inbox item details (listing each place's title) | Like events, not written in `permissions`; counted per contribution into the "It can" list and permission summary shown before install; an upgrade that adds a place needs consent again, see "UI slots", "Permissions" |
| Secrets (implied by `secrets`) | — | Uses the secrets you fill in | Each extension gets only its own |
| Run a program on computers you pick (implied by `node`) | — | Runs on the listed systems (Mac, Linux computers) as the node's system user; asked again each time it is turned on for a computer | Shown only: **no sandbox** |
| `node.permissions.network` | A list of host names, same as `network` | Which places it will reach from those computers | Shown and logged only |
| `node.permissions.files` | `true` | Reads and writes files outside its own data directory on those computers | Shown and logged only |
| `node.permissions.macos_privacy` | `true` | Will request Mac system permissions like Full Disk Access and Accessibility | Shown only; system permissions need the user's consent on that Mac |
| Install command-line programs (implied by `tools`) | — | Downloads and installs the listed programs on the hub machine (each with its version and where it comes from) | The hub downloads them itself, verifies them against the manifest's sha256 / integrity, installs them in the extension's own directory, without sudo and without running install scripts; counted in the permission summary, and an upgrade with a new program or a changed source needs consent again (a version change alone doesn't), see "Command-line programs (tools)" |
| Secrets sent to computers (`node.secrets`) | — | These secrets are sent to the selected computers | Handed to the program in memory only, never written to that computer's disk |

### Installation-owned business storage (#1187)

Installed source programs may explicitly request a bounded logical store:

```json
{"permissions":{"storage":{"keys":["preferences"],"patterns":["state/*","queue/*","media/*"],"queues":true,"blobs":true}}}
```

A key is case-sensitive ASCII letters, digits, `-`, `_`, `.`, or `/`, at most 128 characters. Empty names, leading/trailing or repeated slashes, `.`/`..` segments and escaped filesystem paths are rejected. Patterns are a nonempty literal logical prefix followed by one terminal `*`, not regexes or filesystem globs; `state/*` permits `state/topic` but not `state` or `other/topic`. Declare at most 128 keys and 32 patterns. Missing storage permission denies every operation. Queues and blobs require their separate flags as well as a declared key; install and expanded-upgrade consent disclose each flag. `hub_api`, HTTP scopes and network permissions cannot cover storage authority.

The manager derives the persisted installation, grant revision and opaque process-attempt identity from trusted initialization/stdio callbacks. Guests cannot select an installation or supply a run identity. Each callback rechecks that persisted installation and current grants under a Postgres row lock. A retired process, disable, uninstall, configuration/grant change or reinstall cannot keep using the old scope. A trusted initialization binds the new attempt before guest storage calls; displaced queue leases become **unknown**, not pending. Other runtime modes and source-node programs do not acquire this capability through a fallback.

#### Typed RPC and SDK

`schema/extension/storage.schema.json` defines the plugin-side methods; generated wire DTOs and the official TypeScript `ExtensionStorage` client share them. The client forwards calls without automatic retries. Values and chunks use canonical standard base64; `storageBytes` / `storageValue` encode and decode bounded bytes, not files.

| Method | Purpose |
| --- | --- |
| `storage.request` | Obtain an opaque mutation ID with a host-time expiry window |
| `storage.get`, `storage.list` | Read one entry or bounded key/revision metadata; list filters to currently granted keys and uses the last returned key as `after` |
| `storage.transaction` | Atomically `check`, `put`, `delete`, `enqueue` or `ack` up to 32 operations, including queue reservations with related progress, or completion with a result record |
| `storage.queue.enqueue`, `storage.queue.list` | Deduplicate a job by its declared queue key and immutable deduplication identity; read cursor-addressable pages |
| `storage.queue.claim`, `storage.queue.ack` | Claim due pending work with a host-run-bound lease; complete only the current unexpired lease |
| `storage.queue.retry`, `storage.queue.forget` | Explicitly return a currently leased job to pending under the same job ID and a bounded future deadline; remove only completed jobs |
| `storage.blob.create`, `storage.blob.write` | Allocate an opaque handle under a declared key and append bounded chunks at the exact committed offset |
| `storage.blob.finish`, `storage.blob.read`, `storage.blob.delete` | Seal before reading, read bounded offset ranges, or delete; handles are neither paths nor independently transferable permissions |

Each mutating request carries `request_id`. For a transaction entry, omit `revision` for an unconditional update, pass `""` to require absence, or pass the previous opaque revision for CAS. A `check` requires a revision. A failed operation rolls back the whole transaction, including any earlier progress/queue changes and its receipt. For example:

```ts
import { ExtensionStorage, storageBytes, type Hub } from "@persona/extension";

async function createPreferences(hub: Hub) {
  const store = new ExtensionStorage(hub);
  const { request_id } = await store.request();
  const request = {
    request_id,
    operations: [{ kind: "put", key: "preferences", revision: "",
      data: storageBytes(new TextEncoder().encode('{"version":1}')) }],
  };
  // Persist this exact request identity/payload when recovering an unknown result.
  // Do not manufacture a new ID to repeat an uncertain operation.
  return store.transaction(request);
}
```

Postgres commits the business mutation and payload-fingerprinted replay receipt together **before** success is acknowledged. Replaying the same live ID and payload recovers the committed result; changing the method/payload conflicts. Current key and queue/blob grants are checked before receipt lookup. Request IDs expire after five minutes (at most five seconds of future clock skew); expired IDs stay refused even after their receipt is pruned. A full live receipt ledger rejects another mutation rather than evicting a replay guard. A claim receipt cannot renew or resurrect an expired, completed, retried or replaced-run lease. The same active lease can recover a lost claim response; a stale claim replay is refused.

Queue leases last 1–60 seconds. Expiry or process loss is visible as `unknown` and is never automatically re-enqueued. An explicit retry accepts only the current live worker and preserves the original job ID, with `not_before` between now and 24 hours ahead; integrations choose their own bounded backoff and exhaustion policy. A claim/reservation is **not external-write permission**. Existing exact-operation confirmations and protocol-specific deduplication still gate each effect. A reservation-save failure means no send; external success followed by receipt-save failure is unknown, not authorization for a new send. This store makes no exactly-once remote-delivery promise and provides no privileged unknown-job resolution or transport.

#### Bounds and failure semantics

| Bound | Limit |
| --- | --- |
| One value / one blob write or read chunk | 64 KiB decoded; canonical base64 is at most 87,384 bytes |
| One storage request / transaction | 256 KiB encoded / 32 operations |
| One installation | 8 MiB of decoded data plus bounded identities, keys and receipts; 256 entries, 256 queue names, 128 total jobs, 32 blobs and 256 live receipts |
| One encoded persisted state document | 12 MiB; this is a database-document bound, **not** an enlarged RPC frame |
| All active and retained spaces in this manager database | 128 MiB encoded / 512 spaces |
| Retained spaces per public extension identity | Eight |
| List page / queue page | 64 entry metadata items / 32 jobs (default 16); every item remains cursor-addressable |
| Manager storage call | Five seconds, within unchanged existing RPC/frame, page and runtime budgets |

Metadata and receipts count toward the aggregate; filling a blob to the nominal data cap can therefore fail earlier. No truncation, eviction of pending jobs, memory replacement, larger source frame or extra host/network permission is used to fit data. Blob chunks preserve their exact original bytes and require seal-before-read. Credentials stay in existing sealed plugin/connector storage and must not be written into this business store.

Stable error categories include `permission_denied`, `invalid_params`, `not_found`, `storage_conflict`, `storage_quota`, `storage_retired`, `storage_expired`, `storage_corrupt`, `storage_unavailable`, `storage_unknown`, `storage_canceled` and `storage_timeout`; display text is localized and never an authorization/recovery signal. Cancellation/timeout or a lost reply can leave a commit outcome unknown. Recover only with the original live request ID/payload and inspect committed state when it expires; the SDK does not create replacement IDs. Database failures never fall back to memory. Persisted invalid or newer-format state is refused and preserved, not reset to defaults. Durable acknowledgments assume Postgres WAL durability (`fsync` and synchronous commit enabled); no promise is made for unsafe administrator settings or loss of the underlying storage.

#### Uninstall, retention and explicit owner restore

`keep_data=1` atomically detaches business data before removing the installation and its credentials. `keep_data=0` deletes its active/retained business spaces and credentials. A full retention set fails uninstall before deleting either data or credentials. Disable/logout retain business records without making a revoked scope usable. Existing plugin/connector credential stores retain their own revocation behavior and are never restored from business data.

A new same-ID installation gets an empty namespace. It cannot list or read retained data on its own. Only the authenticated owner principal, not a full/paired/answer-only device role, a forged owner header or an extension token, may call:

- `GET /api/v1/extensions/{id}/-/storage/retained`: opaque IDs, retirement times and encoded byte counts only.
- `POST /api/v1/extensions/{id}/-/storage/restore` with `{source, request_id}`: explicitly bind that exact retained space to the new installation. A changed request or another extension/installation is refused. Actual new data is never overwritten; an untouched empty namespace may be replaced. A lost restore response can be replayed with the same identity and payload.

Restore commits atomically, clears old-installation replay receipts and leaves displaced leases unknown, then replaces the guest so initialization binds the restored space. Failure preserves the retained input and never silently recreates credentials.

This generic store is **not a legacy filesystem importer, business-schema upgrade/downgrade, lease-renewal service, media URL or protocol-specific send/recovery implementation**. Migrations needing those contracts remain blocked until their separately scoped implementation and installed-source restart/crash acceptance preserve the original behavior. The store's schema version is 1; unsupported persisted versions fail closed. Source-default packages, marketplace indexes, signing changes and production activation remain separate phase gates.

#### Hub API: scopes (#1855)

To call the hub's HTTP API, an extension lists the scopes it uses in `permissions.hub` instead of asking for the full token:

```json
"permissions": {"hub": ["kb.read", "agents.read"]}
```

| Scope | Wording on the install page | Routes opened |
| --- | --- | --- |
| `kb.read` | Read the knowledge base | `GET /api/v1/kb/…`, `GET /api/v1/kb-search` |
| `kb.write` | Change entries in the knowledge base | `PUT /api/v1/kb/{kind}/{id}` |
| `inbox.read` | Read the Inbox | `GET /api/v1/inbox`, `/inbox/{id}`, `/inbox/mutes`, `/inbox/policies` |
| `inbox.write` | Handle the Inbox | Mark as handled, draft, reply (text only: replies with `changes` or `/agent` still require full access), document status, mute, set policies, refresh |
| `agents.read` | See the Agents and chats on your computers | `GET /api/v1/sessions…` (list, info, items, images, screen), `/interactions`, `/agents…` (personal Agents), `/workspaces`, `/workspace-board` |
| `agents.control` | Drive Agents | `POST /api/v1/sessions` (create), a chat's `messages` / `interrupt` / `queue` / `queue/send-now` / `seen`, `POST /api/v1/interactions/{id}/resolve`, sending messages to and marking read for personal Agents |
| `machines.read` | See your computers | `GET /api/v1/machines`, `/machines/{id}/metrics`, `/machines/{mid}/stat` |
| `notify` | Send notifications to your devices | `POST /api/v1/notify` (see "Sending notifications") |
| `assistant.ask` | Start chats with Persona and read chat activity | `GET /api/v1/assistant`, `POST /api/v1/assistant/conversations`, `GET …/conversations/{id}/feed` |
| `todos.read` | See to-dos | `GET /api/v1/todos`, `GET /api/v1/todos/{id}/decisions` |
| `todos.write` | Create and edit to-dos, attach chats to to-dos | `POST /api/v1/todos`, `PATCH /api/v1/todos/{id}`, a to-do's `agents/{mid}/{sid}` and `spaces/{mid}/{space…}` (`PUT` / `DELETE`), `POST /api/v1/todos/{id}/decisions` (outcome decisions only, see [todos.md](../assistant/todos.md) "API overview"); deleting to-dos, superseding decisions, completion checks and retries still require full access |
| `assistant.read` | Read your Persona chats (your messages and its replies) | No HTTP route: the `assistant.conversations` / `assistant.messages` RPC (see "Assistant conversations"). `assistant.ask` does not include it |
| `models` | Use the model you pick for it in Settings › Model configuration (never the key) | No HTTP route: the `model.complete` RPC (see "Model calls"). Not covered by the legacy `hub_api` full access |
| `extensions.call:<extension id>` | Call the API of extension "…" | That extension's own HTTP: `/api/v1/extensions/<id>/…` (excluding the hub's `/-/` endpoints) |

- **Which route belongs to which scope** is registered in `routeScopes` in `server/internal/extmgr/extension_scopes.go` (by the pattern the route was registered with); **unregistered routes accept only full credentials**, and extension tokens always get 403.
  All hub routes and the decision for each are listed in `server/internal/hub/testdata/route_scopes.txt` (`-` means full credentials only), guarded by `TestRouteScopes`: adding a route makes the test fail,
  so first decide whether extensions may call it (if so, add it to `routeScopes`), then run `PERSONA_UPDATE_ROUTE_SCOPES=1 go test ./internal/hub -run TestRouteScopes` and review the diff carefully.
- **Token**: starts with `pext_`. Each time the hub starts an extension's program it signs a new one (revoking the old one at the same time) and gives it via the env vars `PERSONA_HUB_URL` / `PERSONA_HUB_TOKEN` (old names `PERSONA_*` given too)
  and `initialize`'s `hub.url` / `hub.token` / `hub.scopes` (extensions running elsewhere get only `initialize`). It is revoked when the extension is disabled, upgraded, reconfigured or uninstalled, and a new one is signed at the next start.
  Only the tokens the manager holds in memory open anything: extension processes are the manager's child processes and restart with it (`docs/operations/hub-restart.md`), and the new run signs new ones, so a token never outlives the run that signed it.
  The manager also records each token in its database (`extension_tokens` table, migration `0431`, #1156): the token's SHA-256 (never the token), the manager and extension it was for, the scopes it opened (or every scope), when it was signed, and when and why it stopped (`replaced`; `stopped` when its run ends while it stays installed and on, such as an upgrade, reconfiguration, changed grant or secrets, or a dependency restart; `disabled`; `uninstalled`; `manager_restart`).
  Nothing recorded is read back as a credential. When a manager starts, before any extension starts, it marks its own rows still live from its last run as `manager_restart`, so a restart never revives a revoked token or one of an extension that is now switched off or uninstalled; other managers' rows are left alone. Revoked rows are dropped after 7 days.
  On a manager other than the built-in one, the manager signs it the same way, and `PERSONA_HUB_URL` / `hub.url` is that manager's relay to the hub on the loopback of its computer; the extension also gets `PERSONA_MANAGER` / `hub.manager` (`docs/assistant/managers.md`, "Extensions and the hub's API").
- Local only: requests with an extension token coming from the public internet return 403 `extension_token_local_only`, unless they came through the extension's manager's relay with its proof (`X-Persona-Manager-Proof`, `docs/assistant/managers.md` "Extensions and the hub's API", which also has the answers to a proof that is the manager's but not taken: `clock_skew`, `proof_replayed`, `proof_busy`, and `manager_offline` for one naming a manager the hub has no bus connection to): a relayed manager's computer may reach the hub no other way. Routes without a scope return 403 `{"code": "permission_denied"}`, and a missing scope also carries `scope` (the missing one).
- When an extension calls these routes with its token, it is treated like a device with full access (not limited the way read-only devices like the watch are: answering cards and sending messages work as usual), except Inbox replies with `changes` / `/agent`.
- **Compatibility**: old extensions with `hub_api: true` still get the hub's full token (only in env vars), with an extra warning on install and upgrade, "hub_api is the legacy form"; extensions in manager.json still get the full token. On a manager other than the built-in one, both get a token of every scope instead (none of the routes open to full credentials only): that manager has no token of the hub's to hand on. Every other extension's token there opens the scopes it asked for and no more.
  The full token covers every scope: upgrading from `hub_api` to a new version that asks for only a few scopes needs no new consent; the reverse does.
- The permission summary includes the scopes (normalized: sorted, deduplicated); for extensions without `hub` the summary is the same as before. An upgrade that adds scopes needs consent again, and `added_permissions` shows "Use more of the hub's features".

<a id="知识库-rpc1908"></a>

### Knowledge base RPC (#1908)

An extension's program can call the seven methods below directly, without passing a token over HTTP. Permissions are still declared only in `permissions.hub`; the hub checks against the permissions actually granted,
not the ones requested in the manifest. `kb.write` does not imply `kb.read`. The legacy `hub_api` full access stays compatible. Every call is forwarded by the hub to the manager's knowledge base,
so the extension does not need to read the hub's or the manager's data directory.

| Method | Parameters | Returns | Scope |
| --- | --- | --- | --- |
| `kb.list` | `{kind,tag?,limit?,offset?,since?}` | `{items,tags,total,next_offset?}` | `kb.read` |
| `kb.recent` | `{since,until?,kinds?,limit?,offset?}` | `{items,total,next_offset?}`: what came in from every source, newest first | `kb.read` |
| `kb.get` | `{kind,id,recent_chars?}` | `{detail}` | `kb.read` |
| `kb.day` | `{id,day}`, day is `YYYY-MM-DD` | `{day,messages,prev,next}`; `messages[].n` is the 1-based line number in the source file | `kb.read` |
| `kb.search` | `{query,messages?,limit?}` | `{hits}` | `kb.read` |
| `kb.update` | `{kind,id,...fields to update,evidence?,unlock?}` | `{id,before?}` | `kb.write` |
| `kb.owner` | `{}` | `{name,aliases,handles:[{kind,value}]}` | `kb.read` |

The params, answers, error codes and events are described in `schema/extension/kb.schema.json`; the Go SDK has them as `ext.Hub`'s `KBList`, `KBRecent`, `KBGet`, `KBDay`, `KBSearch`, `KBUpdate` and `KBOwner`.

kind is `projects`, `chats`, `people`, `docs`, `meetings`, `events` or `recordings`. list returns 30 by default, at most 200, with offset starting at 0;
if not finished, continue with next_offset. Paging is not a snapshot when things change between requests, so extensions should deduplicate and re-read when needed. search searches messages by default, 40 by default, at most 200;
get includes the most recent 12000 characters of messages by default; recent_chars may be 0, at most 20000. Empty lists return `[]`.
`kb.get` does not include live Agents; reading Agent state still needs the matching permission. A missing scope returns `permission_denied` with `scope` in the error data;
unknown fields, missing required fields or bad formats return `invalid_params`, a missing entity returns `not_found`, a write touching what someone else holds returns `conflict`, a manager that is offline returns `assistant_offline`, and a knowledge base that isn't ready returns `kb_unavailable`.
Internal methods such as `kb.forget`, `kb.context`, `kb.posts_*` are not on this allowlist, and even extensions with full access can't call them this way.

`since` / `until` are RFC 3339 times (an entity's update time: a chat's last message, a document's, meeting's or event's time, a recording's start). `kb.recent` defaults to chats, docs, meetings, events and recordings,
50 a page, at most 200; list entries for chats carry `source` (lark, mail, wechat, github…), so one pass covers every source; read a chat's text with `kb.day` and the rest with `kb.get`.

**Writes are the extension's** (#6100): the manager attributes `kb.update` to the calling extension (internally `kb.extension_update`; an extension cannot call that or name another). It may write fields nobody holds and fields it holds itself;
a field the user (or another extension) holds refuses the whole update with `conflict`, data `{owners: {<field>: "user" | "ext:<id>"}}` (a linked entity's field as `<kind>/<id>.<field>`), and nothing is written: put it to the user, don't retry.
The locked fields it writes become its own with `evidence` (`[{chat, day, n?, note?}]`, at most 20, kept in the entity's `meta.provenance`, visible in `kb.get`); `lock: false` holds nothing; `unlock: [fields]` hands back fields it holds.
`before` is each written field's previous value (`null` when unset, the old `summary` text), for the extension's undo. Rules and how this sits with the built-in pipeline: [Knowledge base](../assistant/knowledge.md#who-holds-a-field-6100).

The update fields follow the existing knowledge base update API, plus `pinned`, `lock` and `remove_people/remove_chats/remove_aliases/remove_projects`.
Protection rules, which entities they apply to, encryption of the owner's identity and history limits are in [Knowledge base](../assistant/knowledge.md#扩展整理知识库1908). The owner's aliases are edited by the user in the assistant settings;
there is no separate RPC for extensions to write the owner's identity.

Subscription example:

```json
{"permissions":{"hub":["kb.read","kb.write"]},"activation_events":["on_startup"],
 "contributes":{"events":[{"id":"knowledge","subscribe":["kb.changed","kb.forgotten"]}]}}
```

The hub pushes `event.kb.changed {kind,id,what}` and `event.kb.forgotten {source}`. Only subscribers that are still enabled, have that contribution on, have their program running, and were actually granted
`kb.read` (or legacy full access) receive them; write permission alone is not enough. Notifications carry only the identifying fields above, no body text, owner handles or absolute paths, and are not persisted for redelivery; after starting / reconnecting, an extension reads the full state first.

### Model calls (#6102)

An extension that needs a model (daily summaries, tagging, a weekly review) asks the hub instead of taking an API key of its own: it declares `models` in `permissions.hub`
and calls `model.complete` over its process connection. The call runs on the extension's **own purpose** `extension.<id>` in Settings › Model configuration,
which follows "Knowledge base" (`kb`) until the user picks another connection or model for it there; the extension never sees a key and does not choose the connection.

| Method | Parameters | Returns | Scope |
| --- | --- | --- | --- |
| `model.complete` | `{system?, messages: [{role: user\|assistant, content}], max_tokens?, json_schema?, timeout_seconds?, label?}` | `{text, json?, model, stop_reason, usage}` | `models` |

The schema is `schema/extension/models.schema.json`; the Go SDK has it as `ext.Hub.ModelComplete`.

- **Messages** start and end with the user's turn and alternate; `max_tokens` defaults to 4096, at most 64000 (connections speaking the Responses API are sent none, as for the hub's own calls);
  `timeout_seconds` bounds the whole call (default 120, at most 600). Params are strict: an unknown field (`model`, `temperature`…) is `invalid_params`.
- **Structured output**: with `json_schema` the hub appends the schema to the system prompt (`prompts/tools/json_schema.md`), takes the JSON value out of the reply (a code fence or a sentence around it is fine),
  checks it against the schema and asks once more with what was wrong (`tools/json_schema_retry.md`). The result's `json` is the decoded value; a second miss is `invalid_output` with `{problem, text, usage}`.
  It works the same on every connection (no vendor structured-output feature is needed). Checked keywords: `type`, `enum`, `const`, `properties`, `required`, `additionalProperties`, `items`, `prefixItems`,
  `minItems`/`maxItems`, `minLength`/`maxLength`, `minimum`/`maximum` and the exclusive ones, `allOf`/`anyOf`/`oneOf` (taken as `anyOf`), local `$ref`; others (`format`, `pattern`…) are left to the extension. At most 64 KiB.
- **No budget and no cap** (the user's decision): there is no daily limit and no fallback model. `usage` is what the call spent, every request of it together (`input_tokens`, `output_tokens`,
  `cache_read_input_tokens`, `cache_creation_input_tokens`, `requests`); the manager logs one line per call with the extension, `label`, model and usage. Keep it in the extension's own report if the user should see it.
- **A held connection**: when the connection is out of quota or busy (docs/assistant/model-config.md "Upstream rate limits") nothing is sent and the answer is `model_paused` with `{reason, until}`; try again after `until`.
- **Errors**: `permission_denied` (`{scope: "models"}`), `invalid_params`, `assistant_offline`, `model_unavailable` (nothing callable for this extension: the user sets one up in Settings › Model configuration),
  `model_paused`, `model_failed` (the upstream failed: an error status, a dropped connection), `model_refused`, `timeout`, `invalid_output`.
- **A declined input**: when the model declines to answer (the Messages API's `stop_reason: "refusal"`, a Responses API refusal part, a Chat Completions refusal, or a response stopped by a `content_filter`)
  the answer is `model_refused` with `{stop_reason, usage}`; `usage` is what the declined call spent. Sending the same input again gets the same answer: split it into smaller pieces, drop
  a part or report which input was declined. Match the code, never the message, which is for people and may change. Hubs older than this code answer `model_failed`
  with the message `model.complete: ai: the model declined to answer`; an extension that supports them can keep that match as a fallback for `model_failed` only.
- Internally the manager forwards it to the assistant as `models.extension_complete {extension, params}` (not callable by extensions), so the call is always the extension's own; the assistant lists the
  extensions with a purpose through `app.call plugins.models` (enabled, compatible, granted `models` itself).

```json
{"permissions":{"hub":["kb.read","kb.write","models"]}}
```

### Assistant conversations (#1288)

An extension that learns from what the user tells the assistant (how they judge, what they prefer, how they work) reads those
conversations over its process connection instead of a copy of the transcripts: it declares `assistant.read` in `permissions.hub`,
and the hub checks the grant the user gave, as for `kb.read` (the legacy `hub_api` full access counts too). Read-only: nothing here
sends, renames, archives or changes a conversation. The manager, which owns the conversations, answers both methods.

| Method | Parameters | Returns | Scope |
| --- | --- | --- | --- |
| `assistant.conversations` | `{since, until?, include_archived?, limit?, offset?}` | `{items: [{id, title, main?, archived?, origin, started?, updated}], total, next_offset?}` | `assistant.read` |
| `assistant.messages` | `{id, since?, until?, after_seq?, limit?}` | `{messages: [{seq, at, role, text, truncated?}], next_seq?}` | `assistant.read` |

The schema is `schema/extension/assistant.schema.json`; the Go SDK has them as `ext.Hub.AssistantConversations` and `AssistantMessages`.

- **Which conversations**: only the assistant's own space. The health space's (docs/assistant/health.md "Isolation") and guest
  conversations are never listed or read (`not_found`), nor are other agents' sessions (those are `agents.read`'s).
  `assistant.conversations` lists the ones last active at or after `since`, newest first, 50 a page (at most 200); with `until`, one
  known to have started at or after it is left out (`started` is set when the manager knows when it began). `origin` says who opened it
  (`user`, `channel`, `inbox`, `task`, `spawned`, `lark_agent`, `extension`; what the hub cannot tell is `user`), `title` is empty while
  it has none, `main` marks the main conversation.
- **Only words**: `assistant.messages` gives the user's messages (`role: user`) and the assistant's replies (`role: assistant`) in time
  order. Tool calls and results, thinking, notices and images are left out; so are the messages Persona typed into the conversation for
  the model (task notices, scheduled runs, wake-ups, follow-ups: `prompts.ParseInjected`), messages from other sessions and messages that
  are only a slash command. The user's text comes without context blocks and system reminders (`transcript.StripInjected`), the
  assistant's view cards as their text. `truncated` marks a message cut for pages.
- **Paging**: by `seq`. `limit` is 200 by default, at most 500. One call reads at most 8 of the node's pages (4,000 transcript items):
  a stretch of tool work can end a call with no messages and `next_seq` set. Pass `next_seq` as `after_seq` until it is absent.
  `since` / `until` (RFC 3339) bound the messages' times.
- **Errors**: `permission_denied` (`{scope: "assistant.read"}`), `invalid_params` (params are strict), `not_found`, `assistant_offline`,
  `machine_offline` (the assistant's computer is not connected or not set up).

```json
{"permissions":{"hub":["kb.read","assistant.read"]}}
```

### Sent as the user: `sent.record` (#5278)

The hub's write gate (`docs/extensions/connectors.md` "Sent as the user") sees only the writes that go through it. An extension's connector can also send in the user's name by itself, from inside a tool that reads (a reply the connector decides on, say), and the gate never sees that. A connector that does this says so in its manifest, and tells the hub about each send, so it shows in the same ledger:

```json
{"contributes":{"connectors":[{"id":"examplechat","name":"Example Chat","capabilities":["status","tools"],"sends_as_user":true}]}}
```

- **Consent**: `sends_as_user` is shown before install as "Send messages as you" with the connector's name, and an upgrade that makes another connector send as the user is listed as added and asks again (`extension.Digest` holds the ids of the connectors that declare it; manifests that do not are digested as before). Not written in `permissions`, like events.
- **The call**: the program calls `sent.record` (not under the `connector.` namespace; it is the hub's own) after the send went out. Params are checked in this order: the rate limit, `contribution`, `text`, `target`.

| Param | Meaning |
| --- | --- |
| `contribution` | The connector's id (required). One of the caller's own that declares `sends_as_user` in the manifest as installed, else `permission_denied` and nothing is written; also for a disabled extension or one that cannot run on this hub |
| `text` | What it said (required, may be empty: a missing or null `text` is `invalid_params`). The first 4000 characters are kept |
| `target` | Where it went (required): an object of short values (a chat, a thread, recipients…), else `invalid_params`. Strings are cut to 200 characters; a value that is not a string, a number, a boolean or a list of up to 10 strings is dropped, and so is a string that is empty once cleaned. At most 20 keys are kept, in key order, each name cut to 64 characters |
| `summary?` | One line saying what it was: the first line is kept, cut to 300 characters. Without it the row says `<connector>: <target fields>` (also its first line) |
| `session_id?`, `machine_id?` | The Persona conversation that asked for the send and the computer it ran on, when there was one. **The extension's own word**: the hub cannot check them, so they say who the extension claims asked, not who did. Cut to 200 characters each |
| `message_ref?` | The sent message's id on the other side, trimmed; one longer than 200 characters is dropped (the row has none), not cut. A write that goes through the hub's gate (`actions.do`) says it in its answer instead (`docs/protocol/connector-protocol.md`) |
| `setting?` | The name of the extension's setting that let it send without asking, when there is one (cut to 200 characters) |

  It answers `{}`. The hub writes the row itself: `connector` is `contribution`, `action` is `sent.record`, `origin` is `{extension: <id>}` plus `machine_id` and `session_id` when given (the program cannot claim another origin), and `let_through` is `{kind: "auto", setting?}`: no rule and no card were involved. Other fields in the params are ignored.
- **Errors**: `permission_denied`; `invalid_params` (params that do not decode, no `text`, no `target` object); `rate_limited` with `data.retry_after` in seconds (a fraction is possible), the same code and data a connector's tool uses to ask the hub to wait; `no_database` on a hub without a database. A ledger that cannot be written is an error with a message and **no `code`** (what the database said is logged, not sent back, since it could repeat the text): worth trying again later, though the send stays a send.
- **Rate limit**: 60 calls at once per extension, then one more each second, so a program that loops cannot fill the ledger. Every call counts, refused ones too, and the limit is checked first.
- **The connector's own switch** (Settings › Connectors) does not matter: if the user turned the connector off after it sent, the send still happened and is still recorded. The contribution switches never apply to a connector (`SwitchedElsewhere`), and the declaration is read from the manifest as installed, which is what the user agreed to.
- **One owner per connector id**: installing (or rolling back to) an extension that contributes a connector id another extension or the hub already has is refused, as is a manifest naming the same connector twice, so the id in `contribution` is that extension's alone.
- **Best effort**: the send already happened, so a failed call never undoes it, and the ledger is only as complete as the extension makes it: the hub cannot see sends the extension does not report, which is why the install line says so.
- **Go SDK**: `ext.Hub.RecordSent(ctx, wire.ConnectorSentRecordParams{...})` (`Target` is `map[string]json.RawMessage`, each value JSON: `{"chat": json.RawMessage(`"chat_1"`)}`). The fake hub in `sdk/go/exttest` refuses it for a connector that does not declare `sends_as_user` (and for a missing target or text), holds the params to `connector.schema.json`, keeps 20 target keys like the hub and keeps the calls in `Host.Sent()`; to try a failing ledger, answer `sent.record` with `Host.Answer`.

### Taking back what it sent: `recall` (#5278)

A connector that can take back a message it sent in the user's name (the one a row of the sent-as-user ledger names, `docs/extensions/connectors.md` "Sent as the user") says so by listing the capability `recall` with its others, and answers the hub's `connector.recall`:

```json
{"contributes":{"connectors":[{"id":"examplechat","name":"Example Chat","capabilities":["status","tools","recall"],"sends_as_user":true}]}}
```

`sends_as_user` is in the example because this connector also records the sends its tools make itself (`sent.record`); recall does not need it, and it is a permission the user agrees to, so leave it out if you do not send that way.

- **Not agreed to, shown**: the install preview, the market and the extension's details list "Take back messages it sent in your name" with the connector's name, and say it happens only after you ask and confirm, within the time the other service still allows. It is not in the digest (`extension.Digest`), in what an upgrade adds (`extension.AddedIn`) or in `MorePermissions`, so adding it in an upgrade asks nothing. A recall is asked of the program only after the user asked for it on their own device and confirmed, and the program cannot ask for one: the capability gives it nothing to start.
- **Older hubs**: a hub built before this one does not know the capability: it drops it (logging that once) and runs the connector without it, so only the recall is missing there (it is not a manifest field, which an older hub would refuse). A hub from before connector capabilities of extensions could be dropped (`docs/protocol/connector-protocol.md` "Event grouping") refuses a connector with a capability it does not know, so any extension that declares `recall` fails to install there, not only one that needs it. An extension that is of no use without recall puts the build number of the first build that has `recall` in `engines.persona` (`">=YYYYMMDDHHMM"`, as `fallback` does above), so that the hubs that would run it without recall do not install it; one that works without it needs nothing for them.
- **The call** `connector.recall` is `docs/protocol/connector-protocol.md`'s: the row's `action`, `message_ref`, `target` and `at`, none of what the message said, nor the row's id; answer `{"recalled": true}` only when the service confirms the message is gone. `not_recallable` (it cannot; `unknown_method` and `no_capability` count as this too: the program does not have it), `not_signed_in` / `offline` / `rate_limited` (not now), `recall_unknown` (it may have gone through); anything else is a refusal. The hub waits 30 seconds, and may ask again after an unknown or refused answer, so a repeat on a message already taken back must answer yes (and a message that cannot be found must not: an id that is not the service's is not "gone").
- **Which rows**: the ones with a `message_ref` of this extension's connector: its `actions.do` answers, its `sent.record`s (put the message's id in `message_ref`, and in `target` what you need to find it), and, for a write forwarded by the node's gate proxy of one of its assistant gates, the row with that gate's `command` in `target.command`. The hub asks the extension about such a row only if the command is one of this extension's own gates and no other extension has a gate with the same command (two can, and the node then uses one of them), so give your gate a command nobody else would. Only extensions the assistant is given the gates of count (enabled, with no problem; one that still needs setting up does, and so does one with no program), and only those the hub has applied (`apply`, which runs for each extension at hub start and at every change: during the start one not applied yet is not counted). A row whose command cannot be read is not asked about either, nor is a gate row of a built-in connector (no extension provides it): those answer `not_recallable`. The extension can check the command again. A send from before this install of the extension is not asked about.
- **Go SDK**: `ext.Methods{"recall": ext.Method(func(ctx context.Context, p wire.ConnectorRecallParams) (wire.ConnectorRecallResult, error) {…})}`; errors with `&ext.Error{Code: wire.ConnectorErrorCodeNotRecallable, Message: …}` (its methods have pointer receivers). The fake hub does not call it (it deletes a message), so test it against your own fake service.

### Egress (#1856)

The hub has an **egress proxy** (`server/internal/extension/egress`), started the first time an extension needs it, listening only locally:

- **Who goes through it**: sandboxed extensions ("Sandbox" below) have it as their only way out; when the sandbox is on but this machine has no sandbox, the env vars still point to it (well-behaved programs go through it, misbehaving ones can't be stopped, and the status says so).
  Extensions with the sandbox off, built-in ones, and ones from manager.json are unchanged and don't go through it.
- **What it lets through**: only hosts in this extension's `permissions.network` (`*.example.com` means subdomains under it; IP addresses only if listed literally). Anything else gets 403 "`<host>` is not in this extension's
  permissions.network", a log line, and a per-extension count (allowed / denied counts and the last denied host, in `sandbox.egress` of the extension's details; also counted in the health counters `egress.allowed` / `egress.denied`).
  Names that resolve to local loopback or link-local addresses (cloud metadata services) are not allowed, unless that exact IP is listed. Any port.
- **The hub's own API**: extensions holding a hub token (`hub` scopes or legacy `hub_api`) reach the hub from the sandbox at `http://hub.persona.internal` (that is what `PERSONA_HUB_URL` and `initialize`'s `hub.url` are),
  and the proxy connects it to the hub's local port; loopback inside the sandbox is not the hub's loopback.
- **Local services on the hub machine** (#2942): use the reserved name `host.persona.internal` (modeled on Docker's `host.docker.internal`). The proxy connects `host.persona.internal:<port>` to the hub machine's
  `127.0.0.1:<port>` (without a port, by scheme: `http` 80, CONNECT 443). Plain HTTP forwarding, CONNECT, and upgrades over plain HTTP (WebSocket) all work; when forwarding, the `Host` header becomes `127.0.0.1:<port>`.
  The proxy can't see TLS inside CONNECT, so `Host` / SNI stay as this name.
  - Who can use it: extensions whose `permissions.network` literally lists a loopback address (`127.0.0.1`, `localhost`) or `host.persona.internal` itself; others still get 403, with the same logs and counts.
    Old extensions that list `127.0.0.1` don't need manifest changes; just replace `127.0.0.1` / `localhost` in the service address with `host.persona.internal`.
  - Any port, except the proxy's own port (to prevent looping back into the proxy through it).
  - **Why a different name**: Go's `net/http` (`ProxyFromEnvironment`) hard-codes `localhost` and loopback IPs to bypass the proxy (even with an empty `NO_PROXY`), and HTTP libraries in other languages often have the same special case,
    so the program connects to loopback directly and the sandbox blocks it (`dial tcp 127.0.0.1:<port>: connect: operation not permitted`, with a `sandbox.egress` count of 0); and loopback inside the Linux sandbox isn't the hub machine's anyway.
    So from the sandbox, always use `host.persona.internal` to reach services on the hub machine (local SSH tunnels, locally started APIs).
  - Only the proxy knows this name: sandboxed extensions (including ones on machines without a sandbox that only point at the proxy) get through via the proxy; resolving it directly without the proxy fails (DNS has no such name), which is expected.
    Extensions with the sandbox off don't go through the proxy (see above) and still connect to `127.0.0.1` directly.
- **Who it recognizes**: on the local TCP port, extensions are identified by proxy credentials (`HTTP(S)_PROXY=http://ext:<credential>@127.0.0.1:<port>`, renewed at every start, invalid once disabled or uninstalled); the Linux sandbox also has one unix socket per extension
  (`~/.persona/extensions/run/<id>/proxy.sock`), and the socket itself says who it is.
- **The hub's own upstream proxy**: when the hub's environment has `HTTPS_PROXY` / `HTTP_PROXY` (`NO_PROXY` honored), the proxy goes out through it (CONNECT); extensions don't need to know.
- **Several addresses** (#1181): when a name resolves to several addresses, the proxy checks every one first (the rules above), then races only those
  (`dialRace` in `extension/egress/race.go`, after RFC 8305 Happy Eyeballs v2): IPv4 and IPv6 alternate starting with the resolver's first family, the next address starts 250 ms after
  the previous one or at once when one fails, at most four attempts are open at once (with four open and addresses waiting, the oldest is canceled after 10 s and its address goes once
  more to the back of the line, with 20 s that time, so making room never removes the only address that answers slowly), the first connection wins and every other attempt is canceled
  or closed. When the request's deadline passes, the error is the deadline (with each address's cause), even when the attempts' own timeouts come first. Nothing is resolved again, so the address dialed is always one that was checked. The fetch broker of source runtimes pins the address
  even through the hub's upstream proxy, so there each attempt is its own CONNECT to one checked address and is raced the same way. Process extensions race the addresses the proxy
  resolves itself (allowed names and settings addresses); when the hub has an upstream proxy for them, the CONNECT carries the name and the upstream proxy picks the address, so there is
  one attempt. The request budgets and the broker's admission limits are unchanged.
  Known limits: within a 15-second budget only the first eight addresses get an attempt (the ninth would start at 20 s; dialing one by one reached no further); and the process path
  bounds sockets per request, not in total: an extension's 128 pending requests can hold up to 512 attempts at once, with no cap across extensions.
- Env vars: `HTTP_PROXY`, `HTTPS_PROXY`, `http_proxy`, `https_proxy`, `ALL_PROXY`, `all_proxy` point at the proxy, and `NO_PROXY` is empty. Go, Node (libraries that apply the proxy themselves), Python and curl all honor them;
  old clients that only honor `HTTPS_PROXY` still can't get out from the sandbox, which is the intended effect.

### Sandbox (#1856)

**Who is sandboxed**: only **installed** extensions the hub runs from a package (not built-in ones, ones from manager.json, or `remote:` ones running elsewhere).

| | Default |
| --- | --- |
| Installed package verified against an **official marketplace** entry signed by the final key id `3cce0f471911a1cf`, with that marketplace's catalog still verified | Off |
| Everything else: other marketplaces (unsigned ones included), `dir:`, `file:`, `https://…`, uploads | On |
| **Installed before #1856 was deployed** (when the hub first starts with the sandbox) | Recorded as "off" and runs as before; use the command below to turn it on |

The verified installed key is saved atomically with the extension record in Postgres (`market_key`). It comes only from an unchanged, verified catalog entry and a checksum-checked package, not from manifest metadata or the market's current identity. Existing installs without this snapshot remain untrusted by default; moving their market to an official address cannot retroactively exempt them. An unsigned upgrade clears the snapshot. An upgrade keeps the replaced version's own origin and key (`previous_origin`, `previous_market_key`), and a rollback restores exactly those: going back to unsigned code never inherits the newer version's verified key, and a row upgraded before these columns existed rolls back with no key at all. Retired official keys never grant the default exemption; explicit owner choices, including recorded pre-sandbox choices, remain unchanged.

The owner can change each one (`personad extension sandbox <id> on|off|default`, or the admin UI's `PUT /api/v1/admin/extensions/{id}/sandbox`, or, on the hub machine with the main token over loopback,
`PUT /api/v1/extensions/{id}/-/sandbox` `{"enabled": true|false|null}`, where `null` returns to the default). The phone can't change it (turning off the sandbox widens trust, so like installing extensions it is done only on the hub machine and in the admin UI).
A change restarts the extension and is recorded as `sandbox` in the audit log. The choice is stored in the hub setting `extension_sandbox`; it is deleted on uninstall, and a reinstall starts from the default.

**Socket paths**: an installed extension's sockets live under its manager's directory (`<id>.sock` in `extensions/`, and in a sandbox the ones in its run directory), and a Unix socket path holds at most 103 bytes on macOS and 107 on Linux.
An external manager's directory (`managers/<id>` in the data directory) leaves the least room. The manager checks each path before the program starts, so a bind does not fail with only `invalid argument`:
a socket the manager binds itself (the proxy socket, the agent runtime's socket) that is too long stops the extension with an error naming the path, its length and the limit;
an HTTP socket, which the program binds, stops a program that serves only HTTP (`rpc: none`) the same way, while a program that speaks the protocol still starts and gets the same line in its log
(one that keeps serving the protocol without that socket keeps working; the Go SDK's `Main` exits when it cannot bind it).
The way out is a shorter data directory (`PERSONA_HOME`) or manager directory (`personad manager join --dir`).

**Linux**: `bwrap` (bubblewrap) + `systemd-run --user --scope` (`server/internal/extension/sandbox`):

- New user, PID, network, IPC and UTS namespaces; system directories (`/usr`, `/bin`, `/lib*`, `/etc`, `/opt`) are read-only, the sandbox's own root is read-only too, `/tmp` is a private empty directory, and **the home directory is invisible** (its path can't be written to either);
  the package directory is read-only, while its own data directory (`PERSONA_EXTENSION_DATA`, also `HOME`) and run directory (`~/.persona/extensions/run/<id>/`: its HTTP socket, the proxy socket, and with `agent_runtimes` the socket that leads to the node's `runtime.sock`) are writable.
- The network namespace has only loopback: the sandbox's first process is `personad __sandbox-run` (personad mounted read-only at `/run/persona/personad`), which forwards `127.0.0.1:3128` on the sandbox's own loopback to the proxy's
  unix socket, then runs the extension's program (stdin / stdout / stderr as-is, signals forwarded, exit code passed through). So any language's HTTP client works as long as it honors `HTTP(S)_PROXY`, with no SDK changes; only to reach local services on the hub machine you write `host.persona.internal`, not a loopback address ("Egress" above).
- `systemd-run --user --scope` sets limits: `MemoryMax` (default 1024 MB), `MemorySwapMax=0`, `CPUQuota` (default 100%, i.e. one core). The manifest can request others:
  `runtime.resources: {"memory_mb": 64–8192, "cpu_percent": 10–800}` (old hubs read manifests strictly, so extensions using it put a build number that includes #1856 in `engines.persona`).
  When `systemd-run --user` isn't available (no user systemd), there is only bwrap, with no limits.
- The first time it's used after the hub starts, it probes once: whether `bwrap` exists, whether it can create a network namespace, whether it can do the mounts a real start does (`/proc`, `/dev`, tmpfs for `/tmp` and `/run`, read-only remount of the root, running `true` once with temp directories as package / data / run directories), and whether `systemd-run --user --scope` works.
  In containers or on machines with restricted user namespaces, it can often create namespaces but can't mount `/proc`; that counts as no sandbox, and `why` says "bwrap can't set up the sandbox mounts: …".

**macOS**: `sandbox-exec`, with a profile that allows by default, then denies all outbound network (allowing only the hub proxy's local port and unix sockets in its own run directory), denies all writes (allowing only the data directory, run directory and `/dev`),
and denies reading the home directory (allowing only the package, data and run directories). There is no network namespace, so the proxy uses a local TCP port plus credentials; there are no resource limits. Tested on macOS 27.2 on 2026-11-05: writing data works, writing / reading the home directory doesn't,
only the proxy's port is reachable, direct connections to the internet fail, and a package placed under the home directory can still read itself.
  The paths the hub gives (package, data, run directory, the program itself) often pass through symlinks in the home directory (`~/.persona -> .persona` or the other way round): sandbox-exec treats reading the symlink itself as reading the home directory,
  so denying home-directory reads makes every path through it fail to open (`execvp … Operation not permitted`). The profile allows these directories by their real paths, then allows each symlink along the way individually (`literal`, which only lets it read where the link points;
  what follows is still judged by the real path). So the hub keeps using the original paths (arguments, `HOME`, `TMPDIR`, sockets in env vars), and nothing else in the home directory can be read through the symlinks.

**This machine has no sandbox** (bwrap not installed, the kernel doesn't allow user namespaces, other systems): extensions with the sandbox on still run, only pointed at the egress proxy; the extension's `sandbox.active` is `false`, and `sandbox.why`
and the list's `warnings` say "No sandbox: bwrap (bubblewrap) is not installed on this machine". To install bwrap: Debian / Ubuntu `apt install bubblewrap`; without root you can put a statically built bwrap on the hub's `PATH`.

**API**: the `ExtensionView` of installed extensions (list and details) has an extra `sandbox`: `{enabled, default, chosen, active, why?, trusted?, egress: {allowed, denied, last_denied?, denied_at?}}`.

**Permission summary**: the sha256 of `permissions` (normalized) plus "whether it runs a program"; with `node`, also its systems, node-side permissions and the names of secrets sent to computers (for extensions without `node` the summary is the same as before, so installed ones need no new consent). Installing must send back the summary given by the preview; so must an upgrade with more permissions (if they didn't grow, no new consent is needed, but the upgrade still has to be carried out explicitly).
The accepted one is stored in `plugins.granted`.

## Install, upgrade, roll back, uninstall

Installing an extension means running a new program on the hub machine, so packages from arbitrary sources can be installed only by the hub's owner, in two ways:

- **CLI on the hub machine**: `personad extension …` reads the main token from hub.json and calls the endpoints below over loopback.
- **Admin UI** (the **Extensions** page in `/admin`, [admin.md](../operations/admin.md) ("Extensions")): a session signed in with the admin password, subject to the access scope, CSRF and preview rate limits; endpoints under `/api/v1/admin/extensions…`.
  The admin UI can already add computers and hand out credentials, which matters as much as installing extensions, so it uses the same password; the audit log records it as `admin UI`.

The phone (device credentials) can see the list, toggle, change config and fill in secrets; install, upgrade, roll back and uninstall only work for **extensions from a registered extension marketplace** (see "Extension marketplace").
Arbitrary sources (directories, files, URLs, uploads, running elsewhere) still go only through the two ways above.

Every install, upgrade and uninstall is a step-by-step operation (download → verify → unpack → check → consent → install command-line programs → install → start → sync to computers), and clients can see the progress and which step it's stuck on; see [protocol.md](../protocol/protocol.md) ("Progress of extension install, upgrade and uninstall") (#3277).

Extension package: a directory, or a `.tar.gz` / `.tgz` / `.zip` (with the manifest at the root or in the only top-level directory). At most 200 MB and 20,000 files; unpacking rejects absolute paths, `..`, links pointing outside the package,
and device files, and zips can't contain links; file permissions keep only "executable or not". Packages made on a Mac often carry macOS metadata (`._*` AppleDouble files when tar runs without `COPYFILE_DISABLE=1`,
`__MACOSX/` from Finder compression, `.DS_Store`); unpacking always skips these, and they don't count as top-level directories (#1590: an extension's package had an extra `._<extension id>` at the top level and couldn't be installed).

| Source | How to write it |
| --- | --- |
| Directory on the hub machine | `dir:/path/to/extension` (or just an existing directory): **a copy** is taken, so later changes to the original don't affect what's installed; `.git` isn't copied |
| Package on the hub machine | `file:/path/to/extension.tar.gz` (or just the path) |
| URL | `https://…/extension.tar.gz#sha256=<64 hex chars>`: sha256 is required and checked after download; http is not accepted |
| Running elsewhere | `remote:rpc=<socket>,http=<socket>` (at least one): no files are copied; the manifest is requested from the extension (ones speaking the protocol answer `manifest`, HTTP-only ones answer `GET /manifest`) |
| Browser upload (admin UI only) | The `.tar.gz` / `.tgz` / `.zip` file itself: at most 200 MB, written straight into this preview's staging directory and then unpacked; the source is recorded as `upload:<file name>`; otherwise the same as a package on the hub machine |

Private extensions: clone the private repository on the hub machine and install with `dir:`, or have the private repository's CI produce a package with a sha256.

1. **Preview**: the package goes into `~/.persona/extensions/staging/<preview id>/`; the manifest is parsed and validated, the program is checked to be in the package, `engines` is checked, and contribution ids are checked for clashes.
   Returns `{preview_id, manifest, permissions (a few human-readable lines), permissions_digest, warnings, expires_at, upgrade?, from?, more_permissions?, added_permissions?}`. Cleared after 10 minutes.
   `added_permissions` exists only for upgrades: the items the new version has beyond the installed one (now runs a program, more hosts, more secrets, wants a hub token, wants to use launcher keys); the upgrade page uses it to spell out "what grew".
2. **Install**: with `preview_id` and `permissions_digest`; the package moves to `~/.persona/extensions/installed/<id>/<version>/` and is written to the database. If required secrets aren't filled in or the config doesn't match the schema, the status is `needs_setup` and it doesn't start.
3. **Upgrade**: preview first as well; the version must be higher. The old process stops and the new version starts; if the new version fails to come up within 30 seconds (handshake fails, exits), it automatically rolls back to the old version. Only the current and previous versions' directories are kept. Config and secrets are kept.
4. **Roll back**: back to the previous version (one step only).
5. **Toggle**: a disabled extension runs no process and none of its contributions take effect (connectors are removed from the registry); config and secrets are kept.
6. **Uninstall**: stops the process and deletes the database rows and secrets, the package directory and the data directory (`--keep-data` keeps the data directory); the rows for its contributed connectors in `connector_secrets` and connector settings are deleted too.
   Built-in ones and ones from manager.json can't be uninstalled.
   Servers that support cleanup warnings may return `{ok: true, warnings: [...]}`: the extension is uninstalled, but some files still need manual handling as indicated. The web marketplace and admin page keep the full result and refresh only after it is dismissed; the CLI keeps its success output and exit code and writes each warning to stderr. Old responses without warnings are handled as usual; cleanup warnings must not be treated as an uninstall failure and retried. The iOS / Mac market extension page (#4819) also counts it as uninstalled: an **Uninstalled** sheet lists every warning (selectable, so it can be copied) and closes only on **OK**, with no toast; for screenshots use `-tab settings -openMarketExtension <market>/<extension> -showRemovalWarnings YES`. Android is tracked in #4820.

Every step is recorded in `plugin_audit` (who, when, from which version to which), without config values or secrets. Who: `personad extension` (CLI), `admin UI` (admin UI),
`hub token` / `device:<id>` (the App's toggles, config, secrets).

Connectors are recorded in the same table (#5281), with the connector's id in the `plugin` column and a `connector_` prefix on the action, so they never read as the extension's own:
`connector_login_start` (a sign-in started), `connector_signed_in` (`{account}`, the display name the status shows), `connector_logout`, `connector_enabled` (`{enabled}`) and
`connector_feed` (`{feed, on}`) only when the value changes, `connector_options` (the connector's own settings changed; never their values), `connector_data_deleted`
(`{chats, people, posts, items, inbox}`), `connector_archive_deleted` (`{chats, people, posts, items}`; both add `incomplete: true` when part of the deletion failed), `connector_auth_source`
(`{source, machine}`, where GitHub's token comes from), `connector_recall` (`{row, action}`: a message the sent-as-user ledger has a row for was taken back, `docs/extensions/connectors.md` "Sent as the user"; only a recall that took it back), `connector_chat_rule_added` (`{row, chat}`: the user's "don't ask again in this chat" was made from a ledger row, `docs/extensions/connectors.md` "Sent as the user", Per-chat rules) and `connector_chat_rule_removed` (`{chat, row?}`: such a rule was dropped; never the row's words). This covers Settings › Connectors and binding or unbinding an extension channel, which signs its connector in or out.
Who is `device:<id>` or `hub token`, and `assistant` for what the assistant changed through its app calls; what the user typed and the credential itself are never recorded.
A record that cannot be written does not undo the action; the hub logs it. A program saving its own credential (`connector.secrets.save`, e.g. a refreshed session) is not recorded.

The Feishu bot is recorded the same way (#5353), with `plugin` = `feishu` (the channel's id) and a `bot_` prefix: `bot_credentials` (`{app_id, domain?, cleared?, owner_reset?}`,
only when the App ID, domain or secret really changed; never the secret), `bot_enabled` (`{enabled}`) and `bot_features` (`{features}`) only when they change, `bot_pair_code`
(a new pairing code was issued; never the code), `bot_paired` (`{open_id}`), `bot_pair_guessed` (`{fails}`: the pairing was dropped after too many wrong codes) and
`bot_unpaired`. What happens in the Feishu chat (pairing, guessing) is recorded with the Feishu account as the actor, `feishu:<open_id>`.

Reading it back (#5356): `personad extension audit [<id>] [--limit=N]` prints one line per row, newest first (time in UTC, id, action, actor, detail); the id is an
extension's, a connector's or `feishu` for the bot, and without one it is everyone's. The same comes from `GET /api/v1/extensions/-/audit` (see the API table below).
The table is append-only and never pruned: everyone's rows are read through `plugin_audit_at` (`at DESC, id DESC`), one id's through `plugin_audit_plugin`, each with its own statement.

### Declared identity changes

A market entry may declare one `replaces` predecessor. It is source metadata in the signed entry, never a package/runtime manifest field. A predecessor must be retired from the current catalog; duplicate claims, self-replacement, current-ID collisions and chains/cycles are rejected. The manager discovers a successor only in the installation's persisted market source. Publisher names, current official flags and an unsigned source's empty signing key are not authority to take over an installation.

The preview includes `rename_from` and names both IDs; its non-empty `permissions_digest` must be explicitly confirmed even when no permissions grow. Catalog refresh and automatic updates only announce a rename, never commit it silently. The source address, pin, declaration, installed version and target uniqueness are rechecked under the final operation guard. A fresh installation without the predecessor creates no former-ID aliases, and an installed predecessor cannot be bypassed through a second ordinary install.

`Record.ID` is the current public identity; `StorageID` (or `ID` for legacy rows) is its stable storage key. `PreviousID` follows the previous version and provenance. Postgres `plugins.id` and the `plugin_secrets` foreign key stay fixed; `public_id`, `previous_id` and unique `plugin_identities` names commit atomically with the version. Config, ciphertext, grants, switches, contribution choices, install time, auto-update choices, hub/node data, tools and rollback packages retain their slot. Internal model purposes `extension.<storage-id>` and knowledge owners `ext:<storage-id>` also stay stable; public reports keep the canonical ID.

Owner-facing API/CLI requests resolve only historical names of that committed installation. Program authentication uses exact canonical identity and the current runtime generation: an old token cannot acquire new identity or grants through an alias. Rename retires the old process without ordinary uninstall cleanup, waits for its exit, then activates the replacement. Rollback restores the previous public ID, version and origin in the same slot; uncertain database commits block writes and retain recovery material.

Before any renamed node upload/start/sync, the manager probes authenticated `plugin.node.identity` capability. Incompatible or unavailable nodes cannot receive a sync that would mistake the absent old ID for uninstall. Identity-aware nodes persist slot/revision bindings, preserve disabled/offline data and rollback packages, and reject late starts, uploads and events. The assistant's authenticated `extensions.identity` probe similarly guards stable model/knowledge ownership across manager versions. Do not manually move data or secret rows, or use uninstall/reinstall as a migration.

### Command-line programs (tools)

When an extension works through some command-line tool (for example, wrapping the CLI of a company's internal service), it lists it in the manifest's `tools`, and the hub installs it at install time, so computers that never had that CLI can use it right away (#4507).

```json
"tools": [
  {"name": "node", "title": "Node.js", "version": "22.23.4", "check": {"args": ["--version"], "min_version": "18.0.0"},
   "archive": {"linux-amd64": {"url": "https://nodejs.org/dist/v22.23.4/node-v22.23.4-linux-x64.tar.gz", "sha256": "…",
                               "path": "node-v22.23.4-linux-x64/bin/node", "mirrors": ["https://cdn.npmmirror.com/binaries/node/v22.23.4/node-v22.23.4-linux-x64.tar.gz"]}}},
  {"name": "example-cli", "version": "2.3.4", "requires": ["node"], "check": {"min_version": "2.0.0"},
   "npm": {"package": "@example/example-cli", "version": "2.3.4", "registry": "https://registry.example.com", "integrity": "sha512-…"}}
]
```

| Field | Rules |
| --- | --- |
| `name` | `^[a-z0-9][a-z0-9._-]{0,31}$`, the command name put on `PATH`; at most 8, no duplicates |
| `title`, `version` | For people; `version` is required |
| `requires` | Other programs in the same manifest installed first (no cycles); when checking and installing this one, those are already on `PATH` |
| `check` | If present, first look for a command of the same name on the hub's `PATH`: run `<name> <args…>` (default `--version`, 10 seconds); if the first `x.y.z` in the output is not below `min_version`, use it directly (reuse) and don't download. Ones in the home directory are not reused (the sandbox can't see them). After installing, it is also used to check that the program runs. Without `check`, it always installs its own |
| `archive` | `{"<GOOS>-<GOARCH>": {url, sha256, path?, mirrors?}}`: https only; `url` and up to 4 `mirrors` are tried in order; `.tar.gz` / `.tgz` / `.zip` are unpacked and `path` is the program inside, other extensions are treated as the program itself |
| `npm` | `{package, version, integrity, registry?, bin?}`: the hub itself downloads `<registry>/<package>/-/<name>-<version>.tgz` (`registry` defaults to npmjs, https only; for a private registry give its address), verifies it against `integrity` (`sha512-…` / `sha256-…`), then unpacks it. **No install scripts run and no dependencies are installed**: only suitable for packages that bundle their dependencies. `bin` is a name in `package.json`'s `bin` (default `name`); it's usually a `#!/usr/bin/env node` script, so usually `requires: ["node"]` |

Exactly one of `archive` and `npm`; if this hub's platform has no `archive` entry and nothing can be reused, it can't be installed. Unpacking is capped at 1 GB and 100,000 files (Node.js is about 200 MB and 6000 files).

- **When it's installed**: the `tools` step of install and upgrade (after consent, before writing to the database, `extension.Registry.planTools`); if one can't be installed, the whole install fails (`error_code` `tools_failed`), the extension stays as it was, and you can retry.
- **Where it goes**: `~/.persona/extensions/tools/<id>/<name>-<spec hash>/` (`pkg/` is the unpacked content, `bin/<name>` points to the program); a reused one is `machine-<name>/bin/<name>`, pointing to the machine's copy;
  `versions/<version>.json` records which ones this version uses. The same spec hash means the same copy: upgrades don't re-download, rollbacks use the previous version's; only what the current and previous versions use is kept, and uninstall deletes it all (`--keep-data` deletes it too: it isn't data).
- **Running**: when the hub starts an extension it puts this version's `bin/` directories at the front of `PATH` (dependencies first); in the sandbox this directory is mounted read-only (Linux `--ro-bind`, macOS allows reads), and reused programs live in system directories, which the sandbox can see anyway.
  Child processes the extension runs inherit `PATH` as usual. This applies only to extensions installed from a package: nothing is installed for `dir:` during development (`extension dev`), manager.json or built-in ones.
- **No sudo, nothing global**: it writes only the hub's state directory; anything else the program needs (credentials, caches) goes in the extension's data directory, e.g. by pointing its config directory there with an env var.
- `tools` in the list and details are the programs this version uses: `[{name, title?, version, reused}]`.

### Sign-in (auth)

When an extension needs the user to sign in to some service (scan a QR code, device code, enter a token), the manifest has `auth` and the program implements three methods; the hub turns this into the extension's sign-in state, and every client signs in with the same UI (#4507).

```json
"auth": {"title": "Lark sign-in", "description": "Confirm your identity with Lark before searching", "page": "login"}
```

`page` is optional: a `ui` contribution of `extension_page` (`plugin_page`), the extension's own sign-in page, for old clients that don't know `auth`; new clients don't list it under **Pages**.

| Method | Params → answer | Notes |
| --- | --- | --- |
| `auth.status` | `{}` → `AuthStatus` | Looks only at local state and must be fast (the hub waits at most 10 seconds) |
| `auth.step` | `{step, values}` → `AuthPrompt` | Step-by-step sign-in; `step` of `""` starts from the beginning. Must answer right away: the hub waits only for the process call timeout (30 seconds, 2 minutes for extensions with connectors) |
| `auth.logout` | `{}` → `AuthStatus` | Clears the credentials the extension stores itself |
| Event `auth.changed` | `{}` | The state changed on its own (expired, signed out elsewhere): the hub asks `auth.status` again |

`AuthStatus`: `{state, account?, message?, expires_at?}`, where `state` is `signed_out`, `pending` (sign-in in progress), `signed_in`, `expired` or `error` (`message` gives the reason).
`AuthPrompt` is the connector's `Prompt` (end of `../protocol/connector-protocol.md`) plus an optional `expires_at`: a fill-in-fields step, `device_code` (open `url`, enter `user_code`; `user_code` may be absent), `qr` (scan `url` with another app), or `done`.
For `device_code` / `qr`, the client asks again every `interval` seconds with the same `step` and no values, until a different step or `done` comes back. The hub renders a QR code matrix (`qr`) for the `url` of both `qr` and `device_code`; the extension gives only `url`.
Go SDK: put `ext.AuthHandler(a)` in `Program.Namespaces["auth"]`, and send the event with `hub.AuthChanged()`.
When wrapping a CLI's sign-in, "wait for the user to confirm" is often a blocking command (for example, some CLIs poll once with the device code and take about 30 seconds to time out), longer than the call timeout:
start a background poll in the extension, have `auth.step` answer only what it knows now (the same step while still waiting, `done` once confirmed), and send `auth.changed` once confirmed.

**Credentials stay with the extension**: what sign-in yields is stored in the extension's data directory (`PERSONA_EXTENSION_DATA`); `values` go only to the extension, and the hub doesn't log or echo them. The audit log records only `auth_start`, `auth_signed_in`, `auth_logout` and who did it.

**Hub API** (same as config changes: devices with full access and the main token; code in `extmgr/extension_auth.go`):

| Method | Path | Notes |
| --- | --- | --- |
| GET | `/api/v1/extensions/{id}/-/auth` | Asks the extension now and returns `ExtensionAuth`: `{title, description?, page?, state, account?, message?, expires_at?, checked_at?}`; `state` can also be `unknown` (the extension isn't running: disabled, missing settings); without `auth` declared, 404 `no_auth` |
| POST | `/api/v1/extensions/{id}/-/auth/step` | `{step, values}` → `AuthPrompt` (with `qr`); disabled: 409 `disabled`, not running: 409 `not_running` |
| POST | `/api/v1/extensions/{id}/-/auth/logout` | → `ExtensionAuth` |

- `auth` in the list and details is the last state the hub recorded (if it hasn't asked yet, it asks once in the background and pushes an event when it gets an answer); it asks again after the extension restarts, upgrades or changes config.
- When not signed in or the sign-in expired, `warnings` gets an extra line, "Not signed in (<title>). Sign in from the extension’s details", visible to old clients too.
- Event `{"type": "extension_auth", "extension": "<id>", "auth": ExtensionAuth}`: pushed whenever the state changes. `hello.features` includes `extension_auth`.
- When an install or upgrade finishes and the extension needs sign-in but isn't signed in, the operation carries `"next": "auth"`, and the client opens sign-in in the extension's details.
- CLI: `personad extension auth <id>` (status), `… auth <id> login` (prints a QR code, link and verification code in the terminal, or asks for fields one by one, and waits until done), `… auth <id> logout`.

**Clients**: the details have a sign-in section below the status: the state, **Sign In** / **Sign In Again**, and **Sign Out** when signed in; signing in is a card (`device_code` shows a tappable link, the verification code and a QR code of the link, `qr` shows the QR code, fill-in steps show a form).
In the list, the status of a signed-out extension is followed by "· Not signed in", and **About** lists the command-line programs (reused ones marked "already on the machine"). The install confirmation page has an extra line, **Install command-line programs**, and install progress has an extra step, **Install command-line tools** (one line per program, with progress for the one downloading).

### Turning contributions on and off (#1854)

Besides the whole extension's toggle, **each contribution** (`<extension point>/<id>`) can be turned off on its own: for example, keep an extension's connector but not its entry in the main navigation, or keep only a few vendors from a provider extension.

- A contribution that is off has no effect, and the rest of the extension works as usual: the hub registers the manifest "minus the contributions that are off" (`Extension.Manifest`; the full manifest is `Extension.Full`),
  so UI slots, provider presets, the assistant's skills and instructions, event subscriptions, App API, quota and personal Agent sources don't see it; the program gets only the enabled ones in `initialize`'s `active`
  (one entry per extension point: `{"ui": ["usage"], "providers": ["fast"], …}`), and the extension doesn't have to do anything for contributions that are off. Changing a toggle restarts the extension (like a config change).
- Ids that are off still belong to this extension: other extensions can't take them by installing; permissions, the permission summary and upgrade comparisons all use the full manifest, so turning off contributions doesn't make the extension "ask for fewer permissions".
- A **connector**'s toggle is the connector's own toggle (already used by **Settings › Connectors**, the Inbox and the knowledge base); this is just another entry point to the same toggle. **Channels** bound to a connector follow that connector.
  In the list they are `via: "connector"`, `can_disable: false`, and changes go through the connector's toggle (409 `not_running` when the connector hasn't started yet).
- Extensions from manager.json can't have individual contributions turned off (change manager.json instead).
- Stored in `plugins.disabled_contributions` (migration `0363`, `["ui/usage", …]`); kept across upgrades and rollbacks (if the new version no longer has that item, it has no effect).
- **Off by default** (#2829): the manifest's `off_by_default: ["<extension point>/<id>", …]` lists contributions that start off when installed, e.g. the extension's existing features are on at install while a newly added sidebar page waits for the user to turn it on.
  On first install these items are written into `disabled_contributions`; on upgrade only items that **the previous version didn't have** and the new version lists in `off_by_default` start off, and the user's choices for existing items are untouched; rollbacks don't change anything.
  The extension itself is still on once installed. Connectors, channels bound to connectors, and contributions that follow settings can't be listed (their toggle isn't the extension's); development mode (`personad extension dev`) doesn't start them off.

API (devices with full access):

| Endpoint | What it does |
| --- | --- |
| `GET /api/v1/extensions/{id}/-/` | The details get an extra `contributions: [{point, id, title?, enabled, can_disable, via?}]` (by extension point, then in manifest order); the old `contributes` (extension point → ids) is still the full set |
| `PUT /api/v1/extensions/{id}/-/contributions/{point}/{cid}` `{enabled}` | Toggles one item and returns the details; 404 if the item doesn't exist, 409 `read_only` for manager.json ones. The old path `/api/v1/plugins/…` works too |

CLI: `personad extension contributions <id>` (list), `personad extension contribution <id> <point>/<cid> on|off`.

### Dependencies, exports and packs (#1854)

**Dependencies** `depends: [{id, version?}]`: this extension needs other extensions installed and enabled (`version` is a version range: `>=1.2.0`, `<2.0.0` or both, separated by a space; omitted means any version).

- **On install and upgrade**: the preview's `depends` lists how each item looks on this hub (`state`: `ok` truly usable / `missing` not installed / `disabled` disabled / `old` version too low / `not_ready` installed, enabled and the version matches, but config, secrets or transitive dependencies aren't ready; `installed` is the installed version). The state is still a string, with no new fields; clients must not show "satisfied" for unknown values. `stopped` for on-demand programs is not the same as not ready, and the check won't force a program to start just to decide whether a dependency is usable.
  For ones not installed or with too low a version, the hub looks in the registered marketplaces (first the marketplace it came from, then others by id); found ones go into `bundle` as previews to **install together** (dependencies of dependencies too, at most 4 levels), and ones not found go into `unresolved`.
  The install / upgrade request must include `bundle: [{preview_id, permissions_digest}]`, and **every one** must match the preview (the user saw what each can do on the same screen). The hub merges version and marketplace constraints from every branch, builds the complete plan first, then commits the members and the main package at once, without activating them one by one; installed members that satisfy the constraints aren't reinstalled, and disabled state and config choices stay as they are.
  If there is anything in `unresolved`, or the full reachable graph still has unsatisfied versions / missing dependencies, nothing is installed (400). The commit also rechecks transitive dependencies of installed branches; extensions not from a marketplace (directories, files, uploads) also look for dependencies in the marketplaces.
- The details also return `depends: [{id, name?, version?, installed?, state}]` and `extension_pack: [{id, market?, version?}]`. The web settings use them to show dependencies and pack members, and show nothing when an old hub lacks these fields.
- **At runtime**: when a dependency isn't installed, is disabled, has too low a version, or isn't ready further down, this extension is `needs_setup`; "Still missing" gives the specific reason and the dependency cycle path, and it doesn't start. After dependencies are filled in, toggled, config / secrets change, an upgrade or an uninstall, the hub rechecks the full affected graph in dependency order, not just direct dependents, and never implicitly turns on extensions the user turned off.
- **Candidate graph check**: preview, install / upgrade commit and pack planning reject newly introduced cycles across the whole candidate graph, including disabled nodes; if the graph changed after preview, the commit rechecks. Existing cycle records are kept after loading, marked not ready, and repairs or unrelated installs are allowed; user data is not deleted.
- **Disabling or uninstalling something depended on**: while enabled extensions still depend on it, `PUT …/-/enabled {enabled: false}` and `DELETE …/-/` return 409 `{code: "has_dependents", dependents: [{id, name}], error}`;
  with `cascade` (`{"enabled": false, "cascade": true}`, `?cascade=1`) they are disabled first (only disabled, not uninstalled along with it). The admin UI endpoints work the same way (`cascade` in the request body). Circular dependencies are not supported (the second one can't be installed).

**Exported extension points** `exports: {extension_points: [{id, version, description?, schema?}]}`: an extension opens its own extension points for other extensions to contribute to.

- The contributor writes `contributes["<exporter id>.<extension point id>"]: [{id, …}]` and must list the exporter in `depends`; each item is an object with an `id` (`a-z0-9_-`, at most 64), globally unique within that extension point (like the hub's own extension points).
- `schema` is the JSON Schema for each item (the config subset, and it must declare a string `id`): if an item doesn't match, or the exporter has no such extension point, the contributor is `needs_setup` with the reason stated.
- The exporter gets, in `initialize`'s `contributions_to_me`, all contributions to it from extensions that are enabled and whose dependency graph, config and secrets are all usable (`{<extension point id>: [{extension, item}]}`); while the exporter itself isn't ready, contributions aren't exposed either. Both initialization and change events use a stable registry snapshot, and changes in transitive dependencies trigger updates too. When things change later (install, upgrade, toggle, config / secrets, turning off a single item, uninstall), the hub sends the event
  `extension.contributions_changed {contributions_to_me}`. The hub doesn't relay calls between them: to call the other side, use each one's HTTP, or the hub API (scope `extensions.call:<id>`, see "Permissions").
- Contributing to another extension's extension point adds a line "Provides content to other extensions" to the "It can" list shown before install. These contributions can be turned off individually like any other (`<exporter>.<extension point>/<id>`).
- Types generated from the schema keep the hub's built-in extension points strongly typed and preserve custom extension-point keys as opaque JSON (`Contributes.AdditionalProperties` in Go, index-signature fields in TypeScript, and additional-property maps in Swift and Kotlin). Nested JSON round-trips without losing custom fields, and additional properties cannot overwrite built-in fields. Opaque storage does not bypass the exporter's schema, dependency or permission checks.

**Extension packs** `extension_pack: [{id, market?, version?}]`: an extension with only members and no `runtime` / `contributes` / settings / secrets / permissions / dependencies / exports.

- Installing it = installing a group at once: the preview's `bundle` holds the members that aren't installed (or have too low a version) and their dependencies, each with its permissions listed, confirmed once (as above, with `bundle`). `market` is the marketplace address (`github:owner/repo`);
  omitted means the pack's own marketplace, then others by id. The pack itself is listed as an installed extension (`kind` is `extension_pack`, status "started").
- **All-or-nothing commit and recovery (#2466)**: the regular API, admin UI, CLI and auto-upgrade share the registry's plan executor. All previews / digests, full manifests, package fingerprints, source trust, engines, contribution namespaces, saved config and the full reachable dependency graph are validated first; any failure writes nothing. Missing new required config / secrets can still lead to `needs_setup`, but if existing values don't fit the new schema the upgrade is refused rather than dropping them.
  Once all packages are prepared, the members and main package are written in one Postgres transaction; only after the database commit succeeds is everything published, the success audit recorded, OnChange notified, and activation and old-version cleanup done. Normal failures in package preparation / the Nth record / the main package / the database commit restore the packages in reverse; the original records, versions, grants, config, sealed secrets, data and old packages are unchanged, and a valid preview can be retried; uninstall is not called to simulate a rollback.
  If the COMMIT acknowledgment is lost or package restoration doesn't finish, it explicitly reports unknown / incomplete, keeps the packages and backups, doesn't activate, refuses new writes, and waits for a reconciliation after the hub restarts and loads from PG; it doesn't promise that external side effects of programs after start can be rolled back.
  When auto-upgrade needs to add or upgrade members, it first turns into "needs confirmation" and doesn't silently grant member permissions; existing disabled dependencies that satisfy the constraints are not implicitly enabled. Marketplace installs / upgrades from devices with full access also require every bundle member to come from a registered marketplace, so local packages can't be slipped in to bypass owner permissions.
- Uninstall: `DELETE …/-/?members=1` (admin UI `{"members": true}`) also uninstalls the installed members and returns `removed_members`; without it only the pack is uninstalled and the members stay. When members are depended on by extensions outside the pack, it first returns 409 listing the full dependency chain, and after confirming `cascade` only the outside dependents are disabled; any read-only dependent blocks the operation before anything is written.
- Uninstalling a pack uninstalls the members first, and the pack only once all succeed. If deleting a member or the pack itself fails, it returns 409 `{ok:false, code:"partial_uninstall", removed_members:[id], failed_members:[{id,error}], remaining_members:[id], error}`: the pack is kept, and a retry handles only members still installed; packages / data already uninstalled are not pretended to be rolled back. The regular web UI and admin UI both show the same `error` (uninstalled / not uninstalled / retryable). A marketplace pack can't let a regular device bypass the uninstall permission for local packages: if any member was installed from outside a marketplace, the whole operation is refused first, to be handled on the hub machine or in the admin UI.
- CLI: `personad extension install …` prints the dependencies needed, the ones installed together and the ones not found, and installs them together after confirmation; `disable <id> --cascade`, `uninstall <id> [--cascade] [--members]`.

**Compatibility**: these three fields are new. Older hubs (before #1854) read the manifest strictly: an extension that has them fails to install, and its marketplace entry is dropped with a warning. Extensions that use them put a hub build number that includes #1854 in `engines.persona`.

### Command line

Only used on the hub machine: it reads the master token and the `listen` port from hub.json in the state directory (default `~/.persona`, see "Naming and compatibility"; the port defaults to 18660 if unset, overridden by the env var `PERSONA_LISTEN`, the old name `PERSONA_LISTEN` is also accepted), and calls "The hub's API" via `127.0.0.1`.
If there is no hub.json on this machine it errors out; it does not connect anywhere else. `personad ext …` and `personad plugin …` (the name before #1844) are the same command. `help` prints usage to stdout; with no subcommand, usage is printed to stderr as an error with exit code 1; `contributions` / `contribution` are only listed together when a subcommand is mistyped.
An option the command does not take, a missing argument or an extra one is refused with exit code 1 before anything is sent to the hub (a mistyped `--keep-data` does not delete the data).
`new`, `test` and `pack` below do not need the hub; everything else needs the hub running.

```text
personad extension list                              One line each: id, version, source, status, name; errors are shown on failing ones, and ones needing setup show "还缺：…" (missing: …)
personad extension show <id>                         Details as JSON: permissions, settings, whether secrets are set
personad extension install <source> [--yes]          First prints what it can do, the dependencies it uses, what gets installed with it and what cannot be found, then asks "继续吗？[y/N]" (continue?);
                                                     installs only on y / yes; --yes skips the question. Afterwards prints "<id> <version>：<status>", and what is missing if settings are missing
personad extension upgrade <id> <source> [--yes]     Same as install, but upgrades the installed <id>
personad extension rollback <id>                     Go back to the version before the last upgrade
personad extension enable <id> | disable <id> [--cascade]   --cascade: also disable extensions that depend on it
personad extension sandbox <id> on|off|default       Whether to run in the sandbox (default: off for trusted marketplaces, on otherwise), see "Sandbox"
personad extension auth <id> [login|logout]          The extension's sign-in: status; login shows a QR code / link / code in the terminal and waits until done, see "Sign-in (auth)"
personad extension contributions <id>                Each contribution and its switch
personad extension contribution <id> <point>/<cid> on|off   Turn a single contribution on or off (for a connector, this is the connector's switch)
personad extension config <id> key=value …           Values that parse as JSON are taken as JSON, otherwise as text; key= deletes the entry
personad extension secret <id> <name> [--delete]     The value is read from stdin (not echoed in a terminal; first line from a pipe), never as a command-line argument; an empty one is refused (--delete removes it)
personad extension uninstall <id> [--keep-data] [--cascade] [--members]   --members: also uninstall a bundle's members
personad extension market list                       Registered marketplaces: id, name, extension count, signing, address (the official one is marked "官方" (official))
personad extension market add <spec> [--token | --token-from=<marketplace id>]
                                                     --token: read the token from stdin; --token-from: use the token of that registered marketplace (not shown)
personad extension market set-spec <id> <spec>       The marketplace moved (repo renamed): the id stays, and what was installed from it still counts as its own, see "Naming and compatibility"
personad extension market remove <id> | refresh <id>
personad extension market token <id> [--clear]       Replace the token (read from stdin, not echoed) and reload; --clear removes it. Setting one helps even for a public GitHub marketplace, see "Extension marketplace", "GitHub rate limits"
personad extension search [keyword]                  Each result printed as market:<marketplace id>/<extension id>, with installed / upgradable
personad extension install market:<marketplace id>/<extension id>  Install from a marketplace; if already installed, this upgrades it
personad extension auto-upgrade on|off [<id>]        Master switch for auto-upgrading marketplace extensions; with an id, the switch for that one extension
personad extension upgrades                          Marketplace extensions with a new version: upgradable / waiting for you to confirm / skipped
personad extension audit [<id>] [--limit=N]         The audit, newest first: who installed, switched, signed in or deleted what, and when; without an id, everyone's

# Writing extensions (see "Writing extensions: SDK, templates and command line"); without <dir> it is the current directory
personad extension new <id> [--template go|ts] [--name <name>] [--dir <dir>]   go template by default; name and dir both default to the id
personad extension dev [<dir>]                       Attach it to this hub to try it out; rebuilds and restarts on change; needs the hub running
personad extension test [<dir>]                      Build for this machine and run it through the hub's checks
personad extension pack [<dir>] [--platforms linux-amd64,…] [--out <dir>] [--split]
                                                     --platforms defaults to the platforms the manifest has programs for (this machine's if the manifest has none), --out defaults to <dir>/dist
```

## Extension marketplace

A repository or website publishes an **index** `persona-extensions.json` that declares all the extensions it offers. Once the hub registers this marketplace, the app, web, admin UI and command line can all browse, search,
install, upgrade and uninstall its extensions. Only one official marketplace is preset (see "Official marketplace" below); every other marketplace is registered by the user. Code is in `server/internal/extension/market.go` (index, download, verification) and `server/internal/extmgr/extension_market*.go` (registration, catalog, endpoints).

**Marketplace address**:

| Form | What is read |
| --- | --- |
| `https://example.com/extensions/` (not ending in `.json`) | `https://example.com/extensions/persona-extensions.json` |
| `https://example.com/v100/market.json` | That file itself |
| `github:owner/repo`, `github:owner/repo@branch` (also accepts `https://github.com/owner/repo`, `…/tree/<branch>`) | `persona-extensions.json` at the repo root, read through the GitHub API; private repos work too |
| `git:<address>`, `git:<address>#branch` (address is `git@host:path.git`, `ssh://…` or `https://…`; `git@host:path.git` can also omit `git:`) | The hub shallow-clones the repo into `~/.persona/extensions/markets/<marketplace id>/` and reads `persona-extensions.json` at the root; refresh does a fetch. Uses **the hub machine's own credentials** for SSH; HTTPS clones may use a token scoped to the original clone URL, without credential redirects, argv secrets or persisted tokens; ssh / https only, no prompts, 3-minute timeout. Use this for private git hosting that has no "read a single file with a token" endpoint |

https and GitHub marketplaces accept https only. A private https / GitHub marketplace takes a **token** (for GitHub, a read-only fine-grained token: Contents read permission on this repo). It is stored encrypted with the manager's `secret.key`,
sent only as `Authorization: Bearer` to the marketplace's own origin (for a GitHub marketplace, the API origin), except for the explicitly derived GHCR binding described under "OCI packages and mirrors". It is never sent to external redirects or echoed back. Token reuse and address moves retain the original HTTPS origin or GitHub owner binding.

**GitHub rate limits**: without a token the GitHub API gives each egress address only 60 requests per hour. All devices at home share one public address, and once it is used up you get 403. So:

- **Public repos can take a token too** (a fine-grained token with Public repositories selected and no permissions checked), which raises the quota to 5000 per hour. When the app / web cannot read a GitHub marketplace and has no token,
  the error is followed directly by **Add token** and this explanation (the official marketplace is the exception: the catalog page does not let newcomers fill it in there and only shows an unobtrusive link to **Manage marketplaces**, see onboarding.md 4.5); on the command line, `personad extension market token <id>` (read from stdin, `--clear` removes it).
- **Fallback addresses without a token** (`githubFallback` in `extension/market.go`): when the API returns a rate limit, the same file is read from places without the API quota — files in the repo (the single catalog document and packages at relative paths)
  from `raw.githubusercontent.com/<owner>/<repo>/<branch, default HEAD>/<path>`, `release:` assets from `github.com/<owner>/<repo>/releases/download/<tag>/<asset>`. Only public repos are there;
  private ones return 404 and still report the rate limit / need a token. Marketplaces with a token always go through the API only (the token is not sent elsewhere). What is read is still signature-verified and packages are still checked by sha256; reading from another place does not change what is trusted.
  A signed catalog is one document, so caching cannot pair a new payload with an old detached signature.
- No conditional requests (ETag / `If-None-Match`): tested, a 304 without a token still uses up the 60-request quota (GitHub only skips counting 304s that carry credentials), so it does not help in the case that hits rate limits most.

**Index**:

```json
{"market_version": 1,
 "name": "Example extensions",
 "description": "Example marketplace",
 "homepage": "https://example.com/",
 "extensions": [
   {"manifest": {"manifest_version": 1, "id": "example-notes", "name": "Example Notes", "version": "1.2.0", "…": "…"},
    "tags": ["notes"],
    "packages": [
      {"platforms": ["linux-amd64", "linux-arm64"], "url": "dist/example-notes-1.2.0-linux.tar.gz", "sha256": "<64 hex chars>", "size": 123456},
      {"url": "release:v1.2.0/example-notes-1.2.0.tar.gz", "sha256": "<64 hex chars>"}]}]}
```

- `manifest` is the extension's whole `persona-extension.json`: its permissions and contributions can be seen without downloading. The manifest in the downloaded package must match it (id, version, permission summary), otherwise it is not installed.
- `tags` are the labels shown in the marketplace. The extension repo generates them from the manifest's `tags` and takes them out of `manifest` when writing the index (older hubs do not recognize `tags` in a manifest and would drop the whole entry); when an entry has no `tags`, the hub uses the ones in `manifest`.
- `packages` are searched in order for the first whose `platforms` includes this hub (`GOOS-GOARCH`); one without `platforms` counts as usable anywhere. `url` is a path relative to the index (for GitHub and git marketplaces, a path in the repo; git marketplaces cannot escape the repo through links),
  an HTTPS address, an immutable `oci:<registry>/<repository>@sha256:<64 lowercase hex>` reference, or for GitHub marketplaces `release:<tag>/<asset name>` (downloads the Release asset through the API; private repos work too). `sha256` is required and checked after download; on mismatch it is not installed.
- Broken entries (invalid manifest, no packages, non-https address, the same id twice) are dropped with a warning and the rest work as usual; a whole index whose `market_version` is not 1 or that has no `name` is rejected.
  Limits: 4 MB per index, 500 extensions, 32 marketplaces per hub.

**Endpoints** (full-access devices; the admin UI has the same set under `/api/v1/admin/extensions/-/…`, going through its session):

| Endpoint | What it does |
| --- | --- |
| `GET /api/v1/extensions/-/markets` | `{markets: [{id, spec, name, description, homepage, has_token, extensions (count), warnings, error, fetched_at, added_at, official?, signature: {state, key_id?}}]}` (`signature`, see "Signing" below, is also on the old path); the official marketplace comes first with `official: true`, the rest by registration time |
| `POST /api/v1/extensions/-/markets` `{spec, token?, token_from?}` | Reads the index once first; if it cannot be read it is not registered (400, `error` gives the reason: bad address, private needs a token, wrong token); returns the marketplace. `token_from` is the id of another registered marketplace whose token to use (one account's token reading several private repos); the caller never sees the token, and only one of it and `token` may be given; on the command line `market add <spec> --token-from=<marketplace id>` |
| `DELETE /api/v1/extensions/-/markets/{id}` | Stop reading this marketplace; extensions installed from it stay, but their auto-upgrade pauses with `auto_upgrade_paused: "market_removed"`. Add its original registration address again to restore upgrades under the saved settings |
| `POST /api/v1/extensions/-/markets/{id}/refresh` | Re-read the index now; on a bad signature returns 400 `market_signature` (the last good index is kept) |
| `PUT /api/v1/extensions/-/markets/{id}/token` `{token}` | Replace the token (empty string removes it) and re-read |
| `GET /api/v1/extensions/-/catalog?q=&market=&refresh=1` | `{markets, extensions: [CatalogItem], platform}`. `q` searches id, name, description, publisher, tags (case-insensitive); indexes older than 6 hours are re-read in the background, `refresh=1` waits for all of them to be re-read |
| `POST /api/v1/extensions/-/catalog/preview` `{market, extension}` (the old field name `plugin` is also accepted) | Download, verify, return a preview (the same as the "Install" preview, plus `market`); if already installed it is an upgrade preview |

`CatalogItem`: `market`, `market_name`, `id`, `name`, `version`, `description`, `publisher`, `homepage`, `tags`, `contributes` (extension point → id),
`permissions` (a few human-readable lines, same as before install), `problem` (why it cannot be installed: no package for this hub's platform, needs a newer hub, bad marketplace signature), `installed` (installed version),
`installed_source`, `from_this_market`, `upgradable`.

**Who can do it**: full-access devices can register / delete marketplaces; when a preview comes from a marketplace they can install and upgrade with it (`POST /api/v1/extensions/-/install`, `…/{id}/-/upgrade`, with the permission summary as usual),
and can roll back and uninstall extensions whose source is recorded as `market:<marketplace id>/<extension id>`. Why: a full-access device can already run arbitrary commands on every computer through an Agent, so opening up marketplaces does not widen the trust boundary;
packages are pinned by the sha256 in the marketplace index, and permissions are listed before install as usual. Read-only and answer-only devices cannot.

**Progress in the app** (iOS / iPad / Mac, #3307; protocol in `docs/protocol/protocol.md` ("Extension install, upgrade and uninstall progress")): when the hub reports `extension_operations` (and the WebSocket `?features=` also declares it, otherwise no events arrive),
tapping **Install** / **Update** sends `catalog/preview` with `async: true`, and an **Active** section appears right away on the extensions page, drawing the steps and a download progress bar from `extension_operation` (a ratio only when there is a `total`, otherwise an indeterminate bar).
When it stops at `needs_consent`, the preview page pops up by itself (if you close it and want to install after all, tap **Review Permissions**); agreeing calls install / upgrade with `async` and the same operation continues, declining cancels it with `DELETE …/operations/{id}`.
Uninstall is still a synchronous call (extensions that depend on it still get a 409 asking first), and the hub also records it as an operation and pushes steps. On failure it shows which step it stopped at and the `error`; **Retry** on a `retryable` one goes through `POST …/retry` (no question again if the package is cached and the permissions were already agreed to),
and ones that cannot resume preview again (for uninstall, uninstall again). Operations live in the app-global `ExtensionOperations`, so they are visible after leaving the page, coming back or reconnecting (`GET …/-/operations`), and marketplace list rows are marked **Installing**, **Waiting for You**, **Install failed**.
Older hubs have none of this: they preview synchronously as before and wait for the install request to return, and the section only says **Installing…**, what it is doing and how long it has been. Step names are the app's own copy; unknown step ids are shown as-is.

### Official marketplace

(#2351) The public repo `github:nwverse/persona-extensions` is the official marketplace. Every hub has it without registering it by hand (`extension.Official`, `server/internal/extension/official.go`):

- **When it is added**: the first time the hub loads marketplaces (same for a fresh install and for an upgrade from a version without this feature), it registers it and reads the index once in the background;
  the setting `extension_official_market` records that it has been added (it records the spec), so it is **added only once**: if the user deletes it, it does not come back by itself; to have it again, add it by hand.
  If the official spec changes later, it is added once more under the new spec.
- **No duplicates**: if the user had already added the same repo by hand (a `github:` / `https://github.com/…` / `git:` address, with or without a branch, different case), it is not added again and that one counts as the official marketplace;
  while the official marketplace is there, adding or moving another marketplace to the same repo in any form is refused. Address moves recheck exact-address and official-identity uniqueness under the final lock after fetching the target.
- **Pinned signature**: every official alias uses the compiled-in key, including manual re-registration and cached catalogs. Unknown saved owner pins remain unchanged and isolated; there is no trust endpoint. Release integration fixes the official key id at `3cce0f471911a1cf` without rotating it for embedded signatures. A shipped update can explicitly list recognized retired official pins in `extension.Official.PreviousSigningKeys`: only exact matches are replaced, across all official aliases. Removed markets stay removed, unsigned registrations and unknown pins are not migrated, and a failed write leaves loading retryable. The retained old envelope or legacy detached payload cannot authorize installation until a complete new envelope verifies against the new pin.
- **Order**: the official marketplace comes first in `markets` and in the catalog, the rest by registration time (all ordering is done on the hub; all three clients show the hub's order). The web (settings and `/admin`) and Android show an **Official** badge based on `official: true`; iOS / macOS do not yet (separate task).
- **Name** (#5017): the official marketplace is always displayed as **Official extensions** (Japanese: 公式拡張機能, the shipped Japanese label), not the index's `name` (which names the publisher).
  The hub fills `markets[].name` and the catalog's `market_name` in the owner's interface language (`marketName`, `extension_market_catalog.go`); when the web, Android, iOS / macOS see `official: true`, they localize it again,
  so it also works with old hubs. Only display changes: spec, index, signature, and market id remain unchanged; `personad extension market list` uses this name too.
  In iOS / macOS Settings › Extensions, Extension marketplace is above installed extensions (also above extension cards on Mac).
- Users' own marketplaces, such as ones on a company network, stay as they are.

**Publishing a marketplace**: put `persona-extensions.json` at the repo root, put packages in Release assets (or in the repo, or on a website), and write their `sha256sum` into the index. The extension repo's packaging script generates the index along the way.

### Signing

The only catalog file is `persona-extensions.json`. The official catalog embeds its signatures in one envelope:

```json
{"payload":"{\"market_version\":1,\"name\":\"Official extensions\",\"extensions\":[]}",
 "signatures":[{"alg":"ed25519","key_id":"<first 16 hex of SHA-256 of the 32-byte public key>","sig":"<base64 signature>"}]}
```

The signature covers **the exact UTF-8 bytes of the JSON-decoded `payload` string**. Do not trim it, regenerate the index JSON or sign the outer JSON literal. Outer formatting may change; payload whitespace, newlines and non-ASCII text may not. Exact duplicate keys are rejected in every object; known protocol struct fields also reject noncanonical case and Unicode-fold aliases. Arbitrary maps such as configuration properties retain distinct `Foo` and `foo` keys. Signature objects use only `alg`, `key_id` and `sig`; unknown signature fields, malformed envelopes and invalid UTF-8 are refused. No adjacent signature file is fetched.

Only the official marketplace requires signature verification, using the compiled-in key rather than catalog-supplied keys. GitHub, git and direct HTTPS catalog aliases, token-bearing reads, manual re-registration and cached catalogs all use that gate. A failed signature blocks installation and upgrades, including the last good catalog and every bundle member. Only a catalog that was actually read counts: a missing envelope, a malformed one, a bad signature or another signer marks the market `invalid`. A read that gets no catalog (404, 401, another HTTP status, a timeout or network failure) records the error and changes nothing else: the last verified catalog and its `signed` state stay, and installs keep using it. The two are typed failures (`extension.ErrCatalogUnavailable`, `extension.ErrCatalogUnverified`), never told apart by message text; the refresh endpoint answers `market_unreachable` for the first and `market_signature` for the second, and a later network failure does not lift a quarantine. An official market cannot move to an unofficial address to bypass verification. Preview-time source and signing-key snapshots are checked again at commit.

Nonofficial catalogs normally publish a plain index with **no `signatures` field**. Legacy `signing_key`, saved TOFU keys and key-change states are neither trust grants nor permanent locks. A structurally valid embedded nonofficial envelope may be parsed, but is reported as unsigned and acquires no saved key or sandbox exemption. There is no market trust CLI command or HTTP endpoint.

Postgres retains the complete envelope and payload string, not a regenerated index. Every manager restart re-verifies cached envelopes. Legacy official caches that cannot be verified remain isolated and refresh in the background. Unknown saved owner pins remain unchanged and isolated until an approved migration; failed database operations remain retryable. The once-added marker prevents deleted official marketplaces from being silently re-added.

Release integration fixes the official key id at `3cce0f471911a1cf` and the final source address at `https://github.com/nwverse/persona-extensions`. Embedding signatures does not rotate that key. Publishing private keys stay outside repository files, logs and tests and are available only to the signing step. Repository cutover, package publication and visibility changes are separate release gates; a passing fixture is not final-key or production verification.

### OCI packages and mirrors

OCI packages use immutable references: `oci:<registry>/<repository>@sha256:<64 lowercase hex>`. Tags, credentials, queries, fragments and path traversal are refused. The publisher emits a schema-version-2 OCI manifest with `artifactType: application/vnd.persona.extension.v1`, an empty or standard config object, and exactly one layer with `mediaType: application/vnd.persona.extension.package.v1+gzip`. The layer annotation `org.opencontainers.image.title` is a safe `.tar.gz` filename.

Verification checks the digest of the manifest's original bytes, the layer digest and size, then the catalog package's `sha256` and required OCI `size`. Packages cannot exceed 200 MiB. The download keeps the archive filename so unpacking works; cached packages are checked again on use.

Anonymous registries may challenge for a short-lived bearer. Realms, services and scopes are bound to the current registry and exact repository's pull namespace. Private HTTPS catalog credentials are usable only by a registry and realm at that same origin. A GitHub marketplace token may authorize only the derived `ghcr.io/<owner>/<market-repo>/…` namespace, through GHCR's standard `/token` realm and pull scope. Another organization, marketplace or registry never inherits that token. Git clone tokens never authorize OCI requests. Both the market credential and issued bearer are stripped on external CDN and cross-namespace redirects; authentication endpoints cannot redirect. When the registry refuses the market credential (401 / 403 at the token endpoint or on the manifest / blob request), the download tries once more anonymously and pulls the rest of that package without it (#2042): a token that can read the catalog repository may still lack package access (on GitHub, `read:packages`), and a public package needs none. A package refused both ways reports that the token may lack permission to read packages.

Mirror settings belong to **one extension manager**, not a globally distributed hub setting:

| Endpoint (full-access device, or the existing admin session prefix) | Body / response |
| --- | --- |
| `GET /api/v1/extensions/-/market-settings` | `{"registry_mirror":"","catalog_mirror":""}` |
| `PUT /api/v1/extensions/-/market-settings` | Both fields; empty strings clear them |

`registry_mirror` is an HTTPS registry base, optionally with a path prefix; requests use `<base>/v2/<repository>/…`. `catalog_mirror` is a complete official catalog JSON URL, ignored for nonofficial markets. Neither setting changes specs, package references, digests or install origins. URLs cannot contain credentials, queries, fragments or noncanonical paths. Loopback HTTP is reserved for isolated registry tests. Settings use the manager's existing Postgres setting store, with retryable reads and saves. Opening settings does not initialize markets or fetch catalogs. Clients select the manager through the existing manager query routing.

Mirrors have an independent eight-second budget and receive **no market token**, even when their origin matches the market. Their bearers are independent and origin-bound. Missing files, timeouts, partial archives and any catalog/manifest/blob/package verification failure fall back upstream with a new staging file, hash and download context. A catalog mirror cannot downgrade the official catalog to unsigned. The configured GitHub network proxy covers GHCR, token and GitHub CDN traffic separately from mirror settings.

A catalog mirror provides no repository-relative package files. For a git market, those downloads lazily synchronize the checkout from the original clone URL/ref with its original credential restrictions and bounded context, then verify the package SHA256. OCI and HTTP packages do not require a git clone, even when their catalog came from a git market.

## Auto-upgrade

For extensions installed from an extension marketplace (source `market:<marketplace id>/<extension id>`), the hub upgrades them itself when the marketplace has a new version (#1699). Built-in ones, ones in manager.json, and ones installed from elsewhere
(`dir:`, `file:`, `https://…`, `remote:`) never take part, and neither does an attached extension whatever its origin ("Attached extensions"). Code is in `server/internal/extmgr/extension_autoupgrade.go`.

- **Reading marketplaces**: once a minute the hub checks which marketplaces are due: last read 6 hours ago (plus a random 0–30 minutes per marketplace to spread them out); failed reads back off 15 minutes, 30 minutes… doubling, up to 6 hours.
  The log gets one line when a marketplace becomes unreadable and one when it recovers, not one every time. When the app opens the catalog (`catalog`), indexes older than 6 hours are still re-read in the background as before.
- **When to upgrade**: the source marketplace is still registered, the new version is higher than the installed one, usable on this hub (has a package for its platform, `engines` satisfied), the extension is enabled, both the master switch and this extension's own switch are on (both on by default),
  **it asks for no more than the user approved** (`MorePermissions` is false; the permission summary equals `granted_digest`), and the extension is idle: no calls in flight, no open HTTP requests or WebSockets
  (a connector always holds its extension, which does not count as in use), and the assistant is not working (its next step might call this extension's tools). If busy, check again the next minute.
- **How to upgrade**: the same path as a manual upgrade: download per the marketplace index, verify sha256, preview, then upgrade (in the audit log the actor is `自动升级` (auto-upgrade)); stop the old, start the new, and if the new version does not come up within 30 seconds, roll back to the old one (`watchUpgrade`, actor `hub` in the audit log).
  **A rolled-back version is recorded as skipped** and never auto-installed again (regardless of who upgraded it originally), until the marketplace has a higher version.
  That holds for a rollback the user starts too (the admin page's **Roll back**, `POST /api/v1/extensions/{id}/-/rollback`, `personad extension rollback`, #1826): the version left is skipped with `skipped_by: "user"` on the newer version's `upgrade` (and its "upgraded" note goes), so the apps say the user rolled back from it rather than that it didn't start, and nothing is pushed. A version that failed to install (download or verification failed) is retried the next time the marketplace yields a new index.
- **Disabled extensions** (#1525): while an extension is turned off, the hub neither upgrades it nor tells the user of a version that asks for more; its newer version shows `state` `off` with a `detail` saying it is paused,
  and `ExtensionView.auto_upgrade_paused` is `disabled` while its marketplace remains registered. A disabled extension has no process, so `watchUpgrade` could not tell whether the new version comes up, and a broken one would only show itself when the user turns the extension back on.
  Disabling does not write to the switches: `auto_upgrade` keeps the extension's own setting (and `PUT …/auto-upgrade` still changes it), and once the extension is turned back on that setting applies again at the next check.
- **Removed marketplaces** (#1895): extensions installed from a removed marketplace stay installed, but their details report `auto_upgrade_paused: "market_removed"`, including when the extension is disabled. Upgrade checks skip them, offer no `upgrade` candidate and do not download a new version.
  The hub keeps each extension's `auto_upgrade` preference, and `PUT …/auto-upgrade` can still change it. Add the marketplace again with its original registration address (`spec`) to restore its id and resume upgrades under the saved settings; an extension that is still disabled then reports `disabled` instead. If the address was edited after registration, use the original address that generated the id in the extension's origin.
  An unreadable catalog, a bad signature or a missing catalog entry does not mean the marketplace was removed. Client handling of this reason (disabled switches, an explanation of how to resume, and affected-extension counts in deletion confirmations) remains follow-up work.
- **More permissions**: no upgrade; the user is told once ("Example UI 1.2.0 needs one more permission: broader network access (api.example.com), go confirm"), and the upgrade happens only after the user confirms in the app
  (through the marketplace preview and upgrade, with the permission summary).
  While the master switch or the extension's own switch is off, such a version (more permissions, a changed permission summary, or new dependencies or pack members to confirm) is only shown on the extension's page (#1828):
  its `upgrade` still says `needs_permission` with what it asks for, but there is no `upgrade_note`, no broadcast and no push, and it is not recorded as told, so the user is told at the first check once both switches are on again.
  A new name in top-level `secrets` counts too, whether required or optional and even when absent from `node.secrets`: the marketplace candidate immediately shows `needs_permission` with "Use more secrets" and the new secrets' titles.
  Automatic checks do not download the package; the user starts a marketplace preview to review and confirm it. Repeated checks, marketplace refreshes and manager restarts do not repeat the notice for that version. Keeping, removing or reordering secret names, or changing only their titles or required flags, adds no secret permission.
- **Telling the user**: success, rollback, waiting for confirmation and install failure are all recorded on the extension (`ExtensionView.upgrade_note`); online apps receive `{"type": "extension_upgrade", "extension", "note"}`;
  the first three also send a quiet push (`thread-id` `plugin_upgrade`, `interruption-level` `passive`, with both `plugin_upgrade: {plugin, kind, version}` and `extension_upgrade: {extension, kind, version}`).
- **Where it is stored**: the hub setting `plugin_upgrades` (master switch, extensions switched off, skipped versions, versions already announced, each extension's last result); no new table.

`ExtensionView` has these extra fields for marketplace extensions: `auto_upgrade` (this extension's saved switch), `auto_upgrade_paused` (why that switch does not apply now: `market_removed` when its source marketplace is no longer registered, otherwise `disabled` when the extension is turned off; omitted otherwise),
`upgrade` (when there is a new version: `{id, name, market, from, to, state, added_permissions?, detail?}`,
where `state` is `available` installs at the next check / `waiting_idle` waiting for idle / `needs_permission` waiting for you to confirm / `off` switch is off, or the extension is disabled / `skipped` was rolled back / `failed` did not install / `incompatible` cannot be installed),
and `upgrade_note` (`{kind: upgraded | rolled_back | needs_permission | failed, from, to, detail?, at}`).

**Endpoints** (full-access devices; only present when `features` includes `extension_auto_upgrade`):

| Endpoint | What it does |
| --- | --- |
| `GET /api/v1/extensions/-/upgrades` | `{auto (master switch), upgrades: [the upgrade above], off (extensions switched off individually), last (extension → upgrade_note)}` |
| `PUT /api/v1/extensions/-/upgrades` `{auto}` | Master switch; returns the same as above |
| `PUT /api/v1/extensions/{id}/-/auto-upgrade` `{auto}` | This extension's switch; one not installed from a marketplace returns 400 `not_from_market`; returns the details |

## When it runs; crashes and timeouts

A process only runs when "something needs it":

| | When it starts | When it stops |
| --- | --- | --- |
| `on_startup` | As soon as the hub is up and the extension is enabled | Never |
| `on_call` | The first time the hub calls it (a call in progress counts as needing it) | After 10 minutes with no calls |
| `on_http` | When the first request forwarded to it arrives (waits for its `/health` to pass, up to 30 seconds) | After 10 minutes with no requests and no open connections (WebSockets count) |
| Contributes a connector | Starts while the connector is on (the connector holds it) | When the connector is switched off in settings and nothing else needs it; `on_call` does not count for such extensions |

- On exit it restarts with backoff: from 2 seconds, doubling, up to 5 minutes; running for a full minute counts as recovered. An extension installed from `remote:` is reached, not started: see "Attached extensions" below.
- An HTTP-only extension the hub started whose `/health` does not answer within 30 seconds becomes `error` (reason `not answering /health within 30s`) and restarts with the same backoff, instead of staying in `starting`.
- Status: `stopped` (not running, nothing needs it, not an error), `starting`, `running`, `error` (with a reason), `disabled`, `needs_setup`, `incompatible`.
  Clients (app, web, admin UI) show both `running` and `stopped` as **On**: to the user an extension is only on or off; an idle extension starts by itself when used,
  so there is no need to tell them apart. Whether it is running is only noted in small text in the details ("Running" / "Not running; starts when needed"). On-demand start and idle reclaim stay as they were (saves resources and battery, #1670).
- Calls time out after 30 seconds by default (2 minutes for connectors) and return `timeout`; 3 timeouts in a row restart it. Extensions that speak the protocol are `ping`ed every 30 seconds, and restarted after 3 missed replies in a row.
- Once up, an HTTP-only extension the hub runs gets a `/health` request every 10 seconds (each waits up to 5 seconds). If it fails, the extension is marked `error` and checked every second; after 3 failures in a row it is restarted (also when the program is still running but hung).
- Stopping a process: close stdin first, SIGTERM the process group after 5 seconds, SIGKILL after another 5 seconds (extensions that do not speak the protocol get SIGTERM directly). Child processes end with it.
- A problem in one extension only affects its own contributions; hub startup never waits for extensions. stderr (plus stdout for ones that do not speak the protocol) goes line by line into the hub's log, prefixed `plugin <id>:`, each line truncated to 4 KB;
  it also goes into this extension's own log ("Runtime status" below), viewable per extension in the admin UI.
- Installed extensions run in the sandbox by default, with memory / CPU limits (Linux); see "Sandbox" and "Egress" under "Permissions".

## Attached extensions

An extension installed from `remote:rpc=<socket>,http=<socket>` runs on its own; the manager only reaches it on its sockets and never starts, stops or replaces its program. Such an extension is **attached**.
Activation still decides when the manager connects (an `on_startup` one is connected from the start, an `on_call` one at its first call). Code is in `server/internal/extension/host` (`Spec.Attached`, `Proc.Link`) and `server/internal/extmgr` (`ExtensionView.attached`).

- **Online**: one that speaks the protocol is online while its socket connection is open and its `initialize` answer was accepted; pings (every 30 seconds, 3 missed in a row) and calls (3 timeouts in a row) take it offline as for any other extension.
  An HTTP-only one is online while its last `GET /health` answered 200 (checked every 10 seconds).
- **Offline**: the manager tries again every **10 seconds**, with no doubling. For **30 seconds** after it went offline, or after it was needed while not connected (an `on_startup` one from when the manager began, an `on_call` one from a call while it was `stopped`), its status stays `starting`, with no error, so a restart of the program does not show as a failure;
  after that it is `error` with the reason, and it stays `error` until it is back (tries that fail alike change nothing) or, for an `on_call` one, until nothing has used it for its idle time. It is then `stopped` instead of tried again when the wait after the try in progress ends: about 10 seconds after that try ends, so up to about 40 seconds after its idle time for a socket that accepts but never answers `initialize` (that try waits 30 seconds for the answer).
  Until then a call fails at once on the `error`; once it is `stopped`, its next call gets the 30 seconds anew.
  A call while it is `starting` (or `stopped`, for an `on_call` one) waits for it, up to 30 seconds, as for any extension that is starting.
- **Refused**: one that answers `initialize` as another protocol, id or version is `error` at once and tried again with the usual backoff (2 seconds doubling, up to 5 minutes): every `initialize` hands it its secrets and a new hub token, and nothing changes until the user acts. An `on_call` one that nothing uses any more is `stopped` when that wait ends.
- **Version differs**: the manager never uses a version the user did not approve. When the program says another version than the installed one, the view's `attached.version` says which, and `error` says how to approve it on the hub machine:
  `personad extension upgrade <id> <its remote: source>` (the source is under **Installed from** in the admin UI, `origin` in the details). Approving restarts the connection at once.
  A program that says it is another extension, or speaks another protocol, is refused for that, and its version is not shown.
- **Log and runs**: the manager logs a failure, and counts it as an exit in "Runtime status", only when what failed changes (a closed connection after it was online, then a missing socket, a refusal with other details, …), not at each try; it logs when it is back (`connected after <duration> offline`), and a run starts each time it comes online.
- **Auto-upgrade**: never, whatever the origin ("Auto-upgrade" below): the manager has no program of it to replace.
- **API**: the `ExtensionView` (list and details) of an attached extension has `attached`: `{online, since?, error?, version?}`. `since` is when it came online or went offline (none while it is switched off or nothing needs it); `error` is why it is offline, also while its status is still `starting`;
  `version` is set only when the program says another version than `version`. Other extensions have no `attached`.
- **Clients**: the web's Settings › Extensions and the admin UI keep the status as before (`starting` is a gray pulsing dot, `error` red) and add a line: "Runs on its own · online since 10:42", "… reconnecting, offline since 10:40: <why>", "… offline since 10:40", or "Runs on its own; connects when needed";
  the details' version reads "1.0.0 approved, the program is 1.1.0" (the admin list shows a red **program 1.1.0** tag). `personad extension list` adds `独立运行，在线（自 …）` or `离线（自 …）` and the program's version. The iOS / Mac and Android apps show the status as they always have.
- **Old and new**: older apps and managers ignore `attached`; the protocol is unchanged, so a program written for an older manager is attached as it is.

## Source runtime (#5956)

The [2026-11-07 migration and acceptance record](../archive/extensions/source-extension-migration-2026-11-07.md) separates merged implementation, native-client evidence, complete source-package acceptance and outstanding production/platform gates. It is a historical snapshot, not deployment authorization.

Executable packages retain the default `runtime.mode: "process"` behavior. A source package explicitly selects `"deno"` and carries a prebuilt, self-contained JavaScript module:

```json
{"runtime":{"mode":"deno","source":{"entry":"src/main.tsx","bundle":"bundle/main.mjs","sha256":"<bundle SHA-256>"},"resources":{"memory_mb":128,"cpu_percent":20}}}
```

The module default-exports an SDK `Program`; the manager supplies a separate, embedded trusted stdio adapter using the existing `connect` protocol. A page can use `ui: { home: componentPage(request => <Home key="home" />) }` from the TypeScript SDK. The root factory runs once per manager-owned session; `Home` returns the fixed native View/Section/component vocabulary and may use state, effects and sanctioned fetch/timer hooks. Service namespaces remain ordinary `Program.namespaces` handlers and keep working while a page is hidden.

The additive process bridge handles `ui.page.supported`, `mount`, `snapshot`, `event`, `suspend`, `resume` and `dispose`; asynchronous source renders notify `ui.page.snapshot`. Only opaque callback IDs and native JSON leave the isolate. The manager authenticates owners and process generations, checks saved displayed Actions through native confirmation/write gates, fits snapshots on detached copies, and computes lossless native deltas. A source callback's authorization is never user approval. Every invoked or ambiguous event consumes a wire revision, even when its delta has no operations; bounded receipt eviction cannot replay that write. Source render revisions and coalesced wire revisions are independent.

Apps explicitly negotiate `extension_ui_stream_v1` plus `context.stream` on the existing app WebSocket. Canonical envelopes, lifecycle and bounds are specified in [the schema README](../../schema/extension/README.md). Private frames target one authenticated connection and never enter the global broadcast/replay log. Snapshot-only HTTP callers of component pages use owner-bound server sessions with opaque version checkpoints; callbacks/state survive requests, but UI-only work suspends after each response. Widgets and system surfaces continue to use full fitted snapshots. Production activation and client-renderer acceptance remain separate gates; injected Node-process tests are transport evidence, not Deno sandbox or native-client E2E.

Open retries keep the original request ID and complete request payload. While its owner-bound session exists, concurrent duplicates and retries after a lost initial snapshot recover the same allocation; another live connection cannot steal it. A disconnected retry uses the normal snapshot-first resume path. Independent pages and explicit remounts after expiry use new open IDs. Lifecycle errors expose manager-owned structured codes: `expired` permits an explicit fresh mount, while `inactive` is a transient suspension/busy condition and must not allocate a replacement. `invalid`, `forbidden`, `quota`, `negotiation` and generic `rejected` remain distinct; localized error text is display-only, never a recovery or authorization signal. These identities do not apply to legacy HTTP checkpoint sessions.

- `personad extension new <id> --template deno` creates source scaffolding; `pack` bundles with Bun (no lifecycle scripts or macros), updates the artifact digest and uses the existing package/market signing flow. Authoring inputs and SDK copies stay out of the package. `entry` is never evaluated during activation. The artifact is nonempty, limited to 16 MiB, and rejects traversal and symlinks. A pinned ECMAScript parser rejects import statements, re-exports, dynamic imports and import metadata, including imports inside template expressions; ordinary strings, comments and regular expressions remain valid. Unsupported syntax fails closed, and dependencies must be erased during bundling.
- Source validation has its own **disposable native process**, separate from the manager heap and from the eventual guest. The manager streams a bounded artifact after checking its signed hash and contained path; the child checks the same stream's hash before constructing or walking an AST. A fresh systemd scope limits memory to 128 MiB, swap to zero, CPU to 50% and tasks to 32, with a five-second external deadline. The supervisor verifies actual controller membership and kernel limits before entering bubblewrap; the parser rechecks them inside the namespace. Bare helper invocation, partial delegation and unavailable controls fail before parsing. Only the trusted native executable, exact ELF libraries and its own read-only cgroup metadata are mounted; there is no artifact directory, proxy, configuration, secret, inherited environment or guest execution. Results use a fixed 16-byte buffer, stderr is discarded, and cancellation kills/reaps the process and disposes its unique scope. Admission allows one active validator and two queued requests; excess requests fail immediately.
- Preview/build/activation use the process validator. Commit-time checks under the registry lock recheck only the already-validated signed digest and package fingerprint, so another extension's reads do not wait for a parser. Activation validates again outside that lock. Production resource acceptance remains deployment gate #5975. The explicit development policy below is shared by `sandbox.ValidationProcess` / `RunValidation` and `sourcevalidate.Run`; no manager-side AST construction or independent parser fixture bypass exists. The validator prefers a verified kernel scope, including `pids.max <= 32`; a missing resource controller may select only the tagged manager's exact opt-in. Both parser-entry gates remain mandatory.
- The manager's `manager.json` supplies `extension_runtime: {"path":"<absolute pinned executable>","version":"2.9.6","sha256":"<runtime SHA-256>"}`. Installation is explicit offline administration. Activation verifies the bounded executable, its digest and actual version; it never searches PATH, downloads a runtime or discovers project configuration. The trusted adapter is content-addressed separately from extension source.
- **Production:** one shared pinned installation runs **one independent process and Linux namespace sandbox per extension**, inside a separate systemd resource scope. Exact kernel cgroup files and this scope's memory/CPU membership are checked **before any guest executes**, on both cgroup v1 and v2. A working user bus or accepted systemd properties alone is not enforcement. Missing namespaces, resource delegation, memory/swap denial or CPU quota fail closed. Trusted markets and owner sandbox switches cannot opt out. The explicit development resource exception below does not apply to production builds.
- Read-only mounts contain only the runtime, adapter, own bundle, exact ELF dependencies and trusted launcher. No manager directory, other extension, host data or general system directories are exposed. Deno can read only its bundle, connect only to its registered loopback broker, and cannot access environment variables, write files, spawn processes, use FFI or load remote/npm modules. String-based code generation and worker creation are disabled.
- Global `fetch` uses a bounded broker over the **existing registered egress**. Deno's proxy-aware client still requires destination permissions, so proxy environment variables or a custom client alone are insufficient. Every target and redirect uses the registration's granted network rules; guest identity cannot select another registration. Registration replacement, settings changes and revocation cancel pending HTTP and tunnels. Input, output, bodies and pending queues are bounded. Source-only listeners also cap idle/pre-header sockets and header bytes; revocation closes sockets even before they submit a request.
- Config, secrets, scoped tokens, grants, idle reclamation, timeout strikes and selective shutdown remain manager/`host.Proc` responsibilities. Full hub access, command-line tools, agent-runtime sockets, remote process sockets and source-runtime node programs are not supported by this stage. Existing executable node programs remain unchanged; no source program silently falls back to an unsandboxed node.
- CLI source `test` verifies the package but refuses to label a bare engine launch as runtime acceptance. Actual-engine/namespace tests are separate from full resource acceptance; tests requiring unavailable enforcement explicitly report unavailable rather than passing via a developer bypass.

### Finite source-page snapshot loading (#1184)

Use `useLoadEffect(effect, dependencies)` from `@persona/extension/ui` for finite initial or navigation loads. It accepts the same abort-aware effect/cleanup forms as `useEffect`; ordinary subscriptions and long-lived effects stay on `useEffect` and are not awaited for a snapshot.

The official `componentPage` factory advertises `snapshotReadiness: 1`. For an owner-bound snapshot-only HTTP session, the manager supplies `transport: "snapshot"` and waits within the existing deadline for those finite loads and their synchronous state updates. It selects this from the session's legacy transport, not from `ui_version`. Native stream sessions and old factories without the readiness contract retain immediate loading snapshots and asynchronous deltas. A caller must not turn all effects into finite loads or increase budgets to hide a nonterminating effect.

Canonical `initialize` replies advertise `ui_page_cancellation: 1` for component pages. The host negotiates cancellation only for page mount/event/resume requests; SDK `rpc.cancel` targets that request's scoped `AbortSignal`, not service namespaces or a later invocation with another identity. Request cancellation, visibility and allocation-generation fences are rechecked after snapshot flushing. A canceled or suspended page cannot return a late success, and ambiguous callback receipts cannot authorize a replayed write. This does not promise preemption of arbitrary JavaScript or global serialization across independent allocations.

These are SDK/manager lifecycle contracts, not new pages, permissions, source release defaults or production isolation acceptance. An installed package must include the matching official SDK and be rebuilt; native JSON remains the only UI content sent to clients.

### Opt-in exact JSON integer request params (#1201)

The TypeScript SDK keeps `Handler(method, params)` and its default `JSON.parse` / `Number` behavior. A namespace that needs the original request tokens explicitly wraps its handler with `withExactParams`. Only inbound namespace RPC requests get the third `ExactCall` argument; events get `undefined`. This does not change tool schemas, replies, outbound calls/events, state, HTTP response parsing, authorization, egress or resource gates.

```ts
import { withExactParams, type Program } from "@persona/extension";

export default {
  id: "example", version: "1.0.0",
  namespaces: {
    connector: withExactParams((method, legacyParams, call) => {
      if (method !== "tools.call" || !call) return {};
      const args = call.params?.field("args");
      const { topic, replies } = args?.int64Fields(["topic", "replies"])
        ?? { topic: 0n, replies: 0n };
      // Apply the existing tool's required/positive/range checks here.
      // Render exact values as text; JSON.stringify cannot serialize bigint.
      return { topic: topic.toString(), replies: replies.toString() };
    }),
  },
} satisfies Program;
```

`legacyParams` is still the ordinary parsed object. `call.rawParams` preserves the selected original JSON text, including numeric lexemes; `call.params` is an `ExactJSON` view of it. The metadata selects the last Go-folded `params` RawMessage field (escaped/case-aliased keys included), without changing the legacy argument's exact-key lookup. Missing params (`undefined`) and JSON null (`raw === "null"`) stay distinct even though the legacy handler argument still defaults to `{}`. The view is made from raw input, never by converting a rounded Number to a string. `ExactJSON.parse(rawText)` also exposes the same validated view for explicit local use.

- `member(name)` chooses the last exact JSON-decoded object key, including null, like a `json.RawMessage` map. Escaped keys match their decoded names.
- `field(name)` chooses the last `json.RawMessage` struct field, including null, using Go's case-fold matching for an ASCII JSON field name (including the long-s and Kelvin-sign aliases). It does not merge repeated nested typed structs. A Go envelope with `Args json.RawMessage` first selects this last raw value, then decodes it separately.
- `fieldValues(name)` yields all folded raw field occurrences in order for custom decoders, including mixed string/integer IDs. Apply the existing decoder to each occurrence, preserving its own null/type/error rules; looking only at the last token would erase earlier errors.
- `int64(previous = 0n)` accepts only lexical `-?(0|[1-9][0-9]*)` within `[-9223372036854775808, 9223372036854775807]`, returning bigint. JSON null leaves `previous` unchanged. `1.0`, `1e0`, `1.0000000000000001`, strings, booleans and containers are errors, whereas `9007199254740993` and the full signed bounds are valid. Malformed numeric lexemes are invalid JSON and never dispatch.
- `int64Fields(names)` decodes a fresh, non-embedded Go struct of int64 fields with distinct ASCII JSON names and distinct folds. Missing/null fields start at zero, duplicate fields are read in order, null does not overwrite, and an earlier type/lexical/overflow error cannot be erased by a later valid value. Unknown fields are ignored. Failures use `JSONIntegerError` (`invalid_params`) across separately bundled SDK copies. This is Go lexical integer decoding, **not** JSON Schema's mathematical integer meaning.

The selected tokens are immutable, scanning is iterative (including deep unknown fields), and result field maps have null prototypes. The opt-in raw frame/view is bounded to 8 MiB of UTF-16 text; existing transport byte and process budgets are separate and unchanged. Extensions must keep their existing string-ID branch when a tool accepts both string IDs and numeric IDs; calling `int64Fields` unconditionally is not a migration of that mixed contract. There is no end-to-end exact JSON serialization promise.

### Development-only resource fallback (#5956)

The maintainer authorized this exception solely for isolated local development/test instances without delegated kernel memory/CPU controllers. **Production remains blocked by deployment issue #5975.** Never change cgroup, system services or live configuration to exercise it.

Both gates are mandatory:

1. Build on Linux with `go build -tags persona_dev_resources -o "$DEV_BINARY" ./cmd/personad` from `server/`. Normal release builds omit this tag and contain only rejecting stubs, not the child-limit implementation.
2. Set `PERSONA_DEV_SOURCE_RESOURCES=1` **only for the isolated manager process** (it is not a persisted configuration field). Tagged builds default off; normal builds reject the opt-in. If kernel controls are available, the normal verified scope is still used. Only a resource-probe failure after successful namespace verification is eligible.

Every fallback activation/restart emits a localized warning and the machine-readable marker `DEVELOPMENT_ONLY_RESOURCE_FALLBACK`; sandbox status includes the same warning. The filesystem/network namespaces, exact read-only mounts, registered egress/redirect checks, signed bundle and pinned engine, native permission gates, scoped tokens, and per-extension config/secrets remain unchanged. Missing namespace or mount enforcement refuses even in development.

| Mechanism | Development default | What it actually enforces |
| --- | --- | --- |
| Child `RLIMIT_CPU`, soft and hard | 30 seconds | Cumulative CPU time for this process lifetime, **not a CPU percentage/quota** |
| V8 `--max-old-space-size` | 64 MiB | Old-space heap ceiling, **not total process memory** |
| External child RSS watchdog | Kill above 256 MiB, sampled every 25 ms | Best-effort observed RSS termination threshold; allocation can overshoot before the next scheduled sample |
| External wall-clock watchdog | Five minutes per process lifetime | Terminates even an idle or blocked guest; normal host backoff can restart it |
| Child `RLIMIT_NOFILE` | 128 | Descriptor-table ceiling |
| Child `RLIMIT_FSIZE` | 8 MiB | Individual file growth ceiling; native writes remain denied by Deno |
| Child `RLIMIT_CORE` | Zero | Ordinary core-file size limit; host pipe-based crash collectors can ignore it |
| Existing RPC/adapter/egress bounds | Unchanged | Frame, concurrency, body and pending-I/O budgets |

A separate trusted child helper calls `setrlimit` and immediately `exec`s Deno. Neither the manager nor the supervising proxy changes its own limits. The supervisor kills only this child; namespace shutdown closes its proxy connections and the host closes pending RPCs. Lifetime expiration deliberately discards that development process's ephemeral state. The implementation does **not** use ineffective Linux `RLIMIT_RSS`, limit V8's reserved address space with `RLIMIT_AS`, disable the V8/native sandbox, or claim a hard total RSS cap. A pinned-engine measurement reserved about 18 GiB of virtual address space with ordinary RSS around 35–42 MiB; restricting that reservation would break runtime startup, not provide valid sandbox evidence.

For **native source validation**, the same explicit policy uses a two-second child `RLIMIT_CPU`, 128 MiB sampled RSS termination, a five-second deadline (including resource probing and queue time), and the same FD/file/core limits. Go applies a 96 MiB cooperative memory target, a 2 MiB per-stack runtime ceiling, 16 runtime threads and `GOMAXPROCS=2`; there is no V8 heap in the compiler/validator. These do not establish hard process RSS, CPU quota or kernel task-count enforcement. A sealed manager-created policy descriptor authorizes the launcher; bubblewrap consumes it into one exact read-only policy mount, the trusted supervisor re-seals it, and the parser verifies both that descriptor and its actual child rlimits before reading source. The namespace environment is cleared, so child environment variables, manifests and guests cannot opt in. Every validator launch warns separately. Production builds reject all development helper modes and opt-ins.

Race tests build the trusted limited child without race instrumentation and run the parent/infrastructure with `-race` separately; `PERSONA_TEST_VALIDATOR_HELPER` selects that already-built test helper only inside test code. TSAN startup failure is not an adversarial-parser pass. The validator's allocation comparison records parent and child **total allocation deltas**, not peak heap/RSS; sampled RSS peaks are separately reported and can exceed the watchdog threshold.

Resource-only executable verification (use the **same already verified engine**, not another installation):

```sh
GOTMPDIR="$TEST_BUILD_DIR" python3 -I scripts/test/test_source_development_resources.py \
  --engine "$PINNED_DENO" --sha256 "$PINNED_DENO_SHA256" \
  --log "$PRIVATE_NEW_LOG" --watch-file "$UNCHANGED_CONFIG"
```

The harness runs normal-build default/refusal and opt-in rejection, tagged default-off/explicit acceptance, real hostile CPU/heap/ArrayBuffer/wall-time cases with neighboring Deno survival and pending RPC/I/O cleanup, namespace/egress/native API/config separation checks, child descriptor/file-limit probes, and before/after controller/config comparisons. It starts no hub or node. Test budget overrides only tighten the defaults (for example one CPU second and one wall second). CPU death is distinguished from the longer caller timeout; heap failure is distinguished from sampled ArrayBuffer RSS termination. Raw output is private, with exact mechanisms and observed peaks, including overshoot. These are resource-layer tests, **not full hub/session/client E2E or production resource acceptance**. Manager page sessions, client renderers, Apple/platform verification and node-side source eligibility remain separate stages.

## Runtime status (#1857)

The hub records per extension how it is running, viewable on the extension page of the admin UI ([admin.md](../operations/admin.md) ("Extensions")). Code is in `server/internal/extmgr/extension_metrics*.go`;
the host side (every call, every run, every log line) is the `Observer` in `extension/host`.

- **Calls**: per method (`connector.status`, `ui.render`…) it records the count, error count (by error code) and a latency histogram (15 buckets from 1 ms to 60 s, plus one bucket for slower; p50 / p95 / p99 are interpolated linearly within the bucket).
  HTTP the hub forwards to the extension counts too, recorded by route: under the extension's own path it is `HTTP /<first segment>`, the endpoints contributed by `api` are `HTTP /api/v1/<prefix>` (deeper segments are mostly ids and are not split out);
  5xx and connection failures count as errors (`http_5xx`), 4xx only records the code (`http_4xx`) and is not the extension's error, and WebSockets count only the number, not latency. Calls abandoned by the caller (`canceled`) are not errors either.
  Each extension records at most 64 methods separately; any more go under `other`.
- **Runs**: each start, each stop because nothing needs it, and each error exit (restarted after backoff) is recorded once; exit reasons are counted by bucket (at most 16 kinds per bucket), and the last 20 exits keep their time.
  An attached extension's run starts when it comes online, and an exit is recorded when it goes offline or what keeps it offline changes, not at each try ("Attached extensions").
- **Logs**: each extension keeps its last 2000 lines (4 KB per line), with source `program` (its stderr, plus stdout for ones that do not speak the protocol) or `hub` (what the hub says about it: restarts, timeouts, rollbacks, i.e. the `plugin <id>: …` lines in the hub log).
  Before going in, anything that looks like a credential is scrubbed by rule (`Authorization` / `Cookie` headers, `Bearer …`, `token=` / `secret=` / `password=` / `api_key=` and the like, `?token=` / `key=` in URLs) along with the hub's own token, replaced with `***`;
  for what the rules cannot catch, the extension is responsible: no secrets, message bodies or numbers in logs (see "Process extension protocol"). The hub's system log (journal) stays the unmodified copy as before.
- **Retention**: one bucket per 5 minutes, 24 hours kept in memory; every hour closed buckets are written to the database (`extension_metrics` table, migration `0358`; when the hub stops, the current bucket is written too) and kept for 7 days;
  after a hub restart the last 24 hours are read back from the database. Logs are only in memory and are gone when the hub restarts.
- **Calls to old endpoints** (#1924): each call to `/api/v1/plugins…` or `/api/v1/admin/plugins…` is recorded by route in a counter named `-legacy` (also persisted) and listed in the overview, to judge whether the compatibility layer can be removed.

**Endpoints** (admin UI sessions only; the old path `/api/v1/admin/plugins/…` works the same):

| Endpoint | Returns |
| --- | --- |
| `GET /api/v1/admin/extensions/-/metrics?window=1h\|24h\|7d` | `{window, from, to, extensions: [{id, calls, errors, error_rate, p95_ms, starts, exits, last_exit?}], legacy_calls: [{route, calls (within the window), since_start (since this hub start)}]}`; `window` defaults to `24h` |
| `GET /api/v1/admin/extensions/{id}/metrics?window=` | `{extension, window, from, to, step_seconds, total, methods: [{method, …}], series: [{at, calls, errors, p95_ms}], starts, stops, exits, exit_reasons, recent_exits: [{at, reason}], counters}`; `total` and each method are `{calls, errors, error_rate, p50_ms, p95_ms, p99_ms, codes}` (the percentiles are `null` when there are no timed calls). Series step: 5 minutes for 1h, 1 hour for 24h, 6 hours for 7d |
| `GET /api/v1/admin/extensions/{id}/logs?since=<seq>&q=<text>&limit=<lines>` | `{lines: [{seq, at, source, text}], next, kept}`: lines after `since` (all if omitted), `q` filters case-insensitively, `limit` (default 500) keeps the newest ones; `next` is the sequence number of the next line |
| `GET /api/v1/admin/extensions/{id}/logs/stream?since=<seq>` | server-sent events: first the kept lines after `since` (or `Last-Event-ID`) (the last 200 lines if neither is given), then new ones live, one event per line (`id` is the sequence number, `data` is that line's JSON), with a keep-alive comment line every 25 seconds |

## How config and secrets reach the extension

- Config is stored in the database (`plugins.config`) and validated against the schema on change; the extension gets the copy with `default`s filled in.
- Secrets are encrypted with the manager's `secret.key` (like mail passwords, launcher keys and connector secrets; [managers.md](../assistant/managers.md) ("The built-in manager's own files")), one per row (`plugin_secrets`), and are **write-only**: endpoints only return "set / not set".
- Extensions that speak the protocol get `config` and `secrets` in `initialize`; ones that do not get them through the env vars `PERSONA_EXTENSION_CONFIG` and `PERSONA_EXTENSION_SECRETS` (both JSON).
  Each extension only gets its own. When config, secrets or the switch change, the hub restarts the extension's process (no hot reload).
- Env vars: `PERSONA_EXTENSION_ID`, `PERSONA_EXTENSION_DIR` (package directory), `PERSONA_EXTENSION_DATA` (`~/.persona/extensions/data/<id>/`, the extension's own cache and data, deleted on uninstall),
  `PERSONA_EXTENSION_SOCKET` (when it has `http`); extensions that asked for `hub` scopes also get `PERSONA_HUB_URL` and `PERSONA_HUB_TOKEN` (only those scopes, a new one each start, see "Permissions", "The hub's API: scopes"), and ones that accepted `hub_api` (the old form) get the full token. The old `PERSONA_*` names are given too.
- **Installed extensions do not get the hub's own environment**: the hub's environment may contain its own credentials (the database connection string in a Docker deployment), so installed extensions inherit only an allowlist
  (`PATH`, `HOME`, `USER`, `LANG`, `TZ`, `TMPDIR`, a few proxy and certificate vars, `host.PassEnv`), plus the ones above. Built-in ones and ones from manager.json still inherit the hub's whole environment.
- State a connector saves for itself while running (Telegram's session) still goes through `connector.secrets.*`, stored in `connector_secrets`, one copy per connector id: that is written by the program itself, separate from secrets the user fills in.

### Settings the user fills in (#5906)

For an extension whose user enters a service's address and key in 设置 › 扩展 (a gateway's quota extension, first), the
manifest can ask for four things. A hub from before them refuses the manifest, so raise `engines.persona`.

- **`permissions.network: ["config:<field>"]`**: the egress proxy lets the extension reach the address the user wrote in
  configuration field `<field>`, which must be a string of `format: uri` with no `default`. A manifest whose field has
  one is refused: the proxy would let the extension reach that address before the user wrote any, and an upgrade that
  only changed it would move everyone who never filled the field in without asking again (#6099).
  - Only that host and port, as written: no subdomain, no other port. A redirect to another host is refused like any
    host.
  - https only (a CONNECT tunnel) unless the entry is `config:<field>:http`, which also takes plain http.
  - The proxy resolves the name itself and dials the address it checked. It refuses the name when any answer is
    loopback, link-local (the usual cloud metadata address included), unspecified, multicast, a metadata address (`fd00:ec2::254`,
    `100.100.100.200`), or this machine's own address at the hub's or the proxy's port. Private network addresses are
    allowed.
  - The value is the user's: only the settings pages (and the hub) write an extension's configuration, and no method a
    program calls can (`TestExtensionCannotWriteItsSettings`).
  - When the settings change, the old address is refused at once (`Proxy.SetTargets`), before the restart.
  - The consent line is 「访问你在设置里填的地址」: the field's title, the address there now (「当前：…」, 未填 before
    install), and whether http is allowed. An upgrade that adds such an entry lists it under the permissions that grew,
    like a new host.
  - Only with the sandbox on does the proxy hold the extension to it; without the sandbox the extension reaches what it
    likes, as before.
- **`secrets[].hint: "last4"`**: once set, the detail's `secrets[].hint` is the secret's last four characters (none for one
  shorter than 12). The apps show 已设置 · 末 4 位 <hint>. It is read for the detail only, never logged or audited.
- **`settings_check: true`**: before saving a secret or the configuration, the hub calls the running program's
  `settings.check {config, secrets}`, with the values as they would be after the change. The answer is `{ok: true}` or
  `{ok: false, error}`.
  - A refusal saves nothing and is not audited. The route answers 400 `check_failed` with the extension's words.
  - Before the words go back, every secret handed to the check is replaced by `***`: whole, and any prefix or suffix of
    6 characters or more. The words are cut to 300 characters.
  - No answer in 10 seconds is a failure. A disabled or stopped extension can't be checked, so nothing is saved.
  - An extension waiting for its settings (`needs_setup`: a required secret or field not filled in yet) does not run,
    so it can't check them: those settings are saved unchecked, and it starts once nothing is missing (#6099).
  - At most 6 checks per extension a minute; more answer 429 `check_rate_limited` without reaching the program, so it
    can't be used to guess keys.
  - A change saved by someone else while one was being checked makes it answer 409 `settings_moved`.
  - Deleting a secret is never checked.
- **`open_url: "persona://settings/extensions/<its own id>"`** from an action: the apps open 设置 › 扩展 › that extension
  (the web `/settings?tab=extensions&extension=<id>`). Another extension's id is refused like any non-https link.

Code: `extension/configured.go`, `extension/egress` (`Target`, `dialConfigured`), `extmgr/extension_settings_check.go`;
the clients `web/src/lib/extensions.ts`, `web/src/views/settings/Extensions.tsx`,
`apple/Persona/Views/Settings/ExtensionsModel.swift`, `ExtensionUIModel.swift`.

## The hub's API

`/api/v1/extensions/<id>/…` is forwarded to the extension as a whole, so the hub's own endpoints sit under the reserved `-` segment (an extension's own HTTP paths cannot start with `/-/`; the hub does not forward those).
The `-` endpoints below exist only when `features` in `/health` and hello includes `extension_manager`.

| Endpoint | Who can call | Notes |
| --- | --- | --- |
| `GET /api/v1/extensions` | Any device | `{"extensions": [ExtensionView], "can_install"}`. `ExtensionView`: the old `id`, `name`, `kind` (the first extension point; voice extensions are still `voice`, connectors are `connectors`), `status`, `error`, plus `version`, `source`, `enabled`, `description`, `contributes` (extension point → id), `needs_setup` (what is still missing, human-readable), `warnings` |
| `GET /api/v1/extensions/{id}/-/` | Full-access devices | Details: `ExtensionView` plus `publisher`, `homepage`, `origin`, `previous_version`, `read_only`, `form` (`{fields: [{name, type, title, description, default, required, enum: [{value, title}], format, pattern, min_length, max_length, minimum, maximum, items, min_items, max_items, unique_items, fields}]}`), `config`, `secrets: [{name, title, description, required, set}]`, `permissions` (a few human-readable lines). Ones from manager.json do not return `config` or `secrets` |
| `PUT /api/v1/extensions/{id}/-/enabled` `{enabled}` | Full-access devices | Returns the details; enabling an extension that uses the Inbox and has no fallback while **Message triage** cannot be called returns 409 `needs_triage_model` ("General Inbox rules" above); the same for install and upgrade |
| `PUT /api/v1/extensions/{id}/-/config` `{config}` | Full-access devices | Replaces the whole thing; not matching the schema returns 400 `{code: "invalid_config", field, message}` |
| `PUT /api/v1/extensions/{id}/-/secrets/{name}` `{value}`, `DELETE …` | Full-access devices | At most 16 KB; the value is never echoed and never goes into logs or the audit log |
| `GET /api/v1/extensions/-/audit?plugin=&limit=` | Full-access devices (never an extension's token) | `{audit: [{plugin, action, actor, detail, at}]}`, newest first: `plugin` is an extension's, a connector's or `feishu`, everyone's without it; `limit` defaults to 100, at most 1000 (400 below 1); 503 without a database |
| `POST /api/v1/extensions/-/preview` `{source, upgrade?}` | Master token over loopback | Returns a preview; from anywhere else 403 `this_machine_only` |
| `POST /api/v1/extensions/-/dev` `{dir, config?, secrets?}`, `POST …/{id}/-/dev/restart`, `DELETE …/{id}/-/dev`, `GET …/{id}/-/dev/logs` | Same as above | Extensions under development (`personad extension dev`), see "Writing extensions", "dev" |
| `POST /api/v1/extensions/-/install` `{preview_id, permissions_digest}` | Same as above | Returns the details |
| `POST /api/v1/extensions/{id}/-/upgrade` `{preview_id, permissions_digest?}` | Same as above | Needs the digest when permissions grow |
| `POST /api/v1/extensions/{id}/-/rollback` | Same as above | |
| `DELETE /api/v1/extensions/{id}/-/` `?keep_data=1` | Same as above | |
| `/api/v1/admin/extensions…` | Admin UI sessions | The same preview / install / upgrade / rollback / uninstall / switch, plus browser upload; see [admin.md](../operations/admin.md) ("Endpoints") |
| Any `/api/v1/extensions/{id}/{path…}` | Full-access devices | HTTP forwarded to the extension (WebSocket too), with the app's `Authorization` and `token` removed and the caller's identity added (see below); on-demand extensions are started first |

### The caller on forwarded requests (#3498)

When a request goes to the extension's own HTTP (the last row of the table above, WebSocket too; the same for programs on computers via `…/-/machines/{machine id}/…`), the manager first deletes every `X-Persona-*`
(those sent by the client were already deleted by the hub; those the hub passes to the manager, such as scopes, addresses and whether it is the owner, are not given to the extension either), then adds:

| Header | Value |
| --- | --- |
| `X-Persona-Device` | The id of the caller's credential: a device id; for a pairing token, `pair_<hash>`; for another extension, `extension:<extension id>`; empty for the master token |
| `X-Persona-Role` | `full` (full-access devices, including pairing tokens and the master token), `extension` (another extension, which must have `extensions.call:<id>`). Read-only devices (`answer`) get a 403 before forwarding and never reach the extension |
| `X-Persona-Lang` | The language the hub uses to answer this request (`zh-Hans` / `en` / `ja`): the request's `Accept-Language`, or the user's language if absent |

- Extensions decide what to open to the caller based on these headers (the voice extension restricts by `X-Persona-Role`). Requests forwarded by older versions lack these headers; the extension decides how to treat them.
- The master token (the hub's own command line, or clients still using the master token) is `full` as before, with an empty `X-Persona-Device`; to tell it apart, check whether that header is empty.
- Every request the hub passes to the manager (including extension WebSockets and long-lived streams) is registered under the caller's credential: when a device is revoked (`DELETE /api/v1/auth/tokens/{id}`, the admin UI, linked hubs' `creds.revoked`),
  the hub cuts that forward on the spot, and the manager cuts the extension side in turn, without waiting for the phone to hang up. Forwards opened with a pairing token are cut when the pairing token becomes invalid (expired, or the grace period of the device paired with it ran out;
  revoking that device invalidates it immediately, cut within 15 seconds at most), the same as the `/api/v1/ws` it opened. The master token and admin UI sessions are not registered (they cannot be revoked).
- `api` contributions ("App endpoints") and `messaging` only carry `X-Persona-Device` and `X-Persona-Role`, with the same values as above.
- How the manager runs HTTP over the forwarded stream (pipes, when the forwarded stream's context ends): see [lessons/server.md](../lessons/server.md) ("Don't let `http.Server` read `websocket.NetConn` directly").

## App endpoints

An extension contributes `api` to provide a set of app-facing endpoints `/api/v1/<prefix>/…` on its own HTTP side (`PERSONA_EXTENSION_SOCKET`):

- The hub authenticates first (like any other endpoint; read-only devices are restricted as usual), then forwards as-is to the extension: path unchanged, `Authorization` and `token` removed, `X-Persona-Device` (device id, empty for the master token) and
  `X-Persona-Role` added; any `X-Persona-*` headers the caller sent are dropped. Parts of the path the caller escaped (`%2F`) reach the extension as-is; the extension reads `EscapedPath()`.
- Cache headers follow the extension: the `Cache-Control: no-store` the hub sets on its own endpoints is removed before forwarding, and `no-store` is added only when the extension's response has no `Cache-Control`
  (`ReverseProxy` appends response headers; without removing it you get `no-store, private, max-age=3600` and the app cannot cache anything; #1320).
- Prefixes the hub owns (any route under `/api/v1/<prefix>`) are **never given to extensions as a whole**, except prefixes being migrated out of the hub (`migratablePrefixes`, currently `lark`): the extension's version takes precedence,
  and the hub's copy is deleted once the migration is done.
- The same for connectors: connectors being migrated out of the hub (`connector.MigratableConnectors`, currently `lark`) are not reserved. When an extension contributes the same id, the hub's own one gives up the registry while the extension is enabled,
  and is put back after the extension is disabled or uninstalled (`extmgr/connectors_migrate.go`). The id stays the same, so knowledge base sessions (`lark-<feed>`), Inbox sources and mutes, and connector settings all carry over;
  since 3c (#1520) the hub no longer has its own copy, and the migrated id is simply not reserved. How its Inbox entries stay unchanged: see
  `docs/protocol/connector-protocol.md` ("Connectors migrated out of the hub").
- `features` are added to the `features` of hello and `/health`: clients decide what to show based on features as before, and they disappear once the extension is disabled.
- The extension sends the event `api.broadcast {type, …}`: the hub pushes it as-is to online clients (with `contribution` removed); `type` must be in `events`, otherwise it is dropped and logged. The manager reports the running extensions' `events` to the hub (the extensions' report, `events_declared: true`), which passes a manager's message of such a type only from that manager (from an older manager, whose report does not say, any type but the hub's own), never one of the hub's own types, and only about that manager's computers and sessions (`docs/assistant/managers.md`, "What the hub keeps of each manager").
- Extensions that push in real time must stay resident: `api` requires `on_startup`.
- **Send receipts** (#1503): writes listed in `receipts` (`method` is POST / PUT / PATCH / DELETE, `path` is a path under the prefix with `*` for one segment, `kind` is the receipt kind) get the hub's
  same receipt as its own sends (`withReplyReceipt`): the receipt id is derived from the request body's `client_id` and `kind` (or the caller's own `X-Persona-Op-Id`); a retry with the same id gets the first answer
  without reaching the extension again, a different body returns 409, and it can be looked up at `GET /api/v1/ops/{id}`. Routes migrated out of the hub use the hub's original `kind`, so the receipt id the app computes stays the same.

## Assistant

An extension contributes `assistant`:

- `skills`: skill directories in the package (each with a `SKILL.md`). The hub brings them in `assistant.prepare` to the computer that runs the assistant (each skill's files only once per computer, after that only a digest;
  those the node reports as "missing" are brought again next time). The node puts them in `~/.persona/plugin-skills/<name>` and copies them into the assistant directory like built-in skills; the user's own skill with the same name takes precedence; they are removed from the assistant directory when the extension is disabled or uninstalled.
  In the skill list their source is `plugin`. One extension's skills total at most 8 MB.
- `instructions`: a markdown file in the package, appended after the assistant's CLAUDE.md (starting with `<!-- plugin <id> -->`).
- `gate`: a JSON file in the package, a **command gate** (#1468): a command in the extension's skills that calls an HTTP API as the user (`<command> METHOD PATH [BODY] [curl args]`; the command carries its own credentials).
  Every time the assistant calls it via Bash, the node reads the command and, call by call, allows / asks for confirmation on the phone / denies by rule (`node/larkgate.go`), the same gate as lark-cli:

  | Field | Meaning |
  | --- | --- |
  | `command`, `script` | The command name on the assistant's PATH (lowercase, cannot be `lark-cli`) and the script it runs (a relative path under the skill directory; must be in one of this contribution's skills, e.g. `<skill>/scripts/x.sh`) |
  | `title` | The card says "以你的身份在<title>上…" ("as you, on <title>…") |
  | `connector?` | Rules the user wrote for this connector in `connector_gate` as "method path" (or `*`) come before `rules` |
  | `env?`, `base_env?` | Names of the credential variables (the extension's `assistant.credentials` can only fill these); `base_env` is the one holding the API address, and curl / wget directly to its host is always denied |
  | `secret?` | Variable names (tokens) that cause denial as soon as they appear in the assistant's command |
  | `allowed_env?`, `headers?`, `path_prefix?` | `VAR=x` allowed before the command; allowed `-H` headers (lowercase); a prefix stripped from the path |
  | `rules` | `[{method, path, body?, action}]`: in `path`, `*` is one segment and `**` everything remaining; `body` compares top-level fields; the first match wins, `allow` / `confirm` / `deny`; if none match, confirm on the phone |
  | `actions?` | `[{pattern, what}]`: the "what it does" on the card; `pattern` is a regex against "METHOD /path" |
  | `tool?` | `{name, description}`: a tool for runtimes without Bash (parameters `{args: [the words after the command]}`), going through the same gate |
  | `auth?` | How the credential is added to the request (#1317): `{header, value}`, where `${name}` in `value` is replaced with that credential from `env`, e.g. `{"header": "X-Api-Key", "value": "${FEED_TOKEN}"}`; if omitted, a gate with exactly one `secret` (which is in `env`) and a `base_env` uses `Authorization: Bearer ${<secret>}`. `{"env": true}`: the script needs the credential itself, so it is put into the assistant's environment as before (the assistant can read it; not recommended). The header cannot be `Host`, `Content-Type`, `Origin`, `Cookie` or others managed by the proxy or transport |

  - **Credentials**: the hub only gives them to the node running the assistant (`assistant.lark`; the node asks at most once a minute) by calling the extension's `assistant.credentials {contribution}` → `{env}`; only names declared in `env` are kept, never written to disk or logs.
    **Credentials never enter the assistant's process** (#1317, `node/gateproxy.go`): the node starts a loopback-only proxy locally and gives each assistant process its own address (`http://127.0.0.1:<port>/<random string>`, invalid once the process ends),
    put into `base_env`; the variables used by `secret` and `auth` are not given, and the rest of `env` is given as usual. The command wrapper (`~/.persona/assistant-bin/<command>`, running the script in the skill directory) requests `$<base_env><path>` as usual;
    the proxy appends the path to the real API address, adds the credential per `auth`, forwards only the headers scripts commonly use (`Accept`, `Content-Type`, those in `headers`…; the request's own `Authorization` and `Cookie` are removed), and replaces `Origin` with the API's own.
    The proxy checks every actual request against the same `rules`, whether sent by the command, curl or a script: `allow` passes, `deny` rejects; `confirm` passes only the single call the gate just had confirmed on the phone (or covered by **Always Allow in Session**),
    with the same method and path and the confirmed body (JSON-equivalent; the body is not compared when it contains `$`, when `@file` was cut off, or when uploading files), used once within 10 minutes; everything else gets a 403 with the reason (JSON), and the model adjusts accordingly.
    A write the proxy forwarded and the API answered with a 2xx is also told to the manager (`lark.sent`), which keeps it in the sent-as-user ledger with how the gate let it through (`docs/extensions/connectors.md` "Sent as the user").
    So commands the gate cannot parse (loops, scripts, `$(…)`) are denied outright if they contain a write, with a hint to send it as a separate command. A resident node's proxy is process-wide: chats that survive a hub reconnect keep using the original address, and calls allowed on the new connection still pass.
    Once the extension is disabled, both the command and the credentials are gone. Gates with no `auth` that do not meet the default condition (several secrets, no `base_env`) still put credentials into the assistant's environment, as do gates provided by older hubs (before #1317).
  - **Card**: the confirmation card's title is `title`; the extension can answer `assistant.summary {contribution, summary}` → `{summary}` to replace ids in the card with names (left as-is if they cannot be resolved).
  - **Space** (#6003): every assistant process has `PERSONA_SPACE` in its environment: `health` in the health space (`docs/assistant/health.md`), `main` everywhere else, decided by the conversation's directory when the node starts the process (`assistantSpace` in `node/larkgate.go`). An extension's skills and commands reach every space; a command meant for one space checks this rather than its working directory, which a `cd` in front of the command changes without a confirmation. Overriding it takes `PERSONA_SPACE=… <command>`, which the gate does not let through unless the gate lists the variable in `allowed_env`, so it goes to the normal permission prompt.
  - The gate file is read in strict mode and checked at install time (the script must be inside a skill); credentials cannot start with `PERSONA_` / `PERSONA_` or be `PATH`.

## MCP servers

An extension contributes `mcp_servers`: when the computer running the assistant has a certain command-line tool (usually the MCP server bundled with the upstream tool the extension corresponds to), the node attaches it as
one more MCP server when it starts the **Claude assistant**, with tool names `mcp__<id>__<tool>` (`node/assistant_mcp.go`; only for the Claude assistant, not attached to Codex or runtime assistants). It is purely declarative; the extension does not need to run a process.

| Field | Meaning |
| --- | --- |
| `id` | The server name the assistant sees (`a-z0-9_-`, cannot be `persona` or `persona`); when several extensions give the same id, the one whose extension id sorts first wins |
| `command` | The command name (lowercase letters, digits, `-`; cannot be a command the assistant already has, nor a shell, interpreter or similar command that runs anything given as arguments): looked up in `PATH` first, then `~/bin/<command>` |
| `env?` | An env var name starting with `PERSONA_`: if set, no lookup happens and the value is the command's path; set to `""`, `0`, `off` or `false` to not attach on this computer. When unset, the matching `PERSONA_` one is also honored |
| `args?` | Arguments after the command, e.g. `["mcp-server", "--control"]` |
| `requires?` | Words `<command> --help` must print; if any is missing it is not attached (cached by path, mtime and size), so old versions of the command are not attached by mistake |
| `allow?` | Tools that can run without asking (put into `--allowedTools`) |
| `deny?` | Tools that can never be called (`--disallowedTools`), whatever the command's own config says; cannot overlap with `allow` |

- Other tools follow the chat's permission mode: asked on the phone by default, run directly under `bypassPermissions`.
- **Credential isolation**: the command reads its own config; Persona adds nothing to its environment (Claude starts it with the assistant process's environment as usual, like any other MCP server),
  and does not read its credentials or log them.
- **Trust**: installing the extension means trusting it to run this command on the computer running the assistant, just like the scripts in its skills; so the command can only be a specific tool, and the hub rejects shells and interpreters.
- If the command cannot be found, is turned off by `env`, or `requires` is not met, nothing is added and the assistant simply lacks this set of tools. After the extension is disabled or uninstalled, they are gone the next time the assistant process starts.
- Older hubs do not recognize this extension point; it just has no effect (see the end of "Extension points" above), so there is no need to raise `engines` for it.
- Example: the Lark extension uses it to attach the upstream personal Agent control tool; the exact tool names and switches are in the extension's own docs.

## Usage quota

Quota extensions contribute `quota` and answer a launcher's quota (`docs/integrations/launchers.md` ("Quota")). They **never see the launcher's key**:

1. The hub sends `quota.query {contribution, query_id, base_url, tz}`: `base_url` is the gateway's root (with `/v1` removed), `tz` is the user's time zone (may be empty).
2. When the extension needs to read the gateway, it calls the hub's `quota.http {query_id, method, url, headers?, body?}`: `url` is an absolute same-origin address on the gateway or a path starting with `/`;
   wherever `${launcher_key}` appears in the address, headers or body, the side holding the key (the hub, or the computer that has this launcher) replaces it with the key. It returns `{status, headers, body}`,
   with the key replaced back by `${launcher_key}` wherever it appears in the response. Valid only while this query is in progress and only for the extension being asked (other `query_id`s return `permission_denied`); at most 16 requests per query;
   methods are GET / POST / PUT / DELETE / HEAD only, request body at most 1 MB, response at most 4 MB, and redirects are only followed to the same origin.
3. The extension returns `{report}` (`Report` in `server/internal/quota`: `key_name`, `usage`, `budget`, `pools`, `note`…; when `gateway` / `gateway_name` are omitted, the contribution's id / name are used; the app shows `gateway_name`);
   if it is not its gateway, it returns the error code `unsupported` and the hub asks the next one.

**Outside the launcher page** (#2846): sidebar pages (`nav_page`), cards and widgets have no launcher context, so extensions that accepted `launcher_keys` can look up launchers themselves:

- `gateway.launchers {}` → `{launchers: [{name, base_url}]}`: launchers that have a gateway address (`base_url` is the gateway's root, without the key). The launcher list comes from the manager (the hub asks for it via `models.launchers`); when the manager is offline it returns `assistant_offline`, and when the manager is too old to know it, `unsupported`.
- `gateway.open {launcher, machine?}` → `{query_id, base_url, machine}`: opens a session on this launcher; the `query_id` goes to `gateway.http` just like in the launcher page section
  (`${launcher_key}` is filled in by the side holding the key, at most 16 requests per session), and **expires after 1 minute**; to read again, open another one. A missing launcher returns an error,
  and a gateway that does not belong to this extension returns `unsupported`.
- The reachable gateways are the same as in the launcher page section: when both the extension's `quota` and `launcher_section` specify `hosts`, only launchers with matching hosts are listed and opened; if either omits `hosts` (any gateway), it is all of them.
  Extensions that did not accept `launcher_keys` get `permission_denied` from both. Older hubs do not have these two methods (`unknown_method`), and the extension treats that as no data.

**Gateway account inventory** (#4576): extensions that accepted `usage_gateways` can read all accounts of the account-pool gateways among the hub's usage sources (`usage.sources[]` in the manager's `manager.json`), not limited to the accounts one launcher key can reach:

- `usage.gateway_accounts {}` → `{sources: [{id, name, type, checked_at, error, listed, complete}], accounts: [{source, account_id, name, provider, plan, plan_raw, plan_display_name, status, schedulable, reason, cooldown_until, windows: [{kind, scope, scoped_model, used_ratio, limit, remaining, resets_at}], checked_at, cached, stale}]}`.
  What the fields mean, where the windows of disabled accounts come from, what `reason` keeps and the limits: see `docs/server/usage-windows.md` ("Gateway account inventory").
- It returns what the manager last read (one round every 5 minutes by default) and **never asks the gateway on its behalf**; with no sources it returns `{sources: [], accounts: []}`. The response contains no admin key and no gateway address.
- Without `usage_gateways` it returns `permission_denied`; older hubs do not know this method (`unknown_method`) and do not know this permission either (a manifest that lists it fails to install).
  So an extension that wants it first ships a version that does not list this permission and falls back to `gateway.launchers` + `gateway.open` on those two error codes, then ships the version with the permission after the hub is upgraded (the user consents once more).

The open-source repo ships no quota extension for any particular gateway.

> The subscription windows of the **upstream accounts an account-pool gateway maintains** (the sidebar usage bar, #2595) do not go through this extension point: a quota extension answers "how much this launcher key has left" and needs the launcher's key;
> that other thing uses the gateway's own admin key and is a very small built-in source in the hub (`usage.sources[]` in the manager's `manager.json`); for the trade-offs see `docs/server/usage-windows.md` ("Account-pool gateways").

## Declarative UI

Extensions **do not run code in the app**: they return a View (a few fixed kinds of items), which iOS, Mac, Android and web each draw with their own design system (correct in every interface style and in light and dark),
and button actions go back to the extension. Views and components are defined in `schema/extension/view.schema.json` (the types on all four clients are generated from it, see [schema/extension/README.md](../../schema/extension/README.md));
the hub first checks against the schema (`wire.Validate`, `sdk/go/wire`), then checks what the schema cannot express (item count per page, no duplicate field names, consistent labels across chart series), in `server/internal/extension/ui.go`.

**Where it goes** (the `placement` of a `ui` contribution): see the slot table in the next section, "UI slots". The first two:

| placement | Appears in | Extra |
| --- | --- | --- |
| `plugin_page` | **Settings › Extensions** › this extension, a row that opens a page | — |
| `launcher_section` | A launcher's detail page: launchers whose gateway hostname is in `hosts`; without `hosts`, every launcher (self-hosted gateways whose host is not fixed). If the extension recognizes while drawing that it is not its gateway, it returns the error code `unsupported`, the hub returns 404 `unsupported`, and the app does not show the section (not an error) | Like quota, it can access the gateway with this launcher's key (the extension never sees the key); needs the `launcher_keys` permission |

**The hub's endpoints** (full-access devices):

- `GET /api/v1/extensions/-/ui?placement=<p>[&launcher=<name>&machine=<machine>]` → `{ui: [{extension, extension_name, id, title, placement, size, icon?, opens?, badge?}]}`: what enabled, running extensions draw here;
  `launcher_section` needs `launcher` and only lists those whose gateway matches; without `placement`, all slots except `launcher_section` (main navigation, overview and machine details in one go).
  Extension details also have `ui` (its `plugin_page` and `nav_page`, both full pages reachable from settings).
- `POST /api/v1/extensions/{id}/-/ui/{ui}` `{context?: {launcher, machine, item, tz, locale, platform, size, ui_version}, state?, action?, values?}` (for the items of `context` see "UI slots", "Context"): without `action` it draws (returns `{view}`),
  with `action` a button was tapped (returns `{view?, toast?, open_url?}`; if none, it draws again). If the extension is missing or not enabled, 404; for a section without `hosts` where the extension says it is not its gateway, 404 `unsupported`;
  `machine_section` without `machine` or for a nonexistent machine returns 404 `not_found`; `inbox_item` without `item`, or with an item not in the Inbox, returns 404 `not_found`; an item not collected by its connector returns 403 `forbidden`;
  if its connector does not currently belong to this extension (not registered), 404 `unsupported`; if the extension errors or the View cannot be drawn, 502 `plugin_failed`.

**The hub calls the extension**: `ui.render {contribution, state, placement, size, ui_version, tz?, locale?, platform?, machine?, launcher?, item?}` → View;
`ui.action {…same as above, action, values}` → `{view?, toast?, open_url?}` (parameters are `extension.UIParams`, see "UI slots", "Context").
`launcher` is `{name, base_url, query_id}`: only for `launcher_section`; the extension uses `query_id` to call the hub's `gateway.http` (like `quota.http` in "Usage quota":
`${launcher_key}` is filled in by the side holding the key, sent only to this gateway, at most 16 requests per call), invalid once drawing is done. Other slots that need to read a launcher's gateway use `gateway.open` ("Usage quota", "Outside the launcher page").

**View**: `{title?, sections: [{title?, footer?, items: [item], more?}], state?, refresh_after?, search?, filter?}`. `state` belongs to the extension (which page, which tab); the app sends it back as-is on the next draw and on button taps, at most 16 KB.
`refresh_after`: see "UI slots", "Refresh"; `search`, `filter`, `more`: see "UI version 3" below.

| Item `type` | Fields | Drawn as |
| --- | --- | --- |
| `text` | `text`, `style?` (`body` / `secondary` / `caption` / `code`) | A paragraph of text |
| `metric` | `label`, `value`, `detail?`, `tone?` | A number with a description |
| `progress` | `label`, `ratio` (0…1), `detail?`, `tone?` | Progress bar |
| `row` | `title`, `subtitle?`, `value?`, `badge?: {text, tone?}`, `action?` | A list row; tappable with `action` |
| `button` | `title`, `action`, `role?` (`normal` / `destructive`) | Button (text never wraps) |
| `link` | `title`, `url` (https only) | A link opened inside the app |
| `field` | `name`, `label`, `kind` (`text` / `secret` / `number` / `toggle` / `choice`), `value?`, `choices?: [{value, title?}]`, `placeholder?` | Input; `secret` is never echoed and cannot be prefilled by the extension |
| `divider` | — | Divider |
| `chart` (UI version 2) | `kind` (`bar` / `line`), `series`, `title?`, `unit?`, `stacked?`, `detail?` | Bar / line chart, see "UI slots", "New items" |
| `stat_grid` (UI version 2) | `stats: [{label, value, detail?, tone?}]` (1–6) | Several numbers side by side, see "UI slots", "New items" |
| `segmented` (UI version 2) | `name`, `choices: [{value, title?}]` (2–5), `value`, `action` | Segmented choice (time range and the like); choosing triggers `action` right away, see "UI slots", "New items" |
| `markdown` (UI version 3) | `text` (at most 20,000 characters) | A safe subset of Markdown, see "UI version 3" below |
| `table` (UI version 3) | `columns: [{title, align?}]` (1–8 columns), `rows: [[text]]` (at most 100 rows), `caption?` | Table |
| `empty` (UI version 3) | `title`, `detail?`, `icon?`, `button?: {title, action}` | Empty state |
| `loading` (UI version 3) | `title?` | Loading |
| `header` (UI version 4) | `title`, `subtitle?`, `badges?`, `facts?: [{label?, value, icon?}]`, `avatar?` | A detail page header, see "UI version 4" below |
| `breadcrumb` (UI version 4) | `crumbs: [{title, action?}]` | Breadcrumb bar |
| `file_tree` (UI version 4) | `entries: [{path, kind?, status?, additions?, deletions?, comments?, badge?}]`, `selected?`, `action?`, `expand?` | File tree |
| `code` (UI version 4) | `text`, `path?`, `language?`, `start_line?`, `highlight?`, `truncated?`, `load?` | The code of one file |
| `diff` (UI version 4) | `path`, `hunks`, `threads?`, `status?`, `additions?`, `deletions?`, `collapsed?`, `load?`, `expand?`, `comment?` | The changes to one file |
| `comment_thread` (UI version 4) | `comments: [{author, body?, at?, avatar?, badge?, event?}]`, `title?`, `resolved?`, `reply?` | Discussion |
| `composer` (UI version 4) | `name`, `buttons: [{title, action, tone?}]`, `placeholder?`, `value?` | Comment box + a row of buttons |

Every item can carry a `key` (UI version 3, unique within a page): when redrawing, clients use it to keep scroll position, expanded state and animations. In UI version 3 a `row` can also carry one of `icon` / `avatar: {url?, text?}` / `thumbnail: {url, aspect?}`.

`tone` is only `neutral` / `positive` / `caution` / `danger`, which the app maps to its own role colors; extensions cannot set colors or font sizes, and icons can only be picked from a fixed icon set. `action` is `{id, confirm?, submit?, push?, values?}` (`values` is UI version 4):
if `confirm` has text, the user confirms first; with `submit`, the values of every `field` on the page are sent as `values` (name → string; toggles are `"true"` / `"false"`).
Limits: at most 300 items per page, at most 2000 characters per string; a `tone` outside the four above, a non-https link, or a known item missing fields rejects the whole page (never half drawn).
`type`s the hub does not know (extensions written for a newer hub) are removed from the View and the rest is drawn as usual; ones the app cannot draw (older apps) are replaced with something it can draw or removed, see "UI slots", "Compatibility" and "UI version 3", "Compatibility".
`open_url` can only be https too; the only exception is `inbox_item` opening the item's own source page (`persona://inbox/<item id>/source`, see "UI slots", "Inbox items").

## UI slots

The places where an extension can draw are called **slots**: the hub has a table of allowed slots (`extension.UISlots`, `server/internal/extension/ui.go`), and the extension says where it plugs in with `contributes.ui[].placement` in its manifest;
positions not in the table fail to install. Extensions still do not run code in the app: every slot draws the declarative View above, and each client draws it with its own design system; what an extension changes in these places is only the
**entry** (one more item in navigation), **badge**, **card** and the button **actions**; it cannot change the app's own pages or behavior. (#1451)

### Slot table

| placement | Appears in | Size | Context | Badge | Shown at install | UI version |
| --- | --- | --- | --- | --- | --- | --- |
| `extension_page` (old name `plugin_page`) | **Settings › Extensions** › this extension, a row that opens a page | full | — | — | — | 1 |
| `launcher_section` | A section of the launcher detail page (see "Declarative UI" above) | full | `launcher` | — | Already shown by `launcher_keys` | 1 |
| `nav_page` | **An entry in the main navigation** that opens a full page | full | — | Yes | Entry in the main navigation | 2 |
| `overview_card` | **A card on Overview**; tapping the card opens the page `opens` points to | compact | — | — | Card on Overview | 2 |
| `capsule_card` | **The card in Android's cross-app floating capsule once expanded**; can open a full page with `opens` | compact | — | — | Card in Android's cross-app floating capsule (shown outside the app) | 3 |
| `machine_section` | **A section in the machine detail page**, after the machine's own sections | full | `machine` | — | A section in the machine detail page (knows which machine) | 2 |
| `inbox_item` | **The detail body of one Inbox item**: items collected by `connector`, below the app's own header (see "Inbox items" below) | full | `item` | — | Item details for its connector's items in the Inbox (reads that item's title, summary and priority) | 2 |
| `widget` | **Outside the app**: Home Screen widgets, Lock Screen and watch face complications (see "System-level placements" below); not in `GET …/-/ui`, listed separately | One of `families` | — | — | Home Screen widgets and watch faces (shown outside the app) | 3 |

Where each client puts them:

| placement | Web | Mac | iOS | Android |
| --- | --- | --- | --- | --- |
| `nav_page` | In the sidebar's **Overview / Machines / Settings** group, before Settings, one row per page (icon, title, badge); at most 3 in the sidebar, later ones go first under **More** at the end of the group, and the user can reorder, show or put away pages in settings (web.md "The sidebar's pages") | The left section column, same group and position as the web | In the new shell's drawer (iPhone) and iPad sidebar, after the **Machines** group, one row per page (icon, title, badge), opening directly when tapped (#2961); the **Extensions** section at the top of the Settings home stays too; the Settings tab badge adds these badges (numbers summed; a dot when there are only text badges), and when the drawer already lists these entries, the Settings button at the bottom of the drawer does not add them again | Same as iOS: the **Extensions** section at the top of the Settings home |
| `overview_card` | On the Overview page (`/overview`), a row of cards below the Agent summary | No Overview page: above the list in **Settings › Extensions** | No Overview page: above the entries in the **Extensions** section of the Settings home | Same as iOS |
| `machine_section` | Machine detail page | Machine detail page | Machine detail page | Machine detail page |
| `inbox_item` | The Inbox detail pane: the header (title, source, **Done**, ✦ summary, **AI draft reply** / **Hand to AI**) stays, and the conversation / body below is replaced by what the extension draws | When an item from this connector's source is opened in the Inbox, the right column (✦ summary and buttons, **Done**) stays, and the conversation / body on the left is replaced by the extension's View | When an item from this connector's source is opened in the Inbox, the action bar under the navigation bar (✦ summary, **AI Draft Reply** / **Hand to AI**) and **Done** stay, and the conversation / body below is replaced by the extension's View | Same as web (Android's own PR) |

In the manifest:

```json
"contributes": {"ui": [
  {"id": "usage", "title": "Usage", "placement": "nav_page", "icon": "chart"},
  {"id": "today", "title": "Today's usage", "placement": "overview_card", "opens": "usage"},
  {"id": "gpu", "title": "GPU", "placement": "machine_section"}
]}
```

- Only `nav_page` can set `icon`, picked from a fixed few: `plugin` (default), `chart`, `gauge`, `list`, `bell`, `doc`, `calendar`, `money`; each client maps them to the icon with the same meaning in its own design system,
  and draws unknown ones as `plugin`. Extensions cannot supply images, SF Symbol names or colors.
- Only `overview_card` can set `opens`: the id of a `nav_page` or `plugin_page` of the same extension; without it the card is not tappable (buttons and rows inside the card still are).
- Titles are 1–40 characters; when they do not fit in navigation, each client truncates them (single line), never wrapping.
- Only `inbox_item` can set `connector`, and it must; see "Inbox items" below.

**Placements not built** (evaluated, not opened for now; names reserved):

- Chat toolbar actions (`session_action`): the chat page is the most used page and the one where opening speed matters most, and every chat opened would have to list the extensions' actions; the toolbar on phones is already full and buttons cannot wrap,
  so they could only go into a menu, which is of little value; the actions act on an Agent's session, so an extension would need to read the session and drive the Agent to be useful, and those permissions (reading sessions, `agents.control`) do not exist yet.
  To react to what Agents do, use "Events" (`agent.finished`, `agent.needs_you`) plus your own `nav_page` first.
- An Agent detail section (`agent_section`): clients have no unified "Agent details" (on iOS it is the chat page's info panel, on the web the right-side panel), and it too would hand the session to the extension; it waits until there is a permission to read sessions.

### Context

When the app draws a slot, it states in `context` whom it is drawing for and where; the hub checks it and passes it to the extension (`extension.UIParams`, the parameters of `ui.render` / `ui.action`):

| Field | App sends | Extension receives |
| --- | --- | --- |
| `placement` | — | This contribution's slot |
| `size` | Optional: `compact` / `full` | If not sent, the size from the slot table; `compact` is a card and must be compact (see the limits under "New items") |
| `ui_version` | The UI version the app can draw: the hub knows `6`, apps currently send `5` (web, Android; iOS catching up) until they draw all of UI version 6; **older apps do not send it, which counts as 1** | `min(app's, hub's)`; the extension can draw a version an older app can handle (e.g. `progress` instead of `chart`) |
| `tz` | The user's time zone (IANA name, `Asia/Shanghai`) | Passed on only if it loads; if not sent or invalid, the user time zone the hub knows (reported by the app, or from assistant settings), empty if neither. "Today" is computed in it |
| `locale` | The app's language (`zh-Hans-CN`) | Passed on only if it looks like a language tag, otherwise empty |
| `platform` | `ios` / `mac` / `android` / `web` | Other values are dropped |
| `machine` | `machine_section`: machine id (machine name also accepted) | `{id, name, os, online}`; `machine_section` without it returns 404 |
| `launcher` | `launcher_section`: launcher name (`machine` is the computer it is on) | `{name, base_url, query_id}`, see "Declarative UI" above |
| `item` | `inbox_item`: the Inbox item's id | `{id, title, summary?, tier?, reason?, done, meta, extra?}`, see "Inbox items" below; `inbox_item` without it returns 404 |

### Android floating capsule (#2124)

`capsule_card` is a declarative content and action slot opened up by Android's native floating module. It only shows after the app turns it on in **Settings › Floating capsule** and gets the system overlay permission.
An extension manifest adding this placement goes into the install permission summary and the upgrade confirmation, stating clearly that the content shows outside the app; the switch, window, dragging, keyboard and system permissions always belong to the client.
Other clients ignore slots they do not support. Extensions cannot use this slot to run code on the phone, or to get screenshot or microphone permission.

```json
"contributes": {"ui": [
  {"id": "home", "title": "Task details", "placement": "extension_page"},
  {"id": "task", "title": "Task status", "placement": "capsule_card", "opens": "home"}
]}
```

When the capsule expands to the main chat / session details, it queries `GET /api/v1/extensions/-/ui?placement=capsule_card` and requests `ui.render` per card; the unread and Inbox lists do not load extension cards. The extension receives
`placement: "capsule_card"`, `size: "compact"`, `platform: "android"`. Card limits apply (at most 2 sections, 12 items, no field / search / filter / more);
buttons, rows and segmented choices go through the usual `ui.action`. Buttons that need confirmation first show a confirmation row inside the capsule, and the action is sent only after confirming; errors and receipts also stay inside the card.
`opens` and an action's `push` open the full extension page inside the app, and links are https only. `refresh_after` and `ui.invalidate` only refresh while the card is expanded and the window visible;
on collapse, close, unpairing or switching identity, the composition is removed and requests are canceled; no extra WebSocket is added.

When writing the manifest, set `engines.persona` to the first hub build number that supports this slot; older hubs explicitly reject unknown placements. A full manifest example is in
`schema/extension/examples/Manifest.capsule-card.json`. The client entry point is `ExtensionCapsuleCards(api, onOpen)`.

### Inbox items (`inbox_item`, #1681)

For items the extension's connector collects into the Inbox, the body of the detail page can be drawn by the extension from that item's data (for example, once the Inbox groups messages into events (#1680), the Lark extension draws an event's message chain from the item's meta). Sources without this slot keep the current details.

```json
"contributes": {
  "connectors": [{"id": "lark", "name": "Lark", "capabilities": ["status", "inbox"]}],
  "ui": [{"id": "event", "title": "Event", "placement": "inbox_item", "connector": "lark"}]
}
```

- **Only draws its own connector's items**: `connector` must be one in the same extension's `contributes.connectors` (like `channel.connector`), with at most one `inbox_item` per connector.
  Items from other sources (mail, GitHub…) are not its own; it can neither read nor draw them. This is rejected at manifest check; whether the connector id is currently registered (e.g. the hub's own Lark has not given way yet) is only checked at runtime:
  if not registered, or registered by another extension, it does not appear in the list, and drawing returns 404 `unsupported`.
- **Listing**: `GET /api/v1/extensions/-/ui?placement=inbox_item` → `{ui: [{extension, extension_name, id, title, placement: "inbox_item", size: "full", connector}]}`;
  it is also in the list without `placement` (older apps that do not know `inbox_item` ignore it by the rules). The app caches this list (fetched together with the other slots, see "Compatibility"), and when opening an item finds the entry whose `connector` matches the item's `source`, without asking the hub each time.
- **Drawing**: `POST /api/v1/extensions/{id}/-/ui/{ui}` `{context: {item: "<item id>", …}}`. The hub asks the Inbox (the manager's, `inbox.item` on the bus, `docs/assistant/inbox.md` ("Who owns what")) for this item and checks that its source (`source`) is this UI's `connector`
  (when the manager is not connected it cannot get it and returns 404 `not_found`, and the app draws the original body as before),
  then includes `item` in `ui.render` / `ui.action`:

  | Field | Notes |
  | --- | --- |
  | `id`, `title`, `summary?`, `tier?`, `reason?` | This row: title, the AI's summary, priority (`action` / `fyi` / `noise`; absent if not triaged), reason |
  | `done` | Whether it is done |
  | `meta` | The connector's own data about this event (Inbox event grouping, #1680), passed to the extension **as-is**; the hub does not parse it; `null` if absent |
  | `extra?` | This item's source block (`lark` / `doc` / `moment` / `connector` etc., the same as the app gets), as-is; for items that do not have meta yet (old items, sources without grouping) |

  The hub reads the item's JSON (not fixed fields); once an item has `meta` it is passed along as-is, no matter which PR added `meta` to items. The item's raw conversation (`context`) is not given to the extension.
  The app sends `item` on every draw and every action, and the hub re-reads the item every time (after an extension button does something and redraws, it sees the latest).
- **Where it is drawn**: the app's own header (title, source, **Done**, ✦ summary, **AI Draft Reply** / **Hand to AI**) stays the same; the extension draws the body below, replacing the original conversation page / body
  (slots cannot change the app's own pages and behavior, see above). So the extension does not redraw the title, priority, summary and Done; it draws what only meta has: the people involved, the time span, "continuing 《…》", the message chain.
  Buttons go through `ui.action`: reply is an input field plus submit, and the extension uses the `inbox.write` scope (`hub_api` for older extensions) to call the existing `POST /api/v1/inbox/{id}/reply` (the app refreshes as usual on the `inbox` push); Done is in the app's header, and the extension does not draw it.
- **View in the conversation** (generic, not Lark-specific): `ui.action` returns `open_url: "persona://inbox/<item id>/source"` (the id escaped as one path segment, `extension.InboxSourceURL`; something like `lark:F1:1051` stays as-is).
  `open_url` normally only accepts https; the hub allows this one in-app link only for `inbox_item` and only for the item being drawn (`context.item`); other `persona:` links, other items' links and other slots' links all return 502 `plugin_failed`.
  On receiving it, the app opens the native page of the item's source, i.e. the page you would get by opening the item if there were no slot: for Lark it is the chat page, stopped at the item's first message per the item's `lark.start_position` (the extension fills in the event's first message when writing `extra`).
  The extension does not build Lark-specific links. The web replaces the body area with the original conversation page, with a "Back to <page title>" line above; the extension's page is kept and is restored as-is on going back.
- **Not drawing an item**: for items the extension does not want to draw (e.g. Lark cloud docs or Moments items, which have no meta), `ui.render` returns the error code `unsupported`, the hub returns 404 `unsupported`, and the app draws the original body. The hub does not parse meta, so it always asks the extension.
- **When it cannot be drawn**: when the hub returns 403 / 404 (read-only device, item gone, connector not its own, extension said `unsupported`), the app draws the original body directly; when the extension errors (502), it also draws the original body, with a line above saying the extension's details could not be drawn and can be retried.

### New items (UI version 2)

**`chart`**: a real chart, instead of faking one with a row of progress bars.

```json
{"type": "chart", "kind": "bar", "title": "Last 7 days", "unit": "tokens", "stacked": true,
 "series": [
   {"name": "Input", "points": [{"label": "11-01", "value": 1200000, "display": "1.2M"}, {"label": "11-02", "value": 800000}]},
   {"name": "Output", "tone": "positive", "points": [{"label": "11-01", "value": 300000}, {"label": "11-02", "value": 250000}]}
 ],
 "detail": "Days in Beijing time"}
```

- `kind`: `bar` / `line`. 1–4 `series`, each with 1–120 points; **every series has the same `label`s in the same order** (the x-axis is the first series' labels); with more than one series each needs a `name` (clients draw a legend).
- `value` is a number (may be negative or fractional); `display` is how to write that value ("¥1.20", "1.2M"); without it the app writes a compact number using `unit` (`1.2M tokens`). The app computes the y-axis ticks itself and writes them as compact numbers.
- `tone`: if omitted, the app's own data colors (taken in series order, the first is the primary color); if set, a role color (`positive` / `caution` / `danger` / `neutral`). Extensions cannot set colors.
- `stacked`: stack the bars of multiple series (side by side by default); ignored for line charts.
- Clients: tapping or hovering a point shows the `label` and each series' `display` (or number); in compact, no axis text is drawn, only the bars / lines and the first and last labels. With many labels the app writes every few, never overlapping.

**`stat_grid`**: several numbers side by side, so a card can fit summaries like today / 7 days / cost.

```json
{"type": "stat_grid", "stats": [
  {"label": "Today", "value": "1.2M", "detail": "+12% vs yesterday", "tone": "caution"},
  {"label": "Last 7 days", "value": "8.4M"},
  {"label": "Cost", "value": "¥36.20"}
]}
```

1–6 of them; clients put 2–3 per row depending on width (all in one row on wide web and Mac), and `tone` colors `detail` (like `metric`).

**`segmented`**: pick one of a few, taking effect immediately (time range, view), instead of a row of buttons.

```json
{"type": "segmented", "name": "range", "value": "7d", "action": {"id": "range"},
 "choices": [{"value": "1d", "title": "Today"}, {"value": "7d", "title": "7 days"}, {"value": "30d", "title": "30 days"}]}
```

2–5 choices, each at most 12 characters; `value` is one of them; `name` cannot duplicate a `field` name. When the user picks another one, the app immediately calls `ui.action` with `action` being its `id`,
and `{name: chosen value}` in `values` (together with the page's `field`s when it has `submit`); the extension returns a new View (usually putting the chosen value in `state`).

**Limits for cards (`size: compact`, `overview_card`)**: at most 2 sections and 12 items, no `field` (to fill something in, `opens` a full page). Beyond that the hub returns 502 `plugin_failed`.
A card is best one `stat_grid` plus one small `chart`.

### Refresh

A View carries `refresh_after` (seconds): while the View is on screen, the app redraws it after that long (`ui.render`, with `state`). At least 30 seconds (the hub changes smaller values to 30), at most one day; omitted or 0 means no auto refresh.
It does not refresh while off screen (switched to another page, app in the background); when it comes back to the foreground or reappears and the time has passed, it draws once right away. Manual pull-to-refresh works as before.

### Badges

A `nav_page` entry can carry a badge (a number or short text, at most 8 characters, `tone` as above). The extension sends the hub an event:

```json
{"event": "ui.badge", "params": {"contribution": "usage", "text": "3", "tone": "danger"}}
```

Empty `text` clears it. The hub keeps it in memory, includes `badge` on that entry in `GET /api/v1/extensions/-/ui`, and pushes the WebSocket event `extension_ui {extension, id, badge}` to online apps (the app updates the badge on receipt without refetching the list).
After the extension restarts, the hub clears its badges: the extension sends them again itself after `initialize` (extensions that need timely badges use `on_startup`). Badges not on a `nav_page`, too long, or with a bad `tone` are dropped and logged.

### Permissions

Putting things into the main navigation, Overview or the machine detail page does not get its own entry in `permissions` (they do not get any extra data), but **people need to know**: like "Events", it is computed automatically from contributions —

- The "It can" list before install gets one more line, "show its own UI in the app", listing each place and title ("Entry in the main navigation: Usage; Card on Overview: Today's usage");
- These slots count toward the permission summary: an upgrade that adds a place (say a new `nav_page`) needs consent again (the preview's `more_permissions`, with "show its own UI in more places in the app" in `added_permissions`); one more page in the same place does not;
- `machine_section` lets the extension know which machine (id, name, OS, online or not), so that line says so; `inbox_item` lets the extension read the title, summary and priority (the AI's triage result) of its own connector's items, which is stated too;
  `launcher_section` already requires `launcher_keys`; `plugin_page` is only in the extension's own settings and is not shown.

If you do not want to see an extension's entries and cards: disable the extension.

### Compatibility

**UI versions**: 1 is the original eight item types and two slots; 2 adds three slots, `chart` / `stat_grid` / `segmented`, `refresh_after` and context; for 3 see "UI version 3" above; for 4 see "UI version 4: code hosting"; for 5 see "UI version 5: code review"; for 6 see "UI version 6". The version of the `ui` extension point changes with it (`hub.extension_points.ui` in `initialize`, currently 6).

- **Older app, new slot**: the app only queries the slots it knows by `placement`; it does not see new slots and does not error. `nav_page` also appears in the extension details' `ui`, so older apps (web) can still open it under **Settings › Extensions** › this extension;
  important content can therefore just go in `nav_page`, without a `plugin_page`.
- **Older app, new item**: older apps do not send `ui_version`, so the hub treats them as 1 and removes `chart`, `stat_grid` and `segmented` from the View (removing a whole section if those are all it has left, unless it has a `footer`); the old app does not go blank.
  The extension also sees `ui_version: 1` and can draw an alternative (`progress`, `metric`, a row of `row`s instead of `segmented`). `refresh_after` is an extra field that old apps ignore.
- **Rules for new apps** (future items and slots rely on this rule, without another hub change): skip unknown item `type`s and draw the rest as usual; treat unknown `tone`s as `neutral`; draw unknown `icon`s as `plugin`;
  ignore unknown `placement`s in lists; ignore badges and `refresh_after` if unknown. Only show an error when the whole View cannot be decoded.
- **Extension newer than the hub**: an unknown item `type` no longer rejects the whole page; the hub removes it and logs a line, drawing the rest as usual. Manifests are read strictly: a `placement`, `icon` or `opens` an older hub does not know prevents installation.
  Extensions using new slots put a hub build number that includes UI slots in `engines.persona`, or check `hub.extension_points.ui >= 2` in `initialize`.
- **New app, old hub**: when hello / `/health`'s `features` lacks `extension_ui_slots`, do not query new slots or draw navigation entries and cards (old hubs return an empty list for an unknown `placement`, without an error).
- **`inbox_item` (#1681)**: no new feature and no UI version bump (it still draws version 2 items). A hub without this slot will not list `inbox_item` (absent from the list without `placement`,
  an empty list for `?placement=inbox_item`), so the app draws the original details as before. Installing an extension with `inbox_item` or `connector` on an old hub is rejected by strict manifest validation (the previous rule).
  This is expected: an extension using it puts a hub build number that includes this slot in `engines.persona`, or ships it in a separate version.

**Conformance tests**: `conformance.Check` calls `ui.render` once for every `ui` contribution in the manifest, using its slot (with sample context; for `inbox_item`, a sample item from its connector with `meta` set to `null`). A returned View must be drawable by the app in that slot
(`extension.Fit` passes, no unknown items, snake_case keys). An error is also allowed (tests may not reach the extension's own service), but not `unknown_method` / `no_contribution`.

## UI version 3 (#1850)

UI version 3 adds components, list search / filters / load more, page navigation and `ui.invalidate`, and makes the assistant's cards use the same components. The app sends `3` in `context.ui_version`,
the hub's `features` includes `extension_ui_v3`, and `hub.extension_points.ui` in `initialize` is `3`. Field definitions are authoritative in `schema/extension/view.schema.json`.

### New components

- **`markdown`**: a safe subset: headings 1–3, paragraphs, ordered / unordered lists, bold, italic, inline code, code blocks, quotes and links. Links must be https (opened in-app); **no** HTML (treated as text),
  images or tables (use `table` for tables). The hub rejects images and non-https links (not those inside code). At most 20,000 characters.
- **`table`**: 1–8 `columns` (`align` is `start` / `end`; use `end` for numeric columns), at most 100 `rows`, with as many cells per row as columns; `caption` appears below. Horizontal scrolling on narrow screens, not wrapped cells squeezed together.
- **`empty`**: an empty state: one `title`, one `detail`, an icon and at most one button (`button: {title, action}`). Each client draws it with its own empty-state component.
- **`loading`**: loading. Return it first while extension data is not yet available (e.g. `{"sections":[{"items":[{"type":"loading"}]}]}`), then send `ui.invalidate` when data arrives so the app redraws.
- **Row icon / avatar / thumbnail**: `row` can carry one of them: `icon` (fixed set: `extension` (default, old name `plugin`), `chart`, `gauge`, `list`, `bell`, `doc`, `calendar`,
  `money`, `person`, `chat`, `mail`, `link`, `folder`, `star`, `check`, `warning`, `clock`, `tag`, `search`, `settings`; each client maps it to an icon with the same meaning in its design system, with unknown ones drawn as the default),
  `avatar: {url?, text?}` (https image; without an image, 1–2 characters as a monogram), or `thumbnail: {url, aspect?: square | wide}`. Images must be https; the app loads them directly without credentials and shows a placeholder on failure.
  `nav_page`'s `icon` and `empty`'s `icon` use this set too.
- **`key`**: any component may carry it, unique within the page.

### Search, filters and load more

- View's `search: {placeholder?, value?, action}`: a search box at the top of the page. After the user pauses for 0.3 seconds, the app calls `ui.action`, with `action` set to its `id` and `values: {"q": text}`; the extension returns a new View (putting `q` in `state`).
- View's `filter: {name, title?, choices: [{value, title?}] (2–20), value, action}`: a dropdown filter next to the search box. Selecting immediately calls `ui.action`, with `values: {name: value}` (including the current `q` when there is a search box).
  `name` cannot duplicate a field on the page or be `q`.
- Section's `more: {title?, action}`: a row at the end of the section (default **Load more**). Scrolling to the bottom triggers it once automatically (waits for the response, without repeated triggers); it is also tappable. The extension returns a **complete** new View (keeping the cursor in `state`), and the app preserves scroll position by `key`.
- Cards (`compact`) cannot have any of these three (the hub returns 502); for search, `opens` a full page.

### Page navigation (push / pop)

- `action.push: {ui?, state?, title?}`: tapping **does not call `ui.action`**, but directly pushes a page onto the current navigation stack: the app calls `ui.render` (a `ui` contribution from the same extension; the current one if omitted), with this `state`.
  `title` is the new page's title before the View returns. The target must be a full page (`extension_page` / `nav_page`): cards and machine-page sections must specify `ui` to navigate. The hub checks push targets in every View; an invalid target rejects the whole page (502).
- A `ui.action` response may contain `push` (as above, e.g. going to details after saving) and `pop: true` (return to the previous page, which redraws): "list → details → edit → save → back".
- The stack is at most 8 levels (the app stops pushing). The `state` carried by `push` has the same 16 KB limit as a View's.

### `ui.invalidate`

When its data changes, an extension can actively ask the app to redraw instead of relying only on `refresh_after` polling:

```json
{"event": "ui.invalidate", "params": {"contribution": "notes"}}
```

Omitting `contribution` means all this extension's UIs. The hub coalesces an extension's `ui.invalidate` events within 1 second into one, then pushes WebSocket `{"type": "extension_ui", "extension", "id"?, "invalidate": true}`
(`id` identifies the UI; absent means all; it **does not send** the old `plugin_ui`, which old apps would treat as a badge update). The app only redraws pages currently visible (with current `state`, search and filters, preserving fields being filled in);
it records off-screen ones and draws them when they appear.

### Assistant cards: `persona-view` and `persona-card`

Cards in assistant replies use the same components and renderer as extension Views (but are not interactive: `action`, `push`, `field`, `search`, `filter` and `more` have no effect in assistant cards; links work as usual).

- **```` ```persona-view ````**: a View (`{"sections": […]}`) in a code block, or a component directly (`{"type": "metric", …}`). When hub.json has `assistant.persona_view: true`, the assistant prompt teaches it to write these
  (`prompts/assistant/claude_md.md`); off by default, to be enabled once every client can draw UI version 3.
- **```` ```persona-card ````** (#886, old format) still renders: clients **convert it to components** before drawing. The rules are documented in `sdk/go/wire/personacard.go` (the Go reference implementation), consistently across all four clients;
  `schema/extension/examples/persona-card/` contains cases (card → expected View; `null` means it cannot be drawn and stays a code block), used by each client's tests:
  - Cannot draw: `stat` without `value`; `stacked_bar` with no parts, negative values or a sum of 0; `line` with fewer than two points; `bar` with no points.
  - One section; the card's `title` is the section title (a `stat`'s `title` is its `metric`'s `label`); `subtitle` is a leading paragraph of secondary text. Numbers use thousands separators and at most one decimal place (`2,250`, `7.5`), followed by a space and the unit.
  - `stat` → `metric {label: title or "Value", value, detail: note}`.
  - `stacked_bar` → `metric {label: Total, value: total (the sum of parts if absent), detail: note}`; one stacked bar chart, one series per part (beyond 4 parts, the first three plus "Other"; `rest` and "Other" are `neutral`),
    one point per series with `title` or "Total" as its label; then one `row {title: label, subtitle: note, value}` per part. Use `total`'s unit, or the card's if absent.
  - `line` / `bar` → when there is a `value`, start with `metric {label: Current, value}`; one chart of this kind, one series (the last 120 points), with `note` as `detail`.

### Compatibility

- **Older apps (`ui_version` 1 or 2)**: the hub replaces new things with what they can draw (`extension.Fit`): `markdown` → `text` (truncated to 2000 characters); `table` → one `row` per row (first cell is the title, the rest joined with " · "), plus
  small text for `caption`; `empty` → secondary text plus a button; `loading` → "Loading…" text; a section's `more` → a **Load more** button at the end (old apps call `ui.action` on tap and the extension returns a longer View);
  `filter` → `segmented` (version 2, at most 5 choices and at most 12 characters each; otherwise removed); `search`, `key`, row `icon` / `avatar` / `thumbnail`, action `push`, and response `push` / `pop` are removed.
  The action `id` stays: tapping a push row on an old app calls `ui.action`, which the extension must handle itself (return the target page's View; the official SDK does this for it when `ui_version < 3`).
- **New app, old hub**: even when `features` lacks `extension_ui_v3`, send `ui_version: 3`; the old hub caps it at its own version, 2, and the extension sees 2.
- **Slot names**: `plugin_page` was renamed to `extension_page`; the hub accepts both (either works in `?placement=`). New endpoints return `extension_page`, old endpoints (`/api/v1/plugins/…`) return `plugin_page`;
  the extension receives whichever it wrote in its manifest in `ui.render`'s `placement`. A manifest with `extension_page` cannot install on an old hub (strict manifest reading); put a hub build number with UI version 3 in `engines.persona`.
- **Extension newer than the hub**: as before (unknown components removed, one log line).

### Component showcase (`showcase`, #1852)

`extensions/showcase` in the `persona-extensions` repo: a page for every component and every state (the catalog pushes each page; list search / filters / load more and "details → edit → save → back";
a page that returns loading first and sends `ui.invalidate` after 3 seconds; overview cards, every widget size and two shortcuts), using only local data and no permissions. It shows extension authors the result, and is also the **reference for screenshot alignment across clients**:

- Each page's rendered View is saved as a snapshot (`go test ./extensions/showcase -run TestSnapshots -update` in the extension), with a copy in this repo's `schema/extension/examples/showcase/`;
  `scripts/test/test_extension_schema.py` checks the schema, and `TestShowcaseIsDrawable` in `server/internal/extension` checks the hub's `CheckView` / `Fit` (forms the schema allows but the hub rejects,
  such as images in Markdown or http links, cannot appear in snapshots).
- Each client's screenshot tests draw directly from these snapshots without connecting to a hub: web `web/showcase.html` (only with `npx vite` in development, not in the build; `?theme=dark`, `?pages=a,b`, `?only=<page>`, rendered with the real `ExtensionUI`,
  a fake hub returning that page's snapshot by `state`), unit test `web/src/showcase/showcase.test.tsx`; Android `ExtensionShowcaseShotsTest` (Robolectric, 35 pages × three styles × light/dark, 210 screenshots,
  output to `feature/settings/build/extension-showcase-shots/`). iOS `ExtensionShowcaseTests` reads the same snapshots unchanged through mock HTTP and draws real app pages;
  five styles × light/dark, 350 page-top screenshots, plus 50 screenshots at the bottom of long pages. Set the output directory with `TEST_RUNNER_PERSONA_SHOTS` for each run;
  see the contact sheets below. Mac is compiled only, with no windows opened or automatic screenshots, respecting the user's focus restriction.
- When a component changes: update the showcase extension first (add a page or state), regenerate and copy snapshots, then inspect each client's screenshots again.

Verification image removed; see the associated PR.

Verification image removed; see the associated PR.

Apple's 35-page contact sheets (sorted by file name, repeated navigation bars cropped; full originals are output by the screenshot tests):

| Interface style | Light | Dark |
| --- | --- | --- |
| Liquid Glass | Light | Dark |
| BeautifulUI | Light | Dark |
| Clay | Light | Dark |
| Island | Light | Dark |
| Lark | Light | Dark |

Clients share content and action semantics, not pixel-for-pixel rendering: Apple uses native Form cards and system choice controls, search in the navigation bar, filter chips and horizontal scrolling for wide tables;
chart series colors and line rendering follow each client's design system. Android's Robolectric network-image placeholders are not production image-load failures. Widget-size snapshots here are still ordinary pages,
not proof that #1862's WidgetKit or watch-face placements are complete.

## UI version 4: code hosting (#2951)

For code-hosting extensions such as an internal code-hosting service and GitHub: the underlying APIs differ, but the same components draw **the same pages and flows** (repository → code / merge requests → details → changes → review).
The app sends `4` in `context.ui_version`; `hub.extension_points.ui` in `initialize` is `4`. No new feature: older hubs cap the app's version at their own, which the extension can see. Fields are authoritative in `schema/extension/view.schema.json`.

### Pages

- **`View.tabs: {name, value, items: [{value, title?, badge?}] (2–8), action}`**: tabs below the title (Overview / Changes 3 / Commits / Checks). Selecting immediately calls `ui.action`, with `values: {name: value}` (including search `q` and filter values).
  `name` cannot duplicate field, search or filter names.
- **`View.layout: "split"` + `Section.pane: "side"`**: on wide screens (web ≥ 1024px, iPad, Mac), `side` sections go in a left column (about 300pt, stays visible with the page, scrolls independently), the rest on the right;
  on narrow screens (phones), `side` sections collapse into a "Files · 12" strip at the top, expand in place on tap and collapse after choosing a file. Narrow iOS / Mac columns (iPhone, portrait iPad with the Settings column open and a detail column below 700pt) instead navigate in place one level at a time:
  the `side` sections become the page; tapping a folder replaces them with its contents ("‹ Up one level" at the top), and tapping a file or "View all changes · N" swaps in the main sections ("‹ Files" returns), without pushing onto the navigation stack (#2961). Older apps draw all sections in order.
- **`Action.values`** (at most 8): values carried by the action. After a tap, `values` sent to `ui.action` merge in this order: page fields (with `submit`) → the component's own values (documented below for each component) → the action's values (win on duplicate names).
  A row of commits or a sequence of paths no longer needs parameters embedded in `id`.
- **`Filter.choices`**: at most 100 (previously 20; branch dropdowns). Older apps receive at most 20, always including the selected one.
- New icons: `branch`, `commit`, `code`, `pull_request`, `merge`, `file` (`nav_page` can use `branch` / `code` / `pull_request` / `merge` from these).

### Components

- **`header`**: large title, status badges (≤4, "Open", "Merged", "Draft"), a row of facts (≤8: author, `source → target`, change counts, time; with an `icon`, draw the icon rather than `label`).
- **`breadcrumb`**: `repo / src / main.go`; all but the last segment are tappable (each has its own `action`, usually with `values: {path}`).
- **`file_tree`**: `entries` are full paths (≤3000); the app builds the tree, compresses single-subdirectory chains to `a/b/c`, lets folders collapse, and shows file status letters (A / M / D / R), `+a −d` and comment counts; `selected` is highlighted. More than 12 files gets a filter box.
  Tapping a file calls `action` with `values: {path}`; **without `action`**, scroll to the `diff` on the page with the same `path` (used on PR change pages, purely client-side). Repository browsing only supplies expanded folders; unexpanded ones have `kind: dir` and no children, and tapping calls `expand` with `values: {path}`.
- **`code`**: file content (≤200,000 characters): line numbers, syntax highlighting (done by each client; `language` is a Linguist name or extension, guessed from `path` if omitted), horizontal scrolling without wrapping, copy support, and `highlight: {start, end}` to mark lines and scroll to them.
  Extensions truncate overly long content: `truncated: true` + `load`; tapping **Load remaining** calls `load` with `values: {path, from: next line number}`, and the extension returns a longer View.
- **`diff`**: one file's changes. `hunks: [{header?, old_start, new_start, lines}]`, each line prefixed: ` ` on both sides, `+` added, `-` deleted, `\` a note (`\ No newline at end of file`);
  ≤20,000 lines per diff, and ≤4 MB of code per View (`code` text plus `diff` lines).
  - View mode (unified / side by side) is an app preference: wide screens can switch to side by side, narrow screens are unified only. Two line-number columns, addition/deletion backgrounds and syntax highlighting (old and new sides highlighted separately, preserving multiline comments).
  - `threads: [{id?, side: old | new, line, start_line?, resolved?, outdated?, comments, reply?}]`: inline comment threads appear below `line` on `side`; `outdated` threads or ones whose line is not in the diff go at the end of the file.
    Reply calls `reply` with `values: {thread: id, body}`.
  - With `comment`, tapping a line number (the line itself on Android) opens a comment box; submission calls `comment` with `values: {path, side, line, body}` (deleted lines use `old` and the old line number, all others `new`).
  - With `expand`, there is **Expand** between hunks and at the file's start/end: calls `expand` with `values: {path, hunk: hunk index, direction: up | down}`; the extension returns a View with those lines merged into the hunk.
  - `collapsed: true` starts collapsed (tap the file header to expand); no `hunks` but a `load` shows **Load changes**, calling `load` with `values: {path}`: for PRs with many files, supply `hunks` only for the first few and load the rest on demand. `binary: true` only says "Binary file".
- **`comment_thread`**: discussion / timeline: avatar, author, relative time (`at` is RFC 3339), Markdown body and badges ("Approved", "Changes requested"); `event: true` draws one line of small text ("Pushed 2 commits", "Merged").
  With `reply`, a reply box at the bottom calls `reply` with `values: {body}`. ≤200 comments.
- **`composer`**: multiline input (Markdown) plus 1–4 buttons (Comment / Approve / Request changes / Merge); whichever is pressed sends the input text under `name` (together with page fields, following the button's usual `submit`);
  button `tone`: `positive` is the primary-action color, `danger` is red. Not allowed in cards (like `field`).
- Starting with UI version 4, Markdown (the `markdown` component and comment `body`) supports GFM tables and task lists (older apps treat them as text); images and HTML are still not allowed.

### Write operations

Comments, inline comments, approvals / change requests and merges are all extension `ui.action`s: the extension still goes through hub confirmation (tap Approve on the phone), and buttons can also carry `confirm` to ask locally first. Components only hand input and anchors to the extension.

### Recommended page structure

Both extensions follow this structure (demo snapshots below):

- **Repository list** (`nav_page`): search + filter, `row`s (repository name, description, language), pushing the repository page on tap.
- **Repository page**: `tabs` (Code / Merge requests / Commits). Code: `filter` for branches, `layout: split`, left `file_tree` (lazy-loaded), right `breadcrumb` + `code` (selected file, or README `markdown` when none is selected).
- **Merge request details**: `header` + `tabs` (Overview / Changes N / Commits / Checks). Overview: description `markdown`, detail `row`s (reviewers, labels, checks; tap checks to switch to the Checks page), `comment_thread`, `composer`;
  Changes: `layout: split`, left `file_tree` (changed files, no `action`), right one `diff` per file; Checks: `row`s (status badge + duration, opening the log link on tap).

Demo snapshots (for cross-client screenshot comparison, alongside the other showcase pages): `schema/extension/examples/showcase/code-overview.json` (merge request overview), `code-review.json` (changes, split),
`code-browse.json` (repository browsing: split, branch filter, lazy-loaded folders). View on the web in `web/showcase.html` (`?only=code-review&width=1408&theme=dark`); Android's `ExtensionShowcaseShotsTest` produces screenshots together with the rest.

### Entry point

`nav_page` is a direct sidebar entry: extension pages in the web sidebar (code pages automatically use the full page width), the Mac / iPad sidebar, and the Android wide-screen navigation rail (below the four tabs, opening in the Settings detail pane); after **Machines** in the new iPhone shell's drawer (#2961), and at the top of the **Settings** home on other phones.

### Compatibility

- **Older apps (`ui_version` 3 and below)**: the hub converts to drawable forms (`extension.Fit`): `tabs` → a leading `segmented` (at most 5, always including the selected one, with `badge` appended to the title); `layout` and `pane` removed (draw in order);
  `header` → text (title, badges, subtitle) plus a `row` per fact; `breadcrumb` → text joined with " / "; `file_tree` → one `row` per file (at most 60 rows, then "N more files");
  `code` → a path line plus at most 8 text blocks in `code` style (≤2000 characters each, split at lines); `diff` → a file `row` (status badge, `+a −d`) plus unified diff `code` text and the first comment of each thread;
  `comment_thread` → a `row` per comment (author, time, badge) plus `markdown` body (converted further to text for versions 1 and 2; at most 30 comments); `composer` → a `text` field plus one `submit` `button` per button;
  action `values` and new icons removed. If conversion exceeds 300 items, truncate and append a note. Older apps do not send `values` when tapping those rows or buttons: extensions supporting them inspect `ui_version` and draw accordingly (e.g. push + `state`).
- **Extension newer than the hub**: as before (unknown components removed, one log line).

## UI version 5: code review (#3136)

Builds on UI version 4's code components to make diff viewing usable for daily review: retrieving hidden lines, viewed state, generated files, multiline comments, draft reviews, suggestions, resolving threads and line links,
plus actions that return only data or replace only one component (without redrawing the page). The app sends `5` in `context.ui_version`; `hub.extension_points.ui` is `5`. Fields are authoritative in `schema/extension/view.schema.json`;
see #3136 for the design and trade-offs. Demo snapshot: `schema/extension/examples/showcase/code-review-v5.json`.

### Fields

| Where | Field | What it does |
| --- | --- | --- |
| `diff` | `context` | Fetch hidden lines: the app sends `values {path, side: new\|old, from, to}` (inclusive, `to` may be `end`), the extension returns `{lines: {path, side, start, lines, total?}}` (below), **without redrawing the page**. Returning more is allowed (the whole file is best: cached per file by the app, so subsequent expansions need no request). With `context`, the app does not use `expand` |
| `diff` | `viewed`, `mark_viewed` | Viewed state. Checking / unchecking calls `mark_viewed` with `values {path, viewed: "true"\|"false"}`; the app updates first, then sends, and any response works (empty `{}` is best). Without `mark_viewed`, the app stores viewed state locally by `path` + `revision` (diff content if absent), automatically invalidating it when the file changes |
| `diff` | `revision` | Identifier for this file's changes (blob id, revision head): local viewed state only applies to it |
| `diff`, `TreeEntry` | `generated` | Generated files and lockfiles: initially collapsed, not auto-loaded. When the extension does not specify it, the app recognizes paths (`go.sum`, `package-lock.json`, `*.pb.go`, `*_gen.go`, `dist/`, `vendor/`…); changes over 400 lines also start collapsed |
| `TreeEntry` | `viewed` | Viewed state in the file tree |
| `diff` | `similarity` | Rename similarity (0–100) |
| `diff` | `draft` | Adds **Add to review** to the comment box: same `values` as `comment`. Drafts live on the extension side (GitHub pending reviews; extension data for platforms without them), rendered in `threads` with `pending: true` |
| `diff` | `suggestions` | The extension understands ```` ```suggestion ```` blocks: adds **Suggest change** to the comment box, and renders suggestions in comments as a small diff of those lines |
| `diff.comment` / `draft` `values` | `start_line` | Multiline comments: from `start_line` to `line` (same side). The app supports drag selection or tapping two line numbers in sequence |
| `Thread` | `pending` | Draft (not submitted yet): a **Draft** badge, not counted as unresolved |
| `Thread` | `resolve` | Resolve / reopen: `values {thread: id, resolved: "true"\|"false"}` |
| `View` | `review` | Review bar at the bottom: `{count, name, placeholder?, buttons: [{title, action, tone?}] (1–4), discard?}`. Shows "Review drafts · N", expanding to a summary input (sent under `name`) + buttons (Comment / Request changes / Approve, carrying `values {event}`); `discard` discards all drafts |
| `code` | `line_url` | Line-link template (https): `{start}` and `{end}` replaced with line numbers, e.g. `https://github.com/o/r/blob/<sha>/a.go#L{start}-L{end}` |

### Action responses

`ui.action` gains two response forms, **neither redrawing the whole page** (extensions return them only for `ui_version >= 5`):

- `lines: {path, side?, start, lines: [string], total?}`: lines requested by `context` (≤20,000 lines, ≤20,000 characters per line, ≤4 MB total); `total` is the file's total line count (the app uses it to calculate how much remains hidden in the last segment).
- `items: [Component]` (≤50): replace components on the page with matching `key`s (every item must have a `key`), e.g. `load` returns just this `diff`. If a later full-page redraw returns the same `diff` without `hunks`, the app keeps the already loaded version.

The hub validates as usual (`lines` limits, `items` checked for the slot, https-only `line_url`), and removes `lines` and `items` for apps with `ui_version < 5` (which then redraw the whole page).

### Rendering on each client

- Word-level highlighting: pair a run of deleted lines with the immediately following additions, then compare words in paired lines (longest common subsequence); changed words get a stronger background. Skip lines that are too long (>1000 characters) or too dissimilar.
- Hidden lines: between-hunk bars offer "20 lines down / 20 lines up / All"; the first segment only offers up, the last only down (file length is known only after fetching lines).
- Viewed state: checkboxes in the file header and file tree; checking collapses the file. At the page top: "Viewed x / y", "Unresolved N" (tap to jump to the next), and an **Ignore whitespace** switch.
- Ignore whitespace (app-side): a deletion/addition pair differing only in whitespace is drawn as unchanged.
- Web shortcuts: `n` / `p` files, `j` / `k` changes, `]` / `[` unresolved comments, `c` comment, `v` mark viewed and next, `x` collapse, `s` unified / side by side, `w` ignore whitespace, `/` filter files, `?` shortcut list.
- Narrow screens: unified only; a floating "‹ 3 / 52 ›" bar and draft bar at the bottom, with **Submit** opening a summary sheet.
- Performance: extensions initially supply `hunks` for the first 10 files (or 3000 lines), the rest `collapsed + load`; the app auto-loads near the viewport (except generated files). On the web, every file and every hunk over 300 lines uses `content-visibility: auto`.

### Compatibility

- **UI version 4 apps**: the hub removes these fields and actions; `review` becomes the page's first section ("Review drafts · N" + `composer` + discard button), and the first comment in each draft thread gains a **Draft** badge. Older versions follow UI version 4 downgrades.
- Extensions still return a whole-page View for `ui_version < 5` apps (including the file when `load` is called).

## UI version 6 (#5464)

Pictures, controls in rows, more charts, a row's extra actions, grids and carousels, more field kinds and a few more styles: what smart-home and
market pages need. The app sends `6` in `context.ui_version`; `hub.extension_points.ui` is `6`; there is no new `features` entry (as with 4 and 5,
an older hub holds the app's version to its own, and the extension sees that). Fields are authoritative in `schema/extension/view.schema.json`
(`x-since: 6`); examples are `schema/extension/examples/View.v6-*.json`. **An app sends 6 only when it draws all of the below**: with 6 the hub no
longer downgrades, and a half-done app would skip what it does not know.

Still no arbitrary values: colors are named, icons a fixed set, text sizes two more levels.

### Components and fields

| Where | Field | What it does |
| --- | --- | --- |
| `image` (new) | `src {url \| ref, width?, height?}`, `alt`, `caption?`, `aspect?`, `fit?`, `no_zoom?` | A picture. `url` is https, loaded without credentials; `ref` is the extension's own picture (a camera, a map) served by `ui.image` through the hub, which writes `src.path` for the app (an extension that writes `path` is refused). `alt` is required (screen readers, failed loads). `aspect` `square` 1:1, `landscape` 4:3 (default), `wide` 16:9, `portrait` 3:4, `auto` (from `width`/`height`); `fit` `fill` (default, cropped) or `fit`. Tapping opens it full screen unless `no_zoom` |
| `row.thumbnail`, `row.avatar`, `header.avatar`, `Comment.avatar` | `ref` | The same as `image`'s; a thumbnail has exactly one of `url` and `ref` |
| `row` | `control {kind, name, action, on?, value?, min?, max?, step?, unit?, display?, disabled?}` | A `switch`, `slider` or `stepper`. Changing it calls `ui.action` with `action` and `values {name: "true"\|"false"\|the number}`. The row needs a `key`; `name` is a page field name like any other |
| `row` | `actions [{title, action, icon?, role?, swipe?}]` (1–4) | Extra actions: in the row's menu (long press, right click, the … button), `swipe: true` ones (at most 2) also under a trailing swipe. `role: destructive` needs `action.confirm`. **Shortcuts only**: the same must be possible on the page the row opens; older apps get none |
| `row`, `metric`, `Stat`, `Tile` | `spark {values (2–60), color?, tone?, fill?}` | A small trend line, no axes |
| `chart.kind` | `area`, `candlestick`, `donut`, `pie` | `area` follows `line`'s rules (`stacked` is a stacked area). `candlestick`: one series, every point has `open/high/low/close` (`value` is the close, used by screen readers and older apps), `low ≤ min(open, close)`, `high ≥ max(open, close)`, `volume` on all points or none, at most 240 points (other charts 120). `donut` / `pie`: one series of 2–8 points, values ≥ 0 summing to more than 0, labels unique |
| `chart` | `center` (donut), `rising` (candlestick) | The words in a donut's hole; a rising candle's color, `green` (default) or `red` as the market writes it |
| `grid` (new) | `tiles` (1–60), `max_columns?` (1–6, default 4) | Tiles in columns fitting the width (two on a phone) |
| `carousel` (new) | `tiles` (1–20), `title?`, `width?` (`narrow` / `regular` / `wide`) | Tiles in a line scrolling sideways (cameras, scenes, a watchlist) |
| `Tile` | `title`, `subtitle?`, `value?`, `icon?` or `image?`, `color?`, `badge?`, `active?`, `spark?`, `control?` (a switch), `actions?`, `action?`, `key?` | A tile. Each tile counts as a component toward a page's 300; `key`s share the page's set; a tile with a control has a `key` |
| `FieldKind` | `date`, `time`, `datetime`, `multichoice`, `multiline` | Values stay strings: `YYYY-MM-DD`, `HH:MM` (24 hours), `YYYY-MM-DDTHH:MM` local to `context.tz` (no offset), a JSON array of the chosen `value`s, the text with `\n` |
| `field` | `earliest`, `latest` (date / time / datetime), `detail` | Bounds written like `value` (`earliest ≤ latest`); a line of help under any field |
| `TextStyle` | `title`, `headline` | A section's big title and a small heading (at most 200 characters) |
| `Color` | `red orange amber yellow green teal blue indigo purple pink gray` | On `Badge.color`, `row.icon_color`, `metric.icon_color`, `empty.icon_color`, `Tile.color`, `ChartSeries.color`, `Spark.color` only, never on text. `tone` is a meaning (good / caution / bad), `color` only a category: one object never has both. A chart's series colors differ |
| `NavIcon` | 45 more (`home`, `light`, `power`, `thermometer`, `camera`, `lock`, `trend_up`, …) | Also `metric.icon`. Each app maps every name to its own icon |

Field names starting with `~` are the hub's (below) and refused in an extension's view.

**Controls**: a switch has `on` and no numbers; a slider `min < max`, `value` between them, `step > 0` (default `(max − min) / 100`) and at most 10,000 steps;
a stepper `value` and `step > 0`, `min` / `max` optional. A control's `action.values` has room for its own value (at most 7 others). The app shows the
new value at once (a slider sends when the finger lifts, a stepper 0.6 s after the last tap, one request in flight per control, the latest kept),
draws what the extension answers (best: `ActionResult.items` with just this row, by `key`), and puts back the extension's last value if the action fails.
A redraw while a request is in flight keeps the user's value for that control.

**Limits**: at most 50 pictures a page (images, rows and tiles with one); at most 100 rows and tiles with `actions` a page.
A card (`compact`) takes switches and steppers but no slider, at most 4 pictures, a grid of at most 4 tiles and a carousel of at most 6, tiles counting toward its 12 items.
A widget draws none of version 6 yet: the hub gives it what an app of version 5 gets. An assistant card (`persona-view`) takes `url` pictures but no `ref` and no controls.

### Pictures through the hub (`ui.image`, #2301)

A `ref` is the extension's own name for a picture (a camera, a map) that the app could not reach itself: the phone is often outside the home network,
and loading it directly would mean giving the app the device's token. For an app of UI version 6 the hub writes, after `Fit`, next to every `ref`
(`image.src`, `row.thumbnail`, avatars, `Tile.image`, in a view and in an action's `items`) a `path` of its own:

```
path  = /api/v1/extensions/{id}/-/image/{token}
token = base64url({e: extension, u: ui, r: ref, x: expiry}) "." base64url(HMAC-SHA256(key, payload))
```

- The token is good for 15 minutes; a view drawn again is signed again. A page on screen longer gets `410 expired` for its pictures and draws again (`ui.render` with its `state`).
- The key is derived with HKDF-SHA256 (info `persona extension image v1`) from the hub's secret key, the one that seals extension secrets and market tokens (`secretkey.Box.Derive`): nothing more is stored, and it holds across restarts, redeploys and blue-green switches.
- `GET …/-/image/{token}?w=<pixels>` is for a device with full rights, as drawing the page is (the app's own credentials: the `Authorization` header, or `?token=` on the web as for its other hub pictures); never an extension's token. `w` is rounded up to 320, 640, 1280 or 2048.
- The hub checks the signature, the expiry, that `{id}` is the token's extension, and that the extension is on and running with that ui (otherwise 404), then answers from its disk cache
  (`<hub dir>/cache/extension-images/<extension>/<sha256(ui, ref, width)>`, 256 MB in all, oldest first; an extension switched off, upgraded or removed loses its own) or asks the extension once however many ask at the same time (at most 4 calls of one extension at once, 10 s each):

```json
// hub → extension
{"method": "ui.image", "params": {"contribution": "home", "ref": "camera.front_door", "width": 1280, "placement": "nav_page"}}
// extension → hub
{"result": {"mime": "image/jpeg", "data": "<base64>", "max_age": 10}}
```

- What it answers must pass `wire.CheckImage` (shared with the conformance tests): at most 4 MB decoded; a png, jpeg, webp or gif **as read from the bytes** (`http.DetectContentType`; the declared `mime` is ignored), never SVG; its size readable, at most 8192 pixels a side and 40 M in all. Otherwise `502 plugin_failed`, a line in the log and the counter `ui.image refused` in the extension's runtime status.
- `max_age` (seconds, 0–86400, 60 when absent) is how long the hub keeps it: a camera 5–10, a map 30, a still 86400; 0 is not kept.
- The answer has the type read from the bytes, `Cache-Control: private, max-age=<the least of max_age and the token's time left>`, `X-Content-Type-Options: nosniff`, `Content-Security-Policy: default-src 'none'`, `Content-Disposition: inline`.

**Security boundary**:

- The app sends its credentials only to `path`s the hub signed; an extension's `url` is https only and loaded **without** credentials. An extension cannot use this to make the app reach an address of its choosing, nor learn the app's credentials. A `path` written by the extension refuses the whole view (502).
- A `ref` is opaque to the hub and only ever handed back to the extension and ui that drew it: a token of extension A opens nothing of B, and a token changed in any byte opens nothing.
- What the extension reaches (the device, Home Assistant) is still governed by its `permissions.network` and the egress proxy; the image proxy opens no new way out. No new permission: the pictures are the extension's own data, drawn on its own pages.
- Read-only devices get none (as with drawing the page). A leaked token is good for at most 15 minutes and still needs a device's credentials.
- Assistant cards (`persona-view`) have no `ref` pictures: nothing would serve them.


### Compatibility

The hub draws version 6 for an older app (`downgrade6`, before the older downgrades), so a page never goes blank:

| Version 6 | Version 4–5 | Version 1–3 |
| --- | --- | --- |
| `image` with `url` | a `row` with the picture as a thumbnail (version 3 too) | version 1–2: a `text` (caption or alt) |
| `image` with `ref`, ref thumbnails and avatars | a caption `text` 「图片：<alt>（请在新版 App 中查看）」; ref thumbnails left out, an avatar keeps its `text` | the same |
| `switch` | the row taps to the opposite value (`action.values`), its value 开 / 关; a row that has its own `action` keeps it and gets a row 「开关」 under it | a `toggle` field and an 「应用」 button that submits |
| `slider` | the row with its value, a `number` field, a 「设为该值」 button | the same |
| `stepper` | the row with its value, 「−」 / 「+」 buttons carrying the value a step away (held to min / max) | a `number` field and a button |
| a disabled control, or one in a card of version 1–3 | the row with its value only | the same |
| `area`, `candlestick` | a `line` (a candlestick's closes, its latest 120, detail 「收盘价 …」) | version 1: no chart |
| `donut`, `pie` | `bar` (`center` joins `detail`) | version 1: no chart |
| `grid`, `carousel` | a `row` per tile (`active` without a badge gets 「开启」; a `url` image as the thumbnail); a carousel's title as a `text` before them | the same |
| `date`, `time`, `datetime`, `multiline` | a `text` field (`placeholder` the format); `detail` joins the label | the same |
| `multichoice` | a `text` with the label, then a `toggle` per choice named `~<name>.<base64url(value)>`; more than 20 choices (or names over 128 characters): one `text` field `~csv:<name>`, values with commas | the same |
| `title`, `headline` | `body` | the same |
| `Badge.color`, `ChartSeries.color` | the nearest `tone` (`red` danger; `orange` / `amber` / `yellow` caution; `green` / `teal` positive; others none), a tone once per chart | the same |
| `actions`, `spark`, `icon_color`, `Tile.color`, new icons, `metric.icon` | left out (an app's default icon) | the same |

When an older app sends an action, the hub puts the multichoice back (`extension.CollectValues`): every `~<name>.*` toggle that is `"true"` joins `<name>`'s JSON array,
`~csv:<name>` is split at commas (`,`, `，`, `、`), and the `~` names are taken out, so the extension always sees version 6's format.
The view the extension drew is checked first, whatever the app's version (a view is refused or drawn alike for every app); then what the app gets is checked again.
Extensions may still draw an older app differently (`ui_version`), for example a `link` to the device's own web page in place of a camera picture.

## System-level placements (#1858)

Extension content **outside the app**: Home Screen widgets, Lock Screen and Apple Watch complications (the `ui` slot `widget`), actions in Shortcuts and Siri (extension point `shortcuts`),
and buttons on extension notifications. Extensions still do not run code in the app: widgets draw a subset of declarative View, while button and shortcut actions go back to the extension.
Server implementation: `server/internal/extmgr/extension_system.go` (widgets, shortcuts, device widgets) and `FitWidget` in `server/internal/extension/ui_widget.go`;
schema in `schema/extension/` (`WidgetFamily`, `WidgetPlace`, `WidgetAnswer`, `DeviceWidget`, `ShortcutItem`, `ShortcutResult`, `NotifyAction`, `ExtensionNotification`),
with types generated for all four clients. **#1147 adds Apple system surface wiring**, with build, visual, signing and device acceptance kept separate (see "Apple system surface boundaries" below); Android Home Screen widgets (Glance) can use the same endpoints later, not in this round.

The corresponding capabilities exist only when `features` includes `extension_widgets` / `extension_shortcuts` / `extension_notify_actions` (see "Notification buttons" below).
Public `/api/v1/health` deliberately omits `features` for some ingress paths, including with an Authorization header. An omitted field means unknown, not unsupported: system clients read the authenticated WebSocket `hello` before querying a catalog. An explicit feature list without the capability does not query that catalog.

### Widgets and watch faces (`widget`)

Manifest:

```json
"contributes": {"ui": [
  {"id": "usage", "title": "Usage", "placement": "nav_page", "icon": "chart"},
  {"id": "today", "title": "Today's usage", "placement": "widget",
   "families": ["small", "medium", "accessory_rectangular", "accessory_inline"], "refresh_minutes": 30, "opens": "usage"}
]}
```

- `families` (required, at least one, no duplicates): `small` / `medium` / `large` (Home Screen, Mac desktop and Notification Center), `accessory_circular` / `accessory_rectangular` / `accessory_inline` (Lock Screen and watch faces).
- `refresh_minutes`: 15–1440, default 30. Widgets refresh through their own timeline; an extension's `ui.invalidate` triggers an earlier refresh ("Refresh" below).
- `opens`: full page opened on widget tap (the same extension's `nav_page` / `extension_page`), or the app if omitted. Nothing inside a widget is tappable.
- Before install, "It can" lists "Home Screen widgets and watch faces (shown outside the app): Today's usage"; counted in the permission digest, so adding a widget in an upgrade requires consent again (like `nav_page`).

**What to draw**: the hub calls the extension's `ui.render` with `placement: widget` and `size` set to this family (`small`…), other context as for pages (`tz`, `locale`, `platform`, `ui_version`).
The returned View is checked as usual (`Fit`), then `FitWidget` trims it to what this size can draw (**trim, not reject**, with trimmed content logged):

| family | Allowed content |
| --- | --- |
| `small` / `medium` / `large` | `metric`, `stat_grid` (at most 2 stats for `small`, 4 otherwise), `progress`, `chart`, `text` (at most 3 lines, 200 characters), `row` (at most 4, not tappable); at most 2 sections, 4 items for `small`, 6 for `medium`, 12 for `large`. `markdown` becomes text, `empty` / `loading` one line of secondary text; `button`, `link`, `field`, `segmented`, `table`, `divider`, `search`, `filter`, `more`, actions, avatars and thumbnails are removed; `refresh_after` unused |
| `accessory_circular` / `accessory_rectangular` | First `metric`, `progress` or `text` (at most 3 text lines for `rectangular`, 1 for `circular`) |
| `accessory_inline` | First `metric` or one line of `text` (no progress) |

Returns 502 if nothing drawable remains. Prefer drawing directly for `size`: a `metric` plus `stat_grid` for small, add a compact `chart` for medium, one `progress` or sentence for watch faces.

**Endpoints** (full-access devices and the watch's read/answer devices may call; extension tokens may not):

| Endpoint | What it does |
| --- | --- |
| `GET /api/v1/extensions/-/widgets` | `{widgets: [{extension, extension_name, id, title, families, refresh_minutes, opens?}]}`: all widgets from enabled, running extensions. **Not in** `GET …/-/ui` (older apps do not see them) |
| `POST /api/v1/extensions/{id}/-/widget/{ui}` `{context: {size: <family>, tz, locale, platform, ui_version}}` | `{view, expires_at}`: `expires_at` is now plus `refresh_minutes`, used by the app for the next timeline entry. The hub caches by (extension, widget, family, time zone, language, platform, UI version) until `expires_at`, avoiding duplicate extension calls for background refreshes and simultaneous device requests. `size` outside `families` returns 400; a non-`widget` contribution returns 400; drawing a widget via `…/-/ui/{ui}` also returns 400 |
| `PUT /api/v1/devices/{id}/widgets` `{widgets: [{extension, id, family}]}` | Which widgets are on this device's screen (`id` is the app's own device id, as in `…/prefs`); **full replacement** (empty list clears all), at most 64; returns the stored copy. Apple reconciles `WidgetCenter.getCurrentConfigurations` on foreground entry, timeline generation and a widget background wake. A failed configuration query is unknown, never an empty replacement. Removing the last widget has no guaranteed app callback; its report is cleared on the next foreground/background wake. Stored in the hub setting `extension_widgets`, surviving hub restarts |

**Refresh**:

- Normally: the app schedules timelines by `expires_at`, and the system asks the widget extension for new content when due (served directly if the hub has a cache).
- Extension reports a change: sends `ui.invalidate {contribution: <widget id>}` (omitting `contribution` means all its widgets). The hub drops this widget's cache; online apps receive `extension_ui {invalidate}` as usual (foreground apps call `reloadTimelines` themselves);
  it also sends a background push to devices **that reported this widget**: `{"aps": {"content-available": 1}, "extension_widget": {"extension", "id"}}` (`apns-push-type: background`, priority 5),
  **at most once per device per widget in 15 minutes** (iOS budgets background pushes and widget refreshes). On receipt the app calls `WidgetCenter.shared.reloadTimelines(ofKind:)`.
- Extension lifecycle changes (disable, uninstall, enable, install, upgrade, rollback or reconfigure): the manager clears all cached widget families and contexts for that extension and rejects render replies from its earlier lifecycle. After applying the change, it sends the same background push to every device that reported one of its widgets, including widget IDs removed from the manifest. Targets come from persisted device reports, so uninstall does not need a surviving manifest. Each device/widget gets one push regardless of reported families. These lifecycle wakes bypass the 15-minute content refresh limit; ordinary `ui.invalidate` remains throttled.
- Widget and complication extensions cannot read the keychain (on real devices, though simulators allow it; see CLAUDE.md, "Common pitfalls"): hub addresses and device credentials are supplied through the existing App Group approach.

### Shortcuts (extension point `shortcuts`)

Manifest:

```json
"contributes": {"shortcuts": [
  {"id": "usage_today", "title": "Today's usage", "description": "Speak today's usage", "ui": "usage"},
  {"id": "set_budget", "title": "Set budget", "params": [
    {"name": "amount", "title": "Amount", "kind": "number", "required": true},
    {"name": "period", "title": "Period", "kind": "choice", "choices": [{"value": "day", "title": "Daily"}, {"value": "month", "title": "Monthly"}]}]}
]}
```

- At most 8 `params`, with unique `name`s matching `^[a-z][a-z0-9_]{0,31}$`; `kind` is `text` / `number` / `toggle` / `choice`; only `choice` has `choices` (1–50).
- `ui`: a full page of the same extension (`nav_page` / `extension_page`); the app adds **Open** to the result card.
- Before install, "It can" lists "Actions in Shortcuts and Siri: Today's usage, Set budget", counted in the permission digest.

**Endpoints** (full-access devices and watches; not extension tokens):

| Endpoint | What it does |
| --- | --- |
| `GET /api/v1/extensions/-/shortcuts` | `{shortcuts: [{extension, extension_name, id, title, description?, params?, ui?}]}`: all shortcuts from enabled, running extensions |
| `POST /api/v1/extensions/{id}/-/shortcut/{sid}` `{values: {name: string}, context: {tz, locale, platform, ui_version}}` | The hub checks values first: only declared parameters, required values present, numbers parseable, toggles `"true"` / `"false"`, choices one of the declared values; invalid input returns 400 `bad_request`. Then calls the extension's `shortcut.run {contribution, values, ui_version, tz?, locale?, platform?}` (20-second timeout), returning `{text?, view?}`: `text` is a sentence spoken / shown by Siri (at most 2000 characters), `view` trimmed to the `large` widget subset (Siri result card). Missing extension / shortcut returns 404, extension failure 502 `plugin_failed` |

**App side** (#1147): one generic App Intent, **Run Extension Action**, with an **Action** parameter (dynamic choices from `GET …/-/shortcuts`, labelled with the hub group and extension).
App Intents cannot generate `@Parameter` declarations from a runtime manifest. Optional **Text**, **Number**, **Toggle** and **Choice** map to the first declared parameter of each kind; **JSON Values** accepts a string-to-string object for additional fields, with explicit JSON names winning. The client checks required values and declared kinds; the hub remains authoritative. Choice options come from the selected action, not another group.
Running it uses the exact selected group's credential with `APIClient`, without the ordinary intent client's first-group fallback. `text` becomes the result value and dialog; iOS/visionOS on Swift 6.4+ adds a read-only card and **Open** when `ui` exists. Xcode 26.x, macOS and watchOS return the text/dialog without a snippet. The watch executes with its own credential.

### Apple system surface boundaries (#1147)

- `apple/Shared/ExtensionSurfaceStore.swift` holds a dedicated, bounded App Group snapshot: per-group credentials, widget/action directories and family/locale/time-zone content caches. Files are atomically replaced under a cross-process lock, mode 0600, excluded from backups and protected until first unlock. Nothing is written into Fleet's appearance store, URLs, logs or the repository. Group removal, credential rotation and authenticated 401/403 clear the affected content; generation and invalidation fences reject late render replies. A successful empty directory removes disabled/uninstalled contributions. Offline reads retain content, marked stale at `expires_at` rather than pretending it is current.
- `apple/Shared/ExtensionSystemWidget.swift` provides one configurable widget kind: iOS small/medium/large and circular/rectangular/inline accessories; `PersonaWatchWidgets` supplies watchOS 10 complications for the three accessory families. Timelines use `expires_at` and include a stale entry at expiry. Foreground reconciliation reports the **entire** current configuration set per group, including empty lists; a WidgetCenter enumeration failure is unknown, never an empty report. Foreground refresh prewarms configured content. On iOS, `extension_ui` invalidations and authenticated `extension_widget` background pushes invalidate caches and request timeline reloads, without changing notification-button handling. The refreshed directory removes disabled, uninstalled or removed widget contributions. WidgetKit/APNs scheduling remains the system's decision, not a guaranteed refresh time. The watch app does not yet handle `extension_widget` pushes; prompt complication invalidation needs a separate client change, and the existing foreground/timeline refresh remains its fallback.
- The widget transport uses the configured direct hub URL, system TLS trust and no redirects; app-only tunnels are unavailable to extension processes. Offline cached content is the fallback, not another group's token. The phone forwards optional per-group `WatchConfigs.features` from the exported hub's authenticated hello. The wrist snapshot records `answerOnly` and optional `capabilities`: missing means unknown, `[]` means known unsupported. watchOS also treats pre-metadata snapshots as answer-only, including a complication launched before its containing app updates the snapshot. When public health omits features, an answer-only wrist uses that metadata, **never** `/api/v1/ws`; unknown metadata preserves credentials and cached projections and asks for a phone sync. Full-credential phone processes retain the authenticated hello fallback. Only hello metadata changes trigger the phone's extra snapshot sync and WatchConfigs push; identical exports do not rewrite the snapshot, and metadata-only changes preserve the pairing generation/cache while fencing older catalog replies.
- Device widget reports need a formally issued device id. The wrist never lists `/api/v1/auth/tokens`. The phone migrates old watch credentials without ids by explicitly issuing a new `kind: watch` token and accepting only `role: answer`. The token listing exposes ids but no token hash, so names cannot safely identify the old token. Adoption is fenced by primary hub id, phone config, old watch config and old watch id. Unidentified legacy tokens and newly issued tokens orphaned by a concurrent pairing change are not automatically revoked: review and manually revoke obsolete Apple Watch entries in Settings › Devices after confirming the replacement works. A failed migration leaves the old wrist credential working but does not fabricate a device report.
- `apple/Shared/ExtensionSurfaceView.swift` draws only metric/stat-grid/progress/chart/text/row with `apple/Shared/Design/ExtensionSurfaceTokens.swift`, without app renderer dependencies or interactive callbacks. Charts use monochrome labelled small multiples and VoiceOver data equivalents; pie/donut data uses bars, candles use an OHLC text projection. There is no hover on system widgets. This constrained rendering is not the full interactive page renderer.
- Pairing, rotation and foreground entry sync the containing app's snapshot. The watch writes **only** its own WatchConfigs to a separate watch App Group; missing watch credentials never fall back to phone credentials. Complication links preserve group/owner/page/state, contain no credentials and reject unpaired groups. The watch resolves a complication's `opens` page to a widget contributed by the same owner and group, then uses its best-fitting declared family and context-matched cache/widget endpoint. It never requests the full `/-/ui` directory or page renderer; interactive controls and deep-link page state remain on iPhone.
- The complication requires a **new** configured/derived Bundle ID and a **new** watch App Group attached to both watch app and complication. Existing IDs and the phone's App Group stay unchanged. See [release signing](../operations/release.md) and [configuration](../operations/config.md). This change does not register identities or modify account capabilities, profiles or private configuration.
- Verification for #1147 is compiler-free only while Apple builds are paused. Swift tests were added, not executed. Xcode 26.0 compilation, Release App Intent metadata, light/dark widget and watch screenshots, actual WidgetCenter configuration removal, and first-unlock/offline/revocation/background-APNs behavior on paired devices are **pending**, not passing evidence.

### Notification buttons

When an extension sends a notification (hub `POST /api/v1/notify`, scope `notify`; the endpoint itself is in #1576, connected here after merging), it may include:

```json
{"title": "Example gateway panel", "body": "90% of today's budget used", "level": "active",
 "ui": "usage", "state": "range=1d",
 "actions": [{"id": "raise", "title": "Add ¥10 to budget"},
             {"id": "note", "title": "Reply", "input": {"placeholder": "Say something"}},
             {"id": "stop", "title": "Disable this key", "role": "destructive", "foreground": true}]}
```

- At most 3 `actions`: `id` (1–128), `title` (1–40 characters, button text never wraps), `role?` (`normal` / `destructive`), `foreground?` (open the app before running; background if omitted), `input?: {placeholder?}` (ask for one line of text first).
  `ui` is the page opened by tapping the notification itself (a `ui` contribution from the same extension, with `state`), and the destination for button actions.
- In the push: `aps.category` is `ext.<extension id>.<hash of button ids>` (same category for the same button set), `extension_notification: {extension, ui?, state?, actions?}` (schema `ExtensionNotification`).
  Direct APNs with extension buttons also includes `aps.mutable-content: 1`, so the Notification Service Extension registers a category from `actions` before delivering the notification (`UNNotificationCategory`; `input` uses `UNTextInputNotificationAction`), covering the first background notification with new buttons. The relay keeps its existing encrypted envelope and `mutable-content`; ordinary notifications and extension notifications without buttons do not enable the service extension for this, and existing static interaction categories keep their behavior.
- **Button tap**: the app calls `POST /api/v1/extensions/{id}/-/ui/{ui}` `{action: <button id>, state, values: {"text": <input text>}}`, going through `ui.action` exactly like a page button;
  for background actions, response `toast` is shown as a local notification and `open_url` / `push` ignored; `foreground` opens the app on the `ui` page first, then calls and handles the response as a page would. Tapping the notification itself opens `ui` with `state`.
- The server already supports `actions` / `ui` / `state` on `POST /api/v1/notify` (scope `notify`) and reports `extension_notify_actions` in `features`; example: `scripts/mock/apns/extension-notification.apns`.
- **Apple notification integration (part of #1862)**: the iOS / macOS notification service extension registers dynamic categories before delivery, and app startup merges static categories without overwriting extension buttons. Foreground notifications register first too, before deciding whether to show.
  When the source is clear, use only the group specified by `hub_id` or the decryption key; unknown or conflicting sources do not execute and do not fall back to the current hub. Old notifications without a source route only when there is one hub group.
- Foreground actions are first claimed by the system entry point held by AppModel, then executed with the notification's original `state`, afterwards sharing the ordinary extension page. Execute before rendering so a new state returned by rendering cannot replace the notification state; appearance changes or page rebuilding do not replay the action.
  The system entry temporarily shows a full page without competing for modal presentation with an open Settings sheet; **Done** returns to the original main page. Background buttons show only `toast`, not pages, links or navigation results; expiry cancels the request and completes the notification delegate callback once.
- **Validation boundary**: `simctl push` does not run the Notification Service Extension. Dedicated local notifications can verify buttons, input and app callbacks, and unit tests of the shared registration logic can verify category merging, but neither proves that the first real-device background APNs notification was verified.
  Home Screen widgets, watch complications and the generic App Intent are wired in #1147; device acceptance remains pending. Notification integration and compiler-free checks do not establish system-surface acceptance.

### Mock hub

In the `MOCK_EXTENSIONS=1` mock hub, `example-gateway` has a `today` widget (five sizes, opens `usage`) and two shortcuts (`usage_today` returns a sentence and a card; `set_budget` requires an amount).
`PUT /api/v1/devices/{id}/widgets` stores in memory; notifications with buttons use the `.apns` example above. Only on the new `/api/v1/extensions/…` paths.


## Personal Agent sources

Each `agent_sources` entry is a personal Agent source on the Agents page (the hub has no sources of its own; all Agents-page sources come from extensions); no app changes needed:
the endpoints remain `/api/v1/agents*` (`docs/protocol/protocol.md` ("Personal Agents")), session ids are `<contribution id>:<sessionKey>`, grouped and named by `provider`.
Used to connect directly to a personal Agent service (e.g. one with real-time push, avoiding the once-a-minute polling used by the Lark gateway).

- **id**: cannot duplicate another extension's. Session ids are `<id>:<sessionKey>`, so keeping the id across upgrades preserves app sessions, unread state and cache. The source appears on the Agents page when the extension is enabled and running, and is removed first on disable, uninstall or upgrade
  (the app refetches on `{"type":"agents"}`). If the process crashes, times out or fails to start, the source remains: `status` carries an error and listing all sessions or searching skips it (other sources work as usual);
  an error is returned only when querying it alone.
- **Hub calls the extension** (`agentsrc.<method>`, parameters include `contribution`; fields in `server/internal/agents/wire.go`):

| Method | Parameters | Returns |
| --- | --- | --- |
| `status` | — | `{enabled?, setup?, providers?, accounts?}`: omitted `enabled` means `true`; `setup` is `no_accounts` etc. (see protocol.md); omitted `providers` uses the manifest's |
| `sessions` | — | `{sessions: [{sessionKey, accountId?, provider, title, kind?, parentSessionKey?, canSend, unreadCount, rankTimeMs, avatarUrl?}]}` |
| `messages` | `session_key`, `cursor?`, `limit` | `{session?, messages: [{externalId, sessionKey, role, blocks, createTimeMs, position}], hasMore, nextCursor?}` |
| `send` | `session_key`, `client_id` (UUID), `text` | Any JSON (passed unchanged to the app); deduplicate by `client_id`: the hub retries failures with the same id |
| `delete_message` | `session_key`, `client_id` | `{}` |
| `mark_read` | `session_key` | `{}`; return `unsupported` if unavailable, treated as no unread messages |
| `search` | `q` | `{hits: [{sessionKey, externalId, text, role, createTimeMs, title, provider}]}`; return `unsupported` if unavailable, treated as no results |

- **Error codes**: missing session `not_found` (app sees 404), invalid parameters `invalid` (400), cannot send now `conflict` (409), unsupported `unsupported` (501);
  other errors contain only `message`, and the app sees 502.
- **Extension event to the hub** `agentsrc.updated {contribution?, session_keys?}`: these sessions changed (new messages, unread state, title); omitting `session_keys` means the list changed.
  The hub clears caches and pushes `{"type":"agents","session_ids":[…]}`, and the app refetches immediately. Only this extension's own sources are accepted. The hub also pushes after its own sends, then again at 4 / 15 / 45 seconds;
  every 60 seconds it relists all sources as a fallback.
- Implementation: `server/internal/extmgr/extension_agents.go`; tests `TestExtensionAgentSource` (`extension_agents_test.go`, fake extension exercises the protocol),
  `agents.TestAddAndRemoveSources`.

## Events

Extensions contribute `events` to subscribe to things happening on the hub. These only **notify** the extension (`event.<name>` events, no response awaited); it cannot intercept or alter anything through them. To act, use permissions it has
(such as `hub` scopes). Subscribed events are listed before install like permissions, and subscribing to more in an upgrade requires consent again. The extension must use `on_startup` (events go only to running extensions; an event does not start a process).

| Event | Parameters |
| --- | --- |
| `inbox.changed` | `id`, `source`, `kind`, `title`: new or updated Inbox item (if more than 50 items are stored at once, only the first 50 are sent) |
| `agent.needs_you` | `machine_id`, `machine`, `session_id`, `interaction_id`, `kind`, `summary`: an Agent is waiting for your answer or approval |
| `agent.finished` | `machine_id`, `machine`, `session_id`, `title`: an Agent finished |
| `todo.changed` | `id`, `title`, `thread?`, `done?`, `what`: a to-do was created, deleted, or its title, whether it is done, or the thread it is bound to changed (needs `todos.read`, see below) |
| `todo.decision` | `id`, `title`, `thread?`, `decision`: a decision was written on a to-do (needs `todos.read`, see below) |

SDK programs receive them in `Namespaces["event"]` (method name is the event name).

### To-do events (#5279)

`todo.changed` and `todo.decision` let a connector extension follow the to-dos, for instance to post into a chat thread it owns, in the user's name rather than the bot's. Schema: `schema/extension/todos.schema.json` (`wire.TodoChangedEvent`, `wire.TodoDecisionEvent`).

```json
{"permissions":{"hub":["todos.read"]},"activation_events":["on_startup"],
 "contributes":{"events":[{"id":"todos","subscribe":["todo.changed","todo.decision"]}]}}
```

- **Who is told**: an extension that is enabled, has that contribution on, has its program running, subscribed to the topic **and** was granted `todos.read` (or the legacy full access). Subscribing without the scope is told nothing; `todos.write` alone, or another scope, is not enough. (The user agrees to a manifest's permissions as a whole at install; the grant is what is checked, not the declaration.) Declaring `todos.read` lets the extension read the to-dos and their decisions (`GET /api/v1/todos`, `GET /api/v1/todos/{id}/decisions`) anyway, which is why these events carry the titles and decision text.
- **`todo.changed {id, title, thread?, done?, what}`**: sent when a to-do is created (`what: "created"`), deleted (`"deleted"`; `title` and `thread` are the last it had), or its title, whether it is done (including a task that failed to start), or its bound thread (bound, rebound, retitled, unbound) changed (`"updated"`). Changes to one to-do (or several) within about a second are told once, as each to-do is at the end of that second; a to-do that is back as it was is not told. Notes, agents, spaces, stage, delivery and decisions changing do not send it. `thread` is `{channel, chat_id, thread_id, title}`, as in `PUT /api/v1/todos/{id}/thread`, and left out when the to-do has none; `done` is `true` while the to-do is done (closed, or its task failed to start) and left out otherwise. To know more, read the to-do again.
- **`todo.decision {id, title, thread?, decision}`**: sent once for every decision written, at once (not merged). `decision` is `{id, question, choice, reason?, source, supersedes_id?}`; `source` is `manual`, `card`, `outcome`, `agent` or `thread`. A decision that comes from the to-do's own chat thread (`thread`) **is** sent: the extension may forward it elsewhere. One that posts decisions into the thread should skip `source: "thread"` itself, or it echoes the user's reply back (the built-in bot posts do the same). Marking an old decision superseded does not send anything.
- **No order between the two events**: `todo.decision` and `todo.changed` are separate events, and the extension's handlers may run at once, so a decision can be heard before the `todo.changed` that came earlier (and the reverse). Use each event's own fields, and read the to-do when the order matters.
- They go through a queue of their own (512; the knowledge base's is another), in which a `todo.changed` for a to-do that has one waiting is merged into it (a backlog is at most one a to-do: a to-do created and deleted before the extension was told is none, one created and then changed stays `created`, with the latest state). Past the limit the oldest is dropped and the log says so. Like `kb.changed` they are not stored for redelivery, and a restarted extension reads the to-dos again first.

## Error reports

Extensions with an `error_reports` contribution (and the permission `error_reports`) are given the errors and crashes of the hub, the manager and the apps (#5533), for example to forward them to an error tracker. The main repository has only this generic pipeline; which tracker, its address and its key are the extension's (the Sentry extension in the official marketplace keeps its DSN as a required secret).

- **Off unless an extension takes them**: reporting is on only while an enabled, running extension has an `error_reports` contribution switched on. The manager then adds `error_reports` to `features` (the WebSocket `hello`, `GET /api/v1/health`); the hub, the manager and the apps collect and send only while it is there. An extension whose required secret is missing doesn't start, so it turns nothing on.
- **What goes in**: the apps send `POST /api/v1/error-reports` ([protocol.md](../protocol/protocol.md)); the hub's handlers' panics (answered with 500) and its log's error lines (`panic`, `error`, `failed`, a 5xx request) go to the manager over the bus (`extensions.error_reports`); the manager's process reports its own log's error lines.
- **Format**: `ErrorReport` in `schema/extension/protocol.schema.json`: `id` (the sender's, for dedupe), `source`, `kind` (`exception` / `crash` / `anr` / `panic` / `log`), `level` (`fatal` / `error` / `warning`), `time`, `release` / `build` / `commit`, `os` / `os_version` / `device_model` (the model only), `message`, `exceptions` (`type`, `value`, `frames`, innermost last), `breadcrumbs`, `tags`, `previous_launch`. The manager adds `fingerprint`, `count` and `received_at`. Longer fields are cut: message 1 KB, 5 exceptions of 64 frames, 30 breadcrumbs of 200 characters, 16 tags.
- **Scrubbing** (`server/internal/errreport`, applied to every string before an extension sees it; clients don't put user content in reports to begin with): quoted spans in messages become `"<redacted>"`; named secrets (`token=`, `password`, `Authorization`, `Bearer …`), known key prefixes, JWTs and long hex / base64 become `<redacted>`; home directories become `~` (no user names); e-mail addresses `<email>`; IPv4 / IPv6 `<ip>`; URLs keep scheme and path only (`https://<host>/api/v1/inbox`); the hub machine's host name and user name, and every computer's name, id, SSH host, host name and user name become `<machine>`.
- **Dedupe and limits**: the same `id` within 24 hours is dropped (a retry); the same fingerprint within 10 minutes is folded, and the next one after that carries `count`; at most 30 reports a minute per device, 60 from the hub and the manager, 300 overall.
- **Delivery**: notifications `error_reports.deliver {contribution, reports}` (`ErrorReportsDeliver`), at most 20 reports or every 2 seconds, one per contribution with the sources it takes. Nothing is persisted for redelivery: a report lost while the extension restarts is lost.

## Auth relay

Extensions with an `auth_relay` contribution (and the permission `auth_relay`) complete the authorization links the user's Agents stop at, with the account the extension is signed in with (#5644): before the triage puts such a link on a "Needs you" card, the manager hands it to the extension whose contribution names its host and path, once it has checked that the Agent's own command printed it on one of the user's computers. The program answers `auth_relay.complete {contribution, url}` with `{status, app?, reason?}` (`AuthRelayCompleteParams`, `AuthRelayResult`); `manual` with `not_handled` passes it to the next extension. Contributions start off. The flow, the checks, what an extension must do and the security trade-offs are in [auth-relay.md](../operations/auth-relay.md).

## Sending notifications

When an extension decides "this needs a person", it sends a phone alert through the hub: `POST /api/v1/notify` (scope `notify`, or full access; use `PERSONA_HUB_URL` / `PERSONA_HUB_TOKEN`); parameters in `docs/protocol/protocol.md`.

- **The extension chooses the level**: `active` (default, normal notification) or `time-sensitive` (breaks through Focus, vibrates the watch when the phone is locked; users can turn it off in system settings). iOS needs the app's `com.apple.developer.usernotifications.time-sensitive` entitlement to truly show it as time-sensitive; without it the system treats it as `active` (the hub's own "Needs you" pushes are affected too, see #1572). No `critical` (requires separate Apple approval) or `passive`.
- **Responsibilities**: when to notify, how often per session and how to detect a stall are the extension's business; the hub only delivers and honors user settings: no notification when the device has **Needs you** (`input`) off, the session is blocked, or the phone is muted. The hub's own pushes (questions, permissions, completion) stay as before; extensions cannot intercept them and do not cause them to be sent twice.
- **Opening**: alerts with `machine_id` + `session_id` are grouped by session and open that session on tap; without them it is just a message. Buttons are covered in "System-level placements", "Notification buttons".
- Example: subscribe to `agent.needs_you` / `agent.finished`, then poll `GET /api/v1/sessions`; if a session is stuck waiting for confirmation (no screen reading, no interaction card created by the hub) or has been spinning for 30 minutes, send one `time-sensitive` alert, at most once per session in 30 minutes.

## Model providers

Extensions contribute `providers` to declare model providers: address, connection type and models. These can be purely declarative extensions (no program). `GET /api/v1/launchers/presets` lists providers from all enabled extensions
(`{presets: [{id: "<extension>/<provider>", plugin, name, description, kind, api, base_url, model, small_model, models, key_hint, models_error?}]}`);
for `dynamic_models`, the hub calls the extension's `provider.models {contribution}` → `{models: [{id, name?}]}` and merges with declared models (on failure, declared models remain and `models_error` explains).
When creating a launcher with `POST /api/v1/launchers` and `preset`, omitted Base URL, connection type, API, model and small model are filled from the preset; the user only enters a name and key. The launcher records its source preset.
In the app this is **Settings › Model Providers › Add**: iOS shows **Add from Extension** only when presets exist; the web lists them alongside hub built-in presets in the first step, with an **Extension** badge. See `docs/integrations/launchers.md` ("Add from plugin").
The new-Agent model picker also lists preset models: launchers added from this preset, and those with the same gateway address (manually added or from ~/.zshrc), get all preset models appended after the `/model` options.

## Purely declarative contributions (#1854)

What extensions with only a manifest and a few files, no program, can do (model `providers` covered above). Like other contributions, these can be switched individually ("Contribution switches"), and all disappear when the extension is disabled.

| Extension point | Form | Destination |
| --- | --- | --- |
| `skills` | `{"id": "weekly-review", "dir": "skills/weekly-review"}`: directory in the package containing `SKILL.md` | Same path as `assistant`'s `skills`: sent with `assistant.prepare` to the computer running the assistant and placed in the assistant directory (see "Assistant") |
| `prompts` | `{"id": "tone", "kind": "instructions", "file": "assistant/tone.md"}` | Appended to the assistant's CLAUDE.md (like `assistant.instructions`) |
| | `{"id": "today", "kind": "quick", "title": "What did I do today?", "text": "…"}` | Quick prompts in the app input: `GET /api/v1/extensions/-/prompts` → `{prompts: [{extension, extension_name, id, title, text}]}` |
| `themes` | `{"id": "forest", "name": "Forest", "light": {"accent": "#2E7D32", …}, "dark": {"accent": "#81C784"}}`: only design-system role colors (`accent` required, `positive`, `caution`, `danger`, `ai` optional), not fonts, icons or other colors | `GET /api/v1/extensions/-/themes` → `{themes: [{extension, extension_name, id, name, light, dark, problems?}]}` |
| `triage_templates` | `{"id": "quiet-groups", "title": "Only mentions in large groups", "text": "…"}` | Selectable when filling a source's triage rules (`triage_rules`): `GET /api/v1/extensions/-/triage-templates` → `{templates: [{extension, extension_name, id, title, text}]}` |

- Install checks for `SKILL.md` and `.md` files in the package (like `assistant`). Any device may read the three `GET`s; they list only enabled contributions of enabled extensions. Extension tokens cannot call them (full credentials only).
- **Theme-color contract (for clients)**: the hub calculates WCAG contrast for each role color in light (against white) and dark (against black), recording values below 3 in `problems` ("light.accent #FFF59D contrast 1.2, below 3").
  Clients: themes with `problems` do not appear in **Settings › Appearance › Theme Color**; others appear as a new theme color (`ThemeColor`) named `name`, replacing only supplied role colors and using the default theme's other colors,
  then pass through each client's own checks such as `StyleSpecTests` (real backgrounds, every style); failing themes are likewise hidden. Disabling the extension or contribution resets users of that theme to the default.
- **Quick prompts**: clients list them in input quick prompts / slash menus (single-line truncated titles); selection puts `text` in the input, without sending it.
- **Triage templates**: when editing a source's **Triage Rules** in Settings, available templates add **Fill from Template**; selection fills `text` (confirm before replacing).

### Apple management and declarative contributions (#1861)

- Details prioritize individual `contributions` switches. `can_disable: false` is not actionable; `via: connector` says "Follows connector". Older hubs without these fields still show the original `contributes` summary, without guessing switches. `depends` shows current dependency state; `extension_pack` lists bundle members.
- When disabling or uninstalling returns `has_dependents`, list the affected objects returned by the server and send `cascade` only after a second confirmation. Bundles offer uninstalling just the bundle or its members too; report successful member counts only from `removed_members`, never describing partial success as complete.
- Install confirmation lists permissions for the main preview and every package in `bundle`, submitting each `preview_id` and `permissions_digest` in original order. Confirmation is blocked if the main preview or any member expired, has `unresolved`, or still has unready dependencies; the page task waits for the earliest expiry among main and member packages, updates confirmation state when due, and checks once more before submission.
- Extension prompts join the existing input's plus menu; BeautifulUI inputs outside the new shell also wire them into the slash menu. Selection only fills and focuses, never sends. Templates appear only for `triage_rules` string fields in existing config forms: confirm before replacing, still require Save afterwards, and do not restore the old triage-management page.
- Themes enter the design system through `ColorSpec` / `StyleSpec`, changing colors only when **Extension Theme** is selected, without altering existing styles or fixed themes. Reject server `problems`, malformed values or insufficient contrast on actual backgrounds; check light/dark pages and cards in all five styles, white text against accent colors, and bubble text. Missing roles keep original defaults, without auto-adjusting colors to pass.
- Themes, prompts and templates are read only while the page and app are foregrounded, refreshed every 60 seconds. If the selected theme contribution disappears or the hub returns 404, fall back to following the style; temporary network errors for the same source keep the previous result. Switching hubs never borrows themes or prompts from another group.
- Offline cache for the selected theme lives in local Application Support, atomically written with `0600` permissions, only when content changes. Unsaved forms stay in `ExtensionDrafts` memory, isolated by hub and extension; appearance rebuilding and refetching do not overwrite them. Remove after successful save; never store secrets or write UserDefaults.

## Avatars (`looks`, #3200)

Extensions add avatars to Persona: name, avatar image (multisize PNG, optional SVG, optional light/dark variants), colors, animated status dot in the top bar, and which built-in avatar's icon to use where only app-bundled icons work.
"Avatar" and "Logo" are one concept: Settings has one **Avatar** option, which changes the avatar image, brand mark, colors, favicon and desktop icon (where changeable) together. Example: `schema/extension/examples/example-looks/` (two avatars, images generated by `scripts/assets/make_example_looks.py`); install with `personad extension install dir:schema/extension/examples/example-looks`.

```json
"looks": [{
  "id": "aurora", "name": "极光", "names": {"en": "Aurora", "ja": "オーロラ"}, "subtitle": "A glowing little jellyfish",
  "avatar": {"sizes": {"128": "looks/aurora/avatar-128.png", "256": "looks/aurora/avatar-256.png"}, "svg": "looks/aurora/avatar.svg",
             "dark": {"sizes": {"256": "looks/aurora/avatar-dark-256.png"}, "svg": "looks/aurora/avatar-dark.svg"}},
  "colors": {"light": {"primary": "#5B6CFF", "accent": "#3F4FE0", "bubble": "#3F4FE0", "bubble_text": "#FFFFFF"}, "dark": {"accent": "#8C98FF"}},
  "dot": {"size": 144, "states": {"idle": {"file": "looks/aurora/idle.png", "fps": 6}, "working": {"file": "looks/aurora/working.png"}}},
  "app_icon_fallback": "persona"
}]
```

| Field | Rules |
| --- | --- |
| `id` | Avatar-package id rules (clients use it as a cache-directory name); globally unique among `looks`. `persona` and all five retired bundled IDs remain reserved (see [persona.md](../apple/persona.md)); `nwverse` belongs to the compiled hub pack and the hub's version wins. A valid disk pack with the same nonreserved id takes precedence over an extension |
| `name` / `names` | Source language 1–20 characters; `names` only allows `zh-Hans` / `en` / `ja`, each 1–40. Clients resolve `names[UI language]` → `names.en` → `name` |
| `subtitle` / `subtitles` | Second line on the Settings card, ≤40 characters |
| `avatar.sizes` | Side length (16–1024) → square PNG in the package, actual side length matching the key; at least one (bitmaps for watch, Lock Screen and places without SVG rendering) |
| `avatar.svg` / `avatar.dark` / `avatar.safe` | Optional vector avatar (≤256 KB, sanitized by the hub before delivery, preferred by web); dark `sizes` / `svg` (missing entries use light ones); `safe` as for avatar packages |
| `colors` | `light` / `dark`, keys only `primary`, `secondary`, `accent`, `bubble`, `bubble_text` (aligned with Apple's `PersonaPalette`, docs/apple/persona.md ("Color families")), `#RRGGBB`. Insufficient contrast is darkened by clients under avatar-package rules, not rejected |
| `dot` | Optional APNG or PNG per each of five states (`idle` required), frame side ≤512, matching `size` if specified. Without `dot`, the hub uses the largest avatar PNG as `idle` |
| `app_icon_fallback` | New manifests must name `persona`, the only bundled drawing/icon. For a valid custom look in an older manager report, the hub normalizes retired or unknown icon values to Mochi without dropping the custom picture |

**Hub delivery** (`server/internal/hub/looks.go`):

- Each enabled avatar becomes an avatar package (format 1): the hub synthesizes persona.json from the entry (adding `names`, `subtitles`, `avatar.svg`, `avatar.dark`, `app_icon_fallback`, `source`, absent in format 1 and ignored by old apps),
  exposing only the named files (no other extension-package files), with sanitized SVG. Thus `GET /api/v1/personas` lists it too, and apps not yet upgraded can still select it.
- `GET /api/v1/looks` is the new unified list: built-in avatars (`source: builtin`, drawn by the app, no files), hub avatar packages (`hub`), extension avatars (`extension`, with `extension` / `extension_name`); each has `app_icon` (itself for built-ins, `persona` for packages, `app_icon_fallback` for extensions); `current` is the selected avatar. See [protocol.md](../protocol/protocol.md).
- **Validation** (at install, rejecting failure; again before every delivery): ordinary package files (no escape from the package, no dot-prefixed files), PNG ≤4 MB, decodable, square, side in range and matching the key; SVG ≤256 KB and sanitizable; ≤16 MB per avatar.
- **SVG sanitization** (`wire.CleanSVG`): retain only drawing elements (`svg g path circle ellipse rect line polyline polygon defs linearGradient radialGradient stop clipPath mask use symbol filter fe… text tspan title desc`);
  remove other elements (`script`, `foreignObject`, `style`, `image`, `a`, `animate`…) with their content; remove `on*` attributes, external `href`s, `url()` other than `url(#…)`, `javascript:`, comments and DOCTYPE; reject entities; root must be `<svg>` with `viewBox` or width/height.
  Responses also carry `Content-Security-Policy: default-src 'none'; style-src 'unsafe-inline'; sandbox`; web renders through `<img>`.
- **Cache**: as with avatar packages, clients cache by `rev` (including synthesized persona.json and sanitized SVG); file URLs with the current `?rev=` return `Cache-Control: private, max-age=31536000, immutable`.
- Installing, uninstalling, upgrading or disabling an extension with avatars broadcasts `personas`.

**The selected avatar is stored on the hub** (setting `look`); one hub belongs to one person, and clients follow it: `PUT /api/v1/looks/current {"id"}`, broadcasting `look` on change.

- **Retired bundled IDs**: neither disk packs nor extension reports can reclaim them. A saved retired or unknown choice receives `current.effective: "persona"`; the raw ID and timestamp remain for compatibility. Clients never try to draw a removed built-in option.
- **Disable** the extension or contribution: selection remains, `current.effective` returns `persona`; re-enabling restores it. **Uninstall**: if its avatar is selected, reset selection to `persona` and broadcast `look`.
- **Migration from old local client settings** (iOS `UserDefaults persona.id`, Android `persona` in `persona-appearance`, web `localStorage persona.persona`):
  connect to a hub with `/api/v1/looks`; if hub `current.id` is `null` and a local choice was explicitly made → `PUT {"id": local selection, "if_unset": true}`, honoring the response (the first connected device's choice becomes everyone's); if the hub already has a choice → use it;
  no local choice → do not upload; old hub (404) → use the local choice as before. Local selection remains a cache, displayed on startup without waiting for the hub.

**Can desktop icons follow the avatar?**

| Location | What can follow | Why |
| --- | --- | --- |
| Web favicon, notification icon | Any avatar (including extensions) | Replace `link[rel=icon]` at runtime, dark avatar via `prefers-color-scheme`; unread badge still overlaid |
| Installed web PWA icon | Does not follow, always Mochi | Icon taken from the manifest at install (fixed when iOS adds to Home Screen; Chrome WebAPK only updates on periodic manifest checks with user confirmation); manifest is static and shared by everyone |
| Android desktop icon | Currently always Mochi | Can only switch between predeclared `activity-alias` entries in AndroidManifest; only Mochi is bundled, and switching makes some launchers remove pinned shortcuts/widgets and may kill the process. Future built-in avatar icons will follow `app_icon` |
| iOS desktop icon | Built-in avatar icons | `setAlternateIconName` only selects bundled icon sets, chosen by `app_icon`; Apple integration |

Extension tests use `exttest.CheckLooks(t, manifest, dir)` to check avatar files under hub rules.
Screenshot mock hub: `MOCK_LOOKS=example python3 scripts/mock/mock_hub.py` lists both example-extension avatars (at `/api/v1/looks` and `/api/v1/personas`); `MOCK_LOOK=<id>` selects it at startup, and `PUT /api/v1/looks/current` pushes `look`.

## Agent filtering (#2428)

Claude / Codex sessions launched on your computer by other platforms (such as a cloud Agent platform's workspace chats and automation, thousands per week) share `~/.claude` and `~/.codex` with your own sessions,
so blocking config directories cannot separate them. An extension for that platform declares their markers through `agent_filters`; the hub sends active rules to every computer's node (`agent_filters` in `node.configure`,
once on connection, again on rule changes, followed by another session-list fetch). The node removes matching sessions when listing: the Agents page, `agents_list` and real-time snapshots all use this list, so none show them. Session files are untouched; turning off rules brings them back, including existing historical sessions.

```json
"configuration": {"type": "object", "properties": {
  "hide_agents": {"type": "boolean", "title": "Hide platform-launched Agents in Persona", "default": true}}},
"contributes": {"agent_filters": [
  {"id": "codex", "title": "Platform Codex sessions", "when": "hide_agents", "agents": ["codex"], "originators": ["codex-bot"]},
  {"id": "claude", "title": "Platform Claude Code sessions", "when": "hide_agents", "agents": ["claude"], "added_dirs": ["~/.bot/skills"]}]}
```

- **Matching**: for sessions in `agents` (both types if omitted), remove a session if any marker matches:
  - `originators`: exact match against `originator` in the Codex rollout's first-line `session_meta` (Persona launches use `persona`, CLI `codex-tui`, desktop app `Codex Desktop`);
  - `added_dirs`: additional working directories (`additionalWorkingDirectories`) in Claude Code transcript environment attachments equal or descend from this directory; absolute or `~/…` paths, at least two levels;
  - `first_message_prefixes`: first user message (leading whitespace stripped) begins with this text, 8–200 characters. Only a fallback for sessions without stable markers: overly broad prefixes can hide your own sessions.
- **Switches**: a rule with `when` is active only when that boolean config field is true (must exist in `configuration` and be `boolean`, otherwise installation fails).
  This field is its only switch: contribution lists show `via: "config"`, `can_disable: false`, and switching individually returns 409. Without `when`, rules follow the extension switch and can also be turned off individually in the contribution list. Disable or uninstall the extension and its rules disappear.
- **Never removed**: assistant sessions, reserved sessions (main chat), forked sessions and Agents in hosts (herdr panes, runtime). Rules inspect only transcripts / rollouts found on disk.
- **Cost**: the node reads each file's beginning (at most 512 KB, stopping after the first message), caching results per file; Claude sessions are filtered before the "latest 80" cutoff, so platform sessions do not crowd out your own.
- Code: `server/internal/extension/agentfilters.go` (extension point and validation), `server/internal/extmgr/agent_filters.go` (distribution), `server/internal/node/agentfilters.go` (marker reading and filtering), `server/internal/proto/agentfilters.go` (rules and matching).

## In-App tunnels

An extension contributes `tunnels` to tell the App that this hub can also be reached through an **in-App tunnel**. When the phone cannot reach the hub's own address (for example, the hub is on a dedicated network), the App starts a tunnel inside its own process and connects to the hub's address on that network through it. Only Persona's traffic to the hub uses it; it does not create a system VPN. See [network.md](../operations/network.md) ("Connecting to the hub through an in-App tunnel").

- **Pure declaration**: no program runs on the hub. The tunnel is a tunnel core compiled into the App at build time (a C static library; interface in network.md). Apps without this core show no entry.
- **Destination**: extension settings `target_host` (required, hostname or IP resolved by DNS inside the tunnel) and `target_port` (default 18660, the hub's HTTP port). Install and upgrade validate both fields; invalid values prevent installation.
- **How the App reads it**: on every hello, request `GET /api/v1/extensions` once and find enabled extensions whose `contributes` contains `tunnels`. Then read `config` and `contributions` from details (full-access devices), using the enabled item with `point` equal to `tunnels` and `title` equal to `name`. Store the result in pairing information for use when the hub is unreachable. Disabling or removing the extension, or disabling this contribution, removes both the entry and tunnel. Only the first tunnel is used for each hub.
- Sign-in information such as accounts and nodes is managed by the tunnel core's own control panel, only on the phone, never through the hub.

## App: Settings › Extensions

On iOS (Mac shares the same pages), when the hub reports `extension_manager`, the Settings home gains an **Extensions** row below connectors (count and how many need attention). Code in
`apple/Persona/Views/Settings/ExtensionsModel.swift` (data, endpoints, view model) and `ExtensionsViews.swift`.

**New and old hubs** (#1859): when the hub's `features` includes `extensions`, extension requests use `/api/v1/extensions/…` (`APIClient.extensionsAPI`, set from features by `HubLink.api`;
all paths through `APIClient.extensionPath`); otherwise (old hub) `/api/v1/plugins/…`. Responses accept both key sets: lists/catalog `extensions` / `plugins`,
UI placements `extension` / `extension_name` and `plugin` / `plugin_name`, marketplace counts `extensions` / `plugins`; marketplace-preview request bodies use `extension` on new hubs and `plugin` on old ones.
Features accept new and old names (`hasExtensionFeature("ui_slots")` means `extension_ui_slots` or `plugin_ui_slots`, likewise `"manager"`); both `extension_ui` and `plugin_ui` events
are accepted (new hubs send both; badges are set values, so receiving twice is harmless); channel `settings_url` accepts `settings/extensions/<id>` and `settings/plugins/<id>`.
Views, components, actions, responses, placements and context all use schema-generated types (`apple/Shared/Generated/ExtensionWire.swift`, #1860); the app only adds decoding helpers alongside them
(`ExtensionUIModel.swift`: `ExtensionUIPlace.owner` accepts both key forms, `ExtensionUIContext.here(…)` includes time zone, language, platform and `ui_version: 3`).

- List: each extension's name and status (**On** / **Starting** / **Needs Setup** / **Off** / **Not available on this hub** / **Something Went Wrong**; both `running` and `stopped` are **On**), opening details on tap; footer explains installation via the hub's admin UI (`/admin`) or on the hub machine (`personad extension`).
- Details: status, version, source, description, small running-state text ("Running" / "Not running; starts when needed"), "Missing: …" and errors; **Enabled** switch; **Settings** form drawn from `form.fields` (text, numbers, toggles, choices, text lists / multiselect,
  objects as separate sections), **Save** at top right submits the whole config; hub-rejected fields show the reason below that item and preserve the draft; **Secrets** only shows set / not set per item, opening a separate input form (a saved value is never echoed; while typing, the eye button can show it for a moment; on iOS the field goes back to hidden and empty as soon as it gives up the keyboard),
  with clear available for set values; **Provides**, **It Can**, **About** (version, previous version, source, installed from, publisher, homepage, warnings). Ones from manager.json are read-only, without switches or forms.
- Contributed connectors (#3178, iOS / Mac #3228): when contributing a connector with Inbox support (`contributes.connectors`, connector has `inbox`), web, Android and iOS / iPad / Mac details show a section per connector after **Pages**:
  its **Show in Inbox** switch (the hub connector switch `PUT /api/v1/connectors/{id}/settings {"inbox"}`, shared with **Settings › Connectors › …**, not stored separately) and a **Connector Settings** row (opens the connector page, refetches on return).
  Hidden without error when the extension is disabled, connector lists cannot be read, the hub lacks `connectors`, or the connector has no `inbox` field (old hub). Settings for which items enter the Inbox (e.g. GitHub repositories/types) remain the extension's own `configuration`, rendered as a form.
  Mock hub: `MOCK_EXTENSIONS=1 MOCK_GITHUB_EXTENSION=1` (`=old` is an old hub without the switch); UI test `ExtensionConnectorInboxTests`.
- Pages (#1328, "Declarative UI"): details lists each extension `plugin_page` as a row (**Pages** section), opening a full page; launcher details add one section per
  `launcher_section` after quota and availability. `ExtensionViewRenderer` (`ExtensionViewRenderer.swift`) draws eight item types with the design system: dividers split a section into two groups (title on the first, footer on the last),
  `tone` maps to `Theme.positive` / `caution` / `danger` (neutral: secondary color, accent for progress), five `field` kinds (`secret` uses SecureField,
  never prefilled), single-line buttons; `confirm` asks first, `submit` includes all page fields; a returned `view` replaces the current one, `toast` uses app feedback, `open_url` and `link`
  open in the app's link card (Mac side panel), https only; errors have empty states and retry. A launcher-section response of 404 `{"code":"unsupported"}`
  (without `hosts`, it is listed on every launcher, #1339) hides the whole section without an error. Logic in `ExtensionUIModel.swift` (`ExtensionViewModel`, `ExtensionLauncherModel`).
- UI slots (#1517, "UI slots"): query and draw only when hub `features` includes `extension_ui_slots`. `ExtensionSlots` (`ExtensionSlots.swift`) fetches
  `nav_page` and `overview_card` together without `placement`, updating badges in place on `extension_ui`. iPhone / iPad: cards first below the Settings-home header (compact, tapping pushes `opens`),
  then an **Extensions** section with one row per `nav_page` (`icon` maps to `Theme.Icon.plugin…`, unknown icons use the extension icon; numeric badges use `CountBadge`, text `Chip`),
  summing badges on the Settings tab and the new shell drawer's Settings button (a dot for text-only badges). Mac: `nav_page` in the left section column before Settings (`MacShellState.pluginPage`),
  cards above the **Settings › Extensions** list. Machine details add each `machine_section` after the machine's own sections (`ExtensionMachineModel`, using the machine's hub group),
  hiding the section on hub 404. Every draw/action includes `context` (`ExtensionUIContext.here`: `tz`, `locale`, `platform`, `ui_version: 3`, `size`, `launcher` / `machine` / `item`).
  New items: `chart` uses Swift Charts (`DataChart`: side-by-side / stacked bars, lines, data colors when series lack `tone`; press or hover a column to show each series' `display`,
  no axes in compact, only endpoint labels, thinning labels when numerous); `stat_grid` (`ViewThatFits`: all in one row if possible, otherwise 3 or 2 per row); `segmented`
  (`SegmentedChoice`, immediately sends `values: {name: value}` on selection); unknown items skipped, unknown `tone` treated as `neutral`. `refresh_after` counts only with the page and
  app foregrounded (`.pluginRefresh`, at least 30 seconds), immediately redrawing on return if overdue, and preserving fields during auto redraw. Cards and machine sections do not render link panels inside list rows:
  links go through the page (Mac's side panel wraps the whole page; putting it in a row compresses that row to just its title).
- UI version 3 (#1860, "UI version 3"): same renderer (`ExtensionViewRenderer` for list pages, `ExtensionViewStack` for cards, machine sections and assistant cards),
  components in `ExtensionComponents.swift`.
  `markdown` uses the chat's Markdown theme (`ExtensionMarkdown`: never loads images, https-only links opened in-app); `table` keeps cells single-line and scrolls horizontally when wide (`ExtensionTable`);
  `empty` uses the design-system `EmptyStateView` (button calls its action); `loading` is a small spinner and sentence. Rows may include an icon (set mapped by `ExtensionUIStyle.icon`, also for `nav_page` entries and empty states),
  avatar (https image; absent or failed images become a 1–2-character `Monogram`), or thumbnail (square / wide); images load through a separate session without any cookies or credentials (`ExtensionRemoteImage`).
  Components with `key` are identified by it (positions do not jump on a new page). Search is full-page navigation-bar search (`.extensionSearch`, sends `values: {q}` after 0.3 seconds, shows only the last search response);
  filters are `FilterChip`s below search (send immediately, with current `q`); section `more` is a final **Load more** row, triggered once on scrolling into view (not again during `loadingMore`), also tappable.
  `push` does not ask the extension: Settings and Inbox push a page by value through `SplitNavigation` paths (`splitPush`, target `ExtensionPushTarget` registered alongside other pages with `extensionPushDestination()`),
  elsewhere `navigationDestination(item:)`; pushed pages are the same `ExtensionPageView`, first draw with `push.state`, at most 8 levels; returning redraws the underlying page.
  Response `pop` closes this page. `.extensionPushes(…)` goes on pages (list rows cannot push): extension pages, Settings-home cards, launcher pages, machine pages, Inbox-item pages.
  `extension_ui {invalidate}` (`ServerEvent.extensionInvalidate`) marks models of that extension (that UI) in the same hub group `stale` through `ExtensionUIRegistry`: visible pages redraw immediately,
  off-screen ones on appearance (same `.pluginRefresh` timer as `refresh_after`), preserving fields. Task keys use the invalidation version; marks are handled only after requests end,
  so refresh does not cancel itself. Events during an operation are retried afterwards; canceled refreshes do not consume events, and new events arriving during a request remain pending.
- Assistant cards (#1860, "Assistant cards"): both ```` ```persona-view ```` and ```` ```persona-card ```` are in `PersonaCard.swift` (`AssistantCardBlock`):
  `persona-card` converts to components under `sdk/go/wire/personacard.go` rules (`ExtensionPersonaCard.view()`, number formatting like `Number`; `PersonaCardTests` compares every field of every case in
  `schema/extension/examples/persona-card/`), `persona-view` is a View or one component; both draw in a card with `ExtensionViewStack`,
  with no model, so rows, buttons, fields, search, filters and more are inactive; links still work. Undrawable content remains a code block.
- Screenshots: `example-gateway` in the `MOCK_EXTENSIONS=1` mock hub has a "Notes (components v3)" page (`-openExtension example-gateway -openExtensionPage notes`),
  `GET /mock/invalidate` syncs one note and sends `ui.invalidate`; UI test `ExtensionV3Tests` (list, filters, more, search to empty state, push note page, edit/save/pop, invalidate),
  unit tests `ExtensionUIv3Tests`, `PersonaCardTests`. `MOCK_EXTENSION_CARDS=1` with `ExtensionAssistantCardTests` checks in a real assistant chat that
  `persona-view` and old `persona-card` share the renderer, actions are not tappable, links remain, and saves light/dark screenshots. Four-client showcase comparison depends on #1852, not replaceable by notes screenshots.
- Inbox-item body (#1682, `inbox_item`, see "Inbox items" above): when the hub has `extension_ui_slots` (also query immediately after app startup before hello arrives: an item opened at launch must not be permanently assigned the original body because of that;
  hubs not knowing this slot simply return an empty list), query each hub once via `GET /api/v1/extensions/-/ui?placement=inbox_item` (again on pairing / hub switch or opening Inbox),
  recording connectors with extension rendering (`InboxItemSlots`, `connector` matched to item `source`). Opening such items calls `POST …/-/ui/{ui}` with the item id in `context.item`,
  rendering a returned View below the detail header with `ExtensionViewRenderer`, replacing the conversation / body (like `plugin_page`, pull-to-refresh and `refresh_after`); redraw on item changes (new messages, done / moved back).
  Header unchanged: iPhone action bar below navigation (✦ summary, **AI Draft Reply** / **Hand to AI**) and Done at top right, Mac right column. Sources absent from the list immediately use the original body; before the first query, show a spinner.
  403 / 404 / `unsupported` quietly use the original body; extension errors (502), hub connection failures or no View also use it, with a "<extension name> details could not be shown: <reason>" line and **Retry**.
  An action returning `open_url: persona://inbox/<this item>/source` (view in chat, `InboxSourceLink`; only this drawn item allowed) switches to the original body (Lark chat positioned at the event's first message),
  with "Back to <page title>" returning to the extension page (same `ExtensionViewModel`, preserving state and entered text); other `open_url`s and links follow extension-page rules through page `openURL`
  (Lark docs in the reader, Mac adjacent panel). Code `Persona/Views/Inbox/InboxItemSlot.swift` (`InboxItemSlots`, `InboxItemSlotModel`, `InboxExtensionItemPage`),
  unit test `InboxItemSlotTests`; screenshots: add `MOCK_INBOX_ITEM_UI=1` to mock (extension `example-chat`), `-tab mail -openInboxItem lark:f1:100`
  (`lark:f3:300` is the group chat / extension-error item).
- Extension marketplace (#1328, "Extension marketplace"): when `GET /api/v1/extensions` returns `markets: true`, the list starts with **Extension Market**. The catalog has search (waits 0.3 seconds before querying),
  marketplace filter chips (`FilterChip`, only with more than one marketplace), extension names, versions, descriptions, tags and status (available / upgradable / installed / reason unavailable),
  with unreadable-marketplace reasons above; details list permissions. **Install** / **Update to …** first call `catalog/preview`, opening a confirmation page with permissions (upgrades separately list "New permissions" first),
  then install / upgrade with `permissions_digest`; **Uninstall** available only for `from_this_market` (confirm first). Top-right **Markets** opens marketplace management: address, count,
  fetch time, errors, warnings, with **Refresh**, **Set / Change Token**, **Delete** per row; **Add** takes an address and optional token (write-only, no echo), passed unchanged to the hub without client-side format validation,
  retaining hub rejection reasons in the form (#1373: https, `github:owner/repo`, `git:<address>[#branch]`; git uses the hub machine's SSH credentials, no token).
  Logic in `ExtensionMarketModel.swift` (`ExtensionMarketModel`, `MarketExtensionModel`), pages in `ExtensionMarketViews.swift`.
- Auto-upgrade (#1699, "Auto-upgrade"): details for marketplace-installed extensions (with `ExtensionView.auto_upgrade`) add an **Update** section below **Enabled**:
  - **Auto-Update** switch (`PUT …/{id}/-/auto-upgrade`). With `auto_upgrade_paused` (the extension is turned off) it shows off and greyed out, and the footer says auto-update is paused until the extension is on again,
    when the extension's own setting comes back (web, iOS / Mac / Vision and Android alike; mock hub `MOCK_PLUGIN_UPGRADE=disabled`).
  - With a new version, "New version 1.1.0" plus small status text: auto-update when idle / auto-update off / needs one more permission: … (orange) / last version failed to start, no further auto-update (orange) / you rolled back from this version, no auto-update to it (orange, `skipped_by: "user"`) / failed to install / incompatible.
    Actionable rows end with **Review** or **Update**, opening the marketplace upgrade page with permissions and confirmation.
  - Without a new version, show the last result: "Auto-updated to 1.0.0 (was 0.9.0) · 3 hours ago", or "1.1.0 failed to start, reverted to 1.0.0".

  Marketplace management starts with a master **Auto-Update Extensions** switch (`GET/PUT /api/v1/extensions/-/upgrades`), hidden on old hubs without the endpoint and when there are no marketplaces.
  Extensions awaiting consent append "· New version needs approval" to their status. Mock hub `MOCK_PLUGIN_UPGRADE` (`needs_permission` (default) / `waiting_idle` / `skipped` / `rolled_back_by_user` / `upgraded`)
  determines `example-gateway`'s state; UI tests `ExtensionMarketTests.testAutoUpgrade`, `testAutoUpgradeForAll`, unit test `ExtensionUpgradeTests`.
- Screenshots: the `MOCK_EXTENSIONS=1` mock hub (like a new hub: reports `extensions` and `extension_*`, returns new keys on `/api/v1/extensions/…`; add `MOCK_EXTENSIONS_LEGACY=1` for a pre-rename hub) has five sample extensions with different states, plus `example-gateway` pages and a launcher section (and one always returning unsupported),
  two marketplaces and their catalogs; `-tab settings -openExtensions YES` (`-openExtension <id>` opens details directly, add `-openExtensionPage <ui>` for its page and `-openExtensionManager <id>` for another manager's copy; old `-openPlugin…` forms still work),
  `-openExtensionMarket YES` (catalog), `-openExtensionMarkets YES` (marketplace management), `-openMarketExtension <marketplace id>/<extension id>`, `-openLauncherPage <launcher>`.
  UI slots: `-tab settings` shows cards and entries; `-openExtension example-gateway -openExtensionPage usage` opens usage; `-openMachines YES -openMachineDetail macbook`
  shows the machine section. Mac uses `scripts/app/mac_shot.sh` (`DEMO=0` plus `-pairURL`); `-openMacExtensionPage example-gateway/usage` opens that sidebar page.
  UI tests `ExtensionSettingsTests`, `ExtensionMarketTests` (marketplace installation, upgrade preview, marketplace management, saving extension pages, launcher section; `TEST_RUNNER_PERSONA_EXTENSIONS_HUB`),
  `ExtensionSlotsTests` (cards, entries/badges, usage-page segmented controls and chart, opening cards, machine sections),
  unit tests `ExtensionsModelTests`, `ExtensionMarketModelTests`, `ExtensionViewModelTests`, `ExtensionSlotsModelTests`.
- Web (#1207): **Settings › Extensions**, same lists and details (`web/src/views/settings/Extensions.tsx`, `web/src/lib/extensions.ts`, `/settings?tab=extensions&extension=<id>`; old `?tab=plugins&plugin=<id>` links still open), see `docs/web/web.md` ("Settings").
  #1330 adds declarative UI (`settings/ExtensionView.tsx`, `lib/extension-ui.ts`: **Pages** in extension details, launcher-detail sections) and the extension marketplace (`settings/ExtensionMarket.tsx`, `lib/extension-market.ts`: catalog, details, marketplace management).
  #1699 adds auto-upgrade ("Auto-upgrade"): an **Upgrades** group in marketplace-extension details (switch, new-version row and **Review**, last result, `UpgradeGroup`), master switch at the top of marketplace management, same wording as the app (`upgradeDetail` etc. in `lib/extensions.ts`).
  #1681 adds `inbox_item` (`inbox/InboxExtensionBody.tsx`: Inbox detail body; list from `lib/extension-slots.ts` cache), see "UI slots" under "Settings" in `docs/web/web.md`.
- Android (#1206) has the same features, entered through **Extensions** on the Settings home; code and screenshots in [android.md](../android/android.md) ("Extensions (Settings › Extensions)"). Extension-rendered UI (extension pages, launcher sections) and the marketplace (#1329) are covered by "Extension-rendered UI and the extension marketplace" in that section. Auto-upgrade, per-machine switches and detail-page uninstall (**Keep data** checked by default) are in "Auto-upgrade, per-machine switches and uninstall" (#3049).

## Admin UI: Extensions

The hub admin UI ([admin.md](../operations/admin.md)) **Extensions** page (`web/src/admin/pages/Extensions.tsx`) handles installation; settings and secrets are still filled in the app:

- List: name, version, source (built-in / `hub.json`, i.e. manager.json / installed), status (same wording as app), description, "Missing: …" and errors; expand for **Now** (running state), **It can**, contributions, previous version, installed from, publisher, homepage, warnings.
  Installed extensions have **Upgrade**, **Rollback** (with a previous version, non-remote), **Uninstall**; all except `hub.json` sources (manager.json) have switches.
- Install: choose a source (hub-machine path, https link with sha256, file upload, socket running elsewhere); the page checks syntax first, then **See what it can do**:
  only after a preview is returned does it show name, version, publisher, description, every **It can** item, contributions, warnings and minutes left on the preview; **only Agree and install submits the preview's `permissions_digest` to install**.
- Upgrade: preview first likewise (`old → new`); extra permissions show a red "The new version requires more permissions" notice at the top and a separate **Added** list, changing the button to red **Agree to new permissions and upgrade**; without consent, no upgrade. No extra permissions means a normal **Upgrade**.
- Rollback and uninstall require dialog confirmation; uninstall offers **Keep its data** (`~/.persona/extensions/data/<id>/`).
- Runtime status (#1857, `web/src/admin/pages/ExtensionRunning.tsx`, see "Runtime status" above): each row has **Error rate** and **p95** columns before buttons (last 24 hours; error rate orange from 1%, red from 10%, no color below 5 calls),
  expanded **Runtime status** offers 1 hour / 24 hours / 7 days: four metrics for calls, error rate, p95 latency (with p50 / p99), starts / error exits; two charts for calls/errors (stacked bars) and p95 latency,
  a method table (calls, error rate, p95, error codes), error-exit reasons; **Logs** supports search (queries after a 0.3-second pause), live following (server-sent events, scrolling only while the reader is at the bottom), and copy.
  Old hubs without these endpoints (404) hide the whole block. Screenshots as above; `?dialog=details` expands the rollback-capable row (mock data in `web/src/admin/mock-metrics.ts`).
- Extension marketplace (#1330): above the list (#5017), register marketplaces (add, remove, reread, change token) and browse their catalog (search, filter by marketplace); catalog install / upgrade previews first, then uses the flows above after consent. See [admin.md](../operations/admin.md) ("Extensions").
- Screenshots: `cd web && npx vite`, `/admin/extensions?mock=app&theme=dark`, `?dialog=install|upload|preview|upgrade|uninstall|details|market|market-add` (`web/src/admin/mock.ts`).

Verification image removed; see the associated PR.

Verification image removed; see the associated PR.

## Process extension protocol (extension protocol 1)

Generalized from connector protocol v2: the same `internal/rpc`, one JSON per line, requests `{"id","method","params"}`, responses `{"id","result"}` / `{"id","error":{"message","code"}}`,
events `{"event","params"}`. Hub-started extensions use stdin / stdout, remote ones unix sockets. stderr is for logs, **never secrets, message bodies or numbers**. All fields snake_case.

**Handshake**: the hub sends `initialize`:

```json
{"protocol": 1,
 "hub": {"build": 202611050000, "extension_points": {"computer": 1, "connectors": 2, "voice": 1}, "role": "center", "timezone": "Asia/Shanghai", "locale": "en",
         "capabilities": ["inbox_group"]},
 "extension": {"id": "example-notes", "version": "1.2.0", "dir": "…", "data_dir": "…"},
 "config": {"base_url": "https://notes.example.com", "folders": ["work"], "include_shared": false},
 "secrets": {"api_key": "…"},
 "granted": {"network": ["notes.example.com"]},
 "active": {"connectors": ["examplenotes"]}}
```

`active` lists contributions used in this run, one entry per extension point (disabled connectors and individually disabled contributions omitted; see "Contribution switches"); extensions exporting points also get `contributions_to_me` (see "Dependencies, exports and bundles"). `hub.role` (#1503) is this hub's role in its linked-hub group: `center` (alone or primary hub: serving apps, driving computers), or
`member` (another hub is primary: group work such as reacting to chat-service events, running /agent and full crawling is handed to the primary). Role changes restart extensions contributing `connectors` or `api`.
`hub.timezone` is the user's time zone (IANA, as reported by devices), used for displayed times ("today", message timestamps); empty when only the hub's own clock is known, read at each start.
`hub.locale` is the user's language (`zh-Hans` / `en` / `ja`, `docs/protocol/i18n.md` ("Protocol")); extensions use it for human-facing text (status, sign-in prompts, bot replies), read at each start, defaulting to `zh-Hans` if absent.
`hub.capabilities` (#1680) lists what this hub can do so extensions can adapt: `inbox_group` (associate batches of new connector messages with Inbox events, `docs/assistant/inbox.md` ("Event grouping")).
Missing field or entry means the hub does not do it.
`hub.manager` is the id of the manager running the extension when it is not the built-in one (also `PERSONA_MANAGER` for a program it starts; `docs/assistant/managers.md`, "Extensions and the hub's API"); absent for the built-in manager.
The program returns `{"protocol": 1, "id", "version", "contributes"?}`: protocol mismatch returns `wrong_protocol`
(or the hub stops it on detecting a mismatch); `id` and `version` must match the manifest; `contributes` is a manifest subset, and discrepancies are logged with the manifest taking precedence.

Base methods: `initialize`, `ping` (after `initialize`, returns `{}`), `manifest` (callable before `initialize`, returns `persona-extension.json`; required remotely, optional for programs started by the hub from packages).
Other methods before `initialize` return `not_initialized`; both SDKs wait for initialization to finish before answering calls sent immediately after it without awaiting its response. The program exits when the hub closes stdin (or the socket).

**Namespaces by extension point**: other methods are `<namespace>.<method>`, with object parameters including `contribution` (contribution id; may be omitted when there is only one).
Program-to-hub calls and events follow the same pattern (`connector.secrets.load`, event `connector.changed`).

**Error codes**: `not_initialized`, `unknown_method`, `wrong_protocol`, `no_capability`, `no_contribution` (missing contribution), `permission_denied`, `timeout` (generated locally by the hub).
Other errors have only `message`, a human-readable sentence without secrets or message bodies. Changing field semantics or removing fields increments the protocol version; adding optional fields does not.

Use the official SDK for extensions ("Writing extensions: SDK, templates and command line" below); hub built-in connector programs use `connector/serve.MainWith`, accepting both handshakes.

## Programs running on computers (`node` section, #1830)

The hub stores only desired state: if an extension is enabled, configured, selects this computer in `node.machines`, and has a program for its OS, it should run there (`extmgr/extension_nodes.go`),
and the node follows (`node/plugins.go`). Reconciliation runs on computer connection and extension install / switch / config change / upgrade / uninstall, as full reconciliation without queuing; offline computers reconcile when reconnecting.

- **Package**: the hub makes a tar.gz for the computer's platform (`extension.NodePackage`: excludes other-platform and hub programs, fixed order, no timestamps or owners, byte-identical for the same package),
  using sha256 as digest; transfer only if the computer lacks it (`plugin.node.have`), in chunks (`plugin.node.put.begin/chunk/commit`, 1 MB each), then the node verifies sha256, extracts and checks as at install.
  Stored in that computer's `~/.persona/node-plugins/<id>/<version>/`, program data in `~/.persona/node-plugins/<id>/data/`, retained across versions.
  **Not `~/.persona/extensions/`**: on the hub computer, node and hub share `~/.persona`; `plugins/` is the hub's extension-install location (`installed/`, `data/`, `staging/`…).
  Every node-created extension directory has `.persona-node`; reconciliation deletes only marked directories. #1921's node used `plugins/` and deleted the hub's `installed/` during reconciliation as if it were an uninstalled extension (#1933).
- **Start**: `plugin.node.start`, with config and secrets from `node.secrets` (only in `initialize`, never written to that computer's disk), **without the hub token** (computer programs have no `hub_api`).
  Env vars `PERSONA_EXTENSION_ID`, `PERSONA_EXTENSION_DIR`, `PERSONA_EXTENSION_DATA` (also `PERSONA_EXTENSION_SOCKET` with `http`), clean environment (like installed hub extensions).
  If identical to the running program (version, config, secrets), leave it alone; stop the old version before changing versions; if the new version fails to start within 30 seconds, stop it, restart the previous version and report the error to the hub.
- **Reconcile**: `plugin.node.sync` carries all desired `{id, version, digest}` and ids of hub-installed extensions: stop unwanted programs and delete their packages; keep current and previous versions for wanted programs (rollback);
  delete data too when uninstalled on the hub, preserve data when merely disabled on this computer.
- **Lease**: the hub renews every 10 minutes (`plugin.node.renew`, 30 minutes); after 30 minutes without renewal, the node stops `keep_running: "lease"` programs (`lease_expired`),
  restarting when the hub returns; `always` programs remain. All programs exit with the node process (`always` too; after restart, wait for hub connection because secrets live on the hub).
- **HTTP**: `node.http` programs serve HTTP on `PERSONA_EXTENSION_SOCKET`; the app accesses `/api/v1/extensions/{id}/-/machines/{machine id}/{path…}` through the hub (WebSocket too):
  each request opens a stream (`plugin.node.dial`, same `internal/streams` as port forwarding) to the program's socket on that computer, forwarding `/{path…}` without `Authorization` or `token`,
  with caller identity ("The hub's API", "The caller on forwarded requests");
  full-access devices only, only on extension-enabled machines; missing extension or not enabled there 404, offline machine 409, program not running / not responding 502.
- **Calls and events**: the hub-side program calls `nodes.call {machine, method, params}`, forwarded to that computer program's extension-protocol method (enabled computers only);
  computer-program events are passed to the hub program as `node.event {machine_id, event, params}`. Computer programs calling hub methods always get `unknown_method`.
- **In the app**: extension details include `node_machines` (computer-selector field name); enabled rows show the program's state there ("Program running · version", "Program error: reason", "Offline: starts on connection", "personad on this computer is too old…").
  For program errors, Go `exit status N` displays as "Exit code N", `signal: X` as "Terminated by signal (X)" (converted by app/web, including reports from old nodes);
  disabled rows still show just online / offline. Enabling a computer asks first (the hub adds field `confirm`: "Runs the ‘…’ program on this computer…", unless supplied by the manifest).
  Installation permission rows choose icons by key (`node_runs_code`, `node_network`, `node_files`, `node_macos_privacy`, `node_secrets`).
- **Status**: every extension with a `node` section includes `nodes: [{machine_id, version, state, error?}]` in the extension list, where `state` is `running` / `starting` / `error` /
  `missing` (this computer lacks this package version) / `lease_expired` / `offline` / `outdated` (personad too old to recognize `plugin.node.*`).
- **Built-in node programs (cua, ego, #1830 step 5)**: computer/browser control node code lives in personad, not packages or subprocesses (`node/builtin.go`).
  The node reports `["cua", "ego"]` in `node.info`'s `builtin_programs`; the hub then calls via `plugin.node.call {id, method, params}` (`status`, `call`, `install`, `grant`, `cancel`,
  `clipboard`, `cursor`, `nolock`, `lock`; ego `status`, `install`, `open`, `cancel`, `call`, `cookies.list/import/clear`). Old nodes not reporting them use `computer.*` / `browser.*`
  (`callBuiltin` in `extmgr/builtin_programs.go` chooses from node declarations, not error/retry). New nodes answer both names using the same code, so old hubs are unaffected.
  Built-ins are outside reconciliation: no package transfer or `plugin.node.start` (node rejects it too), no directories in `node-plugins/`; UI remains the existing Cua / ego state on machine/settings pages,
  without `node_machines`, pre-enable confirmation or node permission rows.
  **imessage** (step 6) is built-in too: method names remain connector protocol `connector.list/chats/older/changes/search`, through `plugin.node.call {id: "imessage", method: "connector.chats", …}`;
  old nodes still receive direct `connector.*`. Built-in **Messages (iMessage)** settings have one Mac selector `machines` (`x-os: darwin`, `maxItems: 1`):
  enabling a second Mac while one is already enabled returns hub 400 `invalid_config`, `field: machines`, "Only one Mac can be read; turn off ‘<that one>’ first". The `imessage` connector stays the same,
  with its computer selected here: use it if the extension is enabled and one is selected, otherwise manager.json `imessage.machine`; with neither, no connector (`extmgr/imessage_extension.go`).
  The extension then reports `needs_setup`, not running. Selecting another Mac replaces the old selection in one save. Its **Connector Settings** link opens Messages' own access/status and Hand to Persona settings, not a second generic Inbox switch.
  Reading `chat.db` needs **Full Disk Access** on the chosen Mac for the process that runs the node. Grant it to the SSH-server process for an SSH-launched node, or to personad for a launchd-installed node; reconnect/restart the node after granting. The connector page reports the resulting access error instead of treating a configured extension as proof of permission.
  manager.json values migrate once at hub startup: name becomes machine id, extension enabled, marker recorded (`imessage.migrated_to_extension`); later user disable/switch choices are not overwritten.
  Unknown machine names or failed writes produce one log line and retry next startup, reading manager.json meanwhile. The manager.json field remains unchanged for one version.
  All three built-in manifests specify `node: {command: "builtin", machines: "machines"}` (`NodeBuiltin`). Installed packages with `builtin` are rejected; only hub built-ins may use it.
  Their `Manifest.NodeProgram()` is nil: no packages, no `plugin.node.sync`, no `nodes` / `node_machines`, no added "Runs on this computer…" confirmation,
  no node permission rows, and the consent digest is the same as without a node section. UI is therefore unchanged. Anything related to packages, status, confirmation or consent uses `NodeProgram()`, not `Node` directly.
  On-connection reconciliation (`reconcileNodePrograms`, `extmgr/builtin_programs.go`): first each built-in's own queries (cua status, no-auto-lock and cursor, ego status,
  `builtinConnected`; runs even with the extension disabled, each knows what to report then), then installed-package sync; each runs in its own goroutine without waiting for others, as before.

## Schema and types for all four clients

Manifests, process protocol, Views and components have one definition: JSON Schema in `schema/extension/` (#1849). Generate types from it, never hand-write them:
Go `sdk/go/wire` (used directly by the hub, `server/go.mod` uses `replace` to `../sdk/go`), TypeScript `sdk/ts/src/wire.ts` and web `web/src/lib/extension-wire.ts`,
Swift `apple/Shared/Generated/ExtensionWire.swift`, Kotlin `android/core/contracts/.../generated/ExtensionWire.kt`.

- After schema changes, regenerate with `python3 scripts/gen/extension_schema.py`; `--check` compares only (`scripts/test/test_extension_schema.py` runs it and checks examples/counterexamples under `examples/`).
- Hub validation of extension Views and conformance checks of `initialize` responses first use the schema (`wire.Validate`), then checks the schema cannot express (`extension.CheckView`).
- Adding a component/field: schema first, then examples, generation and rendering on each client. Supported schema subset, each language's representation and steps for adding a component: [schema/extension/README.md](../../schema/extension/README.md).

## Conformance tests

`extension/conformance.Check(t, manifest, run, init)` checks a program at the other end of the protocol: before `initialize`, `ping` returns `not_initialized`; another protocol version returns
`wrong_protocol`; `initialize` id/version match the manifest; `contributes` is a manifest subset; response keys are snake_case; unknown methods return `unknown_method`;
unknown contributions return `no_contribution`; every `ui` View is drawable in its slot ("UI slots", "Compatibility"); exit within 5 seconds after stdin closes. Any language can use `conformance.Program(t, path, args…)`; Go can use `conformance.Serve`.
Connector `connector/conformance.Check` tests both handshakes for every connector (direct / protocol, identical results).

Complete external-program examples are connector extensions in the marketplace (such as personal WeChat connectors): outside this repo, delivered as extension packages (`.tar.gz` plus sha256),
installed from an extension marketplace (or with `file:` or an admin upload), speaking extension protocol 1 plus connector protocol v2 to the hub. Their guides are published in their own repositories.
This repo uses the neutral `examplechat` connector fixture to represent such programs (`schema/extension/examples/Manifest.examplechat.json`);
per-capability connector checks (`CheckProgram`, synthetic production-style fixtures): [connector-protocol.md](../protocol/connector-protocol.md) ("Conformance tests").

## Writing extensions: SDK, templates and command line

Extension packages contain only platform executables: the hub does not bundle Node or depend on any machine-installed runtime (#1853).
Networking: use a standard HTTP client honoring `HTTP(S)_PROXY`; for hub-machine local services from the sandbox, use `host.persona.internal:<port>`, not `127.0.0.1` / `localhost` (HTTP libraries bypass proxies for loopback; see "Egress").
The official SDK is in the main repo's `sdk/`, **self-contained with zero dependencies**;
extension repos copy a versioned snapshot (the main repo is private, and their CI should not need its token just to fetch the SDK). Generated protocol and View types come from `schema/extension/`.

### Go: `sdk/go`

Module `github.com/nwverse/persona/sdk/go`, standard library only. The hub uses it too (`server/go.mod` replaces with the same code, Docker builds copy `sdk/go/`),
and built-in programs use the thin forwarding layer `server/internal/extension/serve` (converts to hub `InitializeParams` and `rpc.Error`).

| Package | Contents |
| --- | --- |
| `wire` | Generated types: manifest, protocol frames/error codes, `initialize`, `UIParams`, View/components, `ActionResult`… (do not edit manually) |
| `ext` | Runtime: `ext.Main(ext.Program{…})` speaks the protocol over stdin/stdout (`initialize`, `ping`, `manifest`, `not_initialized` / `wrong_protocol` / `unknown_method` / `no_contribution`), dispatches calls by namespace to `Namespaces`; `UI` routes `ui.render` / `ui.action` by contribution id (empty action result redraws); `HTTP` serves on the hub-supplied socket (`/health` answered by SDK; when the hub closes stdin or sends SIGTERM, `Main` stops accepting connections and waits up to 5 seconds for the requests in flight before it returns); `Hub.Call` / `Notify` / `Badge` / `Invalidate`; `Env()` accepts three sets of env names; `Init.Info()` accepts `extension` and legacy `plugin`; errors use `*ext.Error{Code, Message}`; system placements ("System-level placements"): widgets are `Page`s with `placement: widget`, identified by `UIRequest.Widget()` / `Family()`; `Shortcuts` routes `shortcut.run` by contribution id (`ShortcutRequest` has `Values`, returns `wire.ShortcutResult{Text, View}`) |
| `exttest` | Conformance checks in the extension's own `go test`: `exttest.Check(t, manifest, exttest.Serve(t, program))` (or `exttest.Program(t, path)` for a built program): handshake, error codes, snake_case, every `ui` contribution rendered in its slot (widgets once per `families` entry) with View checks (`schema/extension`, plus https, item/character counts and card limits), every shortcut called once through `shortcut.run` (empty values; may reject with text, not `unknown_method` / `no_contribution`), exit after stdin closes. `personad extension test` uses the hub's stricter checks (same as install/render validation) |

```go
func main() {
	ext.Main(ext.Program{ID: "example", Version: "1.0.0", Manifest: manifest,
		UI: map[string]ext.Page{"home": {Render: home, Action: act}}})
}
```

### TypeScript: `sdk/ts`

`@persona/extension` (not published to npm; copied into extension repos), for Bun: `main({id, version, manifest, init, namespaces, ui, fetch})` maps one-to-one to Go's `ext.Main`
(`connect(prog, write)` is the same without stdin, for tests); `Hub`'s `call` / `notify` / `badge` / `invalidate`, `env()`, `decodeState` / `encodeState`, `ExtError`;
`shortcuts` (`shortcut.run`), `isWidget()`; `wire.ts` contains generated types. Calls arriving immediately after `initialize`, without awaiting its response, wait for it to finish.

**Packaging**: `bun build --compile --minify --target=bun-<linux|darwin>-<x64|arm64> src/main.ts --outfile bin/<id>-<platform>`; all four platforms can cross-compile on one Linux machine,
60–80 MB per file (includes Bun). The package contains only this executable; neither hub nor computer needs Node / Bun.
When building `darwin-arm64` on Apple silicon Macs, `personad extension` checks signing after compilation (`codesign -v`): macOS immediately terminates unsigned arm64 programs.
If unsigned, add an ad-hoc signature; if that fails, error with the Bun version and ask the user to `bun upgrade` and retry (#2555: npm-wrapped Bun 1.3.12 produced unsigned binaries that could not be signed; 1.4.3 works).

### Templates

`sdk/templates/go`, `sdk/templates/ts`: runnable extensions with a main-navigation page (search box, icon list, row tap pushes details (`markdown`), `empty` for no results),
an Overview card (`stat_grid`), contribution ids `<id>` and `<id>-today` (ids are hub-wide unique within an extension point, so `new` ids are at most 26 characters), one setting (greeting), one secret (API key, only set/unset shown), tests (Go: `exttest` conformance + search/open; TS: protocol round trip).
Rendering for apps older than UI version 3 is included too (search replaced with an input plus button, details with **Back**), as an example of supporting old apps.

**push / pop and old apps**: both SDKs handle compatibility for apps older than UI version 3 (they do not know `push` / `pop`, and tap calls `ui.action` with the action `id`):
when sending a View to an old app, actions with `push` become SDK-owned ids (`ext.push.<n>`, latest 512 kept in memory); on tap, the SDK draws the pushed page directly, replacing the current page;
a `ui.action` response with `push` draws that page; `pop` draws this page's initial state (empty `state`). Extensions write `push` / `pop` only once (`ext.Open` / `open()`).

### Command line

| Command | What it does |
| --- | --- |
| `new <id> [--template go\|ts] [--name <name>] [--dir <dir>]` | Creates an extension from a template (templates/SDK embedded in personad: `sdk.Go`, `sdk.Bundle` in `sdk/go`), copying SDK into `<dir>/sdk/go` or `sdk/ts`, with Go `go.mod` replacing to it; refuses a nonempty directory. No hub needed |
| `test [<dir>]` | Builds for this machine, checks manifest/package as at install (program existence, config schema, Inbox triage rules), then starts the program for hub conformance checks (`extension/conformance`, same as hub View validation). No hub needed |
| `pack [<dir>] [--platforms …] [--out <dir>] [--split]` | Builds every manifest platform (Go: `CGO_ENABLED=0 go build -buildvcs=false -trimpath`; TS: `bun build --compile`), creates `<id>-<version>.tar.gz` (`--split` one per platform) containing only manifest (without `tags`, written both as `persona-extension.json` and `persona-plugin.json` for pre-rename hubs), platform programs, `skills/`, `assistant/`, `assets/`, README, LICENSE, no source/SDK; prints sha256 and marketplace-index `packages` ready to paste. `--platforms` is comma-separated, defaults to platforms with programs in the manifest (this machine if absent); output to `--out`, default extension `dist` directory |
| `dev [<dir>]` | Builds and attaches to this machine's hub (below), checks files every second, rebuilds and asks the hub to restart on changes (reattaches for manifest changes); prints extension stderr and each hub call (method, duration, error) in the terminal; Ctrl-C removes it |

**dev**: hub machine only, master token over loopback (like install). The hub treats it as a `dev`-source extension: runs directly from that directory (no copy/database, gone after hub restart), enabled,
with all declared permissions (owner's own code on their machine), the same clean environment as installed extensions but no sandbox (like official marketplace extensions; see "Sandbox"). Config/secrets come from the directory's `extension.dev.json` (`{"config": {…}, "secrets": {…}}`, never commit),
kept only in hub memory; visible in the app (source `dev`), but switches/config/secrets/uninstall are changed through that file and `dev`. Its id cannot duplicate an extension from another source.
Endpoints: `POST /api/v1/extensions/-/dev {dir (absolute path), config?, secrets?}` attach/replace (returns details), `POST /api/v1/extensions/{id}/-/dev/restart`,
`DELETE /api/v1/extensions/{id}/-/dev`, `GET /api/v1/extensions/{id}/-/dev/logs` (line-by-line text stream: program logs, `→ <method> <duration>[: error]`).

### For extension repositories: `scripts/gen/sync-sdk.sh`

`scripts/gen/sync-sdk.sh <extension repo> [go|ts|all]` copies this repo's SDK into `<extension repo>/sdk/go` and `sdk/ts`, recording the source commit in `sdk/VERSION`.
Extension repo `go.mod`: `require github.com/nwverse/persona/sdk/go v0.0.0` + `replace github.com/nwverse/persona/sdk/go => ./sdk/go`.
After TS SDK/template changes, run `python3 scripts/gen/sdk_bundle.py` (`scripts/gen/extension_schema.py` also runs it; `scripts/test/test_sdk_bundle.py` checks it is current).

### Voice extensions: from `/usr/bin/node` to packages

External voice extensions declared in the manager's manager.json with `"command": "/usr/bin/node"`, `"args": ["…/main.mjs"]` depend on machine-installed Node. Migrate to a regular installed extension:

1. `personad extension new voice-x --template ts`; move the old `main.mjs` into `src/` (Bun runs Node-style `http` / `ws` code directly; `Bun.serve`'s `websocket` is simpler for WebSocket servers).
2. Manifest: remove template `ui`, add `"contributes": {"voice": [{"id": "voice-x", "name": "…"}]}`, `"runtime": {"command": {…four platforms…}, "rpc": "none", "http": true}`,
   `"activation_events": ["on_startup"]`; if it needs the hub token, add `"permissions": {"hub_api": true}` (same as manager.json, listed at install).
   Let `main({…, fetch})` handle HTTP (with `rpc: none`, the SDK does not wait on stdin and serves until the hub stops it); socket is `env().socket`.
3. The old entry's `config`: scalars become manifest `configuration` fields, credentials go in `secrets`; structures beyond the form subset (upstream object arrays) go in a `string` field containing JSON, parsed by the program.
   Read `env().config` / `env().secrets` (hub supplies env vars with `rpc: none`).
4. `personad extension pack` → `personad extension install file:dist/voice-x-<version>.tar.gz`; fill settings/secrets in the app, confirm voice works, then delete the manager.json entry and restart the hub.
   The app finds voice by extension `kind` (first extension point `voice`) and `running`; changing id also changes model-config purpose `plugin.<id>`, requiring model selection again.

Built-in `personad voice` ("Built-in voice extension" below) is unaffected: it is personad itself, with no Node dependency.

## Telegram: from built-in extension to extension (#1704)

The Telegram connector was the built-in extension `telegram` (`personad-telegram`, beside personad). Since 2026-11-05 it is extension `telegram`, outside this repo: before 0.1.2 in a private extension repository, from 0.1.3 in `extensions/telegram/` of `nwverse/persona-extensions` (#2057), installed/upgraded from that marketplace. It connects directly over TCP to Telegram data centers and cannot run in the sandbox (official-marketplace installs default to no sandbox; see "Sandbox").
Connector id remains `telegram`, preserving login in `connector_secrets`, Inbox, knowledge base, connector switches/options under the same id; installation requires no new sign-in.
During handoff, the registry lets the built-in yield to an installed extension with the same id (`Registry.Yields`): stop the built-in process before starting the extension, so both never connect to the same Telegram session simultaneously.
Once the built-in is removed, hubs without the extension have no Telegram connector (the app shows only hub-listed connectors); the login row remains in the database for use when installed.

## GitHub: from hub to extension (#2830)

The GitHub connector is moving to extension `github` in official repo `nwverse/persona-extensions`, like Lark (`docs/plans/plan-extensions.md` ("Step 3")): `github` is in `connector.MigratableConnectors`,
so an installed, enabled extension replaces the hub's own connector; disabling, uninstalling or failure to start restores the hub's. Hubs without the extension behave as before. The id stays, preserving login (device-code token in `connector_secrets`),
Inbox, knowledge base `github-*` sessions, switches, options and mutes; uninstalling the extension **does not delete** these (the hub's connector still needs them, `hasOwnConnector`).

- **Login source**: the user's choice remains in hub setting `github_auth` (manager.json `github.auth.from_machine` if unset), passed to the extension after every start via `connector.auth.set`;
  for a computer's gh login, the extension calls `connector.credentials.gh_token` (`gh_login` permission). `GET/PUT /api/v1/connectors/github/auth` stays unchanged: the hub's own connector queries computers and gh sign-ins (even while sidelined),
  the extension answers source choice and token scopes (`connector.auth.get`), no app changes needed.
- **Settings**: app-edited Inbox settings (`options.*`) remain on the hub by id, passed back after every extension start; unedited settings initially use the hub's own copy (including manager.json `github.inbox`).
  manager.json `github.proxy` and `github.auth.host` migrate on first takeover into extension `proxy` and `host` (once only, never overwriting user input, `github.migrated_to_extension`).
- Hub code reading the GitHub account (avatar tiers) should use connector status, not concrete types. Today it reads only the hub's own connector (#6077).
- Still undone: remove the hub's own GitHub connector (separate issue after a period of real-hub validation).

### Validating the takeover on a real hub (#6062)

Before the hub's own GitHub connector is removed, run this on a real hub, against the latest personad and the latest `github` extension. Record both versions. Every case passing means the separate removal issue can start. The known bugs referenced below (#6077, #6107) must be fixed first and their checks run again. Where a case says "record", write down what happened; if it differs from what is expected, file an issue.

The cases run in order, and each starts from the state the one before leaves. Where a case changes something the next ones rely on, it ends with a step putting it back.

**Setup**

- **An isolated hub**, not the one you use every day, with its own `HOME`, database and port.
  - Write its `hub.json` by hand: a `database_url` of a database of its own, a `listen` port of its own, and a `public_url` with that port. Pairing codes carry `public_url`, whose default points at port 18660, the everyday hub's. Don't run `personad hub setup` where the everyday hub's database container is reachable: setup always uses the same container name, and an existing one becomes the new hub's database too.
  - Don't install it as a service beside the everyday hub; run it by hand.
  - Run the isolated hub, and every `personad` command for it (`hub pair`, `extension …`), as a separate user, or in a clean environment: `env -i HOME=<dir> PATH=… PERSONA_HOME=<dir>/.persona`. The state directory comes from `PERSONA_HOME` (or `JARVIS_HOME`) before `HOME`, and `PERSONA_DATABASE_URL` overrides the `database_url` in `hub.json`. The command line reads `hub.json` from the state directory and takes the port from its `listen` (default 18660; `PERSONA_LISTEN` overrides it). With the everyday environment, each `disable`, `uninstall` or `rollback` below would act on the everyday hub.
  - Give it `"machines": []` (or `PERSONA_LOCAL_NODE=0`; the old `JARVIS_LOCAL_NODE` works too). Otherwise it runs a node on its own machine, which drives the herdr found through `HERDR_SOCKET_PATH`, possibly the one you work in. The assistant then runs on the first computer you add, which needs Claude Code and a model for case 4's assistant checks.
- **Clients.** Pair one app with it, and keep the web app open to see what clients see.
- **A computer with `gh auth login` done**, added to the isolated hub, for the computer sign-in source. Use a computer (or a user account on it) the everyday hub does not use: pairing it with another hub takes it away from the everyday one, and a user's computer has only one node. Use `--insecure-storage` where the keychain can't be read over SSH; see the `github` entry in `docs/operations/config.md`.
- **Two GitHub accounts and an organization team.** The second account requests reviews, mentions, assigns and comments, since a user's own actions bring nothing. The team, which the test account is in, is for `team_mention`. Use a few repositories, one of them with a workflow you can make fail.
- **A proxy you can watch**, set as manager.json `github.proxy`, and `github.auth.host` set to `github.com`. Marketplace reads and downloads go through it too. The extension's `host` defaults to `github.com` as well, so of the two moved values only `proxy` shows whether the move happened.
- **A test marketplace you own**, for the upgrade cases: register an unsigned copy with `personad extension market add`. Nonofficial signing metadata and legacy `trusted_extension_markets` entries do not grant sandbox exemptions. Preserve the default sandbox during acceptance.
  - An index keeps one entry per id, so publish one version at a time, each higher than the last. Six are used, all newer than the official marketplace's latest. Cases 8 and 9 use A and B, with the same permissions as the version installed; C, asking for one more permission; and D, with exactly C's permissions, tools, events and surfaces (anything else asks for consent again), but whose program exits at once. Case 11 uses E, working again, and F.
  - The hub reads a marketplace about every 6 hours. After each publish, run `personad extension market refresh <id>`, except where case 11 says not to.
- **Versions and the sandbox.** Record the extension's version and `sandbox` from `personad extension show github` in every case. A release on the official marketplace during cases 2 to 7 upgrades it when idle, which explains a restart you didn't cause. Installs from `file:` or `https://…`, and a marketplace that is no longer there, default to the sandbox, which limits where the program may connect and can block `proxy` (see "Sandbox"). To compare a `file:` install with a marketplace one, run `personad extension sandbox github off`.

**Where to look**

| What | Where |
| --- | --- |
| Which connector holds the id | The manager's log: the hub's log while the manager runs inside the hub, otherwise its own service's journal or `manager.log` (`docs/assistant/managers.md`). It shows `connector github: a plugin takes it over` and `connector github: back to the hub's own`. Also `personad extension list` / `show github`. When the hub restarts the extension for a change (its settings, the move itself too, a secret, the sandbox, an upgrade or a rollback), the log shows the pair, and the hub's own runs for those few moments: check for duplicates around them. Enabling shows only the takeover. A crashed program restarted within its backoff shows neither. |
| Sign-in and its source | **Settings › Connectors › GitHub**, `GET /api/v1/connectors/github/auth`; the `connector_secrets` row for `github`; the setting `github_auth` |
| The one-time move | The extension's settings (`proxy`, `host`), the setting `github.migrated_to_extension`, the log line `github: manager.json's github.proxy / auth.host moved …` |
| Who changed what | `personad extension audit github`: the extension's rows, and the connector's `connector_*` rows |
| What arrived | Inbox items with source `github` (count them per thread), knowledge base chats `github-*` |

**Cases**

1. **Baseline, before installing.**
   - Sign the hub's own connector in.
   - Let a few items arrive, and make sure some PRs are archived in the knowledge base.
   - Then, in the app, change some of its Inbox settings (ignored branches, for example). Mute one repository you won't use in case 4, and switch the knowledge-base feed off. Leave the repositories used in case 4 unmuted, and the CI-failure setting so that case 4's failing run is let in.
   - Write down the counts. Everything below is compared with them.
2. **Install.**
   - Install from the official marketplace: `market:<marketplace id>/github`, with the official marketplace's id from `personad extension market list` (`search` lists the test marketplace's `github` too, without saying which is which). Or use the app's market page.
   - The preview describes its permissions: the computer's gh login, and network access to GitHub's hosts, including where CI logs are stored.
   - Check:
     - it runs, not sandboxed;
     - the log shows the takeover;
     - Settings › Connectors › GitHub shows the same account, with no new sign-in;
     - the audit shows the install.
3. **What the takeover carries over.**
   - **Sign-in.** It stays the same, with nothing asked. Switch the source both ways in the app (device code, a computer's gh login). The extension follows: `connector.auth.set` after every start, `connector.credentials.gh_token` for gh, and token scopes in the auth view.
   - **Settings.** Inbox settings edited before the takeover show unchanged on the extension's side. Unedited ones come from the hub's copy, but only when nothing was ever edited in the app. To see that, run case 2 separately on a fresh isolated hub that has manager.json `github.inbox` set and no settings edited in the app: the extension starts with that copy.
   - **Mutes and switches.** Items from the muted repository still arrive filed as done (count the open ones), and the knowledge-base feed stays off. Then switch it back on for the cases below.
   - **Knowledge base.** New writes go to the existing `github-*` chats, with no second chat for a PR that already had one.
   - **The one-time move.**
     - Right after the install, `proxy` and `host` are in the extension's settings and the marker is set. The extension's GitHub requests go through the proxy; the hub's own connector still checks the computers' gh logins, and marketplace reads use the proxy too.
     - Change `proxy` by hand and restart the hub: the move does not run again.
     - A value already in the extension's settings is never overwritten. To see this:
       1. Remove the `github.migrated_to_extension` row from the `settings` table of the manager's database (for the built-in manager, the isolated hub's database).
       2. Set `personad extension config github proxy=<another proxy>`. The change restarts the extension, and that start runs the move again: the value stays, and the marker is set again.
       3. Set `proxy` back to the watched proxy.
4. **Sync.**
   - From the second account and the team, make one of each kind: `review_requested`, `mention`, `team_mention`, `assign`, `author` (a comment or review on the test account's PR), and `ci_failed` (a failing run the test account started). Each arrives once, in the expected tier.
   - A thread that had an item before the takeover updates that item; it gets no second one.
   - Closing or merging a PR settles its item.
   - Related PRs and issues are archived into the knowledge base.
   - The **GitHub** page (the extension's `nav_page`) works on the web, iOS and Android: lists, diff, comments, CI logs. A comment, review or merge is previewed and sent only after confirming.
   - The assistant's GitHub tools read PRs, diffs and checks. A comment or review asks on the phone first.
   - The avatar tiers find the account. Install a tiered look, use an account that is on its board, leave `personas.github_login` unset (it skips the connector), and read `login` in the `persona_tiers` setting. Today the tiers read only the hub's own connector (#6077), so expect them to lose the account while the extension holds the id; record it.
5. **Roll back by disabling.**
   - Run `personad extension disable github`. The log shows the hub's own back.
   - What the hub's own connector has carries back as the extension last had it: sign-in, source, its Inbox settings, mutes, switches and feeds. Changes made while the extension ran are kept.
   - The extension's own settings (`triage_rules`, its `inbox` repositories and kinds, `proxy`, `host`) mean nothing to the hub's own, which goes on with manager.json's `github.proxy`. Don't count them as lost.
   - Events from before, during and after the switch arrive once each: nothing is lost or duplicated.
   - The knowledge base goes on in the same chats.
   - Enable it again: it takes over again with no sign-in, its own settings are as they were, and the move does not run again.
6. **Roll back by uninstalling.** Run `personad extension uninstall github`.
   - The same checks as disabling.
   - What the hub keeps by id stays: the `connector_secrets` row, the connector's settings, switches and feeds, `github_auth`, the mutes and the knowledge base.
   - What is the extension's own goes: its settings (the moved `proxy` and `host` among them), and its package and data directories. Case 9 checks the rest, once case 8 has made it.
   - Install it again from the official marketplace: it takes over with no sign-in. The marker is still set, so `proxy` and `host` do not come back by themselves (#6107). On a network that reaches GitHub only through the proxy, a reinstalled extension can't sync until `proxy` is set again.
   - Before going on, set `proxy` again with `personad extension config github proxy=…`.
7. **When it does not start.**
   - A package without this platform's program, or with an `engines` this hub does not meet, is refused at the preview (try it as an upgrade) and never installed. Check that the installed extension is untouched.
   - Make an installed version fail to start. The problem shows at the next start of the hub (or of the manager running as its own service), not before:
     - remove its directory under the isolated hub's `~/.persona/extensions/installed/github/` and restart: `package_missing`;
     - or run a build of the current source numbered below the extension's `engines.persona` (`-X main.build=…`). An unnumbered development build only warns about `engines`, so "the latest personad" here means a numbered build.
   - Either way the hub's own connector takes the id back and keeps working.
   - Before going on, put the directory back (move it aside rather than deleting it), or return to the latest build, and restart. Installing over the same id is refused, and an upgrade needs a newer version; uninstalling and installing again would lose the extension's own settings.
   - Kill the running program a few times. Record what happens. Expected: the extension restarts with its backoff, and the hub's own does not come back while it stays enabled.
8. **Upgrade**, from your test marketplace, in this order. Auto-upgrade reads only the marketplace an extension came from.
   1. Publish A, and run `personad extension upgrade github market:<test market id>/github`. The extension now comes from the test marketplace. Settings are kept, the takeover stays, nothing asks for a sign-in, the move does not run again, and the log shows the usual brief flip.
   2. Publish B and refresh. With auto-upgrade on, it upgrades only when idle (no calls running, the assistant not working), with the same checks. The hub looks once a minute (the first time two minutes after it starts), so allow a few minutes.
   3. Publish C and refresh. It waits for you: `personad extension upgrades` shows it waiting. Confirm with `personad extension upgrade github market:<test market id>/github`, or in the app. Then do case 9's first part before step 4.
   4. Publish D and refresh. Once it goes in, it rolls back to C within about 30 seconds and records D as skipped; C takes the id again.
9. **Downgrade.**
   - Run this before case 8's last step, while the last upgrade succeeded: an automatic rollback leaves no version to go back to. So after step 3, run `personad extension rollback github`: it goes back to B, with the takeover and the extension's settings kept. A rollback also takes back the permissions granted to C, so C now waits for you again rather than coming back by itself. Confirm C once more as in step 3, then go on with step 4.
   - To go further back, once case 8 is done:
     1. Turn its auto-upgrade switch off and record a sandbox choice (`personad extension sandbox github off`). Then uninstall it (`--keep-data` keeps only its data directory). The hub's own comes back. Its sandbox choice, upgrade notes and the skipped version D go; its auto-upgrade switch stays off.
     2. Install an older package with `file:`, and run `personad extension sandbox github off`.
     3. It starts with only what the hub keeps by id, as in case 6: its own settings, `proxy` and `host` are gone.
     4. Set `proxy` again.
   - A `file:` or `https://…` install or upgrade defaults to the sandbox (record it). With `proxy` set, GitHub requests may then fail. Such an install stays out of auto-upgrade, and has no auto-upgrade switch.
10. **Restarts.**
    - Restart the hub, and the manager too where it runs as a service of its own. The extension starts and takes over again; nothing moves twice.
    - The inbox goes on without gaps or duplicates.
11. **Offline hub, or no marketplace.** Each part that installs the extension starts by uninstalling the one left installed before it: installing over the same id is refused.
    - **A marketplace that can't be read.** Use the test marketplace, not the official one: the official one and GitHub's API are reached the same way, through `github.proxy`, so cutting one cuts both.
      1. Publish E and install it from the test marketplace: the index now offers E, not the broken D. Set `proxy` again (#6107), and turn its auto-upgrade on (`personad extension auto-upgrade on github`): case 9 left it off.
      2. Publish F without refreshing (a refresh would let auto-upgrade install F), and make the marketplace unreadable: make its repository private, if it was added without a token.
      3. Run `personad extension market refresh <test market id>`: it fails with the error.
      4. `personad extension market list` shows the error. About 15 minutes later the hub's own read logs `plugin market <id>: … (trying again later)`. The installed extension keeps running, and no upgrade happens.
      5. Make the marketplace readable again afterwards.
    - **The official marketplace removed.**
      1. Install the extension from the official marketplace, and set `proxy` again (#6107).
      2. Remove the marketplace. An extension installed from it stops counting as a trusted marketplace's, and starts in the sandbox from its next start, unless a sandbox choice is recorded for it.
      3. `show github` reports the sandbox at once, but the running program changes only at its next start. Restart, then record whether it still syncs.
      4. A removed official marketplace is not added back by itself: add `github:nwverse/persona-extensions` again by hand.
    - **A package.**
      1. Install it from a package: `file:`, made with `personad extension pack`, or a release asset checked against its sha256.
      2. Switch the sandbox off and set `proxy` again.
      3. It takes over as after case 6's reinstall, and stays out of auto-upgrade.
    - **No network.** Cut the hub's network, the proxy included, and restart it. The installed extension starts from its package, and marketplace reads fail and back off. Bring the network back: sync catches up, with no gaps or duplicates.
    - **No extension.** A hub that never installs the extension keeps the hub's own connector, as before.
    - GitHub itself still has to be reachable (directly or through `github.proxy`) for either connector to sync.
12. **More than one manager.**
    - Add a second manager: make one in the app, and run `personad manager join <hub> <code>` (or `install`) with its own directory and database, as `docs/assistant/managers.md` describes. Sign its own GitHub connector in, from the app with that manager chosen.
    - The extension and its takeover belong to the manager it was installed on. On the second manager, Settings › Connectors › GitHub still shows its own connector, signed in as before, and its log has no takeover.
    - The command line always acts on the default manager. Reach another one from the app, or through the API with `X-Persona-Manager` (`docs/assistant/managers.md`).
    - Record anything that differs.

**If the hub's own connector were removed instead.** In this option, upgrading a hub installs the official extension once, by itself, behind a marker; there is no hub connector left to fall back to. The cases change as follows:

| Case | Without the hub's own connector |
| --- | --- |
| Install | Nothing for the user to do. The first start of the new personad installs `github` from the official marketplace, once, behind a marker, and must say so when the install fails, and retry. That install needs the marketplace: network access to it, through `github.proxy` where set, and a valid signature. An install today needs the owner's consent to its permissions (the preview's digest); installing by itself grants `gh_login` and network access unasked, so which hubs get it (all, or only those with GitHub signed in) must be decided. |
| Takeover | The same carry-overs, done once by the automatic install. What rests on the hub's own connector today must first move to the manager or the extension: `GET` / `PUT /api/v1/connectors/github/auth` and its scope check, the check of each computer's gh login, the gh token behind `connector.credentials.gh_token`, manager.json's `github` section (read by the move and the marketplace client), the copy of the `github.inbox` defaults, and the avatar tiers' account (#6077). |
| Sync | The same checks. |
| Disable, uninstall | Disabling turns GitHub off, with no fallback. Uninstalling also deletes the sign-in (`connector_secrets`) and the connector's switch, options and feeds: today they stay only because the hub's own connector needs them. `github_auth`, mutes, inbox items and the knowledge base stay. Reinstalling then needs a new sign-in, and #6107 matters more. |
| Does not start, crashes | GitHub is down until a fixed version is installed or the last one is rolled back. Nothing takes the id meanwhile. |
| Upgrade, downgrade | The same as today, through the marketplace. A rolled-back upgrade still works, but going further back means uninstalling, which loses everything above. |
| Restarts | No brief flip to a hub connector. Between the extension's stop and start no connector holds the id. |
| Offline hub, no marketplace | The main difference. A hub that reaches GitHub but not the official marketplace (the owner removed it, its index or signature fails, its host is blocked) gets no GitHub at all, where today it keeps the hub's own connector. Its only ways in are a package installed by hand (`file:`, out of auto-upgrade and in the sandbox) or another marketplace that lists `github`. A hub that is offline at upgrade time gets GitHub once the network is back and the install is retried; with no network, neither option syncs. |
| More than one manager | Each manager would install it on its own; which managers get it must be decided. |

**If a copy of the extension were bundled instead**, as a built-in plugin in `server/plugins/` like the WeChat plugin and its program `personad-wechat`:
- No marketplace is needed, so a hub that can't reach the official marketplace keeps GitHub. It upgrades and downgrades with personad, with no auto-install and no permission prompts.
- The cost is the same code in two repositories, kept in step by hand.
- A hub that already has the marketplace `github` gets the built-in instead: the built-in is registered first, and the installed row with the same id is ignored (its settings unread) until the built-in's first write replaces it; its package and data directories stay behind. `Registry.Yields` is the existing switch for the opposite, an installed copy winning.
- A built-in's connector starts switched off unless the user's switch was recorded, so the move must keep GitHub on.
- The program must join the released programs (`update.ProgramNames`) and every install path (`install.sh`, the Docker image).

## Built-in extension: WeChat (`wechat`)

When `personad-wechat` is beside personad, it registers as built-in extension `wechat` (#1352, `docs/extensions/wechat.md`), contributing connector `wechat` (only `status` and QR-code `login`,
declared in `wechat/spec`) and channel `wechat` (`channel` extension point, #1466, [channels.md](channels.md)), with only `network: *.weixin.qq.com` permission.
It must be built alongside the hub and runs with the connector switch. The first version combined `hub_api` + `events` (extension polling sessions and tracking the turn); since #1466 these use the hub's channel pipeline,
so the extension no longer gets the hub token or subscribes to events. A connector program acts as a channel: implement `channel.Transport` (`Send`, optional `Typing` / `Edit`), hand incoming messages to `serve.Init.Channel` (`Receive`);
`connector/serve` forwards `channel.*` and sends only once per `ref`.

## Built-in extension: computer control (`cua`)

Purely declarative (no process), contributing extension point `computer` (version 1, `{id, name?}`). **Off by default**; config is `machines` (enabled machines, written by machine-page switches), `backend` (Cua Driver / LCU / Auto) and `confirm`
(confirm on the phone before operations). Only when enabled does the assistant get `computer_state` / `computer_run` and the machine page show **Computer Control**. LCU runtime confirmation also uses these cards, not Claude / Codex. See [computer-use.md](computer-use.md).

## Extensions in manager.json

Manually configured extensions in the manager's `manager.json` (`~/.persona/managers/assistant/manager.json` for the built-in manager, `docs/operations/config.md` ("manager.json"); API source value `hub.json`) (Node-based voice extensions use this; `personad voice` entries are taken over by the built-in voice extension, see "Built-in voice extension") each get a synthesized manifest from the registry: HTTP only, `on_startup`, `hub_api` accepted, contributing their `kind`. Behavior is unchanged from before the foundation,
except the maximum backoff rises from 60 seconds to 5 minutes.

```json
{
  "extensions": [
    {
      "id": "voice",
      "name": "Voice",
      "kind": "voice",
      "command": "/usr/bin/node",
      "args": ["~/.persona/plugins/voice/src/main.mjs"],
      "dir": "~/.persona/plugins/voice",
      "env": {"SOME_SETTING": "…"}
    }
  ]
}
```

- `id`: `a-z0-9_-`, at most 32; also the app access path (`/api/v1/extensions/<id>/…`).
- `kind`: tells the app what to show. Currently only `voice` (voice contract below).
- `command` / `args` / `dir`: `~/` refers to the hub machine's home (`command` and `dir` expanded, `args` passed unchanged for the extension to expand).
  personad's systemd unit has a short PATH; use an absolute `command`.
- `disabled: true` disables while preserving config.
- `config`: extension settings (any JSON), passed unchanged as `PERSONA_EXTENSION_CONFIG`. Not included in registry config/forms or returned by endpoints (may contain credentials).
  Exception for entries taken over by the built-in voice extension: their upstream `config` goes in `PERSONA_VOICE_HUB_CONFIG`; `PERSONA_EXTENSION_CONFIG` contains Settings form values (see "Built-in voice extension").
- Built-ins may still use compatible `"command": "personad"` (no path) to refer to the hub binary itself, without changes after moves/upgrades. `personad` works too.
- `socket` without `command`: extension runs elsewhere; hub only connects (health checks, forwarding), never starts or restarts it.
  Useful when the hub machine cannot reach extension dependencies: run on a machine that can, forward the socket with `ssh -R <hub socket>:<local socket> <hub machine>`;
  the extension then configures `PERSONA_HUB_URL` (hub `public_url`) and token itself.
- Supplied env vars: `PERSONA_EXTENSION_SOCKET` (`~/.persona/extensions/<id>.sock`, old socket removed by hub before start, extension serves HTTP there), `PERSONA_EXTENSION_ID`,
  `PERSONA_EXTENSION_CONFIG` when configured, `PERSONA_HUB_URL` and `PERSONA_HUB_TOKEN` (local hub API address/token: configured by the hub owner, considered part of the hub like nodes,
  allowed to read knowledge base, Agents, etc.); corresponding `PERSONA_*` names also supplied.
- Request extension `GET /health` every second (every 10 seconds once started); 200 means `running`. Changes to `extensions` require hub restart.

## Voice (kind = voice)

Full duplex: the phone continuously sends microphone audio (including silence); the model decides when to speak. The app does no VAD and no push-to-talk.

This section is protocol 1. Voice-mode protocol 2 (spoken by the built-in voice extension from #3502; `start` includes `protocol: 2` and `target` binding the current chat, adding `run` / `link` frames, `resend` and target in `ready`; #3504 adds `ask`; from #3503 the extension also sends `clear` when the user interrupts; from #3505 it pauses the upstream while the bound conversation works, and `link` frames gain `paused` / `waking`) is in `docs/assistant/voice.md` section 3. Frames remain unchanged for clients not opting in; iOS only shows entry points for voice extensions whose `GET /info` reports `protocol` ≥2.
Both protocols may include `error.code`: `upstream_forbidden` (service rejected the key), `upstream_unavailable`, `upstream_failed` (disconnected, reconnect failed); the app treats unknown codes as absent.

### `GET /models`

```json
{"models":[{"id":"…","name":"…"}],"default":"…"}
```

Selectable models (`name` optional, id shown if absent); `default` is the extension-configured default.

### `GET /voices?model=<id>`

```json
{"voices":[{"id":"…","name":"Gentle Peach","gender":"female","languages":["zh"]}],"default":"…"}
```

Voices available for this model (different models may have different sets; omitted `model` uses the default model). The app stores the selected model and voice locally and sends them at conversation start.

### `GET /ws` (WebSocket)

Text frames are JSON; binary frames are audio (PCM16 little-endian, mono).

App → extension:

| Frame | Meaning |
| --- | --- |
| `{"type":"start","model":"<id>","voice":"<id>"}` | First frame; `model` and `voice` optional (extension defaults) |
| Binary | Microphone audio at `ready.input_rate`; do not send before `ready` |
| `{"type":"interrupt"}` | Interrupt the spoken response |
| `{"type":"text","text":"…"}` | Type a sentence (equivalent to speaking) |

Extension → app:

| Frame | Meaning |
| --- | --- |
| `{"type":"ready","input_rate":16000,"output_rate":24000,"model":"<id>","voice":"<id>"}` | Session ready; these sample rates are authoritative, never hardcode |
| Binary | Audio to play at `output_rate` |
| `{"type":"clear"}` | Discard unplayed audio (user interrupted or response canceled) |
| `{"type":"transcript","id":"…","role":"user"\|"assistant","text":"…","final":false}` | Captions: each update for the same `id` contains **all text so far**, directly replaced by the app; unchanged after `final`; `text` is never empty (the built-in extension opens a line only once it has words, #5587) |
| `{"type":"tool","id":"…","label":"Search knowledge base","done":false}` | Tool in progress (app shows a status line); several may be active (e.g. work delegated to main chat running in the background), tracked by `id`, latest shown |
| `{"type":"error","message":"…","fatal":true,"code":"…"}` | With `fatal`, extension closes the connection afterwards. `code` optional; built-in voice has `no_upstream` (fatal: upstream not configured), `model_unavailable` (nonfatal: selected model's upstream gone, default model used this time); app chooses wording by `code`, with `message` as detail |

Closing the WebSocket ends this conversation.

### App

- Android: tap the **Persona** tab title, then **Voice chat** in its menu (only with a `running` voice extension) to open voice; **Voice** at top right
  (a **Model configuration / Voice** menu once model configuration is wired into the host). Model comes from purpose `plugin.<id>`; old hubs use the extension's default.
  Recording uses `VOICE_COMMUNICATION` plus system echo cancellation (so the model does not hear itself on speaker), see `docs/android/android.md`.
- iOS: duplex voice chat in CarPlay (`docs/apple/carplay.md`), using the plugin's default model and voice.

## Built-in voice extension (`personad voice`)

personad includes a kind = `voice` extension, using a real-time service speaking the **GPT-Live protocol** (code `server/internal/voice`, prompts
`server/internal/prompts/voice/`, tool descriptions `prompts/mcp/voice/`). No separate Node or SDK installation.

Voice-mode design (turn-scoped delegation, binding the current chat, upstream setup in-app with keys encrypted on the hub, taking over manager.json entries) is in `docs/assistant/voice.md`; manager.json configuration below remains supported.

It is built-in: appears in **Settings › Extensions** without a manager.json entry (off by default, except when manager.json configures another voice program); upstreams can be set up in-app, see "Configure upstreams in the app" below.

```json
{
  "extensions": [
    {
      "id": "voice",
      "name": "Voice",
      "kind": "voice",
      "command": "personad",
      "args": ["voice"],
      "config": {
        "providers": [
          {"id": "codex", "name": "GPT-Live", "dialect": "codex", "url": "wss://<gateway>/v1/live", "key_file": "~/.persona/voice-gateway.key"},
          {"id": "rt", "name": "Real-time service", "url": "wss://<real-time service>/v1/live", "key_command": "<command printing credentials>", "ssh": "<machine that can reach it>"}
        ],
        "owner": "<what to call you>"
      }
    }
  ]
}
```

One extension may have several upstreams (`providers`); the app combines their model lists (model ids `<provider>/<model>`, upstream name appended to model names when there are several),
connecting to the upstream of the selected model. `config.default` specifies the default model (first upstream's default if omitted).

| Field | Notes |
| --- | --- |
| `id`, `name` | `a-z0-9_-`; model-id prefix and display name |
| `url` | Session WebSocket: `wss://<host>/v1/live` |
| `dialect` | `live` (default): public GPT-Live protocol; `codex`: the dialect Codex uses with `gpt-live-1-codex` (below); `fake`: development only, no service connection (no `url`), app-typed text goes unchanged to main chat, text for the model returns unchanged as its caption, enabling real-hub/real-Agent end-to-end runs without GPT-Live. `fake` allowed only in manager.json |
| `key` / `key_file` / `key_command` | Credentials sent as `Authorization: Bearer`: inline value, file's first line, or first line printed by command (`sh -c`). Command runs for every conversation, immediately using credentials renewed elsewhere. Credentials only on the hub machine (manager.json 0600 or separate file) |
| `ssh` | When the hub cannot reach the service, connect through this machine (`ssh -W host:port <machine>`, TLS still end-to-end); hub must have passwordless SSH access |
| `models`, `default_model` | Listed models; if omitted, live dialect reads service `GET <url>/models`, codex dialect uses `gpt-live-1-codex` |
| `tools` | Live dialect: declare Persona function tools in `session.start` (knowledge-base search, list Agents, send to Agent, hand to main chat). Some services delegate only when tools are declared (`docs/lessons/voice.md`, "Other behavior"); the public GPT-Live API rejects `session.tools`. **Unset (auto, the default since #1420)**: declared; when the service refuses them before `session.started` (an `error` naming `session.tools` or saying "tools", one about an unknown or unsupported field that names no other field, or a close whose reason says "tools"), the extension starts the session again once without them, logs it, and remembers it for that upstream until the extension restarts (a refused key, a handshake failure or a dropped socket never counts). `true` / `false`: always / never, with no fallback. Full devices (`X-Persona-Role` `full` or absent) declare all four; calls from other extensions declare only `ask_persona` (hand to main chat, their only capability); protocol 2 calls bound to a chat declare knowledge-base search, list Agents and `delegate_task` (hand to bound chat, `docs/assistant/voice.md` 4.4) |
| `input_rate` | Microphone sample rate 16000 / 24000; default live 16000, codex 24000 |

### Configure upstreams in the app (#3500)

The built-in manifest (`server/internal/extmgr/voice_extension.go`) includes a config form and secrets, so the generic **Settings › Extensions › Real-time Voice** details on iOS / Mac, Android and web
can set addresses, keys and switches; admin has a switch too:

| Form field | Notes |
| --- | --- |
| `url` | Service address `wss://<host>/v1/live` (`^wss?://`); `ws://` only for LAN, sends the key in plaintext |
| `name` | Name, default `GPT-Live`; shown after model names with multiple upstreams |
| `dialect` | `live` (default) / `codex` |
| `models`, `default_model` | As above; omitted live-dialect values read the service's model list |
| `input_rate` | 16000 / 24000 |
| `owner`, `notes` | What to call you, extra instructions for the voice assistant; supplied values override manager.json |
| Secret `key` | API key sent as `Authorization: Bearer`; encrypted with `secret.key` in `plugin_secrets`, write-only, endpoint reports only "set" |

`ssh`, `key_file`, `key_command`, an explicit `tools` and additional upstreams are manager.json-only (`key_command` executes on the hub machine, and the form subset has no object arrays).
The form's upstream has no `tools` switch, so it is auto: it declares the function tools and drops them only if the service refuses them (#1420). To keep them off, configure that upstream in manager.json with `"tools": false` instead.

- **Merge** (`voice.Merge`): manager.json upstreams first; the form upstream is appended when it has **both** address and key, fixed id `custom` (model ids `custom/<model>`).
  If manager.json already has `custom`, it wins and the form upstream is unused, with a note in extension-page `warnings` and `GET /info` `warnings`;
  likewise if manager.json `default` points to a nonexistent upstream (use the first upstream's default). A form with `providers` is read in full under the old format (manual `personad voice`).
- **Starts even without a usable upstream**: if there are no manager.json upstreams and the form lacks address or key, the extension still runs, `GET /health` returns 200, `GET /info.setup` says what is missing
  (`url_and_key` / `key` / `url`), and `/ws` returns `error{code: "no_upstream", fatal: true}`. Hub extension status is `needs_setup`,
  with a sentence such as "Real-time voice service address and key" in `needs_setup` (like `hubJSONWaitsForTriageModel`: running but needs configuration); the app only recognizes `running` voice extensions, so no voice entry appears.
- **Keys are sent only to the address they were set for**: when config changes `url` to another origin (scheme + host + port; case, default port and path differences do not count),
  the hub **deletes the key before saving config**, responds with `secrets_cleared: ["key"]` and adds "Address changed; key cleared. Please enter it again" to `warnings`; clearing the address counts as a change too.
  With an empty address, `PUT …/-/secrets/key` returns 400 `needs_url`. This prevents changing the address to an attacker's host and receiving the key as a Bearer token.
  - Implemented in the registry (`Registry.BoundSecrets` / `SecretNeeds`, hooks from `voice_extension.go`): applies to all `SetConfig` callers (assistant config changes, etc.), not just this endpoint;
    checking, key deletion, config save and key setting share one registry operation lock, so interleaved requests cannot pair a key with an address it was never set for.
  - **Prefer deletion on failure**: delete whether or not key-presence can be read (idempotent); unreadable presence still returns `secrets_cleared`. If deletion fails, save nothing, return 500 `secret_not_cleared`;
    if config save fails after deletion (database write failure, 409), the error also includes `secrets_cleared` and the old address remains. Invalid form config (400) changes nothing and keeps the key.
- **Model fallback**: if the selected model's upstream disappears (address cleared, manager.json changed), no error/disconnect: use default model and voice, then send nonfatal `error{code: "model_unavailable"}` after connecting.
- **`GET /info`**: `{"protocol": 2, "features": ["target", "runs", "asks", "resume", "suspend"], "setup"?: "…", "warnings"?: ["…"]}`, without keys or upstream list. `protocol` is the highest voice-frame version this extension speaks (2 since #3502; clients not sending `protocol: 2` in `start` still use protocol 1 from this section); `features` lists only implemented capabilities (`docs/assistant/voice.md` ("Version negotiation")).
- **Extension env vars**: `PERSONA_EXTENSION_CONFIG` (form values with defaults), `PERSONA_EXTENSION_SECRETS` (`{"key": "…"}`, decrypted only in process environment),
  `PERSONA_VOICE_HUB_CONFIG` (original `config` of the taken-over entry), entry `env`, and `PERSONA_HUB_URL` / `PERSONA_HUB_TOKEN` (manifest `hub_api`, as before);
  legacy `PERSONA_PLUGIN_*` names are also supplied and accepted.

### Taking over manager.json entries

- The **first** manager.json entry with empty or `voice` `kind`, `command` of `personad` / `personad` (no path), and `args[0]` of `voice` no longer runs as a manager.json extension,
  instead registering as a built-in with the same id: **id unchanged** (model-config purpose `plugin.<id>`, selected models and app voice choices remain), socket still `~/.persona/extensions/<id>.sock`,
  with `dir`, `env`, `config` supplied as before (env vars above). Entry `socket` unused; later matching entries still run as manager.json extensions.
- **Switch**: `disabled: true` keeps it off (even if its database row says on; enabling in-app returns 409 with a reason, settings remain editable);
  otherwise the database row wins, default on without a row, making upgrades transparent for existing entries. `disabled` is only a manager.json override (`Registry.KeepOff`), not written into the row:
  config/key edits preserve that row's switch (default "on" if absent); removing `disabled` and restarting follows the row. Turning it off in-app during this period is the owner's choice and is recorded.
- **No entry**: register built-in `voice`, off by default (like `cua`), enabled/configured under **Settings › Extensions**. Do not register if manager.json has another voice program (Node script),
  which runs as before; nor if an installed package already owns id `voice` (also skip when database reads fail and package presence is unknown).
  The built-in **does not yield its id** (no `Yields`): installing a `voice` package is rejected as a duplicate, preventing another program from obtaining keys set here (`plugin_secrets` keyed by id, not cleared by install).
  Disabled voice extensions are absent from model-config purposes; channel-list **Voice** prioritizes enabled voice extensions.
- **Rollback to old personad**: old versions only recognize hub.json entries, ignoring the built-in voice database row; entries still work, and `plugin_secrets` keys/forms remain for another upgrade.
- Voice is the only "taken over by same-id built-in" case; general support (and secret-address binding manifest field `bound_to`) comes later.

### Two dialects

| | live (public GPT-Live) | codex (`gpt-live-1-codex`) |
| --- | --- | --- |
| Handshake | `Authorization: Bearer` | Also `OpenAI-Alpha: quicksilver=v2`, model in URL `?model=` |
| First frame | `session.start{model, instructions, audio{format, output.voice}, delegation{type: client}, tools?}` | `session.update{instructions, audio.output.voice, delegation{type: client}}` |
| Microphone / audio | `session.input_audio.append` / `session.output_audio.delta{delta}` | `input_audio.append` / `output_audio.delta{audio}` (24 kHz) |
| Captions | `session.input_transcript.delta`, `session.output_transcript.delta` (append only, no completion marker) | `input_transcript.added`, `output_transcript.added`, full sentence in `turn.done` |
| Delegated work | `session.delegation.created{target: client}`; with tools, `response.event{function_call}` | `delegation.created{item{type: delegation, target: client, content}}` |
| Answer | Full turn response as `session.commentary.append{delegation_id}` (the delegation's only result, spoken by the model; 4000-character limit), with nothing returned at delegation time | `delegation.context.append{channel: speakable}`, split into 500-byte chunks |
| Typed sentence | `session.commentary.append{delegation_id: null}` | `session.context.append{channel: speakable}` |

When the model delegates to the client (delegation, or `ask_persona` function call with `tools` enabled), the extension hands it to **Persona's main chat** (creating and marking one as main if absent),
with message `client_msg_id` `voice-<16 random hex digits>`, task on the first line and a "Context · Voice chat" block after it. Return nothing at handoff (the model already said it handed it to main chat).
The extension tracks by turn (`docs/assistant/voice.md` ("Turn tracking"), same `channel.Run` rules as the channel pipeline): find the message by id, wait for **that turn** to end (queue behind another busy main-chat turn),
read its complete response, convert to speakable text (code, tables, links and hashes become placeholders such as "12 lines of code, on screen"; paths retain only filenames),
then return it in an `<agent_output>` data block as the delegation's only result; the model gives a one- or two-sentence conclusion. If the delegation is gone (after reconnect), say it as new context.
When main chat pauses to ask (`ask_user`), full-device calls read questions one by one (option names only); delegated utterances during this period count as answers, resolved through `POST /api/v1/interactions/{id}/resolve`.
Plans/permissions are announced only by category, asking you to tap on the phone; spoken "approve" never taps for you (`docs/assistant/voice.md` 4.7, #3504). Calls from other extensions, or without speakable cards, still say "Check your phone".
If background work remains, speak the result first and again when background work finishes.
The app shows "Persona main chat is working" meanwhile. With `tools` enabled upstream, knowledge-base and Agents queries execute directly through hub APIs.

**User-utterance gate**: delegated work (main chat, `send_to_agent`) runs only after a recent user utterance: the user spoke (caption or typing) after the model received the previous answer, within 30 seconds;
wait up to 2.5 seconds for late captions. "Received" is the upstream `session.commentary.appended` receipt (results may queue for tens of seconds while the model speaks; utterances while queued do not count); dialects without receipts use send time.
Otherwise do not send, and tell the model "You did not say this; I did not send it". Thus text like "Next step: call …" in an answer cannot make the model issue its own instructions.
Knowledge-base and Agents results do not close the gate (the model often looks before delegating); others' words in results use the same `<agent_output>` data block.
If the upstream rejects a result (expired delegation, too long), resend through `say` or a shorter version; never silently drop the turn's answer.
**Interruption gate** (#3502, both protocols, `docs/assistant/voice.md` 4.5 rule 3): extension-initiated speech (results for gone delegations, cards awaiting you) waits until you stop for 1.2 seconds and model audio ends for 1.5 seconds;
delegation results themselves wait for you to stop and model audio to finish. "You are speaking" uses app audio energy; sustained noise (TV, car) is subtracted as background after a few seconds. Your voice delays results at most 4 seconds, other speech at most 15 seconds.

The extension tracks through hub APIs (`PERSONA_HUB_URL` / `PERSONA_HUB_TOKEN`): session lists, pagination `…/items?after_seq=`, `…/items/{id}/full`, `GET /api/v1/machines`,
plus one shared `GET /api/v1/ws` (`subscribe` main chat, receive `turn_duration` in `items`, `sessions`, `delta`, `hook_event`; nonblocking read loop); without it, poll every 5 seconds.
`X-Persona-Lang` selects the language of these model/main-chat messages (Chinese, English, Japanese).

If the upstream session drops mid-conversation (service restart, network loss), the app connection stays open and shows "Disconnected, reconnecting"; the extension opens a new session with the last 40 utterances in the prompt,
at most 3 consecutive reconnects per conversation (reset after a session lasts 60 seconds). On app `interrupt`, the extension discards unsent audio for this response (GPT-Live has no interrupt frame; the model stops when it hears the interruption).

**Provider compatibility (#1229)**: `live` and `codex` are GPT-Live dialects. An ordinary ARK API Key, an OpenAI-compatible `/v1/realtime` endpoint, or a Doubao speech-service key does not make either dialect speak Volcengine's protocol. Volcengine Seeduplex uses `wss://openspeech.bytedance.com/api/v3/duplex/realtime/dialogue`, `X-Api-Key` and `session.model = "1.2.6.1"`; its adapter is tracked separately in #1229. Until it lands, this settings form requires a real GPT-Live-compatible service and key. A complete configuration (`GET /info` without `setup`) is not proof that the upstream WebSocket, account, audio or delegation works; there is no connection-test endpoint yet. The service must be enabled in its console before testing with its own credentials.

### Deployment

Deploying personad normally with `scripts/deploy.sh` includes this extension; add the entry above to the manager's manager.json, then `systemctl --user restart personad` (`extensions` changes require a hub restart, which restarts the built-in manager).
Try locally: `PERSONA_EXTENSION_SOCKET=/tmp/v.sock PERSONA_EXTENSION_CONFIG='{"providers":[…]}' personad voice` (old format),
or `PERSONA_EXTENSION_CONFIG='{"url":"wss://<real-time service>/v1/live"}' PERSONA_EXTENSION_SECRETS='{"key":"<key>"}'` (form format), then send `GET /info`, `GET /models` and open `/ws` on that socket.
App-configured upstreams need no hub restart: changing settings/keys restarts the extension (disconnecting active calls).

### Validation (2026-11-03)

- A real-time service speaking the public GPT-Live protocol (company-hosted): typed utterance, model speech/captions, delegation to main chat, function tools and connection through an `ssh` jump host all tested.
- Codex dialect: implemented from Codex source (`codex-rs` realtime_websocket), unit-tested with a fake upstream; an OpenAI-compatible gateway's `/v1/live` needs a voice-capable Codex account,
  otherwise handshake returns `503 no eligible upstream account` (shown unchanged in the app by the extension).
