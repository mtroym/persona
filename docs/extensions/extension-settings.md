# Extension settings: the host-owned layout

This is the shared interaction contract for extension settings in Persona. Web implements it first; Apple and Android clients should follow the same order and behavior in their own design systems. This document does not introduce a new visual style.

## Ownership

The host renders every extension's settings page, including built-in, configured, attached and market-installed extensions. An extension supplies only its declared configuration fields, setup/authentication flow and capability-specific configuration. It cannot replace, rename, reorder or implement the host's enablement, information, update or removal controls. A custom extension page is not a substitute for this settings page.

Lifecycle writes use the extension's owning manager and extension ID together. Two managers may have an extension with the same ID; selecting one must never read or change the other.

## Sections, in order

1. **Enabled** — the first control, before setup and configuration.
2. **Extension configuration** — setup/sign-in, declared settings, secrets and capability-specific controls, followed by that configuration's Save action. If there are no configurable fields, show **No configuration required**.
3. **Information** — a read-only identity and provenance summary.
4. **Updates** — update availability, manual checks/review, automatic upgrades and rollback history.
5. **Danger zone** — uninstall, last on the page, with explicit confirmation.

Daily controls come first. Read-only identity precedes maintenance; removing an extension is deliberately separated from configuration and update actions. Use the existing Settings cards, typography, spacing and semantic colors. Headings, controls and confirmations are localized in English, Simplified Chinese and Japanese. Button labels remain on one line; narrow layouts may stack rows, not truncate the explanation of an unavailable action.

Unsupported lifecycle capabilities are **visible but disabled, with a reason**. Do not silently omit the Updates or Danger zone sections. A temporary offline/busy state is different from an unsupported capability and must not change the page's section order.

## Standard controls and text

Canonical English wording is listed below; each client localizes it through its resources, not through extension-provided text.

### Enabled

- Label: **Enabled**. The switch reflects the manager's persisted preference, not runtime health.
- Explanation: **When off, this extension's capabilities are unavailable. Your configuration is kept.**
- A missing required setting or secret appears as **Needs setup**, not as a second enable switch. Configuration remains editable while disabled.
- While changing enablement, disable repeat submissions. After success, refresh the manager's canonical state; after failure, retain the previous value and show an actionable error.
- For packs, show the host's explanation of member behavior and a confirmation when changing members. Never imply disabling the pack itself has changed each member unless the manager confirms it.

### Extension configuration

- Label: **Extension configuration**.
- Authentication, a connection test, choosing a computer and additional module-specific controls belong here. These are not lifecycle controls.
- Save applies only configuration; toggling Enabled or Auto-upgrade must not submit a dirty configuration form.
- Never return a stored secret to the client. Show its saved state, provide an empty replacement input and preserve it when left unchanged. Configuration validation and authentication errors stay inside this section.
- A configuration check or sign-in action is distinct from **Check for updates**.

### Information

Show rows in this order:

| Label | Value / boundary |
| --- | --- |
| **Version** | Current installed version; **Not provided** when missing. Do not infer a version from a repository URL. |
| **Extension ID** | Stable machine-readable extension ID, selectable for support. |
| **Source** | **Built-in**, **Configured (manager.json)**, **Attached**, **Development**, or **Installed** according to manager-owned metadata. |
| **Runtime** | Declared runtime kind, or **Not provided**. Runtime connectivity is a separate status, not Enabled. |
| **Installed from** | Host-provided installation provenance when available; **Not provided** otherwise. Never expose local installation directories or credentials as provenance. |

The page title and description come from the localized extension manifest; the information labels and source classifications come from the host. Manager identity, when needed to disambiguate copies, is host chrome, not extension content. Optional **Publisher**, **Command-line tools**, **Homepage** and **Warning** rows follow the required identity rows when that metadata is present. Homepages must be valid HTTPS links; warnings retain the manager's explanation.

Keep the host's existing contribution switches, isolation status, declared permissions, dependencies and pack-member details. These form an advanced, host-owned part of Information after the identity rows; extensions cannot supply or override their controls. They must not interrupt the extension configuration form or appear after the Danger zone. Disclosure sections may reduce visual noise, but do not hide a failed isolation state or a dependency blocker.

### Updates

