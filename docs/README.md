# Documentation

[Super rules](SUPER_RULES.md) are the highest-priority repository rules. Read them before acting; changes to that
file require explicit approval from administrator `chyroc` in a GitHub issue before editing or opening a linked PR.

Start with [Getting started](operations/getting-started.md), [configuration](operations/config.md),
and the [architecture](server/architecture.md). The [protocol](protocol/protocol.md) is the client/server contract.

## Modules

| Directory | Scope | Entry points |
| --- | --- | --- |
| [server/](server/) | Hub architecture, managers' infrastructure, mesh, computers and usage | [Server](server/server.md), [mesh](server/mesh.md), [machines](server/machines.md) |
| [apple/](apple/) | iPhone, Apple Watch, Mac and Vision Pro; native design and phone data | [iOS and watch](apple/ios.md), [icons](apple/icons.md), [sidebars](apple/sidebar.md), [Mac](apple/mac.md) (frozen), [design](apple/mac-design.md), [Vision Pro](apple/vision.md) |
| [android/](android/) | Android modules and design system | [Android](android/android.md), [design](android/android-design.md) |
| [web/](web/) | Browser client and workspace board | [Web](web/web.md), [workspace board](web/workspace-board.md) |
| [assistant/](assistant/) | Main chat, managers, knowledge, Inbox, tasks, models, prompts and voice | [Assistant](assistant/assistant.md), [managers](assistant/managers.md), [knowledge](assistant/knowledge.md) |
| [integrations/](integrations/) | Agent runtimes, engines, sessions, launchers and terminal | [Runtime contract](integrations/runtimes.md), [engines](integrations/engines.md), [Claude Code](integrations/claude-integration.md), [Codex](integrations/codex.md) |
| [extensions/](extensions/) | Extension system, data connectors, messaging and channels | [Extensions](extensions/extensions.md), [extension settings](extensions/extension-settings.md), [connectors](extensions/connectors.md), [channels](extensions/channels.md) |
| [protocol/](protocol/) | Wire protocol, connector protocol, states, localization and rolling compatibility | [API](protocol/protocol.md), [connector protocol](protocol/connector-protocol.md), [localization](protocol/i18n.md) |
| [operations/](operations/) | Setup, configuration, network, deployment, testing, releases and collaboration | [Docker](operations/docker.md), [upgrading to the split hub and manager](operations/upgrade.md), [network](operations/network.md), [testing](operations/testing.md), [releases](operations/release.md), [collaboration](operations/collaboration.md) |
| [plans/](plans/) | Proposals with open decisions or unfinished scope, not implementation guarantees | [Extension roadmap](plans/plan-extensions.md), [connector roadmap](plans/plan-connectors.md) |
| [lessons/](lessons/README.md) | Reusable pitfalls and fixes, organized by topic | [Lessons index](lessons/README.md) |
| [archive/](archive/README.md) | Historical measurements, unfinished acceptance boundaries and superseded designs, grouped by module | [Archive index](archive/README.md) |
| [assets/readme/](assets/readme/) | Only the media used by the English and Chinese READMEs | [Generator](../scripts/assets/make_readme_assets.py) |

The complete maintained-guide index remains in [CLAUDE.md](../CLAUDE.md#docs-index).
Archive records are evidence from their stated date, not current instructions. Follow the maintained module guide when they disagree.
Issue and PR numbers in lines written up to 2026-11-07 (check `git blame`) refer to the previous tracker, not this one ([collaboration](operations/collaboration.md)).

## Keeping this tree small

- Put a maintained guide in its module directory. Update repository-wide references and the index whenever it moves.
- Merge durable findings into the guide or a lesson. Keep a historical report only when it preserves unique measurements,
  unresolved acceptance limits, or decisions not recorded elsewhere; group it under `archive/<module>/`.
- Do not commit development screenshots, recordings, before/after mockups, or capture-only indexes.
  Save verification output outside the checkout and attach evidence to the relevant PR.
- Only README, app, or website production media belongs in git. App and website resources stay in their owning module;
  this documentation tree retains only the README assets. Interactive design prototypes are text, live beside their
  module guide (or in its archive), and do not depend on stored verification images.
- Run `python3 scripts/test/test_doc_paths.py`, `python3 scripts/test/test_doc_go_paths.py`, and
  `python3 scripts/test/test_readme_languages.py` after changing document or asset paths.
