// Manager-owned extension metadata and settings APIs (docs/extensions/extension-settings.md).
// Configuration is schema-backed and secrets are write-only. Lifecycle management is host-owned;
// market installation and consent flows live in extension-market.ts.
import { api, ApiError, enc } from "./api";
import type { Machine } from "./types";
import { placeOf, type UIPlace } from "./extension-ui";
import { extRoot, listOf } from "./extension-api";
import { refreshExtensionPlaces } from "./extension-slots";
import { EXTENSIONS_CHANGED } from "./extension-prompts";
import type { Contribution, DependencyState, PackMember } from "./extension-model";
import type { ExtensionAuth, ExtensionTool } from "./extension-auth";
import { clock } from "./format";
import { itemHeaders } from "./manager-choice";
import { t } from "../i18n";

export type Json = string | number | boolean | null | Json[] | { [k: string]: Json };
export type JsonObject = { [k: string]: Json };

/** One of GET /api/v1/extensions. */
export interface HubExtension {
  /** The manager whose extension it is, in the hub's merged list (docs/protocol/protocol.md "Per-manager routing"); none: the default. */
  manager?: string;
  id: string;
  name: string;
  kind: string;
  /** disabled | needs_setup | incompatible | stopped | starting | running | error */
  status: string;
  error?: string;
  version?: string;
  /** builtin | hub.json | installed */
  source?: string;
  enabled?: boolean;
  description?: string;
  /** The extension points it contributes to, with the ids. */
  contributes?: Record<string, string[]>;
  /** What it still needs before it runs, in words. */
  needs_setup?: string[];
  warnings?: string[];
  /** An extension from a market (a hub with plugin_auto_upgrade): its own auto-upgrade switch, its newer version and what
   *  happened last with an upgrade (docs/extensions/extensions.md "Automatic upgrades"). */
  auto_upgrade?: boolean;
  /** Why its own switch does not apply now: "disabled" while the extension is off (the switch is kept for when it is on). */
  auto_upgrade_paused?: string;
  upgrade?: ExtensionUpgrade;
  upgrade_note?: ExtensionUpgradeNote;
  /** An extension with a program on computers: how that program is on each machine it is switched on for. */
  nodes?: ExtensionNodeState[];
  /** An installed extension's sandbox and its connections through the hub's egress proxy (hub extension_sandbox.go). */
  sandbox?: ExtensionSandbox;
  /** An extension one signs in to (hub extension_auth, #4507): its state as the hub last knew it. */
  auth?: ExtensionAuth;
  /** The command-line programs this version uses. */
  tools?: ExtensionTool[];
  /** An extension running on its own that the hub reaches on its sockets (a `remote:` install): how it is connected. */
  attached?: ExtensionAttached;
}

/** How an attached extension is connected (docs/extensions/extensions.md "Attached extensions"). */
export interface ExtensionAttached {
  online: boolean;
  /** When it came online or went offline; none while it is switched off or nothing needs it. */
  since?: string;
  /** Why it is offline, also while its status is still `starting` (the hub waits 30 seconds before it says `error`). */
  error?: string;
  /** The version the program says it is, when that is not the approved `version`: it is not used until approved. */
  version?: string;
}

/** Whether an installed extension runs isolated, and what it reached through the egress proxy (Android ExtensionSandbox). */
export interface ExtensionSandbox {
  /** It runs in a sandbox, or would: `active` says whether the sandbox is in effect on the hub's machine, `why` why not. */
  enabled: boolean;
  default?: boolean;
  chosen?: boolean;
  active: boolean;
  why?: string;
  trusted?: boolean;
  egress?: { allowed: number; denied: number; last_denied?: string; denied_at?: string };
}

/** The sandbox's state in words: in effect, asked for but not in effect, or off (Android ExtensionModelText.sandbox). */
export function sandboxState(s: ExtensionSandbox): string {
  if (s.active) return t("lib-extensions.sandbox_active");
  return t(s.enabled ? "lib-extensions.sandbox_inactive" : "lib-extensions.sandbox_off");
}

/** What it reached through the egress proxy: "through the egress proxy: N allowed · M refused", null without counts. */
export function sandboxEgress(s: ExtensionSandbox): string | null {
  const e = s.egress;
  if (!e) return null;
  return t("lib-extensions.sandbox_egress", {
    allowed: t("lib-extensions.sandbox_allowed", { count: e.allowed ?? 0 }),
    denied: t("lib-extensions.sandbox_denied", { count: e.denied ?? 0 }),
  });
}

