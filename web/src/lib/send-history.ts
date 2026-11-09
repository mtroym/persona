// What was sent from the composer, per conversation, for ↑/↓ recall and the Ctrl+R search (issue #2434; Moshi keeps the
// last 100 sent messages of each session and searches the ones of all of them). Stored on this browser only, never
// sent anywhere. Everything here is pure or takes the storage as an argument, so it is tested without a DOM.

export const HISTORY_MAX = 100;
export const SEARCH_MAX = 100;
const PREFIX = "persona.sent.";

export interface SentEntry {
  /** The message as it was typed. */
  text: string;
  /** When it was sent (ms): orders entries of different conversations in the search. */
  at: number;
}

type Store = Pick<Storage, "getItem" | "setItem" | "length" | "key">;

/** localStorage, or nothing where the browser has none (a private mode that throws, a test without a DOM). */
function browserStore(): Store | undefined {
  try {
    return typeof localStorage === "undefined" ? undefined : localStorage;
  } catch {
    return undefined;
  }
}

const storageKey = (draftKey: string) => PREFIX + draftKey;

function parse(raw: string | null): SentEntry[] {
  if (!raw) return [];
  try {
    const v: unknown = JSON.parse(raw);
    if (!Array.isArray(v)) return [];
    return v
      .filter((e): e is SentEntry => Boolean(e) && typeof (e as SentEntry).text === "string" && typeof (e as SentEntry).at === "number" && (e as SentEntry).text.trim() !== "")
      .slice(-HISTORY_MAX);
  } catch {
    return [];
  }
}

/** The conversation's sent messages, oldest first. */
export function readHistory(draftKey: string, store: Store | undefined = browserStore()): SentEntry[] {
  try {
    return store ? parse(store.getItem(storageKey(draftKey))) : [];
  } catch {
    return [];
  }
}

/** The list after sending `text`: blank messages are not kept, an earlier copy of the same text moves to the end
 *  (recalling it once is enough), and only the last HISTORY_MAX stay. */
export function withSent(list: SentEntry[], text: string, at: number): SentEntry[] {
  if (!text.trim()) return list;
  return [...list.filter((e) => e.text !== text), { text, at }].slice(-HISTORY_MAX);
}

/** Remembers a message that went out. A full or blocked storage loses the entry, never the send. */
export function recordSent(draftKey: string, text: string, store: Store | undefined = browserStore(), at: number = Date.now()): SentEntry[] {
  const next = withSent(readHistory(draftKey, store), text, at);
  try {
    store?.setItem(storageKey(draftKey), JSON.stringify(next));
  } catch {
    /* storage full or disabled */
  }
  return next;
}

/** Every conversation's sent messages, newest first: the candidates of the search. */
export function readAllHistory(store: Store | undefined = browserStore()): SentEntry[] {
  const all: SentEntry[] = [];
  try {
    if (store) for (let i = 0; i < store.length; i++) {
      const k = store.key(i);
      if (k?.startsWith(PREFIX)) all.push(...parse(store.getItem(k)));
    }
  } catch {
    /* unreadable storage: no candidates */
  }
  return all.sort((a, b) => b.at - a.at);
}

/** The search: the query is split on spaces and every word must be in the message (case-insensitive); newest first,
 *  one row per text, at most `max`. An empty query lists the newest. */
export function searchSent(candidates: SentEntry[], query: string, max: number = SEARCH_MAX): string[] {
  const words = query.toLowerCase().split(" ").filter(Boolean);
  const seen = new Set<string>();
  const out: string[] = [];
  for (const e of candidates) {
    if (seen.has(e.text)) continue;
    const text = e.text.toLowerCase();
    if (!words.every((w) => text.includes(w))) continue;
    seen.add(e.text);
    out.push(e.text);
    if (out.length >= max) break;
  }
  return out;
}

// ── ↑ / ↓ recall ─────────────────────────────────────────────────────────────

/** Whether ↑ / ↓ walk the history: the box is empty or still shows the message it recalled. A multi-line message
 *  keeps the arrows for its own lines until the caret is on the first (↑) or the last (↓) one. */
export function canRecall(o: { draft: string; shown: string | null; dir: "up" | "down"; firstLine: boolean; lastLine: boolean }): boolean {
  if (o.draft === "") return true;
  if (o.shown === null || o.draft !== o.shown) return false;
  return o.dir === "up" ? o.firstLine : o.lastLine;
}

/** One step: ↑ goes to an older message (the newest from an empty box, stopping at the oldest), ↓ to a newer one and
 *  past the newest back to an empty box. `index` -1 is the empty box. */
export function recallStep(length: number, index: number, dir: "up" | "down"): number {
  if (length === 0) return -1;
  if (dir === "up") return index < 0 ? length - 1 : Math.max(0, index - 1);
  if (index < 0) return -1;
  return index + 1 >= length ? -1 : index + 1;
}

// ── keys ─────────────────────────────────────────────────────────────────────

export interface KeyLike {
  key: string;
  ctrlKey: boolean;
  metaKey: boolean;
  altKey: boolean;
  shiftKey: boolean;
  isComposing?: boolean;
  keyCode?: number;
}

/** An input method is composing (the Enter or arrow that picks a candidate is not ours). */
export const composing = (e: Pick<KeyLike, "isComposing" | "keyCode">): boolean => Boolean(e.isComposing) || e.keyCode === 229;

const plainCtrl = (e: KeyLike, k: string) => e.ctrlKey && !e.metaKey && !e.altKey && !e.shiftKey && e.key.toLowerCase() === k;

/** Ctrl+R in the box opens the search (⌃ on a Mac too: ⌘R stays the browser's reload). */
export function opensSearch(e: KeyLike): boolean {
  return !composing(e) && plainCtrl(e, "r");
}

export type SearchAction = "up" | "down" | "pick" | "cancel";

/** The search panel's keys: ↑ ↓ and Emacs' Ctrl+R / Ctrl+P (up) and Ctrl+S / Ctrl+N (down) move, Enter or Tab picks,
 *  Esc or Ctrl+G cancels. null: the key is the search box's own (typing). */
export function searchAction(e: KeyLike): SearchAction | null {
  if (composing(e)) return null;
  const bare = !e.ctrlKey && !e.metaKey && !e.altKey && !e.shiftKey;
  if (bare && e.key === "ArrowUp") return "up";
  if (bare && e.key === "ArrowDown") return "down";
  if (bare && (e.key === "Enter" || e.key === "Tab")) return "pick";
  if (bare && e.key === "Escape") return "cancel";
  if (plainCtrl(e, "r") || plainCtrl(e, "p")) return "up";
  if (plainCtrl(e, "s") || plainCtrl(e, "n")) return "down";
  if (plainCtrl(e, "g")) return "cancel";
  return null;
}

/** The highlighted row after a move: ↑ goes toward the top row (the newest) and wraps to the last. */
export function moveActive(active: number, count: number, action: "up" | "down"): number {
  if (count <= 0) return 0;
  return action === "up" ? (active - 1 + count) % count : (active + 1) % count;
}
