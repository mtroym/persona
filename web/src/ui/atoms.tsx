import { useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { t } from "../i18n";
import type { AgentState } from "../lib/types";
import { Icon, type IconName } from "./icons";
import { StatusMark } from "./StatusMark";
import { collapseMounted } from "../lib/disclosure";
import { FeedbackGate, offlineNow } from "../lib/feedback";
import { useCloseOnBack } from "../lib/back-close";
import { BUSY_MOTION, spinStyle } from "../lib/busy-motion";
import { composing } from "../lib/send-history";
import { keyboardFrame } from "../lib/viewport";
import { prefersReducedMotion } from "./stream/motion";
import { accelLabel, activation, edge, enabled, keyIntent, renameCommit, rowLabel, type MenuItem } from "./menu-model";
import { NARROW_QUERY, placeMenu, placeSubmenu, SHEET_QUERY, useEscapeClose, useMedia, useModalFocus, type MenuPlace } from "./overlay";
import { STATE_LABEL, stateName } from "../lib/state-labels";

export const EASE = "cubic-bezier(0.23, 1, 0.32, 1)";

/** Spread on a field holding a secret that is not this site's password (a key, a computer's password): no spell check,
 *  and the 1Password, LastPass and Bitwarden extensions leave it alone. The field sets its own `autoComplete`. */
export const SECRET_INPUT = { spellCheck: false, "data-1p-ignore": true, "data-lpignore": "true", "data-bwignore": true } as const;

export function cx(...c: (string | false | null | undefined)[]) {
  return c.filter(Boolean).join(" ");
}

// A light sweep across a label: work in progress.
export function Shimmer({ children, className = "" }: { children: React.ReactNode; className?: string }) {
  return (
    <span
      className={`bg-clip-text text-transparent ${BUSY_MOTION} ${className}`}
      style={{
        backgroundImage: "linear-gradient(90deg, var(--ink-3) 35%, var(--ink) 50%, var(--ink-3) 65%)",
        backgroundSize: "200% 100%",
        animation: "shimmer-text 1.4s linear infinite",
      }}
    >
      {children}
    </span>
  );
}

const CHEVRON = Array.from({ length: 9 }, (_, i) => {
  const r = Math.floor(i / 3),
    c = i % 3;
  return (c + Math.abs(r - 1)) * 90;
});

// Beautiful UI's pixel-grid loader (the "Drive" variant).
export function PixelGrid({ className = "" }: { className?: string }) {
  return (
    <span aria-hidden className={`grid shrink-0 grid-cols-[repeat(3,4px)] gap-[1.5px] ${className}`}>
      {CHEVRON.map((d, i) => (
        <span key={i} className={`size-[4px] rounded-[1px] bg-ink ${BUSY_MOTION}`} style={{ opacity: 0.15, animation: `pixel-on 650ms ease-in-out ${d}ms infinite` }} />
      ))}
    </span>
  );
}

export function useNow(every = 1000, on = true) {
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    if (!on) return;
    const t = setInterval(() => setNow(Date.now()), every);
    return () => clearInterval(t);
  }, [every, on]);
  return now;
}

// role=status: a polite live region, so a screen reader reads the label when the loader appears
export function Loading({ label = t("common.loading") }: { label?: string }) {
  return (
    <div role="status" className="flex items-center gap-2.5">
      <PixelGrid />
      <Shimmer className="text-[13px] font-medium">{label}</Shimmer>
    </div>
  );
}

/** A state as a word in a pill; `awaiting`: seen and awaiting the user's reply, a quiet pending mark (#5096). */
export function StatePill({ state, awaiting = false, waiting = false }: { state: AgentState; awaiting?: boolean; /** working only on background commands that wait (#6045) */ waiting?: boolean }) {
  const tone: Record<AgentState, string> = {
    needs_you: "bg-field text-attention",
    working: "bg-field text-orange",
    unread: "bg-green-tint text-green",
    idle: "bg-field text-ink-2",
    offline: "bg-field text-ink-3",
    unknown: "bg-field text-ink-3",
  };
  const reply = awaiting && state === "idle";
  const bg = waiting && state === "working";
  return (
    <span className={`inline-flex h-5 shrink-0 items-center gap-1.5 whitespace-nowrap rounded-full px-2 text-[11px] font-medium ${reply ? "bg-field text-green" : bg ? "bg-field text-ink-2" : tone[state]}`} data-bg-waiting={bg || undefined}>
      {(state === "working" || state === "needs_you") && <StatusMark state={state} size="sm" label={false} waiting={bg} />}
      {reply ? stateName("awaits_reply") : bg ? stateName("bg_waiting") : STATE_LABEL[state]}
    </span>
  );
}

export function IconButton({
  icon,
  label,
  onClick,
  active,
  size = 28,
  className = "",
  disabled,
  iconSize = 15,
}: {
  icon: IconName;
  label: string;
  onClick?: (e: React.MouseEvent<HTMLElement>) => void;
  active?: boolean;
  size?: number;
  className?: string;
  disabled?: boolean;
  iconSize?: number;
}) {
  return (
    <button
      type="button"
      aria-label={label}
      title={label}
      disabled={disabled}
      onClick={onClick}
      className={cx(
        "touch-hit flex shrink-0 items-center justify-center rounded-[8px] transition-[background-color,color,transform] duration-150 enabled:active:scale-[0.94] disabled:opacity-40",
        active ? "bg-hover text-ink" : "text-ink-3 enabled:hover:bg-hover enabled:hover:text-ink",
        className,
      )}
      style={{ width: size, height: size }}
    >
      <Icon name={icon} size={iconSize} />
    </button>
  );
}

type BtnTone = "primary" | "secondary" | "quiet" | "danger" | "accent";