/** The channel an extension contributes (its first `channel` id), whose page is where it is bound. */
export function channelOf(p: Pick<HubExtension, "contributes">): string | null {
  return p.contributes?.channel?.find((id) => id.trim() !== "") ?? null;
}

/** An extension's program on one machine (docs/extensions/extensions.md "Programs that run on computers"). */
export interface ExtensionNodeState {
  machine_id: string;
  version?: string;
  /** running | starting | stopped | error | missing | lease_expired | offline | outdated */
  state: string;
  error?: string;
}

/** A market extension's newer version and where it stands. */
export interface ExtensionUpgrade {
  id: string;
  name?: string;
  market: string;
  from: string;
  to: string;
  /** available | waiting_idle | needs_permission | off | skipped | failed | incompatible */
  state: string;
  added_permissions?: ExtensionPermission[];
  detail?: string;
  /** "user" when a skipped version is one the user rolled back from (absent: it didn't start). */
  skipped_by?: string;
}

/** What happened last with an extension's upgrade. */
export interface ExtensionUpgradeNote {
  /** upgraded | rolled_back | needs_permission | failed */
  kind: string;
  from: string;
  to: string;
  detail?: string;
  at: string;
}

export interface ExtensionChoice {
  value: Json;
  title?: string;
}

/** One field of the form: the hub's reading of the manifest's JSON Schema subset. */
export interface ExtensionField {
  name: string;
  /** string | integer | number | boolean | array | object */
  type: string;
  title?: string;
  description?: string;
  default?: Json;
  required?: boolean;
  enum?: ExtensionChoice[];
  format?: string;
  pattern?: string;
  min_length?: number;
  max_length?: number;
  minimum?: number;
  maximum?: number;
  /** A list's items; format machine: the hub's machines by id (of the systems in os), drawn as a switch each. */
  items?: { type: string; enum?: ExtensionChoice[]; format?: string; os?: string[] };
  min_items?: number;
  max_items?: number;
  unique_items?: boolean;
  /** An object's fields (a group of the form). */
  fields?: ExtensionField[];
  /** x-confirm: what to ask before a switch of it is turned on (the extension cua's "no automatic screen lock while the extension is on"). */
  confirm?: string;
}

/** A declared secret and whether it has a value (never the value). */
export interface ExtensionSecret {
  name: string;
  title: string;
  description?: string;
  required?: boolean;
  set: boolean;
  /** Its last four characters, when it is set and the extension asked for them (secrets[].hint "last4"); never more. */
  hint?: string;
}

/** A line of what an extension may do. */
export interface ExtensionPermission {
  key: string;
  title: string;
  detail?: string;
}

/** GET /api/v1/extensions/{id}/-/: the extension with its form, settings, secrets and permissions. */
export interface HubExtensionDetail extends HubExtension {
  publisher?: string;
  homepage?: string;
  origin?: string;
  previous_version?: string;
  /** hub.json's: switched and set up there only. */
  read_only?: boolean;
  form?: { fields?: ExtensionField[] };
  config?: JsonObject;
  secrets?: ExtensionSecret[];
  permissions?: ExtensionPermission[];
  /** The pages it draws in its settings (placement plugin_page, docs/extensions/extensions.md "Declarative UI"). */
  ui?: UIPlace[];
  /** The field (format machine) that picks the computers its program runs on; their rows say how it is there. */
  node_machines?: string;
  contributions?: Contribution[];
  depends?: DependencyState[];
  extension_pack?: PackMember[];
}

// ── the hub ─────────────────────────────────────────────

const base = (id: string) => `${extRoot()}/${enc(id)}/-`;

type ListAnswer = { extensions?: HubExtension[]; plugins?: HubExtension[]; can_install?: boolean; markets?: boolean };

export const listExtensions = () => api<ListAnswer>(extRoot()).then((r) => listOf(r));
/** The list with what this hub allows: installing at all (a database), and markets (docs/extensions/extensions.md "Extension markets"). */
export const listExtensionsInfo = () =>
  api<ListAnswer>(extRoot()).then((r) => ({
    extensions: listOf(r),
    canInstall: !!r.can_install,
    markets: !!r.markets,
  }));
