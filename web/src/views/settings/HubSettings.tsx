// Settings › hub (docs/operations/hub-update.md "In the app", the app's HubSettingsPage): the hub's version and channel, checking for
// and installing a release (waiting for the assistant and the agents to be idle, or now after a confirmation), a Docker
// hub's commands, auto-update, the personad on each computer, and the history. Follows the hub's `hub_update` pushes.
import { useCallback, useEffect, useRef, useState } from "react";
import { ApiError, errorText } from "../../lib/api";
import { ago } from "../../lib/format";
import { t } from "../../i18n";
import {
  channelTitle, checkHubUpdate, dockerSteps, hubUpdate, hubUpdateHistory, latestWarning, recordTitle, recordTone, setHubUpdateSettings, startHubUpdate,
  updateFinished, updateStateTitle, updateView, updateWorking, type HubUpdateRecord, type HubUpdateStatus,
} from "../../lib/hub-update";
import { navigate } from "../../lib/router";
import { isResumed } from "../../lib/reconnect";
import { onHubEvent, useStore } from "../../lib/store";
import { Button, copyText, cx, Dialog, IconButton, Loading, Spinner, toast } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { Group, Pill, Row, Switch } from "./SettingsParts";
import { ExtensionDownloadMirrors } from "./ExtensionDownloadMirrors";

export function HubSettings() {
  return <><HubUpdates /><ExtensionDownloadMirrors /></>;
}

