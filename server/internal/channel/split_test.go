package channel

import (
	"strings"
	"testing"
)

// assertFencesBalanced replays Split's fence rule over one piece: every piece must leave every block closed.
func assertFencesBalanced(t *testing.T, p string) {
	open := ""
	for _, l := range strings.Split(p, "\n") {
		if m, _, bare := fenceLine(l); m != "" {
			if open == "" {
				open = m
			} else if m[0] == open[0] && len(m) >= len(open) && bare {
				open = ""
			}
		}
	}
	if open != "" {
		t.Fatalf("piece leaves a %q code block open:\n%s", open, p)
	}
}
func bareFence(l string) bool { m, _, bare := fenceLine(l); return m != "" && bare }

// reassemble removes the close/reopen fence pairs Split inserts at piece seams and joins the source back.
func reassemble(parts []string) string {
	lines := make([][]string, len(parts))
	for i, p := range parts {
		lines[i] = strings.Split(p, "\n")
	}
	for i := 0; i+1 < len(lines); i++ {
		a, b := lines[i], lines[i+1]
		if len(a) > 0 && len(b) > 0 && bareFence(a[len(a)-1]) {
			if m, _, _ := fenceLine(b[0]); m != "" {
				lines[i], lines[i+1] = a[:len(a)-1], b[1:] // injected close plus reopen
			}
		}
	}
	cores := make([]string, len(lines))
	for i, l := range lines {
		cores[i] = strings.Join(l, "\n")
	}
	return strings.Join(cores, "\n")
}
func TestSplitFencesAcrossPieces(t *testing.T) {
	cases := []struct {
		name, text, marker, head string
		n                        int
		noFence, crlf, midCut    bool
	}{
		{"tilde", "开头\n~~~\n" + strings.Repeat("echo hi\n", 40) + "~~~\n结尾", "~~~", "~~~", 60, false, false, false},
		{"backtick with info string", "开头\n```go\n" + strings.Repeat("fmt.Println(1)\n", 40) + "```\n结尾", "```", "```go", 200, false, false, false},
		{"four backticks with inner triple fence", "说明\n````\n" + strings.Repeat("code line\n", 12) + "```go\nx := 1\n```\n" + strings.Repeat("more code\n", 12) + "````\n尾部", "````", "````", 80, false, false, false},
		{"tilde block ignores inner backticks", "开头\n~~~\n" + strings.Repeat("a\n", 20) + "```not close\n" + strings.Repeat("b\n", 20) + "~~~\n结尾", "~~~", "~~~", 60, false, false, false},
		{"inner longer run with text does not close", "~~~~\n" + strings.Repeat("~", 47) + "x\n" + strings.Repeat("c\n", 40) + "~~~~\nend", "~~~~", "~~~~", 60, false, false, false},
		{"inner long run with info string does not close", "```\n" + strings.Repeat("`", 20) + "python xyz\n" + strings.Repeat("c\n", 40) + "```\nend", "```", "```", 60, false, false, false},
		{"inner run padded with spaces then text does not close", "```\n```" + strings.Repeat(" ", 17) + "x\n" + strings.Repeat("c\n", 40) + "```\nend", "```", "```", 60, false, false, false},
		{"fence-looking line with text does not close", "```\n" + strings.Repeat("a\n", 10) + "``` not close\n" + strings.Repeat("b\n", 30) + "```\nend", "```", "```", 60, false, false, false},
		{"closing fence longer than the opening one", "``````\n" + strings.Repeat("x\n", 40) + "```````\nend", "``````", "``````", 80, false, false, false},
		{"up to three leading spaces still make a fence", "x\n   ```\n" + strings.Repeat("line\n", 30) + "   ```\nend", "```", "```", 60, false, false, false},
		{"CRLF line endings", "开头\r\n~~~\r\n" + strings.Repeat("echo hi\r\n", 40) + "~~~\r\n结尾", "~~~", "~~~", 60, false, true, false},
		{"hard-cut long line inside a backtick fence", "开头\n" + "```" + "\n" + strings.Repeat("字", 200) + "\n```\n结尾", "```", "```", 60, false, false, true},
		{"hard-cut long line inside a tilde fence", "开头\n~~~\n" + strings.Repeat("字", 200) + "\n~~~\n结尾", "~~~", "~~~", 60, false, false, true},
		{"four-space indent is not a fence", strings.Repeat("    ```not fence\n", 30) + "结尾", "", "", 60, true, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parts := Split(tc.text, tc.n)
			if len(parts) < 2 {
				t.Fatalf("not split into pieces: %d", len(parts))
			}
			seams := 0
			for i, p := range parts {
				if n := len([]rune(p)); n > tc.n {
					t.Fatalf("piece %d has %d runes, limit %d", i, n, tc.n)
				}
				assertFencesBalanced(t, p)
				if tc.noFence {
					for _, l := range strings.Split(p, "\n") {
						if m, _, _ := fenceLine(l); m != "" {
							t.Fatalf("piece %d treats an indented line as a fence: %q", i, l)
						}
					}
				}
			}
			for i := 0; i+1 < len(parts) && !tc.noFence; i++ {
				a := strings.Split(parts[i], "\n")
				if c := a[len(a)-1]; bareFence(c) && strings.Split(parts[i+1], "\n")[0] == tc.head {
					if c != tc.marker {
						t.Fatalf("seam after piece %d closes with %q, want %q", i, c, tc.marker)
					}
					seams++
				}
			}
			if !tc.noFence && seams == 0 {
				t.Fatal("the block never crossed a piece boundary; no close/reopen seam was exercised")
			}
			want, got := tc.text, reassemble(parts)
			if tc.crlf { // a seam inside CRLF text joins on an injected LF
				want, got = strings.ReplaceAll(want, "\r\n", "\n"), strings.ReplaceAll(got, "\r\n", "\n")
			} else if tc.midCut { // a mid-line hard cut injects newlines; compare with every newline dropped
				want, got = strings.ReplaceAll(want, "\n", ""), strings.ReplaceAll(got, "\n", "")
			}
			if got != want {
				t.Fatalf("reassembled text differs\n got: %q\nwant: %q", got, want)
			}
		})
	}
}
func TestSplitLongFenceOpenerFitsPiece(t *testing.T) {
	// The opening piece must never overflow on a long fence (n+L-8 at L >= 9 regression): prose flushes first.
	for _, tc := range []struct{ prose, n int }{{40, 60}, {3490, 3500}} {
		for _, L := range []int{9, 10, 12, 20, 50} {
			if L > tc.n/2 {
				continue
			}
			mark := strings.Repeat("`", L)
			text := strings.Repeat("字", tc.prose) + "\n" + mark + "\n" + strings.Repeat("x\n", 30) + mark + "\nend"
			parts := Split(text, tc.n)
			for i, p := range parts {
				if n := len([]rune(p)); n > tc.n {
					t.Fatalf("prose=%d n=%d L=%d piece %d has %d runes", tc.prose, tc.n, L, i, n)
				}
				assertFencesBalanced(t, p)
			}
			if got := reassemble(parts); got != text {
				t.Fatalf("prose=%d L=%d reassembled text differs", tc.prose, L)
			}
		}
	}
}
func TestSplitSmallNDoesNotPanicOrHang(t *testing.T) {
	// splitOK enforces the strict bound: degraded pieces stay at n too (stale marker must not inflate them).
	splitOK := func(src string, n int) ([]string, string) {
		parts := Split(src, n)
		joined := ""
		for i, p := range parts {
			joined += p
			if p == "" || len([]rune(p)) > n {
				t.Fatalf("n=%d piece %d invalid: %q", n, i, p)
			}
		}
		return parts, joined
	}
	for _, n := range []int{1, 8, 12, 20, 29, 30} {
		_, joined := splitOK("```go\n"+strings.Repeat("x", 200)+"\n```\nend", n)
		if !strings.Contains(joined, "end") || strings.Count(joined, "x") != 200 {
			t.Fatalf("n=%d lost content", n)
		}
	}
	// A closer longer than its opener cannot be balanced at small n: degrade first, but emit it intact.
	parts, joined := splitOK("```\n"+strings.Repeat("y", 60)+"\n``````\nend", 9)
	intact := false
	for _, p := range parts {
		intact = intact || p == "``````" // the long closer was never hard-cut
	}
	if !intact || !strings.Contains(joined, "end") || strings.Count(joined, "y") != 60 {
		t.Fatalf("closer-degrade lost content: %q", joined)
	}
	// A mid-line degrade must not let the current line's marker reopen the fence (stale-marker regression).
	stale := "`````````\n" + strings.Repeat("~", 47) + "info\n" + strings.Repeat("z", 120) + "\n`````````\nend"
	_, joined = splitOK(stale, 20)
	if strings.Count(joined, "~") != 47 || strings.Count(joined, "z") != 120 || !strings.Contains(joined, "end") {
		t.Fatalf("stale-marker degrade lost content: %q", joined)
	}
	if got := Split(strings.Repeat("\n", 100), 10); len(got) != 1 || got[0] != strings.Repeat("\n", 100) {
		t.Fatalf("whitespace-only input: %q", got)
	}
}
func TestFenceLine(t *testing.T) {
	cases := []struct {
		line, marker, head string
		bare               bool
	}{
		{"```", "```", "```", true},
		{"```go", "```", "```go", false},
		{"~~~text", "~~~", "~~~text", false},
		{"   ```", "```", "```", true},
		{"    ```", "", "", false},         // four spaces: indented code block
		{"\t```", "", "", false},           // a tab indents past column 3
		{"``", "", "", false},              // too short
		{"a```", "", "", false},            // not at the start
		{"```\t", "```", "```", false},     // only a tab after the run: not a closing fence
		{"```a`b", "```", "```a`b", false}, // backticks in the info string stay part of the line
		{"~~~a~b", "~~~", "~~~a~b", false},
		{"```" + strings.Repeat("语", 25), "```", "```" + strings.Repeat("语", 25), false}, // full info, capped only on reopen
	}
	for _, tc := range cases {
		if m, h, b := fenceLine(tc.line); m != tc.marker || h != tc.head || b != tc.bare {
			t.Fatalf("fenceLine(%q) = %q, %q, %v; want %q, %q, %v", tc.line, m, h, b, tc.marker, tc.head, tc.bare)
		}
	}
}