/** A detail as either kind of hub sends it, its pages' places read with the new keys. */
export const detailOf = (d: HubExtensionDetail): HubExtensionDetail => (d.ui ? { ...d, ui: d.ui.map(placeOf) } : d);
export const extensionDetail = (id: string) => api<HubExtensionDetail>(`${base(id)}/`).then(detailOf);
/** After a change that can start, stop or reconfigure an extension: its entries, cards and sections are asked for again. */
export const changed = <T>(r: T): T => {
  refreshExtensionPlaces();
  if (typeof window !== "undefined") window.dispatchEvent(new Event(EXTENSIONS_CHANGED));
  return r;
};
export const setExtensionEnabled = (id: string, enabled: boolean, cascade = false) =>
  api<HubExtensionDetail>(`${base(id)}/enabled`, { method: "PUT", json: { enabled, ...(cascade ? { cascade } : {}) } }).then(detailOf).then(changed);
export const setContribution = (id: string, point: string, cid: string, enabled: boolean) =>
  api<HubExtensionDetail>(`${base(id)}/contributions/${enc(point)}/${enc(cid)}`, { method: "PUT", json: { enabled } }).then(detailOf).then(changed);
export const setExtensionAutoUpgrade = (id: string, auto: boolean) =>
  api<HubExtensionDetail>(`${base(id)}/auto-upgrade`, { method: "PUT", json: { auto } }).then(detailOf);
/** The auto-upgrade switch for all market extensions; a hub from before it has no route (null). */
export const extensionUpgrades = (manager?: string) =>
  api<{ auto: boolean; upgrades?: ExtensionUpgrade[] }>(`${extRoot()}/-/upgrades`, { headers: itemHeaders(manager) }).then((r) => r.auto, () => null);
export const setExtensionUpgrades = (auto: boolean) => api<{ auto: boolean }>(`${extRoot()}/-/upgrades`, { method: "PUT", json: { auto } }).then((r) => r.auto);
export const setExtensionConfig = (id: string, config: JsonObject) =>
  api<HubExtensionDetail>(`${base(id)}/config`, { method: "PUT", json: { config } }).then(detailOf).then(changed);
export const setExtensionSecret = (id: string, name: string, value: string) =>
  api<HubExtensionDetail>(`${base(id)}/secrets/${enc(name)}`, { method: "PUT", json: { value } }).then(detailOf).then(changed);
export const clearExtensionSecret = (id: string, name: string) =>
  api<HubExtensionDetail>(`${base(id)}/secrets/${enc(name)}`, { method: "DELETE" }).then(detailOf).then(changed);

/** A 400 invalid_config: the field the hub refused (its dotted path, "" when it names none) and why. */
export function configError(e: unknown): { field: string; message: string } | null {
  if (!(e instanceof ApiError) || e.code !== "invalid_config") return null;
  const field = typeof e.body.field === "string" ? e.body.field : "";
  const message = typeof e.body.message === "string" && e.body.message ? e.body.message : e.message;
  return { field, message };
}

/**
 * A save the extension itself turned down (400 check_failed, in its own words), a check asked for too often (429
 * check_rate_limited), or settings changed meanwhile (409 settings_moved): nothing was saved, and the hub's words say
 * why. null for any other error.
 */
export function checkError(e: unknown): string | null {
  if (!(e instanceof ApiError)) return null;
  return e.code === "check_failed" || e.code === "check_rate_limited" || e.code === "settings_moved" ? e.message : null;
}

/**
 * The secrets a refused configuration save deleted anyway (the hub clears the ones bound to the old configuration
 * before it writes the new one, then answers 500 secret_not_cleared or 409 read_only with secrets_cleared); empty
 * when the error names none.
 */
export function clearedSecrets(e: unknown): string[] {
  if (!(e instanceof ApiError) || !Array.isArray(e.body.secrets_cleared)) return [];
  return e.body.secrets_cleared.filter((n): n is string => typeof n === "string" && n !== "");
}

// ── words ───────────────────────────────────────────────

// Running or idle (stopped: started again when something needs it) is all one to the user: it is on.
const STATES: Record<string, string> = {
  running: t("lib-extensions.state_on"),
  stopped: t("lib-extensions.state_on"),
  starting: t("lib-extensions.state_starting"),
  needs_setup: t("lib-extensions.state_needs_setup"),
  disabled: t("lib-extensions.state_disabled"),
  incompatible: t("lib-extensions.state_incompatible"),
  error: t("lib-extensions.state_error"),
};

