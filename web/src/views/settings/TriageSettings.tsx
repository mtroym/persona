// Settings › Session triage (docs/assistant/agent-work.md "Switches"): when an agent stops, the hub can read its last reply and say whether it is
// done or what it waits for, and act on it. All off until turned on here: the triage calls a model on every stopped
// turn, and "Auto-continue" / "Wake on schedule" run a turn of the agent on its own model. Saved on the hub (PUT /agents/triage), so it is
// the same on every device.
// A read that fails is not "all off" (#4491): the switches are PUT as a whole set, so drawing them off and letting one be
// flipped would write off over every other switch the hub has on. Until the hub answers there are no switches, only
// "Loading" or why it could not read them and "Retry". A hub with no such route (404) is older than the triage: it says so,
// without a "Retry" that could not help (iOS / Android do the same).
// "Remind when seen, unanswered" (#5096) is rule-based and spends no tokens: on unless the hub says false, and not tied to the main switch.
import { useEffect, useState } from "react";
import { t } from "../../i18n";
import { tRich } from "../../i18n/rich";
import { api, ApiError, errorText } from "../../lib/api";
import type { TriageSwitches } from "../../lib/types";
import { Button, Loading, toast } from "../../ui/atoms";
import { Group, Row, Switch } from "./SettingsParts";

const OFF: TriageSwitches = { enabled: false, auto_continue: false, wake: false, decide: false };

// the hub's default wait before Remind When Seen, Unanswered (assistant/agents_reply.go), when it does not send its own
const REPLY_REMINDER_MINUTES = 30;

/** Remind When Seen, Unanswered is on unless the hub stored it off (absent: on). */
export function replyReminderOn(sw: Pick<TriageSwitches, "reply_reminder">): boolean {
  return sw.reply_reminder !== false;
}

export function TriageSettings({ onModel }: { onModel?: () => void }) {
  const [sw, setSw] = useState<TriageSwitches | null>(null);
  const [error, setError] = useState("");
  const [unsupported, setUnsupported] = useState(false);
  const [reload, setReload] = useState(0);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    let dead = false;
    setError("");
    api<TriageSwitches>("/agents/triage")
      .then((r) => !dead && setSw({ ...OFF, ...r }))
      .catch((e) => {
        if (dead) return;
        if (e instanceof ApiError && e.status === 404) setUnsupported(true);
        else setError(errorText(e));
      });
    return () => {
      dead = true;
    };
  }, [reload]);

  const change = async (next: TriageSwitches) => {
    const before = sw;
    setSw(next);
    setBusy(true);
    try {
      setSw({ ...OFF, ...(await api<TriageSwitches>("/agents/triage", { method: "PUT", json: next })) });
    } catch (e) {
      setSw(before);
      toast(t("settings-triagesettings.save_failed", { error: errorText(e) }), "error");
    } finally {
      setBusy(false);
    }
  };
  const flip = (k: "enabled" | "auto_continue" | "wake" | "decide" | "tool_loop" | "reply_reminder") => (on: boolean) => sw && change({ ...sw, [k]: on });
  const off = !sw?.enabled;

  return (
    <Group
      anchor="triage"
      title={t("settings-triagesettings.title")}
      footer={
        <>
          {tRich("settings-triagesettings.footer", {
            model: onModel ? (
              <button type="button" onClick={onModel} className="mx-0.5 text-accent hover:underline">
                {t("settings-triagesettings.model_path")}
              </button>
            ) : (
              t("settings-triagesettings.model_path_quoted")
            ),
          })}
        </>
      }
    >
      {!sw ? (
        unsupported ? (
          <p className="px-4 py-3 text-[12.5px] text-ink-3">{t("settings-triagesettings.unsupported")}</p>
        ) : error ? (
          <div className="flex items-center gap-3 px-4 py-3">
            <p role="alert" className="min-w-0 flex-1 text-[12.5px] text-red">{t("settings-triagesettings.load_failed", { error })}</p>
            <Button tone="secondary" icon="retry" onClick={() => setReload((n) => n + 1)}>
              {t("settings-triagesettings.retry")}
            </Button>
          </div>
        ) : (
          <div className="px-4 py-3">
            <Loading />
          </div>
        )
      ) : (
        <>
          <Row label={t("settings-triagesettings.title")} hint={t("settings-triagesettings.enabled_hint")}>
            <Switch on={sw.enabled} onChange={flip("enabled")} disabled={busy} label={t("settings-triagesettings.title")} />
          </Row>
          <Row label={t("settings-triagesettings.decide")} hint={t("settings-triagesettings.decide_hint")}>
            <Switch on={sw.decide} onChange={flip("decide")} disabled={busy || off} label={t("settings-triagesettings.decide")} />
          </Row>
          <Row label={t("settings-triagesettings.auto_continue")} hint={t("settings-triagesettings.auto_continue_hint")}>
            <Switch on={sw.auto_continue} onChange={flip("auto_continue")} disabled={busy || off} label={t("settings-triagesettings.auto_continue")} />
          </Row>
          <Row label={t("settings-triagesettings.wake")} hint={t("settings-triagesettings.wake_hint")}>
            <Switch on={sw.wake} onChange={flip("wake")} disabled={busy || off} label={t("settings-triagesettings.wake")} />
          </Row>
          <Row label={t("settings-triagesettings.tool_loop")} hint={t("settings-triagesettings.tool_loop_hint")} wrapHint>
            <Switch on={sw.tool_loop === true} onChange={flip("tool_loop")} disabled={busy || off} label={t("settings-triagesettings.tool_loop")} />
          </Row>
          {/* no model call, so it works with the triage off */}
          <Row label={t("settings-triagesettings.reply_reminder")} hint={t("settings-triagesettings.reply_reminder_hint", { minutes: sw.reply_reminder_minutes || REPLY_REMINDER_MINUTES })}>
            <Switch on={replyReminderOn(sw)} onChange={flip("reply_reminder")} disabled={busy} label={t("settings-triagesettings.reply_reminder")} />
          </Row>
        </>
      )}
    </Group>
  );
}
