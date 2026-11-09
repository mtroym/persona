// Settings › Connectors (iOS ConnectorsSection, docs/extensions/mail.md): where Persona's data comes from — the mail accounts (list, add
// one with a password or app password, delete), Lark (the Lark gateway through the hub, feature `lark`: read-only status), and the hub's other connectors
// (GitHub, Telegram, an extension's: GET /api/v1/connectors), each with a page of its own (HubConnector.tsx, `&connector=`).
// "This iPhone" (phone_* tools) is the phone's own and has no place in a browser; Gmail signs in with Google through the
// phone app (its OAuth client is an iOS one whose callback only the app can catch), so the web form leaves it to the phone,
// unless the hub has no such client (GET /mail/oauth/google/config): then Gmail goes in here with an app password.
// Outlook too (Microsoft calls back to a custom scheme only the app catches), though its client ID can be set up here.
import { useEffect, useId, useState } from "react";
import { parseOwnedSub } from "../../lib/manager-choice";
import { clientIDError, EMPTY_MAIL_FORM, MAIL_KINDS, mailAccountLine, mailCodeGuide, mailErrors, mailProviderBadge, mailRequest, mailSummary, signsInOnPhone, type MailAccount, type MailForm, type MicrosoftConfig } from "../../lib/agent-settings";
import { t } from "../../i18n";
import { api, enc, errorText } from "../../lib/api";
import { ago } from "../../lib/format";
import { accountTitle, type LarkAccount, type LarkAccountList } from "../../lib/lark-accounts";
import { useLarkOn, useStore } from "../../lib/store";
import { Button, copyText, cx, Dialog, IconButton, Loading, Spinner, toast } from "../../ui/atoms";
import { Icon, type IconName } from "../../ui/icons";
import { Count, Monogram } from "../inbox/InboxParts";
import { ConnectorDetail, ConnectorList } from "./HubConnector";
import { LarkDisplaySettings } from "./LarkDisplaySettings";
import { LarkHomeSwitch } from "./LarkHomeSwitch";
import { Group, Pill, RowConfirm } from "./SettingsParts";
import { FeishuBot } from "./FeishuBot";

interface LarkStatus {
  configured: boolean;
  connected: boolean;
  name?: string;
  error?: string;
  last_event_at?: number;
}

export function Connectors({ open, onOpen, onChannel }: { open: string | null; onOpen: (id: string | null) => void; onChannel?: (id: string, manager?: string) => void }) {
  // An older hub has no connectors API (feature "connectors"): only mail and Feishu, as before.
  const hasConnectors = useStore((s) => s.features.includes("connectors"));
  // Lark (the Lark gateway through /api/v1/lark/…) only on a hub that has it (feature `lark`); "Lark bot" is the hub's own.
  const lark = useLarkOn();
  // "feishu-bot" is not a hub connector: FeishuBot reads it and opens its own dialog.
  // `<id>@<manager>`: another manager's connector, opened from its extension's page (docs/assistant/managers.md "Clients")
  if (open && open !== "feishu-bot") {
    const { id, manager } = parseOwnedSub(open);
    return <ConnectorDetail key={open} id={id} manager={manager} onBack={() => onOpen(null)} onChannel={onChannel} />;
  }
  return (
    <>
      <p className="mb-5 px-1 text-[12.5px] leading-relaxed text-ink-3">
        {t("settings-connectors.intro")}
      </p>
      <Mail />
      {lark && <Feishu />}
      <FeishuBot onChannel={onChannel} />
      {hasConnectors && <ConnectorList onOpen={onOpen} />}
    </>
  );
}

// ── Mail ────────────────────────────────────────────────

