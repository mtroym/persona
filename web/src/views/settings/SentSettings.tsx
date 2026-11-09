// Settings › Connectors › Records sent as you (#2412, docs/extensions/connectors.md "Sent as the user", docs/web/web.md "Settings"): what Persona wrote in the
// user's name through the connectors, newest first, from GET /api/v1/connectors/sent. A row: the connector, where it went,
// how long ago, the first lines of what it said and how the gate let it through (a rule, "always allow" in the chat, the
// card on the phone, the assistant answering that card, or the extension sending on its own) with the chat that asked,
// which opens. A row opens its full text and fields. Older rows load a page at a time; the connector chips filter (Lark and
// the connectors the loaded rows show).
import { Fragment, useCallback, useEffect, useRef, useState, useSyncExternalStore } from "react";
import { sentOwner, subscribeSentOwner } from "../../lib/sent-chat-rules";
import { SentChatRules } from "./SentChatRules";
import { formatNumber, t } from "../../i18n";
import { ago } from "../../lib/format";
import { conversationRoute } from "../../lib/inbox";
import { href, onLinkClick } from "../../lib/router";
import {
  allowedBy, connectorChoices, connectorName, listSent, mergeSent, originInfo, previewLines, refreshed, seenConnectors, SENT_TEXT_MOST,
  sentProblem, targetFields, targetText, textCut, whenText, type SentItem,
} from "../../lib/sent";
import { useStore } from "../../lib/store";
import { documentPage, visiblePoll } from "../../lib/visible-poll";
import { Button, copyText, cx, Dialog, Loading } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { Chip } from "../inbox/InboxParts";
import { Group, Pill } from "./SettingsParts";

/** One filter's reading of the ledger: a response that arrives after the filter changed (or the page left) is dropped by `live`. */
interface Reading {
  filter: string | null;
  live: boolean;
  /** Older pages were loaded: a refresh of the first page merges instead of replacing. */
  older: boolean;
  /** The first page has come in once. */
  has: boolean;
  /** Reads of the first page started: only the latest one is taken, whatever order they come back in. */
  seq: number;
  blocked: boolean;
  moreBusy: boolean;
  refresh: () => Promise<void>;
  controllers: Set<AbortController>;
}

export function SentSettings() {
  const owner = useSyncExternalStore(subscribeSentOwner, sentOwner, sentOwner);
  return <OwnedSentSettings key={owner} />;
}

