// @vitest-environment jsdom
// Another manager's extension page (docs/assistant/managers.md "Clients"): the connectors it contributes, its channel and its
// upgrade are that manager's, not the default's of the same id; a link that names no manager waits for the roster.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../lib/store", async (original) => {
  const real = await original<typeof import("../../lib/store")>();
  return { ...real, useStore: <T,>(select: (s: ReturnType<typeof real.getState>) => T): T => select({ ...real.getState(), features: ["channels", "extension_markets"] }) };
});
vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast: vi.fn() }));

import { managerHeaderFor, resetManagerChoice } from "../../lib/manager-choice";
import { loadRoster, setRoster } from "../../lib/managers";
import { ChannelPage } from "./Channels";
import { Connectors } from "./Connectors";
import { Extensions } from "./Extensions";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const ROSTER = {
  default: "assistant",
  managers: [
    { id: "assistant", name: "助手", online: true, builtin: true },
    { id: "m-1", name: "工作", online: true },
  ],
};
const DETAIL = {
  id: "example-wechat", name: "示例微信", kind: "process", status: "running", source: "installed", origin: "market:official/example-wechat", enabled: true,
  contributes: { channel: ["wechat"], connectors: ["wechat"] },
  auto_upgrade: false, upgrade: { market: "official", id: "example-wechat", from: "1.0.0", to: "1.1.0", state: "off" },
};
const CHANNEL = { id: "wechat", name: "微信", kind: "im", source: "plugin", installed: true, enabled: true, state: "ready" };

let host: HTMLDivElement;
let root: Root;
let calls: { url: string; method: string; header: string | null; body?: string }[] = [];
let roster: (() => void) | null = null; // set: the roster's answer waits until it is called

const reply = (body: unknown, status = 200) => ({ ok: status < 400, status, text: async () => JSON.stringify(body), headers: new Headers() });
const flush = async () => {
  for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); });
};

