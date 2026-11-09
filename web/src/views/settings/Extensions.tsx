// Settings › Extensions: all sources use the host-owned layout in ExtensionSettingsPage.
// This controller owns manager routing, configuration drafts, authentication and mutation results;
// extensions supply only their configuration fields and custom content (docs/extensions/extension-settings.md).
import { useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from "react";
import { enc, errorText } from "../../lib/api";
import { defaultManager, pinManagerAt, useKnownDefault } from "../../lib/manager-choice";
import { groupByManager, multiManager, useRoster } from "../../lib/managers";
import { isResumed } from "../../lib/reconnect";
import { onHubEvent, useStore } from "../../lib/store";
import { EXTENSIONS_CHANGED } from "../../lib/extension-prompts";
import { operations, operationsListed, opOf, watchOperations } from "../../lib/extension-operations";
import { extensionsRoute, routeSub } from "../../lib/extension-market";
import { composing } from "../../lib/send-history";
import {
  changed, checkError, choiceLabel, clearedSecrets, clearExtensionSecret, compact, configError, fieldLabel, fieldTitle, isDirty, listItems, listExtensionsInfo, listSummary, numberProblems,
  placeholder, extensionDetail, extensionState, extensionTone, pointText, readNumber, runNote, same, secretState, setAt, setContribution, setExtensionConfig, setExtensionEnabled,
  channelOf, machineStatus, nodeProgramStatus, sandboxEgress, sandboxState, saveFooter, setExtensionAutoUpgrade, setExtensionSecret, sourceText, stateLine, valueAt,
  type HubExtension, type HubExtensionDetail, type Json, type JsonObject, type ExtensionField, type ExtensionNodeState, type ExtensionSandbox, type ExtensionSecret,
} from "../../lib/extensions";
import { Button, cx, Dialog, EASE, IconButton, Loading, Spinner, toast } from "../../ui/atoms";
import { TriageRuleField } from "../../ui/TriageRuleField";
import { Icon } from "../../ui/icons";
import { ChannelLink } from "./Channels";
import { MarketCatalog, MarketItem, Markets, UninstallDialog } from "./ExtensionMarket";
import { ExtensionSettingsPage } from "./ExtensionSettingsPage";
import { ExtensionUI } from "./ExtensionView";
import { ExtensionConnectorInbox } from "./HubConnector";
import { getAuth, listAuthNote, onAuthChange, shownPages, signInAsked, takeSignIn, withAuth, type ExtensionAuth } from "../../lib/extension-auth";
import { AuthGroup } from "./ExtensionAuth";
import { dependencyText, dependentError, type Contribution, type Dependent } from "../../lib/extension-model";
import { Group, Pill, Row, Switch, SettingsHint } from "./SettingsParts";
import { ManagerHeading } from "../ManagerParts";
import { formatList, t } from "../../i18n";

/**
 * The tab's pages, by its `extension` parameter (lib/extension-market.ts extensionsRoute): the list, an extension, a page an extension
 * draws, and the market (its catalog, one extension of it, the markets).
 */
export function Extensions({ open, onOpen, onChannel, onConnector }: {
  open: string | null;
  onOpen: (id: string | null) => void;
  /** `manager`: another manager's channel / connector (its extension's page links to its own); none: the default's. */
  onChannel?: (id: string, manager?: string) => void;
  onConnector?: (id: string, manager?: string) => void;
}) {
  const r = extensionsRoute(open);
  const go = (to: Parameters<typeof routeSub>[0]) => onOpen(routeSub(to));
  switch (r.kind) {
    case "extension":
      return (
        <OwnedExtension id={r.id} manager={r.manager}>
          {(manager) => (
            <ExtensionDetail
              key={`${r.id}@${manager ?? ""}`}
              id={r.id}
              manager={manager}
              onBack={() => onOpen(null)}
              onPage={(ui) => go({ kind: "page", id: r.id, ui, manager })}
              onUpgrade={(market, id) => go({ kind: "item", market, id, manager })}
              onChannel={onChannel}
              onConnector={onConnector}
            />
          )}
        </OwnedExtension>
      );
    case "page":
      return (
        <OwnedExtension id={r.id} manager={r.manager}>
          {(manager) => <ExtensionPage key={`${open}@${manager ?? ""}`} id={r.id} ui={r.ui} manager={manager} onBack={() => go({ kind: "extension", id: r.id, manager })} />}
        </OwnedExtension>
      );
    case "catalog":
      return <MarketCatalog onBack={() => onOpen(null)} onOpen={(it) => go({ kind: "item", market: it.market, id: it.id })} onMarkets={() => go({ kind: "markets" })} />;
    case "item":
      // another manager's upgrade goes back to that manager's extension: the catalog is the default's
      return (
        <MarketItem
          key={open}
          market={r.market}
          id={r.id}
          manager={r.manager}
          onBack={() => go(r.manager ? { kind: "extension", id: r.id, manager: r.manager } : { kind: "catalog" })}
          onInstalled={(id) => go({ kind: "extension", id, manager: r.manager })}
        />
      );
    case "markets":
      return <Markets onBack={() => go({ kind: "catalog" })} />;
    default:
      return <ExtensionList onOpen={onOpen} onMarket={() => go({ kind: "catalog" })} onMarkets={() => go({ kind: "markets" })} />;
  }
}

/**
 * Whose extension a route is: the manager it names (`<id>@<manager>`), else, for a link that names none (written by
 * hand, or before several managers), the one the merged list has it under: the default's when it has it, else the
 * manager that has it, so the page doesn't open on a manager without it (a 404).
 */
function OwnedExtension({ id, manager, children }: { id: string; manager?: string; children: (manager: string | undefined) => React.ReactNode }) {
  const [found, setFound] = useState<{ id: string; manager?: string } | null>(null);
  const roster = useRoster();
  const known = useKnownDefault();
  const several = multiManager(roster); // one manager: every extension is its own, nothing to look up
  useEffect(() => {
    if (manager || !several) return;
    let live = true;
    listExtensionsInfo()
      .then(({ extensions }) => {
        if (!live) return;
        const listed = extensions.filter((x) => x.id === id);
        const own = listed.some((x) => !x.manager || x.manager === defaultManager());
        setFound({ id, manager: own ? undefined : listed[0]?.manager });
      })
      .catch(() => live && setFound({ id })); // the page itself says what is wrong
    return () => {
      live = false;
    };
  }, [id, manager, several]);
  // before the roster is read it is not known whose a link without a manager is: nothing of the default's is drawn
  if (!manager && !known) return <div className="p-6"><Loading /></div>;
  if (manager || !several) return <>{children(manager)}</>;
  if (found?.id !== id) return <div className="p-6"><Loading /></div>;
  return <>{children(found.manager)}</>;
}

/** A page an extension draws (placement plugin_page), under the extension's name. */
function ExtensionPage({ id, ui, manager, onBack }: { id: string; ui: string; manager?: string; onBack: () => void }) {
  usePinnedExtension(id, manager);
  const [d, setD] = useState<HubExtensionDetail | null>(null);
  const [n, setN] = useState(0);
  useEffect(() => {
    extensionDetail(id).then(setD, () => {});
  }, [id]);
  const title = d?.ui?.find((u) => u.id === ui)?.title;
  return (
    <>
      <div className="mb-3 flex items-center gap-2">
        <Button tone="quiet" icon="chevronLeft" onClick={onBack} className="-ml-2">
          <span className="max-w-[240px] truncate">{d?.name ?? id}</span>
        </Button>
        <span className="flex-1" />
        <IconButton icon="retry" label={t("settings-extensions.redraw")} onClick={() => setN((x) => x + 1)} />
      </div>
      <ExtensionUI key={n} extension={id} ui={ui} title={title} manager={manager} />
    </>
  );
}

// A plain list (joined with the Chinese enumeration comma, "a, b, c"): what these rows always showed in Chinese.
const NARROW_LIST: Intl.ListFormatOptions = { type: "conjunction", style: "narrow" };

const DOT: Record<ReturnType<typeof extensionTone>, string> = { red: "bg-red", orange: "bg-orange", green: "bg-green", idle: "bg-line-strong" };
const TILE: Record<ReturnType<typeof extensionTone>, string> = {
  red: "bg-red-tint text-red",
  orange: "bg-orange-tint text-orange",
  green: "bg-accent-tint text-accent-ink",
  idle: "bg-inset text-ink-3 shadow-hairline",
};

function StateDot({ extension }: { extension?: HubExtension | null }) {
  return <span aria-hidden className={cx("size-1.5 shrink-0 rounded-full", DOT[extensionTone(extension)])} />;
}

// ── the list ────────────────────────────────────────────

function ExtensionList({ onOpen, onMarket, onMarkets }: { onOpen: (id: string) => void; onMarket: () => void; onMarkets: () => void }) {
  const [extensions, setExtensions] = useState<HubExtension[] | null>(null);
  const [markets, setMarkets] = useState(false);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  // Reads are numbered: an answer older than the last one asked for is dropped.
  const reads = useRef(0);
  const load = () => {
    const mine = ++reads.current;
    setLoading(true);
    listExtensionsInfo()
      .then((r) => mine === reads.current && (setExtensions(r.extensions), setMarkets(r.markets), setError("")))
      .catch((e) => mine === reads.current && setError(errorText(e)))
      .finally(() => mine === reads.current && setLoading(false));
  };
  useEffect(load, []);
  // An install, upgrade or uninstall that finished (here, on another device, or the hub's auto-upgrade) changes the
  // list: lib/extension-operations.ts sends EXTENSIONS_CHANGED when one succeeds. watchOperations starts its push
  // listener, which otherwise only a market page starts. Several at once read the list once.
  useEffect(() => {
    let queued = false;
    const again = () => {
      if (queued) return;
      queued = true;
      queueMicrotask(() => {
        queued = false;
        load();
      });
    };
    window.addEventListener(EXTENSIONS_CHANGED, again);
    const stop = watchOperations(() => {});
    return () => {
      window.removeEventListener(EXTENSIONS_CHANGED, again);
      stop();
    };
  }, []);
  // a sign-in that changed (signed in, out, expired) shows at once
  // only the row of the manager whose extension signed in (or out): another manager's of the same id stays as it is
  useEffect(() => onAuthChange((id, auth, from) => setExtensions((cur) => cur?.map((x) => ((x.manager || defaultManager()) === from ? withAuth(x, id, auth) : x)) ?? cur)), []);
  // a reconnect the hub could not resume: the extension_auth pushes missed meanwhile are not replayed (#4954)
  useEffect(() => onHubEvent("hello", (m) => !isResumed(m) && load()), []);
  // with several managers, each one's extensions under its name (docs/assistant/managers.md "Clients"); one: as before
  const roster = useRoster();
  const groups = groupByManager(extensions ?? [], roster, (x) => x.manager);

  return (
    <>
      <p className="mb-5 px-1 text-[12.5px] leading-relaxed text-ink-3">
        {t("settings-extensions.intro")}
      </p>
      {/* Extension market first, above the installed list (#5017): where a phone or a browser gets another extension. */}
      {markets && (
        <Group title={t("settings-extensions.market_title")} footer={t("settings-extensions.market_footer")} anchor="market">
          <Row
            label={
              <span className="flex items-center gap-2">
                <Icon name="search" size={13} className="shrink-0 text-ink-3" />
                {t("settings-extensions.browse_market")}
              </span>
            }
            onClick={onMarket}
          />
          <Row
            label={
              <span className="flex items-center gap-2">
                <Icon name="gear" size={13} className="shrink-0 text-ink-3" />
                {t("settings-extensions.manage_markets")}
              </span>
            }
            onClick={onMarkets}
          />
        </Group>
      )}
      <Group
        anchor="extensions"
        title={
          <span className="flex items-center gap-2">
            {t("settings-extensions.title")}
            {extensions && <span className="font-normal">{listSummary(extensions)}</span>}
          </span>
        }
        right={<IconButton icon="retry" label={t("settings-extensions.refresh")} onClick={load} disabled={loading} size={24} iconSize={13} className="-mb-1" />}
        footer={t("settings-extensions.list_footer")}
      >
        {!extensions && !error && (
          <div className="px-4 py-3">
            <Loading />
          </div>
        )}
        {error && (
          <div className="flex items-center gap-3 px-4 py-3">
            <p className="min-w-0 flex-1 text-[12.5px] break-words text-red">{t("settings-extensions.list_error", { error })}</p>
            <Button tone="secondary" icon="retry" onClick={load} busy={loading}>
              {t("settings-extensions.retry")}
            </Button>
          </div>
        )}
        {extensions?.length === 0 && (
          <div className="flex flex-col items-center gap-2 px-6 py-8 text-center">
            <span className="flex size-10 items-center justify-center rounded-[10px] bg-inset text-ink-3 shadow-hairline">
              <Icon name="extension" size={18} />
            </span>
            <p className="text-[13px] font-medium text-ink">{t("settings-extensions.empty_title")}</p>
            <p className="max-w-[420px] text-[12px] leading-relaxed text-ink-3">{t("settings-extensions.empty_detail")}</p>
          </div>
        )}
        {(groups ?? [{ manager: null, items: extensions ?? [] }]).map((g) => [
          g.manager && (
            <div key={`h:${g.manager.id}`} className="bg-inset px-4 py-1.5">
              <ManagerHeading manager={g.manager} count={g.items.length} />
            </div>
          ),
          ...g.items.map((p) => {
            const i = (extensions ?? []).indexOf(p);
            return (
              <button
                key={`${p.manager ?? ""}:${p.id}`}
                type="button"
                onClick={() => onOpen(routeSub({ kind: "extension", id: p.id, manager: p.manager && p.manager !== defaultManager() ? p.manager : undefined }) ?? p.id)}
                className="flex w-full items-center gap-3 px-4 py-2.5 text-left transition-colors hover:bg-hover"
                style={{ animation: `fade-up 320ms ${EASE} ${Math.min(i, 8) * 30}ms both` }}
              >
                <span className={cx("flex size-8 shrink-0 items-center justify-center rounded-[8px]", TILE[extensionTone(p)])}>
                  <Icon name="extension" size={15} />
                </span>
                <div className="min-w-0 flex-1">
                  <div className="flex min-w-0 items-center gap-2">
                    <span className="truncate text-[13px] text-ink">{p.name}</span>
                    {p.source && p.source !== "installed" && <Pill>{sourceText(p.source)}</Pill>}
                  </div>
                  {(p.needs_setup?.length || p.error || p.description) && (
                    <div className={cx("mt-0.5 truncate text-[11.5px]", p.error ? "text-red" : p.needs_setup?.length ? "text-orange" : "text-ink-3")}>
                      {p.error || (p.needs_setup?.length ? t("settings-extensions.needs_setup", { items: formatList(p.needs_setup, NARROW_LIST) }) : p.description)}
                    </div>
                  )}
                </div>
                <span className="flex shrink-0 items-center gap-1.5 whitespace-nowrap text-[12px] text-ink-2">
                  <StateDot extension={p} />
                  {extensionState(p)}
                  {listAuthNote(p.auth)}
                  {p.upgrade?.state === "needs_permission" && t("settings-extensions.upgrade_pending")}
                </span>
                <Icon name="chevronRight" size={12} stroke={2} className="shrink-0 text-ink-3" />
              </button>
            );
          }),
        ])}
      </Group>
    </>
  );
}

// ── one extension ──────────────────────────────────────────

/** A number field as typed, by dotted path, until it reads as one. */
type Typed = Record<string, { text: string; integer: boolean }>;

/** Requests about extension id (its details, settings, models, pages) go to manager while the view is open: the same
 *  id may be installed under several managers (each one's built-ins), and the default's own keep going to it elsewhere. */
function usePinnedExtension(id: string, manager: string | undefined) {
  useLayoutEffect(() => pinManagerAt([`/extensions/${enc(id)}`, `/plugins/${enc(id)}`], manager), [id, manager]);
}

function ExtensionDetail({ id, manager, onBack, onPage, onUpgrade, onChannel, onConnector }: {
  id: string;
  /** Whose extension it is (several managers); none: the default's. */
  manager?: string;
  onBack: () => void;
  onPage: (ui: string) => void;
  onUpgrade: (market: string, id: string) => void;
  onChannel?: (id: string, manager?: string) => void;
  onConnector?: (id: string, manager?: string) => void;
}) {
  usePinnedExtension(id, manager);
  const roster = useRoster();
  const offline = roster?.managers.find((m) => m.id === (manager || defaultManager()))?.online === false;
  const [removing, setRemoving] = useState(false);
  const [detail, setDetail] = useState<HubExtensionDetail | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [draft, setDraft] = useState<JsonObject>({});
  const [typed, setTyped] = useState<Typed>({});
  // The hub's word on a field it refused, by dotted path ("" = the whole form).
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});
  // The extension's own word on a save it turned down (check_failed, check_rate_limited, settings_moved): shown at the
  // save bar, where the draft stays to be changed and saved again; gone once the draft changes.
  const [checkFailed, setCheckFailed] = useState("");
  const [saving, setSaving] = useState(false);
  const [toggling, setToggling] = useState(false);
  const [secret, setSecret] = useState<ExtensionSecret | null>(null);
  const [leaving, setLeaving] = useState(false);
  const [dependents, setDependents] = useState<Dependent[] | null>(null);

  const apply = (d: HubExtensionDetail) => {
    setDetail(d);
    setDraft(d.config ?? {});
    setError("");
  };
  // Reads of the detail overlap (opening the page, an operation that succeeded, a save that cleared secrets), and so do
  // the writes that answer with the whole detail. All are numbered in the order they leave, and an answer is dropped
  // when one that left after it is already shown, so an older detail (the version before an upgrade, the settings before
  // a save) is not put back.
  const order = useRef({ asked: 0, shown: 0 });
  const readDetail = (then: (d: HubExtensionDetail) => void, failed?: (e: unknown) => void) => {
    const n = ++order.current.asked;
    return extensionDetail(id).then(
      (d) => {
        if (n <= order.current.shown) return;
        order.current.shown = n;
        then(d);
      },
      (e) => void (n > order.current.shown && failed?.(e)),
    );
  };
  /** A write leaving: its number, for `wrote` when it answers. */
  const writing = () => ++order.current.asked;
  /** A write answered with `d`: shown (and true) unless something that left after it is shown already. */
  const wrote = (n: number, d: HubExtensionDetail) => {
    if (n <= order.current.shown) return false;
    order.current.shown = n;
    setDetail(d);
    return true;
  };
  const load = () => {
    setLoading(true);
    void readDetail((d) => (apply(d), setTyped({}), setFieldErrors({})), (e) => setError(errorText(e))).finally(() => setLoading(false));
  };
  useEffect(load, [id]);
  // its sign-in as the hub pushes it; an install whose next step is signing in opens the dialog once
  useEffect(() => onAuthChange((x, auth, from) => (manager || defaultManager()) === from && setDetail((cur) => (cur ? withAuth(cur, x, auth) : cur))), [manager]);
  const [startSignIn] = useState(() => signInAsked(id));
  useEffect(() => void takeSignIn(id), [id]);
  const authChanged = (a?: ExtensionAuth) => {
    if (a) setDetail((cur) => (cur ? { ...cur, auth: a } : cur));
    else void getAuth(id).then((v) => setDetail((cur) => (cur ? { ...cur, auth: v } : cur)), () => {});
  };
  // a reconnect the hub could not resume: an extension_auth push missed meanwhile is not replayed (#4954); only the
  // sign-in is read again, so what is being typed into the settings stays
  useEffect(() => onHubEvent("hello", (m) => !isResumed(m) && authChanged()), [id]);

  const problems = useMemo(() => numberProblems(typed), [typed]);
  const dirty = !!detail && (isDirty(draft, detail.config) || Object.keys(problems).length > 0);
  const canSave = dirty && !Object.keys(problems).length && !saving && !offline;
  const fields = detail?.form?.fields ?? [];
  const editable = !!detail && !detail.read_only && detail.source !== "dev" && detail.source !== "hub.json";

  // An operation of this extension that succeeds while the page is open (an upgrade, also the hub's auto-upgrade, or
  // an install or uninstall from another device): its version, upgrades and permissions are read again. A dirty form
  // keeps its draft, as a switch does; an uninstall goes back to the list. Those that ended before the page opened
  // are not acted on: when this page is the first to start listening, the hub's list (which keeps ended operations a
  // while) arrives after it opened, and what it brings that was not pushed meanwhile is old.
  const dirtyNow = useRef(dirty);
  dirtyNow.current = dirty;
  const backNow = useRef(onBack);
  backNow.current = onBack;
  useEffect(() => {
    // this manager's operations of the extension: another manager's upgrade of the same id is not this page's
    const ended = () => operations().filter((o) => o.extension === id && o.state === "succeeded" && opOf(o, manager));
    const seen = new Set(ended().map((o) => o.id));
    let listed = operationsListed();
    return watchOperations(() => {
      if (!listed && operationsListed()) {
        listed = true;
        ended().forEach((o) => seen.add(o.id));
        return;
      }
      const fresh = ended().filter((o) => !seen.has(o.id));
      if (!fresh.length) return;
      fresh.forEach((o) => seen.add(o.id));
      // newest first: an uninstall followed by a fresh install stays on the page
      if (fresh[0].kind === "uninstall") return backNow.current();
      void readDetail((d) => (dirtyNow.current ? setDetail(d) : (apply(d), setTyped({}), setFieldErrors({}))));
    });
  }, [id, manager]);

  const edit = (path: string[], value: Json | undefined) => {
    setDraft((d) => setAt(d, path, value));
    const key = path.join(".");
    if (fieldErrors[key] || fieldErrors[""]) setFieldErrors(({ [key]: _a, [""]: _b, ...rest }) => rest);
    setCheckFailed("");
  };
  const type = (path: string[], text: string, integer: boolean) => {
    const key = path.join(".");
    setTyped((t) => ({ ...t, [key]: { text, integer } }));
    setCheckFailed("");
    const n = readNumber(text, integer);
    if (!n.error) edit(path, n.value);
  };
  const revert = () => {
    if (!detail) return;
    setDraft(detail.config ?? {});
    setTyped({});
    setFieldErrors({});
    setCheckFailed("");
  };

  const save = async () => {
    if (!canSave) return;
    setSaving(true);
    setFieldErrors({});
    setCheckFailed("");
    const n = writing();
    try {
      const d = await setExtensionConfig(id, compact(draft));
      if (wrote(n, d)) apply(d);
      setTyped({});
      toast(t("settings-extensions.saved_restart"), "ok");
    } catch (e) {
      const ce = configError(e);
      const refused = checkError(e);
      if (ce) setFieldErrors({ [ce.field]: ce.message });
      else if (refused) setCheckFailed(refused);
      else toast(t("settings-extensions.not_saved", { error: errorText(e) }), "error");
      // The configuration stayed as it was, but some of its secrets are gone: read the page again so the Secrets group
      // says so; the draft and what is being typed stay. The hub restarts the program without them, so its entries,
      // cards and quick prompts are asked for again too, as after clearing a secret.
      if (clearedSecrets(e).length) {
        changed(null);
        void readDetail(setDetail);
      }
    } finally {
      setSaving(false);
    }
  };

  const toggleAutoUpgrade = async (on: boolean) => {
    setToggling(true);
    const n = writing();
    try {
      wrote(n, await setExtensionAutoUpgrade(id, on));
      toast(on ? t("settings-extensions.auto_upgrade_on") : t("settings-extensions.auto_upgrade_off"), "ok");
    } catch (e) {
      toast(t(on ? "settings-extensions.auto_upgrade_on_failed" : "settings-extensions.auto_upgrade_off_failed", { error: errorText(e) }), "error");
    } finally {
      setToggling(false);
    }
  };

  const toggle = async (on: boolean, cascade = false) => {
    setToggling(true);
    const n = writing();
    try {
      const d = await setExtensionEnabled(id, on, cascade);
      // A switch never overwrites the unsaved configuration draft.
      wrote(n, d);
      setDependents(null);
      toast(t(on ? "settings-extensions.enabled_name" : "settings-extensions.disabled_name", { name: d.name }), "ok");
    } catch (e) {
      const deps = dependentError(e);
      if (!on && deps) setDependents(deps);
      else toast(t(on ? "settings-extensions.enable_failed" : "settings-extensions.disable_failed", { error: errorText(e) }), "error");
    } finally {
      setToggling(false);
    }
  };
  const toggleContribution = async (c: Contribution, on: boolean) => {
    setToggling(true);
    const n = writing();
    try {
      wrote(n, await setContribution(id, c.point, c.id, on));
      toast(on ? t("settings-extensions.contribution_on") : t("settings-extensions.contribution_off"), "ok");
    } catch (e) {
      toast(t("settings-extensions.change_failed", { error: errorText(e) }), "error");
    } finally {
      setToggling(false);
    }
  };

  // A machine's switch (format machine) is saved at once, on top of what the hub has; the rest of the draft stays.
  const saveNow = async (path: string[], value: Json | undefined) => {
    if (!detail) return;
    const saved = detail.config ?? {};
    setDraft((d) => setAt(d, path, value));
    const n = writing();
    try {
      const d = await setExtensionConfig(id, compact(setAt(saved, path, value)));
      if (wrote(n, d)) setDraft((cur) => setAt(cur, path, valueAt(d.config ?? {}, path)));
    } catch (e) {
      setDraft((cur) => setAt(cur, path, valueAt(saved, path)));
      toast(t("settings-extensions.not_saved", { error: errorText(e) }), "error");
    }
  };
  const computers = !!detail?.contributes?.computer;
  const nodes = detail?.node_machines ? { field: detail.node_machines, states: detail.nodes ?? [] } : undefined;

  // A secret's answer is a whole detail too; keep the form's draft as it is.
  // The dialog's request is numbered as it leaves, like the page's own writes.
  const secretStart = () => writing();
  const secretDone = (n: number, d: HubExtensionDetail) => wrote(n, d);

  const back = () => (dirty ? setLeaving(true) : onBack());
  const p = detail;

  return (
    <div>
      <div className="relative mb-3 flex items-center gap-2">
        <Button tone="quiet" icon="chevronLeft" onClick={back} className="-ml-2">
          {t("settings-extensions.title")}
        </Button>
        {leaving && (
          <span className="absolute top-full left-0 z-10 mt-1 flex items-center gap-1 rounded-control bg-surface p-1 shadow-overlay" style={{ animation: `pop-in 160ms ${EASE} both` }}>
            <span className="px-2 text-[12px] whitespace-nowrap text-ink-2">{t("settings-extensions.unsaved")}</span>
            <Button tone="quiet" onClick={() => setLeaving(false)}>
              {t("settings-extensions.keep_editing")}
            </Button>
            <Button tone="danger" onClick={onBack}>
              {t("settings-extensions.discard")}
            </Button>
          </span>
        )}
        <span className="flex-1" />
        <IconButton icon="retry" label={t("settings-extensions.reload")} onClick={load} disabled={loading} />
      </div>

      {/* what it is and how it is doing */}
      <section className="mb-6 overflow-hidden rounded-card bg-surface shadow-card">
        <div className="flex items-center gap-3 px-4 py-3.5">
          <span className={cx("flex size-11 shrink-0 items-center justify-center rounded-[11px]", TILE[extensionTone(p)])}>
            <Icon name="extension" size={20} />
          </span>
          <div className="min-w-0 flex-1">
            <h2 className="truncate text-[15px] font-semibold text-ink">{p?.name ?? id}</h2>
            <div className="mt-0.5 flex min-w-0 items-center gap-1.5 text-[12px] text-ink-3">
              <StateDot extension={p} />
              <span className="truncate">{p ? stateLine(p) : loading ? t("settings-extensions.loading") : t("settings-extensions.unavailable")}</span>
            </div>
          </div>
          {loading && !p && <Spinner />}
        </div>
        {(p?.description || (p && runNote(p)) || p?.needs_setup?.length || p?.error || error) && (
          <div className="flex flex-col gap-1.5 border-t border-line px-4 py-3">
            {p?.description && <p className="text-[12.5px] leading-relaxed text-ink-2">{p.description}</p>}
            {p && runNote(p) && <p className="text-[11.5px] text-ink-3">{runNote(p)}</p>}
            {!!p?.needs_setup?.length && <p className="text-[12px] text-orange">{t("settings-extensions.needs_setup", { items: formatList(p.needs_setup, NARROW_LIST) })}</p>}
            {p?.error && <p className="text-[12px] break-words text-red select-text">{p.error}</p>}
            {error && <p className="text-[12px] break-words text-red select-text">{t("settings-extensions.detail_error", { error })}</p>}
          </div>
        )}
      </section>

      {p && (
        <ExtensionSettingsPage
          detail={p}
          manager={manager}
          busy={toggling || saving}
          offline={offline}
          onEnabled={(on) => void toggle(on)}
          onAutoUpgrade={(on) => void toggleAutoUpgrade(on)}
          onUpgrade={onUpgrade}
          onUninstall={() => setRemoving(true)}
          onChecked={async () => { await readDetail((d) => dirtyNow.current ? setDetail(d) : apply(d), (error) => { throw error; }); }}
          configuration={<>
          {p.auth && <AuthGroup id={p.id} auth={p.auth} enabled={p.enabled ?? true} start={startSignIn} onChange={authChanged} />}
          {onChannel && channelOf(p) && <ChannelLink id={channelOf(p)!} manager={manager} onOpen={onChannel} />}
          {!editable && <Group><Row label={t(p.read_only || p.source === "hub.json" ? "settings-extensions.read_only" : "settings-extensions.externally_managed")} wrapHint /></Group>}
          {editable && !fields.length && !p.secrets?.length && !shownPages(p.ui, p.auth).length && !p.auth && !channelOf(p) && !p.contributes?.connectors?.length && <Group><Row label={t("settings-extensions.no_configuration")} /></Group>}

          {shownPages(p.ui, p.auth).length > 0 && (
            <Group title={t("settings-extensions.pages_title")} footer={t("settings-extensions.pages_footer")}>
              {shownPages(p.ui, p.auth).map((u) => (
                <Row
                  key={u.id}
                  label={
                    <span className="flex items-center gap-2">
                      <Icon name="layers" size={13} className="shrink-0 text-ink-3" />
                      {u.title || u.id}
                    </span>
                  }
                  onClick={() => onPage(u.id)}
                />
              ))}
            </Group>
          )}

          {(p.enabled ?? true) && !!p.contributes?.connectors?.length && <ExtensionConnectorInbox ids={p.contributes.connectors} manager={manager} onOpen={onConnector} />}

          {editable && fields.length > 0 && (
            <>
              {fieldErrors[""] && <p role="alert" className="mb-3 rounded-[8px] bg-red-tint px-3 py-2 text-[12px] break-words text-red">{t("settings-extensions.not_saved", { error: fieldErrors[""] })}</p>}
              {fields.some((f) => f.type !== "object") && (
                <Group footer={saveFooter(fields)}>
                  {fields
                    .filter((f) => f.type !== "object")
                    .map((f) => (
                      <FieldRow key={f.name} field={f} path={[f.name]} draft={draft} typed={typed} errors={fieldErrors} problems={problems} onEdit={edit} onType={type} onSaveNow={saveNow} computers={computers} nodes={nodes} />
                    ))}
                </Group>
              )}
              {fields
                .filter((f) => f.type === "object")
                .map((g) => (
                  <Group key={g.name} title={fieldLabel(g)} footer={g.description}>
                    {(g.fields ?? []).map((f) => (
                      <FieldRow key={f.name} field={f} path={[g.name, f.name]} draft={draft} typed={typed} errors={fieldErrors} problems={problems} onEdit={edit} onType={type} onSaveNow={saveNow} computers={computers} nodes={nodes} />
                    ))}
                  </Group>
                ))}
            </>
          )}

          {editable && !!p.secrets?.length && (
            <Group title={t("settings-extensions.secrets_title")} footer={t("settings-extensions.secrets_footer")}>
              {p.secrets.map((s) => (
                <Row
                  key={s.name}
                  label={
                    <span className="flex items-center gap-2">
                      <Icon name="key" size={13} className="shrink-0 text-ink-3" />
                      {s.title}
                    </span>
                  }
                  hint={s.description}
                  onClick={() => setSecret(s)}
                >
                  <span className={cx(!s.set && s.required && "text-orange")}>{secretState(s)}</span>
                </Row>
              ))}
            </Group>
          )}

          </>}
          information={<>
          {!!p.depends?.length && (
            <Group title={t("settings-extensions.depends_title")} footer={t("settings-extensions.depends_footer")}>
              {p.depends.map((d) => <Row key={d.id} label={d.name || d.id} hint={d.version}><span className={cx(d.state !== "ok" && "text-orange")}>{dependencyText(d)}</span></Row>)}
            </Group>
          )}
          {!!p.extension_pack?.length && (
            <Group title={t("settings-extensions.pack_title")} footer={t("settings-extensions.pack_footer")}>
              {p.extension_pack.map((m) => <Row key={m.id} label={m.id} hint={m.market} mono>{m.version || ""}</Row>)}
            </Group>
          )}
          {!!p.contributions?.length && (
            <Group title={t("settings-extensions.contributions_title")} footer={t("settings-extensions.contributions_footer")}>
              {p.contributions.map((c) => (
                <Row key={`${c.point}/${c.id}`} label={c.title || c.id} hint={`${pointText(c.point)} · ${c.id}${c.via === "connector" ? t("settings-extensions.via_connector") : c.via === "config" ? t("settings-extensions.via_config") : ""}`}>
                  <Switch label={c.title || c.id} on={c.enabled} disabled={!editable || toggling || offline || !c.can_disable} onChange={(on) => void toggleContribution(c, on)} />
                </Row>
              ))}
            </Group>
          )}
          {!p.contributions?.length && p.contributes && Object.keys(p.contributes).length > 0 && (
            <Group title={t("settings-extensions.provides_title")}>
              {Object.keys(p.contributes)
                .sort()
                .map((point) => (
                  <Row key={point} label={pointText(point)} mono>
                    <span className="truncate">{formatList(p.contributes?.[point] ?? [], NARROW_LIST)}</span>
                  </Row>
                ))}
            </Group>
          )}

          {p.sandbox && <SandboxGroup s={p.sandbox} />}

          {!!p.permissions?.length && (
            <Group title={t("settings-extensions.permissions_title")} footer={t("settings-extensions.permissions_footer")}>
              {p.permissions.map((perm) => (
                <div key={perm.key} className="px-4 py-2.5">
                  <div className="text-[13px] text-ink">{perm.title}</div>
                  {perm.detail && <div className="mt-0.5 text-[11.5px] leading-snug text-ink-3">{perm.detail}</div>}
                </div>
              ))}
            </Group>
          )}

          </>}
          saveBar={dirty && (
            <Group>
              {checkFailed && <SettingsHint alert>{t("settings-extensions.not_saved", { error: checkFailed })}</SettingsHint>}
              <Row label={Object.keys(problems).length ? t("settings-extensions.not_a_number") : t("settings-extensions.unsaved")}>
                <Button tone="quiet" onClick={revert} disabled={saving}>{t("settings-extensions.revert")}</Button>
                <Button tone="accent" busy={saving} disabled={!canSave} onClick={() => void save()}>{t("common.save")}</Button>
              </Row>
            </Group>
          )}
        />
      )}
      {removing && p && <UninstallDialog id={p.id} name={p.name} version={p.version} pack={p.extension_pack} onClose={() => setRemoving(false)} onDone={onBack} />}
      {dependents && p && (
        <Dialog open title={t("settings-extensions.disable_dependents_title", { name: p.name })} onClose={() => !toggling && setDependents(null)} footer={<><Button tone="quiet" disabled={toggling} onClick={() => setDependents(null)}>{t("common.cancel")}</Button><Button tone="danger" busy={toggling} onClick={() => void toggle(false, true)}>{t("settings-extensions.disable_all")}</Button></>}>
          <p className="text-ink-2">{t("settings-extensions.dependents_body", { names: formatList(dependents.map((d) => d.name), NARROW_LIST) })}</p>
        </Dialog>
      )}


      {secret && <SecretDialog extension={id} secret={secret} onClose={() => setSecret(null)} onStart={secretStart} onDone={secretDone} />}
    </div>
  );
}