function HubUpdates() {
  const [status, setStatus] = useState<HubUpdateStatus | null>(null);
  const [history, setHistory] = useState<HubUpdateRecord[]>([]);
  const [unsupported, setUnsupported] = useState(false);
  const [error, setError] = useState("");
  const [checking, setChecking] = useState(false);
  const [starting, setStarting] = useState(false);
  const [confirmNow, setConfirmNow] = useState(false);
  const last = useRef<string | undefined>(undefined);

  const apply = useCallback((s: HubUpdateStatus) => {
    const finished = updateFinished(last.current, s.state);
    last.current = s.state;
    setStatus(s);
    if (finished) hubUpdateHistory().then(setHistory, () => {});
  }, []);

  const load = useCallback(() => {
    setError("");
    return hubUpdate()
      .then((s) => {
        apply(s);
        setError("");
        hubUpdateHistory().then(setHistory, () => {});
      })
      .catch((e) => (e instanceof ApiError && e.status === 404 ? setUnsupported(true) : setError(errorText(e))));
  }, [apply]);

  useEffect(() => {
    void load();
    const off = onHubEvent("hub_update", (m) => m.update && apply(m.update as HubUpdateStatus));
    // a reconnect the hub could not resume (a restart into the new version, often): the hub_update pushes missed
    // meanwhile (confirming, updated, rolled back) are not replayed (#4954)
    const offHello = onHubEvent("hello", (m) => !isResumed(m) && void load());
    return () => {
      off();
      offHello();
    };
  }, [apply, load]);

  const check = async () => {
    setChecking(true);
    try {
      apply(await checkHubUpdate());
    } catch (e) {
      toast(t("settings-hubsettings.check_failed", { error: errorText(e) }), "error");
    } finally {
      setChecking(false);
    }
  };

  const update = async (now: boolean) => {
    setStarting(true);
    try {
      apply(await startHubUpdate(now));
      setConfirmNow(false);
    } catch (e) {
      toast(t("settings-hubsettings.start_failed", { error: errorText(e) }), "error");
    } finally {
      setStarting(false);
    }
  };

  const setAuto = async (on: boolean) => {
    try {
      apply(await setHubUpdateSettings({ auto: on }));
    } catch (e) {
      toast(t("settings-hubsettings.save_failed", { error: errorText(e) }), "error");
    }
  };

  if (unsupported) {
    return (
      <Group>
        <p className="px-4 py-3 text-[12.5px] leading-relaxed text-ink-3">{t("settings-hubsettings.unsupported")}</p>
      </Group>
    );
  }
  if (!status) {
    return error ? (
      <div className="flex items-center gap-3 px-1">
        <p role="alert" className="min-w-0 flex-1 text-[13px] break-words text-red">{t("settings-hubsettings.version_error", { error })}</p>
        <Button icon="retry" onClick={() => void load()}>{t("settings-hubsettings.retry")}</Button>
      </div>
    ) : (
      <Loading />
    );
  }

  const working = updateWorking(status);
  const view = updateView(status);
  const warning = latestWarning(status);

  return (
    <>
      {status.auto_notice && (
        <div className="mb-6 flex items-center gap-3 rounded-card bg-accent-tint px-4 py-2.5 text-[12.5px] text-accent-ink">
          <Icon name="info" size={14} className="shrink-0" />
          <span className="min-w-0 flex-1">{t("settings-hubsettings.auto_notice")}</span>
          <Button tone="quiet" onClick={() => void setAuto(false)} className="h-7">{t("settings-hubsettings.turn_off")}</Button>
          <Button tone="quiet" onClick={() => void setHubUpdateSettings({ auto_notice_seen: true }).then(apply, (e) => toast(errorText(e), "error"))} className="h-7">
            {t("settings-hubsettings.got_it")}
          </Button>
        </div>
      )}

      <Group title={t("settings-hubsettings.version")} anchor="hub-version">
        <Row label={t("settings-hubsettings.version")} mono>
          <span className="max-w-[320px] truncate" title={status.version}>{status.version}</span>
        </Row>
        <Row label={t("settings-hubsettings.channel")}>{channelTitle(status.channel)}</Row>
        {status.last_check && <Row label={t("settings-hubsettings.last_check")}>{ago(status.last_check)}</Row>}
        <div className="flex justify-end px-4 py-2.5">
          <Button icon="retry" busy={checking} disabled={checking || working} onClick={() => void check()}>
            {checking ? t("settings-hubsettings.checking") : t("settings-hubsettings.check")}
          </Button>
        </div>
      </Group>

      <Group title={t("settings-hubsettings.update")} anchor="hub-update" footer={status.can_update ? t("settings-hubsettings.update_footer") : undefined}>
        {view.kind === "docker" && <DockerRows s={status} />}
        {view.kind === "cannot" && <p className="px-4 py-3 text-[12.5px] leading-relaxed text-ink-3">{view.why}</p>}
        {view.kind === "newer" && status.latest && (
          <>
            <div className="px-4 py-3">
              <div className="flex items-center gap-2">
                <span className="truncate text-[13.5px] font-medium text-ink">{t("settings-hubsettings.new_version", { version: status.latest.version })}</span>
                {status.latest.released && <span className="shrink-0 text-[11.5px] text-ink-3">{t("settings-hubsettings.released", { time: ago(status.latest.released) })}</span>}
              </div>
              {status.latest.notes && <p className="mt-1 text-[12.5px] leading-relaxed text-ink-2">{status.latest.notes}</p>}
              {warning && (
                <p className="mt-1.5 flex items-start gap-1.5 text-[12px] leading-snug text-orange">
                  <Icon name="info" size={13} className="mt-px shrink-0" />
                  {warning}
                </p>
              )}
            </div>
            {view.working ? (
              <Progress s={status} onNow={() => setConfirmNow(true)} />
            ) : (
              <div className="flex justify-end px-4 py-2.5">
                <Button tone="accent" busy={starting} disabled={starting} onClick={() => void update(false)}>
                  {t("settings-hubsettings.update_hub")}
                </Button>
              </div>
            )}
          </>
        )}
        {view.kind === "working" && <Progress s={status} onNow={() => setConfirmNow(true)} />}
        {view.kind === "current" && (
          <div className="flex items-center gap-2 px-4 py-3 text-[13px] text-green">
            <Icon name="check" size={14} stroke={2.4} />
            {t("settings-hubsettings.up_to_date")}
          </div>
        )}
        {status.error && !working && <p className={cx("px-4 py-2.5 text-[12px] break-words", status.state === "rolled_back" ? "text-orange" : "text-red")}>{status.error}</p>}
      </Group>

      {status.can_update && (
        <Group footer={t("settings-hubsettings.auto_footer")}>
          <Row label={t("settings-hubsettings.auto_update")}>
            <Switch on={status.auto} onChange={(on) => void setAuto(on)} label={t("settings-hubsettings.auto_update")} />
          </Row>
        </Group>
      )}

      <Nodes />

      {history.length > 0 && (
        <Group title={t("settings-hubsettings.history")}>
          {history.slice(0, 10).map((r, i) => (
            <div key={`${r.to_build}/${r.started ?? i}`} className="px-4 py-2.5">
              <div className="flex items-center gap-2">
                <span className={cx("text-[13px] font-medium", { green: "text-green", orange: "text-orange", red: "text-red" }[recordTone(r.result)])}>{recordTitle(r.result)}</span>
                {r.auto && <Pill>{t("settings-hubsettings.auto")}</Pill>}
                <span className="flex-1" />
                {(r.ended || r.started) && <span className="shrink-0 text-[11.5px] text-ink-3">{ago(r.ended || r.started)}</span>}
              </div>
              <div className="mt-0.5 truncate font-mono text-[11.5px] text-ink-3">{`${r.from_version} → ${r.to_version}`}</div>
              {r.reason && <p className="mt-0.5 text-[11.5px] leading-snug text-ink-3">{r.reason}</p>}
            </div>
          ))}
        </Group>
      )}

      <Dialog
        open={confirmNow}
        onClose={() => setConfirmNow(false)}
        title={t("settings-hubsettings.confirm_title")}
        width={400}
        footer={
          <>
            <Button tone="quiet" onClick={() => setConfirmNow(false)}>{t("common.cancel")}</Button>
            <Button tone="danger" busy={starting} onClick={() => void update(true)}>{t("settings-hubsettings.update_now")}</Button>
          </>
        }
      >
        <p className="text-[12.5px] leading-relaxed text-ink-2">
          {t("settings-hubsettings.confirm_body")}
        </p>
      </Dialog>
    </>
  );
}

