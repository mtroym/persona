package channel

import (
	"strings"
	"unicode"
)

// Split cuts long Markdown into pieces of at most n runes (no limit when n <= 0), at line breaks where it can
// and never inside a code block without closing and reopening it. Whitespace-only input is returned as one piece.
func Split(s string, n int) []string {
	if n <= 0 || len([]rune(s)) <= n {
		return []string{s}
	}
	var out []string
	var cur strings.Builder
	curLen := 0
	fenceMark := "" // opening fence run, e.g. "~~~" or "````"
	fenceHead := "" // trimmed opening line, used to reopen the block on the next piece
	degraded := false
	room := n - 8 // leave room for closing the piece's fence
	flush := func() {
		if curLen == 0 {
			return
		}
		piece := cur.String()
		if fenceMark != "" {
			piece = strings.TrimRight(piece, "\n") + "\n" + fenceMark
		}
		if text := strings.TrimRight(piece, "\n"); text != "" {
			out = append(out, text)
		}
		cur.Reset()
		curLen = 0
		if fenceMark != "" {
			cur.WriteString(fenceHead + "\n")
			curLen = len([]rune(fenceHead)) + 1
		}
	}
	// degrade stops balancing fences and hard-cuts the rest as plain text. flush closes the current piece,
	// then its reopen prefix is discarded; reachable only for tiny n, and every piece stays within n.
	degrade := func() {
		flush()
		cur.Reset() // discard the reopen prefix flush wrote: no fence follows after degradation
		curLen = 0
		fenceMark, fenceHead, degraded = "", "", true
		room = max(n, 1)
	}
	for _, line := range strings.SplitAfter(s, "\n") {
		runes := []rune(line)
		m, head, bare := "", "", false
		if !degraded {
			m, head, bare = fenceLine(strings.TrimRight(line, "\n"))
		}
		opens := fenceMark == "" && m != ""
		closes := fenceMark != "" && m != "" && m[0] == fenceMark[0] && len(m) >= len(fenceMark) && bare
		if opens {
			if curLen > 0 && curLen+len(runes)+len(m)+1 > n {
				flush() // prose before the opener starts its own piece, which must have room for a close
			}
			if len(runes)+len(m)+1 > n { // opener plus an injected close cannot fit one piece: give up
				degrade()
				opens = false
			}
		} else if closes {
			if len([]rune(fenceHead))+1+len(runes) > n { // reopen plus whole closer cannot fit: never hard-cut a closer
				degrade()
				closes = false
			} else if curLen+len(runes) > n {
				flush() // next piece starts with the reopen, then carries the whole closer
			}
		}
		if !opens && !closes {
			if curLen+len(runes) > room {
				flush()
			}
			danglingN := 0                 // opener run length of a backtick span a previous cut entered
			prefixRun := 0                 // trailing backtick run length of the previous piece, joining this one's lead
			for curLen+len(runes) > room { // one line longer than a piece: cut it
				cut := room - curLen
				if cut <= 0 {
					degrade()
					danglingN, prefixRun = 0, 0
					continue
				}
				cut, danglingN = safeCut(runes, cut, room, fenceMark == "", danglingN, prefixRun) // prose spans are kept whole; code blocks stay as-is
				cur.WriteString(string(runes[:cut]))
				curLen += cut
				prefixRun = 0 // a run ending at the seam joins this fragment's leading run
				for t := cut - 1; t >= 0 && runes[t] == '`'; t-- {
					prefixRun++
				}
				runes = runes[cut:]
				flush()
			}
		}
		cur.WriteString(string(runes))
		curLen += len(runes)
		if m != "" && !degraded { // degrade on this very line invalidates its own marker
			switch {
			case fenceMark == "":
				fenceMark = m
				fenceHead = head // cap the reopen copy so a long info string cannot eat the next piece's budget
				if r := []rune(head); len(r) > max(len(m), 20) {
					fenceHead = string(r[:max(len(m), 20)])
				}
				room = n - len(m) - 1 // bound the piece so its injected close fits
			case closes:
				fenceMark, fenceHead = "", "" // same char, at least as long, only spaces after it
				room = n - 8
			}
		}
	}
	if curLen > 0 {
		fenceMark = ""
		if text := strings.TrimRight(cur.String(), "\n"); text != "" {
			out = append(out, text)
		}
	}
	if len(out) == 0 {
		out = []string{s} // whitespace-only input still yields one piece, like the short-input path
	}
	return out
}