/** "Isolation" (Android ExtensionModelParts sandbox()): whether the sandbox is in effect and why, what went through the
 *  egress proxy and the last thing it refused. Only looked at here; it is changed on the hub's side. */
export function SandboxGroup({ s }: { s: ExtensionSandbox }) {
  const egress = sandboxEgress(s);
  return (
    <Group title={t("settings-extensions.sandbox_title")} footer={t("settings-extensions.sandbox_footer")}>
      <div className="flex items-start gap-3 px-4 py-2.5">
        <Icon name="lock" size={14} className={cx("mt-0.5 shrink-0", s.active ? "text-green" : s.enabled ? "text-orange" : "text-ink-3")} />
        <div className="min-w-0 flex-1">
          <div className="text-[13px] text-ink">{sandboxState(s)}</div>
          {s.why && <div className="mt-0.5 text-[11.5px] leading-snug break-words text-ink-3">{s.why}</div>}
        </div>
      </div>
      {egress && (
        <div className="px-4 py-2.5">
          <div className="text-[12.5px] text-ink-2">{egress}</div>
          {s.egress?.last_denied && (
            <div className="mt-0.5 font-mono text-[11.5px] break-all text-ink-3 select-text">{t("settings-extensions.sandbox_last_denied", { target: s.egress.last_denied })}</div>
          )}
        </div>
      )}
    </Group>
  );
}