func TestSplitLongLineSafeBoundaries(t *testing.T) {
	link := "[docs](https://example.com/a/" + strings.Repeat("p", 20) + ")"
	code := "`short code inside here`"
	tag := "<https://example.com/" + strings.Repeat("p", 22) + ">"
	cases := []struct {
		name      string
		text      string
		n         int
		keep      string // must appear unbroken inside one piece
		punct     rune   // every non-final piece must end after this boundary
		joinExact bool   // expect the pieces to reassemble the text, compared newline-stripped
		minPieces int
	}{
		{"link intact", strings.Repeat("a", 40) + link + " tail\nend", 60, link, 0, true, 2},
		{"inline code intact", strings.Repeat("a", 30) + code + " " + strings.Repeat("y", 30) + "\nend", 60, code, 0, true, 2},
		{"triple-backtick code intact", strings.Repeat("a", 40) + "```code here``` tail\nend", 60, "```code here```", 0, true, 2},
		{"triple-backtick span longer than piece", "a```" + strings.Repeat("x", 200) + "```\nend", 60, "", 0, true, 3},
		{"tag intact", strings.Repeat("a", 40) + tag + "\nend", 60, tag, 0, true, 2},
		{"entity intact", strings.Repeat("a", 40) + "&" + strings.Repeat("e", 30) + "; tail\nend", 60, "&" + strings.Repeat("e", 30) + ";", 0, true, 2},
		// markup quoted inside an inline code span is literal and must not be parsed or split as a link
		{"markup inside code", strings.Repeat("a", 40) + "`[x y](u)`" + strings.Repeat("q", 200) + "\nend", 60, "`[x y](u)`", 0, true, 2},
		{"cjk punctuation", strings.Repeat("这是一句话。", 20) + "\nend", 60, "", '。', true, 2},
		{"whitespace", strings.Repeat("word ", 30) + "tail\nend", 60, "", ' ', true, 2},
		{"no boundary token", strings.Repeat("x", 300) + "\nend", 60, "", 0, true, 5},
		{"span longer than piece", "`" + strings.Repeat("x", 200) + "`\nend", 60, "", 0, true, 3},
		// an inner run of another length is content: only an exact-length run can close the span
		{"inner run not a closer", strings.Repeat("a", 30) + "`" + strings.Repeat("x", 10) + "``" + strings.Repeat("x", 30) + "` tail\nend", 60, "`" + strings.Repeat("x", 10) + "``" + strings.Repeat("x", 30) + "`", 0, true, 2},
		// a punctuation boundary inside a tag before a later link must not shove the cut into the link
		{"link after tag and cjk", tag + strings.Repeat("界", 16) + link + "长句" + strings.Repeat("界", 120) + "\nend", 60, link, 0, true, 2},
		// hard-cutting a doomed over-long link earlier in the line must not swallow a later code span
		{"code after doomed link", "[d](https://example.com/" + strings.Repeat("p", 22) + ") word go `" + strings.Repeat("c", 19) + "` tail\nend", 30, "`" + strings.Repeat("c", 19) + "`", 0, true, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parts := Split(tc.text, tc.n)
			if len(parts) < tc.minPieces {
				t.Fatalf("got %d pieces, want at least %d", len(parts), tc.minPieces)
			}
			// a flush at a line end trims that newline, so the content invariant is newline-stripped
			if tc.joinExact && strings.ReplaceAll(strings.Join(parts, ""), "\n", "") != strings.ReplaceAll(tc.text, "\n", "") {
				t.Fatalf("pieces do not reassemble the source")
			}
			if tc.keep != "" {
				found := false
				for _, p := range parts {
					if runes := []rune(p); len(runes) <= tc.n && strings.Contains(p, tc.keep) {
						found = true
					}
				}
				if !found {
					t.Fatalf("span %q was not kept whole in one within-limit piece: %q", tc.keep, parts)
				}
			}
			for i, p := range parts {
				if l := len([]rune(p)); l > tc.n {
					t.Fatalf("piece %d has %d runes, limit is %d", i, l, tc.n)
				}
				if tc.punct != 0 && i < len(parts)-1 {
					if r := []rune(p); len(r) == 0 || r[len(r)-1] != tc.punct {
						t.Fatalf("piece %d ends with %q, want a cut right after %q", i, p, tc.punct)
					}
				}
			}
		})
	}
}

