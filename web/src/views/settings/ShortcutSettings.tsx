// Settings › Shortcuts: the switch for all, then every shortcut with its chord; click one and press the new chord (Esc cancels),
// a chord another command has offers "Replace" (that one is cleared) or "Cancel". Each row can be cleared (its key does nothing,
// no default either), turned off (keeps its chord), and put back to its default; "Reset all" for the page. Stored in
// this browser (lib/shortcuts.ts). docs/web/web.md "Keyboard shortcuts".
import { useEffect, useState } from "react";
import {
  checkBinding,
  chordParts,
  clearBinding,
  conflictOf,
  currentPlatform,
  eventChord,
  getConfigured,
  holdShortcuts,
  isDefault,
  LOCAL_SHORTCUTS,
  modName,
  replaceBinding,
  resetAllBindings,
  resetBinding,
  SCOPE_LABEL,
  SHORTCUTS,
  setBinding,
  setShortcutEnabled,
  setShortcutsOn,
  shortcutDef,
  useBindings,
  useShortcutPrefs,
  type ShortcutDef,
  type ShortcutScope,
} from "../../lib/shortcuts";
import { t } from "../../i18n";
import { Button, cx, IconButton, Kbd } from "../../ui/atoms";
import { Group, Row, RowConfirm, Switch } from "./SettingsParts";
import { composing } from "../../lib/send-history";

const SCOPES: ShortcutScope[] = ["global", "session", "sidebar"];

/** A chord that another command has: "Replace" gives it to `id` and clears `with`. */
interface Pending {
  id: string;
  chord: string;
  with: string;
  /** It is "Reset to default" that found the default taken. */
  reset?: boolean;
}