// ── the form ────────────────────────────────────────────

const inputClass = "h-8 w-full rounded-control bg-field px-2.5 text-[12.5px] text-ink outline-none placeholder:text-ink-3 focus:shadow-[inset_0_0_0_1px_var(--accent)] disabled:opacity-60";

interface FieldProps {
  field: ExtensionField;
  path: string[];
  draft: JsonObject;
  typed: Typed;
  errors: Record<string, string>;
  problems: Record<string, string>;
  onEdit: (path: string[], value: Json | undefined) => void;
  onType: (path: string[], text: string, integer: boolean) => void;
  /** Saves one field at once (a machine's switch). */
  onSaveNow: (path: string[], value: Json | undefined) => void;
  /** The extension operates computers: machine rows say how Cua Driver is there. */
  computers: boolean;
  /** The plugin's program on computers: the field that picks them, and how it is on each (their rows say). */
  nodes?: { field: string; states: ExtensionNodeState[] };
}

/** One field of the form, by its type; its title above (or beside a switch), its description and error below. */
function FieldRow(props: FieldProps) {
  const { field: f, path, draft, errors, problems, onEdit } = props;
  const key = path.join(".");
  const err = errors[key] ?? problems[key];
  const title = fieldTitle(f);
  const id = `extension-field-${key}`;
  const below = (
    <>
      {f.description && <p className="mt-1 text-[11.5px] leading-snug text-ink-3">{f.description}</p>}
      {err && (
        <p role="alert" className="mt-1 text-[11.5px] text-red">
          {err}
        </p>
      )}
    </>
  );

  if (f.type === "boolean") {
    const v = valueAt(draft, path);
    const on = typeof v === "boolean" ? v : f.default === true;
    return (
      <div className="px-4 py-2.5" data-field={key}>
        <div className="flex items-center gap-3">
          <span className="min-w-0 flex-1 text-[13px] text-ink">{title}</span>
          <Switch on={on} onChange={(x) => onEdit(path, x)} label={fieldLabel(f)} />
        </div>
        {below}
      </div>
    );
  }

  return (
    <div className="px-4 py-2.5" data-field={key}>
      <label htmlFor={id} className="mb-1.5 block text-[12px] font-medium text-ink-2">
        {title}
      </label>
      <Control {...props} id={id} invalid={!!err} />
      {below}
    </div>
  );
}

