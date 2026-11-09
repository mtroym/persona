// The pointer on a terminal the page drives (#2441, docs/integrations/terminal.md "Mouse"): which events go to herdr (and so to a
// program that takes the mouse) and which stay with the page (xterm's selection, Persona's right-click menu), the
// button and modifier bits herdr takes, and how motion is thinned to one event per cell. Pure, so the decisions are
// tested without a browser; lib/terminal.ts keeps the older wheel / click geometry.

import { t } from "../i18n";

export type MouseButton = "left" | "middle" | "right";
export type MouseAction = "down" | "up" | "drag" | "move";

/**
 * Who gets the mouse, a choice the user keeps (terminal settings › Mouse). herdr does not say whether the program in the pane takes
 * the mouse (nothing in its control stream, API or pane info shows it), so the page cannot pick per program:
 *  - program (the default): clicks, drags and buttons go to herdr, which hands them to a program that takes the mouse
 *    and drops them for one that does not; Shift makes the pointer the page's (a selection in xterm, the Persona menu).
 *  - local: the page selects and has its menu, as before; Shift sends the pointer to herdr.
 */
export type MouseMode = "program" | "local";

export const MOUSE_MODES: { mode: MouseMode; label: string }[] = [
  { mode: "program", label: t("terminal-mouse.mode_program") },
  { mode: "local", label: t("terminal-mouse.mode_local") },
];

export function parseMouseMode(v: string | null): MouseMode {
  return v === "local" ? "local" : "program";
}

/** PointerEvent.button / MouseEvent.button → herdr's button; the others (back, forward) have none. */
export function mouseButton(button: number): MouseButton | null {
  return button === 0 ? "left" : button === 1 ? "middle" : button === 2 ? "right" : null;
}

/** herdr's modifier bits on a mouse event: shift 1 (never sent: Shift picks the route), ctrl 2, alt 4 (measured,
 *  docs/integrations/terminal.md "Mouse"). */
export const MOD_CTRL = 2;
export const MOD_ALT = 4;

export interface PointerKeys {
  shiftKey: boolean;
  ctrlKey: boolean;
  altKey: boolean;
  metaKey: boolean;
}

/**
 * Whether the pointer goes to herdr. Shift is the page's key in both modes: it flips the route, so it is not also sent
 * as a modifier. Meta (⌘ / the Windows key) always stays with the page, because xterm opens a link on ⌘-click and a
 * program has no use for it. `touch` is never sent (a finger scrolls and selects; a tap is the older `click`).
 */
export function mouseRoute(mode: MouseMode, e: PointerKeys, pointerType = "mouse"): "program" | "page" {
  if (pointerType === "touch" || e.metaKey) return "page";
  return e.shiftKey === (mode === "local") ? "program" : "page";
}

/** The modifier bits to send with an event routed to the program (Shift chose the route, so it is left out). */
export function mouseModifiers(e: PointerKeys): number {
  return (e.ctrlKey ? MOD_CTRL : 0) | (e.altKey ? MOD_ALT : 0);
}

/**
 * What a right click (the contextmenu event) does. Not forwarding the mouse (watching, or an older hub): as before, a plain
 * right click opens Persona's menu and Shift leaves the browser's own. Forwarding: the same routing as every other
 * pointer event, so what goes to the program is not also a menu, and the menu stays one chord away (Shift in the default
 * mode, the plain right click in "Select locally"). A touch long press is left alone (it selects text on a phone).
 */
export function contextAction(e: PointerKeys, forwarding: boolean, mode: MouseMode, pointerType = "mouse"): "menu" | "program" | "browser" {
  if (pointerType === "touch") return "browser";
  if (!forwarding) return e.shiftKey ? "browser" : "menu";
  return mouseRoute(mode, e, pointerType) === "program" ? "program" : "menu";
}

/** One motion event per cell: a drag is a stream of pixels, a program wants cells. */
export class CellThrottle {
  private last: string | null = null;

  /** True when this cell is not the one the last event was sent for. */
  step(cell: readonly [number, number]): boolean {
    const key = cell[0] + "," + cell[1];
    if (key === this.last) return false;
    this.last = key;
    return true;
  }

  reset(cell?: readonly [number, number]) {
    this.last = cell ? cell[0] + "," + cell[1] : null;
  }
}

/** A point of the screen's box clamped into it: a drag that leaves the terminal keeps pointing at its nearest edge cell. */
export function clampedPoint(x: number, y: number, width: number, height: number): [number, number] {
  return [Math.max(0, Math.min(width, x)), Math.max(0, Math.min(height, y))];
}
