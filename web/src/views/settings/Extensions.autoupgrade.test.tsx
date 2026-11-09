// @vitest-environment jsdom
// A market extension's auto-upgrade switch shows its saved preference even while execution is paused.
// Turning the extension off disables the control without disguising the preference as off.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../lib/store", async (original) => {
  const real = await original<typeof import("../../lib/store")>();
  return { ...real, useStore: <T,>(select: (s: ReturnType<typeof real.getState>) => T): T => select({ ...real.getState(), features: ["extension_markets"] }) };
});
vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast: vi.fn() }));

import { resetManagerChoice } from "../../lib/manager-choice";
import { setRoster } from "../../lib/managers";
import { Extensions } from "./Extensions";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const ROSTER = { default: "assistant", managers: [{ id: "assistant", name: "助手", online: true, builtin: true }] };
const BASE = {
  id: "example-ui", name: "示例界面", kind: "ui", source: "installed",
  upgrade: { market: "official", id: "example-ui", from: "1.0.0", to: "1.1.0", state: "off" },
};

let host: HTMLDivElement;
let root: Root;
let detail: Record<string, unknown> = {};

const reply = (body: unknown, status = 200) => ({ ok: status < 400, status, text: async () => JSON.stringify(body), headers: new Headers() });
const flush = async () => {
  for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); });
};

beforeEach(() => {
  resetManagerChoice();
  setRoster(ROSTER);
  vi.stubGlobal("fetch", vi.fn(async (url: string) => {
    if (url.endsWith("/managers")) return reply(ROSTER);
    if (/\/(plugins|extensions)\/example-ui\/-\/$/.test(url)) return reply(detail);
    return reply({ error: "not found" }, 404);
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
  setRoster(null);
  resetManagerChoice();
});

describe("auto-upgrade switch", () => {
  it.each([
    { name: "on, its switch on", enabled: true, own: true, paused: undefined, checked: "true", greyed: false, footer: "扩展空闲时自动装上" },
    { name: "on, its switch off", enabled: true, own: false, paused: undefined, checked: "false", greyed: false, footer: "扩展空闲时自动装上" },
    { name: "off, its switch on", enabled: false, own: true, paused: "disabled", checked: "true", greyed: true, footer: "恢复你原来的自动升级设置" },
    { name: "off, its switch off", enabled: false, own: false, paused: "disabled", checked: "false", greyed: true, footer: "恢复你原来的自动升级设置" },
  ])("$name", async ({ enabled, own, paused, checked, greyed, footer }) => {
    detail = { ...BASE, enabled, status: enabled ? "running" : "disabled", auto_upgrade: own, auto_upgrade_paused: paused };
    act(() => root.render(<Extensions open="example-ui" onOpen={() => {}} />));
    await flush();
    const sw = host.querySelector<HTMLButtonElement>('[role="switch"][aria-label="自动升级"]')!;
    expect(sw.getAttribute("aria-checked")).toBe(checked);
    expect(sw.disabled).toBe(greyed);
    expect(host.textContent).toContain(footer);
  });
});