function Control({ field: f, path, draft, typed, onEdit, onType, onSaveNow, computers, nodes, id, invalid }: FieldProps & { id: string; invalid: boolean }) {
  const ring = invalid ? "shadow-[inset_0_0_0_1px_var(--red)]" : "shadow-inset-field";
  const v = valueAt(draft, path);

  if (f.type === "string" && f.name === "triage_rules") return <TriageRuleField id={id} value={typeof v === "string" ? v : ""} placeholder={placeholder(f)} maxLength={f.max_length} invalid={invalid} onChange={(text) => onEdit(path, text || undefined)} />;

  if (f.type === "array" && f.items?.format === "machine") {
    return <MachineSwitches field={f} path={path} draft={draft} onSaveNow={onSaveNow} computers={computers} nodes={nodes?.field === f.name ? nodes.states : undefined} id={id} />;
  }
  if (f.type === "array") return <ListControl field={f} path={path} draft={draft} onEdit={onEdit} id={id} ring={ring} />;

  if (f.enum?.length) {
    const at = f.enum.findIndex((c) => v !== undefined && same(c.value, v));
    return (
      <select id={id} value={at < 0 ? "" : String(at)} onChange={(e) => onEdit(path, e.target.value === "" ? undefined : f.enum![Number(e.target.value)].value)} className={cx(inputClass, ring, "max-w-[320px]")}>
        <option value="">{placeholder(f)}</option>
        {f.enum.map((c, i) => (
          <option key={i} value={String(i)}>
            {choiceLabel(c)}
          </option>
        ))}
      </select>
    );
  }

  if (f.type === "integer" || f.type === "number") {
    const key = path.join(".");
    const text = typed[key]?.text ?? (typeof v === "number" ? String(v) : "");
    const range = [f.minimum !== undefined ? t("settings-extensions.min", { value: f.minimum }) : "", f.maximum !== undefined ? t("settings-extensions.max", { value: f.maximum }) : ""].filter(Boolean).join(t("settings-extensions.range_separator"));
    return (
      <div className="flex items-center gap-2">
        <input
          id={id}
          value={text}
          inputMode={f.type === "integer" ? "numeric" : "decimal"}
          onChange={(e) => onType(path, e.target.value, f.type === "integer")}
          placeholder={placeholder(f)}
          spellCheck={false}
          autoComplete="off"
          className={cx(inputClass, ring, "max-w-[200px] font-mono")}
        />
        {range && <span className="text-[11px] whitespace-nowrap text-ink-3">{range}</span>}
      </div>
    );
  }

  return (
    <input
      id={id}
      value={typeof v === "string" ? v : ""}
      onChange={(e) => onEdit(path, e.target.value === "" ? undefined : e.target.value)}
      type={f.format === "email" ? "email" : f.format === "uri" ? "url" : "text"}
      placeholder={placeholder(f)}
      maxLength={f.max_length}
      spellCheck={false}
      autoComplete="off"
      autoCapitalize="off"
      className={cx(inputClass, ring, f.format && "font-mono")}
    />
  );
}