// fenceLine reports the code fence a line starts: up to three leading spaces, then >= 3 identical backticks or
// tildes (CommonMark). It returns the run, the trimmed run+info line, and whether only spaces (or a CRLF CR) follow.
func fenceLine(line string) (marker, head string, bare bool) {
	i := 0
	for i < len(line) && line[i] == ' ' {
		if i == 3 { // four or more leading spaces make an indented code block, not a fence
			return "", "", false
		}
		i++
	}
	if i >= len(line) {
		return "", "", false
	}
	c := line[i]
	if c != '`' && c != '~' {
		return "", "", false
	}
	start := i
	for i < len(line) && line[i] == c {
		i++
	}
	if i-start < 3 {
		return "", "", false
	}
	rest := strings.TrimRight(line[i:], " \r") // CommonMark: only spaces may follow the run; CR comes from CRLF
	return line[start:i], strings.TrimSpace(line[start:]), rest == ""
}

// safeCut chooses how many leading runes of an over-long line the current piece takes (1 <= k <= max),
// backing off the fixed limit to whitespace then punctuation (CJK included). In prose it keeps inline
// spans (backtick code, [...] links, <...> tags, &...; entities) whole when they fit a fresh piece
// (room), otherwise hard-cuts at the fixed limit. Either way the cut then backs off to a grapheme
// cluster boundary. The returned int is the opener length still open.
func safeCut(line []rune, max, room int, spans bool, danglingN, prefixRun int) (k, nextDangling int) {
	k, foundSpace := max, false
	for i := max; i >= 1; i-- { // prefer a boundary right after whitespace
		if c := line[i-1]; c == ' ' || c == '\t' {
			k, foundSpace = i, true
			break
		}
	}
	if !foundSpace { // otherwise right after punctuation
		for i := max; i >= 1; i-- {
			if boundaryPunct(line[i-1]) {
				k = i
				break
			}
		}
	}
	if !spans {
		return graphemeSafe(line, k), 0
	}
	closed, open, _ := scanSpans(line, max, danglingN, prefixRun)
	for _, sp := range closed { // a boundary inside a complete span must not cut it
		if sp.start < k && k < sp.start+sp.length {
			if sp.start >= 1 {
				k = sp.start // move before the span so it starts one piece whole
			} else {
				k = max // the span leads the remainder and cannot be pulled
			}
			break
		}
	}
	for _, sp := range open { // the cut enters a span whose closing rune is beyond it
		if sp.start < k && k < sp.start+sp.length {
			if sp.start >= 1 && sp.length <= room {
				k = sp.start // start the next piece with the whole span; else hard-cut at the fixed limit
			}
			break
		}
	}
	k = graphemeSafe(line, k) // span openers are never extenders: a span moved to the next piece stays whole
	// backtick state is replayed at the real cut (not the limit), so a closer the cut leaves behind
	// cannot be consumed early.
	_, _, nextDangling = scanSpans(line, k, danglingN, prefixRun)
	return k, nextDangling
}

// graphemeSafe moves the cut k left to the nearest grapheme cluster boundary, so a piece never ends inside
// a ZWJ sequence, a flag, a skin-tone or keycap sequence, or a letter with its marks (a close approximation
// of UAX #29 that needs no tables). When no cut in 1..k is a boundary, one cluster outgrows the whole piece
// and k stays a hard cut, so every piece still makes progress within the limit.
func graphemeSafe(line []rune, k int) int {
	for j := k; j >= 1; j-- {
		if clusterBoundary(line, j) {
			return j
		}
	}
	return k
}

// clusterBoundary reports whether a cut between line[j-1] and line[j] falls between two grapheme clusters.
func clusterBoundary(line []rune, j int) bool {
	if j <= 0 || j >= len(line) {
		return true
	}
	if graphemeExtender(line[j]) || line[j-1] == '\u200D' { // ZWJ glues the pictograph after it
		return false
	}
	if !regionalIndicator(line[j]) {
		return true
	}
	before := 0 // flags pair regional indicators from the start of their run
	for i := j - 1; i >= 0 && regionalIndicator(line[i]); i-- {
		before++
	}
	return before%2 == 0
}

// graphemeExtender reports runes that never start a grapheme cluster: combining, enclosing and spacing
// marks (variation selectors and the keycap U+20E3 among them), ZWJ and ZWNJ, emoji skin tones, emoji tag
// characters and the halfwidth kana sound marks.
func graphemeExtender(r rune) bool {
	switch {
	case r < 0x0300:
		return false // fast path: nothing below the combining diacritics extends a cluster
	case r == '\u200C', r == '\u200D', r == '\uFF9E', r == '\uFF9F':
		return true
	case r >= 0x1F3FB && r <= 0x1F3FF, r >= 0xE0020 && r <= 0xE007F:
		return true
	}
	return unicode.Is(unicode.M, r)
}