- Heading: **Updates**.
- Show the current update status first, then **Check for updates**, any available **Review** / **Upgrade** action, **Auto-upgrade**, and rollback/history last.
- **Check for updates** refreshes the owning manager's available updates. Keep the current page and configuration drafts. It never installs anything.
- **Upgrade** previews the new version and permissions before installation. A version needing consent uses **Review** and cannot be auto-approved. Cancellation does not change the installed version.
- **Auto-upgrade** is the saved per-extension preference, not a claim that an upgrade is currently running. Explain that compatible updates run automatically and permission changes require review.
- While the extension is disabled, retain its saved Auto-upgrade value and show **Auto-upgrade is paused while this extension is off.** A global pause must also be distinguished from the per-extension preference.
- Built-in reason: **Built-in extensions are updated with Persona.** Disable checking, auto-upgrade and upgrade actions.
- Configured/attached/non-market reason: **This extension is not managed by an extension market. Update it at its source.** Disable market update actions. An attached process stays externally managed; turning it off does not uninstall that process.
- A removed/unavailable market is not an up-to-date result. Explain that the source cannot be checked, retain the saved preference and expose the failure/retry without claiming success.
- While checking/installing/rolling back, show the corresponding progress and prevent duplicate writes. Keep the last known version until completion is confirmed. Failures and lost operations show an error and a recovery path rather than a permanent spinner.
- Rollback appears only when there is a retained older version; absence of a rollback version is not an unavailable standard action that needs an empty button. Show the prior version and require confirmation through the existing preview flow.

### Danger zone

- Heading: **Danger zone**. Label and action: **Uninstall**.
- Built-in reason: **Built-in extensions cannot be uninstalled. Turn the extension off instead.**
- Configured/attached reason: **This extension is managed outside the extension market. Remove it at its source.**
- Unsupported Uninstall is visible and disabled with the relevant reason. A missing market does not prevent removing an otherwise removable local installation.
- For supported installations, preview the manager's dependency/blocker report before confirmation. Blocked removal explains what depends on this extension; it must not proceed.
- Confirmation identifies the extension and version. Default: **keep extension data**. The **Keep extension data** checkbox starts on; turning it off is the explicit choice to delete extension data. Cancel sends no uninstall request.
- Packs offer **Uninstall together** only when the manager supports removing members; explain the member set and dependency blockers. Do not silently remove unrelated or externally managed extensions.
- Navigate back to the extension list only after confirmed success. On failure stay on the page with configuration and errors intact.

## Web implementation and API boundary

`web/src/views/settings/ExtensionSettingsPage.tsx` owns the five standard sections and lifecycle controls. Every extension-detail route in `web/src/views/settings/Extensions.tsx` renders it; authentication, schema fields, secrets and custom-page links are passed only into its configuration area. Host-rendered contribution, isolation and permission details follow the identity summary. The market's package-preview page is an installation/consent workflow, not another extension settings page; connector and channel settings retain their separate business switches.

No new server fields are needed for this web implementation: the established `source`, `origin`, `read_only`, `attached`, `auto_upgrade`, `auto_upgrade_paused`, `upgrade` and `previous_version` responses already distinguish the supported full-rights market operations. `origin` is manager-owned installation metadata, not a manifest setting. Web does not offer owner-only local/attached management actions or infer privileges from an arbitrary source URL. Global automatic-upgrade state and manual catalog refresh explicitly target the same manager as the extension. Actual authorization remains enforced by the existing manager endpoints.

## Compatibility and authority

Version, provenance, builtin/configured/attached classification, available update and local installation state are read from manager-owned extension responses. The client must not let a manifest setting called `enabled`, `version` or `auto_upgrade` replace these standard controls: such a field is an extension-specific setting only, scoped under Extension configuration.

Clients talking to older managers must fail closed for lifecycle actions whose capability is unknown: show them disabled with **This manager does not provide this action.** Do not invent a request from a displayed source URL. The host may derive support from the established installed/builtin/market metadata when that contract is explicit, but an extension cannot grant itself uninstall or upgrade authority.

Offline managers retain their last-known information. Show the offline explanation and disable lifecycle writes and connection-dependent checks, not the entire page. Do not wipe or present an empty settings form as authoritative if settings could not be read. Retry uses the same manager-bound read operation.

## Inventory before the web unification

All settings pages listed by the live hub were opened before implementation: built-in computer control, browser control, messages, voice and messaging bot; market-installed connectors, community/monitoring integrations and runtime integrations; a locally installed example and quota integrations. Private extension names, configuration values and provenance are intentionally excluded from this public inventory.

| Kind | Previous standard items and placement | Inconsistency addressed |
| --- | --- | --- |
| Built-in with configuration | Enabled, Settings, then Version / Source / Runtime | Updates and removal absent, with no common reason. |
| Built-in without fields | Enabled, then information | No explicit empty configuration section. |
| Market-installed | Enabled, Upgrades / Auto-upgrade / Review or Upgrade / Rollback, Settings, information, Actions / Remove | Maintenance interrupts setup; removal uses a generic Actions heading and a different verb. Manual checks are detached from individual settings. |
| Installed outside a market | Enabled, optional Settings, information, optional Actions | No consistent update capability explanation; removal depends on local installation metadata. |
| Attached / configured | Enabled, optional Settings, information | Externally managed lifecycle is not consistently explained. |

The web implementation must have one shared settings shell. Tests cover its order, supported and unsupported actions, no-field extensions, manager identity, failures and uninstall data defaults. Browser acceptance opens at least three different real extension pages in light and dark mode after deployment; screenshots must be cropped or redacted to remove accounts, conversations, computer names, installation URLs and secrets before being posted publicly.
