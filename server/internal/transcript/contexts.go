package transcript

import (
	"fmt"
	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/lazyre"
	"html"
	"sort"
	"strings"
)

// A message can carry <persona-context kind="…" title="…" url="…"> blocks: context picked on the phone, or everything
// a Feishu /agent request hands its agent (prompts/lark). The tag is being renamed <persona-context> (#3182): readers
// take either name. The apps show only the text outside the blocks as what the
// user wrote and fold the blocks into one row (docs/extensions/lark-agent.md "How the app shows it").

// contextBlockRe is one attributed block (the bare snapshot, ContextRe, is dropped before this).
var contextBlockRe = lazyre.New(`(?s)(<` + compat.ContextPattern() + `\b[^>]*>)(.*?)(</` + compat.ContextPattern() + `>)`)

// legacyLarkAgentRe is a Feishu /agent prompt as hub wrote it before the instructions moved into a block: an intro line,
// then a request heading (what was asked), a context heading (blocks, plus a loose line or two), and a how-to heading (the rules).
var legacyLarkAgentRe = lazyre.New(`(?s)^([^\n]*(?:用 /agent 叫你|又(?:发|写)了一条 /agent 请求)[^\n]*)\n+## 请求\n+(.*?)\n+## 上下文\n+(.*)\n+## 怎么做\n+(.*?)\s*$`)

// legacyLarkAgent rewrites an old Feishu /agent prompt into today's shape, so conversations started before the change
// show the same way: the instructions and context in blocks, the request outside them.
func legacyLarkAgent(s string) string {
	m := legacyLarkAgentRe.FindStringSubmatch(s)
	if m == nil {
		return s
	}
	intro, task, ctx, rules := m[1], strings.TrimSpace(m[2]), m[3], m[4]
	blocks := contextBlockRe.FindAllString(ctx, -1)
	loose := strings.TrimSpace(contextBlockRe.ReplaceAllString(ctx, ""))
	var b strings.Builder
	// Still the old name until the apps that read <persona-context> are out (#3182).
	b.WriteString(`<persona-context kind="instructions" title="给 Agent 的说明">` + "\n" + intro + "\n")
	if strings.HasPrefix(task, "（") { // no request written: the note in its place is for the agent
		b.WriteString("\n" + task + "\n")
		task = "/agent"
	}
	b.WriteString("\n## 怎么做\n\n" + rules + "\n")
	if loose != "" {
		b.WriteString("\n" + loose + "\n")
	}
	b.WriteString("</persona-context>\n")
	for _, bl := range blocks {
		b.WriteString(bl + "\n")
	}
	b.WriteString(task)
	return b.String()
}

// messageSourceKindRe identifies provenance metadata, including the user-authored
// sentinel. Cutting its JSON changes authorship, so it is fixed-size context.
var messageSourceKindRe = lazyre.New(`\bkind\s*=\s*["']message-source["']`)

// lightContexts fits a message with context blocks into limit bytes by cutting the blocks' bodies, longest first,
// so what the user wrote and every block's title stay whole (a plain cut could end inside a block). ok is false when
// even that does not fit, or there is nothing to cut.
func lightContexts(s string, limit int) (string, bool) {
	all := contextBlockRe.FindAllStringSubmatchIndex(s, -1)
	locs := all[:0]
	for _, loc := range all {
		if !messageSourceKindRe.MatchString(s[loc[2]:loc[3]]) {
			locs = append(locs, loc)
		}
	}
	if len(locs) == 0 {
		return "", false
	}
	fixed := len(s)
	bodies := make([]int, len(locs)) // body lengths
	for i, l := range locs {
		bodies[i] = l[5] - l[4]
		fixed -= bodies[i]
	}
	const note = "\n…（已截断，共 %d 字节）"
	budget := limit - fixed - len(locs)*(len(note)+8)
	if budget < len(locs)*64 {
		return "", false
	}
	// Water-fill: short bodies stay whole, the long ones share what is left equally.
	order := make([]int, len(locs))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool { return bodies[order[a]] < bodies[order[b]] })
	keep := make([]int, len(locs))
	left := budget
	for n, i := range order {
		share := left / (len(order) - n)
		keep[i] = min(bodies[i], share)
		left -= keep[i]
	}
	var b strings.Builder
	prev := 0
	for i, l := range locs {
		b.WriteString(s[prev:l[4]])
		body := s[l[4]:l[5]]
		if keep[i] < len(body) {
			end := keep[i]
			for end > 0 && (body[end]&0xC0) == 0x80 {
				end--
			}
			if nl := strings.LastIndexByte(body[:end], '\n'); nl > end-256 && nl > 0 {
				end = nl
			}
			body = body[:end] + fmt.Sprintf(note, len(body)) + "\n"
		}
		b.WriteString(body)
		prev = l[5]
	}
	b.WriteString(s[prev:])
	if b.Len() > limit {
		return "", false
	}
	return b.String(), true
}