function Mail() {
  const [accounts, setAccounts] = useState<MailAccount[] | null>(null);
  const [error, setError] = useState("");
  const [adding, setAdding] = useState(false);
  const [confirm, setConfirm] = useState<string | null>(null);
  const [deleting, setDeleting] = useState<string | null>(null);

  const load = () =>
    api<{ accounts: MailAccount[] }>("/mail/accounts")
      .then((r) => (setAccounts(r.accounts ?? []), setError("")))
      .catch((e) => setError(errorText(e)));
  useEffect(() => void load(), []);

  const remove = async (a: MailAccount) => {
    setDeleting(a.id);
    try {
      await api(`/mail/accounts/${enc(a.id)}`, { method: "DELETE" });
      toast(t("settings-connectors.mail_deleted", { email: a.email }), "ok");
      setConfirm(null);
      await load();
    } catch (e) {
      toast(t("settings-connectors.delete_failed", { error: errorText(e) }), "error");
    } finally {
      setDeleting(null);
    }
  };

  return (
    <>
      <Group
        anchor="mail"
        title={
          <span className="flex items-center gap-2">
            {t("settings-connectors.mail")}
            {accounts && <span className="font-normal">{mailSummary(accounts)}</span>}
          </span>
        }
        right={
          <Button tone="quiet" icon="plus" onClick={() => setAdding(true)} className="-mb-1 h-7">
            {t("settings-connectors.add_mail")}
          </Button>
        }
        footer={t("settings-connectors.mail_footer")}
      >
        {!accounts && !error && (
          <div className="px-4 py-3">
            <Loading />
          </div>
        )}
        {error && <p className="px-4 py-3 text-[12.5px] text-red">{t("settings-connectors.mail_load_failed", { error })}</p>}
        {accounts?.length === 0 && <p className="px-4 py-3 text-[12.5px] text-ink-3">{t("settings-connectors.mail_empty")}</p>}
        {accounts?.map((a) => (
          <div key={a.id} className="flex items-center gap-3 px-4 py-2.5">
            <span className={cx("flex size-8 shrink-0 items-center justify-center rounded-[8px]", a.status === "error" ? "bg-red-tint text-red" : "bg-accent-tint text-accent-ink")}>
              <Icon name="mail" size={15} />
            </span>
            <div className="min-w-0 flex-1">
              <div className="flex min-w-0 items-center gap-2">
                <span className="truncate text-[13px] text-ink">{a.email}</span>
                {mailProviderBadge(a) && <Pill>{mailProviderBadge(a)}</Pill>}
                {a.status === "error" && <Pill tone="red">{t("settings-connectors.has_error")}</Pill>}
              </div>
              <div className={cx("mt-0.5 truncate text-[11.5px]", a.status === "error" ? "text-red" : "text-ink-3")} title={mailAccountLine(a)}>
                {mailAccountLine(a)}
              </div>
            </div>
            <RowConfirm
              open={confirm === a.id}
              confirm={t("settings-connectors.confirm_delete")}
              busy={deleting === a.id}
              onCancel={() => setConfirm(null)}
              onConfirm={() => void remove(a)}
              trigger={<IconButton icon="trash" label={t("settings-connectors.delete_account", { email: a.email })} onClick={() => setConfirm(a.id)} />}
            />
          </div>
        ))}
      </Group>
      <AddMail
        open={adding}
        onClose={() => setAdding(false)}
        onAdded={(email) => {
          setAdding(false);
          toast(t("settings-connectors.mail_added", { email }), "ok");
          void load();
        }}
      />
    </>
  );
}

const fieldClass = "h-9 w-full rounded-control bg-field px-3 text-[13px] text-ink outline-none placeholder:text-ink-3 focus:shadow-[inset_0_0_0_1px_var(--accent)] disabled:opacity-60";