func regionalIndicator(r rune) bool { return r >= 0x1F1E6 && r <= 0x1F1FF }

type spanRange struct {
	start  int
	length int
}

// scanSpans replays the inline-span grammar over line[:lim], returning spans fully inside it, spans
// closing beyond it (both ordered by start), and the backtick opener length still open after it. Code
// spans never nest, so '[', '<', '&' and other-length runs inside one are literal. danglingN names a
// doomed span a previous cut entered (only a same-length run closes it); prefixRun joins the previous
// piece's trailing run onto this fragment's lead run; a run crossing the cut stays undecided.
func scanSpans(line []rune, lim, danglingN, prefixRun int) (closed, open []spanRange, pending int) {
	btStart, btN := -1, 0 // the single active backtick span in this fragment
	for i := 0; i < lim; {
		switch r := line[i]; {
		case r == '`':
			j := i + 1
			for j < len(line) && line[j] == '`' {
				j++
			}
			if j > lim { // the run crosses the cut: the next fragment measures it whole
				i = lim
				continue
			}
			n := j - i
			if i == 0 {
				n += prefixRun
			}
			switch {
			case danglingN > 0 && n == danglingN:
				danglingN = 0 // closer of the doomed span cut in a previous piece
			case danglingN > 0:
				// a different-length run is literal content of that open doomed span
			case btStart >= 0 && n == btN:
				closed = append(closed, spanRange{btStart, j - btStart})
				btStart, btN = -1, 0
			case btStart < 0:
				btStart, btN = i, n
			}
			i = j
		case btStart >= 0 || danglingN > 0:
			i++ // markup inside a code span is literal
		case r == '[':
			if e, ok := linkEnd(line, i); ok && e <= lim {
				closed = append(closed, spanRange{i, e - i})
				i = e
			} else if ok {
				open = append(open, spanRange{i, e - i})
				i++
			} else {
				open = append(open, spanRange{i, len(line) - i})
				i++
			}
		case r == '<':
			if e := indexRune(line, '>', i+1); e >= 0 && e+1 <= lim {
				closed = append(closed, spanRange{i, e + 1 - i})
				i = e + 1
			} else if e >= 0 {
				open = append(open, spanRange{i, e + 1 - i})
				i++
			} else {
				open = append(open, spanRange{i, len(line) - i})
				i++
			}
		case r == '&':
			if e := indexRune(line, ';', i+1); e >= 0 && e+1 <= lim {
				closed = append(closed, spanRange{i, e + 1 - i})
				i = e + 1
			} else if e >= 0 {
				open = append(open, spanRange{i, e + 1 - i})
				i++
			} else {
				i++ // a bare '&' with no ';' is literal text
			}
		default:
			i++
		}
	}
	if btStart >= 0 { // an opener with no mate inside the limit: measure the real mate anywhere in line
		sp := spanRange{btStart, len(line) - btStart}
		if e := closeBacktickRun(line, btStart+btN, btN); e >= 0 {
			sp.length = e - btStart
		}
		pos := 0 // keep open ordered by start
		for pos < len(open) && open[pos].start < sp.start {
			pos++
		}
		open = append(open, spanRange{})
		copy(open[pos+1:], open[pos:])
		open[pos] = sp
	}
	return closed, open, danglingN + btN // exactly one of danglingN and btN is nonzero when pending
}

// closeBacktickRun returns the index past the first later run of exactly n backticks, else -1; runs are atomic.
func closeBacktickRun(line []rune, from, n int) int {
	for i := from; i < len(line); {
		if line[i] != '`' {
			i++
			continue
		}
		j := i
		for j < len(line) && line[j] == '`' {
			j++
		}
		if j-i == n {
			return j
		}
		i = j
	}
	return -1
}

// linkEnd returns the exclusive end of a [...] or [...](...) link starting at i, or ok=false if it never closes.
func linkEnd(line []rune, i int) (end int, ok bool) {
	j := indexRune(line, ']', i+1)
	if j < 0 {
		return -1, false
	}
	if j+1 < len(line) && line[j+1] == '(' {
		e := indexRune(line, ')', j+2)
		if e < 0 {
			return -1, false
		}
		return e + 1, true
	}
	return j + 1, true
}

func indexRune(line []rune, want rune, from int) int {
	for i := from; i < len(line); i++ {
		if line[i] == want {
			return i
		}
	}
	return -1
}

func boundaryPunct(r rune) bool {
	switch r {
	case '.', ',', '!', '?', ';', ':', ')', ']', '}', '…', '—':
		return true
	case '。', '，', '、', '；', '：', '！', '？', '）', '】', '》', '」', '』', '．', '–':
		return true
	}
	return false
}