beforeEach(() => {
  calls = [];
  roster = null;
  resetManagerChoice();
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    const header = new Headers(init?.headers).get("X-Persona-Manager");
    calls.push({ url, method: init?.method ?? "GET", header, body: init?.body as string | undefined });
    if (url.endsWith("/managers")) {
      if (roster === null) return reply(ROSTER);
      return new Promise((done) => (roster = () => done(reply(ROSTER))));
    }
    if (/\/(plugins|extensions)\/example-wechat\/-\/$/.test(url)) return reply(DETAIL);
    if (url.endsWith("/connectors")) {
      // the merged list: each manager's copy of the connector, stamped
      return reply({ connectors: [
        { id: "wechat", name: "微信", status: "ok", account: "home@example.com", inbox: false, manager: "assistant" },
        { id: "wechat", name: "微信", status: "ok", account: "work@example.com", inbox: true, manager: "m-1" },
      ] });
    }
    if (url.includes("/connectors/wechat/settings")) return reply({ id: "wechat", name: "微信", status: "ok", inbox: false });
    if (url.endsWith("/channels")) return reply({ channels: header === "m-1" ? [CHANNEL] : [] });
    if (url.endsWith("/channels/wechat")) return reply({ ...CHANNEL, capabilities: {}, bind: "none", waiting: { answers: 0, asks: 0 } });
    if (url.includes("/-/catalog")) return reply({ markets: [{ id: "official", name: "官方", official: true }], extensions: [{ id: "example-wechat", name: "示例微信", market: "official", version: "1.1.0", installed: "1.0.0" }] });
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

const button = (text: string) => [...host.querySelectorAll<HTMLElement>("button, [role=button], [role=switch]")].find((b) => b.textContent?.includes(text) || b.getAttribute("aria-label") === text);

describe("another manager's extension page", () => {
  it("shows and changes that manager's connector, and links to its connector and channel pages", async () => {
    setRoster(ROSTER);
    const onChannel = vi.fn();
    const onConnector = vi.fn();
    act(() => root.render(<Extensions open="example-wechat@m-1" onOpen={() => {}} onChannel={onChannel} onConnector={onConnector} />));
    await flush();
    expect(calls.find((c) => /example-wechat\/-\/$/.test(c.url))?.header).toBe("m-1");
    // m-1's copy (inbox on), not the default's (off)
    const sw = host.querySelector<HTMLButtonElement>('[role="switch"][aria-label="进收件箱"]')!;
    expect(sw.getAttribute("aria-checked")).toBe("true");
    await act(async () => sw.click());
    await flush();
    const put = calls.find((c) => c.method === "PUT" && c.url.includes("/connectors/wechat/settings"))!;
    expect(put.header).toBe("m-1");
    act(() => button("连接器设置")!.click());
    expect(onConnector).toHaveBeenCalledWith("wechat", "m-1");
    // its channel is read from m-1 and opens as m-1's
    expect(calls.some((c) => c.url.endsWith("/channels") && c.header === "m-1")).toBe(true);
    act(() => button("在 设置 › 渠道 里设置微信")!.click());
    expect(onChannel).toHaveBeenCalledWith("wechat", "m-1");
  });

  it("upgrades that manager's copy: the market page names it on its requests and on the consent", async () => {
    setRoster(ROSTER);
    const onOpen = vi.fn();
    act(() => root.render(<Extensions open="example-wechat@m-1" onOpen={onOpen} />));
    await flush();
    act(() => button("去升级")!.click());
    expect(onOpen).toHaveBeenLastCalledWith("-market/official/example-wechat@m-1");
    act(() => root.render(<Extensions open="-market/official/example-wechat@m-1" onOpen={onOpen} />));
    await flush();
    expect(calls.filter((c) => c.url.includes("/-/catalog")).map((c) => c.header)).toEqual(["m-1"]);
    expect(host.querySelector("[data-for-manager]")?.textContent).toContain("工作");
    // back goes to that manager's extension, not to the default's catalog
    act(() => button("扩展市场")?.click());
    expect(onOpen).toHaveBeenLastCalledWith("example-wechat@m-1");
  });

  it("the default's own page names no manager anywhere", async () => {
    setRoster(ROSTER);
    act(() => root.render(<Extensions open="example-wechat" onOpen={() => {}} onChannel={() => {}} onConnector={() => {}} />));
    await flush();
    const sw = host.querySelector<HTMLButtonElement>('[role="switch"][aria-label="进收件箱"]')!;
    expect(sw.getAttribute("aria-checked")).toBe("false");
    expect(calls.filter((c) => !c.url.endsWith("/managers")).every((c) => c.header === null)).toBe(true);
  });

  it("waits for the roster before drawing a link that names no manager", async () => {
    roster = () => {}; // the roster's answer is on its way
    void loadRoster();
    act(() => root.render(<Extensions open="example-wechat" onOpen={() => {}} />));
    await flush();
    expect(calls.some((c) => /example-wechat\/-\/$/.test(c.url))).toBe(false);
    expect(host.textContent).not.toContain("示例微信");
    await act(async () => roster!());
    await flush();
    expect(calls.some((c) => /example-wechat\/-\/$/.test(c.url))).toBe(true);
  });
});

describe("another manager's connector and channel pages", () => {
  it("shows that manager's copy of the connector, and names it on the page's requests while open", async () => {
    setRoster(ROSTER);
    act(() => root.render(<Connectors open="wechat@m-1" onOpen={() => {}} />));
    await flush();
    expect(host.textContent).toContain("work@example.com");
    expect(host.textContent).not.toContain("home@example.com");
    expect(managerHeaderFor("/connectors/wechat/settings")).toBe("m-1");
    act(() => root.render(<Connectors open="wechat" onOpen={() => {}} />));
    await flush();
    expect(host.textContent).toContain("home@example.com");
    expect(managerHeaderFor("/connectors/wechat/settings")).toBeUndefined();
  });

  it("reads another manager's channel with its name", async () => {
    setRoster(ROSTER);
    act(() => root.render(<ChannelPage id="wechat" manager="m-1" onBack={() => {}} onSettings={() => {}} />));
    await flush();
    expect(calls.find((c) => c.url.endsWith("/channels/wechat"))?.header).toBe("m-1");
  });
});