// injectedBlockRes are the blocks Persona and Claude Code put into a user message around what the user wrote: the
// <persona-context> snapshot and attributed blocks, the <persona-view-errors> feedback, <system-reminder>s.
var injectedBlockRes = []*lazyre.Regexp{
	lazyre.New(`(?s)<` + compat.ContextPattern() + `\b[^>]*>.*?</` + compat.ContextPattern() + `>`),
	lazyre.New(`(?s)<persona-view-errors\b[^>]*>.*?</persona-view-errors>`),
	lazyre.New(`(?s)<system-reminder\b[^>]*>.*?</system-reminder>`),
}

// injectedOpenRe is one of those blocks left open: a message clipped inside it (Meta.FirstUser is) ends there.
var injectedOpenRe = lazyre.New(`<(?:` + compat.ContextPattern() + `|persona-view-errors|system-reminder)\b`)

// StripInjected is what the user wrote in a message, for a line that shows it (a title, a notification): without the
// injected blocks, a block cut off by a clip, and the paste wrappers (what was pasted stays). "" when nothing is left.
func StripInjected(s string) string {
	if !strings.Contains(s, "<") {
		return strings.TrimSpace(s)
	}
	for _, re := range injectedBlockRes {
		s = re.ReplaceAllString(s, " ")
	}
	if loc := injectedOpenRe.FindStringIndex(s); loc != nil {
		s = s[:loc[0]]
	}
	s = cutPartialTag(s)
	return strings.TrimSpace(pastedRe.ReplaceAllString(s, ""))
}

// injectedNames are the injected blocks' tag names, for an opening tag a clip cut inside its name.
var injectedNames = append(compat.ContextNames(), "persona-view-errors", "system-reminder")

// cutPartialTag drops an opening tag clipped before its name ended ("…<persona-con…"), which injectedOpenRe cannot
// tell from text: what follows the last "<" is a start of one of the names, behind which a clip's "…" ends the text
// (or which is long enough not to be someone's "<p").
func cutPartialTag(s string) string {
	i := strings.LastIndexByte(s, '<')
	if i < 0 {
		return s
	}
	rest := strings.TrimSpace(s[i+1:])
	clipped := strings.HasSuffix(rest, "…") || strings.HasSuffix(rest, "...")
	rest = strings.TrimSuffix(strings.TrimSuffix(rest, "…"), "...")
	if rest == "" || (!clipped && len(rest) < 4) {
		return s
	}
	for _, name := range injectedNames {
		if strings.HasPrefix(name, rest) {
			return s[:i]
		}
	}
	return s
}

// blockTitleRe is an attributed block's whole opening tag with its title.
var blockTitleRe = lazyre.New(`<` + compat.ContextPattern() + `\b[^>]*?\stitle="([^"]*)"[^>]*>`)

// Title is a message (or a title someone made of one) as a title or a preview line: what the user wrote, else the
// title of the first attributed block ("Continuing the old main chat" for a main conversation handed over), else "" (the caller
// falls back to the agent's project). A tag cut off by a clip ("<persona-context kind="context" title="Continuing…") has
// no title to take: it is dropped. Strip first, then clip: a clip first can end inside a tag.
func Title(s string) string {
	if t := dropLeadingBlocks(StripInjected(s)); t != "" {
		return t
	}
	if m := blockTitleRe.FindStringSubmatch(s); m != nil {
		return strings.TrimSpace(html.UnescapeString(m[1]))
	}
	return ""
}

// leadingTagRe is an opening tag with attributes at the start of a message (`<memory-index file="MEMORY.md">`), or one a
// clip cut off before its ">": markup a tool wrote into the first message, not words a person typed. Like a custom element
// the name has a "-" or "_", so HTML someone pasted (`<a href="…">`) stays.
var leadingTagRe = lazyre.New(`^<([A-Za-z][\w.]*[-_][\w.-]*)(?:\s+[\w:.-]+="[^"]*(?:"|$))+\s*(?:>|$)`)

// dropLeadingBlocks is a title without the blocks other tools put in front of what the user wrote: each one up to its
// closing tag, or to the end when a clip cut it off. Persona's own blocks are gone already (StripInjected).
func dropLeadingBlocks(s string) string {
	for {
		// Most titles start with words: skip the regexp for them, it allocates on every call (the session lists title
		// every row, #2371).
		if !strings.HasPrefix(s, "<") {
			return s
		}
		m := leadingTagRe.FindStringSubmatchIndex(s)
		if m == nil {
			return s
		}
		end := "</" + s[m[2]:m[3]] + ">"
		i := strings.Index(s[m[1]:], end)
		if i < 0 {
			return ""
		}
		s = strings.TrimSpace(s[m[1]+i+len(end):])
	}
}