function OwnedSentSettings() {
  const owner = useRef(sentOwner()).current;
  const current = () => owner === sentOwner();
  const [filter, setFilter] = useState<string | null>(null);
  const [items, setItems] = useState<SentItem[] | null>(null);
  // what the list holds now, for the reads that come in later to merge with
  const held = useRef<SentItem[] | null>(null);
  const [next, setNext] = useState<number | null>(null);
  // `next` as the list holds it now: a read of older rows is taken only for the cursor it was asked with (a refresh that
  // started the list over meanwhile moved it, and the rows read would no longer join what is shown)
  const cursor = useRef<number | null>(null);
  const setCursor = (n: number | null) => {
    cursor.current = n;
    setNext(n);
  };
  const [problem, setProblem] = useState<{ kind: string; text: string } | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);
  const [moreError, setMoreError] = useState("");
  const [attempt, setAttempt] = useState(0);
  const [open, setOpen] = useState<SentItem | null>(null);
  const [seen, setSeen] = useState<string[]>([]);
  const reading = useRef<Reading | null>(null);

  // One reading per filter: the list starts over when it changes (what comes in for the old one is dropped), and the
  // first page is read again each time the page becomes visible.
  useEffect(() => {
    const r: Reading = { filter, live: true, older: false, has: false, seq: 0, blocked: false, moreBusy: false, refresh: async () => {}, controllers: new Set() };
    reading.current = r;
    held.current = null;
    setItems(null);
    setCursor(null);
    setProblem(null);
    setMoreError("");
    setLoadingMore(false);
    const refresh = async () => {
      if (!r.live || !current() || r.blocked) return;
      const mine = ++r.seq;
      const controller = new AbortController(); r.controllers.add(controller);
      return listSent({ connector: filter, signal: controller.signal })
        .then((page) => {
          if (!r.live || !current() || r.blocked || mine !== r.seq) return;
          r.has = true;
          setProblem(null);
          setSeen((s) => seenConnectors(s, page.items));
          const out = refreshed(held.current ?? [], page, r.older);
          r.older = out.loadedOlder;
          held.current = out.items;
          setItems(out.items);
          setOpen(old => old ? out.items.find(it => it.id === old.id) ?? old : null);
          if (out.next !== undefined) setCursor(out.next);
        })
        .catch((e) => {
          // a list that is there stays when a later read fails; only the first read's failure is shown
          if (r.live && current() && mine === r.seq) {
            if (!r.has) setProblem(sentProblem(e));
            throw e;
          }
        }).finally(() => r.controllers.delete(controller));
    };
    r.refresh = refresh;
    const stop = visiblePoll(() => { void refresh().catch(() => {}); }, 0, documentPage);
    return () => {
      r.live = false;
      r.controllers.forEach(c => c.abort());
      stop();
    };
  }, [filter, attempt]);

  const loadMore = useCallback(() => {
    const r = reading.current;
    if (!r || !r.live || !current() || r.blocked || next === null || r.moreBusy) return;
    r.moreBusy = true;
    const seq = r.seq;
    const asked = next;
    setLoadingMore(true);
    setMoreError("");
    const controller = new AbortController(); r.controllers.add(controller);
    listSent({ before: asked, connector: r.filter, signal: controller.signal })
      .then((page) => {
        if (!r.live || !current() || r.blocked || seq !== r.seq || cursor.current !== asked) return;
        r.older = true;
        setSeen((s) => seenConnectors(s, page.items));
        held.current = mergeSent(held.current ?? [], page.items);
        setItems(held.current);
        setCursor(page.next);
      })
      .catch((e) => r.live && current() && seq === r.seq && setMoreError(sentProblem(e).text))
      .finally(() => { r.controllers.delete(controller); if (r.live && current()) { r.moreBusy = false; setLoadingMore(false); } });
  }, [next, loadingMore]);

  const choices = connectorChoices(seen, filter);
  const nameOf = connectorName;
  // Nothing to filter by while the ledger is empty and every connector is shown, or when the unfiltered list can't be
  // read; under a filter the chips stay, so a failed read can be left by choosing another one or All
  const chips = filter !== null || (!problem && seen.length > 0);

  let body: React.ReactNode;
  if (problem) {
    body = (
      <Group>
        <div className="flex flex-col items-start gap-3 px-4 py-3">
          <p role={problem.kind === "error" ? "alert" : undefined} className={cx("text-[12.5px] leading-relaxed", problem.kind === "error" ? "text-red" : "text-ink-3")}>{problem.text}</p>
          {problem.kind === "error" && (
            <Button icon="retry" onClick={() => setAttempt((n) => n + 1)}>
              {t("settings.retry")}
            </Button>
          )}
        </div>
      </Group>
    );
  } else if (!items) {
    body = <Loading />;
  } else if (!items.length) {
    body = (
      <Group>
        <div className="flex flex-col items-start gap-2 px-4 py-4 text-[12.5px] leading-relaxed text-ink-3">
          {filter ? (
            <>
              <p className="text-ink-2">{t("settings-sent.empty_filtered", { name: nameOf(filter) })}</p>
              <Button tone="quiet" onClick={() => setFilter(null)}>
                {t("settings-sent.show_all")}
              </Button>
            </>
          ) : (
            <>
              <p className="text-[13px] font-medium text-ink">{t("settings-sent.empty_title")}</p>
              <p>{t("settings-sent.empty_recorded")}</p>
              <p>{t("settings-sent.empty_not_recorded")}</p>
              <p>{t("settings-sent.empty_days")}</p>
            </>
          )}
        </div>
      </Group>
    );
  } else {
    body = (
      <>
        <Group footer={t("settings-sent.intro")}>
          {items.map((it) => (
            <SentRow key={it.id} item={it} name={nameOf(it.connector)} onOpen={() => setOpen(it)} />
          ))}
        </Group>
        {next !== null && (
          <div className="-mt-2 mb-6 flex flex-col items-center gap-2">
            <Button tone="quiet" busy={loadingMore} onClick={loadMore}>
              {t("settings-sent.load_earlier")}
            </Button>
            {moreError && <p role="alert" className="px-1 text-[12.5px] text-red">{moreError}</p>}
          </div>
        )}
      </>
    );
  }

  return (
    <>
      {chips && (
        <div role="group" aria-label={t("settings-sent.filter_label")} className="mb-4 flex flex-wrap gap-1.5">
          <Chip on={filter === null} onClick={() => setFilter(null)}>
            {t("settings-sent.all")}
          </Chip>
          {choices.map((c) => (
            <Chip key={c.id} on={filter === c.id} onClick={() => setFilter(filter === c.id ? null : c.id)}>
              {c.name}
            </Chip>
          ))}
        </div>
      )}
      {body}
      <SentChatRules item={open} onChanging={() => {
        const r = reading.current;
        if (r) { r.blocked = true; r.seq++; r.older = false; r.controllers.forEach(c => c.abort()); }
      }} onChanged={async () => {
        const r = reading.current;
        if (r && current()) { r.blocked = false; await r.refresh(); }
      }}>
        {action => <SentDetail item={open} name={open ? nameOf(open.connector) : ""} onClose={() => setOpen(null)} action={action} />}
      </SentChatRules>
    </>
  );
}

