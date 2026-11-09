# CLAUDE.md

## Highest-priority rules

Read [the super rules](docs/SUPER_RULES.md) before acting. They take precedence over this file, `AGENTS.md`, and all
other repository documentation. Changes to the super-rules file, including additions and deletions, require a
GitHub issue and explicit approval from administrator `chyroc` in that issue before editing or opening a PR.
The PR must link the approved issue. No Agent or delegated task may bypass this requirement, even to fix a failing check.

Persona is a self-hosted personal SuperAgent. A Go server, `personad`, runs as the **hub** on an always-on machine and holds all state;
the clients (iPhone, Apple Watch, Mac, Vision Pro, Android and the web) talk only to the hub. Each of your computers runs a **node**
(connected to the hub through Persona Link), where coding Agents (Claude Code, Codex, pi, opencode and other runtimes) run in an engine
(herdr, Orca or Paseo); any client can watch them live, answer their questions and approve plans and tools.
The assistant's main chat (the **Persona** tab) hands work to those Agents and draws on the knowledge base (`~/.persona/data`), the Inbox,
mail, phone data tools and extensions (connectors, channels, voice). Overview: `README.md`, `docs/server/architecture.md`.

Values specific to your machines and accounts go in `CLAUDE.local.md` (not committed); without it, set up from `docs/operations/config.md`.

**Names**: the product is Persona; the binary is `personad`, the connector `persona-link`, the runtime directory `~/.persona`, the build
config `~/.config/persona/config.json`, environment variables `PERSONA_*`. Older names still work so old and new versions interoperate,
but their literals live only in each platform's compatibility files (`docs/protocol/rolling-upgrade-compatibility.md`); everything else
uses Persona. Registered app identities (bundle IDs, App Groups, keychain groups) come from private config and never change.

## Conventions (mandatory)

### Workflow

- **Every change goes issue → claim → branch → PR → merge after local verification passes** ("Development workflow" below). Code, docs and config alike;
  never commit or push to `main` directly.
- **Old issue numbers**: `#N` in lines written up to 2026-11-07 (`git blame`) refers to the previous tracker; new text cites current numbers (`docs/operations/collaboration.md`, "Rules").
- **TestFlight feedback**: issues from crash reports or testers' feedback get the `testflight` label (or a `[TestFlight]` title prefix),
  with a scrubbed body and a dedupe marker (`docs/operations/collaboration.md`, "TestFlight feedback").
- **Write lessons down as you go**: pitfalls into `docs/lessons/<topic>.md` (index `docs/lessons/README.md`), design trade-offs into the
  module doc. Only the cross-module pitfalls people hit most get a line in "Most common pitfalls" here; keep this file short.
  One-off plans, acceptance reports and audits go to `docs/archive/` when done. New maintained docs go into the Docs index below
  (`scripts/test/test_doc_paths.py` checks the index and every quoted path).
- **Quality bar**: a top App Store app. Check every changed screen (watch included) in a simulator screenshot, dark and light, through
  DEBUG launch arguments + `xcrun simctl io <udid> screenshot`, and actually look at it; compiling is not enough (`docs/operations/testing.md`).

### Public repository

This is a public open-source repository; everyone and every Agent maintains it to that standard.

- **Write as if anyone can read it**: code, docs, comments, test fixtures, file names, commit messages, issue and PR titles, bodies and
  comments. Edit history is public: changing the text later does not remove it, so don't write it in the first place.
- **No internal names**: internal company services, internal repositories, internal tools and intranet access setups never appear.
  Capabilities that need them are extensions kept in a private extension repository; this repository has only generic extension points and names.
- **Issue scope**: do not open issues here for changes to a private extension repository. If this repository also needs changes,
  describe only the work needed here in its issue, not the work in the private repository.
- **No private information**: no intranet domains / IPs / URLs, host names, real people's names or messages, personal accounts, mail
  addresses, tokens, keys or project IDs. Examples use placeholders (`<hub address>`, `me@example.com`). `scripts/privacy_check.py` checks
  (`git config core.hooksPath scripts/hooks` runs it on commit and push; `docs/operations/testing.md`, "Privacy scan").
- **Make it configurable**: machine names, domains, accounts, paths, launchers and preferences come from config (build / deploy:
  `~/.config/persona/config.json`; hub runtime: `~/.persona/hub.json`). The UI says "the hub machine" and "your computers".
