// Settings › Notifications (#2971, the app's NotificationSettingsView): this browser's system notifications, and what it is told
// about: "When something needs you" and "When an agent finishes" ("Don't notify" / "Long tasks only" / "Every time"). The two choices are kept by the hub per device
// (GET / PUT /devices/{id}/prefs, lib/notify.ts), as the phone's are. The "Alert sound" switch sits here too, under the system
// one (#4057): it is this browser's alone (lib/attention.ts), so it shows while the hub's prefs load, and it follows them.
import { useCallback, useEffect, useState } from "react";
import { formatDate, t } from "../../i18n";
import { errorText, isAbort } from "../../lib/api";
import { setAttentionSoundEnabled, useAttentionSoundSetting } from "../../lib/attention";
import { DEFAULT_QUIET, durationText, loadNotifyPrefs, notifyPrefsGeneration, saveNotifyPrefs, setSystemNotify, useNotifyPrefs, useNotifyPrefsGeneration, useSystemNotify, type DonePref, type Permission, type QuietHours } from "../../lib/notify";
import { Button, cx, Loading, toast } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { Group, Pill, Row, Switch } from "./SettingsParts";

const DONE: { v: DonePref; key: string }[] = [
  { v: "off", key: "settings-notifications.done_off" },
  { v: "long", key: "settings-notifications.done_long" },
  { v: "always", key: "settings-notifications.done_always" },
];

/** This browser's time zone, kept with the window so the hub reads it as this browser does. */
function browserZone(): string | undefined {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || undefined;
  } catch {
    return undefined;
  }
}

/** "23:00" as this language writes a time of day ("11:00 PM" in English), as the time fields show it. */
function clockText(hhmm: string): string {
  const [h, m] = hhmm.split(":").map(Number);
  return formatDate(new Date(2000, 0, 1, h, m), { timeStyle: "short" });
}

const timeClass =
  "h-8 w-28 rounded-control bg-field px-2.5 text-[12.5px] tabular-nums text-ink shadow-inset-field outline-none focus:shadow-[inset_0_0_0_1px_var(--accent)] disabled:opacity-60";

function permissionHint(p: Permission, on: boolean): string | undefined {
  if (p === "unsupported") return t("settings-notifications.system_unsupported");
  if (p === "denied") return t("settings-notifications.system_denied_short");
  if (p === "default" || !on) return t("settings-notifications.system_ask");
  return t("settings-notifications.system_on");
}