function SentRow({ item, name, onOpen }: { item: SentItem; name: string; onOpen: () => void }) {
  const lines = previewLines(item);
  const allowed = allowedBy(item.let_through);
  const target = targetText(item);
  // what names the row for a screen reader (every row has the same "Open chat" link): the connector and where it went
  const what = target === "—" ? name : `${name} ${target}`;
  return (
    <div data-sent={item.id} className="relative px-4 py-3 transition-colors hover:bg-hover">
      {/* the whole row opens the details; the chat link below sits above it */}
      <button type="button" onClick={onOpen} className="block w-full text-left after:absolute after:inset-0 after:content-['']">
        <span className="flex min-w-0 items-baseline gap-2">
          <span className="shrink-0 text-[13px] font-medium text-ink">{name}</span>
          <span className="min-w-0 flex-1 truncate text-[12.5px] text-ink-2">{target}</span>
          {/* above the row's overlay so its title shows on hover; a click on it still reaches the button */}
          <time dateTime={item.at} title={whenText(item.at)} className="relative z-10 shrink-0 text-[11.5px] tabular-nums text-ink-3">
            {ago(item.at)}
          </time>
        </span>
        <span className="mt-1 block space-y-0.5">
          {lines.length ? (
            lines.map((l, i) => (
              <span key={i} className="block truncate text-[12.5px] text-ink">{l}</span>
            ))
          ) : (
            <span className="block text-[12.5px] text-ink-3">{t("settings-sent.no_text")}</span>
          )}
        </span>
      </button>
      <div className="pointer-events-none relative z-10 mt-2 flex flex-wrap items-center gap-x-2 gap-y-1">
        <Pill tone={allowed.tone === "orange" ? "orange" : undefined}>{allowed.label}</Pill>
        <Origin origin={item.origin} what={what} />
      </div>
    </div>
  );
}

/** Who asked: the chat (a link), "Extension-reported" with the extension when the row is one an extension recorded, or no chat. */
function Origin({ origin, what }: { origin: SentItem["origin"]; what: string }) {
  const { conversation, extension } = originInfo(origin);
  return (
    <>
      {extension && (
        <>
          <Pill>{t("settings-sent.origin_extension")}</Pill>
          <span className="min-w-0 truncate font-mono text-[11px] text-ink-3">{extension}</span>
        </>
      )}
      {conversation ? (
        <ChatLink mid={conversation.mid} sid={conversation.sid} what={what} />
      ) : (
        !extension && <span className="text-[11.5px] text-ink-3">{t("settings-sent.origin_none")}</span>
      )}
    </>
  );
}

/** `what`: which row's chat it is, for the link's name (its words are the same on every row); the details, which are one row, need none. */
function ChatLink({ mid, sid, what }: { mid: string; sid: string; what?: string }) {
  const to = useStore((s) => conversationRoute(mid, sid, s.sessions, s.assistant));
  return (
    <a
      href={href(to)}
      aria-label={what ? t("settings-sent.open_chat_row", { what }) : undefined}
      onClick={(e) => onLinkClick(e, to)}
      className="pointer-events-auto inline-flex items-center gap-0.5 whitespace-nowrap text-[11.5px] text-accent-ink hover:underline"
    >
      {t("settings-sent.open_chat")}
      <Icon name="chevronRight" size={10} stroke={2.2} />
    </a>
  );
}

