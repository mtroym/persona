import type { AgentState, Interaction, Session, SessionCapabilities } from "./types";
import { formatDate, formatRelative, t } from "../i18n";
import { usageLimitLabel } from "./usage-limit";
import { workLine } from "./work";

// Missing session metadata is not permission to write; legacy sources retain their existing behavior.
export function sessionCan(s: Session | undefined, operation: keyof SessionCapabilities): boolean {
  if (!s) return false;
  if (operation === "attach") return Boolean(s.source_kind && s.capabilities?.attach === true && !s.attached);
  return !s.source_kind || s.capabilities?.[operation] === true;
}

/** A script request explicitly outside a session has no session-source approval
 *  policy. A nonempty session id still requires its real session metadata. */
export function interactionCanAnswer(ix: Pick<Interaction, "session_id">, session: Session | undefined): boolean {
  return ix.session_id === "" || sessionCan(session, "approve");
}

export function sourceSubtitle(s?: Session): string {
  if (!s?.source_kind) return "";
  const label = s.source_kind === "codex_app_server" ? t("lib-format.source_codex_live") : s.source_kind === "codex_history" ? t("lib-format.source_codex_history") : s.source_kind === "identity_conflict" ? t("lib-format.source_identity_conflict")
    : s.source_kind === "claude_bg" ? t("lib-format.source_claude_background")
    // Claude outside Persona's terminals (docs/integrations/claude-integration.md "Claude outside Persona's terminals")
    : s.source_kind === "claude_desktop" ? t("lib-format.source_claude_desktop")
    : s.source_kind === "claude_cli" ? t("lib-format.source_claude_terminal")
    : s.source_kind === "runtime" ? s.agent_label || s.agent || "runtime" // an agent runtime's session (docs/integrations/runtimes.md): its own name
    : s.source_kind;
  const detail = s.source_kind !== "identity_conflict" && s.agent_name ? ` · ${s.agent_name}` : "";
  return sessionCan(s, "send") ? label + detail : t("lib-format.read_only", { source: label + detail, reason: s.read_only_reason || t("lib-format.read_only_default_reason") });
}

export function resumeCommand(s: Session): string {
  const codex = s.agent === "codex" || s.source_kind === "codex_app_server" || s.source_kind === "codex_history";
  const command = codex ? "codex resume" : s.agent === "traex" ? "traex resume" : "claude --resume";
  const resume = `${command} ${s.source_thread_id || s.session_id}`;
  // Without a folder `cd  && …` would resume in the home folder instead.
  return s.cwd ? `cd ${shellPath(s.cwd)} && ${resume}` : resume;
}

/** A path as one shell word: as is when it is plain, else single-quoted (as the Mac's resumeLine does), so a space,
 * `;` or `$(…)` in it stays part of the path. A leading `~` / `~/` stays outside the quotes so it still expands. */
