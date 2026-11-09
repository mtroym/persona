import { describe, expect, it } from "vitest";
import { initI18n } from "../i18n";
import {
  AGENT_TEXT,
  applySummary,
  effective,
  EMPTY_MAIL_FORM,
  isUnset,
  mailAccountLine,
  mailCodeGuide,
  mailErrors,
  mailProviderBadge,
  clientIDError,
  MAIL_KINDS,
  mailRequest,
  mailSummary,
  optionText,
  pickMachine,
  settingDetail,
  settingTitle,
  textValue,
  valueText,
  withValue,
  dirMeta,
  looseRules,
  patternError,
  shortPath,
  toggledPatterns,
  withPattern,
  type AgentSetting,
  type ClaudeDirsState,
} from "./agent-settings";

const recap: AgentSetting = { key: "awaySummaryEnabled", title: "离开回顾", detail: "", type: "bool", default: true, value: null };
const effort: AgentSetting = { key: "effortLevel", title: "思考强度", detail: "", type: "enum", options: ["low", "high"], default: "high", value: "low" };

describe("agent settings", () => {
  it("read the value in effect and whether it is set", () => {
    expect(effective(recap)).toBe(true);
    expect(isUnset(recap)).toBe(true);
    expect(effective(effort)).toBe("low");
    expect(isUnset(effort)).toBe(false);
    expect(effective({ ...recap, default: undefined })).toBeNull();
  });

  it("change one key and roll it back", () => {
    const list = [recap, effort];
    const changed = withValue(list, "awaySummaryEnabled", false);
    expect(changed[0].value).toBe(false);
    expect(changed[1]).toBe(effort);
    expect(withValue(changed, "awaySummaryEnabled", null)[0].value).toBeNull();
  });

  it.each([
    ["en", "not applied", "Model configuration › Agents › Reasoning effort"],
    ["zh-Hans", "未生效", "模型配置 › Agents › 思考强度"],
    ["ja", "反映されていません", "モデル構成 › Agents › 思考レベル"],
  ])("explains ignored max in %s while keeping the model default selected", async (lang, warning, breadcrumb) => {
    try {
      await initI18n(lang);
      const ignored: AgentSetting = { ...effort, value: null, default: "auto", ignored_value: "max" };
      expect(effective(ignored)).toBe("auto");
      expect(isUnset(ignored)).toBe(true);
      expect(settingDetail(ignored)).toContain(warning);
      expect(settingDetail(ignored)).toContain(breadcrumb);
      expect(settingDetail(ignored)).toContain("/effort max");
      const changed = withValue([ignored, recap], "effortLevel", "high");
      expect(changed[0].ignored_value).toBeUndefined();
      expect(settingDetail(changed[0])).not.toContain(warning);
      expect(settingDetail(changed[0])).toContain("auto");
      expect(settingDetail(changed[0])).toContain(breadcrumb);
      expect(settingDetail(changed[0])).toContain("/effort max");
      expect(changed[1]).toBe(recap);
    } finally {
      await initI18n("zh-Hans");
    }
  });

  it("send an empty text field as back to the default", () => {
    expect(textValue("  中文 ")).toBe("中文");
    expect(textValue("   ")).toBeNull();
  });

  it("edit an online machine, the remembered one first", () => {
    const ms = [
      { id: "nas", online: false },
      { id: "server", online: true },
      { id: "macbook", online: true },
    ];
    expect(pickMachine(ms, "macbook")).toBe("macbook");
    expect(pickMachine(ms, "nas")).toBe("server");
    expect(pickMachine(ms, "")).toBe("server");
    expect(pickMachine([{ id: "nas", online: false }], "nas")).toBe("");
  });

  it("report a change sent to several machines", () => {
    expect(applySummary([{ id: "a", name: "A" }])).toEqual({ ok: true, text: "已保存" });
    expect(applySummary([{ id: "a", name: "A" }, { id: "b", name: "B" }, { id: "c", name: "C" }])).toEqual({ ok: true, text: "已应用到 3 台机器" });
    expect(applySummary([{ id: "a", name: "A" }, { id: "b", name: "NAS", error: "machine is offline" }])).toEqual({ ok: false, text: "1 台已应用，1 台失败（NAS：machine is offline）" });
    expect(applySummary([{ id: "b", name: "NAS", error: "x" }]).text).toBe("没有改成（NAS：x）");
  });

  it("name Claude Code's options in plain words, without the settings-file key (#3599)", () => {
    expect(settingTitle(recap)).toBe("回来时先说进展");
    expect(settingDetail(recap)).toContain("离开 5 分钟");
    expect(optionText("effortLevel", "xhigh")).toBe("很高");
    expect(optionText("autoUpdatesChannel", "latest")).toBe("最新版");
    // An option or key the web does not know stays as the node wrote it.
    expect(optionText("effortLevel", "ultra")).toBe("ultra");
    expect(optionText("approval_policy", "later")).toBe("later");
    const unknown: AgentSetting = { key: "newThing", title: "新东西", detail: "说明", type: "bool" };
    expect(settingTitle(unknown)).toBe("新东西");
    expect(settingDetail(unknown)).toBe("说明");
  });

  it("name Codex's options in words, not config.toml values (#3855)", () => {
    const options: Record<string, string[]> = {
      model_reasoning_effort: ["minimal", "low", "medium", "high", "xhigh", "max", "ultra"],
      model_reasoning_summary: ["auto", "concise", "detailed", "none"],
      model_verbosity: ["low", "medium", "high"],
      approval_policy: ["untrusted", "on-failure", "on-request", "never"],
      sandbox_mode: ["read-only", "workspace-write", "danger-full-access"],
    };
    for (const [key, list] of Object.entries(options)) {
      for (const o of list) expect(optionText(key, o), `${key}=${o}`).not.toBe(o);
    }
    expect(optionText("approval_policy", "never")).toBe("一律不问");
    expect(optionText("sandbox_mode", "danger-full-access")).toBe("不设限（危险）");
    const sandbox: AgentSetting = { key: "sandbox_mode", title: "沙箱", detail: "danger-full-access 不设限", type: "enum" };
    expect(settingDetail(sandbox)).not.toMatch(/[a-z]-[a-z]/);
  });

  it("say values in words", () => {
    expect(valueText(true)).toBe("开");
    expect(valueText(false)).toBe("关");
    expect(valueText(null)).toBe("默认");
    expect(valueText("high")).toBe("high");
    expect(valueText("high", "effortLevel")).toBe("高");
    expect(valueText("stable", "autoUpdatesChannel")).toBe("稳定版");
    expect(AGENT_TEXT.codex.path).toBe("codex-settings");
    expect(AGENT_TEXT.claude.footer).toContain("~/.claude/settings.json");
  });
});

