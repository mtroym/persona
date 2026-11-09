// Settings › Extensions › Extension marketplace (docs/extensions/extensions.md "Extension marketplace"): the catalog of the markets this hub reads (search, a market at a
// time), one extension of it (what it may do; preview → install / upgrade; uninstall when this market installed it), and
// the markets themselves (add with an optional write-only token, remove, read again, change the token). Logic in
// lib/extension-market.ts.
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { t } from "../../i18n";
import { enc, errorText } from "../../lib/api";
import { pinManagerAt } from "../../lib/manager-choice";
import { useRoster } from "../../lib/managers";
import { ago } from "../../lib/format";
import {
  addMarket, agreeLabel, canUninstall, getCatalog, installPreview, isGitMarket, itemLine, itemState, listMarkets, MARKET_SPEC_HINT, MARKET_SPEC_PLACEHOLDER, marketKind, marketLine, previewFromMarket,
  previewVersion, renameConsent, refreshMarket, removeMarket, setMarketToken, specLabel, uninstallExtension, upgradePreview, wantsGitHubToken,
  GITHUB_TOKEN_URL, isGitHubMarket, type Catalog, type CatalogItem, type Market, type MarketPreview,
} from "../../lib/extension-market";
import { extensionDetail, extensionUpgrades, pointText, setExtensionUpgrades, type ExtensionPermission } from "../../lib/extensions";
import { Button, cx, Dialog, EASE, IconButton, Loading, Spinner, toast, useNow } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { dependentError, dependencyText, previewBlocked, type Dependent, type PackMember } from "../../lib/extension-model";
import { Group, Pill, Row, settingsInputClass as inputClass, Switch } from "./SettingsParts";
import { agreeTo, cancelOperation, forget, getOperation, isActive, operationsOn, retryOperation, startFromMarket, wantsSignIn } from "../../lib/extension-operations";
import { askSignIn } from "../../lib/extension-auth";
import { useStore } from "../../lib/store";
import { OperationSteps, useOperation } from "./ExtensionOperation";

function Back({ label, onBack, right }: { label: string; onBack: () => void; right?: React.ReactNode }) {
  return (
    <div className="mb-3 flex items-center gap-2">
      <Button tone="quiet" icon="chevronLeft" onClick={onBack} className="-ml-2">
        {label}
      </Button>
      <span className="flex-1" />
      {right}
    </div>
  );
}

function Problem({ text, onRetry, busy }: { text: string; onRetry: () => void; busy?: boolean }) {
  return (
    <div className="flex items-center gap-3 px-4 py-3">
      <p className="min-w-0 flex-1 text-[12.5px] break-words text-red">{text}</p>
      <Button tone="secondary" icon="retry" onClick={onRetry} busy={busy}>
        {t("settings-extensionmarket.retry")}
      </Button>
    </div>
  );
}

const STATE_TONE = { accent: "accent", green: "green", orange: "orange", red: "red", gray: undefined } as const;

/** Under a GitHub market's read error: why a token helps, and the way to add it (the markets' own dialog). */
function TokenHint({ onFix }: { onFix: () => void }) {
  return (
    <div className="mt-1.5 flex flex-wrap items-center gap-2">
      <p className="min-w-[200px] flex-1 text-[11.5px] leading-relaxed text-ink-3">{t("settings-extensionmarket.token_rate_limit_hint")}</p>
      <Button tone="secondary" icon="key" onClick={onFix} className="shrink-0">
        {t("settings-extensionmarket.set_token")}
      </Button>
    </div>
  );
}

// ── the catalog ─────────────────────────────────────────

