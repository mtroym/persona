#!/usr/bin/env python3
"""End-to-end check of Settings › Connectors › Records sent as you (#2412, docs/web/web.md "Settings", docs/extensions/connectors.md "Sent as the user"): the
secondary entry, legacy redirects and back navigation, the rows (where, the first two lines, how each was let through, who asked), a chat
link, Load earlier, the connector chips and the empty state under one, the details (every field, a text of exactly 4000
characters noted as cut, copy), and the other states (an empty ledger, no database, no full access, an older hub), at
wide and phone width.

Needs the web page against the fake hub with MOCK_SENT=1 and Playwright for Python:

    MOCK_SENT=1 PORT=19031 python3 scripts/mock/mock_hub.py &
    (cd web && PERSONA_HUB_URL=http://127.0.0.1:19031 npx vite --host 127.0.0.1 --port 5431 --strictPort &)
    uv run --no-project --with playwright==1.58.0 python3 scripts/test/web_sent_e2e.py http://127.0.0.1:5431 \\
        --hub http://127.0.0.1:19031 [--shots <dir>] [--inter <dir>] [--channel chrome]

Prints one line per check and a verdict; exits 1 on the first failure. The other states are switched on the running fake
hub with GET /mock/sent?mode=empty|nodb|forbidden|old (back to 1 at the end).

--shots <dir> takes the screenshots the docs use, in English (a Linux box has no Chinese font): the page at wide and at phone
width, light and dark, saved as wide-<theme>.png and phone-<theme>.png, and put side by side as web-sent-as-you-<theme>.png.
--inter <dir>: a folder with Inter-Regular.woff2, Inter-Medium.woff2 and Inter-SemiBold.woff2 (the `web/` folder of the npm
package inter-ui), for a machine that doesn't have Inter, so the shots look like the app. --channel: the browser
(chrome, msedge…); by default Playwright's own Chromium.
"""
import argparse
import base64
import json
import os
import re
import struct
import urllib.request

from playwright.sync_api import expect, sync_playwright

TOKEN = "mock"
ASSISTANT_CHAT = "/persona/b2e4d6f8-0000-4000-8000-000000000001"  # the chat the fake ledger's first row says asked
WIDE = {"width": 1280, "height": 900}
PHONE = {"width": 390, "height": 844}


def ok(msg):
    print("ok  " + msg, flush=True)


def set_mode(hub, mode):
    with urllib.request.urlopen(f"{hub}/mock/sent?mode={mode}", timeout=5) as r:
        assert json.load(r)["mode"] == mode, mode


def new_page(browser, size, theme, lang, scale=1):
    context = browser.new_context(viewport=size, color_scheme=theme, device_scale_factor=scale, permissions=["clipboard-read", "clipboard-write"])
    context.add_init_script(f"localStorage.setItem('persona.theme', {theme!r}); localStorage.setItem('persona.language', {lang!r})")
    page = context.new_page()
    page.set_default_timeout(15000)
    errors = []
    page.on("pageerror", lambda e: errors.append(str(e)))
    return context, page, errors


def login(page, base):
    """The first load takes the token (and lands on the home page); later loads of any address keep it."""
    page.goto(f"{base}/overview?token={TOKEN}")
    page.locator("aside").first.wait_for()


def open_sent(page, base):
    login(page, base)
    page.goto(f"{base}/settings?tab=connectors&connector=-sent")


def ids(rows):
    return [int(r.get_attribute("data-sent")) for r in rows.all()]