func TestSafeCutBoundaryPrecedence(t *testing.T) {
	// whitespace exactly at the fixed limit is a real boundary and wins over earlier punctuation
	spaceAtLimit := []rune("abc。" + strings.Repeat("w", 7) + " " + strings.Repeat("z", 40))
	if k, _ := safeCut(spaceAtLimit, 12, 12, true, 0, 0); k != 12 {
		t.Fatalf("space right at the limit: cut %d, want 12", k)
	}
	// without whitespace, the cut backs off to the nearest preceding punctuation
	punctEarlier := []rune("abc。" + strings.Repeat("w", 20))
	if k, _ := safeCut(punctEarlier, 12, 12, true, 0, 0); k != 4 {
		t.Fatalf("punctuation fallback: cut %d, want 4 (right after the 。)", k)
	}
}

func TestSplitTrivialInputs(t *testing.T) {
	cases := []struct {
		text string
		n    int
	}{
		{"", 10},
		{"a", 10},
		{strings.Repeat("字", 10), 10},
	}
	for _, tc := range cases {
		got := Split(tc.text, tc.n)
		if len(got) != 1 || got[0] != tc.text {
			t.Fatalf("Split(%q, %d) = %q, want the input unchanged as one piece", tc.text, tc.n, got)
		}
	}
}