export function ShortcutSettings() {
  const prefs = useShortcutPrefs();
  // re-read on every change (useShortcutPrefs re-renders): the chords as set, on or off
  const configured = getConfigured();
  const active = useBindings();
  // the command whose new chord is awaited, and what was wrong with the last one pressed for it
  const [recording, setRecording] = useState<string | null>(null);
  const [problem, setProblem] = useState<{ id: string; text: string } | null>(null);
  const [pending, setPending] = useState<Pending | null>(null);
  const platform = currentPlatform();
  const changed = prefs.off || SHORTCUTS.some((s) => !isDefault(s.id, prefs));

  // the app's own shortcuts stand aside while a chord is awaited
  useEffect(() => (recording ? holdShortcuts() : undefined), [recording]);

  const stop = () => {
    setRecording(null);
    setProblem(null);
  };

  const onKey = (e: React.KeyboardEvent, def: ShortcutDef) => {
    if (composing(e.nativeEvent)) return;
    // Tab on its own moves on (and the blur ends the recording): a keyboard user is not trapped here
    if (e.key === "Tab" && !e.ctrlKey && !e.metaKey && !e.altKey) return;
    e.preventDefault();
    e.stopPropagation();
    if (e.key === "Escape") return stop();
    const pressed = eventChord(e.nativeEvent, platform);
    // a modifier on its own: still waiting for the key
    if (!pressed && ["Control", "Meta", "Alt", "Shift", "AltGraph"].includes(e.key)) return;
    const check = checkBinding(def.id, pressed, configured, platform);
    if (!check.ok) {
      if (check.reason === "conflict" && check.with && check.chord) {
        stop();
        return setPending({ id: def.id, chord: check.chord, with: check.with });
      }
      return setProblem({ id: def.id, text: check.message });
    }
    setBinding(def.id, check.chord);
    stop();
  };

  // "Reset to default" for one: its default chord may have been given to another command since
  const reset = (def: ShortcutDef) => {
    stop();
    const other = configured[def.id] === def.chord ? undefined : conflictOf(def.id, def.chord, configured);
    if (other) return setPending({ id: def.id, chord: def.chord, with: other.id, reset: true });
    resetBinding(def.id);
  };

  const replace = (p: Pending) => {
    replaceBinding(p.id, p.chord, p.with);
    setShortcutEnabled(p.id, true);
    setPending(null);
  };

  return (
    <>
      <Group
        title={t("settings-shortcutsettings.title")}
        right={
          <Button tone="quiet" disabled={!changed} onClick={() => (stop(), setPending(null), resetAllBindings())}>
            {t("settings-shortcutsettings.reset_all")}
          </Button>
        }
        footer={prefs.off ? t("settings-shortcutsettings.all_off_footer") : undefined}
      >
        <Row label={t("settings-shortcutsettings.enable_all")} hint={t("settings-shortcutsettings.enable_all_hint")}>
          <Switch on={!prefs.off} onChange={setShortcutsOn} label={t("settings-shortcutsettings.enable_all")} />
        </Row>
      </Group>
      {SCOPES.map((scope, n) => (
        <Group
          key={scope}
          title={SCOPE_LABEL[scope]}
          footer={
            n === SCOPES.length - 1
              ? t(platform === "mac" ? "settings-shortcutsettings.footer_mac" : "settings-shortcutsettings.footer_other", {
                  mod: modName(platform),
                  help: chordParts(active.help ?? "", platform).join(platform === "mac" ? "" : "+") || t("settings-shortcutsettings.help_command"),
                })
              : undefined
          }
        >
          {SHORTCUTS.filter((s) => s.scope === scope).map((s) => {
            const waiting = recording === s.id;
            const chord = configured[s.id] ?? s.chord;
            const on = !prefs.disabled.includes(s.id);
            const asking = pending?.id === s.id ? pending : null;
            const error = problem?.id === s.id ? problem.text : "";
            const hint = asking ? (
              <span className="text-orange">
                {t(asking.reset ? "settings-shortcutsettings.default_taken" : "settings-shortcutsettings.taken", {
                  chord: chordParts(asking.chord, platform).join(platform === "mac" ? "" : "+"),
                  label: shortcutDef(asking.with)?.label ?? asking.with,
                })}
              </span>
            ) : error ? (
              <span className="text-red">{error}</span>
            ) : !on ? (
              t("settings-shortcutsettings.row_off")
            ) : undefined;
            return (
              <Row key={s.id} label={<span className={cx((!on || prefs.off) && "text-ink-3")}>{s.label}</span>} hint={hint}>
                <RowConfirm
                  open={Boolean(asking)}
                  confirm={t("settings-shortcutsettings.replace")}
                  onCancel={() => setPending(null)}
                  onConfirm={() => asking && replace(asking)}
                  trigger={
                    <>
                      {!isDefault(s.id, prefs) && !waiting && <IconButton icon="undo" label={t("settings-shortcutsettings.reset_shortcut", { name: s.label })} size={26} iconSize={13} onClick={() => reset(s)} />}
                      {chord && !waiting && <IconButton icon="x" label={t("settings-shortcutsettings.clear_shortcut", { name: s.label })} size={26} iconSize={13} onClick={() => (stop(), clearBinding(s.id))} />}
                      <button
                        type="button"
                        aria-label={
                          waiting
                            ? t("settings-shortcutsettings.recording_label", { name: s.label })
                            : chord
                              ? t("settings-shortcutsettings.chord_label", { name: s.label, chord: chordParts(chord, platform).join(" ") })
                              : t("settings-shortcutsettings.unset_label", { name: s.label })
                        }
                        onClick={() => {
                          setProblem(null);
                          setPending(null);
                          setRecording(s.id);
                        }}
                        onKeyDown={waiting ? (e) => onKey(e, s) : undefined}
                        onBlur={() => waiting && stop()}
                        autoFocus={waiting}
                        ref={(el) => {
                          if (waiting) el?.focus();
                        }}
                        className={cx(
                          "touch-hit flex min-h-7 min-w-[7.5rem] items-center justify-end gap-1 whitespace-nowrap rounded-control px-1.5 transition-[background-color,box-shadow,opacity] duration-150",
                          waiting ? "bg-accent-tint text-[12px] text-accent-ink shadow-[inset_0_0_0_1px_var(--accent)]" : "hover:bg-hover",
                          !waiting && (!on || prefs.off) && "opacity-50",
                        )}
                      >
                        {waiting ? (
                          t("settings-shortcutsettings.recording")
                        ) : chord ? (
                          chordParts(chord, platform).map((k, i) => <Kbd key={i}>{k}</Kbd>)
                        ) : (
                          <span className="text-[12px] text-ink-3">{t("settings-shortcutsettings.unset")}</span>
                        )}
                      </button>
                      <Switch on={on} onChange={(v) => setShortcutEnabled(s.id, v)} label={t("settings-shortcutsettings.enable_item", { name: s.label })} />
                    </>
                  }
                />
              </Row>
            );
          })}
        </Group>
      ))}
      <Group title={t("settings-shortcutsettings.terminal_title")} anchor="shortcuts-terminal" footer={t("settings-shortcutsettings.terminal_footer")}>
        {LOCAL_SHORTCUTS.map((s) => (
          <Row key={s.id} label={s.label} hint={platform === "other" ? s.note : undefined}>
            <span className="flex items-center gap-1 px-1.5">
              {chordParts(s.chord[platform], platform).map((k, i) => (
                <Kbd key={i}>{k}</Kbd>
              ))}
            </span>
          </Row>
        ))}
      </Group>
    </>
  );
}