def wide(browser, base, hub):
    context, page, errors = new_page(browser, WIDE, "light", "zh-Hans")
    main = page.locator("main")
    try:
        login(page, base)
        page.goto(f"{base}/settings?tab=connectors")
        nav = main.get_by_role("navigation", name="设置分类")
        expect(nav.get_by_role("button", name="以你的名义发出", exact=True)).to_have_count(0)
        entry = main.locator('[data-settings-group="sent"]')
        expect(entry).to_contain_text("以你的名义发出过什么、为什么放行")
        assert entry.evaluate("el => el === el.parentElement.lastElementChild")
        entry.get_by_role("button").click()
        expect(page).to_have_url(base + "/settings?tab=connectors&connector=-sent")
        expect(main.get_by_role("heading", name="以你的名义发出的记录", exact=True)).to_be_visible()
        ok("Sent is a secondary page entered from the bottom of Connectors, not a Settings tab")
        main.locator("input").first.fill("为什么放行")
        nav.get_by_role("button", name=re.compile("连接器.*以你的名义发出的记录")).click()
        expect(page).to_have_url(base + "/settings?tab=connectors")
        expect(entry).to_be_in_viewport()
        entry.get_by_role("button").click()
        ok("Settings search finds the existing hint and scrolls to the Connectors entry")

        for old in ("/settings?tab=sent", "/settings/sent"):
            page.goto(base + old)
            expect(page).to_have_url(base + "/settings?tab=connectors&connector=-sent")
            expect(main.locator("[data-sent]")).to_have_count(50)
        ok("both legacy URL forms redirect to the secondary page")

        rows = main.locator("[data-sent]")
        expect(rows).to_have_count(50)
        first = rows.first
        expect(first).to_contain_text("ann@example.com · Re: Design review moved to Tuesday")
        expect(first).to_contain_text("Hi Ann,")
        expect(first).to_contain_text("Tuesday afternoon works for me")
        expect(first).not_to_contain_text("Thanks")  # the third line is for the details
        expect(first).to_contain_text(re.compile(r"\d+\s?分钟前"))
        assert ids(rows) == sorted(ids(rows), reverse=True) and ids(rows)[0] == 70
        ok("50 rows, newest first: target, relative time and only the first two lines of the text")

        for label in ("按规则", "本对话始终允许", "你在确认卡片上同意", "助手代答", "扩展自行发送 · auto_reply"):
            expect(main.get_by_text(label, exact=True).first).to_be_visible()
        expect(main.get_by_text("助手代答", exact=True).first).to_have_class(re.compile("text-orange"))
        expect(main.get_by_text("扩展自行发送 · auto_reply", exact=True).first).to_have_class(re.compile("text-orange"))
        expect(main.get_by_text("你在确认卡片上同意", exact=True).first).not_to_have_class(re.compile("text-orange"))
        ok("all four ways through the gate are badged; the assistant's answer and the extension's own send in orange")

        expect(main.get_by_text("无对话", exact=True).first).to_be_visible()
        expect(main.get_by_text("扩展自述", exact=True).first).to_be_visible()
        expect(main.get_by_text("没有记下正文")).to_have_count(0)
        mail_empty = rows.filter(has_text="Out of office")
        expect(mail_empty.first).to_contain_text("Send an email to bob@example.com, subject: Out of office")  # no text: the summary
        ok("no chat, extension-reported, and a send with no text shows its summary")

        # the link is named by its row, so a screen reader can tell the rows' links apart
        expect(first.get_by_role("link", name=re.compile("^打开对话：邮件 ann@example.com · Re: Design review"))).to_have_count(1)
        first.get_by_role("link", name="打开对话").click()
        expect(page).to_have_url(base + ASSISTANT_CHAT)
        page.go_back()
        expect(main.locator("[data-sent]").first).to_be_visible()
        ok("a row's chat link opens the assistant's chat in Persona, and Back returns to the list")

        main.get_by_role("button", name="加载更早").click()
        expect(rows).to_have_count(70)
        expect(main.get_by_role("button", name="加载更早")).to_have_count(0)
        found = ids(rows)
        assert found == list(range(70, 0, -1)), found
        ok("Load earlier adds the other 20 rows once each and the button goes with the last page")

        # the details of the long one
        rows.filter(has_text="Quarterly summary (long)").first.click()
        dialog = page.get_by_role("dialog")
        expect(dialog).to_be_visible()
        for words in ("mail_send", "bob@example.com, team@example.com", "Quarterly summary (long)", "ann@example.com", "ix-demo-2", "你在确认卡片上同意"):
            expect(dialog).to_contain_text(words)
        text = dialog.locator("pre").text_content()
        assert len(text) == 4000, len(text)
        expect(dialog).to_contain_text("在 4,000 个字处被截断")
        dialog.get_by_role("button", name="复制正文").click()
        expect(page.get_by_text("已拷贝", exact=True)).to_be_visible()
        assert page.evaluate("navigator.clipboard.readText()") == text
        page.keyboard.press("Escape")
        expect(dialog).to_have_count(0)
        ok("the details show every field, note a text of exactly 4000 characters as cut, and copy it whole")

        rows.filter(has_text="Standup notes").first.click()
        expect(dialog).to_contain_text("msg-8841")
        expect(dialog).to_contain_text("auto_reply")
        expect(dialog).to_contain_text("扩展自述")
        expect(dialog.locator("pre")).to_contain_text("I'll post the action items after lunch.")
        expect(dialog).not_to_contain_text("被截断")
        page.keyboard.press("Escape")
        ok("an extension's own record names the extension and its message id")

        # the chips
        group = main.get_by_role("group", name="按连接器筛选")
        expect(group.get_by_role("button", name="全部")).to_have_attribute("aria-pressed", "true")
        names = [x.strip() for x in group.get_by_role("button").all_text_contents()]
        # Lark, then the connectors the loaded rows show: nothing for the connectors that never write (the fake hub lists Messages)
        assert names == ["全部", "飞书", "邮件", "GitHub", "examplechat"], names
        with page.expect_request(lambda r: "/connectors/sent?connector=mail" in r.url):
            group.get_by_role("button", name="邮件", exact=True).click()
        expect(rows).to_have_count(24)
        assert all("邮件" in t for t in rows.all_text_contents())
        expect(main.get_by_role("button", name="加载更早")).to_have_count(0)
        expect(group.get_by_role("button", name="邮件", exact=True)).to_have_attribute("aria-pressed", "true")
        ok("chips are Lark and the connectors the rows show (no Messages); the 邮件 chip asks with connector=mail and lists its 24 rows")

        group.get_by_role("button", name="邮件", exact=True).click()
        expect(rows).to_have_count(50)
        # a chip whose rows are gone (the ledger is emptied under the page): the chips stay, 显示全部 goes back to all
        set_mode(hub, "empty")
        try:
            group.get_by_role("button", name="GitHub", exact=True).click()
            expect(main.get_by_text("GitHub 还没有记录")).to_be_visible()
            expect(group.get_by_role("button", name="GitHub", exact=True)).to_be_visible()
        finally:
            set_mode(hub, "1")
        main.get_by_role("button", name="显示全部").click()
        expect(rows).to_have_count(50)
        ok("nothing recorded under a chip (GitHub) says so, the chips stay, and 显示全部 brings the list back")

        # the other states, as the hub gives them
        states = [
            ("empty", ["还没有记录到以你名义发出的内容", "lark-cli", "90 天"]),
            ("nodb", ["这台 hub 没有数据库"]),
            ("forbidden", ["只有拥有完整权限的设备才能看这个列表"]),
            ("old", ["升级 hub"]),
        ]
        try:
            for mode, words in states:
                set_mode(hub, mode)
                page.reload()
                for w in words:
                    expect(main.get_by_text(w).first).to_be_visible()
                expect(rows).to_have_count(0)
                expect(main.get_by_role("group", name="按连接器筛选")).to_have_count(0)
                ok(f"MOCK_SENT={mode}: {words[0]}")
        finally:
            set_mode(hub, "1")
        assert not errors, errors
    finally:
        context.close()


