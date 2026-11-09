import { managedProvider } from "../lib/compatibility";
// Settings (docs/web/web.md): appearance, where the assistant runs, "Models" (every launcher: rc-file and PATH ones synced, the
// built-in claude, Persona ones kept by the hub — docs/integrations/launchers.md), this browser and the other devices, about. The same data as the app's Settings.
import { BASE, stripBase } from "../lib/base";
import { href } from "../lib/router";
import { useScrollMemory } from "../lib/scroll-memory";
import { useEffect, useId, useMemo, useRef, useState } from "react";
import { api, enc, errorText } from "../lib/api";
import { splitDevices } from "../lib/devices";
import { ago } from "../lib/format";
import { install, useInstall, type InstallState } from "../lib/pwa";
import { useStore } from "../lib/store";
import { Button, cx, Dialog, EASE, IconButton, Loading, Spinner, toast } from "../ui/atoms";
import { Icon, type IconName } from "../ui/icons";
import { ExtensionThemePicker } from "../ui/ExtensionThemePicker";
import { setTheme, useTheme, type Theme } from "./theme";
import { Group, Row, Pill, RowConfirm, Switch } from "./settings/SettingsParts";
import { LanguageRow } from "./settings/LanguageRow";
import { TextSizeRow, ThemeColorPicker } from "./settings/LookSettings";
import { setReplyMotionEnabled, useReplyMotionSetting } from "../ui/stream/motion";
import { setSidebarTerminals, useSidebarTerminals } from "../lib/sidebar-terminals";
import { setAgentDetails, setContextAsUsed, useAgentDetails, useContextAsUsed } from "../lib/context-usage";
import { HubList } from "./Hubs";
import { useDisconnect } from "./DisconnectBrowser";
import { ModelConfiguration } from "./settings/ModelConfiguration";
import { AgentSettings } from "./settings/AgentSettings";
import { SidebarPagesGroup } from "./settings/SidebarPages";
import { AgentGroup } from "./settings/AgentGroup";
import { AssistantSettings } from "./settings/AssistantSettings";
import { PersonaPicker } from "./assistant/Persona";
import { Connectors } from "./settings/Connectors";
import { TriageSettings } from "./settings/TriageSettings";
import { TranslationSettings } from "./settings/TranslationSettings";
import { Extensions } from "./settings/Extensions";
import { ShortcutSettings } from "./settings/ShortcutSettings";
import { NotificationSettings } from "./settings/NotificationSettings";
import { ChannelList, ChannelPage } from "./settings/Channels";
import { ConnectDevice } from "./settings/ConnectDevice";
import { settingsTarget } from "../lib/channels";
import { extensionsRoute, routeSub } from "../lib/extension-market";
import { ownedSub, parseOwnedSub } from "../lib/manager-choice";
import { HubSettings } from "./settings/HubSettings";
import { ManagersSettings } from "./settings/ManagersSettings";
import { SentSettings } from "./settings/SentSettings";
import { LauncherQuota } from "./settings/LauncherQuota";
import { LauncherExtensionSections } from "./settings/ExtensionView";
import { hubHasExt } from "../lib/extension-api";
import { hostOf, type ProviderView } from "../lib/providers";
import { ProviderWizard } from "./settings/ProviderWizard";
import { ProviderImport } from "./settings/ProviderImport";
import { builtinRows, loadLaunchers, rcFilesOf, rcGroups, syncStateText, type LauncherSync } from "../lib/launcher-list";
import { TerminalModeRow } from "./settings/TerminalModeSetting";
import { FROM_LIST, NAV_GROUPS, openedFromList, redirectLegacySettings, searchSettings, settingsAt, settingsHref, TAB_INFO, TABS, type SearchGroup, type SettingsSection, type Tab } from "./settings/nav";
import { SHEET_QUERY, useMedia } from "../ui/overlay";
import { formatList, t } from "../i18n";

// ── types (docs/integrations/launchers.md) ───────────────────────────

interface LauncherDetail {
  agent?: string;
  base_url?: string;
  auth?: string;
  models?: { role: string; env: string; model: string }[];
  context_window?: number;
  compact_window?: number;
  effort?: string;
  timeout_ms?: number;
  args?: string[];
  env?: { key: string; value: string }[];
  error?: string;
}

/** GET /api/v1/launchers' entry: a managed provider's fields (lib/providers.ts) plus the ~/.zshrc ones'. */
interface LauncherView extends ProviderView {
  /** zshrc: an rc file's (~/.zshrc, ~/.bashrc); path: a wrapper script on the computer's PATH (#5954); builtin: plain claude (no edit, can't be hidden). */
  source: "zshrc" | "path" | "persona" | "agy" | "builtin" | string;
  agent?: "claude" | "codex" | "agy" | string;
  machines?: string[];
  hidden?: boolean;
  /** zshrc: the rc files it was found in; absent from older hubs (all ~/.zshrc). */
  rc_files?: string[];
  /** Machines of `machines` that did not answer this time: their entry is from their last list. */
  stale?: string[];
  /** builtin: Claude Code's version per machine. */
  versions?: Record<string, string>;
  definitions?: Record<string, string>;
  details?: Record<string, LauncherDetail>;
}

interface LauncherCheck {
  ok: boolean;
  agent?: string;
  model?: string;
  answer?: string;
  millis: number;
  error?: string;
}

interface DeviceToken {
  id: string;
  name: string;
  kind: string;
  role: string;
  created_at?: string;
  last_seen?: string;
  /** Set once revoked: the hub keeps listing it (lib/devices.ts). */
  revoked_at?: string;
}

const enLauncher = (n: string) => `/launchers/${enc(n)}`;

export function tokens(n: number): string {
  if (n >= 1_000_000 && n % 100_000 === 0) return `${n % 1_000_000 === 0 ? n / 1_000_000 : (n / 1_000_000).toFixed(1)}M`;
  if (n >= 1000) return `${Math.floor(n / 1000)}K`;
  return String(n);
}

// ── page ────────────────────────────────────────────────