export function MarketCatalog({ onBack, onOpen, onMarkets }: { onBack: () => void; onOpen: (it: CatalogItem) => void; onMarkets: () => void }) {
  const [q, setQ] = useState("");
  const [market, setMarket] = useState("");
  const [data, setData] = useState<Catalog | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [tokenFor, setTokenFor] = useState<Market | null>(null);
  const seq = useRef(0);
  const load = (refresh = false) => {
    const n = ++seq.current;
    setLoading(true);
    getCatalog(q, market, refresh)
      .then((c) => n === seq.current && (setData(c), setError("")))
      .catch((e) => n === seq.current && setError(errorText(e)))
      .finally(() => n === seq.current && setLoading(false));
  };
  // typing waits a moment; picking a market reads at once
  useEffect(() => {
    const timer = setTimeout(() => load(), q ? 250 : 0);
    return () => clearTimeout(timer);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [q, market]);

  const markets = data?.markets ?? [];
  const broken = markets.filter((m) => m.error);
  // nothing listed because the market(s) shown could not be read: not "This marketplace has no extensions yet" under "can't read right now" (#4480)
  // with several markets and none picked there is no single "this marketplace": say what was read is empty (some may be unreadable)
  const unread = market ? !!markets.find((m) => m.id === market)?.error : markets.length > 0 && broken.length === markets.length;
  return (
    <>
      <Back
        label={t("settings-extensionmarket.extensions")}
        onBack={onBack}
        right={
          <>
            <IconButton icon="retry" label={t("settings-extensionmarket.reload_all")} onClick={() => load(true)} disabled={loading} />
            <Button tone="secondary" icon="gear" onClick={onMarkets}>
              {t("settings-extensionmarket.manage_markets")}
            </Button>
          </>
        }
      />
      <h2 className="mb-1 px-1 text-[15px] font-semibold text-ink">{t("settings-extensionmarket.market_title")}</h2>
      <p className="mb-4 px-1 text-[12.5px] leading-relaxed text-ink-3">{t("settings-extensionmarket.market_intro")}</p>

      <div className="mb-3 flex items-center gap-2 rounded-control bg-surface px-2.5 shadow-card">
        <Icon name="search" size={14} className="shrink-0 text-ink-3" />
        <input
          value={q}
          onChange={(e) => setQ(e.target.value)}
          placeholder={t("settings-extensionmarket.search_placeholder")}
          aria-label={t("settings-extensionmarket.search_label")}
          spellCheck={false}
          className="h-9 min-w-0 flex-1 bg-transparent text-[13px] text-ink outline-none placeholder:text-ink-3"
        />
        {loading && data && <Spinner />}
        {q && <IconButton icon="x" label={t("settings-extensionmarket.clear_search")} onClick={() => setQ("")} size={24} iconSize={12} />}
      </div>
      {markets.length > 1 && (
        <div role="radiogroup" aria-label={t("settings-extensionmarket.filter_market")} className="mb-4 flex flex-wrap gap-1.5">
          {[{ id: "", name: t("settings-extensionmarket.all") }, ...markets].map((m) => {
            const on = market === m.id;
            return (
              <button
                key={m.id || "all"}
                type="button"
                role="radio"
                aria-checked={on}
                onClick={() => setMarket(m.id)}
                className={cx(
                  "flex h-7 items-center whitespace-nowrap rounded-full px-3 text-[12px] transition-[background-color,box-shadow,color] duration-150",
                  on ? "bg-accent-tint font-medium text-accent-ink shadow-[inset_0_0_0_1px_var(--accent)]" : "text-ink-2 shadow-hairline hover:bg-hover",
                )}
              >
                {m.name}
              </button>
            );
          })}
        </div>
      )}
      {broken.map((m) =>
        // the official market out of reach is the network's, grey not red and no token asked of a newcomer here
        // (docs/assistant/onboarding.md 4.5); the hub reads it elsewhere when GitHub rate-limits it, and for whoever still wants a
        // token a quiet way to the markets page, where it is set
        m.official ? (
          <div key={m.id} className="mb-3">
            <p title={m.error} className="flex items-start gap-2 px-1 text-[12px] leading-relaxed text-ink-3">
              <Icon name="info" size={13} className="mt-0.5 shrink-0" />
              {t("settings-extensionmarket.official_unreachable")}
            </p>
            {wantsGitHubToken(m) && (
              <button type="button" onClick={onMarkets} className="mt-1 block max-w-full truncate pr-1 pl-[25px] text-[11.5px] text-ink-3 underline underline-offset-2 hover:text-ink-2">
                {t("settings-extensionmarket.official_token_link")}
              </button>
            )}
          </div>
        ) : (
          <div key={m.id} className="mb-3">
            <p className="flex items-start gap-2 rounded-[8px] bg-red-tint px-3 py-2 text-[12px] leading-relaxed break-words text-red">
              <Icon name="info" size={13} className="mt-0.5 shrink-0" />
              {t("settings-extensionmarket.market_unreadable", { name: m.name, error: m.error ?? "" })}
            </p>
            {wantsGitHubToken(m) && <TokenHint onFix={() => setTokenFor(m)} />}
          </div>
        ),
      )}

      <Group title={data ? t("settings-extensionmarket.extension_count", { count: data.extensions.length }) : t("settings-extensionmarket.extensions")}>
        {!data && !error && (
          <div className="px-4 py-3">
            <Loading />
          </div>
        )}
        {error && <Problem text={t("settings-extensionmarket.catalog_failed", { error })} onRetry={() => load()} busy={loading} />}
        {data && markets.length === 0 && (
          <div className="flex flex-col items-center gap-2 px-6 py-8 text-center">
            <span className="flex size-10 items-center justify-center rounded-[10px] bg-inset text-ink-3 shadow-hairline">
              <Icon name="extension" size={18} />
            </span>
            <p className="text-[13px] font-medium text-ink">{t("settings-extensionmarket.no_markets")}</p>
            <p className="max-w-[420px] text-[12px] leading-relaxed text-ink-3">{t("settings-extensionmarket.no_markets_body")}</p>
            <Button tone="primary" icon="plus" onClick={onMarkets}>
              {t("settings-extensionmarket.add_market")}
            </Button>
          </div>
        )}
        {data && markets.length > 0 && data.extensions.length === 0 && (
          <p className="px-4 py-6 text-center text-[12.5px] text-ink-3">{q ? t("settings-extensionmarket.not_found", { query: q.trim() }) : unread ? t("settings-extensionmarket.market_unread") : !market && markets.length > 1 ? t("settings-extensionmarket.markets_read_empty") : t("settings-extensionmarket.market_empty")}</p>
        )}
        {data?.extensions.map((it, i) => {
          const st = itemState(it);
          return (
            <button
              key={`${it.market}/${it.id}`}
              type="button"
              onClick={() => onOpen(it)}
              className="flex w-full items-center gap-3 px-4 py-3 text-left transition-colors hover:bg-hover"
              style={{ animation: `fade-up 320ms ${EASE} ${Math.min(i, 8) * 30}ms both` }}
            >
              <span className={cx("flex size-9 shrink-0 items-center justify-center rounded-[9px]", it.installed ? "bg-accent-tint text-accent-ink" : "bg-inset text-ink-3 shadow-hairline")}>
                <Icon name="extension" size={16} />
              </span>
              <div className="min-w-0 flex-1">
                <div className="flex min-w-0 items-center gap-2">
                  <span className="truncate text-[13px] font-medium text-ink">{it.name}</span>
                  <span className="shrink-0 font-mono text-[11px] text-ink-3">{it.version}</span>
                </div>
                {it.description && <div className="mt-0.5 truncate text-[11.5px] text-ink-3">{it.description}</div>}
                <div className="mt-1 flex min-w-0 flex-wrap items-center gap-1">
                  <span className="mr-1 truncate text-[11px] text-ink-3">{it.market_name}</span>
                  {(it.tags ?? []).map((tag) => (
                    <Pill key={tag}>{tag}</Pill>
                  ))}
                </div>
              </div>
              <span className="flex shrink-0 flex-col items-end gap-1">
                <Pill tone={STATE_TONE[st.tone]}>{st.label}</Pill>
                {it.problem && <span className="max-w-[180px] truncate text-[11px] text-orange">{it.problem}</span>}
              </span>
              <Icon name="chevronRight" size={12} stroke={2} className="shrink-0 text-ink-3" />
            </button>
          );
        })}
      </Group>
      {tokenFor && (
        <TokenDialog
          market={tokenFor}
          onClose={() => setTokenFor(null)}
          onDone={() => {
            setTokenFor(null);
            load();
          }}
        />
      )}
    </>
  );
}

// ── one extension of a market ──────────────────────────────

/** `manager`: whose copy it is for (an upgrade from another manager's extension page, docs/assistant/managers.md "Clients"): its
 *  catalog, preview, consent and operation are that manager's, named on each request, and the page says so; none: the
 *  default's. */
export function MarketItem({ market, id, manager, onBack, onInstalled }: { market: string; id: string; manager?: string; onBack: () => void; onInstalled: (id: string) => void }) {
  // what goes to the extension itself (its details, its upgrade, uninstalling it) names the manager while this is open
  const managerName = useRoster()?.managers.find((m) => m.id === manager)?.name ?? manager;
  const [it, setIt] = useState<CatalogItem | null>(null);
  useLayoutEffect(() => pinManagerAt([id, it?.id, it?.rename_from].filter((key): key is string => !!key)
    .flatMap((key) => [`/extensions/${enc(key)}`, `/plugins/${enc(key)}`]), manager), [id, it?.id, it?.rename_from, manager]);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [preview, setPreview] = useState<MarketPreview | null>(null);
  const [previewing, setPreviewing] = useState(false);
  const [removing, setRemoving] = useState(false);
  // A hub with extension_operations installs in steps the page draws (and finds again when opened later, #3277).
  const stepped = useStore((s) => operationsOn(s.features));
  const op = useOperation(it?.id || id, market, manager);
  const [previewOf, setPreviewOf] = useState(""); // the operation whose preview is open
  const seen = useRef<string>("");
  const load = () => {
    setLoading(true);
    getCatalog("", market, false, manager)
      .then((c) => {
        const found = c.extensions.find((x) => x.id === id) ?? c.extensions.find((x) => x.replaces === id) ?? null;
        setIt(found);
        setError(found ? "" : t("settings-extensionmarket.gone"));
      })
      .catch((e) => setError(errorText(e)))
      .finally(() => setLoading(false));
  };
  useEffect(load, [market, id, manager]);

  // An operation of this page's extension: when it waits for the user, its preview opens; when it is done, the page
  // moves on as after a synchronous install.
  useEffect(() => {
    if (!op) return;
    const key = `${op.id}:${op.state}`;
    const was = seen.current;
    seen.current = key;
    if (op.state === "needs_consent" && previewOf !== op.id && was !== key) {
      getOperation(op.id, manager)
        .then((full) => {
          if (full.preview) {
            setPreview(full.preview);
            setPreviewOf(op.id);
          }
        })
        .catch((e) => toast(t("settings-extensionmarket.preview_failed", { error: errorText(e) }), "error"));
    }
    if (op.state === "succeeded" && was.startsWith(`${op.id}:`) && was !== key) {
      if (op.kind === "uninstall") load();
      else {
        toast(op.kind === "upgrade" ? t("settings-extensionmarket.upgraded", { name: op.name ?? op.extension, version: op.version ?? "" }) : t("settings-extensionmarket.installed", { name: op.name ?? op.extension }), "ok");
        if (wantsSignIn(op)) askSignIn(op.extension); // its detail opens its sign-in
        onInstalled(op.extension);
      }
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [op?.id, op?.state]);

  const look = async () => {
    setPreviewing(true);
    try {
      if (stepped) {
        await startFromMarket(market, it?.id || id, manager); // the steps show below; its preview opens when it is ready
        return;
      }
      setPreview(await previewFromMarket(market, it?.id || id, manager));
    } catch (e) {
      toast(t("settings-extensionmarket.preview_failed", { error: errorText(e) }), "error");
    } finally {
      setPreviewing(false);
    }
  };
  const st = it ? itemState(it) : null;
  const contributes = Object.entries(it?.contributes ?? {});
  let home = "";
  try {
    home = it?.homepage && new URL(it.homepage).protocol === "https:" ? it.homepage : "";
  } catch {
    home = "";
  }

  return (
    <>
      <Back label={t("settings-extensionmarket.market_title")} onBack={onBack} right={<IconButton icon="retry" label={t("settings-extensionmarket.reload")} onClick={load} disabled={loading} />} />
      <section className="mb-6 overflow-hidden rounded-card bg-surface shadow-card">
        <div className="flex items-center gap-3 px-4 py-3.5">
          <span className={cx("flex size-11 shrink-0 items-center justify-center rounded-[11px]", it?.installed ? "bg-accent-tint text-accent-ink" : "bg-inset text-ink-3 shadow-hairline")}>
            <Icon name="extension" size={20} />
          </span>
          <div className="min-w-0 flex-1">
            <h2 className="truncate text-[15px] font-semibold text-ink">{it?.name ?? id}</h2>
            <div className="mt-0.5 truncate text-[12px] text-ink-3">{it ? itemLine(it) : loading ? t("settings-extensionmarket.reading") : ""}</div>
          </div>
          {loading && !it && <Spinner />}
          {st && <Pill tone={STATE_TONE[st.tone]}>{st.label}</Pill>}
        </div>
        {(it?.description || it?.tags?.length || it?.problem || error || managerName) && (
          <div className="flex flex-col gap-2 border-t border-line px-4 py-3">
            {it?.description && <p className="text-[12.5px] leading-relaxed text-ink-2">{it.description}</p>}
            {!!it?.tags?.length && (
              <div className="flex flex-wrap gap-1">
                {it.tags.map((tag) => (
                  <Pill key={tag}>{tag}</Pill>
                ))}
              </div>
            )}
            {it?.problem && <p className="text-[12px] text-orange">{t("settings-extensionmarket.cant_install", { problem: it.problem })}</p>}
            {managerName && <p className="text-[12px] text-ink-2" data-for-manager>{t("settings-extensionmarket.for_manager", { name: managerName })}</p>}
            {error && <p className="text-[12px] break-words text-red">{error}</p>}
          </div>
        )}
        {it && st && (st.kind === "install" || st.kind === "upgrade" || canUninstall(it)) && (
          <div className="flex flex-wrap items-center gap-2 border-t border-line bg-inset px-4 py-2.5">
            {canUninstall(it) && (
              <Button tone="quiet" icon="trash" onClick={() => setRemoving(true)} className="text-red enabled:hover:text-red">
                {t("settings-extensionmarket.uninstall")}
              </Button>
            )}
            <span className="flex-1" />
            {(st.kind === "install" || st.kind === "upgrade") && (
              <Button tone="primary" icon={st.kind === "upgrade" ? "up" : "download"} busy={previewing || (stepped && isActive(op))} onClick={() => void look()}>
                {st.kind === "upgrade" ? t("settings-extensionmarket.upgrade_to", { version: it.version }) : t("settings-extensionmarket.install")}
              </Button>
            )}
          </div>
        )}
        {stepped && op && op.state !== "succeeded" && op.state !== "canceled" && (
          <OperationSteps
            op={op}
            onRetry={async () => {
              try {
                await retryOperation(op.id, manager);
              } catch (e) {
                toast(errorText(e), "error");
              }
            }}
            onCancel={op.state === "running" && op.steps.some((s) => s.id === "consent" && s.state === "pending") ? () => cancelOperation(op.id, manager).then(() => undefined, (e) => toast(errorText(e), "error")) : undefined}
            onReview={() => {
              seen.current = "";
              setPreviewOf("");
              getOperation(op.id, manager).then((full) => {
                if (full.preview) {
                  setPreview(full.preview);
                  setPreviewOf(op.id);
                }
              }, (e) => toast(errorText(e), "error"));
            }}
            onDismiss={() => forget(op)}
          />
        )}
      </section>

      {it && (
        <>
          <Group title={t("settings-extensionmarket.can_title")} footer={t("settings-extensionmarket.can_footer")}>
            {(it.permissions ?? []).length ? (
              (it.permissions ?? []).map((p) => <PermissionRow key={p.key + p.title} p={p} />)
            ) : (
              <p className="px-4 py-2.5 text-[12.5px] text-ink-2">{t("settings-extensionmarket.no_permissions")}</p>
            )}
          </Group>
          {contributes.length > 0 && (
            <Group title={t("settings-extensionmarket.provides")}>
              {contributes.map(([point, ids]) => (
                <Row key={point} label={pointText(point)} mono>
                  <span className="truncate">{ids.join(t("settings-extensionmarket.list_separator"))}</span>
                </Row>
              ))}
            </Group>
          )}
          <Group title={t("settings-extensionmarket.about")}>
            <Row label={t("settings-extensionmarket.version")} mono>
              {it.version}
            </Row>
            {it.installed && (
              <Row label={t("settings-extensionmarket.installed_version")} mono>
                {it.installed}
              </Row>
            )}
            {it.publisher && <Row label={t("settings-extensionmarket.publisher")}>{it.publisher}</Row>}
            <Row label={t("settings-extensionmarket.market")}>{it.market_name}</Row>
            <Row label="id" mono>
              {it.id}
            </Row>
            {home && (
              <a href={home} target="_blank" rel="noreferrer noopener" className="flex items-center gap-3 px-4 py-2.5 whitespace-nowrap transition-colors hover:bg-hover">
                <span className="min-w-0 flex-1 truncate text-[13px] text-ink">{t("settings-extensionmarket.homepage")}</span>
                <span className="min-w-0 truncate text-[12.5px] text-ink-3">{new URL(home).host}</span>
                <Icon name="external" size={12} className="shrink-0 text-ink-3" />
              </a>
            )}
          </Group>
        </>
      )}

      {preview && it && (
        <PreviewDialog
          pv={preview}
          managerName={managerName}
          onClose={() => {
            setPreview(null);
            if (previewOf) void cancelOperation(previewOf, manager).catch(() => undefined); // turned down: the hub drops the preview
            setPreviewOf("");
          }}
          onAgree={async () => {
            if (previewOf) {
              await agreeTo(preview, preview.upgrade ? it.id : undefined, manager); // installs in the background, in steps
              setPreview(null);
              setPreviewOf("");
              return;
            }
            const d = preview.upgrade ? await upgradePreview(it.id, preview, manager) : await installPreview(preview, manager);
            toast(preview.upgrade ? t("settings-extensionmarket.upgraded", { name: d.name, version: d.version ?? "" }) : t("settings-extensionmarket.installed", { name: d.name }), "ok");
            setPreview(null);
            onInstalled(d.id);
          }}
        />
      )}
      {removing && it && (
        <UninstallDialog
          name={it.name}
          id={it.rename_from || it.id}
          onClose={() => setRemoving(false)}
          onDone={() => {
            setRemoving(false);
            load();
          }}
        />
      )}
    </>
  );
}

export function PermissionRow({ p, tone }: { p: ExtensionPermission; tone?: "orange" }) {
  return (
    <div className="flex items-start gap-2.5 px-4 py-2.5">
      <Icon name={permissionIcon(p.key)} size={14} className={cx("mt-0.5 shrink-0", tone === "orange" ? "text-orange" : "text-ink-3")} />
      <div className="min-w-0">
        <div className="text-[13px] text-ink">{p.title}</div>
        {p.detail && <div className="mt-0.5 text-[11.5px] leading-snug break-words text-ink-3">{p.detail}</div>}
      </div>
    </div>
  );
}

/** The preview of what installing (or upgrading) does; agreeing installs exactly this (its permissions digest). */
/** `managerName`: the manager whose copy it installs or upgrades, when another than the default's (said on the
 *  consent, so it is agreed to for that one). */
export function PreviewDialog({ pv, managerName, onClose, onAgree }: { pv: MarketPreview; managerName?: string; onClose: () => void; onAgree: () => Promise<void> }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const now = useNow(1000);
  const left = Math.max(0, Date.parse(pv.expires_at) - now);
  const expired = previewBlocked({ ...pv, unresolved: [] }, now);
  const agree = async () => {
    setBusy(true);
    setError("");
    try {
      await onAgree();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };
  const m = pv.manifest;
  return (
    <Dialog
      open
      onClose={() => !busy && onClose()}
      width={520}
      title={pv.upgrade ? t("settings-extensionmarket.upgrade_title", { name: m.name }) : t("settings-extensionmarket.install_title", { name: m.name })}
      footer={
        <>
          <Button tone="quiet" onClick={onClose} disabled={busy}>
            {t("common.cancel")}
          </Button>
          <Button tone="primary" busy={busy} disabled={previewBlocked(pv, now)} onClick={() => void agree()}>
            {agreeLabel(pv)}
          </Button>
        </>
      }
    >
      <div className="flex max-h-[60vh] flex-col gap-3 overflow-y-auto">
        <div className="flex items-start gap-3">
          <span className="flex size-10 shrink-0 items-center justify-center rounded-[11px] bg-accent-tint text-accent-ink">
            <Icon name="extension" size={18} />
          </span>
          <div className="min-w-0 flex-1">
            <div className="flex flex-wrap items-baseline gap-x-2">
              <span className="text-[14px] font-semibold text-ink">{m.name}</span>
              <span className="font-mono text-[12px] text-ink-2">{previewVersion(pv)}</span>
            </div>
            <div className="mt-0.5 text-[11.5px] text-ink-3">
              <span className="font-mono">{m.id}</span>
              {m.publisher && <> · {m.publisher}</>}
            </div>
            {m.description && <p className="mt-1.5 text-[12.5px] leading-relaxed text-ink-2">{m.description}</p>}
          </div>
        </div>
        {managerName && <p className="rounded-[8px] bg-inset px-3 py-2 text-[12px] leading-relaxed text-ink-2 shadow-hairline" data-for-manager>{t("settings-extensionmarket.for_manager", { name: managerName })}</p>}
        {pv.rename_from && (
          <Group title={t("lib-extension-market.rename_title")} footer={renameConsent(pv)}>
            <Row label={t("lib-extension-market.rename_from")} mono>{pv.rename_from}</Row>
            <Row label={t("lib-extension-market.rename_to")} mono>{m.id}</Row>
          </Group>
        )}
        {pv.upgrade && pv.more_permissions && (
          <p className="rounded-[8px] bg-red-tint px-3 py-2 text-[12px] leading-relaxed text-red">{t("settings-extensionmarket.more_permissions")}</p>
        )}
        {pv.upgrade && !!pv.added_permissions?.length && (
          <div>
            <div className="mb-1 px-1 text-[11.5px] font-medium text-orange">{t("settings-extensionmarket.added")}</div>
            <div className="divide-y divide-line overflow-hidden rounded-control bg-orange-tint shadow-hairline">
              {pv.added_permissions.map((p) => (
                <PermissionRow key={p.key + p.title} p={p} tone="orange" />
              ))}
            </div>
          </div>
        )}
        <div>
          <div className="mb-1 px-1 text-[11.5px] font-medium text-ink-3">{pv.upgrade ? t("settings-extensionmarket.new_version_can") : t("settings-extensionmarket.can_title")}</div>
          <div className="divide-y divide-line overflow-hidden rounded-control bg-inset shadow-hairline">
            {pv.permissions.length ? pv.permissions.map((p) => <PermissionRow key={p.key + p.title} p={p} />) : <p className="px-4 py-2.5 text-[12.5px] text-ink-2">{t("settings-extensionmarket.no_permissions")}</p>}
          </div>
        </div>
        {!!pv.depends?.length && <Group title={t("settings-extensionmarket.depends")}>{pv.depends.map((d) => <Row key={d.id} label={d.name || d.id} hint={d.version}>{dependencyText(d)}</Row>)}</Group>}
        {!!m.extension_pack?.length && <Group title={t("settings-extensionmarket.pack_members")}>{m.extension_pack.map((p) => <Row key={p.id} label={p.id} hint={p.market}>{p.version || ""}</Row>)}</Group>}
        {!!pv.bundle?.length && (
          <Group title={t("settings-extensionmarket.bundle")} footer={t("settings-extensionmarket.bundle_footer")}>
            {pv.bundle.map((b) => (
              <div key={b.preview_id}>
                <Row label={b.manifest.name} hint={b.manifest.id} mono>{previewVersion(b)}</Row>
                {b.rename_from && <Group title={t("lib-extension-market.rename_title")} footer={renameConsent(b)}>
                  <Row label={t("lib-extension-market.rename_from")} mono>{b.rename_from}</Row>
                  <Row label={t("lib-extension-market.rename_to")} mono>{b.manifest.id}</Row>
                </Group>}
                {b.permissions.length ? b.permissions.map((p) => <PermissionRow key={p.key + p.title} p={p} />) : <p className="px-4 py-2.5 text-ink-3">{t("settings-extensionmarket.bundle_no_permissions")}</p>}
                {b.warnings?.map((w) => <p key={w} className="px-4 py-2.5 text-orange">{w}</p>)}
              </div>
            ))}
          </Group>
        )}
        {!!pv.unresolved?.length && <p role="alert" className="text-red">{t("settings-extensionmarket.unresolved", { items: pv.unresolved.join(t("settings-extensionmarket.list_separator")) })}</p>}
        {pv.warnings?.map((w) => (
          <p key={w} className="flex items-start gap-1.5 text-[12px] leading-relaxed text-orange">
            <Icon name="info" size={13} className="mt-0.5 shrink-0" />
            {w}
          </p>
        ))}
        <p className="text-[11.5px] leading-relaxed text-ink-3">
          {expired ? t("settings-extensionmarket.preview_expired") : t("settings-extensionmarket.preview_valid", { count: Math.ceil(left / 60000) })}
        </p>
        {error && <p className="rounded-[8px] bg-red-tint px-3 py-2 text-[12px] break-words text-red">{t("settings-extensionmarket.not_done", { error })}</p>}
      </div>
    </Dialog>
  );
}

/** Uninstalling an extension a market installed; its data can stay for a later install. */
export function UninstallDialog({ id, name, version, pack, onClose, onDone }: { id: string; name: string; version?: string; pack?: PackMember[]; onClose: () => void; onDone: () => void }) {
  const [members, setMembers] = useState(false);
  const [packMembers, setPackMembers] = useState(pack);
  useEffect(() => { if (!pack) void extensionDetail(id).then((d) => setPackMembers(d.extension_pack), () => {}); }, [id, pack]);
  const [dependents, setDependents] = useState<Dependent[] | null>(null);
  const [cascade, setCascade] = useState(false);
  const [keep, setKeep] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [warnings, setWarnings] = useState<string[] | null>(null);
  const go = async () => {
    if (busy || warnings) return;
    setBusy(true);
    setError("");
    try {
      const result = await uninstallExtension(id, keep, cascade, members);
      if (result.warnings?.length) setWarnings(result.warnings);
      else {
        toast(t("settings-extensionmarket.uninstalled", { name }), "ok");
        onDone();
      }
    } catch (e) {
      const deps = dependentError(e);
      if (deps) { setDependents(deps); setCascade(false); }
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open
      onClose={() => !busy && (warnings ? onDone() : onClose())}
      width={440}
      title={t(warnings ? "settings-extensionmarket.uninstalled" : "settings-extensionmarket.uninstall_title", { name })}
      footer={
        warnings ? <Button tone="primary" onClick={onDone}>{t("ui-atoms.close")}</Button> : <>
          <Button tone="quiet" onClick={onClose} disabled={busy}>
            {t("common.cancel")}
          </Button>
          <Button tone="danger" busy={busy} disabled={!!dependents && !cascade} onClick={() => void go()}>
            {t("settings-extensionmarket.uninstall")}
          </Button>
        </>
      }
    >
      {warnings ? <div role="status">
        <p className="text-[12.5px] leading-relaxed text-ink-2">{t("settings-extensionmarket.uninstalled_warnings")}</p>
        <ul className="mt-3 space-y-2">
          {warnings.map((warning, index) => <li key={index} className="rounded-control bg-orange-tint px-3 py-2 text-[12.5px] leading-relaxed whitespace-pre-wrap break-all text-orange">{warning}</li>)}
        </ul>
      </div> : <>
      {version && <Row label={t("settings-extensions.version")} mono wrapValue>{version}</Row>}
      <p className="text-[12.5px] leading-relaxed text-ink-2">{t("settings-extensionmarket.uninstall_body")}</p>
      <label className="mt-3 flex cursor-pointer items-start gap-2.5 rounded-control bg-inset px-3 py-2.5 shadow-hairline">
        <input type="checkbox" checked={keep} onChange={(e) => setKeep(e.target.checked)} className="mt-0.5 size-3.5 shrink-0 accent-[var(--accent)]" />
        <span className="min-w-0">
          <span className="block text-[12.5px] font-medium text-ink">{t("settings-extensionmarket.keep_data")}</span>
          <span className="mt-0.5 block text-[11.5px] leading-relaxed text-ink-3">{t("settings-extensionmarket.keep_data_hint")}</span>
        </span>
      </label>
      {dependents && <label className="mt-3 flex items-start gap-2.5 text-ink-2"><input type="checkbox" checked={cascade} onChange={(e) => setCascade(e.target.checked)} />{t("settings-extensionmarket.disable_dependents", { names: dependents.map((d) => d.name).join(t("settings-extensionmarket.list_separator")) })}</label>}
      {!!packMembers?.length && <label className="mt-3 flex items-start gap-2.5 text-ink-2"><input type="checkbox" checked={members} onChange={(e) => { setMembers(e.target.checked); setCascade(false); setDependents(null); }} />{t("settings-extensionmarket.uninstall_members", { names: packMembers.map((p) => p.id).join(t("settings-extensionmarket.list_separator")) })}</label>}
      {error && <p className="mt-2 rounded-[8px] bg-red-tint px-3 py-2 text-[12px] break-words text-red">{t("settings-extensionmarket.not_uninstalled", { error })}</p>}
      </>}
    </Dialog>
  );
}

// ── the markets ─────────────────────────────────────────

export function Markets({ onBack }: { onBack: () => void }) {
  const [list, setList] = useState<Market[] | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [adding, setAdding] = useState(false);
  const [tokenFor, setTokenFor] = useState<Market | null>(null);
  const [busy, setBusy] = useState<Record<string, "refresh" | "remove">>({});
  const [confirm, setConfirm] = useState("");
  // The switch for all market extensions (docs/extensions/extensions.md "Auto-upgrade"); null: a hub without it.
  const [auto, setAuto] = useState<boolean | null>(null);
  const [savingAuto, setSavingAuto] = useState(false);
  useEffect(() => void extensionUpgrades().then(setAuto), []);
  const flipAuto = async (on: boolean) => {
    setSavingAuto(true);
    try {
      setAuto(await setExtensionUpgrades(on));
      toast(on ? t("settings-extensionmarket.auto_on") : t("settings-extensionmarket.auto_off"), "ok");
    } catch (e) {
      toast(t("settings-extensionmarket.auto_failed", { error: errorText(e) }), "error");
    } finally {
      setSavingAuto(false);
    }
  };
  const load = () => {
    setLoading(true);
    listMarkets()
      .then((l) => (setList(l), setError("")))
      .catch((e) => setError(errorText(e)))
      .finally(() => setLoading(false));
  };
  useEffect(load, []);
  const put = (m: Market) => setList((l) => (l ?? []).map((x) => (x.id === m.id ? m : x)));
  const work = async (m: Market, kind: "refresh" | "remove") => {
    setBusy((b) => ({ ...b, [m.id]: kind }));
    try {
      if (kind === "refresh") {
        put(await refreshMarket(m.id));
        toast(t("settings-extensionmarket.reloaded", { name: m.name }), "ok");
      } else {
        await removeMarket(m.id);
        setList((l) => (l ?? []).filter((x) => x.id !== m.id));
        toast(t("settings-extensionmarket.market_removed", { name: m.name }), "ok");
      }
    } catch (e) {
      toast(t(kind === "refresh" ? "settings-extensionmarket.refresh_failed" : "settings-extensionmarket.remove_failed", { name: m.name, error: errorText(e) }), "error");
      if (kind === "refresh") load();
    } finally {
      setBusy(({ [m.id]: _, ...rest }) => rest);
      setConfirm("");
    }
  };

  return (
    <>
      <Back
        label={t("settings-extensionmarket.market_title")}
        onBack={onBack}
        right={
          <Button tone="primary" icon="plus" onClick={() => setAdding(true)}>
            {t("settings-extensionmarket.add_market")}
          </Button>
        }
      />
      <h2 className="mb-1 px-1 text-[15px] font-semibold text-ink">{t("settings-extensionmarket.manage_markets")}</h2>
      <p className="mb-4 px-1 text-[12.5px] leading-relaxed text-ink-3">
        {t("settings-extensionmarket.markets_intro")}
      </p>
      {auto !== null && !!list?.length && (
        <Group
          footer={
            auto
              ? t("settings-extensionmarket.auto_footer_on")
              : t("settings-extensionmarket.auto_footer_off")
          }
        >
          <div className="flex items-center gap-3 px-4 py-2.5">
            <span className="min-w-0 flex-1 truncate text-[13px] text-ink">{t("settings-extensionmarket.auto_upgrade")}</span>
            {savingAuto && <Spinner />}
            <Switch on={auto} onChange={(on) => void flipAuto(on)} disabled={savingAuto} label={t("settings-extensionmarket.auto_upgrade")} />
          </div>
        </Group>
      )}
      <Group title={list ? t("settings-extensionmarket.market_count", { count: list.length }) : t("settings-extensionmarket.markets")} right={<IconButton icon="retry" label={t("settings-extensionmarket.refresh")} onClick={load} disabled={loading} size={24} iconSize={13} className="-mb-1" />}>
        {!list && !error && (
          <div className="px-4 py-3">
            <Loading />
          </div>
        )}
        {error && <Problem text={t("settings-extensionmarket.markets_failed", { error })} onRetry={load} busy={loading} />}
        {list?.length === 0 && <p className="px-4 py-6 text-center text-[12.5px] text-ink-3">{t("settings-extensionmarket.markets_empty")}</p>}
        {list?.map((m) => (
          <div key={m.id} className="px-4 py-3">
            <div className="flex items-start gap-3">
              <span className={cx("flex size-9 shrink-0 items-center justify-center rounded-[9px]", m.error && !m.official ? "bg-red-tint text-red" : "bg-inset text-ink-3 shadow-hairline")}>
                <Icon name={marketKind(m.spec) === "web" ? "web" : "git"} size={16} />
              </span>
              <div className="min-w-0 flex-1">
                <div className="flex min-w-0 items-center gap-2">
                  <span className="truncate text-[13px] font-medium text-ink">{m.name}</span>
                  {m.official && <Pill tone="accent">{t("settings-extensionmarket.official")}</Pill>}
                  {m.has_token && (
                    <Pill>
                      <Icon name="lock" size={10} className="mr-0.5" />
                      token
                    </Pill>
                  )}
                </div>
                <div className="mt-0.5 truncate font-mono text-[11px] text-ink-3" title={m.spec}>
                  {specLabel(m.spec)}
                </div>
                <div className="mt-0.5 truncate text-[11.5px] text-ink-3">{marketLine(m, (iso) => ago(iso))}</div>
                {m.description && <p className="mt-1 text-[12px] leading-relaxed text-ink-2">{m.description}</p>}
                {m.error && (m.official ? (
                  <p title={m.error} className="mt-1 text-[12px] leading-relaxed text-ink-3">{t("settings-extensionmarket.official_unreachable")}</p>
                ) : (
                  <p className="mt-1 text-[12px] leading-relaxed break-words text-red">{t("settings-extensionmarket.market_error", { error: m.error })}</p>
                ))}
                {m.error && wantsGitHubToken(m) && <TokenHint onFix={() => setTokenFor(m)} />}
                {(m.warnings ?? []).map((w) => (
                  <p key={w} className="mt-1 flex items-start gap-1.5 text-[11.5px] leading-snug break-words text-orange">
                    <Icon name="info" size={12} className="mt-0.5 shrink-0" />
                    {w}
                  </p>
                ))}
              </div>
            </div>
            <div className="mt-2 ml-12 flex flex-wrap items-center gap-1.5">
              <Button tone="secondary" icon="retry" busy={busy[m.id] === "refresh"} disabled={!!busy[m.id]} onClick={() => void work(m, "refresh")}>
                {t("settings-extensionmarket.reload")}
              </Button>
              {!isGitMarket(m.spec) && !(m.error && wantsGitHubToken(m)) && (
                <Button tone="secondary" icon="key" disabled={!!busy[m.id]} onClick={() => setTokenFor(m)}>
                  {m.has_token ? t("settings-extensionmarket.change_token") : t("settings-extensionmarket.set_token")}
                </Button>
              )}
              <span className="flex-1" />
              {confirm === m.id ? (
                <span className="flex items-center gap-1" style={{ animation: `pop-in 160ms ${EASE} both` }}>
                  <Button tone="quiet" onClick={() => setConfirm("")}>
                    {t("common.cancel")}
                  </Button>
                  <Button tone="danger" busy={busy[m.id] === "remove"} onClick={() => void work(m, "remove")}>
                    {t("settings-extensionmarket.confirm_delete")}
                  </Button>
                </span>
              ) : (
                <Button tone="quiet" icon="trash" disabled={!!busy[m.id]} onClick={() => setConfirm(m.id)} className="text-red enabled:hover:text-red">
                  {t("settings-extensionmarket.delete")}
                </Button>
              )}
            </div>
          </div>
        ))}
      </Group>
      {adding && (
        <AddMarketDialog
          onClose={() => setAdding(false)}
          onAdded={(m) => {
            setList((l) => [...(l ?? []).filter((x) => x.id !== m.id), m]);
            setAdding(false);
            toast(t("settings-extensionmarket.market_added", { name: m.name }), "ok");
          }}
        />
      )}
      {tokenFor && (
        <TokenDialog
          market={tokenFor}
          onClose={() => setTokenFor(null)}
          onDone={(m) => {
            put(m);
            setTokenFor(null);
            load();
          }}
        />
      )}
    </>
  );
}

function AddMarketDialog({ onClose, onAdded }: { onClose: () => void; onAdded: (m: Market) => void }) {
  const [spec, setSpec] = useState("");
  const [token, setToken] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const git = isGitMarket(spec);
  const go = async () => {
    if (!spec.trim()) return;
    setBusy(true);
    setError("");
    try {
      onAdded(await addMarket(spec.trim(), git ? undefined : token.trim() || undefined));
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open
      onClose={() => !busy && onClose()}
      width={480}
      title={t("settings-extensionmarket.add_market")}
      footer={
        <>
          <Button tone="quiet" onClick={onClose} disabled={busy}>
            {t("common.cancel")}
          </Button>
          <Button tone="primary" busy={busy} disabled={!spec.trim()} onClick={() => void go()}>
            {t("settings-extensionmarket.add")}
          </Button>
        </>
      }
    >
      <form
        className="flex flex-col gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          void go();
        }}
      >
        <label className="block">
          <span className="mb-1.5 block text-[12px] font-medium text-ink-2">{t("settings-extensionmarket.address")}</span>
          <input
            value={spec}
            onChange={(e) => setSpec(e.target.value)}
            autoFocus
            placeholder={MARKET_SPEC_PLACEHOLDER}
            spellCheck={false}
            autoComplete="off"
            autoCapitalize="off"
            disabled={busy}
            className={cx(inputClass, "h-9 font-mono")}
          />
          <span className="mt-1 block text-[11.5px] leading-relaxed text-ink-3">{MARKET_SPEC_HINT}</span>
        </label>
        <label className="block">
          <span className="mb-1.5 block text-[12px] font-medium text-ink-2">{t("settings-extensionmarket.token_optional")}</span>
          <input
            type="password"
            value={token}
            onChange={(e) => setToken(e.target.value)}
            placeholder={git ? t("settings-extensionmarket.token_placeholder_git") : t("settings-extensionmarket.token_placeholder")}
            spellCheck={false}
            autoComplete="new-password"
            disabled={busy || git}
            className={cx(inputClass, "h-9 font-mono")}
          />
          <span className="mt-1 block text-[11.5px] leading-relaxed text-ink-3">
            {git ? t("settings-extensionmarket.token_hint_git") : t("settings-extensionmarket.token_hint")}
          </span>
        </label>
        {busy && <p className="text-[12px] text-ink-3">{t("settings-extensionmarket.adding")}</p>}
        {error && <p className="rounded-[8px] bg-red-tint px-3 py-2 text-[12px] leading-relaxed break-words text-red">{t("settings-extensionmarket.not_added", { error })}</p>}
        <button type="submit" hidden />
      </form>
    </Dialog>
  );
}

function TokenDialog({ market, onClose, onDone }: { market: Market; onClose: () => void; onDone: (m: Market) => void }) {
  const [token, setToken] = useState("");
  const [busy, setBusy] = useState<"" | "save" | "clear">("");
  const [error, setError] = useState("");
  const run = async (kind: "save" | "clear") => {
    if (kind === "save" && !token.trim()) return;
    setBusy(kind);
    setError("");
    try {
      const m = await setMarketToken(market.id, kind === "save" ? token.trim() : "");
      toast(kind === "save" ? t("settings-extensionmarket.token_replaced", { name: m.name }) : t("settings-extensionmarket.token_removed", { name: m.name }), "ok");
      onDone(m);
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
      title={t("settings-extensionmarket.token_title", { name: market.name })}
      footer={
        <>
          {market.has_token && (
            <span className="mr-auto">
              <Button tone="quiet" icon="trash" busy={busy === "clear"} disabled={!!busy} onClick={() => void run("clear")} className="text-red enabled:hover:text-red">
                {t("settings-extensionmarket.remove_token")}
              </Button>
            </span>
          )}
          <Button tone="quiet" onClick={onClose} disabled={!!busy}>
            {t("common.cancel")}
          </Button>
          <Button tone="primary" busy={busy === "save"} disabled={!token.trim() || !!busy} onClick={() => void run("save")}>
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
          {market.has_token ? t("settings-extensionmarket.token_set") : t("settings-extensionmarket.token_unset")}
        </div>
        <input
          type="password"
          value={token}
          onChange={(e) => setToken(e.target.value)}
          autoFocus
          autoComplete="new-password"
          spellCheck={false}
          placeholder={market.has_token ? t("settings-extensionmarket.token_input_replace") : t("settings-extensionmarket.token_input")}
          aria-label="token"
          disabled={!!busy}
          className={cx(inputClass, "h-9 font-mono")}
        />
        <p className="text-[11.5px] leading-relaxed text-ink-3">{t("settings-extensionmarket.token_reload_note")}</p>
        {isGitHubMarket(market) && (
          <div className="mt-1 flex flex-wrap items-center gap-2 rounded-[8px] bg-hover px-3 py-2">
            <p className="min-w-[200px] flex-1 text-[11.5px] leading-relaxed text-ink-3">{t("settings-extensionmarket.token_create_hint")}</p>
            <Button tone="secondary" icon="external" href={GITHUB_TOKEN_URL}>
              {t("settings-extensionmarket.token_create")}
            </Button>
          </div>
        )}
        {error && <p className="rounded-[8px] bg-red-tint px-3 py-2 text-[12px] break-words text-red">{t("settings-extensionmarket.not_saved", { error })}</p>}
        <button type="submit" hidden />
      </form>
    </Dialog>
  );
}

/** A permission line's icon: what it is about (its program on computers' lines like the hub's). */
function permissionIcon(key: string) {
  switch (key) {
    case "network":
    case "node_network":
      return "web";
    case "secrets":
    case "launcher_keys":
    case "node_secrets":
      return "key";
    case "hub_api":
      return "hub";
    case "runs_code":
    case "node_runs_code":
      return "terminal";
    case "node_files":
      return "folder";
    case "tools":
      return "download";
    default:
      return "info";
  }
}

