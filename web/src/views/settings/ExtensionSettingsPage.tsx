import { useEffect, useId, useState, type ReactNode } from "react";
import { errorText } from "../../lib/api";
import { ago } from "../../lib/format";
import { extensionUpgrades, sourceText, upgradeAction, upgradeDetail, upgradeNote, type HubExtensionDetail } from "../../lib/extensions";
import { getCatalog, marketOf, type CatalogItem } from "../../lib/extension-market";
import { toolText } from "../../lib/extension-auth";
import { t } from "../../i18n";
import { Button } from "../../ui/atoms";
import { Group, Row, Switch, SettingsHint, SettingsSection } from "./SettingsParts";

/** Host-owned lifecycle UI for every extension. Only the configuration slot contains extension-supplied fields/pages. */
export function ExtensionSettingsPage({ detail: d, manager, busy, offline = false, configuration, information, saveBar, onEnabled, onAutoUpgrade, onUpgrade, onUninstall, onChecked }: {
  detail: HubExtensionDetail;
  manager?: string;
  busy: boolean;
  offline?: boolean;
  configuration: ReactNode;
  information?: ReactNode;
  saveBar?: ReactNode;
  onEnabled: (on: boolean) => void;
  onAutoUpgrade: (on: boolean) => void;
  onUpgrade: (market: string, id: string) => void;
  onUninstall: () => void;
  onChecked: () => Promise<void>;
}) {
  const editable = !d.read_only && d.source !== "dev" && d.source !== "hub.json";
  const market = marketOf(d.origin) || d.upgrade?.market || "";
  const marketManaged = d.source === "installed" && !d.attached && !d.read_only && !!market;
  const uninstallable = d.source === "installed" && !d.attached && !d.read_only && !!marketOf(d.origin);
  const automatic = d.source === "installed" && !d.attached && !d.read_only && d.auto_upgrade !== undefined;
  const noUpdates = offline ? t("settings-extensions.manager_offline") : marketManaged && !automatic ? t("settings-extensions.action_unknown") : lifecycleReason(d, false);
  const noUninstall = offline ? t("settings-extensions.manager_offline") : lifecycleReason(d, true);
  const blocked = busy || offline;
  return (
    <div data-extension-settings={d.id}>
      <section data-extension-section="enabled" aria-label={t("settings-extensions.enable")}>
        <Group footer={offline ? t("settings-extensions.manager_offline") : editable ? t("settings-extensions.enable_footer") : t(d.read_only ? "settings-extensions.read_only" : "settings-extensions.externally_managed")}>
          <Row label={t("settings-extensions.enable")}>
            <Switch on={d.enabled ?? true} onChange={onEnabled} disabled={blocked || !editable} label={t("settings-extensions.enable")} />
          </Row>
        </Group>
      </section>
      <SettingsSection name="configuration" title={t("settings-extensions.configuration_title")}>
        {configuration}
        {saveBar}
      </SettingsSection>
      <section data-extension-section="information" aria-label={t("settings-extensions.about_title")}>
        <Information d={d} />
        {information}
      </section>
      <section data-extension-section="updates" aria-label={t("settings-extensions.upgrade_title")}>
        <Updates d={d} manager={manager} busy={blocked} market={market} canCheck={marketManaged && !offline} automatic={automatic} reason={noUpdates} onToggle={onAutoUpgrade} onUpgrade={onUpgrade} onChecked={onChecked} />
      </section>
      <section data-extension-section="danger" aria-label={t("settings-extensions.danger_title")}>
        <Group title={t("settings-extensions.danger_title")} footer={uninstallable && !offline ? t("settings-extensions.market_installed_footer") : noUninstall}>
          <Row label={t("settings-extensions.uninstall")}>
            <Button tone="danger" icon="trash" disabled={blocked || !uninstallable} onClick={onUninstall}>{t("settings-extensions.uninstall")}</Button>
          </Row>
        </Group>
      </section>
    </div>
  );
}

/** Capabilities follow the full-rights market routes, not a manifest's configuration or arbitrary source URL. */
function lifecycleReason(d: HubExtensionDetail, removing: boolean): string {
  if (d.source === "builtin") return t(removing ? "settings-extensions.builtin_uninstall" : "settings-extensions.builtin_updates");
  if (d.read_only || d.source === "hub.json" || d.source === "dev" || d.attached || d.source === "installed") {
    return t(removing ? "settings-extensions.external_uninstall" : "settings-extensions.external_updates");
  }
  return t("settings-extensions.action_unknown");
}