- **Secrets never enter the repository**: the hub token and database password live only in `~/.persona/hub.json` (0600); mail
  credentials are AES-GCM encrypted in Postgres with the key file kept out of the database.

### Language

- **English for everything people read in the repository**: code comments, docs, commits, PR titles and bodies (a squash merge makes them
  the commit on `main`), issues and comments. Natural, concise English, not word-for-word translation. `docs/` is English only.
- **The README is bilingual**: `README.md` and `README.zh-CN.md` carry the same content and change in the same PR; their images come from
  `scripts/assets/make_readme_assets.py` in both languages.
- **User-facing text always goes through localization resources** (`docs/protocol/i18n.md`): Apple `apple/Shared/Localization/Localizable.xcstrings`,
  web `web/src/i18n/locales/`, Android `res/values*/strings.xml`, hub `server/internal/i18n/locales/`. Every new string ships in `en`,
  `zh-Hans` and `ja` together; the checks catch gaps. Apple: write `String(localized:)` / `Text`, run `scripts/i18n/apple_l10n.py sync <DerivedData>`
  after a build, then fill in en and ja. Apple tests run in zh-Hans.
- Chinese stays only as data that must match something real (hub or third-party output, prompts sent to a model, fixtures), marked
  `// i18n-ignore` where the checks ask.

### Server and data

- **All server state lives in the Postgres container** `persona-postgres` on the hub machine (`docs/server/server.md`); no JSON state files
  in `~/.persona/`. The exception is the knowledge base `~/.persona/data`: files first, the database as the index (`docs/assistant/knowledge.md`).
- **The hub is only a communication center**: new business logic (endpoints, tables, model calls) goes into the managers
  (`server/internal/assistant`, `server/internal/devices`, `server/internal/extmgr`). `server/internal/hub/architecture_guard_test.go` pins the hub's routes,
  dependencies and Store; when it fails, move the logic as its hint says (`docs/server/architecture.md`).
- **Prompts live in `server/internal/prompts/`** (MCP tool descriptions under `mcp/`): one prompt per file, composed with templates, never
  built with `fmt` / `+` in Go. Update the golden tests and read the diff (`docs/assistant/prompts.md`).