export function Button({
  children,
  tone = "secondary",
  onClick,
  disabled,
  type = "button",
  className = "",
  icon,
  busy,
  title,
  href,
}: {
  children: React.ReactNode;
  tone?: BtnTone;
  onClick?: () => void;
  disabled?: boolean;
  type?: "button" | "submit";
  className?: string;
  icon?: IconName;
  busy?: boolean;
  title?: string;
  /** Opens this in a new tab, as a link (noopener, noreferrer: the page gets no handle on this one, nor its address). */
  href?: string;
}) {
  const base = "inline-flex h-8 shrink-0 items-center justify-center gap-1.5 whitespace-nowrap rounded-control px-3 text-[12.5px] font-medium transition-[background-color,color,transform,opacity,filter] duration-150";
  if (href && !disabled && !busy) {
    // an <a> is never :enabled, so the link has its own hover / press
    const links: Record<BtnTone, string> = {
      primary: "bg-ink text-surface shadow-[inset_0_1px_0_rgba(255,255,255,0.14)] hover:opacity-90",
      accent: "bg-accent-ink text-surface hover:brightness-105",
      secondary: "bg-surface text-ink shadow-btn hover:bg-hover",
      quiet: "text-ink-2 hover:bg-hover hover:text-ink",
      danger: "bg-danger-fill text-white hover:brightness-105",
    };
    return (
      <a href={href} target="_blank" rel="noopener noreferrer" title={title} onClick={onClick} className={cx(base, "active:scale-[0.96]", links[tone], className)}>
        {icon ? <Icon name={icon} size={14} stroke={2.1} /> : null}
        {children}
      </a>
    );
  }
  const tones: Record<BtnTone, string> = {
    primary: "bg-ink text-surface shadow-[inset_0_1px_0_rgba(255,255,255,0.14)] enabled:hover:opacity-90",
    accent: "bg-accent-ink text-surface enabled:hover:brightness-105",
    // dark: at 45% the ring and the light label still read as a button you can press, so a disabled one drops both
    secondary: "bg-surface text-ink shadow-btn enabled:hover:bg-hover dark:disabled:bg-transparent dark:disabled:text-ink-3 dark:disabled:shadow-hairline",
    quiet: "text-ink-2 enabled:hover:bg-hover enabled:hover:text-ink",
    danger: "bg-danger-fill text-white enabled:hover:brightness-105",
  };
  return (
    <button
      type={type}
      title={title}
      disabled={disabled || busy}
      // the spinner is aria-hidden, so without this a busy button just reads as disabled
      aria-busy={busy || undefined}
      onClick={onClick}
      className={cx(
        base,
        "enabled:active:scale-[0.96] disabled:opacity-45",
        tones[tone],
        className,
      )}
    >
      {busy ? <Spinner /> : icon ? <Icon name={icon} size={14} stroke={2.1} /> : null}
      {children}
    </button>
  );
}

export function Spinner({ size = 12 }: { size?: number }) {
  return (
    <span
      aria-hidden
      className={`inline-block shrink-0 rounded-full border-[1.5px] border-line-strong border-t-ink-2 ${BUSY_MOTION}`}
      style={{ width: size, height: size, ...spinStyle(700) }}
    />
  );
}

/** An on / off switch (role=switch): the knob slides over 200 ms with the design system's ease (reduced motion: at once).
 *  Settings' rows, and the chat's and "New agent"'s ultracode switch (#3428). `className`: added to the track (a place
 *  that needs its grayed state drawn more clearly); without it the switch is as before. */
export function Switch({ on, onChange, disabled, label, className, describedBy }: { on: boolean; onChange: (on: boolean) => void; disabled?: boolean; label: string; className?: string; describedBy?: string }) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={on}
      aria-label={label}
      aria-describedby={describedBy}
      disabled={disabled}
      onClick={() => onChange(!on)}
      className={cx("touch-hit relative inline-flex h-[22px] w-[38px] shrink-0 items-center rounded-full transition-colors duration-200 disabled:opacity-50", on ? "bg-accent" : "bg-hover-2", className)}
    >
      <span className="absolute left-[2px] size-[18px] rounded-full bg-white shadow-btn transition-transform duration-200" style={{ transform: on ? "translateX(16px)" : "none", transitionTimingFunction: EASE }} />
    </button>
  );
}

export function Kbd({ children }: { children: React.ReactNode }) {
  return <kbd className="inline-flex h-4.5 min-w-4.5 items-center justify-center rounded-[5px] bg-surface px-1 font-sans text-[10px] text-ink-3 shadow-hairline">{children}</kbd>;
}

// Height-animating disclosure (grid 0fr → 1fr), the expand grammar every Beautiful UI trace uses.
/** Folds its children up and down: the grid's row runs between 0fr and 1fr in CSS (no heights measured in JS), with
 *  a fade. `lazy`: the children mount when it opens and stay while it closes, so they go down with the height instead of
 *  vanishing, then unmount (a long trace costs nothing while folded). Once closed, the content is inert. A chat list holds
 *  the clicked row still while the height runs (`anchoredScroll`, #1124). */
export function Collapse({ open, children, className = "", lazy = false }: { open: boolean; children: React.ReactNode; className?: string; lazy?: boolean }) {
  const [closing, setClosing] = useState(false);
  const [moving, setMoving] = useState(false);
  // A lazy one's new content is laid out at 0fr first and the row opens a frame later: a long trace's first layout
  // would otherwise land inside the transition, which then starts most of the way open.
  const [laidOut, setLaidOut] = useState(open || !lazy);
  const [was, setWas] = useState(open);
  if (was !== open) {
    // adjusted while rendering: the first frame of the change already has the content (opening) or keeps it (closing)
    setWas(open);
    setMoving(true);
    setClosing(!open);
    if (open && lazy && !closing) setLaidOut(false);
  }
  const settle = () => {
    setMoving(false);
    setClosing(false);
  };
  useEffect(() => {
    if (!moving) return;
    const t = setTimeout(settle, 450); // transitionend may not come (hidden tab, interrupted, no change in height)
    return () => clearTimeout(t);
  }, [moving, open]);
  useEffect(() => {
    if (!open || laidOut) return;
    const f = requestAnimationFrame(() => setLaidOut(true));
    return () => cancelAnimationFrame(f);
  }, [open, laidOut]);
  const shown = open && laidOut;
  return (
    <div
      className={`grid transition-[grid-template-rows,opacity] duration-300 ${className}`}
      style={{ gridTemplateRows: shown ? "1fr" : "0fr", opacity: shown ? 1 : 0, transitionTimingFunction: EASE, willChange: moving ? "opacity" : undefined }}
      onTransitionEnd={(e) => e.target === e.currentTarget && e.propertyName === "grid-template-rows" && settle()}
      // inert once closed, not while it closes: switching a long trace inert is a full style pass (34 ms for 650 steps)
      inert={(!open && !moving) || undefined}
    >
      <div className="min-h-0 overflow-hidden [contain:layout_paint]">{collapseMounted(lazy, open, closing) && children}</div>
    </div>
  );
}

// ── popover menu with a gliding highlight ───────────────

export type { MenuItem };

/** How long a row's "Close" confirmation waits for the second choice before it asks no more. */
const CONFIRM_MS = 4000;

const errMessage = (e: unknown) => (e instanceof Error ? e.message : String(e));

