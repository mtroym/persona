// Settings › General › Sidebar pages (docs/web/web.md "The sidebar's pages"): every page of the sidebar in its order, dragged (or moved with ↑ / ↓)
// into another, and switched between the list and More. Settings stays in the list; Reset forgets every choice.
import { useState } from "react";
import { t } from "../../i18n";
import { EXTENSION_CAP, inList, LOCKED, movePage, ordered, resetSidebarPrefs, setPageOrder, setPageShown, useSidebarPrefs } from "../../lib/sidebar-pages";
import { Button, cx, IconButton } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { useNavPages } from "../Sidebar";
import { Group, Switch } from "./SettingsParts";

export function SidebarPagesGroup() {
  const { all } = useNavPages();
  const prefs = useSidebarPrefs();
  const keys = ordered(all.map((p) => p.k), prefs.order);
  const listed = inList(keys, prefs);
  const byKey = new Map(all.map((p) => [p.k, p]));
  const [dragging, setDragging] = useState<string | null>(null);
  const [over, setOver] = useState<string | null>(null);
  const changed = prefs.order.length > 0 || prefs.shown.length > 0 || prefs.hidden.length > 0;

  const drop = (target: string) => {
    if (!dragging || dragging === target) return;
    const next = keys.filter((k) => k !== dragging);
    next.splice(next.indexOf(target), 0, dragging);
    setPageOrder(next);
  };

  return (
    <Group
      anchor="sidebar-pages"
      title={t("settings.sidebar_pages")}
      footer={t("settings.sidebar_pages_footer", { cap: EXTENSION_CAP })}
      right={changed ? <Button tone="quiet" onClick={resetSidebarPrefs} className="-mb-1 h-6 px-2 text-[11.5px]">{t("settings.sidebar_pages_reset")}</Button> : undefined}
    >
      <ul aria-label={t("settings.sidebar_pages")} className="divide-y divide-line">
        {keys.map((k, i) => {
          const p = byKey.get(k)!;
          const on = listed.has(k);
          const locked = k === LOCKED;
          // an extension's page: the extension it comes from (its NavPage.title is "Page · Extension"; a built-in page's
          // title is its badge's explanation, not shown here)
          const from = k.startsWith("p:") && p.title?.startsWith(`${p.label} · `) ? p.title.slice(p.label.length + 3) : "";
          return (
            <li
              key={k}
              data-page={k}
              draggable
              onDragStart={(e) => {
                setDragging(k);
                e.dataTransfer.effectAllowed = "move";
                e.dataTransfer.setData("text/plain", k);
              }}
              onDragOver={(e) => {
                if (!dragging) return;
                e.preventDefault();
                setOver(k);
              }}
              onDragLeave={() => setOver((o) => (o === k ? null : o))}
              onDrop={(e) => {
                e.preventDefault();
                drop(k);
                setDragging(null);
                setOver(null);
              }}
              onDragEnd={() => {
                setDragging(null);
                setOver(null);
              }}
              className={cx("flex items-center gap-3 px-3 py-2 transition-colors", dragging === k && "opacity-50", over === k && dragging !== k && "shadow-[inset_0_2px_0_var(--accent)]")}
            >
              <span aria-hidden className="cursor-grab text-ink-3 active:cursor-grabbing" title={t("settings.sidebar_pages_drag")}>
                <Icon name="menu" size={14} />
              </span>
              <span className="flex size-7 shrink-0 items-center justify-center rounded-control bg-inset text-ink-2 shadow-hairline">
                <Icon name={p.icon} size={14} />
              </span>
              <div className="min-w-0 flex-1">
                <div className="truncate text-[13px] text-ink">{p.label}</div>
                <div className="mt-0.5 truncate text-[11.5px] text-ink-3">
                  {locked ? t("settings.sidebar_pages_locked") : on ? t("settings.sidebar_pages_in_list") : t("settings.sidebar_pages_in_more")}
                  {from && ` · ${from}`}
                </div>
              </div>
              <IconButton icon="up" label={t("settings.sidebar_pages_up", { name: p.label })} disabled={i === 0} onClick={() => movePage(keys, k, -1)} size={26} iconSize={13} />
              <IconButton icon="arrowDown" label={t("settings.sidebar_pages_down", { name: p.label })} disabled={i === keys.length - 1} onClick={() => movePage(keys, k, 1)} size={26} iconSize={13} />
              <Switch on={on} disabled={locked} onChange={(v) => setPageShown(k, v)} label={t("settings.sidebar_pages_show", { name: p.label })} />
            </li>
          );
        })}
      </ul>
    </Group>
  );
}
