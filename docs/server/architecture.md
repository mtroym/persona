# Architecture

The phone connects to the hub directly: HTTPS / WSS over the public internet (TLS + public key pinning), with HTTP / WS kept for the LAN / VPN. Public access prefers IPv6; the VPN is set up by the user. See the [network doc](../operations/network.md).

## Goals

- On the phone (and watch), talk to Claude like in a chat app, but Claude itself is always an **interactive Claude Code** inside herdr on some machine:
  open `herdr` at the computer and it is the same session, so you can take over seamlessly; the phone is just another "terminal".
- Pick a launcher at start (`cc_ds` / `cc_max` / `ccc` … from `~/.zshrc`; in essence this picks the model and gateway).
- See the chat as it happens on the phone: text, thinking, tool calls and results, the current screen.
- Every step that needs a person (AskUserQuestion, plan approval, tool permissions, trust-folder and other TUI dialogs) can be handled on the phone / watch.
- Claude can use the iPhone's data (contacts, calendar, reminders, photos, health, location, clipboard…) and mail.

## Components

```
 phone / watch ─┐  HTTPS/WSS (public internet, clients that support pinning)
                ┤  HTTP/WS (LAN / VPN, compatible with older apps)
                ┤ ─────────────────────────►  personad hub (HTTP + optional TLS)
                │                                   │  ├─ mail (IMAP/SMTP, encrypted credentials)
                │  WS: tool.call ◄──────────────────┤  ├─ interaction queue (questions / plans / permissions)
                │      (phone data tools)            │  └─ session index, event fan-out
                                                    │
                     stdio JSON-RPC over `ssh <host> personad node --stdio`
                     (on the hub machine itself, started directly as a child process)
                                                    ▼
                              personad node (one per machine, lifetime follows the hub's connection)
                               ├─ herdr socket API (~/.config/herdr/herdr.sock, CLI fallback): create tab, start claude, prompt,
                               │   send-keys, read, agent status; events.subscribe pushes changes
                               ├─ tail ~/.claude/projects/*/<session>.jsonl → structured events
                               ├─ unix socket ~/.persona/node.sock ◄── personad hook (Claude Code hooks)
                               │                                 ◄── personad mcp (MCP tools for claude)
                               ├─ unix socket ~/.persona/runtime.sock ◄── runtime: Agent runtimes without a terminal (DSH's web plugin, docs/integrations/runtimes.md)
                               └─ launcher list: zsh -ic (`_claude_launchers` + aliases that call claude)
```

The hub keeps `listen` (HTTP `:18660` by default); TLS is enabled separately with `public.tls_listen`, on a port the user picks. Both listeners share the API and the source-based auth rules. The master token is only accepted from loopback / LAN sources; the public internet uses temporary pairing tokens or device tokens. The public response of `health` contains no version, capabilities or candidate addresses.

Candidate addresses come from config and the local network interfaces, ordered IPv6, domain names, LAN, public IPv4. After auth they are sent through `/api/v1/endpoints` and the WS `hello`; address change events go only to connections that declare the `endpoints` capability. An address on an interface does not mean the modem or firewall lets traffic in; it still has to be verified from outside. Existing iOS / Watch apps, without code changes, keep using the original LAN / VPN address.

A single binary `personad`, four modes:

| Mode | Where it runs | What it does |
| --- | --- | --- |
| `hub` | An always-on machine, resident under systemd --user | The app's API, event fan-out, interaction queue, mail, dispatching to each node |
| `node --stdio` | Every machine, started by the hub over SSH | Relays local herdr / claude / transcript / hooks |
| `hook <event>` | Called by Claude Code hooks | Hands events to the node; for things a person must decide (questions, plans, permissions) it blocks waiting for the phone's answer |
| `mcp` | Claude Code MCP (stdio) | Exposes phone data, mail and other tools; calls go node → hub → phone |

## Why it is designed this way

- **No `claude -p --output-format stream-json`**: that is a one-shot headless mode; you cannot take over at the computer, and there is no TUI interaction. What the user wants is
  "a claude inside herdr on the server / Mac".
- **Structured content comes from the transcript JSONL**: Claude Code appends a line as each content block (thinking / text / tool_use / tool_result) completes,
  so tailing it gives a reliable structured stream. **Character-by-character in-progress output** comes from `herdr pane read` (screen sampled every 400 ms, only while someone is subscribed and the agent is working),
  shown in the app as the "live screen" strip. The two combined = real-time streaming.
- **Interaction goes through hooks, not simulated keystrokes**: a hook gets the question / plan as structured data, and the answer is structured too (tested, see `docs/integrations/claude-integration.md`).
  Simulated keystrokes (`herdr agent send-keys`) are only a fallback: for any TUI dialog hooks cannot cover (trust folder, model picker…), the app shows a screen snapshot +
  a key panel.