/** The update under way, and while it waits for idle what it waits for, with "Update now". */
function Progress({ s, onNow }: { s: HubUpdateStatus; onNow: () => void }) {
  return (
    <>
      <div className="flex items-center gap-2 px-4 py-3 text-[13px] text-ink">
        <Spinner />
        <span className="truncate">{updateStateTitle(s)}</span>
        {s.progress && s.state === "downloading" && <span className="shrink-0 font-mono text-[11.5px] text-ink-3">{s.progress}</span>}
      </div>
      {s.state === "waiting_idle" && s.busy.length > 0 && (
        <>
          {s.busy.map((b) => (
            <div key={`${b.machine_id}/${b.session_id ?? b.title}`} className="flex items-center gap-3 px-4 py-2">
              <span className="size-1.5 shrink-0 rounded-full bg-accent" />
              <span className="min-w-0 flex-1 truncate text-[12.5px] text-ink">{b.title}</span>
              <span className="shrink-0 text-[11.5px] text-ink-3">{b.machine}</span>
            </div>
          ))}
          <div className="flex justify-end px-4 py-2.5">
            <Button tone="quiet" onClick={onNow} className="text-red enabled:hover:text-red">{t("settings-hubsettings.update_now_ellipsis")}</Button>
          </div>
        </>
      )}
    </>
  );
}

function DockerRows({ s }: { s: HubUpdateStatus }) {
  const { headline, steps } = dockerSteps(s);
  return (
    <div className="px-4 py-3">
      {headline && <div className="mb-1.5 text-[13.5px] font-medium text-ink">{headline}</div>}
      {steps.map((st) => (
        <div key={st.copy} className="mb-2 last:mb-0">
          <p className="text-[12.5px] leading-relaxed text-ink-3">{st.text}</p>
          <div className="mt-1 flex items-center gap-2 rounded-control bg-inset px-2.5 py-1.5 shadow-hairline">
            <code className="min-w-0 flex-1 truncate font-mono text-[12px] text-ink select-all">{st.copy}</code>
            <IconButton icon="copy" label={t("settings-hubsettings.copy")} size={24} iconSize={12} onClick={() => void copyText(st.copy).then(() => toast(t("settings-hubsettings.copied"), "ok"))} />
          </div>
        </div>
      ))}
    </div>
  );
}

/** Each computer's personad: the hub puts its own version on them after an update. */
function Nodes() {
  const machines = useStore((s) => s.machines);
  if (!machines.length) return null;
  return (
    <Group title={t("settings-hubsettings.nodes")} footer={t("settings-hubsettings.nodes_footer")}>
      {machines.map((m) => (
        <Row key={m.id} label={m.name} onClick={() => navigate({ name: "machine", mid: m.id })}>
          {m.node_mismatch && <Pill tone="orange">{t("settings-hubsettings.version_differs")}</Pill>}
          <span className="max-w-[220px] truncate font-mono text-ink-3">{m.node_version || (m.online ? t("settings-hubsettings.unknown") : t("settings-hubsettings.offline"))}</span>
        </Row>
      ))}
    </Group>
  );
}