- **Phones reach the hub directly over the internet** (TLS, preferring IPv6; VPNs are the user's own business), with the LAN kept
  compatible (`docs/operations/network.md`).

### Product and UI

- **Links open inside the app**: any link in a conversation opens in a card rising from the bottom that fills the screen; dragged down to
  0.6 it stops blocking the chat, further down it closes. Speed first (preloading, caching, reused WebViews). Selected text can go to the
  Agent as **AI edit** or **Use as context**, carried in the message as XML tags.
- **Never change how an existing screen looks directly**: a different visual style is a new **style** (`AppStyle`), a different color set a
  new **theme color** (`ThemeColor`); the default and every other option stay pixel-identical. Bug fixes and changes the user asked for
  don't count. Android works the same way with its three styles (`docs/android/android-design.md`).
- **The UI uses only the design system**: font sizes, spacing, radii, sizes, animation, colors, icons and brand marks come from tokens;
  pages never hard-code numbers, system font sizes, SF Symbol names or hex colors. Missing something? Add a token first.
- **Button text never wraps**: one line (`.lineLimit(1)`, then `.minimumScaleFactor`, shorter text or an icon); what doesn't fit in a row
  of buttons goes into a menu.
- **Desktop is the web app plus Persona Link**: on computers people use the web app (browser or installed PWA). Persona Link
  (`apple/PersonaLink/`, `persona-link`, the Windows tray) is reserved for what a browser cannot do, such as notifications while no tab is
  open, presence and local integrations. The native Mac app (`apple/Mac/`) is frozen: bug fixes, security fixes and keeping it building
  only; new features skip it. Never start another desktop client (Electron, Tauri, a WebView shell): a desktop need goes into the web app
  first, and into Persona Link only when the browser cannot meet it (`docs/apple/mac.md`, `docs/operations/link.md`). Why: the web app
  already is the desktop UI and Persona Link already holds the native connection on computers that run Agents, so a second UI only adds upkeep.

## Development workflow

People and Agents follow the same steps, each with their own GitHub account. Templates and copyable `gh` commands: `docs/operations/collaboration.md`.

1. **Look for duplicates**: `gh issue list --state all --search "<keywords>"` and `gh pr list --state all --search "<keywords>"`, closed
   and merged ones included.
2. **Claim the existing issue, or create one** (`enhancement` for features, `bug` for fixes) saying what and why.
3. **Claim before you start**: comment `Claim:` / `Plan:` / `ETA:`; an Agent adds `Agent: yes (<machine id> / <branch or session>)`,
   using the config's machine id, never the host name. The author of the latest claim owns the work; nobody is assigned.
4. **Claimed by someone else**: if the owner updated it in the last 24 hours, leave it alone (adding information is fine). After 24 hours
   without an update, comment `Take over:` with the reason. Renew your own claim with `Progress:`.
5. **Work on your own branch / worktree** named with the issue number (`fix/123-xxx`). Bring it up to date by merging `origin/main`
   (preferred over a rebase; never force-push). Giving up: comment `Abandon:` with how far you got.
6. **Open one PR per issue**, in English, with `Fixes #N` / `Closes #N` (or `Refs #N` to link without closing) in the body.
   `.github/workflows/pr-needs-issue.yml` closes PRs that link no issue and reopens them once the link is added.
7. **Merge after local verification passes**: run the checks in "Build and verify" for every affected area, including workflow lint for
   CI changes, and look at screenshots for UI changes. Opening or updating a PR does not run hosted CI. A scheduler dispatches full CI
   on `main`; its failures are fixed separately and do not replace the PR's local verification (`docs/operations/testing.md`, "Hosted CI").
   Then `scripts/hooks/merge-pr.sh <number>` (scans the PR with this machine's private values, which CI does not have, then squash-merges
   and deletes the branch). Make sure the issue is closed.

## Build and verify

- Portable CI: `scripts/ci/server.sh`, `scripts/ci/web.sh`, `scripts/ci/android.sh`, `scripts/ci/apple.sh`, `scripts/ci/scripts.sh`;
  runtime/SDK checks: `scripts/ci/runtimes.sh`. Same scripts locally and on hosted runners, no private config or signing secrets.
  Dependencies, optional path filters, coverage and cost: `docs/operations/testing.md` ("Hosted CI"). Manual dispatch defaults to all
  platform checks; no hosted repository guards run automatically on PRs.
- Server: `scripts/test/test-server.sh` (real Postgres on the hub machine); portable CI creates its own disposable Postgres 18 container.
  Deploy: `scripts/deploy/deploy.sh [machine id…]`.
- iOS: `scripts/app/sim.sh [-openSession <machine>/<session>]` builds, installs on your simulator and pairs it. Before xcodegen / Xcode,
  run `python3 scripts/persona_config.py xcconfig`; before a manual `xcodebuild`, `eval "$(python3 scripts/persona_config.py env)"` or an
  old Xcode reports errors that look like someone else's breakage.
- Android: `scripts/app/android.sh [install|apk|test|lint|shots]` (`docs/android/android.md`).
- Web: `cd web && npm test && npm run build`; deploy with `scripts/deploy/deploy.sh web` (`docs/web/web.md`).
- Docs: `python3 scripts/test/test_doc_paths.py`, `python3 scripts/test/test_doc_go_paths.py` and `python3 scripts/test/test_doc_wire.py`
  (every WebSocket type, feature flag, extension point and manifest field has its row). Every change: `python3 scripts/privacy_check.py`.
- Releases: `docs/operations/release.md`. After `scripts/release/release.sh` uploads, run `python3 scripts/release/asc_testflight.py wait 45 <build number>`:
  it adds the build to the internal group and to the external group with Beta App Review. Report the build number and review status only after both.

## Most common pitfalls

The full list is in `docs/lessons/`; read the file for the area you touch first.

- **Several Agents share this repository**: work in your own worktree from `origin/main`, stage only your files, and check that
  `git diff origin/main...HEAD --stat` lists only them before pushing. Use your own simulator and mock hub port (check with `lsof`; stop a
  mock by its port, never `pkill -f mock_hub.py`). Look at every worktree before picking a database migration number (`docs/lessons/workflow.md`).
- Scripts that wait for background jobs or logs always have a timeout and end with a one-line verdict on success, failure and timeout.
- **Simulators only through `scripts/app/sim-boot.sh`** (`--profile default|watch|live-activity|stock`, slimmed by default); never
  `simctl create/boot` or `-destination name=…`, and never reboot someone else's. A failure only on a slim simulator gets rerun on
  `--profile stock` first; a "no system prompt appeared" test proves something only on stock. UI tests launch with `app.launchPinned()` (`docs/operations/testing.md`).
- A DEBUG launch argument's name must not equal any Swift identifier, or `release.sh`'s strings check blocks TestFlight: name switches
  `open…` / `show…` and add them to the list in `scripts/release/release.sh`.
- Release builds need `SWIFT_REFLECTION_METADATA_LEVEL = all`, or every App Intents / Shortcuts parameter is empty and the app crashes;
  Debug and the simulator never show it (`docs/lessons/ios.md`).
- A missing keychain group shared with extensions silently falls back on devices but works in the simulator: release ipas must pass
  `scripts/release/entitlements.py check` (`docs/operations/release.md`). Everything a Live Activity draws goes in its ContentState or
  App Group files; its extension can't use the keychain on a phone (`docs/apple/live-activity.md`).
- Don't write frequently changing state to UserDefaults (every write recomputes every `@AppStorage` view); use `StateCache`, and
  `@StoredValue` for heavy Mac views (`docs/lessons/macos.md`).
- App targets default to MainActor: closures the system calls on another queue are `{ @Sendable … in`, or they crash; plain data types in
  `apple/Shared` are `nonisolated`. Polling and read receipts use `.tabActiveTask` / `.onTabVisibility`, since hidden pages never get `onDisappear`.
- On the Mac, a constantly running `TimelineView(.animation)` relayouts the window every frame; hand persistent animations to Core Animation (`MacBreathingRing`).
- **Minimum compiler is Xcode 26.0 (Swift 6.2)**: wrap newer-SDK APIs in `#if compiler(>=6.4)` with the old behavior in `#else`
  (`#available` covers only runtime). Build with 26.0 when you have it; otherwise note the Xcode you used and "26.0 not verified" in the
  PR. Raising the minimum is the user's decision (`docs/lessons/ios.md`, "Minimum compatible compiler").
- The watch app targets watchOS 10 and compiles all of `Shared/`: only watchOS 10 APIs, with `#available` fallbacks (`Color.blended`, not `Color.mix`).
- Someone else's redeploy can wipe the hub's memory at any time: flows that span requests must be self-contained or persisted.
- The hub's port belongs to `personad.socket` (socket activation) and versions switch blue-green with `personad hub switch`. Before going
  back to a binary that doesn't understand `LISTEN_FDS`, run `PERSONA_HUB_SOCKET=0 scripts/deploy/hub-systemd.sh`; to stop the hub, stop the
  socket too (`docs/operations/hub-restart.md`).
- Behind a container port mapping every connection can look like the bridge gateway, and a same-machine reverse proxy makes internet
  traffic look like loopback: never trust the source address alone (`containerGateway`, `security.proxy_protocol`; `docs/operations/network.md`).
- Claude Code's config directory isn't always `~/.claude` (`CLAUDE_CONFIG_DIR`), and one session id can exist in two; take the most
  recently modified. Verify transcript or sync changes against real transcripts and bump the app's `ChatCache` version when items change
  (`docs/lessons/agents.md`, `docs/lessons/transcript.md`).

## Design system (iOS)

Tokens live in `apple/Shared/Design` (also compiled for the watch and widgets); values and rules in `docs/apple/ios.md` ("Design language").

| Need | Use |
| --- | --- |
| Font, emphasis, line height | `Theme.Font` levels and emphasis tokens, `.lineHeight(_:)` |
| Spacing | `Theme.Space` (4 pt grid; `page` is the side margin) |
| Corner radius | `Theme.Radius` |
| Size | `Theme.Size` + `.scaledFrame(…)` (scales with the font size); plain `frame` only for lines of 4 pt or less |
| Sidebar rows | `Theme.Sidebar` (web `--sidebar-row`): HIG row height + padding, same in every language (`docs/apple/sidebar.md`) |
| Animation | `Theme.Motion`; chat content unfolds with `.unfolding` |
| Color | `Theme` role colors, `Theme.Fill`, `Theme.Diff`, `Theme.Hue`, `Palette`, `AgentState.tone` |
| Icons, logos | `Theme.Icon` (one SF Symbol per meaning) drawn with `ThemedIcon` / `IconTile` in the style's icon language (`docs/apple/icons.md`); `Theme.Brand` drawn through `BrandMark` / `BrandTile` / `BrandBadge` |
| Style × theme color | `StyleSpec.current` (`ComponentSpec` × `ColorSpec`), one file per style in `apple/Shared/Design/Styles/` |

- **Components** live in `apple/Persona/DesignSystem`: `.surface(…)`, `.themedButton(…)`, `.themedPage()` / `.themedGroupedPage()`,
  `.themedRow()`, `Chip`, `CountBadge`, `EmptyStateView`, `Monogram`, `Loadable`, `SecretField` (never `SecureField`). Never write
  `glassEffect` / `.glass` directly or assemble a lookalike component in a page.
- **A new style** is a `ComponentSpec` + an `AppStyle` case, **a new theme color** a `ColorSpec` + a `ThemeColor` case; everything else reads
  `StyleSpec.current`, never `switch AppStyle`. BeautifulUI components appear only under that style, chosen inside design-system components.
- **Checks** in PersonaTests: `TypeScaleTests`, `IconVocabularyTests`, `TokenRatchetTests` (hard-coded values per file can only go down;
  after reducing them, delete `apple/PersonaTests/TokenBaseline.json` and rerun) and `StyleSpecTests` (contrast). Never add exemptions or raise a baseline.
- **Logic layer**: load → show → edit → save pages use an `@Observable` `XxxModel` with dependencies from `Deps` and tests with `FakeHub`;
  pull pure computation out of busy pages first (`docs/apple/ios.md`, "Logic-layer choice").
- **Android** has its own design system (`android/core/designsystem`, `docs/android/android-design.md`); `scripts/app/android_token_baseline.py`
  keeps hard-coded values going down. Check UI changes with `scripts/app/android.sh shots` in all three styles, dark and light.

## Layout

| Path | Contents |
| --- | --- |
| `server/` | Go module: `personad` (hub / node / hook / mcp and helper commands), `persona-link`, `personad-wechat`; `docs/server/server.md` |
| `apple/` | XcodeGen project (`apple/project.yml`): iOS, watchOS, macOS (`Mac/`) and visionOS (`Vision/`) apps, extensions, shared code in `Shared/`, tests; `apple/PersonaLink/` is the separate Mac menu bar app (`docs/operations/link.md`) |
| `android/` | Kotlin + Compose: `app`, `core/*`, `feature/*`, `platform/*` (`docs/android/android.md`) |
| `web/` | Web app (Vite + React + Tailwind) served by the hub (`docs/web/web.md`) |
| `integrations/` | Agent runtime integrations and the runtime SDK (`docs/integrations/runtimes.md`) |
| `sdk/`, `schema/`, `spec/` | Extension SDK, extension JSON Schemas, shared behavior specs and fixtures |
| `scripts/` | Build, test, deploy, release, mock hub and config tools (`scripts/README.md`) |
| `deploy/docker/`, `Dockerfile` | Docker deployment of the hub (`docs/operations/docker.md`) |
| `site/` | Website, privacy policy and terms (`docs/operations/site.md`) |
| `docs/` | Design docs (`docs/README.md`, index below) |

## Docs index

Every maintained doc under `docs/` (lessons excluded) must be listed here.

| Area | Docs |
| --- | --- |
| Super rules | `docs/SUPER_RULES.md` highest-priority rules and prior administrator approval for changes |
| Start | `docs/README.md` navigation · `docs/operations/getting-started.md` setup without a terminal · `docs/assistant/onboarding.md` new-user onboarding · `docs/operations/config.md` config.json and hub.json · `docs/operations/collaboration.md` claims, PRs, templates · `docs/protocol/i18n.md` localization |
| Architecture and protocol | `docs/server/architecture.md` · `docs/protocol/protocol.md` HTTP / WebSocket API and node RPC · `docs/protocol/agent-states.md` · `docs/protocol/multi-hub.md` one device, several hubs · `docs/protocol/connector-protocol.md` · `docs/protocol/rolling-upgrade-compatibility.md` old / new name interop · `docs/operations/rename.md` naming contract · `docs/operations/persona-cutover.md` approval-gated naming cutover tool |
| Server | `docs/server/server.md` modes and operations · `docs/server/mesh.md` linked hubs · `docs/server/machines.md` monitoring · `docs/server/usage-windows.md` subscription usage · `docs/server/launcher-health.md` · `docs/server/agent-sandbox.md` macOS agent sandbox |
| Operations | `docs/operations/network.md` direct connection, TLS, pinning · `docs/operations/docker.md` · `docs/operations/hub-restart.md` seamless restarts · `docs/operations/hub-update.md` · `docs/operations/upgrade.md` moving an install to the split hub and manager · `docs/operations/push-relay.md` · `docs/operations/admin.md` admin UI · `docs/operations/ports.md` port forwarding · `docs/operations/link.md` Persona Link and the node service · `docs/operations/persona-link-install.md` · `docs/operations/node-update.md` · `docs/operations/windows.md` · `docs/operations/auth-relay.md` · `docs/operations/site.md` website · `docs/operations/testing.md` tests, simulators, privacy scan · `docs/operations/runtime-cli.md` DEBUG command channel · `docs/operations/release.md` signing and TestFlight |
| Agents and runtimes | `docs/integrations/engines.md` herdr / Orca / Paseo · `docs/integrations/claude-integration.md` · `docs/integrations/codex.md` · `docs/integrations/codex-app-server.md` · `docs/integrations/runtimes.md` runtime spec · `docs/integrations/daemons.md` · `docs/integrations/dsh.md` · `docs/integrations/pi.md` · `docs/integrations/opencode.md` · `docs/integrations/paseo.md` · `docs/integrations/agent-goal.md` · `docs/integrations/terminal.md` · `docs/integrations/launchers.md` model providers · `docs/integrations/script-cli.md` `personad ask` / `notify` |
| Assistant | `docs/assistant/assistant.md` main chat · `docs/assistant/managers.md` · `docs/assistant/assistant-views.md` rich UI · `docs/assistant/unified-entry.md` · `docs/assistant/agent-work.md` work state and triage · `docs/assistant/todos.md` · `docs/assistant/voice.md` · `docs/assistant/health.md` · `docs/assistant/knowledge.md` knowledge base · `docs/assistant/inbox.md` · `docs/assistant/model-config.md` · `docs/assistant/model-profiles.md` · `docs/assistant/prompts.md` · `docs/assistant/office.md` Word / PowerPoint / Excel |
| Extensions and sources | `docs/extensions/extensions.md` extension system · `docs/extensions/extension-settings.md` host-owned settings layout · `docs/extensions/connectors.md` · `docs/extensions/subscriptions.md` · `docs/extensions/channels.md` · `docs/extensions/extension-messaging.md` · `docs/extensions/computer-use.md` · `docs/extensions/mail.md` · `docs/extensions/wechat.md` · `docs/extensions/bots.md` Lark bot · `docs/extensions/feishu.md` · `docs/extensions/lark-agent.md` · `docs/extensions/lark-migration.md` · `docs/extensions/lark-attachments.md` · `docs/extensions/lark-encryption.md` |
| Apple | `docs/apple/ios.md` iOS / watchOS, design language · `docs/apple/icons.md` icons · `docs/apple/sidebar.md` sidebar rows · `docs/apple/ios-state.md` state and redraws · `docs/apple/shell.md` · `docs/apple/mac.md` (frozen) · `docs/apple/mac-design.md` · `docs/apple/vision.md` · `docs/apple/live-activity.md` · `docs/apple/intents.md` · `docs/apple/siri.md` · `docs/apple/carplay.md` · `docs/apple/persona.md` avatars · `docs/apple/phone-tools.md` · `docs/apple/recording.md` · `docs/apple/meta-glasses.md` |
| Android and web | `docs/android/android.md` · `docs/android/android-design.md` · `docs/web/web.md` · `docs/web/workspace-board.md` · `docs/web/review.md` reviewing a session's changes · `docs/web/preview.md` dev server previews |
| Plans (open decisions) | `docs/plans/plan-extensions.md` · `docs/plans/plan-connectors.md` · `docs/plans/plan-orca-support.md` · `docs/plans/plan-tmux-support.md` · `docs/plans/plan-lark-cli.md` · `docs/plans/plan-discord.md` · `docs/plans/plan-x.md` · `docs/plans/plan-ego-browser.md` · `docs/plans/plan-antigravity.md` |
| Lessons and archive | `docs/lessons/` pitfalls by topic (index `docs/lessons/README.md`) · `docs/archive/` dated records, evidence only (index `docs/archive/README.md`) |