export function NotificationSettings() {
  const prefs = useNotifyPrefs();
  const generation = useNotifyPrefsGeneration();
  const system = useSystemNotify();
  const sound = useAttentionSoundSetting();
  const [loaded, setLoaded] = useState<{ generation: typeof generation; state: "loading" | "ready" | { error: string } }>({ generation, state: "loading" });
  const state = loaded.generation === generation ? loaded.state : "loading";
  const [asking, setAsking] = useState(false);

  const load = useCallback(() => {
    setLoaded({ generation, state: "loading" });
    loadNotifyPrefs().then(
      () => { if (generation === notifyPrefsGeneration()) setLoaded({ generation, state: "ready" }); },
      (e) => { if (generation === notifyPrefsGeneration() && !isAbort(e)) setLoaded({ generation, state: { error: errorText(e) } }); },
    );
  }, [generation]);
  useEffect(load, [load]);

  const save = (patch: Parameters<typeof saveNotifyPrefs>[0]) => saveNotifyPrefs(patch).catch((e) => {
    if (!isAbort(e)) toast(t("settings-notifications.save_failed", { error: errorText(e) }), "error");
  });

  const toggleSystem = async (on: boolean) => {
    setAsking(true);
    try {
      const p = await setSystemNotify(on);
      if (on && p === "denied") toast(t("settings-notifications.system_denied"), "error");
    } finally {
      setAsking(false);
    }
  };

  const ready = state === "ready";
  const quiet = prefs.quiet ?? DEFAULT_QUIET;
  const saveQuiet = (patch: Partial<QuietHours>) => void save({ quiet: { ...quiet, tz: browserZone(), ...patch } });
  const long = durationText(prefs.long_turn_seconds, {
    s: (count) => t("settings-notifications.seconds", { count }),
    m: (count) => t("settings-notifications.minutes", { count }),
  });

  return (
    <>
      <Group title={t("settings-notifications.system_title")} anchor="system" footer={system.permission === "denied" ? t("settings-notifications.system_denied") : t("settings-notifications.system_footer")}>
        <Row icon="bell" label={t("settings-notifications.system")} hint={permissionHint(system.permission, system.on)}>
          {system.permission === "denied" && <Pill tone="orange">{t("settings-notifications.denied_pill")}</Pill>}
          <Switch
            on={system.on && system.permission === "granted"}
            onChange={(on) => void toggleSystem(on)}
            disabled={asking || system.permission === "unsupported" || system.permission === "denied"}
            label={t("settings-notifications.system")}
          />
        </Row>
      </Group>

      <Group footer={t("settings-notifications.sound_footer")}>
        <Row icon="volume" label={t("settings-notifications.sound")} hint={t("settings-notifications.sound_hint")}>
          <Switch on={sound} onChange={setAttentionSoundEnabled} label={t("settings-notifications.sound")} />
        </Row>
      </Group>

      {state === "loading" && (
        <div className="mb-6 flex justify-center py-6">
          <Loading />
        </div>
      )}
      {typeof state === "object" && (
        <Group>
          <Row label={t("settings-notifications.load_failed")} hint={state.error}>
            <Button tone="secondary" onClick={load}>
              {t("settings-notifications.retry")}
            </Button>
          </Row>
        </Group>
      )}

      {ready && (
        <>
          <Group footer={t("settings-notifications.input_footer")}>
            <Row icon="hand" label={t("settings-notifications.input")} hint={t("settings-notifications.input_hint")}>
              <Switch on={prefs.input} onChange={(input) => void save({ input })} label={t("settings-notifications.input")} />
            </Row>
          </Group>

          <Group title={t("settings-notifications.done_title")} anchor="done" footer={t("settings-notifications.done_footer", { long })}>
            <div role="radiogroup" aria-label={t("settings-notifications.done_title")} className="divide-y divide-line">
              {DONE.map((o) => {
                const on = prefs.done === o.v;
                return (
                  <button
                    key={o.v}
                    type="button"
                    role="radio"
                    aria-checked={on}
                    onClick={() => !on && void save({ done: o.v })}
                    className="flex w-full items-center gap-3 whitespace-nowrap px-4 py-2.5 text-left transition-colors hover:bg-hover pointer-coarse:min-h-11"
                  >
                    <span className="min-w-0 flex-1 truncate text-[13px] text-ink">{t(o.key)}</span>
                    <span
                      aria-hidden
                      className={cx(
                        "flex size-[18px] shrink-0 items-center justify-center rounded-full transition-[background-color,box-shadow] duration-150",
                        on ? "bg-accent text-white" : "shadow-[inset_0_0_0_1.5px_var(--line-strong)]",
                      )}
                    >
                      {on && <Icon name="check" size={11} stroke={3} />}
                    </span>
                  </button>
                );
              })}
            </div>
          </Group>

          <Group footer={t("settings-notifications.quiet_footer")}>
            <Row
              icon="moon"
              label={t("settings-notifications.quiet")}
              hint={quiet.enabled ? t("settings-notifications.quiet_range", { start: clockText(quiet.start), end: clockText(quiet.end) }) : t("settings-notifications.quiet_hint")}
            >
              <Switch on={quiet.enabled} onChange={(enabled) => saveQuiet({ enabled })} label={t("settings-notifications.quiet")} />
            </Row>
            {quiet.enabled && (
              <>
                <Row label={t("settings-notifications.quiet_start")}>
                  <input
                    type="time"
                    className={timeClass}
                    aria-label={t("settings-notifications.quiet_start")}
                    value={quiet.start}
                    onChange={(e) => e.target.value && saveQuiet({ start: e.target.value.slice(0, 5) })}
                  />
                </Row>
                <Row label={t("settings-notifications.quiet_end")}>
                  <input
                    type="time"
                    className={timeClass}
                    aria-label={t("settings-notifications.quiet_end")}
                    value={quiet.end}
                    onChange={(e) => e.target.value && saveQuiet({ end: e.target.value.slice(0, 5) })}
                  />
                </Row>
              </>
            )}
          </Group>
        </>
      )}

      <p className="px-1 text-[11.5px] leading-relaxed text-ink-3">{t("settings-notifications.device_note")}</p>
    </>
  );
}