export function shellPath(path: string): string {
  const home = path === "~" ? "~" : path.startsWith("~/") ? "~/" : "";
  const rest = path.slice(home.length);
  return !rest || /^[\w@%+=:,./-]+$/.test(rest) ? path : `${home}'${rest.replace(/'/g, `'\\''`)}'`;
}

export function sessionMenuActions(s: Session): string[] {
  return [
    ...(sessionCan(s, "attach") ? ["attach"] : []),
    ...(stateOf(s) === "working" && sessionCan(s, "interrupt") ? ["stop"] : []),
    ...(sessionCan(s, "terminal") ? ["resume", ...(!s.assistant && stateOf(s) !== "offline" ? ["close"] : [])] : []),
  ];
}

// "Delete chat" only takes a conversation out of the list (the hub notes it; the transcript stays) until it is used again:
// the statuses the hub keeps out of the lists (hub/deleted_sessions.go stillDeleted), not the assistant's (they archive)
// and not one still starting (issue #801). Idle (finished and seen) too: it stays hidden until it works again.
const DELETABLE_STATUSES = new Set(["offline", "done", "idle", "unknown", "interrupted"]);

export function sessionDeletable(s: Session): boolean {
  return !s.assistant && DELETABLE_STATUSES.has(s.status) && !s.starting;
}

// docs/protocol/agent-states.md: needs you → unread → awaiting reply (seen, awaiting the user's reply, #5096) → working → idle → unknown → offline
const RANK: Record<AgentState, number> = { needs_you: 0, unread: 1, working: 3, idle: 4, unknown: 5, offline: 6 };

// A seen agent awaiting the user's reply ranks between unread and working. The comparator has no mutes: a muted one
// awaiting a reply ranks so here too (the sidebar's By Status sections and folds go by lib/agent-groups.ts awaitsReply).
const rankOf = (s: Session) => (stateOf(s) === "idle" && s.awaits_reply ? 2 : RANK[stateOf(s)]);

export function stateOf(s: Session): AgentState {
  if (s.state) return s.state;
  if (s.status === "offline") return "offline";
  if (s.status === "working") return "working";
  if (s.status === "blocked" || (s.pending_interactions ?? 0) > 0) return "needs_you";
  return "idle";
}

/** Working only because its background commands wait (#6045, Session.background_waiting): its own state is still
 *  "working" (older apps show that); the web shows it as "Waiting in the background", apart from Working. */
export function backgroundWaiting(s: Session): boolean {
  return stateOf(s) === "working" && Boolean(s.background_waiting);
}

export function stateColor(st: AgentState): string {
  switch (st) {
    case "needs_you":
      return "var(--status-needs)";
    case "working":
      return "var(--status-working)";
    case "unread":
      return "var(--status-unread)";
    case "offline":
      return "var(--status-offline)";
    default:
      return "var(--status-idle)";
  }
}

export function byAttention(a: Session, b: Session): number {
  const d = rankOf(a) - rankOf(b);
  if (d) return d;
  const ta = stateOf(a) === "working" ? a.state_since || a.updated_at : a.updated_at;
  const tb = stateOf(b) === "working" ? b.state_since || b.updated_at : b.updated_at;
  return (tb ?? "").localeCompare(ta ?? "") || a.session_id.localeCompare(b.session_id) || a.machine_id.localeCompare(b.machine_id);
}

/** Why it waits for you, in its host's words (a botmux attention), while it needs you; a reason left over from before
 *  (offline, seen) stays out. */
export function attentionReason(s: Session): string | undefined {
  const reason = s.attention_reason?.trim();
  return reason && stateOf(s) === "needs_you" ? reason : undefined;
}

/** The line a list row shows for it: why it waits for you when its host says so, else why its host stopped it on an
 *  error, else where its work stands when the hub triaged its turn (docs/assistant/agent-work.md), else its last words (iOS
 *  listPreview). */
export function sessionPreview(s: Session): string {
  return attentionReason(s) ?? failureReason(s) ?? usageLimitLabel(s) ?? workLine(s) ?? s.last_text ?? "";
}

/** A failure notice without the warning sign botmux starts it with: the page draws its own (iOS Session.failureText). */
export function failureText(reason: string): string {
  return reason.trim().replace(/^[\u26A0\uFE0F\s]+/u, "").trim();
}

/** Why the host stopped the session's turn on an error it will not retry by itself (failure_reason), while the
 *  session is not working (iOS listPreview, FailureNote); undefined otherwise. */
export function failureReason(s: Session | undefined): string | undefined {
  if (!s?.failure_reason || stateOf(s) === "working") return undefined;
  return failureText(s.failure_reason) || undefined;
}

/** "Open in Lark": the session's place outside Persona, a web (https) link only (iOS externalLink). */
export function externalLink(s?: Pick<Session, "external_url">): string | undefined {
  const raw = s?.external_url?.trim();
  if (!raw) return undefined;
  try {
    return new URL(raw).protocol === "https:" ? raw : undefined;
  } catch {
    return undefined;
  }
}

export function sessionTitle(s: Pick<Session, "title" | "cwd">): string {
  return s.title?.trim() || baseName(s.cwd) || t("lib-format.untitled_session");
}

export function baseName(p?: string): string {
  if (!p) return "";
  const parts = p.replace(/\/+$/, "").split("/");
  return parts[parts.length - 1] || p;
}

export function tabLabel(s: Session): string {
  const n = s.slot_name?.trim();
  if (!n) return s.slot_index ? `#${s.slot_index}` : "";
  return /^\d+$/.test(n) ? `#${n}` : n;
}