function Updates({ d, manager, busy, market, canCheck, automatic, reason, onToggle, onUpgrade, onChecked }: {
  d: HubExtensionDetail; manager?: string; busy: boolean; market: string; canCheck: boolean; automatic: boolean; reason: string;
  onToggle: (on: boolean) => void; onUpgrade: (market: string, id: string) => void; onChecked: () => Promise<void>;
}) {
  const [checking, setChecking] = useState(false);
  const [checkError, setCheckError] = useState("");
  const [checked, setChecked] = useState(false);
  const [candidate, setCandidate] = useState<CatalogItem | null>(null);
  const [globalAuto, setGlobalAuto] = useState<boolean | null>(null);
  useEffect(() => { setCandidate(null); setChecked(false); }, [d.version]);
  useEffect(() => {
    if (!automatic) return;
    let live = true;
    setGlobalAuto(null);
    void extensionUpgrades(manager).then((auto) => live && setGlobalAuto(auto));
    return () => { live = false; };
  }, [automatic, manager]);
  const check = async () => {
    if (!canCheck || checking || busy) return;
    setChecking(true);
    setCheckError("");
    setChecked(false);
    try {
      const catalog = await getCatalog(undefined, market, true, manager);
      const source = catalog.markets.find((m) => m.id === market);
      if (!source || source.error) throw new Error(t("settings-extensions.market_unavailable"));
      const item = catalog.extensions.find((e) => e.market === market && e.id === d.id);
      if (!item) throw new Error(t("settings-extensions.market_extension_missing"));
      await onChecked();
      setCandidate(item);
      setChecked(true);
    } catch (error) {
      setCheckError(errorText(error));
    } finally {
      setChecking(false);
    }
  };
  const u = d.upgrade || (candidate?.upgradable ? { id: d.id, market, from: candidate.installed || d.version || "", to: candidate.version, state: "available" } : null);
  const action = u ? upgradeAction(u) || (u.state === "available" ? t("lib-extensions.action_upgrade") : null) : null;
  const note = d.upgrade_note ? upgradeNote(d.upgrade_note) : null;
  const paused = d.auto_upgrade_paused;
  const footer = !automatic ? reason : paused === "market_removed" ? t("settings-extensions.market_removed") : paused ? t("settings-extensions.upgrade_paused_footer") : t("settings-extensions.upgrade_footer");
  const descriptionId = useId();
  return (
    <Group title={t("settings-extensions.upgrade_title")}>
      <Row label={t("settings-extensions.update_status")} hint={!canCheck ? reason : u ? upgradeDetail(u) : checked && candidate?.upgradable === false ? t("settings-extensions.up_to_date") : t("settings-extensions.check_updates_hint")} wrapHint>
        <Button tone="secondary" icon="retry" busy={checking} disabled={busy || !canCheck} onClick={() => void check()}>{t("settings-extensions.check_updates")}</Button>
      </Row>
      {checkError && <SettingsHint alert>{checkError}</SettingsHint>}
      {u && (
        <Row label={t("settings-extensions.new_version", { version: u.to })} hint={upgradeDetail(u)} wrapHint>
          {action && <Button tone="secondary" disabled={busy || checking || !canCheck} onClick={() => onUpgrade(u.market, u.id)}>{action}</Button>}
        </Row>
      )}
      <Row label={t("settings-extensions.auto_upgrade")}>
        <Switch on={d.auto_upgrade ?? false} onChange={onToggle} disabled={busy || checking || !automatic || !!paused} label={t("settings-extensions.auto_upgrade")} describedBy={descriptionId} />
      </Row>
      <SettingsHint id={descriptionId} caution={globalAuto === false}>
        {globalAuto === false ? t("settings-extensions.global_upgrade_off") : footer}
      </SettingsHint>
      {note && d.upgrade_note && <Row label={note} hint={ago(d.upgrade_note.at)} wrapHint />}
      {d.previous_version && <Row label={t("settings-extensions.previous_version")} mono wrapValue>{d.previous_version}</Row>}
    </Group>
  );
}

function Information({ d }: { d: HubExtensionDetail }) {
  let home = "";
  try { home = d.homepage && new URL(d.homepage).protocol === "https:" ? d.homepage : ""; } catch { /* Invalid manifest URL stays unavailable. */ }
  const missing = t("settings-extensions.not_provided");
  const source = d.attached ? t("settings-extensions.source_attached") : d.source === "hub.json" ? t("settings-extensions.source_configured") : d.source === "dev" ? t("settings-extensions.source_development") : sourceText(d.source) || missing;
  const provenance = d.attached ? t("settings-extensions.source_attached") : d.source === "installed" && d.origin && !marketOf(d.origin) ? t("settings-extensions.local_source") : d.origin || missing;
  return (
    <Group title={t("settings-extensions.about_title")}>
      <Row label={t("settings-extensions.version")} mono wrapValue>{d.version || missing}</Row>
      {d.attached?.version && <Row label={t("settings-extensions.reported_version")} mono wrapValue>{d.attached.version}</Row>}
      <Row label={t("settings-extensions.extension_id")} mono wrapValue><span className="select-text">{d.id}</span></Row>
      <Row label={t("settings-extensions.source")} wrapValue>{source}</Row>
      <Row label={t("settings-extensions.runtime")} mono wrapValue>{d.kind || missing}</Row>
      <Row label={t("settings-extensions.installed_from")} mono wrapValue><span data-extension-provenance className="min-w-0 break-all select-text">{provenance}</span></Row>
      {d.publisher && <Row label={t("settings-extensions.publisher")} wrapValue>{d.publisher}</Row>}
      {(d.tools ?? []).map((x) => <Row key={x.name} label={x.title || x.name} hint={t("settings-extensions.tool_title")}>{toolText(x)}</Row>)}
      {home && <Row label={t("settings-extensions.homepage")}><Button tone="quiet" href={home}>{new URL(home).host}</Button></Row>}
      {(d.warnings ?? []).map((warning) => <Row key={warning} label={t("settings-extensions.warning")} hint={warning} wrapHint />)}
    </Group>
  );
}