export const extensionState = (p: HubExtension) => STATES[p.status] ?? t("lib-extensions.state_unknown");

/** Whether an enabled extension's program runs right now (the detail page's small print); "" for any other state. */
export const runNote = (p: HubExtension) =>
  p.attached ? attachedNote(p) : p.status === "running" ? t("lib-extensions.running") : p.status === "stopped" ? t("lib-extensions.idle_note") : "";

/** An attached extension's small print: online or offline since when, and why while it is still reconnecting (its
 *  status says no error yet); "" while it is switched off. */
export function attachedNote(p: Pick<HubExtension, "status" | "attached">): string {
  const a = p.attached;
  if (!a || p.status === "disabled") return "";
  const time = clock(a.since);
  if (!time) return p.status === "stopped" ? t("lib-extensions.attached_idle") : "";
  if (a.online) return t("lib-extensions.attached_online", { time });
  if (p.status === "starting") return a.error ? t("lib-extensions.attached_reconnecting_why", { time, why: a.error }) : t("lib-extensions.attached_reconnecting", { time });
  return t("lib-extensions.attached_offline", { time });
}

/** The version as the detail's line says it: the approved one, and the program's when it says another. */
export function versionText(p: Pick<HubExtension, "version" | "attached">): string | undefined {
  const current = p.attached?.version;
  return current ? t("lib-extensions.version_differs", { approved: p.version ?? "", current }) : p.version;
}

export const isProblem = (p: HubExtension) => p.status === "error" || p.status === "incompatible";

/** The tone of an extension's dot: red broken, orange waiting on the user, green on (running or idle), gray otherwise. */
export function extensionTone(p?: HubExtension | null): "red" | "orange" | "green" | "idle" {
  if (!p) return "idle";
  if (isProblem(p)) return "red";
  if (p.status === "needs_setup") return "orange";
  return p.status === "running" || p.status === "stopped" ? "green" : "idle";
}

export function sourceText(s?: string): string | undefined {
  return s === "builtin" ? t("lib-extensions.source_builtin") : s === "hub.json" ? "manager.json" : s === "installed" ? t("lib-extensions.source_installed") : undefined;
}

export function pointText(name: string): string {
  return name === "connectors" ? t("lib-extensions.point_connectors") : name === "voice" ? t("lib-extensions.point_voice") : name;
}

/** The detail's line under the name: state · version · source. */
/** The "Upgrade" group's line for a newer version: what happens to it (the App's ExtensionText.upgradeDetail). */
export function upgradeDetail(u: ExtensionUpgrade): string {
  switch (u.state) {
    case "needs_permission": return morePermissions(u.added_permissions ?? []);
    case "waiting_idle": return t("lib-extensions.upgrade_waiting_idle");
    case "available": return t("lib-extensions.upgrade_available");
    case "off": return t("lib-extensions.upgrade_off");
    case "skipped":
      return u.skipped_by === "user"
        ? t("lib-extensions.upgrade_skipped_user", { from: u.from ?? "" })
        : t("lib-extensions.upgrade_skipped", { from: u.from ?? "" });
    case "failed": return u.detail ? t("lib-extensions.upgrade_failed_detail", { detail: u.detail }) : t("lib-extensions.upgrade_failed");
    case "incompatible": return u.detail ? t("lib-extensions.upgrade_incompatible_detail", { detail: u.detail }) : t("lib-extensions.upgrade_incompatible");
    default: return "";
  }
}

/** When the line asks something of the user, what its button says (it opens the upgrade in the market). */
export function upgradeAction(u: ExtensionUpgrade): string | null {
  if (u.state === "needs_permission") return t("lib-extensions.action_review");
  return ["off", "skipped", "failed"].includes(u.state) ? t("lib-extensions.action_upgrade") : null;
}

/** What a newer version asks for more, as the hub says it in its notice. */
export function morePermissions(added: ExtensionPermission[]): string {
  if (!added.length) return t("lib-extensions.permissions_changed");
  // small counts in words where the language writes them so (Chinese "two items" in words, not with the digit 2)
  const words = t("lib-extensions.small_counts").split(",");
  const list = added.map((p) => (p.detail ? t("lib-extensions.permission_detail", { title: p.title, detail: p.detail }) : p.title)).join(t("lib-extensions.list_separator"));
  const count = added.length;
  return count <= words.length ? t("lib-extensions.more_permissions_small", { count, n: words[count - 1], list }) : t("lib-extensions.more_permissions", { count, list });
}