func TestSplitKeepsGraphemeClusters(t *testing.T) {
	// A hard cut with no whitespace or punctuation in reach must land between user-perceived characters:
	// never inside a ZWJ sequence, a flag, a skin-tone or keycap sequence, or a letter with its marks.
	family := "\U0001F468\u200D\U0001F469\u200D\U0001F467\u200D\U0001F466"
	flag := "\U0001F1EF\U0001F1F5"
	thumbs := "\U0001F44D\U0001F3FD"
	keycap := "1\uFE0F\u20E3"
	accent := "e\u0301"
	vowelSign := "\u0915\u093F" // a spacing vowel sign joins the consonant before it
	halfwidth := "\uFF76\uFF9E" // a halfwidth voiced sound mark joins the kana before it
	tagFlag := "\U0001F3F4\U000E0067\U000E0062\U000E0073\U000E0063\U000E0074\U000E007F"
	repeat := func(units ...string) []string {
		var out []string
		for size := 0; size < 520; {
			for _, u := range units {
				out = append(out, u)
				size += len([]rune(u))
			}
		}
		return out
	}
	cases := []struct {
		name     string
		clusters []string
		fenced   bool
	}{
		{"zwj family", repeat(family), false},
		{"flags", repeat(flag), false},
		{"skin tone", repeat(thumbs), false},
		{"keycap", repeat(keycap), false},
		{"combining accent", repeat(accent), false},
		{"spacing vowel sign", repeat(vowelSign), false},
		{"halfwidth sound mark", repeat(halfwidth), false},
		{"tag sequence", repeat(tagFlag), false},
		{"mixed", repeat(family, flag, thumbs, keycap, accent, vowelSign, halfwidth, tagFlag, "x"), false},
		{"mixed in a code block", repeat(family, flag, keycap, accent, "x"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { assertClusterSeams(t, tc.clusters, tc.fenced) })
	}
	// One cluster longer than a piece still hard-cuts at the limit instead of shrinking every piece to one rune.
	long := "e" + strings.Repeat("\u0301", 300)
	for n := 200; n <= 212; n++ {
		parts := Split(long, n)
		if len(parts) != 2 || strings.Join(parts, "") != long {
			t.Fatalf("n=%d: over-long cluster split into %d pieces", n, len(parts))
		}
		for i, p := range parts {
			if l := len([]rune(p)); l > n {
				t.Fatalf("n=%d: piece %d has %d runes", n, i, l)
			}
		}
	}
}

// assertClusterSeams splits the clusters' concatenation (inside a code block when fenced) at n = 200..212 and
// checks every piece fits, the pieces reassemble the source, and every seam falls between two clusters.
func assertClusterSeams(t *testing.T, clusters []string, fenced bool) {
	body := strings.Join(clusters, "")
	text := body
	if fenced {
		text = "```\n" + body + "\n```"
	}
	boundary := map[int]bool{} // rune offsets in body between two clusters
	for off, i := 0, 0; i < len(clusters); i++ {
		off += len([]rune(clusters[i]))
		boundary[off] = true
	}
	for n := 200; n <= 212; n++ {
		parts := Split(text, n)
		if len(parts) < 2 {
			t.Fatalf("n=%d: not split: %d piece(s)", n, len(parts))
		}
		joined, off := "", 0
		for i, p := range parts {
			if l := len([]rune(p)); l > n {
				t.Fatalf("n=%d: piece %d has %d runes", n, i, l)
			}
			if fenced { // drop each piece's opening or reopened fence and its closing fence
				lines := strings.Split(p, "\n")
				if len(lines) < 2 || !bareFence(lines[0]) || !bareFence(lines[len(lines)-1]) {
					t.Fatalf("n=%d: piece %d is not one fenced block: %q", n, i, p)
				}
				p = strings.Join(lines[1:len(lines)-1], "\n")
			}
			joined += p
			off += len([]rune(p))
			if i < len(parts)-1 && off > 0 && !boundary[off] {
				r := []rune(body)
				t.Fatalf("n=%d: piece %d ends inside a cluster at rune %d: ...%q | %q...",
					n, i, off, string(r[max(off-4, 0):off]), string(r[off:min(off+4, len(r))]))
			}
		}
		if joined != body {
			t.Fatalf("n=%d: pieces do not reassemble the source", n)
		}
	}
}
