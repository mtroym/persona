// @vitest-environment jsdom
// Settings category navigation keeps the existing URLs and phone history while moving beside the content.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const viewport = vi.hoisted(() => ({ phone: false }));
vi.hoisted(() => {
  window.matchMedia = ((query: string) => ({ get matches() { return viewport.phone; }, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
vi.mock("../lib/store", async (original) => ({
  ...(await original<typeof import("../lib/store")>()),
  useStore: (select: (state: { features: string[]; machines: unknown[]; conn: string; link: { networkUp: boolean; restarting: boolean }; hubName: string; mesh: unknown[] }) => unknown) => select({ features: ["channels", "extension_manager"], machines: [], conn: "online", link: { networkUp: true, restarting: false }, hubName: "", mesh: [] }),
}));
vi.mock("./settings/ShortcutSettings", () => ({ ShortcutSettings: () => <div data-page="shortcuts" /> }));
vi.mock("./settings/NotificationSettings", () => ({ NotificationSettings: () => <div data-page="notifications" /> }));
vi.mock("./settings/ModelConfiguration", () => ({ ModelConfiguration: ({ open }: { open: string | null }) => <div data-page="model-config" data-open={open} /> }));
vi.mock("./settings/TranslationSettings", () => ({ TranslationSettings: () => <div data-page="translation" /> }));
vi.mock("./settings/AssistantSettings", () => ({ AssistantSettings: () => <div data-page="persona" /> }));
vi.mock("./settings/SidebarPages", () => ({ SidebarPagesGroup: () => <div data-page="sidebar-pages" /> }));
vi.mock("../lib/pwa", async (original) => ({ ...(await original<typeof import("../lib/pwa")>()), useInstall: () => "installed" }));
vi.mock("../lib/api", async (original) => ({ ...(await original<typeof import("../lib/api")>()), api: () => Promise.resolve({ accounts: [] }) }));
vi.mock("./settings/AgentSettings", () => ({ AgentSettings: ({ agent }: { agent: string }) => <div data-page="agent" data-agent={agent} /> }));
vi.mock("./settings/Extensions", () => ({ Extensions: ({ open }: { open: string | null }) => <div data-page="extensions" data-open={open} /> }));
vi.mock("./settings/FeishuBot", () => ({ FeishuBot: () => null }));
vi.mock("./settings/HubConnector", () => ({ ConnectorList: () => <div data-page="connector-list" />, ConnectorDetail: () => <div data-page="connector-detail" /> }));
vi.mock("./settings/SentSettings", () => ({ SentSettings: () => <div data-page="sent" /> }));

import { t } from "../i18n";
import { Settings } from "./Settings";
import { FROM_LIST } from "./settings/nav";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let host: HTMLDivElement;
let root: Root;
beforeEach(() => {
  viewport.phone = false;
  localStorage.clear();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});
const mount = async (url: string) => {
  history.replaceState(null, "", url);
  await act(async () => root.render(<Settings onToggleSidebar={() => {}} />));
};
const nav = () => host.querySelector("nav")!;
const category = (label: string) => [...nav().querySelectorAll("button")].find((button) => button.textContent?.startsWith(label))!;

describe("Settings category navigation", () => {
  it("places desktop navigation beside a separately focusable, named content region", async () => {
    await mount("/settings?tab=shortcuts");
    const region = host.querySelector('[role="region"]')!;
    expect(host.querySelector("header nav")).toBeNull();
    expect(nav().parentElement!.nextElementSibling).toBe(region);
    expect(nav().getAttribute("aria-label")).toBe(t("settings.categories"));
    expect(nav().classList.contains("overflow-y-auto")).toBe(true);
    expect(region.classList.contains("overflow-y-auto")).toBe(true);
    expect(region.getAttribute("tabindex")).toBe("0");
    expect(region.getAttribute("aria-label")).toBe(t("settings-nav.tab_shortcuts"));
    for (const button of nav().querySelectorAll("button")) {
      expect(button.type).toBe("button");
      expect(button.getAttribute("aria-controls")).toBe(region.id);
      expect(button.tabIndex).toBe(0);
    }
    expect(nav().querySelectorAll('[aria-current="page"]')).toHaveLength(1);
    expect(category(t("settings-nav.tab_shortcuts")).getAttribute("aria-current")).toBe("page");
  });

  it("changes the URL, current category and region together, and restores them on reload", async () => {
    await mount("/settings?tab=shortcuts");
    await act(async () => category(t("settings-nav.tab_notifications")).click());
    expect(location.search).toBe("?tab=notifications");
    expect(host.querySelector('[data-page="notifications"]')).not.toBeNull();
    expect(category(t("settings-nav.tab_notifications")).getAttribute("aria-current")).toBe("page");
    expect(category(t("settings-nav.tab_shortcuts")).hasAttribute("aria-current")).toBe(false);
    expect(host.querySelector('[role="region"]')?.getAttribute("aria-label")).toBe(t("settings-nav.tab_notifications"));
    await act(async () => root.render(null));
    await act(async () => root.render(<Settings onToggleSidebar={() => {}} />));
    expect(host.querySelector('[data-page="notifications"]')).not.toBeNull();
    expect(category(t("settings-nav.tab_notifications")).getAttribute("aria-current")).toBe("page");
  });

  it("keeps deep-linked configuration purpose and manager when choosing its category again", async () => {
    await mount("/settings?tab=model-config&purpose=agent.triage&manager=example");
    expect(location.search).toBe("?tab=models&purpose=agent.triage&manager=example");
    expect(location.hash).toBe("#settings-model-config");
    expect(host.querySelector('[data-page="model-config"]')?.getAttribute("data-open")).toBe("agent.triage");
    await act(async () => category(t("settings-nav.tab_models")).click());
    expect(location.search).toBe("?tab=models&manager=example");
  });

  it("redirects old agent, translation, and assistant links to their new categories", async () => {
    await mount("/settings?tab=codex");
    expect(location.pathname + location.search + location.hash).toBe("/settings?tab=agents#settings-codex");
    expect([...host.querySelectorAll('[data-page="agent"]')].map((el) => el.getAttribute("data-agent"))).toEqual(["claude", "codex", "agy", "omp"]);
    await act(async () => root.render(null));
    await mount("/settings?tab=translate");
    expect(location.pathname + location.search + location.hash).toBe("/settings?tab=general#settings-translation");
    expect(host.querySelector('[data-page="translation"]')).not.toBeNull();
    await act(async () => root.render(null));
    await mount("/settings?tab=assistant");
    expect(location.search).toBe("?tab=persona");
    expect(host.querySelector('[data-page="persona"]')).not.toBeNull();
  });

  it("scrolls a shared old link to its section in the combined page", async () => {
    const scroll = vi.fn();
    Element.prototype.scrollIntoView = scroll;
    vi.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} });
    await mount("/settings?tab=agy");
    expect(location.hash).toBe("#settings-agy");
    await act(async () => {});
    expect(scroll).toHaveBeenCalledWith({ block: "start" });
  });

  it("preserves legacy extension deep links", async () => {
    await mount("/settings?tab=plugins&plugin=example");
    expect(host.querySelector('[data-page="extensions"]')?.getAttribute("data-open")).toBe("example");
    expect(category(t("settings-nav.tab_extensions")).getAttribute("aria-current")).toBe("page");
  });

  it("opens phone categories above the list and follows browser Back to that list", async () => {
    viewport.phone = true;
    await mount("/settings");
    expect(host.querySelector('[role="region"]')).toBeNull();
    expect(host.querySelector("nav")?.classList.contains("w-56")).toBe(false);
    await act(async () => category(t("settings-nav.tab_shortcuts")).click());
    expect(location.search).toBe("?tab=shortcuts");
    expect(history.state).toEqual({ [FROM_LIST]: true });
    expect(host.querySelector("nav")).toBeNull();
    expect(host.querySelector("h1")?.textContent).toBe(t("settings-nav.tab_shortcuts"));
    const back = host.querySelector<HTMLButtonElement>(`button[aria-label="${t("settings.back")}"]`)!;
    const historyBack = vi.spyOn(history, "back").mockImplementation(() => {});
    await act(async () => back.click());
    expect(historyBack).toHaveBeenCalledOnce();
    await act(async () => {
      history.replaceState(null, "", "/settings");
      dispatchEvent(new PopStateEvent("popstate"));
    });
    expect(host.querySelector("nav")).not.toBeNull();
    expect(host.querySelector("h1")?.textContent).toBe(t("settings.title"));
  });

  it.each([false, true])("opens Sent from the Connectors bottom row and returns to Connectors (phone: %s)", async (phone) => {
    viewport.phone = phone;
    await mount("/settings?tab=connectors");
    expect(nav()?.textContent ?? "").not.toContain(t("settings-sent.entry_label"));
    const entry = host.querySelector('[data-settings-group="sent"]')!;
    expect(entry.parentElement?.lastElementChild).toBe(entry);
    expect(entry.textContent).toContain(t("settings-sent.hint"));
    await act(async () => entry.querySelector("button")!.click());
    expect(location.search).toBe("?tab=connectors&connector=-sent");
    expect(host.querySelector('[data-page="sent"]')).not.toBeNull();
    expect(host.querySelector('[data-page="connector-detail"]')).toBeNull();
    expect(host.querySelector('[data-settings-group="sent"]')).toBeNull();
    const back = [...host.querySelectorAll<HTMLButtonElement>("button")].find(b => b.textContent === t("settings-hubconnector.back"))!;
    await act(async () => back.click());
    expect(location.search).toBe("?tab=connectors");
    expect(host.querySelector('[data-settings-group="sent"]')).not.toBeNull();
  });

  it.each(["/settings?tab=sent", "/settings/sent", "/settings/sent/"])("redirects %s to the secondary Sent page", async (url) => {
    await mount(url);
    expect(location.pathname + location.search).toBe("/settings?tab=connectors&connector=-sent");
    expect(host.querySelector('[data-page="sent"]')).not.toBeNull();
    expect(category(t("settings-nav.tab_connectors")).getAttribute("aria-current")).toBe("page");
  });

  it("normalizes a legacy Sent URL reached by browser history while Settings stays mounted", async () => {
    await mount("/settings?tab=connectors");
    await act(async () => {
      history.replaceState(null, "", "/settings/sent");
      dispatchEvent(new PopStateEvent("popstate"));
    });
    expect(location.pathname + location.search).toBe("/settings?tab=connectors&connector=-sent");
    expect(host.querySelector('[data-page="sent"]')).not.toBeNull();
  });

  it("returns to the phone list from a shared detail URL without navigating away", async () => {
    viewport.phone = true;
    await mount("/settings?tab=notifications");
    await act(async () => host.querySelector<HTMLButtonElement>(`button[aria-label="${t("settings.back")}"]`)!.click());
    expect(location.pathname + location.search).toBe("/settings");
    expect(host.querySelector("nav")).not.toBeNull();
  });
});