describe("mail account form", () => {
  it("needs an address and a password; Gmail is for the phone", () => {
    expect(mailErrors({ ...EMPTY_MAIL_FORM })).toEqual({ email: "填邮箱地址", password: "填密码或授权码" });
    expect(mailErrors({ ...EMPTY_MAIL_FORM, email: "me@", password: "x" }).email).toBe("邮箱地址的格式不对");
    expect(mailErrors({ ...EMPTY_MAIL_FORM, email: "me@example.com", password: "x" })).toEqual({});
    expect(mailErrors({ ...EMPTY_MAIL_FORM, kind: "gmail" }).kind).toContain("手机");
    expect(mailErrors({ ...EMPTY_MAIL_FORM, kind: "outlook" }).kind).toBe("Outlook 要用 Microsoft 账号登录，只能在手机上添加");
    expect(MAIL_KINDS.map((k) => k.k)).toEqual(["gmail", "outlook", "feishu", "imap"]);
    expect(mailErrors({ ...EMPTY_MAIL_FORM, kind: "feishu", email: "me@example.com" }).password).toContain("飞书");
    expect(mailErrors({ ...EMPTY_MAIL_FORM, email: "me@example.com", password: "x", imapHost: "imap.example.com", imapPort: "abc" }).imapPort).toBeTruthy();
  });

  it("tells QQ and NetEase addresses where to make the authorization code (#5123)", () => {
    expect(mailCodeGuide({ ...EMPTY_MAIL_FORM, kind: "imap", email: "me@QQ.com " })).toEqual({ provider: "qq", webMail: "https://mail.qq.com" });
    expect(mailCodeGuide({ ...EMPTY_MAIL_FORM, kind: "imap", email: "me@foxmail.com" })?.provider).toBe("qq");
    expect(mailCodeGuide({ ...EMPTY_MAIL_FORM, kind: "imap", email: "me@126.com" })).toEqual({ provider: "netease", webMail: "https://mail.126.com" });
    expect(mailCodeGuide({ ...EMPTY_MAIL_FORM, kind: "imap", email: "me@vip.163.com" })?.webMail).toBe("https://vip.163.com");
    expect(mailCodeGuide({ ...EMPTY_MAIL_FORM, kind: "imap", email: "me@188.com" })?.webMail).toBe("https://www.188.com");
    expect(mailCodeGuide({ ...EMPTY_MAIL_FORM, kind: "imap", email: "me@example.com" })).toBeNull();
    expect(mailCodeGuide({ ...EMPTY_MAIL_FORM, kind: "imap", email: "me" })).toBeNull();
    expect(mailCodeGuide({ ...EMPTY_MAIL_FORM, kind: "feishu", email: "me@qq.com" })).toBeNull();
    expect(mailCodeGuide({ ...EMPTY_MAIL_FORM, email: "me@qq.com", imapHost: "imap.example.com" })).toBeNull();
  });

  it("builds the hub's request", () => {
    expect(mailRequest({ ...EMPTY_MAIL_FORM, kind: "gmail" })).toBeNull();
    expect(mailRequest({ ...EMPTY_MAIL_FORM, kind: "outlook", email: "me@example.com", password: "pw" })).toBeNull();
    expect(mailRequest({ ...EMPTY_MAIL_FORM, kind: "feishu", email: " me@example.com ", password: "pw", imapHost: "ignored" })).toEqual({
      name: "me@example.com",
      email: "me@example.com",
      provider: "feishu",
      auth: "password",
      password: "pw",
    });
    // servers left empty: the hub guesses them from the domain
    expect(mailRequest({ ...EMPTY_MAIL_FORM, email: "me@example.com", password: "pw" })).toEqual({ name: "me@example.com", email: "me@example.com", provider: "imap", auth: "password", password: "pw" });
    expect(mailRequest({ ...EMPTY_MAIL_FORM, email: "me@example.com", password: "pw", imapHost: "imap.example.com", imapPort: "993", smtpHost: "smtp.example.com", smtpPort: "587" })).toMatchObject({
      imap_host: "imap.example.com",
      imap_port: 993,
      smtp_host: "smtp.example.com",
      smtp_port: 587,
      smtp_security: "starttls",
    });
    expect(mailRequest({ ...EMPTY_MAIL_FORM, email: "me@example.com", password: "pw", smtpHost: "smtp.example.com", smtpPort: "465" })?.smtp_security).toBe("tls");
  });

  // #2442: a hub without google.client_id cannot sign in with Google, so Gmail is added here with an app password.
  it("adds Gmail with an app password on a hub without Google sign-in", () => {
    const gmail = { ...EMPTY_MAIL_FORM, kind: "gmail" as const };
    expect(mailErrors(gmail, true)).toEqual({ email: "填邮箱地址", password: "填 Gmail 的应用专用密码" });
    expect(mailErrors({ ...gmail, email: "me@mail.test", password: "abcd efgh ijkl mnop" }, true)).toEqual({});
    expect(mailRequest({ ...gmail, email: " me@mail.test ", password: "abcd efgh ijkl mnop" }, true)).toEqual({
      name: "me@mail.test",
      email: "me@mail.test",
      provider: "gmail",
      auth: "password",
      password: "abcd efgh ijkl mnop",
      imap_host: "imap.gmail.com",
      imap_port: 993,
      smtp_host: "smtp.gmail.com",
      smtp_port: 465,
      smtp_security: "tls",
    });
  });

  it("describe accounts", () => {
    expect(mailAccountLine({ id: "a", email: "me@example.com", status: "error", error: "auth failed" })).toBe("auth failed");
    expect(mailAccountLine({ id: "a", email: "me@example.com", auth: "oauth2", provider: "gmail" })).toBe("Gmail · Google 账号登录");
    expect(mailAccountLine({ id: "a", email: "me@example.com", auth: "oauth2", provider: "outlook" })).toBe("Outlook · Microsoft 账号登录");
    expect(mailAccountLine({ id: "a", email: "me@example.com", provider: "feishu" })).toBe("飞书邮箱");
    expect(mailProviderBadge({ id: "a", email: "x", provider: "outlook" })).toBe("Outlook");
    expect(mailProviderBadge({ id: "a", email: "x", provider: "gmail" })).toBe("Gmail");
    expect(mailProviderBadge({ id: "a", email: "x", provider: "feishu" })).toBe("飞书");
    expect(mailProviderBadge({ id: "a", email: "x", provider: "imap" })).toBeNull();
    expect(mailAccountLine({ id: "a", email: "me@example.com", provider: "imap", imap_host: "imap.example.com" })).toBe("IMAP · imap.example.com");
    expect(mailSummary(null)).toBe("");
    expect(mailSummary([])).toBe("未添加");
    expect(mailSummary([{ id: "a", email: "x", status: "error" }, { id: "b", email: "y" }])).toBe("2 个 · 有错误");
    expect(mailSummary([{ id: "a", email: "x" }])).toBe("1 个账户");
  });
});