def phone(browser, base):
    context, page, errors = new_page(browser, PHONE, "light", "zh-Hans")
    main = page.locator("main")
    try:
        login(page, base)
        page.goto(f"{base}/settings")
        nav = main.get_by_role("navigation", name="设置分类")
        expect(nav.get_by_role("button", name=re.compile("以你的名义发出"))).to_have_count(0)
        nav.get_by_role("button", name=re.compile("^连接器")).click()
        entry = main.locator('[data-settings-group="sent"]')
        expect(entry).to_contain_text("以你的名义发出过什么、为什么放行")
        entry.get_by_role("button").click()
        expect(page).to_have_url(base + "/settings?tab=connectors&connector=-sent")
        expect(main.get_by_role("heading", name="以你的名义发出的记录", exact=True)).to_be_visible()
        rows = main.locator("[data-sent]")
        expect(rows).to_have_count(50)
        assert page.evaluate("document.documentElement.scrollWidth <= window.innerWidth"), "the page scrolls sideways"
        for r in rows.all()[:12]:
            box = r.bounding_box()
            assert box["x"] >= 0 and box["x"] + box["width"] <= PHONE["width"] + 0.5, box
        rows.filter(has_text="Quarterly summary (long)").first.click()
        dialog = page.get_by_role("dialog")
        expect(dialog).to_be_visible()
        assert abs(dialog.bounding_box()["width"] - PHONE["width"]) < 1  # a sheet over the whole width
        page.keyboard.press("Escape")
        main.get_by_role("button", name="连接器", exact=True).click()
        expect(page).to_have_url(base + "/settings?tab=connectors")
        expect(main.locator('[data-settings-group="sent"]')).to_be_visible()
        main.get_by_role("button", name="返回设置").click()
        expect(page).to_have_url(base + "/settings")
        assert not errors, errors
        ok("phone width: the nested entry has its hint, the page fits, details are a sheet, and Back returns through Connectors to Settings")
    finally:
        context.close()