/** The last upgrade, when it is news of its own (asking and failing are the newer version's line). */
export function upgradeNote(n: ExtensionUpgradeNote): string | null {
  if (n.kind === "upgraded") return t("lib-extensions.upgraded", { to: n.to, from: n.from });
  if (n.kind === "rolled_back") return t("lib-extensions.rolled_back", { to: n.to, from: n.from });
  return null;
}

export const stateLine = (p: HubExtension) => [extensionState(p), versionText(p), sourceText(p.source)].filter(Boolean).join(" · ");

/** How many, and how many need the user. */
export function listSummary(list?: HubExtension[] | null): string {
  if (!list) return "";
  if (!list.length) return t("lib-extensions.none");
  const attention = list.filter((p) => p.status === "needs_setup" || isProblem(p)).length;
  return attention ? t("lib-extensions.count_attention", { count: list.length, attention }) : t("lib-extensions.count", { count: list.length });
}

export const fieldLabel = (f: ExtensionField) => f.title || f.name;

/** The title with "(required)" when it has to be filled in and has no default to fall back to. */
export const fieldTitle = (f: ExtensionField) => (f.required && f.default === undefined ? t("lib-extensions.field_required", { label: fieldLabel(f) }) : fieldLabel(f));

export const choiceLabel = (c: ExtensionChoice) => c.title || (typeof c.value === "string" ? c.value : JSON.stringify(c.value));

/** What shows when a field is empty: the default it falls back to, or "Not set". */
export function placeholder(f: ExtensionField): string {
  if (f.default !== undefined && f.default !== null) {
    const choice = f.enum?.find((c) => same(c.value, f.default!));
    const text = choice ? choiceLabel(choice) : typeof f.default === "object" ? "" : String(f.default);
    if (text) return t("lib-extensions.default_value", { value: text });
  }
  return t("lib-extensions.not_set");
}

export function secretState(s: ExtensionSecret): string {
  if (s.set) return s.hint ? t("lib-extensions.is_set_hint", { hint: s.hint }) : t("lib-extensions.is_set");
  return s.required ? t("lib-extensions.not_set_required") : t("lib-extensions.not_set");
}

// ── the draft, by path ──────────────────────────────────

export function same(a: Json | undefined, b: Json | undefined): boolean {
  if (a === b) return true;
  if (a === null || b === null || a === undefined || b === undefined || typeof a !== "object" || typeof b !== "object") return false;
  if (Array.isArray(a) || Array.isArray(b)) {
    return Array.isArray(a) && Array.isArray(b) && a.length === b.length && a.every((x, i) => same(x, b[i]));
  }
  const ka = Object.keys(a);
  const kb = Object.keys(b);
  return ka.length === kb.length && ka.every((k) => k in b && same(a[k], b[k]));
}

export function valueAt(draft: JsonObject, path: string[]): Json | undefined {
  let cur: Json | undefined = draft;
  for (const k of path) {
    if (!cur || typeof cur !== "object" || Array.isArray(cur)) return undefined;
    cur = cur[k];
  }
  return cur === null ? undefined : cur;
}

/** Sets (undefined: removes) the value at path; a group left empty goes too. */
export function setAt(draft: JsonObject, path: string[], value: Json | undefined): JsonObject {
  const [key, ...rest] = path;
  if (key === undefined) return draft;
  const out: JsonObject = { ...draft };
  let next: Json | undefined = value;
  if (rest.length) {
    const inner = out[key];
    const obj = inner && typeof inner === "object" && !Array.isArray(inner) ? inner : {};
    const set = setAt(obj, rest, value);
    next = Object.keys(set).length ? set : undefined;
  }
  if (next === undefined) delete out[key];
  else out[key] = next;
  return out;
}

/** Without empty strings and lists the user cleared: "not set" (the schema's default applies). */
export function compact(obj: JsonObject): JsonObject {
  const out: JsonObject = {};
  for (const [k, v] of Object.entries(obj)) {
    if (v === null || v === "" || (Array.isArray(v) && !v.length)) continue;
    if (typeof v === "object" && !Array.isArray(v)) {
      const inner = compact(v);
      if (Object.keys(inner).length) out[k] = inner;
      continue;
    }
    out[k] = v;
  }
  return out;
}