/** A label and its value in the details. */
function Field({ label, children, mono }: { label: string; children: React.ReactNode; mono?: boolean }) {
  return (
    <div className="grid grid-cols-[88px_minmax(0,1fr)] items-baseline gap-3 px-3 py-2 text-[12.5px]">
      <span className="truncate text-ink-3" title={label}>{label}</span>
      <span className={cx("min-w-0 break-words text-ink", mono && "font-mono text-[12px]")}>{children}</span>
    </div>
  );
}

function SentDetail({ item, name, onClose, action }: { item: SentItem | null; name: string; onClose: () => void; action: React.ReactNode }) {
  return (
    <Dialog
      open={!!item}
      onClose={onClose}
      width={520}
      title={item && <span className="truncate">{name}</span>}
      footer={<Button tone="primary" onClick={onClose}>{t("settings-sent.done")}</Button>}
    >
      {item && <><DetailBody item={item} /><div className="mt-4">{action}</div></>}
    </Dialog>
  );
}

function DetailBody({ item }: { item: SentItem }) {
  const fields = targetFields(item.target);
  const allowed = allowedBy(item.let_through);
  const { conversation, extension } = originInfo(item.origin);
  const card = item.let_through.interaction_id;
  return (
    <div className="flex flex-col gap-4">
      <div className="divide-y divide-line overflow-hidden rounded-card bg-inset shadow-hairline">
        <Field label={t("settings-sent.d_sent")}>{whenText(item.at)}</Field>
        <Field label={t("settings-sent.d_action")} mono>{item.action}</Field>
        <Field label={t("settings-sent.d_target")}>
          {fields.length ? (
            <span className="grid grid-cols-[max-content_minmax(0,1fr)] gap-x-3 gap-y-1">
              {fields.map((f) => (
                <Fragment key={f.key}>
                  <span className="font-mono text-[11.5px] text-ink-3">{f.key}</span>
                  <span className="min-w-0 break-words">{f.value}</span>
                </Fragment>
              ))}
            </span>
          ) : (
            "—"
          )}
        </Field>
        <Field label={t("settings-sent.d_allowed")}>
          <span className="flex flex-col items-start gap-1">
            <Pill tone={allowed.tone === "orange" ? "orange" : undefined}>{allowed.label}</Pill>
            {allowed.detail && <span className="text-[12px] leading-relaxed text-ink-2">{allowed.detail}</span>}
          </span>
        </Field>
        {card && <Field label={t("settings-sent.d_card")} mono>{card}</Field>}
        {extension && <Field label={t("settings-sent.d_extension")}>{extension} <Pill>{t("settings-sent.origin_extension")}</Pill></Field>}
        <Field label={t("settings-sent.d_chat")}>
          {conversation ? <ChatLink mid={conversation.mid} sid={conversation.sid} /> : <span className="text-ink-3">{t("settings-sent.origin_none")}</span>}
        </Field>
        {item.message_ref && <Field label={t("settings-sent.d_message")} mono>{item.message_ref}</Field>}
      </div>
      <section>
        <div className="mb-1.5 flex items-center gap-2">
          <h3 className="flex-1 text-[12px] font-medium text-ink-3">{t("settings-sent.d_text")}</h3>
          {item.text && (
            <Button tone="quiet" icon="copy" onClick={() => void copyText(item.text)}>
              {t("settings-sent.copy_text")}
            </Button>
          )}
        </div>
        {item.text ? (
          <>
            <pre className="max-h-72 overflow-y-auto whitespace-pre-wrap break-words rounded-control bg-inset p-3 font-sans text-[12.5px] leading-relaxed text-ink shadow-hairline select-text">{item.text}</pre>
            {textCut(item.text) && <p className="mt-1.5 text-[11.5px] leading-snug text-ink-3">{t("settings-sent.text_cut", { count: formatNumber(SENT_TEXT_MOST) })}</p>}
          </>
        ) : (
          <p className="text-[12.5px] text-ink-3">{t("settings-sent.no_text")}</p>
        )}
      </section>
    </div>
  );
}