describe("Outlook client ID", () => {
  it("checks the shape the hub does", () => {
    expect(clientIDError("")).toContain("应用程序(客户端) ID");
    expect(clientIDError("not-an-id")).toContain("不像 client ID");
    expect(clientIDError(" 00000000-0000-0000-0000-000000000000 ")).toBe("");
  });
});

describe("blocked claude dirs", () => {
  const state: ClaudeDirsState = {
    home: "/home/me",
    live: true,
    blocked: ["~/.automation/*", "/opt/x"],
    dirs: [
      { path: "/home/me/.claude", default: true, source: "default", running: 0, sessions: 40 },
      { path: "/home/me/.automation/a", blocked: true, blocked_by: "~/.automation/*", source: "found", running: 1, sessions: 120 },
      { path: "/home/me/.config/cc/work", source: "config", running: 0 },
    ],
  };
  it("reads paths and rows", () => {
    expect(shortPath("/home/me/.automation/a", "/home/me")).toBe("~/.automation/a");
    expect(shortPath("/home/user/x", "/home/me")).toBe("/home/user/x");
    expect(dirMeta(state.dirs[0])).toBe("默认目录，不能屏蔽 · 40 个会话");
    expect(dirMeta(state.dirs[1])).toBe("自动发现 · 120 个会话 · 1 个正在运行 · 由规则 ~/.automation/* 屏蔽");
    expect(dirMeta(state.dirs[2])).toBe("hub.json 里配的");
  });
  it("blocks a directory by its path and lifts the rule that blocks one", () => {
    expect(toggledPatterns(state, state.dirs[2])).toEqual(["~/.automation/*", "/opt/x", "~/.config/cc/work"]);
    expect(toggledPatterns(state, state.dirs[1])).toEqual(["/opt/x"]);
    expect(looseRules(state)).toEqual(["/opt/x"]); // the glob is lifted from the row it blocks
    // A directory blocked by its own path, written with ~: one row, no separate rule, no "blocked by rule".
    const own: ClaudeDirsState = { ...state, blocked: ["~/.automation/a"], dirs: [{ ...state.dirs[1], blocked_by: "~/.automation/a" }] };
    expect(looseRules(own)).toEqual([]);
    expect(dirMeta(own.dirs[0], own.home)).toBe("自动发现 · 120 个会话 · 1 个正在运行");
    expect(toggledPatterns(own, own.dirs[0])).toEqual([]);
  });
  it("checks typed patterns", () => {
    expect(patternError("", [])).not.toBe("");
    expect(patternError("relative", [])).toContain("开头");
    expect(patternError("~", [])).not.toBe("");
    expect(patternError("/opt/x/", ["/opt/x"])).toBe("已经屏蔽了");
    expect(patternError(" ~/.bots/* ", [])).toBe("");
    expect(withPattern(["/a"], " ~/.bots/ ")).toEqual(["/a", "~/.bots"]);
  });
});