- **Hooks are injected only into sessions Persona starts** (a plugin loaded only for that session, `--plugin-dir ~/.persona/claude-hooks`; older Claude Code uses
  `--settings ~/.persona/claude-settings.json`). Global settings are not changed, and claude sessions the user opens themselves are not affected. A plugin is used because `--plugin-dir` stacks,
  while `--settings` only honors the last one and would override the launcher's own (gateway, key, model; `docs/integrations/claude-integration.md` ("Hooks")).
  herdr claude sessions not started by Persona can still be viewed in the app (the transcript is found by the session id herdr reports), sent messages and answered with keys.
- **node over SSH stdio**: reuses existing aliases such as `ssh <machine>` from `~/.ssh/config`; no new ports, no new auth surface; the hub reconnects automatically when the connection drops.
  Computers SSH cannot reach (behind NAT, on another LAN) fall back to tailcat hole punching (`docs/server/server.md` ("tailcat hole punching")): SSH first, tailcat if that fails.
- **An Agent without a terminal is a runtime, not a host**: the host is "where the terminal is open" (herdr / Herdr App / Orca, one engine per machine, `docs/integrations/engines.md`), and the node recognizes sessions from panes and
  transcripts. Agent runtimes like DSH, which manage their own sessions and have no terminal, plug in through the runtime protocol on `runtime.sock`: they report their own sessions, push content, ask the phone,
  and coexist with the engine this machine picked. The hub and app need no changes, and new runtimes need no changes to Persona either (`docs/integrations/runtimes.md`: protocol + SDK + acceptance checklist).
  Paseo is the exception: it has no terminal and connects through the runtime protocol (the node runs its bridge itself), yet it is an engine a machine can pick, and both new and resumed sessions are handed to it (`docs/integrations/paseo.md`).
- **The hub is on an always-on machine**: it is always online and can reach Gmail (verified: imap.gmail.com:993 / smtp.gmail.com:465 reachable directly), and every Mac is relayed through it.

## What carries the data

Every path between an app and a computer goes **through the hub**: the app opens its WebSocket (`/api/v1/ws`) to the hub, the computer holds its Persona Link connection to the hub, and the hub splices the two. There is **no direct phone-to-computer transport** and no way for a phone to address a computer without the hub in between.

Two direct-path facilities exist, and neither covers that leg:

- **tailcat hole punching** (`docs/server/server.md` ("tailcat hole punching"), `server/internal/tunnel`): real NAT traversal, upgrading to `direct` and falling back to a DERP `relay`, but it carries the **hub/manager → node** connection only, because the hub cannot depend on SSH reaching a laptop. The mobile app has no tailcat client.
- **LAN-first endpoint ranking** (`server/internal/link/link.go`, `kindOf` / `rank`: `lan` → `ipv6` → `name` → `ipv4`): this orders the candidates a **computer** tries for the hub, so Persona Link stops making a round trip through the internet when the hub is on its own network. It says nothing about the app's connection.

So when the phone and a computer are on the same Wi-Fi, the traffic still leaves the LAN, reaches the hub, and comes back in.

**Which addresses a client is told about.** `/api/v1/endpoints` is authenticated, and its answer includes the hub's LAN candidates whether or not `public.advertise_lan` is on: that setting controls what is published to the world, not what one of the hub's own paired clients may hear about the network it is already on (`server/internal/hub/endpoints.go`, `endpointScope`). The pairing QR and `health` stay public and never carry LAN addresses. An app that wants to use them ranks them like `link.go` does — LAN first.

