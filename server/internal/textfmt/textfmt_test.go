package textfmt

import (
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestFormatting(t *testing.T) {
	if got := Clip("一二三四五", 3); got != "一二三…" {
		t.Errorf("Clip = %q", got)
	}
	if got := Clip("一二三", 3); got != "一二三" {
		t.Errorf("Clip kept = %q", got)
	}
	if got := OneLine("  a\n\tb   c \n"); got != "a b c" {
		t.Errorf("OneLine = %q", got)
	}
	if got := FirstLine("\n\n## 计划\n第一步"); got != "计划" {
		t.Errorf("FirstLine = %q", got)
	}
	if got := Short("abcdef1234"); got != "abcdef12" {
		t.Errorf("Short = %q", got)
	}
	if Ago(time.Time{}) != "" || Ago(time.Now()) != "刚刚" || Ago(time.Now().Add(-5*time.Minute)) != "5 分钟前" ||
		Ago(time.Now().Add(-3*time.Hour)) != "3 小时前" || Ago(time.Now().Add(-49*time.Hour)) != "2 天前" {
		t.Error("Ago")
	}
	if StateNames[proto.StateNeedsYou] != "需要你" {
		t.Error("StateNames")
	}
}

func TestAgentLabel(t *testing.T) {
	for _, tc := range []struct {
		s    proto.Session
		want string
	}{
		{proto.Session{Title: "修登录", Cwd: "/code/app"}, "修登录"},
		{proto.Session{Title: "Claude Code", Cwd: "/code/app"}, "app"},
		{proto.Session{Cwd: "/code/app", SpaceName: "客户端"}, "客户端 › app"},
		{proto.Session{Title: "修登录", SpaceName: "客户端", SlotName: "修登录"}, "客户端 › 修登录"},
		{proto.Session{Title: "修登录", SpaceName: "客户端", SlotName: "左"}, "客户端 › 左「修登录」"},
	} {
		if got := AgentLabel(tc.s); got != tc.want {
			t.Errorf("AgentLabel(%+v) = %q, want %q", tc.s, got, tc.want)
		}
	}
}

// A plan's title shows in a push, the live activity and the Agent lists as plain text: no Markdown marks (#4387).
func TestPlanTitle(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"\n\n## 方案\n第一步", "方案"},
		{"**把缓存挪到 Postgres**\n细节", "把缓存挪到 Postgres"},
		{"- [ ] 第一步：改 `cache.go`", "第一步：改 cache.go"},
		{"1. 看 [设计](https://example.com/doc) 再动手", "看 设计 再动手"},
		{"> *先* 停掉 ~~旧的~~ 服务", "先 停掉 旧的 服务"},
		{"---\n\n# 计划", "计划"},
		{"直接一句话", "直接一句话"},
		{"改 `__init__.py` 和 `a**b`", "改 __init__.py 和 a**b"},
		{"重命名 my__var，算 2 ** 3", "重命名 my__var，算 2 ** 3"},
		{"", ""},
	} {
		if got := PlanTitle(tc.in); got != tc.want {
			t.Errorf("PlanTitle(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A reply flattened to one line (node's snippet) keeps its words in a push: a table's cells joined, an HTML block's
// text without tags, comments, scripts or styles (#4462).
func TestPushTextOfAFlattenedReply(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"| 项 | 状态 | |---|---| | 构建 | 通过 |", "项 · 状态 · 构建 · 通过"},
		{"结果如下 | 模块 | 结果 | | :--- | ---: | | hub | 通过 |", "结果如下 · 模块 · 结果 · hub · 通过"},
		{"<details><summary>结果</summary>全部通过</details>", "结果 全部通过"},
		{"<details><summary>比较</summary>当 1 < 2 且 3 > 1 时通过</details>", "比较 当 1 < 2 且 3 > 1 时通过"},
		{"<details><summary>A &amp; B</summary>&lt;ok&gt;</details>", "A & B <ok>"},
		{"<script>alert(1)</script>", ""},
		{"<style>p{color:red}</style>", ""},
		{"<!--\n不该出现\n-->", ""},
		{"<div>正文<script>alert(1)</script></div>", "正文"},
		{"a | b 两个都行", "a | b 两个都行"},
		{"use `|---|---|` to mark", "use |---|---| to mark"},
		{"grep -E 'a|---|b'", "grep -E 'a|---|b'"},
		{"进度 |--| 50%", "进度 |--| 50%"},
		{"## 早报 **今天 3 个会**", "早报 今天 3 个会"},
		{"| 项 | 状态 |\n|---|---|\n| 构建 | 通过 |", "项\n状态\n构建\n通过"}, // a table on its own lines, as before
	} {
		if got := PushText(tc.in); got != tc.want {
			t.Errorf("PushText(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Text that already holds what used to be the code-span marker ("\x00N\x00") comes back as it was, with no panic;
// a table's cell next to an HTML tag leaves no separator at the end of the line (review of #4475).
func TestPushTextKeepsWhatItIsGiven(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"\x000\x00 | a | b | |---|---|", "\x000\x00 · a · b"},
		{"`c` \x005\x00 | a | b | |---|---|", "c \x005\x00 · a · b"},
		{"`|x|` | a | b | |---|---|", "|x| · a · b"},
		{"<details><summary>表</summary>| a | b | |---|---| | 1 | 2 |</details>", "表 · a · b · 1 · 2"},
	} {
		if got := PushText(tc.in); got != tc.want {
			t.Errorf("PushText(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// PlainMarkdown is a reply's text without its markup, for a push or a one-line preview.
func TestPlainMarkdown(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"## 根因\nhub 和 node 的 **会话** 不同步", "根因\nhub 和 node 的 会话 不同步"},
		{"**网页的改动**\n- **底部的「收起」**：改成 `Collapse`\n- 第二条", "网页的改动\n底部的「收起」：改成 Collapse\n第二条"},
		{"调 `POST /api/v1/sessions/{mid}/{sid}/interrupt` 停下", "调 POST /api/v1/sessions/{mid}/{sid}/interrupt 停下"},
		{"看 [PR](https://github.com/o/r/pull/1) 和 <https://example.com>", "看 PR 和 https://example.com"},
		{"> 引用 ~~删掉~~ *斜体*\n\n```go\nx := 1\n```", "引用 删掉 斜体\nx := 1"},
		{"| a | b |\n| - | - |\n| 1 | 2 |", "a\nb\n1\n2"},
		{"剪在 **加粗中间…", "剪在 加粗中间…"},
		{"剪在 `代码中间…", "剪在 代码中间…"},
		{"完成了", "完成了"},
		{"", ""},
	} {
		if got := PlainMarkdown(tc.in); got != tc.want {
			t.Errorf("PlainMarkdown(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// An assistant card's push says its fallback, not the card's JSON.
func TestPlainMarkdownOfACardIsItsFallback(t *testing.T) {
	text := "**Result**\n```persona-view\n{\"fallback\":\"Build complete\",\"sections\":[]}\n```\n"
	got := PlainMarkdown(text)
	if strings.Contains(got, "sections") || strings.Contains(got, "persona-view") || !strings.Contains(got, "Build complete") {
		t.Fatalf("push text %q", got)
	}
}

// PushText's handling stays out of PlainMarkdown and PlanTitle: they read these as they did.
func TestPlainMarkdownAndPlanTitleUnchanged(t *testing.T) {
	for in, want := range map[string]string{
		"<details><summary>计划</summary></details>": "",
		"改 `a|-|b`": "改 a|-|b",
		"| 项 | 状态 | |---|---| | 构建 | 通过 |": "| 项 | 状态 | |---|---| | 构建 | 通过 |",
	} {
		if got := PlanTitle(in); got != want {
			t.Errorf("PlanTitle(%q) = %q, want %q", in, got, want)
		}
		if got := PlainMarkdown(in); got != want {
			t.Errorf("PlainMarkdown(%q) = %q, want %q", in, got, want)
		}
	}
}
