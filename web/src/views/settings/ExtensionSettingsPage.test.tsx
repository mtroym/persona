// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ExtensionSettingsPage } from "./ExtensionSettingsPage";
import { Extensions } from "./Extensions";
import type { HubExtensionDetail } from "../../lib/extensions";
import { resetManagerChoice } from "../../lib/manager-choice";
import { setRoster } from "../../lib/managers";

vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast: vi.fn() }));
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
let requests: { url: string; manager: string | null; method: string }[];
let catalog: unknown;
let globalAuto: boolean;
const BASE: HubExtensionDetail = { id: "example-extension", name: "Example extension", kind: "process", source: "installed", status: "running", version: "1.0.0", enabled: true, origin: "market:official/example-extension", auto_upgrade: true };
const ROSTER = { default: "assistant", managers: [{ id: "assistant", name: "Example manager", builtin: true, online: true }] };
const reply = (body: unknown) => ({ ok: true, status: 200, text: async () => JSON.stringify(body), headers: new Headers() });
const flush = async () => { for (let i = 0; i < 12; i++) await act(async () => { await Promise.resolve(); }); };
const button = (label: string) => [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === label || b.getAttribute("aria-label") === label)!;
const sectionNames = () => [...host.querySelectorAll("[data-extension-section]")].map((s) => s.getAttribute("data-extension-section"));
const ORDER = ["enabled", "configuration", "information", "updates", "danger"];

beforeEach(() => {
  requests = [];
  globalAuto = true;
  catalog = { markets: [{ id: "official", name: "Official", error: "" }], extensions: [{ id: BASE.id, market: "official", version: "1.0.0", installed: "1.0.0", upgradable: false }] };
  resetManagerChoice();
  setRoster(ROSTER);
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    requests.push({ url, method: init?.method || "GET", manager: new Headers(init?.headers).get("X-Persona-Manager") });
    if (url.includes("/-/catalog")) return reply(catalog);
    if (url.endsWith("/-/upgrades")) return reply({ auto: globalAuto });
    return reply(BASE);
  }));
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
});
afterEach(() => { act(() => root.unmount()); host.remove(); vi.unstubAllGlobals(); setRoster(null); resetManagerChoice(); });

function render(detail: HubExtensionDetail = BASE, extra: Partial<React.ComponentProps<typeof ExtensionSettingsPage>> = {}) {
  const callbacks = { onEnabled: vi.fn(), onAutoUpgrade: vi.fn(), onUpgrade: vi.fn(), onUninstall: vi.fn(), onChecked: vi.fn(async () => {}) };
  act(() => root.render(<ExtensionSettingsPage detail={detail} busy={false} configuration={<div>Example configuration</div>} {...callbacks} {...extra} />));
  return callbacks;
}