def shots(browser, base, out, inter):
    css = ""
    if inter:
        for weight, name in ((400, "Regular"), (500, "Medium"), (600, "SemiBold")):
            with open(os.path.join(inter, f"Inter-{name}.woff2"), "rb") as f:
                data = base64.b64encode(f.read()).decode()
            css += f"@font-face{{font-family:Inter;font-weight:{weight};src:url(data:font/woff2;base64,{data}) format('woff2')}}"
    os.makedirs(out, exist_ok=True)
    for theme in ("light", "dark"):
        files = {}
        for kind, size in (("wide", {"width": 1180, "height": 800}), ("phone", PHONE)):
            context, page, errors = new_page(browser, size, theme, "en", scale=1.5)
            try:
                open_sent(page, base)
                if css:
                    page.add_style_tag(content=css)
                main = page.locator("main")
                expect(main.locator("[data-sent]")).to_have_count(50)
                expect(main.get_by_role("button", name="Load earlier")).to_be_visible()
                expect(page.get_by_role("heading", name="Records sent as you", exact=True)).to_be_visible()
                if kind == "wide":
                    expect(main.get_by_role("navigation", name="Settings categories").get_by_role("button", name="Connectors", exact=True)).to_have_attribute("aria-current", "page")
                page.evaluate("document.fonts.ready")
                page.wait_for_timeout(500)
                files[kind] = os.path.join(out, f"{kind}-{theme}.png")
                # wide: the page without the sidebar (its chats are named in Chinese by the fake hub)
                (main if kind == "wide" else page).screenshot(path=files[kind])
                assert not errors, errors
            finally:
                context.close()
        compose(browser, files["wide"], files["phone"], os.path.join(out, f"web-sent-as-you-{theme}.png"), theme == "dark")
    ok(f"screenshots in {out}")


def png_size(path):
    """Width and height of a PNG, from its header."""
    with open(path, "rb") as f:
        head = f.read(24)
    return struct.unpack(">II", head[16:24])


def compose(browser, wide_png, phone_png, out, dark, scale=1.5):
    """The wide and the phone screenshot side by side on a plain ground (the docs' usual way), at the size they were seen."""
    def img(path):
        width = png_size(path)[0] / scale
        with open(path, "rb") as f:
            return f'<img style="width:{width}px;border-radius:10px;box-shadow:0 1px 4px rgba(0,0,0,.18)" src="data:image/png;base64,{base64.b64encode(f.read()).decode()}">'
    ground = "#08080a" if dark else "#ececf0"
    gap = 24
    total = png_size(wide_png)[0] / scale + png_size(phone_png)[0] / scale + 3 * gap
    html = (f'<body style="margin:0;padding:{gap}px;background:{ground};display:flex;gap:{gap}px;align-items:flex-start;width:max-content">'
            f'{img(wide_png)}{img(phone_png)}</body>')
    context = browser.new_context(viewport={"width": int(total), "height": 600}, device_scale_factor=scale)
    try:
        page = context.new_page()
        page.set_content(html)
        page.screenshot(path=out, full_page=True)
    finally:
        context.close()


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("base")
    ap.add_argument("--hub", required=True)
    ap.add_argument("--shots")
    ap.add_argument("--inter")
    ap.add_argument("--channel")
    a = ap.parse_args()
    base, hub = a.base.rstrip("/"), a.hub.rstrip("/")
    set_mode(hub, "1")
    with sync_playwright() as p:
        browser = p.chromium.launch(channel=a.channel) if a.channel else p.chromium.launch()
        try:
            wide(browser, base, hub)
            phone(browser, base)
            if a.shots:
                shots(browser, base, a.shots, a.inter)
        finally:
            browser.close()
    print("PASS web sent e2e", flush=True)


if __name__ == "__main__":
    main()