export function shortPath(p?: string): string {
  if (!p) return "";
  return p.replace(/^\/(Users|home|data\d*\/home)\/[^/]+/, "~");
}

export function ago(iso?: string, now = Date.now()): string {
  if (!iso) return "";
  const ts = Date.parse(iso);
  if (!Number.isFinite(ts) || ts < 86400_000) return "";
  const s = Math.round((now - ts) / 1000);
  // "Just now" rather than Intl's "now" for under a minute
  if (s < 45) return t("lib-format.just_now");
  if (s < 3600) return formatRelative(-Math.round(s / 60), "minute");
  if (s < 86400) return formatRelative(-Math.round(s / 3600), "hour");
  // Days by the local calendar, not by 24 h spans: yesterday morning seen late tonight is yesterday, not the day before yesterday (#4281).
  const days = calendarDays(ts, now);
  if (days < 7) return formatRelative(-days, "day");
  return formatDate(ts, { month: "numeric", day: "numeric" });
}

/** How many local midnights lie between `from` and `to` (rounded: a DST day is 23 or 25 hours). */
function calendarDays(from: number, to: number): number {
  const midnight = (ms: number) => {
    const d = new Date(ms);
    d.setHours(0, 0, 0, 0);
    return d.getTime();
  };
  return Math.round((midnight(to) - midnight(from)) / 86400_000);
}

export function duration(ms: number): string {
  const s = Math.max(0, Math.round(ms / 1000));
  if (s < 60) return t("lib-format.seconds", { s });
  const m = Math.floor(s / 60);
  if (m < 60) return s % 60 ? t("lib-format.minutes_seconds", { m, s: s % 60 }) : t("lib-format.minutes", { m });
  const h = Math.floor(m / 60);
  return m % 60 ? t("lib-format.hours_minutes", { h, m: m % 60 }) : t("lib-format.hours", { h });
}

// For a running timer in a list: minutes are enough once past one.
export function shortDuration(ms: number): string {
  const s = Math.max(0, Math.round(ms / 1000));
  if (s < 60) return t("lib-format.seconds", { s });
  const m = Math.floor(s / 60);
  if (m < 60) return t("lib-format.minutes", { m });
  return m % 60 ? t("lib-format.hours_minutes", { h: Math.floor(m / 60), m: m % 60 }) : t("lib-format.hours", { h: m / 60 });
}

export function clock(iso?: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  const today = new Date();
  const hm = `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
  if (d.toDateString() === today.toDateString()) return hm;
  return t("lib-format.date_time", { date: formatDate(d, { month: "short", day: "numeric" }), time: hm });
}

export function bytes(n?: number): string {
  if (n === undefined || !Number.isFinite(n)) return "—";
  const u = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  while (n >= 1024 && i < u.length - 1) {
    n /= 1024;
    i++;
  }
  return `${n >= 100 || i === 0 ? Math.round(n) : n.toFixed(1)} ${u[i]}`;
}

/**
 * A random (v4) UUID. crypto.randomUUID only exists in a secure context (HTTPS, localhost): a hub opened over plain
 * http gets one from getRandomValues, which is everywhere. The hub takes only UUIDs as op ids (X-Persona-Op-Id, #1651).
 */
export function uuid(): string {
  if (typeof crypto.randomUUID === "function") return crypto.randomUUID();
  const b = crypto.getRandomValues(new Uint8Array(16));
  b[6] = (b[6] & 0x0f) | 0x40;
  b[8] = (b[8] & 0x3f) | 0x80;
  const h = Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`;
}

/** Whether s is a UUID as the hub checks op ids. */
export function isUUID(s: string): boolean {
  return /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(s);
}