/** One open submenu: its rows (loaded when they come from a function), the row that opened it and where that row is. */
interface Sub {
  from: number;
  items: MenuItem[];
  rect: { left: number; right: number; top: number; bottom: number };
  loading?: boolean;
  error?: string;
}

/** A menu next to its anchor (a button, or a row's ⋯), or at the pointer (`point`, a right click). ↑ ↓ move, → opens a
 *  submenu and ← goes back, Enter chooses, Esc closes, a row's letter chooses it, ⌫ chooses the close action. A row
 *  can ask again before it runs (`confirm`), turn into an input (`rename`), open a submenu (`children`) or show its
 *  work and failure inside the menu (`inline`). A menu without those keys is the plain one it always was. */
export function Menu({
  items,
  anchor,
  point,
  onClose,
  align = "left",
  side = "bottom",
  width = 208,
  header,
}: {
  items: MenuItem[];
  anchor: HTMLElement | null;
  /** Opens at this pointer position instead of next to `anchor`. */
  point?: { x: number; y: number };
  onClose: () => void;
  align?: "left" | "right";
  side?: "top" | "bottom";
  width?: number;
  header?: string;
}) {
  const id = useId();
  const [subs, setSubs] = useState<Sub[]>([]); // the open submenus: depth d (d ≥ 1) is subs[d - 1]
  const [actives, setActives] = useState<number[]>([-1]); // the highlighted row of each level
  const [confirming, setConfirming] = useState<string | null>(null);
  const [renaming, setRenaming] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [failure, setFailure] = useState<{ depth: number; text: string } | null>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const asking = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const loads = useRef(0);
  const rows = useRef<(HTMLElement | null)[][]>([]);
  // On a narrow screen Back closes the menu first, like the drawer and the sheets, instead of leaving the page (#5132).
  // A row that opens a sheet or a panel takes the menu's entry over (back-close reuses an entry closed a tick before).
  useCloseOnBack(true, onClose, useMedia(NARROW_QUERY));

  const levels = [items, ...subs.map((s) => s.items)];
  // the key handler is registered once and reads this: what the menu is now
  const live = useRef({ levels, actives, subs, confirming, renaming, busy });
  live.current = { levels, actives, subs, confirming, renaming, busy };

  const setActive = (depth: number, index: number) => {
    setActives((a) => {
      const next = a.slice(0, depth + 1);
      for (let i = next.length; i < depth; i++) next[i] = -1;
      next[depth] = index;
      return next;
    });
    const it = live.current.levels[depth]?.[index];
    // moving to another row takes the question back; the pointer leaving (-1) does not: a finger lifting off sends that
    if (index >= 0 && it?.key !== live.current.confirming) setConfirming(null);
  };
  const closeSubsTo = (depth: number) => {
    setSubs((s) => (s.length > depth ? s.slice(0, depth) : s));
    setActives((a) => (a.length > depth + 1 ? a.slice(0, depth + 1) : a));
  };
  const openSub = (depth: number, index: number, focus: boolean) => {
    const { levels: ls, subs: open } = live.current;
    const it = ls[depth]?.[index];
    const el = rows.current[depth]?.[index];
    if (!it?.children || !el) return;
    if (open[depth]?.from === index) {
      if (focus) setActives((a) => [...a.slice(0, depth + 1), a[depth + 1] >= 0 ? a[depth + 1] : edge(open[depth].items, false)]);
      return;
    }
    const r = el.getBoundingClientRect();
    const rect = { left: r.left, right: r.right, top: r.top, bottom: r.bottom };
    const token = ++loads.current;
    const put = (sub: Sub) => {
      setSubs((s) => [...s.slice(0, depth), sub]);
      setActives((a) => [...a.slice(0, depth + 1), focus ? edge(sub.items, false) : -1]);
    };
    setFailure(null);
    if (typeof it.children !== "function") return put({ from: index, items: it.children, rect });
    put({ from: index, items: [], rect, loading: true });
    const settle = (patch: Partial<Sub>) => {
      if (loads.current !== token) return; // another row's submenu has replaced it since
      setSubs((s) => s.map((x, d) => (d === depth ? { ...x, ...patch, loading: false } : x)));
      if (focus && patch.items) setActives((a) => [...a.slice(0, depth + 1), edge(patch.items!, false)]);
    };
    (it.children as () => Promise<MenuItem[]>)().then((list) => settle({ items: list }), (e) => settle({ error: errMessage(e) }));
  };

  const run = async (depth: number, it: MenuItem) => {
    setConfirming(null);
    setFailure(null);
    if (!it.inline) {
      void it.onSelect();
      if (!it.submenu && !it.keepOpen) onClose();
      return;
    }
    setBusy(it.key);
    try {
      await it.onSelect();
      onClose();
    } catch (e) {
      setFailure({ depth, text: errMessage(e) });
      setBusy(null);
    }
  };
  const choose = (depth: number, index: number, focus: boolean) => {
    const { levels: ls, confirming: asked, busy: working } = live.current;
    const it = ls[depth]?.[index];
    if (working) return;
    switch (activation(it, asked)) {
      case "children":
        return openSub(depth, index, focus);
      case "rename":
        setFailure(null);
        return setRenaming(it!.key);
      case "confirm":
        setFailure(null);
        setConfirming(it!.key);
        clearTimeout(asking.current);
        asking.current = setTimeout(() => setConfirming(null), CONFIRM_MS); // the question lapses if nobody answers
        return;
      case "run":
        return void run(depth, it!);
    }
  };
  const commitRename = async (depth: number, it: MenuItem, name: string) => {
    setBusy(it.key);
    setFailure(null);
    try {
      await it.rename!.commit(name);
      onClose();
    } catch (e) {
      setFailure({ depth, text: errMessage(e) });
      setBusy(null);
    }
  };
  const hover = (depth: number, index: number) => {
    clearTimeout(timer.current);
    const { levels: ls, subs: open } = live.current;
    const it = ls[depth]?.[index];
    if (!enabled(it)) return;
    setActive(depth, index);
    // a submenu opens (and the one beside it closes) a moment after the pointer settles on a row, so a pointer
    // crossing rows on its way to the submenu does not swap it
    if (it.children) timer.current = setTimeout(() => openSub(depth, index, false), 100);
    else if (open.length > depth) timer.current = setTimeout(() => closeSubsTo(depth), 100);
  };
  useEffect(() => () => {
    clearTimeout(timer.current);
    clearTimeout(asking.current);
  }, []);

  useEffect(() => {
    // pointerdown, not mousedown: lifting the finger after a long press that opened the menu sends a compatibility
    // mousedown at the finger, outside the menu, which closed it at once. A tap outside still closes it.
    const down = (e: PointerEvent) => {
      const t = e.target as Element | null;
      if (t?.closest?.(`[data-menu="${CSS.escape(id)}"]`) || anchor?.contains(t as Node)) return;
      onClose();
    };
    const key = (e: KeyboardEvent) => {
      const { levels: ls, actives: act, subs: open, renaming: renamed } = live.current;
      if (composing(e) || renamed) return; // an input in the menu takes its own keys
      const t = e.target as HTMLElement | null;
      const typing = Boolean(t && (t.tagName === "INPUT" || t.tagName === "TEXTAREA" || t.tagName === "SELECT" || t.isContentEditable));
      // a field elsewhere on the page keeps its letters and its Backspace
      if (typing && (e.key.length === 1 || e.key === "Backspace" || e.key === "Delete")) return;
      const depth = open.length;
      const intent = keyIntent(e, ls[depth], act[depth] ?? -1, depth);
      if (intent.type === "none") return;
      e.preventDefault();
      e.stopPropagation();
      if (intent.type === "close") onClose();
      else if (intent.type === "back") closeSubsTo(depth - 1);
      else if (intent.type === "move") setActive(depth, intent.index);
      else choose(depth, intent.index, true);
    };
    addEventListener("pointerdown", down);
    addEventListener("keydown", key, true);
    return () => {
      removeEventListener("pointerdown", down);
      removeEventListener("keydown", key, true);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps -- the handlers read `live`; only the menu's anchor and close change them
  }, [anchor, onClose, id]);

  const panelProps = (depth: number) => ({
    id,
    depth,
    items: levels[depth],
    active: actives[depth] ?? -1,
    confirming,
    renaming,
    busy,
    failure: failure?.depth === depth ? failure.text : undefined,
    rows: (list: (HTMLElement | null)[]) => {
      rows.current[depth] = list;
    },
    onHover: (i: number) => hover(depth, i),
    onEnter: () => clearTimeout(timer.current),
    onLeave: () => live.current.subs.length <= depth && setActive(depth, -1),
    onChoose: (i: number) => choose(depth, i, false),
    onRename: (it: MenuItem, name: string) => void commitRename(depth, it, name),
    onCancelRename: () => {
      setRenaming(null);
      setFailure(null);
    },
  });

  return createPortal(
    <>
      <MenuPanel {...panelProps(0)} place={{ anchor, point, align, side, width }} header={header} />
      {subs.map((s, i) => (
        <MenuPanel key={`${s.from}-${i}`} {...panelProps(i + 1)} place={{ rect: s.rect, width }} loading={s.loading} loadError={s.error} />
      ))}
    </>,
    document.body,
  );
}

type Place =
  | { anchor: HTMLElement | null; point?: { x: number; y: number }; align: "left" | "right"; side: "top" | "bottom"; width: number; rect?: undefined }
  | { rect: { left: number; right: number; top: number; bottom: number }; width: number };

/** One level of a menu: the rows, the highlight gliding under them, a rename input or a failure where they call for it. */
function MenuPanel({
  id, depth, items, active, confirming, renaming, busy, failure, rows: report, place, header, loading, loadError,
  onHover, onEnter, onLeave, onChoose, onRename, onCancelRename,
}: {
  id: string;
  depth: number;
  items: MenuItem[];
  active: number;
  confirming: string | null;
  renaming: string | null;
  busy: string | null;
  failure?: string;
  rows: (list: (HTMLElement | null)[]) => void;
  place: Place;
  header?: string;
  loading?: boolean;
  loadError?: string;
  onHover: (i: number) => void;
  onEnter: () => void;
  onLeave: () => void;
  onChoose: (i: number) => void;
  onRename: (it: MenuItem, name: string) => void;
  onCancelRename: () => void;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const els = useRef<(HTMLElement | null)[]>([]);
  const [box, setBox] = useState<{ top: number; height: number } | null>(null);
  const [pos, setPos] = useState<React.CSSProperties>({ visibility: "hidden" });
  const narrow = useMedia(NARROW_QUERY);
  report(els.current);
  // only lines to read (#4054): a popover that explains something, not a menu to choose from
  const reading = items.length > 0 && items.every((it) => it.note);
  const root = place.rect === undefined;
  const origin = root && place.point ? "top left" : root && place.side === "top" ? `bottom ${place.align}` : root ? `top ${place.align}` : "top left";

  useLayoutEffect(() => {
    const reposition = () => {
      if (!ref.current) return;
      // On a phone it keeps to what the keyboard leaves visible (the frame --kb-top / --kb-h follow).
      const view = narrow ? keyboardFrame(innerHeight, window.visualViewport) : undefined;
      // Measure the natural height: the inline maxHeight left by the previous placement (a keyboard that has since
      // closed) would otherwise be read back as the menu's height, and the menu placed for a height it no longer has.
      // The scroll offset is put back too: the uncapped layout may clamp it.
      const el = ref.current;
      const { scrollTop } = el;
      const capped = el.style.maxHeight;
      el.style.maxHeight = "";
      // ...at the width it will have: rows whose text wraps (`detail`, `wrap`) are taller in a narrower panel, and the
      // first placement has no width yet, so a menu above its anchor came out too short and covered it (#5584)
      el.style.width = `${Math.min(place.width, innerWidth - 16)}px`;
      const h = el.offsetHeight;
      el.style.maxHeight = capped;
      el.scrollTop = scrollTop;
      let at: MenuPlace;
      if (place.rect) at = placeSubmenu(place.rect, h, { width: place.width, screenW: innerWidth, screenH: innerHeight, view });
      else if (place.point) {
        const { x, y } = place.point;
        const right = x + Math.min(place.width, innerWidth - 16) + 8 > innerWidth; // no room on the right: it opens leftwards
        at = placeMenu({ left: x, right: x, top: y, bottom: y }, h, { width: place.width, align: right ? "right" : "left", side: "bottom", screenW: innerWidth, screenH: innerHeight, view, gap: 0 });
      } else if (place.anchor) {
        at = placeMenu(place.anchor.getBoundingClientRect(), h, { width: place.width, align: place.align, side: place.side, screenW: innerWidth, screenH: innerHeight, view });
      } else return;
      const { maxHeight, ...rest } = at;
      setPos(maxHeight === undefined ? rest : { ...rest, maxHeight: `min(70vh, 520px, ${maxHeight}px)` });
    };
    reposition();
    // An open menu keeps to where it belongs when the window or the visual viewport (a phone keyboard) resizes.
    const vv = window.visualViewport;
    window.addEventListener("resize", reposition);
    vv?.addEventListener("resize", reposition);
    return () => {
      window.removeEventListener("resize", reposition);
      vv?.removeEventListener("resize", reposition);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps -- placed again when what decides its place or height changes
  }, [place.rect, root ? place.anchor : null, root ? place.point?.x : 0, root ? place.point?.y : 0, place.width, items.length, narrow, loading, loadError]);

  useLayoutEffect(() => {
    const el = els.current[active];
    setBox(el ? { top: el.offsetTop, height: el.offsetHeight } : null);
  }, [active]);

  return (
    <div
      ref={ref}
      role={reading ? "dialog" : "menu"}
      aria-label={reading ? (header ?? items[0].label) : undefined}
      data-menu={id}
      className="fixed z-50 max-h-[min(70vh,520px)] overflow-y-auto rounded-[10px] bg-surface p-1 shadow-overlay"
      style={{ ...pos, animation: `pop-in 160ms ${EASE} both`, transformOrigin: origin }}
      onMouseEnter={onEnter}
      onMouseLeave={onLeave}
    >
      {header && <div className="px-2 pt-1.5 pb-1 text-[10.5px] font-medium tracking-[0.06em] text-ink-3 uppercase">{header}</div>}
      <div className="relative">
        <span
          aria-hidden
          className="pointer-events-none absolute inset-x-0 rounded-[6px] bg-hover"
          style={{ top: box?.top ?? 0, height: box?.height ?? 0, opacity: box ? 1 : 0, transition: `top 200ms ${EASE}, height 200ms ${EASE}, opacity 120ms ease` }}
        />
        {loading && (
          <div className="relative z-10 flex h-8 items-center gap-2 px-2 text-[12.5px] text-ink-3 pointer-coarse:min-h-11">
            <Spinner size={12} />
            {t("ui-atoms.menu_loading")}
          </div>
        )}
        {loadError && <div role="alert" className="relative z-10 px-2 py-1.5 text-[12px] break-words whitespace-pre-wrap text-red">{loadError}</div>}
        {!loading && !loadError && depth > 0 && items.length === 0 && <div className="relative z-10 flex h-8 items-center px-2 text-[12.5px] text-ink-3 pointer-coarse:min-h-11">{t("ui-atoms.menu_empty")}</div>}
        {items.map((it, i) => [
          it.divider && i > 0 && <div key={`${it.key}-sep`} role="separator" className="mx-1 my-1 h-px bg-line" />,
          it.heading ? (
            <div
              key={it.key}
              ref={(el) => {
                els.current[i] = el;
              }}
              role="presentation"
              className="relative z-10 px-2 pt-1.5 pb-1 text-[10.5px] font-medium tracking-[0.06em] text-ink-3 uppercase"
            >
              {it.label}
            </div>
          ) : it.note ? (
            <NoteRow
              key={it.key}
              item={it}
              refCb={(el) => {
                els.current[i] = el;
              }}
            />
          ) : renaming === it.key && it.rename ? (
            <RenameRow
              key={it.key}
              item={it}
              busy={busy === it.key}
              onCommit={(name) => onRename(it, name)}
              onCancel={onCancelRename}
              refCb={(el) => {
                els.current[i] = el;
              }}
            />
          ) : (
            <MenuRow
              key={it.key}
              item={it}
              asking={confirming === it.key}
              working={busy === it.key}
              refCb={(el) => {
                els.current[i] = el;
              }}
              onHover={() => !it.disabled && onHover(i)}
              onChoose={() => onChoose(i)}
            />
          ),
        ])}
      </div>
      {failure && (
        <div role="alert" className="mx-1 mt-1 max-h-28 overflow-y-auto border-t border-line px-1 pt-1.5 pb-1 text-[11.5px] break-words whitespace-pre-wrap text-red">
          {failure}
        </div>
      )}
    </div>
  );
}

function MenuRow({ item: it, asking, working, refCb, onHover, onChoose }: { item: MenuItem; asking: boolean; working: boolean; refCb: (el: HTMLElement | null) => void; onHover: () => void; onChoose: () => void }) {
  const accel = accelLabel(it);
  const detailId = `${useId()}-detail`;
  const label = rowLabel(it, asking ? it.key : null);
  return (
    <button
      ref={refCb}
      role={it.radio ? "menuitemradio" : "menuitem"}
      type="button"
      title={it.state ? `${it.label} · ${STATE_LABEL[it.state]}` : undefined}
      disabled={it.disabled}
      aria-haspopup={it.children ? "menu" : undefined}
      aria-keyshortcuts={it.accel}
      aria-checked={it.radio ? Boolean(it.checked) : undefined}
      aria-describedby={it.detail ? detailId : undefined}
      onMouseEnter={onHover}
      onClick={onChoose}
      className={cx(
        "relative z-10 flex w-full items-center gap-2 rounded-[6px] px-2 pointer-coarse:min-h-11 text-left text-[12.5px] disabled:opacity-40",
        it.wrap || it.detail ? "min-h-8 py-1.5" : "h-8",
        it.danger ? "text-red" : "text-ink",
        asking && "bg-red-tint font-medium",
      )}
    >
      {it.state && <StatusMark state={it.state} />}
      {it.icon && <Icon name={it.icon} size={14} className={it.danger ? "" : "text-ink-3"} />}
      {it.detail ? (
        // label over its explanation, both wrapping: the explanation never pushes the label (or the check) off the row
        <span className="min-w-0 flex-1">
          <span className="block break-words">{label}</span>
          <span id={detailId} className="mt-0.5 block text-[11.5px] leading-snug break-words text-ink-3">{it.detail}</span>
        </span>
      ) : (
        <span className={cx("min-w-0 flex-1", it.wrap ? "break-all" : "truncate", it.mono && "font-mono")}>{label}</span>
      )}
      {it.state && <StatePill state={it.state} />}
      {working ? <Spinner size={12} /> : it.hint && <span className="shrink-0 text-[11px] text-ink-3">{it.hint}</span>}
      {it.checked !== undefined && <Icon name="check" size={13} stroke={2.5} className={cx("shrink-0", it.checked ? "text-ink" : "invisible")} />}
      {accel && !working && <span aria-hidden className={cx("w-3.5 shrink-0 text-center text-[11px]", it.danger ? "" : "text-ink-3")}>{accel}</span>}
      {(it.submenu || it.children) && <Icon name="chevron" size={12} className="-rotate-90 text-ink-3" />}
    </button>
  );
}

/** A line to read (#4054): the label wraps at word breaks, the hint (a number, a unit) stays whole at its end. */
function NoteRow({ item: it, refCb }: { item: MenuItem; refCb: (el: HTMLElement | null) => void }) {
  return (
    <div ref={refCb} data-menu-note className="relative z-10 flex items-baseline gap-2 px-2 py-1.5 text-[12.5px] leading-relaxed text-ink">
      <span className="min-w-0 flex-1 break-words">{it.label}</span>
      {it.hint && <span className="shrink-0 text-[11px] whitespace-nowrap text-ink-3 tabular-nums">{it.hint}</span>}
    </div>
  );
}

/** A row that is an input: Enter commits a changed, non-empty name, Esc (or Enter on the same name) puts the row back. */
function RenameRow({ item, busy, onCommit, onCancel, refCb }: { item: MenuItem; busy: boolean; onCommit: (name: string) => void; onCancel: () => void; refCb: (el: HTMLElement | null) => void }) {
  const rename = item.rename!;
  const [text, setText] = useState(rename.value);
  const hintId = useId();
  // what is wrong with it as typed (rename.validate), said under the input; Enter waits for a good one
  const problem = text.trim() ? (rename.validate?.(text) ?? "") : "";
  const below = problem || (rename.validate ? rename.hint : "");
  const input = (
    <input
      autoFocus
      value={text}
      disabled={busy}
      aria-label={item.label.replace(/…$/, "")}
      aria-invalid={problem ? true : undefined}
      aria-describedby={below ? hintId : undefined}
      placeholder={rename.placeholder}
      spellCheck={rename.validate ? false : undefined}
      autoComplete={rename.validate ? "off" : undefined}
      onChange={(e) => setText(e.target.value)}
      onFocus={(e) => e.currentTarget.select()}
      onKeyDown={(e) => {
        if (composing(e.nativeEvent)) return;
        if (e.key === "Enter") {
          e.preventDefault();
          e.stopPropagation();
          if (problem) return;
          const name = renameCommit(rename.value, text);
          if (name === null) onCancel();
          else onCommit(name);
        } else if (e.key === "Escape") {
          e.preventDefault();
          e.stopPropagation();
          onCancel();
        } else e.stopPropagation();
      }}
      className={cx(
        "h-7 min-w-0 flex-1 rounded-[6px] bg-inset px-2 text-[12.5px] text-ink outline-none placeholder:text-ink-3 pointer-coarse:h-9",
        rename.validate && "font-mono placeholder:font-sans",
        // one ring or the other: both set the same shadow, and the hairline would win
        problem ? "shadow-[0_0_0_1px_var(--red)]" : "shadow-hairline",
      )}
    />
  );
  if (!rename.validate)
    return (
      <div ref={refCb} className="relative z-10 flex h-8 items-center gap-2 px-1 pointer-coarse:min-h-11">
        {input}
        {busy && <Spinner size={12} />}
      </div>
    );
  return (
    <div ref={refCb} className="relative z-10 flex flex-col gap-1 px-1 py-0.5">
      <div className="flex h-8 items-center gap-2 pointer-coarse:min-h-11">
        {input}
        {busy && <Spinner size={12} />}
      </div>
      {below && (
        <p id={hintId} role={problem ? "alert" : undefined} className={cx("px-0.5 pb-0.5 text-[11.5px] leading-snug break-words", problem ? "text-red" : "text-ink-3")}>
          {below}
        </p>
      )}
    </div>
  );
}

export function useMenu() {
  const [anchor, setAnchor] = useState<HTMLElement | null>(null);
  return {
    anchor,
    open: (e: React.MouseEvent<HTMLElement>) => {
      const target = e.currentTarget;
      setAnchor((a) => (a ? null : target));
    },
    close: () => setAnchor(null),
    /** Opens it at an element of the caller's choosing (another menu's anchor, once that menu has closed). */
    openAt: (el: HTMLElement | null) => setAnchor(el),
  };
}

// ── overlay exits ───────────────────────────────────────

/** How long an overlay takes to leave: a little quicker than the enter it undoes (the card's slide-up 220 ms, the
 *  sheet's 300 ms, a toast's 220 ms). `glide`: a toast moving to its new place in the stack, as long as its slide-up. */
export const EXIT_MS = { card: 160, sheet: 220, toast: 160, glide: 220 } as const;
/** The card goes back the way it came in (slide-up's first frame, styles.css); the sheet back down. */
export const CARD_OUT: Keyframe[] = [{ opacity: 1, transform: "none" }, { opacity: 0, transform: "translateY(12px) scale(0.985)" }];
const SHEET_OUT: Keyframe[] = [{ transform: "none" }, { transform: "translateY(100%)" }];
const TOAST_OUT: Keyframe[] = [{ opacity: 1, transform: "none" }, { opacity: 0, transform: "translateY(6px) scale(0.985)" }];

/** Whether an exit plays: the browser has the Web Animations API (jsdom has none) and reduced motion is not asked
 *  for. Otherwise an overlay goes at once, as it always did. */
export function exitsPlay(): boolean {
  return typeof Element !== "undefined" && typeof Element.prototype.animate === "function" && !prefersReducedMotion();
}

/** Calls `done` once the animations finish, or `ms` and a margin later if they never do (a hidden tab, an element
 *  that was not there); returns the cancel, which stops them without calling `done`. */
function afterAll(anims: (Animation | undefined)[], ms: number, done: () => void): () => void {
  let over = false;
  const end = () => {
    if (over) return;
    over = true;
    clearTimeout(timer);
    done();
  };
  const timer = setTimeout(end, ms + 120);
  const live = anims.filter((a): a is Animation => !!a);
  Promise.all(live.map((a) => a.finished)).then(end, () => {}); // a cancelled one rejects: the cancel ran already
  return () => {
    over = true;
    clearTimeout(timer);
    live.forEach((a) => a.cancel());
  };
}

/** Keeps an overlay on screen while it leaves: after `open` turns false it stays mounted (`leaving`) while `play`'s
 *  animations run, then goes. Opened again on the way out, it stops them and is simply open. */
export function useExit(open: boolean, ms: number, play: () => (Animation | undefined)[]) {
  const [leaving, setLeaving] = useState(false);
  const [was, setWas] = useState(open);
  if (was !== open) {
    // adjusted while rendering, like Collapse: the frame that closes it already has it leaving, not gone
    setWas(open);
    setLeaving(!open && exitsPlay());
  }
  useLayoutEffect(() => {
    if (!leaving) return;
    return afterAll(play(), ms, () => setLeaving(false));
  }, [leaving]);
  return { mounted: open || leaving, leaving };
}

// ── modal dialog ────────────────────────────────────────

/** A modal. Wide: a card in the middle of the page. Phone width (max-sm): a sheet up from the bottom of what the
 *  keyboard leaves visible (--kb-top / --kb-h), at most 90% of it, its body scrolling between the title and the footer,
 *  and Back closes it. Either way the focus moves in, Tab stays in, and goes back where it was when it closes
 *  (unless the close moved it elsewhere). */
export function Dialog({
  open,
  onClose,
  title,
  children,
  width = 440,
  footer,
}: {
  open: boolean;
  onClose: () => void;
  title?: React.ReactNode;
  children: React.ReactNode;
  width?: number;
  footer?: React.ReactNode;
}) {
  const panel = useRef<HTMLDivElement>(null);
  const titleId = useId();
  const sheet = useMedia(SHEET_QUERY);
  useCloseOnBack(open, onClose, sheet);
  useEscapeClose(open, onClose);
  // The dialog itself takes the focus, so a phone's keyboard does not come up for its first field (one with autoFocus
  // took it already). On close it goes back to the opener, unless the close moved it outside on purpose.
  useModalFocus(panel, open);
  const backdrop = useRef<HTMLDivElement>(null);
  // what it showed when last open: a caller may clear what the body reads once it is closed, while it still leaves
  const kept = useRef({ title, children, footer });
  if (open) kept.current = { title, children, footer };
  const exit = useExit(open, EXIT_MS.card, () => [
    backdrop.current?.animate([{ opacity: 1 }, { opacity: 0 }], { duration: EXIT_MS.card, easing: "ease-out", fill: "forwards" }),
    panel.current?.animate(sheet ? SHEET_OUT : CARD_OUT, { duration: sheet ? EXIT_MS.sheet : EXIT_MS.card, easing: EASE, fill: "forwards" }),
  ]);
  if (!exit.mounted) return null;
  const shown = open ? { title, children, footer } : kept.current;
  return createPortal(
    <div
      ref={backdrop}
      data-closing={exit.leaving || undefined}
      inert={exit.leaving || undefined}
      className="fixed inset-0 z-40 flex items-start justify-center overflow-y-auto bg-black/25 px-4 pt-[12vh] pb-8 backdrop-blur-[2px] dark:bg-black/45 max-sm:top-(--kb-top) max-sm:bottom-auto max-sm:h-(--kb-h) max-sm:items-end max-sm:overflow-hidden max-sm:p-0"
      style={{ animation: "fade-in 140ms ease-out both" }}
      onMouseDown={(e) => e.target === e.currentTarget && onClose()}
    >
      {/* A column either way, so a tall body scrolls between the title and the footer: the card stops short of the
          backdrop's bottom padding (12vh above it, pb-8 below), the sheet at 90% of what the keyboard leaves. */}
      <div
        ref={panel}
        role="dialog"
        aria-modal={open || undefined}
        aria-labelledby={shown.title ? titleId : undefined}
        tabIndex={-1}
        className="flex max-h-[calc(88vh-(--spacing(8)))] w-full max-w-(--dialog-w) animate-[slide-up_220ms_var(--ease-out-strong)_both] flex-col overflow-hidden rounded-[14px] bg-surface shadow-overlay outline-none max-sm:max-h-[calc(var(--kb-h)*0.9)] max-sm:max-w-none max-sm:animate-none max-sm:rounded-b-none max-sm:transition-transform max-sm:duration-300 max-sm:ease-(--ease-out-strong) max-sm:starting:translate-y-full"
        style={{ "--dialog-w": `${width}px` } as React.CSSProperties}
      >
        {shown.title && (
          <div className="flex shrink-0 items-center justify-between gap-3 px-4 pt-3.5 pb-1">
            <h2 id={titleId} className="text-[14px] font-semibold text-ink">{shown.title}</h2>
            <IconButton icon="x" label={t("ui-atoms.close")} onClick={onClose} size={26} iconSize={14} />
          </div>
        )}
        <div className={cx("min-h-0 flex-1 overflow-y-auto overscroll-contain px-4 pt-2 pb-4", !shown.footer && "max-sm:pb-[calc(16px+env(safe-area-inset-bottom))]")}>{shown.children}</div>
        {shown.footer && <div className="flex shrink-0 items-center justify-end gap-2 border-t border-line bg-inset px-4 py-2.5 max-sm:pb-[calc(10px+env(safe-area-inset-bottom))]">{shown.footer}</div>}
      </div>
    </div>,
    document.body,
  );
}

// ── toasts ──────────────────────────────────────────────

/** A button on a toast ("Undo"): it runs, and the toast goes. */
export type ToastAction = { label: string; run: () => void };
/** `leaving`: timed out or closed, playing its exit before it is gone (the stack removes it when that ends). */
type Toast = { id: number; text: string; tone: "info" | "error" | "ok"; action?: ToastAction; leaving?: boolean };
let toasts: Toast[] = [];
const toastListeners = new Set<() => void>();
let toastId = 0;
/** Each toast's timer: what is left of its time, since when that has been running, and the pending timeout. */
const toastTimers = new Map<number, { left: number; since: number; handle?: ReturnType<typeof setTimeout> }>();
let toastsPaused = false;

const toastsChanged = () => toastListeners.forEach((l) => l());

function removeToast(id: number) {
  if (!toasts.some((t) => t.id === id)) return;
  toasts = toasts.filter((t) => t.id !== id);
  toastsChanged();
}

function dropToast(id: number) {
  clearTimeout(toastTimers.get(id)?.handle);
  toastTimers.delete(id);
  const item = toasts.find((t) => t.id === id);
  if (!item || item.leaving) return;
  if (!exitsPlay() || !toastListeners.size) return removeToast(id);
  toasts = toasts.map((t) => (t.id === id ? { ...t, leaving: true } : t));
  toastsChanged();
  setTimeout(() => removeToast(id), EXIT_MS.toast + 120); // the exit's end may never come (a hidden tab)
}

/** Holds every toast's time while the pointer or the focus is on one (WCAG 2.2.1), and lets it run on after. */
function pauseToasts(on: boolean) {
  if (toastsPaused === on) return;
  toastsPaused = on;
  const now = Date.now();
  for (const [id, timer] of toastTimers) {
    if (on) {
      clearTimeout(timer.handle);
      timer.handle = undefined;
      timer.left -= now - timer.since;
    } else {
      timer.since = now;
      timer.handle = setTimeout(() => dropToast(id), Math.max(0, timer.left));
    }
  }
}

const gate = new FeedbackGate();

/** Shows a line at the bottom. While the hub is out of reach an error that is about it (lib/feedback.ts
 *  isOfflineError) is one calm "didn't go through" at most every few seconds; any other error keeps its words. The
 *  same line again within a few seconds is folded; one with a button always shows. At most three at a time: a fourth
 *  sends the oldest away. */
export function toast(text: string, tone: Toast["tone"] = "info", action?: ToastAction) {
  const shown = action ? { text, tone } : gate.pass(text, tone, offlineNow(), Date.now());
  if (!shown) return;
  const id = ++toastId;
  toasts.filter((t) => !t.leaving).slice(0, -2).forEach((t) => dropToast(t.id));
  toasts = [...toasts, { id, text: shown.text, tone: shown.tone, action }];
  // One with a button stays long enough to reach it.
  const ms = action ? 6000 : shown.tone === "error" ? 5200 : 2800;
  toastTimers.set(id, { left: ms, since: Date.now(), handle: toastsPaused ? undefined : setTimeout(() => dropToast(id), ms) });
  toastsChanged();
}

export function Toasts() {
  const [, force] = useState(0);
  const rows = useRef(new Map<number, HTMLDivElement>());
  // where each row was on screen just before the list changed, and the glides and exits that are running
  const before = useRef(new Map<number, number>());
  const glides = useRef(new Map<number, Animation>());
  const exiting = useRef(new Set<number>());
  const [hovered, setHovered] = useState<number | null>(null);
  const [focused, setFocused] = useState<number | null>(null);
  useEffect(() => {
    const l = () => {
      // measured before React moves anything: the F of FLIP
      before.current.clear();
      if (exitsPlay()) for (const [id, el] of rows.current) before.current.set(id, el.getBoundingClientRect().top);
      force((x) => x + 1);
    };
    toastListeners.add(l);
    return () => {
      toastListeners.delete(l);
    };
  }, []);
  useLayoutEffect(() => {
    // a row the list change moved slides from where it was to its new place, by transform only
    for (const item of toasts) {
      const el = rows.current.get(item.id);
      const first = before.current.get(item.id);
      if (!el || first === undefined || item.leaving) continue;
      glides.current.get(item.id)?.cancel();
      const dy = first - el.getBoundingClientRect().top;
      if (Math.abs(dy) < 0.5) continue;
      const glide = el.animate([{ transform: `translateY(${dy}px)` }, { transform: "none" }], { duration: EXIT_MS.glide, easing: EASE });
      glides.current.set(item.id, glide);
    }
    before.current.clear();
    // one that is leaving plays its exit once, then goes
    for (const item of toasts) {
      const el = rows.current.get(item.id);
      if (!item.leaving || !el || exiting.current.has(item.id)) continue;
      exiting.current.add(item.id);
      glides.current.get(item.id)?.cancel();
      el.animate(TOAST_OUT, { duration: EXIT_MS.toast, easing: EASE, fill: "forwards" }).finished.then(() => removeToast(item.id), () => {});
    }
    for (const id of [...glides.current.keys(), ...exiting.current]) {
      if (toasts.some((t) => t.id === id)) continue;
      glides.current.delete(id);
      exiting.current.delete(id);
    }
  });
  // held while the pointer or the focus is on a toast that is still there (one that went takes its hold with it)
  const live = (id: number | null) => id !== null && toasts.some((t) => t.id === id && !t.leaving);
  const paused = live(hovered) || live(focused);
  useEffect(() => pauseToasts(paused), [paused]);
  useEffect(() => () => pauseToasts(false), []);
  return createPortal(
    <div aria-live="polite" className="pointer-events-none fixed inset-x-0 bottom-5 z-[70] flex flex-col items-center gap-2 px-4">
      {toasts.map((item) => (
        <div
          key={item.id}
          ref={(el) => {
            if (el) rows.current.set(item.id, el);
            else rows.current.delete(item.id);
          }}
          data-closing={item.leaving || undefined}
          inert={item.leaving || undefined}
          onMouseEnter={() => setHovered(item.id)}
          onMouseLeave={() => setHovered((h) => (h === item.id ? null : h))}
          onFocus={() => setFocused(item.id)}
          onBlur={(e) => !e.currentTarget.contains(e.relatedTarget as Node | null) && setFocused((f) => (f === item.id ? null : f))}
          className="pointer-events-auto flex max-w-md items-center gap-2 rounded-[10px] border border-tooltip-border bg-tooltip-bg px-3 py-2 text-[12.5px] text-tooltip-fg shadow-overlay"
          style={{ animation: `slide-up 220ms ${EASE} both` }}
        >
          {item.tone === "error" && <Icon name="warning" size={13} stroke={2.2} className="shrink-0 text-red" />}
          {item.tone === "ok" && <Icon name="check" size={13} stroke={2.6} className="text-green" />}
          <span className="min-w-0">{item.text}</span>
          {item.action && (
            <button type="button" className="ml-1 shrink-0 whitespace-nowrap font-semibold text-accent hover:underline"
              onClick={() => { item.action?.run(); dropToast(item.id); }}>
              {item.action.label}
            </button>
          )}
          <button
            type="button"
            aria-label={t("ui-atoms.close")}
            title={t("ui-atoms.close")}
            onClick={() => dropToast(item.id)}
            className="touch-hit -mr-1.5 ml-0.5 flex size-5 shrink-0 items-center justify-center rounded-[5px] text-tooltip-muted transition-colors duration-150 hover:bg-tooltip-border hover:text-tooltip-fg"
          >
            <Icon name="x" size={12} stroke={2.2} />
          </button>
        </div>
      ))}
    </div>,
    document.body,
  );
}

/** Puts text on the clipboard and says so (`done` / `failed` replace the usual words); whether it got there. */
export async function copyText(text: string, words: { done?: string; failed?: string } = {}): Promise<boolean> {
  let ok = true;
  try {
    await navigator.clipboard.writeText(text);
  } catch {
    // http (not a secure context) has no async clipboard
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.style.position = "fixed";
    ta.style.opacity = "0";
    document.body.appendChild(ta);
    try {
      ta.select();
      ok = document.execCommand("copy");
    } catch {
      ok = false;
    } finally {
      ta.remove();
    }
  }
  if (ok) toast(words.done ?? t("ui-atoms.copied"), "ok");
  else toast(words.failed ?? t("ui-atoms.copy_failed"), "error");
  return ok;
}