/** A list of texts (each removable, more added), or of choices (each one pressed in or out). */
/** A list of the hub's machines (format machine): a switch per machine of the systems the field names, by name and how
 *  it is, in effect at once; a value naming no machine still shows, to be turned off. */
function MachineSwitches({ field: f, path, draft, onSaveNow, computers, nodes, id }: Pick<FieldProps, "field" | "path" | "draft" | "onSaveNow" | "computers"> & { nodes?: ExtensionNodeState[]; id: string }) {
  const all = useStore((s) => s.machines);
  const items = listItems(draft, path, f);
  const os = f.items?.os ?? [];
  const machines = all.filter((m) => !os.length || os.includes(m.os ?? ""));
  const unknown = items.filter((x) => !all.some((m) => m.id === x));
  // x-confirm: a machine waiting on it before it is turned on (turning one off is not asked).
  const [asking, setAsking] = useState<{ id: string; name: string } | null>(null);
  const save = (mid: string, on: boolean) => {
    const next = items.filter((x) => x !== mid);
    if (on) next.push(mid);
    onSaveNow(path, next.length ? next : undefined);
  };
  const flip = (mid: string, name: string, on: boolean) => (on && f.confirm ? setAsking({ id: mid, name }) : save(mid, on));
  // The field picking where the plugin's program runs: a machine switched on says how the program is there.
  const status = (m: (typeof machines)[number]) =>
    nodes && items.includes(m.id) ? `${m.online ? t("settings-extensions.online") : t("settings-extensions.offline")} · ${nodeProgramStatus(nodes.find((n) => n.machine_id === m.id))}` : machineStatus(m, computers);
  const rows = [...machines.map((m) => ({ id: m.id, name: m.name, status: status(m) })), ...unknown.map((x) => ({ id: x, name: x, status: t("settings-extensions.machine_not_found") }))];
  if (!rows.length) return <p className="text-[12.5px] text-ink-3">{os.join() === "darwin" ? t("settings-extensions.no_mac") : t("settings-extensions.no_machine")}</p>;
  return (
    <div id={id} role="group" className="flex flex-col divide-y divide-line rounded-control shadow-hairline">
      {rows.map((r) => (
        <div key={r.id} className="flex items-center gap-3 px-3 py-2">
          <div className="min-w-0 flex-1">
            <p className="truncate text-[13px] text-ink">{r.name}</p>
            <p className="truncate text-[11.5px] text-ink-3">{r.status}</p>
          </div>
          <Switch on={items.includes(r.id)} onChange={(on) => flip(r.id, r.name, on)} label={r.name} />
        </div>
      ))}
      <Dialog
        open={!!asking}
        onClose={() => setAsking(null)}
        title={asking ? t("settings-extensions.turn_on_title", { name: asking.name }) : undefined}
        footer={
          <>
            <Button tone="quiet" onClick={() => setAsking(null)}>{t("common.cancel")}</Button>
            <Button tone="primary" onClick={() => { if (asking) save(asking.id, true); setAsking(null); }}>{t("settings-extensions.turn_on")}</Button>
          </>
        }
      >
        <p className="text-[13px] leading-relaxed text-ink-2">{f.confirm}</p>
      </Dialog>
    </div>
  );
}