// "Add mail account" (iOS AddMailAccountView): Lark or any IMAP server with a password / app password; the hub logs in to IMAP and SMTP
// before it saves, so a wrong password comes back as an error here and nothing is stored.
function AddMail({ open, onClose, onAdded }: { open: boolean; onClose: () => void; onAdded: (email: string) => void }) {
  const [f, setF] = useState<MailForm>(EMPTY_MAIL_FORM);
  const [tried, setTried] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const errorId = useId(); // field k's error line is `${errorId}-${k}`
  // false: this hub has no Google OAuth client, so Gmail goes in with an app password. An older hub (no such route)
  // or a failed read: as before, Gmail on the phone.
  const [googleSignIn, setGoogleSignIn] = useState(true);
  useEffect(() => {
    if (open) {
      setF(EMPTY_MAIL_FORM);
      setTried(false);
      setError("");
      api<{ configured: boolean }>("/mail/oauth/google/config")
        .then((r) => setGoogleSignIn(r.configured !== false))
        .catch(() => setGoogleSignIn(true));
    }
  }, [open]);

  const gmailAppPassword = f.kind === "gmail" && !googleSignIn;
  const errors = mailErrors(f, gmailAppPassword);
  const req = mailRequest(f, gmailAppPassword);
  const guide = mailCodeGuide(f);
  const shown = tried ? errors : {};
  const set = (k: keyof MailForm) => (e: React.ChangeEvent<HTMLInputElement>) => setF((x) => ({ ...x, [k]: e.target.value }));

  const save = async () => {
    setTried(true);
    if (!req || Object.keys(errors).length) return;
    setBusy(true);
    setError("");
    try {
      await api("/mail/accounts", { method: "POST", json: req });
      onAdded(req.email);
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };

  const field = (k: keyof MailForm, label: string, opts: { placeholder?: string; type?: string; mono?: boolean; auto?: string } = {}) => (
    <label className="block min-w-0">
      <span className="mb-1 block text-[12px] font-medium text-ink-2">{label}</span>
      <input
        value={f[k]}
        onChange={set(k)}
        type={opts.type ?? "text"}
        placeholder={opts.placeholder}
        autoComplete={opts.auto ?? "off"}
        spellCheck={false}
        disabled={busy}
        aria-invalid={!!shown[k]}
        aria-describedby={shown[k] ? `${errorId}-${k}` : undefined}
        className={cx(fieldClass, opts.mono && "font-mono", shown[k] ? "shadow-[inset_0_0_0_1px_var(--red)]" : "shadow-inset-field")}
      />
      {shown[k] && <span id={`${errorId}-${k}`} className="mt-1 block text-[11px] text-red">{shown[k]}</span>}
    </label>
  );

  return (
    <Dialog
      open={open}
      onClose={() => !busy && onClose()}
      width={460}
      title={t("settings-connectors.add_mail_title")}
      footer={
        <>
          <Button tone="quiet" onClick={onClose} disabled={busy}>
            {t("common.cancel")}
          </Button>
          {(!signsInOnPhone(f.kind) || gmailAppPassword) && (
            <Button tone="primary" busy={busy} onClick={() => void save()}>
              {busy ? t("settings-connectors.connecting") : t("settings-connectors.connect")}
            </Button>
          )}
        </>
      }
    >
      <div className="flex flex-col gap-3">
        <div className="flex gap-0.5 rounded-control bg-inset p-0.5 shadow-hairline" role="radiogroup" aria-label={t("settings-connectors.mail_kind")}>
          {MAIL_KINDS.map((k) => (
            <button
              key={k.k}
              type="button"
              role="radio"
              aria-checked={f.kind === k.k}
              disabled={busy}
              onClick={() => (setF((x) => ({ ...x, kind: k.k })), setTried(false), setError(""))}
              className={cx(
                "flex h-7 flex-1 items-center justify-center whitespace-nowrap rounded-[7px] px-2.5 text-[12.5px] transition-[background-color,color] duration-150",
                f.kind === k.k ? "bg-surface font-medium text-ink shadow-btn" : "text-ink-3 hover:text-ink-2",
              )}
            >
              {k.label}
            </button>
          ))}
        </div>

        {f.kind === "gmail" && !gmailAppPassword ? (
          <Note title={t("settings-connectors.gmail_title")} body={t("settings-connectors.gmail_body")} />
        ) : f.kind === "outlook" ? (
          <>
            <Note title={t("settings-connectors.outlook_title")} body={t("settings-connectors.outlook_body")} />
            <OutlookClient />
          </>
        ) : (
          <form
            className="flex flex-col gap-3"
            onSubmit={(e) => {
              e.preventDefault();
              void save();
            }}
          >
            {gmailAppPassword && <Note title={t("settings-connectors.gmail_not_configured_title")} body={t("settings-connectors.gmail_not_configured_body")} />}
            {field("email", gmailAppPassword ? t("settings-connectors.gmail_address") : t("settings-connectors.email"), { placeholder: "me@example.com", type: "email", auto: "email" })}
            {field("password", t(f.kind === "feishu" ? "settings-connectors.password_lark" : gmailAppPassword ? "settings-connectors.gmail_app_password" : "settings-connectors.password"), { type: "password", auto: "new-password" })}
            {guide ? (
              <div className="flex flex-col items-start gap-1.5 text-[11.5px] leading-relaxed text-ink-3">
                <p>{t(guide.provider === "qq" ? "settings-connectors.qq_code_steps" : "settings-connectors.netease_code_steps")}</p>
                <a href={guide.webMail} target="_blank" rel="noopener noreferrer" className="whitespace-nowrap font-medium text-accent-ink hover:underline">
                  {t(guide.provider === "qq" ? "settings-connectors.qq_open_web" : "settings-connectors.netease_open_web")}
                </a>
              </div>
            ) : (
              <p className="text-[11.5px] leading-relaxed text-ink-3">
                {t(f.kind === "feishu" ? "settings-connectors.lark_hint" : gmailAppPassword ? "settings-connectors.gmail_app_password_hint" : "settings-connectors.imap_hint")}
              </p>
            )}
            {f.kind === "imap" && (
              <fieldset className="flex flex-col gap-3 rounded-[10px] bg-inset p-3 shadow-hairline">
                <legend className="sr-only">{t("settings-connectors.servers")}</legend>
                <p className="text-[12px] font-medium text-ink-2">{t("settings-connectors.servers_optional")}</p>
                <div className="grid grid-cols-[1fr_88px] gap-2">
                  {field("imapHost", t("settings-connectors.imap_server"), { placeholder: "imap.example.com", mono: true })}
                  {field("imapPort", t("settings-connectors.port"), { placeholder: "993", mono: true })}
                  {field("smtpHost", t("settings-connectors.smtp_server"), { placeholder: "smtp.example.com", mono: true })}
                  {field("smtpPort", t("settings-connectors.port"), { placeholder: "465", mono: true })}
                </div>
                <p className="text-[11px] text-ink-3">{t("settings-connectors.smtp_tls")}</p>
              </fieldset>
            )}
            {error && <p className="rounded-[8px] bg-red-tint px-2.5 py-1.5 text-[12px] break-words text-red">{t("settings-connectors.add_failed", { error })}</p>}
            <button type="submit" hidden />
          </form>
        )}
      </div>
    </Dialog>
  );
}

/** Gmail's and Outlook's note in "Add mail account": they sign in on the provider's page, which returns to the phone app. */
function Note({ title, body }: { title: string; body: string }) {
  return (
    <div className="flex gap-3 rounded-[10px] bg-inset px-3.5 py-3 shadow-hairline">
      <Icon name="info" size={15} className="mt-0.5 shrink-0 text-ink-3" />
      <div className="text-[12.5px] leading-relaxed text-ink-2">
        <p className="font-medium text-ink">{title}</p>
        <p className="mt-1">{body}</p>
      </div>
    </div>
  );
}

const OUTLOOK_STEPS = [1, 2, 3, 4, 5, 6].map((n) => `settings-connectors.outlook_step${n}`);

/**
 * Outlook's client ID (iOS OutlookSignInSection's registration): the app registered in Microsoft Entra that this hub
 * signs in with. Once the hub has one this says so (and where it came from) with "Change"; else the steps, the callback to
 * register and the field. The sign-in itself stays on the phone.
 */
export function OutlookClient() {
  const [config, setConfig] = useState<MicrosoftConfig | null>(null);
  const [failed, setFailed] = useState("");
  const [editing, setEditing] = useState(false);
  const [id, setId] = useState("");
  const [tried, setTried] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const problemId = useId();
  useEffect(() => {
    api<MicrosoftConfig>("/mail/oauth/microsoft/config").then(setConfig, (e) => setFailed(errorText(e)));
  }, []);

  const problem = tried ? clientIDError(id) : "";
  const save = async () => {
    setTried(true);
    if (clientIDError(id)) return;
    setBusy(true);
    setError("");
    try {
      const c = await api<MicrosoftConfig>("/mail/oauth/microsoft/config", { method: "PUT", json: { client_id: id.trim() } });
      setConfig(c);
      setEditing(false);
      toast(t("settings-connectors.client_id_saved"), "ok");
    } catch (e) {
      setError(t("settings-connectors.client_id_save_failed", { error: errorText(e) }));
    } finally {
      setBusy(false);
    }
  };

  if (failed) return <p className="rounded-[8px] bg-red-tint px-2.5 py-1.5 text-[12px] break-words text-red">{t("settings-connectors.outlook_config_failed", { error: failed })}</p>;
  if (!config) return <div className="flex justify-center py-2"><Spinner /></div>;
  if (config.configured && !editing) {
    const source = config.source === "config" ? t("settings-connectors.outlook_source_config") : config.source === "app" ? t("settings-connectors.outlook_source_app") : "";
    return (
      <div className="flex items-center gap-3 rounded-[10px] bg-inset px-3.5 py-2.5 shadow-hairline">
        <Icon name="check" size={14} stroke={2.4} className="shrink-0 text-green" />
        <div className="min-w-0 flex-1">
          <p className="text-[12.5px] leading-snug text-ink">{t("settings-connectors.outlook_ready")}</p>
          <p className="mt-0.5 truncate font-mono text-[11px] text-ink-3" title={config.client_id}>
            {config.client_id}
          </p>
          {source && <p className="mt-0.5 truncate text-[11px] text-ink-3">{source}</p>}
        </div>
        <Button tone="quiet" onClick={() => (setId(config.client_id ?? ""), setTried(false), setError(""), setEditing(true))}>
          {t("settings-connectors.outlook_change")}
        </Button>
      </div>
    );
  }
  return (
    <form
      className="flex flex-col gap-3 rounded-[10px] bg-inset p-3 shadow-hairline"
      onSubmit={(e) => {
        e.preventDefault();
        void save();
      }}
    >
      <div>
        <p className="text-[12px] font-medium text-ink-2">{t("settings-connectors.outlook_register_title")}</p>
        <p className="mt-1 text-[11.5px] leading-relaxed text-ink-3">{t("settings-connectors.outlook_register_intro")}</p>
        <ol className="mt-1 list-decimal pl-5 text-[11.5px] leading-relaxed text-ink-3">
          {OUTLOOK_STEPS.map((k) => <li key={k}>{t(k)}</li>)}
        </ol>
      </div>
      <div className="min-w-0">
        <span className="mb-1 block text-[12px] font-medium text-ink-2">{t("settings-connectors.redirect_uri")}</span>
        <div className="flex items-center gap-1">
          <code className="h-9 min-w-0 flex-1 truncate rounded-control bg-field px-3 font-mono text-[12.5px] leading-9 text-ink shadow-inset-field select-all">{config.redirect_uri}</code>
          <IconButton icon="copy" label={t("settings-connectors.copy_redirect")} onClick={() => void copyText(config.redirect_uri)} />
        </div>
      </div>
      <label className="block min-w-0">
        <span className="mb-1 block text-[12px] font-medium text-ink-2">{t("settings-connectors.client_id")}</span>
        <input
          value={id}
          onChange={(e) => setId(e.target.value)}
          placeholder="xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx"
          autoComplete="off"
          spellCheck={false}
          disabled={busy}
          aria-invalid={!!problem}
          aria-describedby={problem ? problemId : undefined}
          className={cx(fieldClass, "font-mono", problem ? "shadow-[inset_0_0_0_1px_var(--red)]" : "shadow-inset-field")}
        />
        {problem && <span id={problemId} className="mt-1 block text-[11px] text-red">{problem}</span>}
      </label>
      {error && <p className="rounded-[8px] bg-red-tint px-2.5 py-1.5 text-[12px] break-words text-red">{error}</p>}
      <div className="flex justify-end gap-2">
        {config.configured && (
          <Button tone="quiet" disabled={busy} onClick={() => setEditing(false)}>
            {t("settings-connectors.outlook_change_cancel")}
          </Button>
        )}
        <Button tone="primary" type="submit" busy={busy}>
          {t("settings-connectors.save_client_id")}
        </Button>
      </div>
    </form>
  );
}

// ── Lark ────────────────────────────────────────────────

const USES: { icon: IconName; title: string; detail: string }[] = [
  { icon: "inbox", title: t("settings-connectors.use_inbox"), detail: t("settings-connectors.use_inbox_detail") },
  { icon: "book", title: t("settings-connectors.use_knowledge"), detail: t("settings-connectors.use_knowledge_detail") },
  { icon: "spark", title: t("settings-connectors.use_assistant"), detail: t("settings-connectors.use_assistant_detail") },
  { icon: "agent", title: "/agent", detail: t("settings-connectors.use_agent_detail") },
];

function LarkAccountRow({ a, isDefault }: { a: LarkAccount; isDefault: boolean }) {
  const detail = a.quarantined ? t("settings-connectors.account_quarantined") : !a.signed_in ? t("settings-connectors.signed_out") : a.tenant || t("settings-connectors.signed_in");
  return (
    <div data-account={a.id} className="flex items-center gap-3 px-4 py-2.5">
      <Monogram name={accountTitle(a)} size={30} src={a.avatar} />
      <div className="min-w-0 flex-1">
        <div className="truncate text-[13px] text-ink">{accountTitle(a)}</div>
        <div className={cx("mt-0.5 truncate text-[11.5px]", a.signed_in ? "text-ink-3" : "text-red")}>{detail}</div>
      </div>
      {isDefault && <Pill>{t("settings-connectors.default")}</Pill>}
      {a.signed_in && a.unread > 0 && <Count n={a.unread} />}
    </div>
  );
}

function Feishu() {
  const [st, setSt] = useState<LarkStatus | null>(null);
  const [accounts, setAccounts] = useState<LarkAccountList | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const load = () => {
    setLoading(true);
    // the Feishu accounts on the hub's Lark gateway (docs/extensions/feishu.md "Multiple accounts"); a hub from before them has none to list
    api<LarkAccountList>("/lark/accounts").then(setAccounts).catch(() => setAccounts(null));
    api<LarkStatus>("/lark/status")
      .then((r) => (setSt(r), setError("")))
      .catch((e) => setError(errorText(e)))
      .finally(() => setLoading(false));
  };
  useEffect(load, []);

  const state = !st ? (loading ? t("settings-connectors.checking") : t("settings-connectors.status_unknown")) : !st.configured ? t("settings-connectors.not_configured") : st.connected ? t("settings-connectors.connected") : t("settings-connectors.unreachable");
  const dot = !st ? "bg-line-strong" : st.connected ? "bg-green" : st.configured ? "bg-red" : "bg-line-strong";
  const last = st?.last_event_at ? ago(new Date(st.last_event_at * 1000).toISOString()) : "";

  return (
    <Group
      anchor="lark"
      title={t("settings-connectors.lark")}
      footer={st?.configured === false ? t("settings-connectors.lark_footer_unconfigured") : t("settings-connectors.lark_footer")}
    >
      <div className="flex items-center gap-3 px-4 py-3">
        <span className="flex size-9 shrink-0 items-center justify-center rounded-[9px] bg-accent-tint text-accent-ink">
          <Icon name="chat" size={16} />
        </span>
        <div className="min-w-0 flex-1">
          <div className="truncate text-[13.5px] font-medium text-ink">{st?.name || t("settings-connectors.lark")}</div>
          <div className="mt-0.5 flex items-center gap-1.5 text-[11.5px] text-ink-3">
            <span className={cx("size-1.5 shrink-0 rounded-full", dot)} />
            <span className="truncate">{state}</span>
          </div>
        </div>
        <IconButton icon="retry" label={t("settings-connectors.recheck")} onClick={load} disabled={loading} />
      </div>
      {last && (
        <div className="flex items-center gap-3 px-4 py-2.5">
          <span className="min-w-0 flex-1 truncate text-[13px] text-ink">{t("settings-connectors.last_event")}</span>
          <span className="shrink-0 text-[12.5px] text-ink-2">{last}</span>
        </div>
      )}
      {(st?.error || error) && <p className="px-4 py-2.5 text-[12px] break-words text-red">{st?.error || t("settings-connectors.lark_status_failed", { error })}</p>}
      {accounts && accounts.accounts.length > 1 && (
        <>
          <div className="px-4 pt-2.5 pb-1 text-[11.5px] font-medium text-ink-3">{t("settings-connectors.accounts")}</div>
          {accounts.accounts.map((a) => <LarkAccountRow key={a.id} a={a} isDefault={a.id === accounts.default} />)}
          <p className="px-4 pt-1 pb-2.5 text-[11.5px] leading-snug text-ink-3">{t("settings-connectors.accounts_note")}</p>
        </>
      )}
      <LarkHomeSwitch />
      <LarkDisplaySettings />
      <div className="px-4 pt-2.5 pb-1 text-[11.5px] font-medium text-ink-3">{t("settings-connectors.lark_uses")}</div>
      {USES.map((u) => (
        <div key={u.title} className="flex items-start gap-3 px-4 py-2.5">
          <Icon name={u.icon} size={15} className="mt-0.5 shrink-0 text-accent" />
          <div className="min-w-0">
            <div className="text-[13px] text-ink">{u.title}</div>
            <div className="mt-0.5 text-[11.5px] leading-snug text-ink-3">{u.detail}</div>
          </div>
        </div>
      ))}
    </Group>
  );
}