export function Settings({ onToggleSidebar }: { onToggleSidebar: () => void }) {
  const phone = useMedia(SHEET_QUERY);
  // list pages (channels / connectors / extensions) keep their list beside the open item from this width
  const twoCol = useMedia("(min-width: 900px)");
  // Keep the connection pill (App.tsx) below the page header, including at larger text sizes.
  const header = useRef<HTMLElement>(null);
  useEffect(() => {
    const el = header.current;
    const box = el?.closest("main");
    if (!el || !box || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(() => box.style.setProperty("--page-header", `${el.offsetHeight}px`));
    ro.observe(el);
    return () => {
      ro.disconnect();
      box.style.removeProperty("--page-header");
    };
  }, []);
  // The category (null: none named, the phone's list) and the purpose (model configuration) or extension ("Extensions") it has open.
  const [at, setAt] = useState(() => {
    const redirect = redirectLegacySettings(location.search, location.hash);
    if (redirect) history.replaceState(history.state, "", BASE + redirect);
    return settingsAt(location.search, location.hash);
  });
  // Back from a category opened from the list: the URL went back, the page follows it.
  useEffect(() => {
    const back = () => {
      if (stripBase(location.pathname) !== "/settings") return;
      const now = settingsAt(location.search, location.hash);
      setAt((was) => (was.tab === now.tab && was.sub === now.sub && was.section === now.section ? was : now));
    };
    addEventListener("popstate", back);
    return () => removeEventListener("popstate", back);
  }, []);
  const listed = phone && !at.tab;
  // one scroller shows the list and every page: each keeps its own spot, and Back to the list lands where it was (#5133)
  const scroller = useRef<HTMLDivElement>(null);
  useScrollMemory(scroller, listed ? "settings" : `settings/${at.tab ?? "general"}/${at.sub ?? ""}`);
  const tab: Tab = at.tab ?? "general";
  const { sub } = at;
  useEffect(() => {
    if (!at.section || listed) return;
    const section = document.getElementById(`settings-${at.section}`);
    if (!section) return;
    section.scrollIntoView?.({ block: "start" });
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(() => section.scrollIntoView?.({ block: "start" }));
    observer.observe(section);
    if (scroller.current) observer.observe(scroller.current);
    const timer = setTimeout(() => observer.disconnect(), 500);
    return () => { clearTimeout(timer); observer.disconnect(); };
  }, [tab, at.section, listed]);
  // "Extensions" only on a hub with the extension base (docs/extensions/extensions.md "The hub's API"); a link to it still opens it.
  const hasExtensions = useStore((s) => hubHasExt(s.features, "manager"));
  const hasChannels = useStore((s) => s.features.includes("channels"));
  const shown = (k: Tab) => (k !== "extensions" || hasExtensions || tab === "extensions") && (k !== "channels" || hasChannels || tab === "channels");
  const contentId = useId();
  const configurationOwner = useRef<string | null>(new URLSearchParams(location.search).get("manager"));
  const pick = (k: Tab, id: string | null = null, section?: SettingsSection) => {
    configurationOwner.current = new URLSearchParams(location.search).get("manager") ?? configurationOwner.current;
    setAt({ tab: k, sub: id, section: section ?? null });
    const to = settingsHref(k, id, phone, k === "persona" || k === "models" || k === "hub" ? configurationOwner.current : null, section);
    // from the phone's list the category goes on top of it, so Back returns to the list
    if (listed) history.pushState({ [FROM_LIST]: true }, "", BASE + to);
    else history.replaceState(openedFromList(history.state) ? history.state : null, "", BASE + to);
  };
  const toList = () => {
    configurationOwner.current = new URLSearchParams(location.search).get("manager") ?? configurationOwner.current;
    if (openedFromList(history.state)) return history.back();
    history.replaceState(null, "", href({ name: "settings" }));
    setAt({ tab: null, sub: null, section: null });
  };
  // Settings search: filters the sidebar categories, and jumps to a group inside a page (scroll + brief highlight).
  const [query, setQuery] = useState("");
  const search = searchSettings(query);
  const [scrollAnchor, setScrollAnchor] = useState<string | null>(null);
  useEffect(() => {
    if (!scrollAnchor) return;
    const el = scroller.current?.querySelector<HTMLElement>(`[data-settings-group="${scrollAnchor}"]`) ?? document.getElementById(scrollAnchor);
    if (!el) {
      setScrollAnchor(null);
      return;
    }
    el.scrollIntoView({ block: "start" });
    const card = el.querySelector<HTMLElement>(":scope > div") ?? el;
    const prev = card.style.boxShadow;
    card.style.boxShadow = "inset 0 0 0 2px var(--accent)";
    const timer = setTimeout(() => {
      card.style.boxShadow = prev;
      setScrollAnchor(null);
    }, 1200);
    return () => {
      clearTimeout(timer);
      card.style.boxShadow = prev;
    };
  }, [scrollAnchor, tab]);
  const goGroup = (g: SearchGroup) => {
    setQuery("");
    pick(g.tab, null);
    setScrollAnchor(g.anchor);
  };
  return (
    <div className="flex h-full min-h-0 min-w-0 flex-col overflow-hidden">
      <header ref={header} className="flex min-h-14 shrink-0 items-center gap-2 border-b border-line px-3 py-2 sm:px-4">
        {phone && at.tab ? (
          <>
            <IconButton icon="chevronLeft" label={t("settings.back")} onClick={toList} />
            <h1 className="min-w-0 truncate text-[14px] font-semibold text-ink">{TAB_INFO[tab].label}</h1>
          </>
        ) : (
          <>
            <IconButton icon="sidebar" label={t("settings.sidebar")} onClick={onToggleSidebar} className="lg:hidden" />
            <h1 className="shrink-0 whitespace-nowrap text-[14px] font-semibold text-ink">{t("settings.title")}</h1>
          </>
        )}
      </header>
      <div className="flex min-h-0 min-w-0 flex-1">
        {!phone && (
          <SettingsNav tab={tab} query={query} onQuery={setQuery} search={search} shown={shown} onPick={pick} onGroup={goGroup} contentId={contentId} />
        )}
        <div ref={scroller} id={contentId} role={listed ? undefined : "region"} aria-label={listed ? undefined : TAB_INFO[tab].label} tabIndex={0} className="min-h-0 min-w-0 flex-1 overflow-y-auto overscroll-contain focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent">
          {listed ? (
            <div className="px-4 py-5" style={{ animation: `fade-up 320ms ${EASE} both` }}>
              <SettingsList shown={shown} onPick={pick} />
            </div>
          ) : (
            <div key={tab} className={cx("mx-auto px-5 py-6 sm:px-8", tab === "channels" || tab === "connectors" || tab === "extensions" ? "max-w-[1120px]" : "max-w-[760px]")} style={{ animation: `fade-up 320ms ${EASE} both` }}>
              {tab === "general" && <General onModel={() => pick("models", "translate", "model-config")} />}
              {tab === "persona" && <PersonaSettings onTriageModel={() => pick("models", "agent.triage", "model-config")} onExtensions={() => pick("extensions")} />}
              {tab === "notifications" && <NotificationSettings />}
              {tab === "models" && <>
                <section id="settings-providers" className="scroll-mt-4">
                  <h2 className="mb-2 px-1 text-[15px] font-semibold text-ink">{TAB_INFO.models.label}</h2>
                  <Models />
                </section>
                <section id="settings-model-config" className="scroll-mt-4">
                  <ModelConfiguration open={sub} onOpen={(id) => pick("models", id, "model-config")} onProviders={() => pick("models", null, "providers")} />
                </section>
              </>}
              {tab === "agents" && (["claude", "codex", "agy", "omp"] as const).map((agent) => (
                <section key={agent} id={`settings-${agent}`} className="scroll-mt-4">
                  <h2 className="mb-2 px-1 text-[15px] font-semibold text-ink">{agent === "claude" ? "Claude Code" : agent === "codex" ? "Codex" : agent === "agy" ? "Antigravity" : "omp"}</h2>
                  <AgentSettings agent={agent} />
                </section>
              ))}
              {tab === "channels" && (sub ? (twoCol ? (
                <div className="flex gap-6">
                  <div className="w-[300px] shrink-0"><ChannelList onOpen={(id) => pick("channels", id)} onMarketItem={(market, id) => pick("extensions", routeSub({ kind: "item", market, id }))} /></div>
                  <div className="min-w-0 flex-1"><ChannelPage key={sub} {...parseOwnedSub(sub)} onBack={() => pick("channels")} onSettings={(url) => {
                    const target = settingsTarget(url);
                    if (target && TABS.includes(target.tab as Tab)) pick(target.tab as Tab, target.sub);
                  }} /></div>
                </div>
              ) : (
                <ChannelPage key={sub} {...parseOwnedSub(sub)} onBack={() => pick("channels")} onSettings={(url) => {
                  const target = settingsTarget(url);
                  if (target && TABS.includes(target.tab as Tab)) pick(target.tab as Tab, target.sub);
                }} />
              )) : (
                <ChannelList onOpen={(id) => pick("channels", id)} onMarketItem={(market, id) => pick("extensions", routeSub({ kind: "item", market, id }))} />
              ))}
              {tab === "connectors" && (sub && sub !== "feishu-bot" && twoCol ? (
                <div className="flex gap-6">
                  <div className="w-[300px] shrink-0"><Connectors open={null} onOpen={(id) => pick("connectors", id)} onChannel={(id, manager) => pick("channels", ownedSub(id, manager))} /></div>
                  <div className="min-w-0 flex-1"><Connectors open={sub} onOpen={(id) => pick("connectors", id)} onChannel={(id, manager) => pick("channels", ownedSub(id, manager))} /></div>
                </div>
              ) : (
                <Connectors open={sub} onOpen={(id) => pick("connectors", id)} onChannel={(id, manager) => pick("channels", ownedSub(id, manager))} />
              ))}
              {tab === "sent" && <SentSettings />}
              {tab === "extensions" && (extensionsRoute(sub).kind === "extension" && twoCol ? (
                <div className="flex gap-6">
                  <div className="w-[300px] shrink-0"><Extensions open={null} onOpen={(id) => pick("extensions", id)} onChannel={(id, manager) => pick("channels", ownedSub(id, manager))} onConnector={(id, manager) => pick("connectors", ownedSub(id, manager))} /></div>
                  <div className="min-w-0 flex-1"><Extensions open={sub} onOpen={(id) => pick("extensions", id)} onChannel={(id, manager) => pick("channels", ownedSub(id, manager))} onConnector={(id, manager) => pick("connectors", ownedSub(id, manager))} /></div>
                </div>
              ) : (
                <Extensions open={sub} onOpen={(id) => pick("extensions", id)} onChannel={(id, manager) => pick("channels", ownedSub(id, manager))} onConnector={(id, manager) => pick("connectors", ownedSub(id, manager))} />
              ))}
              {tab === "shortcuts" && <ShortcutSettings />}
              {tab === "devices" && <Devices />}
              {tab === "hub" && <HubSettings />}
              {tab === "managers" && <ManagersSettings />}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

/** The desktop left sidebar: a sticky "Settings" title and search box, then the grouped categories. */
function SettingsNav({ tab, query, onQuery, search, shown, onPick, onGroup, contentId }: {
  tab: Tab;
  query: string;
  onQuery: (q: string) => void;
  search: ReturnType<typeof searchSettings>;
  shown: (k: Tab) => boolean;
  onPick: (k: Tab) => void;
  onGroup: (g: SearchGroup) => void;
  contentId: string;
}) {
  const nav = useRef<HTMLElement>(null);
  // A deep link or a width change can select a category below the sidebar's fold.
  useEffect(() => {
    nav.current?.querySelector('[aria-current="page"]')?.scrollIntoView?.({ block: "nearest" });
  }, [tab, query]);
  const tabs = search.tabs.filter(shown);
  const groups = search.groups.filter((g) => shown(g.tab));
  const none = !!query && !tabs.length && !groups.length;
  return (
    <aside className="flex w-[236px] shrink-0 flex-col border-r border-line bg-inset">
      <div className="shrink-0 border-b border-line px-3 pt-3 pb-2.5">
        <div className="text-[14px] font-semibold text-ink">{t("settings.title")}</div>
        <label className="mt-2 flex h-8 items-center gap-2 rounded-control bg-surface px-2.5 shadow-btn focus-within:shadow-[inset_0_0_0_1px_var(--accent)]">
          <Icon name="search" size={13} stroke={2} className="shrink-0 text-ink-3" />
          <input value={query} onChange={(e) => onQuery(e.target.value)} placeholder={t("settings.search_placeholder_settings")} aria-label={t("settings.search_settings")} className="min-w-0 flex-1 bg-transparent text-[13px] text-ink outline-none placeholder:text-ink-3" />
          {query && <IconButton icon="x" label={t("settings.clear")} size={22} iconSize={12} onClick={() => onQuery("")} />}
        </label>
      </div>
      <nav ref={nav} aria-label={t("settings.categories")} className="min-h-0 flex-1 overflow-y-auto overscroll-contain p-2">
        {query ? (
          <>
            {tabs.map((k) => <NavRow key={k} k={k} selected={tab === k} contentId={contentId} onPick={onPick} />)}
            {groups.length > 0 && <div className="px-3 pt-3 pb-1 text-[11px] font-medium text-ink-3">{t("settings.search_in_page")}</div>}
            {groups.map((g) => <GroupResult key={`${g.tab}/${g.anchor}`} g={g} onGroup={onGroup} />)}
            {none && <p className="px-3 py-3 text-[12.5px] leading-relaxed text-ink-3">{t("settings.search_no_results")}</p>}
          </>
        ) : (
          NAV_GROUPS.map((g) => {
            const its = g.tabs.filter(shown);
            if (!its.length) return null;
            return (
              <div key={g.title}>
                <div className="px-3 pt-3 pb-1 text-[11px] font-medium text-ink-3">{g.title}</div>
                {its.map((k) => <NavRow key={k} k={k} selected={tab === k} contentId={contentId} onPick={onPick} />)}
              </div>
            );
          })
        )}
      </nav>
    </aside>
  );
}

function NavRow({ k, selected, contentId, onPick }: { k: Tab; selected: boolean; contentId: string; onPick: (k: Tab) => void }) {
  const info = TAB_INFO[k];
  return (
    <button
      type="button"
      onClick={() => onPick(k)}
      aria-current={selected ? "page" : undefined}
      aria-controls={contentId}
      className={cx(
        "relative flex h-8 w-full items-center gap-2 rounded-[7px] px-2.5 text-left text-[13px] transition-colors",
        selected ? "bg-accent-tint font-medium text-accent-ink" : "text-ink-2 hover:bg-hover hover:text-ink",
      )}
    >
      {selected && <span aria-hidden className="absolute left-0 top-1/2 h-4 w-[2px] -translate-y-1/2 rounded-full bg-accent" />}
      <Icon name={info.icon} size={15} className="shrink-0" />
      <span className="truncate" title={info.label}>{info.label}</span>
    </button>
  );
}

function GroupResult({ g, onGroup }: { g: SearchGroup; onGroup: (g: SearchGroup) => void }) {
  return (
    <button type="button" onClick={() => onGroup(g)} className="flex w-full items-center gap-1.5 rounded-[7px] px-3 py-1.5 text-left text-[12.5px] text-ink-2 transition-colors hover:bg-hover hover:text-ink">
      <span className="min-w-0 truncate">
        <span className="font-medium text-ink">{TAB_INFO[g.tab].label}</span>
        <span className="mx-1 text-ink-3">›</span>
        <span>{g.group}</span>
      </span>
    </button>
  );
}

/** The phone's first page of Settings: the categories in grouped cards, each opening its page (as on iOS). */
function SettingsList({ shown, onPick }: { shown: (k: Tab) => boolean; onPick: (k: Tab) => void }) {
  return (
    <nav aria-label={t("settings.categories")}>
      {NAV_GROUPS.map((g) => {
        const tabs = g.tabs.filter(shown);
        if (!tabs.length) return null;
        return (
          <Group key={g.title} title={g.title}>
            {tabs.map((k) => (
              <Row key={k} icon={TAB_INFO[k].icon} label={TAB_INFO[k].label} hint={TAB_INFO[k].hint} onClick={() => onPick(k)} />
            ))}
          </Group>
        );
      })}
    </nav>
  );
}

const host = hostOf;

// ── General ─────────────────────────────────────────────

function General({ onModel }: { onModel: () => void }) {
  const theme = useTheme();
  const replyMotion = useReplyMotionSetting();

  const themes: { k: Theme; label: string; icon: IconName }[] = [
    { k: "system", label: t("language.system"), icon: "monitor" },
    { k: "light", label: t("settings.theme_light"), icon: "sun" },
    { k: "dark", label: t("settings.theme_dark"), icon: "moon" },
  ];

  return (
    <>
      <Group title={t("settings.appearance")} anchor="appearance">
        <div className="flex gap-2 p-3">
          {themes.map((th) => (
            <button
              key={th.k}
              type="button"
              onClick={() => setTheme(th.k)}
              aria-pressed={theme === th.k}
              className={cx(
                "flex flex-1 items-center justify-center gap-2 whitespace-nowrap rounded-control px-3 py-2 text-[12.5px] transition-[background-color,box-shadow,color] duration-150",
                theme === th.k ? "bg-accent-tint font-medium text-accent-ink shadow-[inset_0_0_0_1px_var(--accent)]" : "text-ink-2 shadow-hairline hover:bg-hover",
              )}
            >
              <Icon name={th.icon} size={14} />
              {th.label}
            </button>
          ))}
        </div>
        <ThemeColorPicker />
        <ExtensionThemePicker />
        <TextSizeRow />
        <LanguageRow />
        <Row label={t("settings.reply_motion")} hint={t("settings.reply_motion_hint")}>
          <Switch on={replyMotion} onChange={setReplyMotionEnabled} label={t("settings.reply_motion")} />
        </Row>
      </Group>

      <SidebarPagesGroup />

      <section id="settings-translation" className="scroll-mt-4">
        <TranslationSettings onConfigure={onModel} />
      </section>
    </>
  );
}

function PersonaSettings({ onTriageModel, onExtensions }: { onTriageModel: () => void; onExtensions: () => void }) {
  const sidebarTerminals = useSidebarTerminals();
  const agentDetails = useAgentDetails();
  const contextAsUsed = useContextAsUsed();
  return (
    <>
      <Group title={t("settings.avatar")} footer={t("settings.avatar_footer")} anchor="avatar">
        <PersonaPicker onExtensions={onExtensions} />
      </Group>
      <AssistantSettings bare />

      <TriageSettings onModel={onTriageModel} />

      <AgentGroup />

      <Group title={t("settings.agent_preferences")} anchor="agent-preferences">
        <TerminalModeRow />
        <Row label={t("settings.sidebar_terminals")} hint={t("settings.sidebar_terminals_hint")}>
          <Switch on={sidebarTerminals} onChange={setSidebarTerminals} label={t("settings.sidebar_terminals")} />
        </Row>
        <Row label={t("context.details")} hint={t("context.details_hint")}>
          <Switch on={agentDetails} onChange={setAgentDetails} label={t("context.details")} />
        </Row>
        <Row label={t("context.as_used")} hint={t("context.as_used_hint")}>
          <Switch on={contextAsUsed} onChange={setContextAsUsed} label={t("context.as_used")} />
        </Row>
      </Group>

      <HubList />
      <About />
    </>
  );
}

/** Settings › General's line under "Runs on": the computer by name; none yet ("" from the hub) says so; nothing before it is read. */
export function assistantFooter(machine: string | undefined, loaded: boolean): string | undefined {
  if (machine) return t("settings.assistant_footer", { machine });
  return loaded ? t("settings.assistant_footer_none") : undefined;
}

function About() {
  const machines = useStore((s) => s.machines);
  const hubName = useStore((s) => s.hubName);
  // interconnected hubs (docs/archive/operations/plan-hub-mesh.md 2.6): this page talks to one hub and sees its whole group
  const mesh = useStore((s) => s.mesh);
  const others = mesh.filter((h) => !h.self);
  const disconnect = useDisconnect();
  return (
    <Group title={t("settings.this_browser")} footer={t("settings.this_browser_footer")} anchor="this-browser">
      <Row label={t("settings.hub_address")} mono>
        {location.host}
      </Row>
      {hubName && <Row label={t("settings.hub")}>{hubName}</Row>}
      {others.length > 0 && (
        <Row label={t("settings.linked_hubs")} hint={t("settings.linked_hubs_hint")}>
          {others.map((h) => (
            <span key={h.hub_id} className="flex items-center gap-1.5">
              <span className={cx("size-1.5 rounded-full", h.online === false ? "bg-ink-3" : "bg-green")} />
              {h.name || h.hub_id}
            </span>
          ))}
        </Row>
      )}
      <Row label={t("settings.computers")}>{t("settings.computers_online", { online: machines.filter((m) => m.online).length, total: machines.length })}</Row>
      <InstallRow />
      <div className="flex justify-end px-4 py-2.5">
        <Button tone="quiet" icon="logout" onClick={disconnect.start} busy={disconnect.busy} className="text-red enabled:hover:text-red">
          {t("settings.disconnect_browser")}
        </Button>
      </div>
      {disconnect.dialog}
    </Group>
  );
}

// Installing the web client as an app (lib/pwa.ts): the browser's prompt where it has one, else where to find it.
const INSTALL_HINT: Record<InstallState, string> = {
  installed: t("settings.install_installed"),
  prompt: t("settings.install_prompt"),
  ios: t("settings.install_ios"),
  safari: t("settings.install_safari"),
  insecure: t("settings.install_insecure"),
  menu: t("settings.install_menu"),
};

function InstallRow() {
  const state = useInstall();
  return (
    <Row label={t("settings.install_app")} hint={INSTALL_HINT[state]}>
      {state === "installed" && <Pill tone="green">{t("settings.installed")}</Pill>}
      {state === "prompt" && (
        <Button icon="download" onClick={() => void install()}>
          {t("settings.install")}
        </Button>
      )}
    </Row>
  );
}

// ── Models ──────────────────────────────────────────────

/** Forgets `add` / `launcher` once what they opened is closed, so going back or reloading does not open it again. */
function dropLinkParams() {
  const q = new URLSearchParams(location.search);
  if (!q.has("add") && !q.has("launcher")) return;
  q.delete("add");
  q.delete("launcher");
  history.replaceState(history.state, "", `${location.pathname}?${q.toString()}`);
}

function Models() {
  const machines = useStore((s) => s.machines);
  const [list, setList] = useState<LauncherView[] | null>(null);
  const [sync, setSync] = useState<LauncherSync[]>([]);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [query, setQuery] = useState("");
  // `?add=1` opens the wizard, `?launcher=<name>` one provider: where "Connect a model" / "Change the key" lead (lib/readiness.ts)
  const [open, setOpen] = useState<string | null>(() => new URLSearchParams(location.search).get("launcher"));
  const [editing, setEditing] = useState<LauncherView | "new" | null>(() => (new URLSearchParams(location.search).get("add") === "1" ? "new" : null));
  const [importing, setImporting] = useState(false);

  const load = () => {
    setLoading(true);
    setError("");
    return loadLaunchers<LauncherView>()
      .then((r) => (setList(r.launchers), setSync(r.sync ?? [])))
      .catch((e) => setError(errorText(e)))
      .finally(() => setLoading(false));
  };
  useEffect(() => void load(), []);

  const name = (id: string) => sync.find((s) => s.machine === id)?.name || machines.find((m) => m.id === id)?.name || id;
  const shown = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!list || !q) return list ?? [];
    return list.filter((l) => [l.name, l.model, l.small_model, l.base_url, ...(l.machines ?? []).map(name)].some((v) => v?.toLowerCase().includes(q)));
  }, [list, query, machines, sync]);
  const managed = shown.filter((l) => managedProvider(l.source));
  const builtin = builtinRows(shown);
  const rc = rcGroups(shown);
  // a wrapper script on PATH or a runtime's launcher (agy, omp): a computer's, but not from an rc file
  const onPath = shown.filter((l) => !managedProvider(l.source) && l.source !== "builtin" && l.source !== "zshrc" && !l.hidden);
  const hidden = shown.filter((l) => !managedProvider(l.source) && l.source !== "builtin" && l.hidden);
  // the machines that did not answer this time, named on the entries they gave before
  const staleMark = (ids?: string[]) =>
    ids?.length ? <Pill tone="orange">{t("settings.stale", { machines: formatList(ids.map(name), { type: "conjunction", style: "narrow" }) })}</Pill> : null;

  const row = (l: LauncherView) => (
    <button key={l.name} type="button" onClick={() => setOpen(l.name)} className="flex w-full items-center gap-3 px-4 py-2.5 text-left transition-colors hover:bg-hover">
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          {l.display_name ? <span className="truncate text-[13px] font-medium text-ink">{l.display_name}</span> : <span className="truncate font-mono text-[13px] font-medium text-ink">{l.name}</span>}
          {managedProvider(l.source) && <Pill tone="accent">Persona</Pill>}
          {l.source === "agy" && <Pill>Antigravity Runtime</Pill>}
          {l.kind === "api" && <Pill>{t("settings.direct_api")}</Pill>}
          {staleMark(l.stale)}
        </div>
        <div className="mt-0.5 flex min-w-0 items-center gap-2 text-[11.5px] text-ink-3">
          {l.display_name && <span className="shrink-0 font-mono">{l.name}</span>}
          {l.model && <span className="truncate font-mono text-ink-2">{l.model}</span>}
          {host(l.base_url) && <span className="truncate">{host(l.base_url)}</span>}
        </div>
      </div>
      {l.machines && l.machines.length > 0 && <span className="hidden max-w-[200px] shrink-0 truncate text-[11.5px] text-ink-3 sm:block">{formatList(l.machines.map(name), { type: "conjunction", style: "narrow" })}</span>}
      <Icon name="chevronRight" size={12} stroke={2} className="shrink-0 text-ink-3" />
    </button>
  );

  return (
    <>
      <div className="mb-8">
        <h2 className="mb-1 px-1 text-[15px] font-semibold text-ink">{t("settings-nav.tab_providers")}</h2>
        <div className="mb-4 flex items-center gap-2">
        <label className="flex h-9 min-w-0 flex-1 items-center gap-2 rounded-control bg-surface px-3 shadow-btn focus-within:shadow-[inset_0_0_0_1px_var(--accent)]">
          <Icon name="search" size={13} stroke={2} className="text-ink-3" />
          <input value={query} onChange={(e) => setQuery(e.target.value)} placeholder={t("settings.search_placeholder")} className="min-w-0 flex-1 bg-transparent text-[13px] text-ink outline-none placeholder:text-ink-3" aria-label={t("settings.search_providers")} />
          {query && <IconButton icon="x" label={t("settings.clear")} size={22} iconSize={12} onClick={() => setQuery("")} />}
        </label>
        <Button tone="accent" icon="plus" onClick={() => setEditing("new")}>
          <span className="max-sm:hidden">{t("settings.add_provider")}</span>
          <span className="sm:hidden">{t("settings.add_provider_short")}</span>
        </Button>
      </div>
      {!list && !error && <Loading />}
      {error && <div className="mb-4 flex items-center gap-3">
        <p role="alert" className="min-w-0 flex-1 text-[13px] break-words text-red">{t("settings.launchers_failed", { error })}</p>
        <Button icon="retry" onClick={() => void load()} busy={loading}>{t("settings.retry")}</Button>
      </div>}
      {list && query && shown.length === 0 && <p className="py-10 text-center text-[13px] text-ink-3">{t("settings.no_matching_providers", { query })}</p>}
      {list && (!query || managed.length > 0) && (
        <Group title={t("settings.managed_title", { count: managed.length })} footer={managed.length ? t("settings.managed_footer") : undefined}>
          {managed.length ? managed.map(row) : <p className="px-4 py-3 text-[12.5px] text-ink-3">{t("settings.managed_empty")}</p>}
        </Group>
      )}
      {builtin.length > 0 && (
        <Group title={t("settings.builtin_title", { count: builtin.length })} footer={t("settings.builtin_footer")}>
          {builtin.map((b) => (
            <div key={`${b.name}/${b.machine}`} className="flex items-center gap-3 px-4 py-2.5">
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2">
                  <span className="truncate text-[13px] font-medium text-ink">{t("settings.builtin_row", { name: b.name, machine: name(b.machine) })}</span>
                  {b.stale && <Pill tone="orange">{t("settings.stale_mark")}</Pill>}
                </div>
                <div className="mt-0.5 truncate font-mono text-[11.5px] text-ink-3">{b.version ? `Claude Code ${b.version}` : "Claude Code"}</div>
              </div>
            </div>
          ))}
        </Group>
      )}
      {rc.map((g, i) => (
        <Group key={g.files} title={t("settings.zshrc_title", { file: g.files, count: g.launchers.length })} footer={i === rc.length - 1 ? t("settings.zshrc_footer") : undefined}>
          {g.launchers.map(row)}
        </Group>
      ))}
      {onPath.length > 0 && (
        <Group title={t("settings.path_title", { count: onPath.length })} footer={t("settings.path_footer")}>
          {onPath.map(row)}
        </Group>
      )}
      {hidden.length > 0 && (
        <Group title={t("settings.hidden_title", { count: hidden.length })} footer={t("settings.hidden_footer")}>
          {hidden.map(row)}
        </Group>
      )}
      {list && !query && (
        // "Import from ~/.zshrc" reads a computer's ~/.zshrc, so it is for people who already run claude there, and needs a computer
        <Group title={t("settings.providers_advanced")} footer={machines.length ? t("settings.zshrc_import_hint") : t("settings.zshrc_import_no_machines")}>
          <Row label={t("settings-providerimport.title")} icon="terminal">
            <Button tone="secondary" icon="download" disabled={machines.length === 0} onClick={() => setImporting(true)}>
              {t("settings-providerimport.import")}
            </Button>
          </Row>
        </Group>
      )}
      {list && !query && sync.length > 0 && (
        <Group title={t("settings.sync_title", { count: sync.length })} footer={t("settings.sync_footer")}>
          {sync.map((s) => {
            const st = syncStateText(s);
            return (
              <div key={s.machine} className="flex items-center gap-3 px-4 py-2.5">
                <div className="min-w-0 flex-1">
                  <div className="truncate text-[13px] text-ink">{name(s.machine)}</div>
                  {st.detail && <div className={cx("mt-0.5 truncate text-[11.5px]", s.state === "error" ? "text-red" : "text-ink-3", s.state === "ok" && "font-mono")} title={st.detail}>{st.detail}</div>}
                </div>
                <Pill tone={st.tone}>{st.label}</Pill>
              </div>
            );
          })}
        </Group>
      )}
      <LauncherDetailDialog
        name={open}
        onClose={() => (setOpen(null), dropLinkParams())}
        onChanged={() => void load()}
        onEdit={(l) => {
          setOpen(null);
          setEditing(l);
        }}
      />
      <ProviderWizard existing={editing} onClose={() => (setEditing(null), dropLinkParams())} onSaved={() => void load()} />
      <ProviderImport open={importing} onClose={() => setImporting(false)} onImported={() => void load()} />
      </div>
    </>
  );
}

export function LauncherDetailDialog({ name, onClose, onChanged, onEdit }: { name: string | null; onClose: () => void; onChanged: () => void; onEdit: (l: LauncherView) => void }) {
  const machines = useStore((s) => s.machines);
  const [l, setL] = useState<LauncherView | null>(null);
  const [error, setError] = useState("");
  const [checks, setChecks] = useState<Record<string, LauncherCheck | null>>({});
  const [confirm, setConfirm] = useState(false);
  const [showDef, setShowDef] = useState(false);
  const mname = (id: string) => machines.find((m) => m.id === id)?.name ?? id;
  // extensions draw a section here (docs/extensions/extensions.md "Declarative UI" launcher_section)
  const hasExtensions = useStore((s) => hubHasExt(s.features, "manager"));

  // The dialog stays mounted from one launcher to the next: an answer or a test result for a name it no longer shows
  // is dropped, or the body (and Delete) would be another launcher's under this one's title (#5374).
  const shown = useRef(name);
  shown.current = name;
  useEffect(() => {
    setL(null);
    setError("");
    setChecks({});
    setConfirm(false);
    setShowDef(false);
    if (!name) return;
    let live = true;
    api<{ launcher: LauncherView }>(enLauncher(name))
      .then((r) => live && setL(r.launcher))
      .catch((e) => live && setError(errorText(e)));
    return () => {
      live = false;
    };
  }, [name]);

  // machines whose launcher sets the same, once
  const groups = useMemo(() => {
    const out: { ids: string[]; d: LauncherDetail }[] = [];
    for (const [id, d] of Object.entries(l?.details ?? {}).sort()) {
      const g = out.find((x) => JSON.stringify(x.d) === JSON.stringify(d));
      if (g) g.ids.push(id);
      else out.push({ ids: [id], d });
    }
    return out;
  }, [l]);

  const testOn = managedProvider(l?.source) ? [""] : (l?.machines ?? []).filter((id) => machines.find((m) => m.id === id)?.online !== false);
  const running = Object.values(checks).some((c) => c === null);
  const check = async () => {
    if (!l) return;
    const asked = name;
    setChecks(Object.fromEntries(testOn.map((id) => [id, null])));
    await Promise.all(
      testOn.map(async (id) => {
        let c: LauncherCheck;
        try {
          c = (await api<{ check: LauncherCheck }>(`${enLauncher(l.name)}/check${id ? `?machine=${enc(id)}` : ""}`, { method: "POST" })).check;
        } catch (e) {
          c = { ok: false, millis: 0, error: errorText(e) };
        }
        if (shown.current === asked) setChecks((x) => ({ ...x, [id]: c }));
      }),
    );
  };

  const remove = async () => {
    if (!l) return;
    try {
      await api(enLauncher(l.name), { method: "DELETE" });
      toast(managedProvider(l.source) ? t("settings.deleted", { name: l.name }) : t("settings.hid", { name: l.name }), "ok");
      onChanged();
      onClose();
    } catch (e) {
      toast(errorText(e), "error");
    }
  };
  const restore = async () => {
    if (!l) return;
    try {
      await api(`${enLauncher(l.name)}/restore`, { method: "POST" });
      toast(t("settings.restored", { name: l.name }), "ok");
      onChanged();
      onClose();
    } catch (e) {
      toast(errorText(e), "error");
    }
  };

  return (
    <Dialog
      open={name !== null}
      onClose={onClose}
      width={620}
      title={<span className="font-mono">{name}</span>}
      footer={
        l && (
          <>
            {l.hidden ? (
              <Button tone="secondary" onClick={() => void restore()}>
                {t("settings.restore")}
              </Button>
            ) : (
              <span className="relative">
                <Button tone="quiet" icon={managedProvider(l.source) ? "x" : undefined} onClick={() => setConfirm((c) => !c)} className="text-red enabled:hover:text-red">
                  {managedProvider(l.source) ? t("settings.delete_provider") : t("settings.hide_in_persona")}
                </Button>
                {/* the confirmation comes up right at the button (like the app), not as another dialog */}
                {confirm && (
                  <span className="absolute bottom-full left-0 mb-1.5 rounded-control bg-surface p-1 shadow-overlay" style={{ animation: `pop-in 160ms ${EASE} both` }}>
                    <Button tone="danger" onClick={() => void remove()}>
                      {managedProvider(l.source) ? t("settings.confirm_delete") : t("settings.confirm_hide")}
                    </Button>
                  </span>
                )}
              </span>
            )}
            <span className="flex-1" />
            {managedProvider(l.source) && (
              <Button tone="secondary" onClick={() => onEdit(l)}>
                {t("settings.edit")}
              </Button>
            )}
            <Button tone="primary" icon={running ? undefined : "run"} busy={running} disabled={testOn.length === 0} onClick={() => void check()}>
              {Object.keys(checks).length ? t("settings.test_again") : t("settings.test")}
            </Button>
          </>
        )
      }
    >
      {!l && !error && <Loading />}
      {error && <p className="text-[13px] text-red">{error}</p>}
      {l && (
        <div className="flex max-h-[62vh] flex-col gap-4 overflow-y-auto pr-1">
          <div className="flex flex-wrap items-center gap-1.5">
            <Pill tone={managedProvider(l.source) ? "accent" : undefined}>{managedProvider(l.source) ? t("settings.managed") : l.source === "agy" ? "Antigravity Runtime (agy acp)" : l.source === "path" ? t("settings.path_script") : t("settings.zshrc_function", { file: rcFilesOf(l).join(" / ") })}</Pill>
            {(l.machines ?? []).map((id) => (
              <Pill key={id} tone={l.stale?.includes(id) ? "orange" : undefined}>{l.stale?.includes(id) ? `${mname(id)} · ${t("settings.stale_mark")}` : mname(id)}</Pill>
            ))}
            {l.hidden && <Pill>{t("settings.hidden")}</Pill>}
          </div>

          {/* how much is left comes before trying it, as in the app */}
          {l.kind !== "api" && (managedProvider(l.source) || l.source === "zshrc" || l.source === "path") && <LauncherQuota name={l.name} />}

          {Object.keys(checks).length > 0 && (
            <KV title={t("settings.availability")}>
              {Object.entries(checks).map(([id, c]) => (
                <div key={id} className="flex items-start gap-2.5 py-1.5">
                  <span className="mt-0.5 flex size-4 shrink-0 items-center justify-center">{c === null ? <Spinner /> : <Icon name={c.ok ? "check" : "x"} size={13} stroke={2.6} className={c.ok ? "text-green" : "text-red"} />}</span>
                  <div className="min-w-0 flex-1">
                    <div className="flex items-baseline gap-2 text-[12.5px]">
                      <span className="font-medium text-ink">{id ? mname(id) : t("settings.hub_machine_title")}</span>
                      {c && <span className="text-ink-3 tabular-nums">{t("settings.elapsed_seconds", { seconds: (c.millis / 1000).toFixed(c.millis >= 10_000 ? 0 : 1) })}</span>}
                    </div>
                    {c === null ? (
                      <div className="text-[11.5px] text-ink-3">{t("settings.requesting")}</div>
                    ) : c.ok ? (
                      <div className="truncate text-[11.5px] text-ink-3">{[c.model, c.answer && t("settings.answered", { answer: c.answer })].filter(Boolean).join(" · ")}</div>
                    ) : (
                      <pre className="mt-0.5 whitespace-pre-wrap break-all font-mono text-[11px] text-red">{c.error}</pre>
                    )}
                  </div>
                </div>
              ))}
            </KV>
          )}

          {managedProvider(l.source) && (
            <KV title={t("settings.config")}>
              <KVRow k={t("settings.access")} v={l.kind === "api" ? t("settings.direct_api") : l.agent === "codex" ? "Codex" : l.agent === "agy" ? "Antigravity (agy)" : t("settings.launcher")} />
              {l.kind === "api" && <KVRow k={t("settings.protocol")} v={l.api === "chat" ? t("settings-providerwizard.protocol_chat") : l.api === "responses" ? "Responses" : "Messages"} />}
              {l.base_url && <KVRow k="Base URL" v={l.base_url} mono />}
              <KVRow k={t("settings.model")} v={l.model} mono />
              {l.kind !== "api" && (!l.agent || l.agent === "claude") && l.small_model && <KVRow k={t("settings.small_model")} v={l.small_model} mono />}
              {l.kind !== "api" && (!l.agent || l.agent === "claude") && l.subagent_model && <KVRow k={t("settings.subagent_model")} v={l.subagent_model} mono />}
              {l.kind !== "api" && (!l.agent || l.agent === "claude") && l.custom_model && <KVRow k={t("settings.custom_model")} v={l.custom_model} mono />}
              {l.kind !== "api" && (!l.agent || l.agent === "claude") && l.custom_model_name && <KVRow k={t("settings.custom_model_name")} v={l.custom_model_name} />}
              <KVRow k="API Key" v={l.has_key ? t("settings.key_set_hidden") : t("settings.not_set")} />
              {l.api !== "responses" && l.api !== "chat" && (!l.agent || l.agent === "claude") && <KVRow k={t("settings.auth")} v={l.auth === "bearer" ? "Bearer" : "x-api-key"} />}
              {l.kind !== "api" && l.args?.length ? <KVRow k={t("settings.extra_args")} v={l.args.join(" ")} mono /> : null}
              {l.models?.length ? <KVRow k={t("settings.model_list")} v={formatList(l.models.map((m) => m.id), { type: "conjunction", style: "narrow" })} mono sub={t("settings.model_count", { count: l.models.length })} /> : null}
              {([["opus_model", "Opus"], ["sonnet_model", "Sonnet"], ["haiku_model", "Haiku"], ["fable_model", "Fable"]] as const).map(([k, tier]) => l[k] && <KVRow key={k} k={t("settings.tier_model", { tier })} v={l[k]} mono />)}
              {l.subagent_force && <KVRow k={t("settings.subagent")} v={t("settings.subagent_force")} />}
              {l.has_headers && <KVRow k={t("settings.headers")} v={t("settings.key_set_hidden")} />}
              {l.has_env && <KVRow k={t("settings.env")} v={t("settings.key_set_hidden")} />}
              {l.proxy && <KVRow k={t("settings.proxy")} v={l.proxy} mono />}
              {l.no_proxy && <KVRow k="NO_PROXY" v={l.no_proxy} mono />}
              {l.timeout_ms ? <KVRow k={t("settings.timeout")} v={`${l.timeout_ms} ms`} mono /> : null}
              {l.compact_window ? <KVRow k={t("settings.compact_window")} v={tokens(l.compact_window)} /> : null}
              {l.max_context ? <KVRow k={t("settings.max_context")} v={tokens(l.max_context)} /> : null}
              {l.effort && <KVRow k={t("settings.default_effort")} v={l.effort} />}
              {l.origin?.startsWith("zshrc:") && <KVRow k={t("settings.source")} v={l.origin.slice("zshrc:".length)} mono sub={t("settings.from_zshrc")} />}
            </KV>
          )}

          {hasExtensions && <LauncherExtensionSections launcher={l.name} machine={managedProvider(l.source) ? undefined : testOn[0] || undefined} />}

          {groups.map((g) => (
            <DetailBlock key={g.ids.join()} d={g.d} place={groups.length > 1 ? formatList(g.ids.map(mname), { type: "conjunction", style: "narrow" }) : undefined} />
          ))}

          {l.definitions && Object.keys(l.definitions).length > 0 && (
            <div>
              <button type="button" onClick={() => setShowDef((x) => !x)} className="flex items-center gap-1.5 text-[12px] font-medium text-ink-2 hover:text-ink">
                <Icon name="chevron" size={10} stroke={2.4} style={{ transform: showDef ? "none" : "rotate(-90deg)", transition: "transform 150ms" }} />
                {l.source === "path" ? t("settings.script_source") : t("settings.function_source")}
                <span className="font-normal text-ink-3">{t("settings.function_source_note")}</span>
              </button>
              {showDef &&
                Object.entries(l.definitions).map(([id, def]) => (
                  <div key={id} className="mt-2">
                    {Object.keys(l.definitions!).length > 1 && <div className="mb-1 text-[11px] text-ink-3">{mname(id)}</div>}
                    <pre className="overflow-x-auto rounded-control bg-inset p-3 font-mono text-[11px] leading-relaxed text-ink-2 shadow-hairline">{def}</pre>
                  </div>
                ))}
            </div>
          )}
        </div>
      )}
    </Dialog>
  );
}

function KV({ title, children }: { title: React.ReactNode; children: React.ReactNode }) {
  return (
    <div>
      <div className="mb-1 text-[11.5px] font-medium text-ink-3">{title}</div>
      <div className="divide-y divide-line rounded-control bg-inset px-3 shadow-hairline">{children}</div>
    </div>
  );
}

function KVRow({ k, v, mono, sub }: { k: React.ReactNode; v?: React.ReactNode; mono?: boolean; sub?: string }) {
  return (
    <div className="flex items-baseline gap-3 py-1.5">
      <div className={cx("shrink-0", sub ? "w-[220px]" : "w-[104px]")}>
        <div className="text-[12px] text-ink-2">{k}</div>
        {sub && <div className="font-mono text-[10px] break-all text-ink-3">{sub}</div>}
      </div>
      <div className={cx("min-w-0 flex-1 break-all text-[12.5px] text-ink", mono && "font-mono")}>{v || "—"}</div>
    </div>
  );
}

function DetailBlock({ d, place }: { d: LauncherDetail; place?: string }) {
  const titled = (s: string) => (place ? `${s} · ${place}` : s);
  if (d.error) return <KV title={titled(t("settings.run"))}><p className="py-2 text-[12px] text-ink-3">{d.error}</p></KV>;
  const agent = d.agent === "codex" ? "Codex" : d.agent === "traex" ? "TRAEX" : d.agent === "agy" ? "Antigravity" : "Claude Code";
  return (
    <>
      <KV title={titled(t("settings.run"))}>
        <KVRow k="Agent" v={agent} />
        {d.base_url && <KVRow k={t("settings.gateway")} v={d.base_url} mono />}
        {d.auth && <KVRow k="Key" v={t("settings.key_set", { auth: d.auth })} mono />}
      </KV>
      {d.models?.length ? (
        <KV title={titled(t("settings.model"))}>
          {d.models.map((m) => (
            <KVRow key={m.role + m.env} k={m.role} sub={m.env} v={m.model} mono />
          ))}
        </KV>
      ) : null}
      {(d.context_window || d.compact_window || d.effort || d.timeout_ms) && (
        <KV title={titled(t("settings.context"))}>
          {d.context_window ? <KVRow k={t("settings.context_window")} v={`${tokens(d.context_window)} tokens`} /> : null}
          {d.compact_window ? <KVRow k={t("settings.compact_window")} v={`${tokens(d.compact_window)} tokens`} /> : null}
          {d.effort ? <KVRow k={t("settings.effort")} v={d.effort} /> : null}
          {d.timeout_ms ? <KVRow k={t("settings.request_timeout")} v={d.timeout_ms >= 60_000 ? t("settings.minutes", { count: Math.round(d.timeout_ms / 60_000) }) : t("settings.seconds", { count: Math.round(d.timeout_ms / 1000) })} /> : null}
        </KV>
      )}
      {d.args?.length ? (
        <KV title={titled(t("settings.launch_args"))}>
          <p className="py-2 font-mono text-[11.5px] break-all text-ink">{d.args.join(" ")}</p>
        </KV>
      ) : null}
      {d.env?.length ? (
        <KV title={titled(t("settings.other_settings", { count: d.env.length }))}>
          {d.env.map((e) => (
            <KVRow key={e.key} k={<span className="font-mono text-[11px]">{e.key}</span>} v={e.value} mono />
          ))}
        </KV>
      ) : null}
    </>
  );
}

// ── Devices ─────────────────────────────────────────────

function Devices() {
  const [data, setData] = useState<{ tokens: DeviceToken[]; current?: string } | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [confirm, setConfirm] = useState<string | null>(null);
  const [connecting, setConnecting] = useState(false);
  const load = () => {
    setLoading(true);
    return api<{ tokens: DeviceToken[]; current?: string }>("/auth/tokens")
      .then((r) => (setData({ tokens: r.tokens ?? [], current: r.current }), setError("")))
      .catch((e) => setError(errorText(e)))
      .finally(() => setLoading(false));
  };
  useEffect(() => void load(), []);

  const revoke = async (id: string) => {
    try {
      await api(`/auth/tokens/${enc(id)}`, { method: "DELETE" });
      toast(t("settings.revoked_toast"), "ok");
      setConfirm(null);
      void load();
    } catch (e) {
      toast(errorText(e), "error");
    }
  };
  const kind: Record<string, string> = { phone: t("settings.kind_phone"), watch: t("settings.kind_watch"), other: t("settings.kind_cli"), web: t("settings.kind_web") };
  const { active, revoked } = splitDevices(data?.tokens ?? [], data?.current);

  // A revoked credential is shown greyed, with when it was revoked and no "Revoke" (iOS DeviceCredentials.swift).
  const row = (tok: DeviceToken, gone: boolean) => (
    <div key={tok.id} className="flex items-center gap-3 px-4 py-2.5">
      <Icon name={tok.kind === "phone" ? "phone" : tok.kind === "watch" ? "watch" : "web"} size={15} className="shrink-0 text-ink-3" />
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className={cx("truncate text-[13px]", gone ? "text-ink-3" : "text-ink")}>{tok.name || tok.id}</span>
          {tok.id === data?.current && <Pill tone="green">{t("settings.this_browser")}</Pill>}
          {tok.role !== "full" && <Pill>{tok.role === "answer" ? t("settings.role_answer") : tok.role}</Pill>}
        </div>
        <div className="mt-0.5 truncate text-[11.5px] text-ink-3">
          {kind[tok.kind] ?? tok.kind}
          {gone && tok.revoked_at ? ` · ${t("settings.revoked_ago", { time: ago(tok.revoked_at) })}` : ""}
          {!gone && tok.last_seen ? ` · ${t("settings.used_ago", { time: ago(tok.last_seen) })}` : ""}
          {tok.created_at ? ` · ${t("settings.added_ago", { time: ago(tok.created_at) })}` : ""}
        </div>
      </div>
      {!gone && tok.id !== data?.current && (
        <RowConfirm
          open={confirm === tok.id}
          confirm={t("settings.confirm_revoke")}
          onCancel={() => setConfirm(null)}
          onConfirm={() => void revoke(tok.id)}
          trigger={
            <Button tone="quiet" onClick={() => setConfirm(tok.id)} className="text-red enabled:hover:text-red">
              {t("settings.revoke")}
            </Button>
          }
        />
      )}
    </div>
  );

  return (
    <>
      {!data && !error && <Loading />}
      {error && <div className="mb-4 flex items-center gap-3">
        <p role="alert" className="min-w-0 flex-1 text-[13px] break-words text-red">{error}</p>
        <Button icon="retry" onClick={() => void load()} busy={loading}>{t("settings.retry")}</Button>
      </div>}
      {data && (
        <Group
          title={t("settings.devices_title", { count: active.length })}
          footer={t("settings.devices_footer")}
          right={
            <Button tone="quiet" icon="plus" onClick={() => setConnecting(true)} className="whitespace-nowrap">
              {t("settings-connectdevice.add")}
            </Button>
          }
        >
          {active.length === 0 && <p className="px-4 py-3 text-[12.5px] text-ink-3">{t("settings.no_devices")}</p>}
          {active.map((tok) => row(tok, false))}
        </Group>
      )}
      {data && revoked.length > 0 && <Group title={t("settings.revoked_title", { count: revoked.length })}>{revoked.map((tok) => row(tok, true))}</Group>}
      <ConnectDevice open={connecting} onClose={() => setConnecting(false)} onPaired={() => void load()} />
    </>
  );
}