/** A number field as typed: the value it reads as (undefined: empty, not set), or why it doesn't read as one. */
export function readNumber(text: string, integer: boolean): { value?: number; error?: string } {
  const s = text.trim();
  if (!s) return {};
  const n = Number(s);
  if (!Number.isFinite(n)) return { error: t("lib-extensions.not_a_number") };
  if (integer && !Number.isInteger(n)) return { error: t("lib-extensions.not_an_integer") };
  return { value: n };
}

/** Number fields typed into that do not read as one, by dotted path. */
export function numberProblems(typed: Record<string, { text: string; integer: boolean }>): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [k, v] of Object.entries(typed)) {
    const e = readNumber(v.text, v.integer).error;
    if (e) out[k] = e;
  }
  return out;
}

/** Whether the draft differs from what the hub has (both read as sent: compacted). */
export const isDirty = (draft: JsonObject, config: JsonObject | undefined) => !same(compact(draft), compact(config ?? {}));

/** The items of a list field: the draft's, else the default. */
export function listItems(draft: JsonObject, path: string[], f: ExtensionField): string[] {
  const v = valueAt(draft, path) ?? f.default;
  return Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : [];
}

/** A machine on an extension's machine list (format machine): online or not, and for an extension that operates computers how
 *  Cua Driver is there (known once it is turned on). */
export function machineStatus(m: Machine, computers: boolean): string {
  const parts = [m.online ? t("lib-extensions.online") : t("lib-extensions.offline")];
  const c = m.computer;
  // Every Mac's while the extension is on, once its node has said (a never-asked one has neither a verdict nor a note).
  if (computers && m.online && c && (c.ready || c.note)) {
    const missing = c.accessibility === "missing" || c.screen_recording === "missing";
    parts.push(!c.installed ? t("lib-extensions.cua_missing") : !c.daemon ? t("lib-extensions.cua_not_running") : missing ? t("lib-extensions.cua_no_permission") : c.desktop === "locked" ? t("lib-extensions.screen_locked") : c.ready ? t("lib-extensions.cua_ready") : t("lib-extensions.cua_installed"));
  }
  return parts.join(" · ");
}

/** How a plugin's program is on a machine switched on for it, for the machine's row: what the user can make of it. */
export function nodeProgramStatus(s: ExtensionNodeState | undefined): string {
  if (!s) return t("lib-extensions.program_waiting");
  const error = s.error && exitText(s.error);
  switch (s.state) {
    case "running":
      return s.version ? t("lib-extensions.program_running_version", { version: s.version }) : t("lib-extensions.program_running");
    case "starting":
      return t("lib-extensions.program_starting");
    case "missing":
      return t("lib-extensions.program_sending");
    case "offline":
      return t("lib-extensions.program_offline");
    case "lease_expired":
      return t("lib-extensions.program_lease_expired");
    case "outdated":
      return t("lib-extensions.program_outdated");
    case "error":
      return error ? t("lib-extensions.program_error_detail", { error }) : t("lib-extensions.program_error");
    default:
      return error ? t("lib-extensions.state_detail", { state: s.state, error }) : s.state;
  }
}

/** Go's words for how a program ended (exec.ExitError), in the apps' words: exit status 3 → "exit code 3". */
export function exitText(error: string): string {
  return error.replace(/\bexit status (-?\d+)/g, t("lib-extensions.exit_code", { code: "$1" })).replace(/\bsignal: ([a-z ]+)/g, t("lib-extensions.killed_by_signal", { signal: "$1" }));
}

/** Under the form: which switches take effect at once (lists of machines) and that the rest waits for "Save". Nothing
 *  when there is only such switches: the fields say so themselves, nothing waits for "Save". */
export function saveFooter(fields: ExtensionField[]): string | undefined {
  const all = fields.flatMap((f) => (f.type === "object" ? (f.fields ?? []) : [f]));
  const now = all.filter((f) => f.items?.format === "machine").map((f) => t("lib-extensions.quoted", { name: f.title || f.name }));
  if (!now.length) return t("lib-extensions.save_footer");
  if (now.length === all.length) return undefined;
  return t("lib-extensions.save_footer_switches", { names: now.join(t("lib-extensions.list_separator")) });
}