function ListControl({ field: f, path, draft, onEdit, id, ring }: Pick<FieldProps, "field" | "path" | "draft" | "onEdit"> & { id: string; ring: string }) {
  const [adding, setAdding] = useState("");
  const items = listItems(draft, path, f);
  const store = (list: string[]) => onEdit(path, list.length ? list : undefined);
  const choices = f.items?.enum;

  if (choices?.length) {
    return (
      <div id={id} role="group" className="flex flex-wrap gap-1.5">
        {choices.map((c) => {
          const value = typeof c.value === "string" ? c.value : String(c.value);
          const on = items.includes(value);
          return (
            <button
              key={value}
              type="button"
              aria-pressed={on}
              onClick={() => store(on ? items.filter((x) => x !== value) : [...items, value])}
              className={cx(
                "flex h-7 items-center gap-1.5 whitespace-nowrap rounded-full px-2.5 text-[12px] transition-[background-color,box-shadow,color] duration-150",
                on ? "bg-accent-tint font-medium text-accent-ink shadow-[inset_0_0_0_1px_var(--accent)]" : "text-ink-2 shadow-hairline hover:bg-hover",
              )}
            >
              {on && <Icon name="check" size={12} stroke={2.2} />}
              {choiceLabel(c)}
            </button>
          );
        })}
      </div>
    );
  }

  const add = () => {
    const v = adding.trim();
    if (!v) return;
    store([...items, v]);
    setAdding("");
  };
  return (
    <div className="flex flex-col gap-1.5">
      {items.length > 0 && (
        <ul className="flex flex-wrap gap-1.5">
          {items.map((item, i) => (
            <li key={`${i}-${item}`} className="flex h-7 items-center gap-1 rounded-full bg-inset pr-1 pl-2.5 text-[12px] text-ink shadow-hairline">
              <span className="max-w-[240px] truncate">{item}</span>
              <IconButton icon="x" label={t("settings-extensions.remove_item", { item })} size={20} iconSize={11} onClick={() => store(items.filter((_, j) => j !== i))} />
            </li>
          ))}
        </ul>
      )}
      <div className="flex max-w-[360px] items-center gap-1.5">
        <input
          id={id}
          value={adding}
          onChange={(e) => setAdding(e.target.value)}
          onKeyDown={(e) => {
            if (composing(e.nativeEvent)) return;
            if (e.key === "Enter") {
              e.preventDefault();
              add();
            }
          }}
          placeholder={t("settings-extensions.add_placeholder")}
          spellCheck={false}
          autoComplete="off"
          className={cx(inputClass, ring)}
        />
        <Button tone="secondary" icon="plus" onClick={add} disabled={!adding.trim()}>
          {t("settings-extensions.add")}
        </Button>
      </div>
    </div>
  );
}