Whether an app-to-computer direct path is in scope is an open question: [#2170](https://github.com/nwverse/persona/issues/2170).

## The hub is only a communication center

The hub only handles communication: app and other clients ↔ managers, hub ↔ hub (linked hubs), connections (WebSocket, node / connector links), pairing and auth, the admin UI, push delivery, and various relays.
Business logic lives in the managers (`server/internal/assistant`, `devices`, `extmgr`; see `docs/server/machines.md`, `docs/extensions/extensions.md` ("Who owns what"), `docs/assistant/assistant.md`):
the data for the assistant, Inbox, to-dos, usage limits, board, extensions and devices, plus model calls (triage, titles, summaries, judgments). The hub forwards these endpoints to the managers as-is (`assistantwire.Routes`, `devicewire.Routes`, `extwire.Routes`).

`server/internal/hub/architecture_guard_test.go` pins down what the hub looks like now and fails if it grows in the wrong direction:

- **Routes the hub answers itself** = `testdata/hub_routes_allowed.txt` (registered with literals in the hub package and not in the three forwarding tables above). New business endpoints go in a manager,
  and the route is added to the matching forwarding table; only routes that truly belong to the hub (connections, pairing / auth, admin, push delivery, relays) get a line added to the list by hand. Removing a route means removing its line too (extra lines fail as well).
  While a piece of business logic is being moved into a manager, its line can be marked `# moving to the manager` at the end, and the line is removed once the move is done (usage windows `docs/server/usage-windows.md` ("Who owns this") and the workspace board `docs/web/workspace-board.md` ("Who owns the board") have both been moved).
- **Dependencies**: the hub's dependencies must not include manager packages (`assistant`, `assistantskills`, `devices`, `extmgr`, `extension/host`, `agents`, `mail`, `office`, `feishubot`, `wechat`, `node`, `mcpserver`),
  the knowledge base (`kb`), the connectors (`connector` and everything under it) or the model client (`ai`): the types it carries for them come from the contract packages below.
- **No model calls**: the hub imports nothing of `internal/ai` (a model setting it hands on is an `aiwire.Config`), and no model API addresses appear in its code.
- **Store** methods = `testdata/hub_store_methods.txt`: new business tables go in the manager's own storage, not in `hub.Store`.

The lists are edited by hand, with no auto-rewrite switch: the changed line shows up in the PR diff, so a reviewer can see whether it really belongs to the hub.

## Contract packages

The parts (the hub, the managers, the node on each computer, extensions) talk only through contracts, and the Go packages that define them hold only types, constants and trivial pure helpers:

| Package | What it carries |
| --- | --- |
| `assistantwire`, `devicewire`, `extwire` | The bus between the hub and a manager: methods, events, routes the hub relays, extension scopes and manifest types the bus carries |
| `kbwire` | Knowledge-base entities, identity hints, updates and chat context as they cross the bus and the connector protocol |
| `connectorwire` | Inbox things and replies, conversation batches, posts and snapshots from connectors (`docs/protocol/connector-protocol.md`) |
| `aiwire` | A model setting (`aiwire.Config`) |
| `dialogwire` | A terminal dialog read off a screen, as the node and the apps see it |
| `nodeproto` | The node protocol handshake: the protocol version and capability names a node announces (`docs/protocol/protocol.md` "Node handshake") |
| `proto`, `rpc`, `relayproto` | The client API and node protocol types, the JSON-RPC framing, the push relay |
| `sdk/go/wire` (the SDK's module) | The extension protocol's types generated from its schema (an error report the bus carries, say), shared with extensions written against the SDK |

The business packages alias the contract types so their callers keep the old names (`type InboxThing = connectorwire.InboxThing` in `connector`, `type Update = kbwire.Update` in `kb`).
A contract type cannot carry business methods: logic moves into the business package as a function (`kb`'s `tagUpdate`, `screen`'s `hash`).
Where the business type needs fields of its own, it stays a separate type with the same JSON and a converter (`kb.Item` keeps store bookkeeping; connectors hand over a `kbwire.Item`, and `kb.ItemFromWire` turns it into one; `kb`'s `TestItemMatchesTheContract` keeps the two in step).

`server/internal/archguard` (tests only) keeps it that way:

- **Pure contracts**: every package in `testdata/contracts.txt` depends only on the standard library and other listed packages (the SDK's `sdk/go/wire` is listed by its full import path), and every package named `*wire` must be listed.
- **Part boundaries**: the hub (`hub`, `hubedge`) depends on no manager (`assistant`, `devices`, `extmgr`, `manager`) or node package, a manager on no hub package, and the node (`node`) on neither.
  Known violations would go into `testdata/boundary_allowed.txt` (shrink-only, each line naming the step that removes it); it is empty, and a line that no longer applies fails too.

## Key flows

### Starting a session
1. App: pick a machine → pick a folder (recent projects / browse) → pick a launcher → optional first message.
2. hub → node `session.start`: in the herdr workspace `persona` (created if missing), create a tab (cwd=folder, label=folder name),
   `pane run "<launcher> --session-id <uuid> --mcp-config ~/.persona/mcp.json --plugin-dir ~/.persona/agent-plugin --plugin-dir ~/.persona/claude-hooks --plugin-dir ~/.persona/claude-live-replies"`
   (the last one, the live replies `MessageDisplay` hook, only while the machine's Settings › Claude Code › Live replies is on; older Claude Code: `--settings ~/.persona/claude-settings.json` instead of the plugins).
3. The node waits for the agent to be ready (`agent wait`); if it is blocked (trust folder, etc.), the screen is pushed to the phone as a `screen` interaction. Once ready, the first message is sent.
4. The node starts tailing the transcript, and events are pushed through the hub to subscribed apps.

### Answering questions / approving plans
1. claude calls AskUserQuestion → PreToolUse hook → `personad hook` connects to node.sock and sends an `interaction` → the hub stores it in the interaction queue and pushes it to the app / watch (including a local notification).
2. The user picks on the phone → hub → node → the hook process outputs `updatedInput.answers` → claude continues.
3. On timeout (30 minutes by default), or when no app is online, the hook lets it through right away → the TUI shows it as usual, and it can be answered at the computer too.

### Phone data tools
claude calls the MCP tool `phone_calendar_events` → `personad mcp` → node.sock → hub → finds an online iPhone (WS) → the app queries with EventKit → returns the same way.
When the app is not online, it returns the error "iPhone is not online, please open Persona".

### Runtime list origin projection

`internal/sessionorigin` classifies the runtime DTO's pane/host metadata using the host registry, matching the clients' desktop-first origin rule (`docs/protocol/protocol.md`, "Pane/host origins"). The hub's existing list adapter adds this scalar to bounded rows and passes it to `internal/sessionlist`; the generic index only stores and matches it. State, preferences and Agents membership remain with their existing owners.