describe("host-owned extension settings", () => {
  it.each([
    { name: "built-in", fields: { source: "builtin", origin: undefined, auto_upgrade: undefined }, supported: false },
    { name: "market", fields: {}, supported: true },
    { name: "local installation", fields: { origin: "file:/example/package", auto_upgrade: undefined }, supported: false },
    { name: "attached", fields: { attached: { online: true }, auto_upgrade: undefined }, supported: false },
    { name: "configured", fields: { source: "hub.json", read_only: true, auto_upgrade: undefined }, supported: false },
    { name: "development", fields: { source: "dev", auto_upgrade: undefined }, supported: false },
    { name: "unknown old manager", fields: { source: undefined, origin: undefined, auto_upgrade: undefined }, supported: false },
  ])("keeps the same sections and disabled lifecycle actions for $name", async ({ fields, supported }) => {
    render({ ...BASE, ...fields }); await flush();
    expect(sectionNames()).toEqual(ORDER);
    expect(button("检查更新").disabled).toBe(!supported);
    expect(button("卸载").disabled).toBe(!supported);
    expect(button("自动升级").disabled).toBe(!supported);
    expect(host.querySelectorAll('[role="switch"][aria-label="启用"]')).toHaveLength(1);
    if (!supported) expect(host.querySelector('[data-extension-section="danger"]')!.textContent).toMatch(/不能卸载|来源端|没有提供/);
  });

  it.each(["builtin", "installed", "hub.json", "dev"])("all %s settings enter the common component through Extensions", async (source) => {
    vi.stubGlobal("fetch", vi.fn(async () => reply({ ...BASE, source, read_only: source === "hub.json", form: { fields: [] }, origin: source === "installed" ? BASE.origin : undefined, auto_upgrade: source === "installed" ? true : undefined })));
    act(() => root.render(<Extensions open={BASE.id} onOpen={() => {}} />)); await flush();
    expect(host.querySelectorAll("[data-extension-settings]")).toHaveLength(1);
    expect(sectionNames()).toEqual(ORDER);
    if (source === "builtin" || source === "installed") expect(host.querySelector('[data-extension-section="configuration"]')!.textContent).toContain("无需配置");
  });

  it("does not call a market installation externally managed when an older manager omits auto-upgrade", async () => {
    render({ ...BASE, auto_upgrade: undefined }); await flush();
    expect(button("检查更新").disabled).toBe(false);
    expect(button("自动升级").disabled).toBe(true);
    expect(host.querySelector('[data-extension-section="updates"]')?.textContent).toContain("没有提供此操作");
    expect(host.querySelector('[data-extension-section="updates"]')?.textContent).not.toContain("不由扩展市场管理");
  });

  it("keeps the enabled switch visible but disabled for configured extensions", async () => {
    const callbacks = render({ ...BASE, source: "hub.json", read_only: true }); await flush();
    const sw = button("启用"); expect(sw.disabled).toBe(true); act(() => sw.click());
    expect(callbacks.onEnabled).not.toHaveBeenCalled();
  });

  it("keeps the saved auto-upgrade preference checked while disabled and explains the pause", async () => {
    render({ ...BASE, enabled: false, auto_upgrade_paused: "disabled" }); await flush();
    expect(button("自动升级").getAttribute("aria-checked")).toBe("true");
    expect(button("自动升级").disabled).toBe(true);
    expect(host.textContent).toContain("扩展关闭期间暂停自动升级");
    expect(document.getElementById(button("自动升级").getAttribute("aria-describedby")!)?.textContent).toContain("自动升级");
  });

  it("distinguishes a removed market from a disabled extension", async () => {
    render({ ...BASE, auto_upgrade_paused: "market_removed" }); await flush();
    expect(host.textContent).toContain("扩展市场已移除");
    expect(host.textContent).not.toContain("扩展关闭期间");
  });

  it("reads the same manager's global preference and does not disguise it as the per-extension value", async () => {
    globalAuto = false;
    render(BASE, { manager: "example-manager" }); await flush();
    expect(requests.find((r) => r.url.endsWith("/-/upgrades"))?.manager).toBe("example-manager");
    expect(button("自动升级").getAttribute("aria-checked")).toBe("true");
    expect(host.textContent).toContain("管理者已关闭自动升级");
  });

  it("checks the selected manager's market without installing and refreshes the detail", async () => {
    const callbacks = render(BASE, { manager: "example-manager" }); await flush();
    await act(async () => button("检查更新").click()); await flush();
    expect(requests.find((r) => r.url.includes("catalog"))).toMatchObject({ manager: "example-manager", method: "GET" });
    expect(requests.find((r) => r.url.includes("catalog"))!.url).toContain("refresh=1");
    expect(callbacks.onChecked).toHaveBeenCalledTimes(1);
    expect(callbacks.onUpgrade).not.toHaveBeenCalled();
    expect(host.textContent).toContain("未发现新版本");
    expect(requests.every((r) => r.method === "GET")).toBe(true);
  });

  it("offers an available checked version through the existing consent route", async () => {
    catalog = { markets: [{ id: "official" }], extensions: [{ id: BASE.id, market: "official", version: "1.1.0", installed: "1.0.0", upgradable: true }] };
    const callbacks = render(); await flush();
    await act(async () => button("检查更新").click()); await flush();
    expect(host.textContent).toContain("1.1.0");
    expect(host.textContent).not.toContain("未发现新版本");
    const upgrade = [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => /升级/.test(b.textContent || "") && b.getAttribute("role") !== "switch")!;
    act(() => upgrade.click()); expect(callbacks.onUpgrade).toHaveBeenCalledWith("official", BASE.id);
  });

  it("reports market failures without claiming the installed version is current", async () => {
    catalog = { markets: [{ id: "official", error: "read failed" }], extensions: [] };
    const callbacks = render(); await flush();
    await act(async () => button("检查更新").click()); await flush();
    expect(host.querySelector('[role="alert"]')?.textContent).toContain("暂不可用");
    expect(host.textContent).not.toContain("未发现新版本");
    expect(callbacks.onChecked).not.toHaveBeenCalled(); expect(button("检查更新").disabled).toBe(false);
  });

  it("shows unknown versions and approved/reported attached versions without exposing socket paths", async () => {
    render({ ...BASE, version: undefined, attached: { online: true, version: "2.0.0" }, origin: "remote:/example/socket" }); await flush();
    const info = host.querySelector('[data-extension-section="information"]')!;
    expect(info.textContent).toContain("未提供"); expect(info.textContent).toContain("程序自报版本（未批准）");
    expect(info.textContent).toContain("2.0.0"); expect(info.textContent).not.toContain("/example/socket");
  });

  it("opens the common uninstall confirmation with version and keeps data unless explicitly changed", async () => {
    const onBack = vi.fn();
    act(() => root.render(<Extensions open={BASE.id} onOpen={onBack} />)); await flush();
    act(() => button("卸载").click()); await flush();
    let dialog = document.querySelector('[role="dialog"]')!;
    expect(dialog.textContent).toContain(BASE.name); expect(dialog.textContent).toContain("1.0.0");
    expect(dialog.querySelector<HTMLInputElement>('input[type="checkbox"]')?.checked).toBe(true);
    act(() => [...dialog.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === "取消")!.click());
    expect(requests.some((r) => r.method === "DELETE")).toBe(false);
    act(() => button("卸载").click()); await flush();
    dialog = document.querySelector('[role="dialog"]')!;
    await act(async () => [...dialog.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === "卸载")!.click()); await flush();
    expect(requests.find((r) => r.method === "DELETE")?.url).toContain("keep_data=1");
    expect(onBack).toHaveBeenCalledWith(null);
  });

  it("disables lifecycle actions while offline without hiding the configuration", async () => {
    const callbacks = render(BASE, { offline: true }); await flush();
    for (const label of ["启用", "自动升级", "检查更新", "卸载"]) expect(button(label).disabled).toBe(true);
    expect(host.textContent).toContain("管理者离线"); expect(host.textContent).toContain("Example configuration");
    act(() => button("卸载").click()); expect(callbacks.onUninstall).not.toHaveBeenCalled();
  });
});