// ── a secret ────────────────────────────────────────────

/** Sets one secret: typed in hidden, saved to the hub, never shown again; one that is set can be cleared. */
function SecretDialog({ extension, secret, onClose, onStart, onDone }: { extension: string; secret: ExtensionSecret; onClose: () => void; onStart: () => number; onDone: (n: number, d: HubExtensionDetail) => void }) {
  const [value, setValue] = useState("");
  // shown while typing, to check what was pasted (a token cut short is a common mistake); a saved one is never shown
  const [shown, setShown] = useState(false);
  const [busy, setBusy] = useState<"" | "save" | "clear">("");
  const [error, setError] = useState("");
  const [confirm, setConfirm] = useState(false);
  const input = useRef<HTMLInputElement>(null);
  const errorId = useId();
  // The field is disabled while the hub answers; a refused value stays typed in it, with the focus back to change it.
  useEffect(() => {
    if (!busy && error) input.current?.focus();
  }, [busy, error]);
  const run = async (kind: "save" | "clear") => {
    const v = value.trim();
    if (kind === "save" && !v) return;
    setBusy(kind);
    setError("");
    try {
      const n = onStart();
      const d = kind === "save" ? await setExtensionSecret(extension, secret.name, v) : await clearExtensionSecret(extension, secret.name);
      // a saved value leaves the page: nothing keeps it once the hub has it
      setValue("");
      onDone(n, d);
      toast(t(kind === "save" ? "settings-extensions.secret_saved" : "settings-extensions.secret_cleared", { title: secret.title }), "ok");
      onClose();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy("");
    }
  };
  return (
    <Dialog
      open
      onClose={() => !busy && onClose()}
      width={440}
      title={secret.title}
      footer={
        <>
          {secret.set && (
            <span className="mr-auto">
              {confirm ? (
                <Button tone="danger" busy={busy === "clear"} disabled={!!busy} onClick={() => void run("clear")}>
                  {t("settings-extensions.confirm_clear")}
                </Button>
              ) : (
                <Button tone="quiet" icon="trash" disabled={!!busy} onClick={() => setConfirm(true)} className="text-red enabled:hover:text-red">
                  {t("settings-extensions.clear")}
                </Button>
              )}
            </span>
          )}
          <Button tone="quiet" onClick={onClose} disabled={!!busy}>
            {t("common.cancel")}
          </Button>
          <Button tone="primary" busy={busy === "save"} disabled={!value.trim() || !!busy} onClick={() => void run("save")}>
            {t("common.save")}
          </Button>
        </>
      }
    >
      <form
        className="flex flex-col gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          void run("save");
        }}
      >
        <div className="flex items-center gap-2 text-[12px] text-ink-3">
          <Icon name="lock" size={13} className="shrink-0" />
          {secretState(secret)}
        </div>
        <div className="flex items-center gap-1.5">
          <input
            ref={input}
            type={shown ? "text" : "password"}
            value={value}
            onChange={(e) => (setValue(e.target.value), setError(""))}
            autoFocus
            autoComplete="new-password"
            spellCheck={false}
            placeholder={secret.set ? t("settings-extensions.secret_replace_placeholder") : t("settings-extensions.secret_placeholder")}
            aria-label={secret.title}
            aria-invalid={!!error || undefined}
            aria-describedby={error ? errorId : undefined}
            disabled={!!busy}
            className={cx(inputClass, "h-9 min-w-0 flex-1 font-mono shadow-inset-field")}
          />
          <IconButton
            icon={shown ? "eyeOff" : "eye"}
            label={shown ? t("settings-extensions.secret_hide") : t("settings-extensions.secret_show")}
            onClick={() => setShown((x) => !x)}
            disabled={!!busy}
          />
        </div>
        {error && (
          <p id={errorId} role="alert" className="rounded-[8px] bg-red-tint px-2.5 py-1.5 text-[12px] break-words text-red select-text">
            {t("settings-extensions.not_saved", { error })}
          </p>
        )}
        <p className="text-[11.5px] leading-relaxed text-ink-3">{secret.description || t("settings-extensions.secret_hint")}</p>
        <button type="submit" hidden />
      </form>
    </Dialog>
  );
}
