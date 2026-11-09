package assistant

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	mrand "math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The history import's driver against a fake source and a fake clock: nothing here reaches a real service, and every
// wait is instant (the clock moves instead).

type historyClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *historyClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *historyClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func (c *historyClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.Advance(d)
	return nil
}

// fakeHistory is a connector whose conversations have consecutive numeric positions (0 the oldest), answering
// limit+5 messages paging back and limit/2+5 on each side around a position, like a real source measured
// while designing the import.
type fakeHistory struct {
	mu       sync.Mutex
	clock    *historyClock
	info     assistantwire.ArchiveSource
	chats    []connectorwire.HistoryChat
	convs    map[string][]kb.SourceMessage
	anchors  map[int][]connectorwire.HistoryAnchor // by year
	took     time.Duration                         // how long each call takes (on the clock)
	real     time.Duration                         // and in real time (for the in-flight test)
	hook     func(n int, q connectorwire.HistoryQuery) (connectorwire.HistoryPage, error, bool)
	media    func(ref string) (connector.Media, error)
	calls    []string
	queries  []connectorwire.HistoryQuery
	pages    int
	archives int
	inflight atomic.Int32
	most     atomic.Int32

	// anchorHook, when set, answers history.anchors instead of the year's anchors (ok false: it does not).
	anchorHook func(q connectorwire.HistoryAnchorsQuery) (connectorwire.HistoryAnchors, error, bool)
}

func (f *fakeHistory) enter() func() {
	if n := f.inflight.Add(1); n > f.most.Load() {
		f.most.Store(n)
	}
	if f.real > 0 {
		time.Sleep(f.real) // sleep-ok: holds the call open in real time, so a second call made meanwhile would overlap and be counted
	}
	if f.clock != nil {
		f.clock.Advance(f.took)
	}
	return func() { f.inflight.Add(-1) }
}

func (f *fakeHistory) Archives(context.Context) ([]assistantwire.ArchiveSource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.archives++
	f.calls = append(f.calls, "archives")
	return []assistantwire.ArchiveSource{f.info}, nil
}

func (f *fakeHistory) Chats(context.Context, string) ([]connectorwire.HistoryChat, error) {
	defer f.enter()()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "chats")
	return slices.Clone(f.chats), nil
}

func (f *fakeHistory) Page(_ context.Context, _ string, q connectorwire.HistoryQuery) (connectorwire.HistoryPage, error) {
	defer f.enter()()
	f.mu.Lock()
	f.pages++
	n := f.pages
	f.calls = append(f.calls, "page "+q.Chat)
	f.queries = append(f.queries, q)
	hook := f.hook
	msgs := f.convs[q.Chat]
	f.mu.Unlock()
	if hook != nil {
		if p, err, ok := hook(n, q); ok {
			return p, err
		}
	}
	if msgs == nil {
		return connectorwire.HistoryPage{Unreadable: true}, nil
	}
	limit := max(q.Limit, 1)
	last := len(msgs) - 1
	lo, hi := 0, -1
	switch {
	case q.Around != "":
		p, _ := strconv.Atoi(q.Around)
		side := limit/2 + 5
		lo, hi = max(0, p-side), min(last, p+side)
	case q.After != "":
		a, _ := strconv.Atoi(q.After)
		lo, hi = a+1, min(last, a+limit+5)
	default:
		b := len(msgs)
		if q.Before != "" {
			b, _ = strconv.Atoi(q.Before)
		}
		lo, hi = max(0, b-limit-5), min(last, b-1)
	}
	page := connectorwire.HistoryPage{}
	if lo > hi {
		return page, nil
	}
	page.Messages = slices.Clone(msgs[lo : hi+1])
	if lo > 0 {
		page.More, page.Older = true, strconv.Itoa(lo)
	}
	if hi < last {
		page.Newer = strconv.Itoa(hi)
	}
	return page, nil
}

func (f *fakeHistory) Anchors(_ context.Context, _ string, q connectorwire.HistoryAnchorsQuery) (connectorwire.HistoryAnchors, error) {
	defer f.enter()()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf("anchors %d", q.Since.Year()))
	if f.anchorHook != nil {
		if a, err, ok := f.anchorHook(q); ok {
			return a, err
		}
	}
	return connectorwire.HistoryAnchors{Anchors: f.anchors[q.Since.Year()]}, nil
}

func (f *fakeHistory) Media(_ context.Context, _ string, ref string) (connector.Media, error) {
	defer f.enter()()
	f.mu.Lock()
	f.calls = append(f.calls, "media "+ref)
	media := f.media
	f.mu.Unlock()
	if media != nil {
		return media(ref)
	}
	return connector.Media{Mime: "image/png", Data: []byte("png")}, nil
}

func (f *fakeHistory) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// historyConv is a conversation of n messages, one an hour from start; mine are the owner's.
func historyConv(chat string, n int, start time.Time, step time.Duration, mine ...int) []kb.SourceMessage {
	out := make([]kb.SourceMessage, n)
	for i := range out {
		out[i] = kb.SourceMessage{ID: fmt.Sprintf("%s-%d", chat, i), Position: strconv.Itoa(i), SenderID: "u1", SenderName: "Alice Example",
			Type: "TEXT", Text: fmt.Sprintf("SECRET text %s #%d.", chat, i), At: start.Add(time.Duration(i) * step)}
		if slices.Contains(mine, i) {
			out[i].Mine = true
		}
	}
	return out
}

type historyTest struct {
	t       *testing.T
	d       *historyDriver
	e       *kb.Engine
	src     *fakeHistory
	clock   *historyClock
	cfg     *assistantwire.KBHistory
	data    string
	notices []string
	sel     atomic.Pointer[kb.Selection] // the engine's selection (the defaults to begin with)
}

// selection changes the engine's selection (a new copy, as the manager does).
func (ht *historyTest) selection(f func(*kb.Selection)) {
	ht.t.Helper()
	next := ht.sel.Load().Clone()
	f(&next)
	if err := next.Validate(); err != nil {
		ht.t.Fatal(err)
	}
	ht.sel.Store(&next)
}

// item gives a chat of the test source a per-item setting: "full" stores it all, "anchor" only what the personal rules
// keep, "off" reads nothing of it.
func (ht *historyTest) item(id, mode string) {
	ht.t.Helper()
	it := kb.SelectionItem{Source: testSource, Kind: kb.ItemChat, ID: id}
	switch mode {
	case "full":
		it.Read, it.Store = true, true
	case "anchor":
		it.Read = true
	case "off":
	default:
		ht.t.Fatalf("mode %q", mode)
	}
	ht.selection(func(s *kb.Selection) { s.SetItem(it) })
}

const testSource = "chat"

func newHistoryTest(t *testing.T, src *fakeHistory) *historyTest {
	t.Helper()
	data := filepath.Join(t.TempDir(), "data")
	st, err := kb.NewStore(data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Rezone(time.UTC); err != nil {
		t.Fatal(err)
	}
	clock := &historyClock{t: time.Now().UTC().Truncate(time.Second)}
	src.clock = clock
	if src.info.ID == "" {
		src.info = assistantwire.ArchiveSource{ID: testSource, History: true}
	}
	f := false
	ht := &historyTest{t: t, src: src, clock: clock, data: data, e: &kb.Engine{Store: st, TextDays: 3650},
		cfg: &assistantwire.KBHistory{Enabled: true, Sources: []assistantwire.KBHistorySource{{Name: testSource, Profile: "standard", NeedsSearchIndex: &f, PageLimit: 10}}}}
	var running atomic.Pointer[string]
	ht.d = &historyDriver{data: data, src: src,
		eng:       func(context.Context) (*kb.Engine, error) { return ht.e, nil },
		cfg:       func(context.Context) *assistantwire.KBHistory { return ht.cfg },
		connected: func() bool { return true },
		now:       clock.Now, sleep: clock.Sleep, rng: mrand.New(mrand.NewPCG(1, 2)), logf: t.Logf,
		notify: func(_ context.Context, source string, why historyReason) {
			ht.notices = append(ht.notices, source+": "+why.String())
		},
		calls:    &sync.Mutex{},
		instance: "test-1", indexed: func() bool { return false }, running: &running, timeout: time.Minute}
	def := kb.Defaults()
	ht.sel.Store(&def)
	ht.e.Selection = ht.sel.Load
	return ht
}

// plan lists the chats, gives the ones in modes their per-item setting (ht.item) and approves the plan (what
// kb_history_plan and kb_history_resume do).
func (ht *historyTest) plan(approve bool, modes map[string]string) *historyPlan {
	ht.t.Helper()
	p, ok, err := loadHistoryPlan(ht.data, testSource)
	if err != nil || !ok {
		p = &historyPlan{Source: testSource}
	}
	p.regenerate(ht.src.chats, ht.clock.Now())
	for id, m := range modes {
		ht.item(id, m)
	}
	if approve {
		ht.approve(p)
	} else {
		p.revoke()
	}
	if err := saveHistoryPlan(ht.data, p); err != nil {
		ht.t.Fatal(err)
	}
	return p
}

// approve approves p as the selection reads it now.
func (ht *historyTest) approve(p *historyPlan) {
	ht.t.Helper()
	sel, ret := historySelection(ht.e)
	p.approve(sel, ret, ht.clock.Now())
}

func (ht *historyTest) run() {
	ht.t.Helper()
	if err := ht.d.runOnce(context.Background()); err != nil {
		ht.t.Fatal(err)
	}
}

// text is everything archived of a chat.
func (ht *historyTest) text(chat string) string {
	ht.t.Helper()
	dir := ht.e.Store.Path(kb.KindChat, kb.ChatID(testSource, chat), "messages")
	entries, _ := os.ReadDir(dir)
	var b strings.Builder
	for _, e := range entries {
		raw, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		b.Write(raw)
	}
	return b.String()
}

func (ht *historyTest) state(chat string) kb.HistoryState {
	ht.t.Helper()
	st, _, err := ht.e.Store.HistoryState(kb.ChatID(testSource, chat))
	if err != nil {
		ht.t.Fatal(err)
	}
	return st
}

var keptNumber = regexp.MustCompile(`#(\d+)\.`)

// kept are the positions of a chat's archived messages, each once (a repeat fails the test).
func (ht *historyTest) kept(chat string) []int {
	ht.t.Helper()
	var out []int
	seen := map[int]bool{}
	for _, m := range keptNumber.FindAllStringSubmatch(ht.text(chat), -1) {
		n, _ := strconv.Atoi(m[1])
		if seen[n] {
			ht.t.Fatalf("message #%d archived twice", n)
		}
		seen[n] = true
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

func span(lo, hi int) []int {
	var out []int
	for i := lo; i <= hi; i++ {
		out = append(out, i)
	}
	return out
}

func recentStart(n int, step time.Duration) time.Time {
	return time.Now().UTC().Add(-time.Duration(n+24) * step).Truncate(time.Hour)
}

// ---- plans ----

// A plan lists the source's chats; what is read of each is the selection's: with the defaults direct messages whole,
// every other chat only around the owner's messages and mentions (the personal rules rank above the kind rules), and
// a per-item setting over both. Regenerating takes new names, kinds, flags and estimates and keeps chats the list no
// longer has.
func TestHistoryPlanByTheSelection(t *testing.T) {
	now := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	p := &historyPlan{Source: "x"}
	p.regenerate([]connectorwire.HistoryChat{
		{ID: "d", Name: "D", Kind: "dm", Estimate: 120},
		{ID: "g", Name: "G", Kind: "group", Estimate: 9000},
		{ID: "m", Name: "M", Kind: "group", Muted: true},
		{ID: "o", Name: "O", Kind: "official"},
		{ID: "b", Name: "B", Kind: "dm", Bot: true},
		{ID: "s", Name: "S", Kind: "system"},
		{ID: "w", Name: "W", Kind: "made-up"},
	}, now)
	sel, ret := kb.Defaults(), kb.Retention{TextDays: 365}
	read := func(id string) string {
		switch need := historyNeed(&sel, ret, "x", *p.chat(id)); {
		case need.Whole:
			return "whole"
		case need.Read:
			return "personal"
		}
		return "none"
	}
	want := map[string]string{"d": "whole", "g": "personal", "m": "personal", "o": "personal", "b": "personal", "s": "personal", "w": "personal"}
	for id, r := range want {
		if got := read(id); got != r {
			t.Fatalf("%s: %s, want %s", id, got, r)
		}
	}
	sel.SetItem(kb.SelectionItem{Source: "x", Kind: kb.ItemChat, ID: "m", Read: true, Store: true})
	sel.SetItem(kb.SelectionItem{Source: "x", Kind: kb.ItemChat, ID: "d", Read: false})
	p.regenerate([]connectorwire.HistoryChat{
		{ID: "m", Name: "M renamed", Kind: "group", Muted: true, Collapsed: true, Estimate: 5},
		{ID: "g", Name: "G", Kind: "group", Estimate: 9100},
		{ID: "n", Name: "N", Kind: "dm"},
	}, now.Add(time.Hour))
	if m := p.chat("m"); m.Name != "M renamed" || m.Estimate != 5 || !m.Collapsed || read("m") != "whole" {
		t.Fatalf("m %+v %s", m, read("m"))
	}
	if read("d") != "none" || read("n") != "whole" || p.chat("g").Estimate != 9100 || len(p.Chats) != 8 {
		t.Fatalf("d %s n %s chats %d", read("d"), read("n"), len(p.Chats))
	}
	g := p.grouped(&sel, ret)
	if len(g.whole) != 2 || g.whole[0].ID != "m" || len(g.none) != 1 || len(g.personal) != 5 || g.personal[0].ID != "g" { // m (5) before n (0)
		t.Fatalf("groups %+v", g)
	}
	// back_days bounds how far back the import goes, whatever the rule's days.
	sel.History = map[string]kb.HistorySelection{"x": {BackDays: new(int)}}
	*sel.History["x"].BackDays = 30
	if need := historyNeed(&sel, ret, "x", *p.chat("g")); need.Days != 30 {
		t.Fatalf("back days: %+v", need)
	}
}

// Off by default: no kb.history, or not enabled, and nothing is asked of any connector.
func TestHistoryOffNeverCallsConnectors(t *testing.T) {
	src := &fakeHistory{chats: []connectorwire.HistoryChat{{ID: "a", Kind: "dm"}}, convs: map[string][]kb.SourceMessage{"a": historyConv("a", 30, recentStart(30, time.Hour), time.Hour)}}
	ht := newHistoryTest(t, src)
	ht.plan(true, nil)
	for _, cfg := range []*assistantwire.KBHistory{nil, {Enabled: false, Sources: ht.cfg.Sources}} {
		ht.cfg = cfg
		ht.run()
	}
	if len(src.calls) != 0 {
		t.Fatalf("calls while off: %v", src.calls)
	}
	// Enabled, but the source is not listed: still nothing.
	ht.cfg = &assistantwire.KBHistory{Enabled: true}
	ht.run()
	if len(src.calls) != 0 {
		t.Fatalf("calls for an unlisted source: %v", src.calls)
	}
}

// A plan not approved (or none at all) runs nothing, and neither does a source waiting for the search index.
func TestHistoryUnapprovedPlanNeverCalls(t *testing.T) {
	src := &fakeHistory{chats: []connectorwire.HistoryChat{{ID: "a", Kind: "dm"}}, convs: map[string][]kb.SourceMessage{"a": historyConv("a", 30, recentStart(30, time.Hour), time.Hour)}}
	ht := newHistoryTest(t, src)
	ht.run() // no plan
	ht.plan(false, nil)
	ht.run()
	if len(src.calls) != 0 {
		t.Fatalf("calls without approval: %v", src.calls)
	}
	ht.plan(true, nil)
	ht.cfg.Sources[0].NeedsSearchIndex = nil // the default: it waits for the index
	ht.run()
	if len(src.calls) != 0 {
		t.Fatalf("calls while waiting for the index: %v", src.calls)
	}
	ht.d.indexed = func() bool { return true }
	ht.run()
	if len(ht.kept("a")) != 30 {
		t.Fatalf("kept %v", ht.kept("a"))
	}
}

// ---- full ----

// Full mode reads backwards from the newest page by the cursor each page answers (limit+5 messages a page), to
// position 0, keeping every message once; the chat is done and a second run reads nothing.
func TestHistoryFullReadsToTheStart(t *testing.T) {
	const n = 237
	src := &fakeHistory{chats: []connectorwire.HistoryChat{{ID: "a", Name: "Alice Example", Kind: "dm", Estimate: n}},
		convs: map[string][]kb.SourceMessage{"a": historyConv("a", n, recentStart(n, time.Hour), time.Hour)}}
	ht := newHistoryTest(t, src)
	ht.plan(true, nil)
	ht.run()
	if got := ht.kept("a"); !slices.Equal(got, span(0, n-1)) {
		t.Fatalf("kept %d: %v", len(got), got)
	}
	st := ht.state("a")
	if st.State != kb.HistoryDone || st.Mode != kb.HistoryFull || st.Read != n || st.Written != n || st.Cursor != "" {
		t.Fatalf("state %+v", st)
	}
	pages := src.count("page")
	if want := (n + 14) / 15; pages != want {
		t.Fatalf("%d pages, want %d", pages, want)
	}
	prev := n + 1
	for _, q := range src.queries {
		b := n
		if q.Before != "" {
			b, _ = strconv.Atoi(q.Before)
		}
		if b >= prev || q.Limit != 10 {
			t.Fatalf("queries %+v", src.queries)
		}
		prev = b
	}
	c, ok := ht.e.Store.Chat(kb.ChatID(testSource, "a"))
	if !ok || c.Pending != 0 || c.HistoryMode != kb.HistoryFull || c.Messages != n {
		t.Fatalf("chat %+v", c)
	}
	ht.run()
	if src.count("page") != pages {
		t.Fatal("a finished chat was read again")
	}
}

// Images of a page are downloaded through archive_media, paced as calls of their own; only pictures are written.
func TestHistoryFullDownloadsImages(t *testing.T) {
	start := recentStart(20, time.Hour)
	msgs := historyConv("a", 20, start, time.Hour)
	msgs[5].Type, msgs[5].Media = "IMAGE", []string{"img-5"}
	msgs[7].Type, msgs[7].Media = "IMAGE", []string{"not-a-picture"}
	msgs[9].Type, msgs[9].Media = "FILE", []string{"doc.pdf"}
	src := &fakeHistory{info: assistantwire.ArchiveSource{ID: testSource, History: true, Media: true},
		chats: []connectorwire.HistoryChat{{ID: "a", Kind: "dm"}}, convs: map[string][]kb.SourceMessage{"a": msgs},
		media: func(ref string) (connector.Media, error) {
			if ref == "not-a-picture" {
				return connector.Media{Mime: "video/mp4", Data: []byte("mp4")}, nil
			}
			return connector.Media{Mime: "image/png", Data: []byte("png")}, nil
		}}
	ht := newHistoryTest(t, src)
	ht.e.AttachmentDaysBySource = map[string]int{testSource: 3650}
	ht.plan(true, nil)
	ht.run()
	if src.count("media") != 2 {
		t.Fatalf("calls %v", src.calls)
	}
	text := ht.text("a")
	if !strings.Contains(text, "[附件](attachments/") || strings.Count(text, "[附件]") != 2 || !strings.Contains(text, "[文件]") { // i18n-ignore: archive data
		t.Fatalf("archived:\n%s", text)
	}
}

// ---- anchors, read backwards ----

// A source that cannot search: a group the selection reads only personally is read backwards through the filter:
// the mine rule's context (50) on each side of the owner's messages, windows merged, a mention kept on its own (it
// splits its gap), the gaps marked.
func TestHistoryAnchorStreaming(t *testing.T) {
	const n = 400
	src := &fakeHistory{chats: []connectorwire.HistoryChat{{ID: "g", Name: "Group", Kind: "group", Estimate: n}},
		convs: map[string][]kb.SourceMessage{"g": historyConv("g", n, recentStart(n, time.Hour), time.Hour, 100, 160, 330)}}
	src.convs["g"][250].Mentioned = true
	ht := newHistoryTest(t, src)
	ht.plan(true, nil)
	ht.run()
	want := append(append(span(50, 210), 250), span(280, 380)...)
	if got := ht.kept("g"); !slices.Equal(got, want) {
		t.Fatalf("kept %v", got)
	}
	text := ht.text("g")
	for _, line := range []string{"[……此处省略 19 条]", "[……此处省略 29 条]", "[……此处省略 39 条]", "[……此处省略 50 条]"} { // i18n-ignore: archive data
		if strings.Count(text, line) != 1 {
			t.Fatalf("want %q once in:\n%s", line, text)
		}
	}
	st := ht.state("g")
	if st.State != kb.HistoryDone || st.Mode != kb.HistoryAnchor || st.Read != n || st.Filter == nil || st.Filter.Windows != 2 || st.Filter.Kept != 1 {
		t.Fatalf("state %+v filter %+v", st, st.Filter)
	}
	if st.Plan == nil || st.Plan.Whole || !st.Plan.Mine || !st.Plan.Mentions {
		t.Fatalf("plan %+v", st.Plan)
	}
}

// ---- anchors, by position ----

func positionalSource(chat string, msgs []kb.SourceMessage, anchors ...int) *fakeHistory {
	src := &fakeHistory{info: assistantwire.ArchiveSource{ID: testSource, History: true, HistoryAnchors: true},
		chats: []connectorwire.HistoryChat{{ID: chat, Name: "Group", Kind: "group", Estimate: len(msgs)}},
		convs: map[string][]kb.SourceMessage{chat: msgs}, anchors: map[int][]connectorwire.HistoryAnchor{}}
	for _, p := range anchors {
		m := msgs[p]
		y := m.At.Year()
		src.anchors[y] = append(src.anchors[y], connectorwire.HistoryAnchor{Chat: chat, Position: m.Position, At: m.At, Mine: true, Message: &m})
	}
	return src
}

// A source that can search: windows of ±50 positions around the anchors it found, read around each anchor and then
// before or after until covered, merged where they overlap, nothing read twice; the gaps get omission lines counted
// from positions (between windows and below the oldest, none above the newest).
func TestHistoryPositionalWindows(t *testing.T) {
	const n = 1000
	start := recentStart(n, 10*time.Minute)
	msgs := historyConv("g", n, start, 10*time.Minute, 100, 160, 600)
	src := positionalSource("g", msgs, 100, 160, 600)
	src.convs["g"][640].Mentioned = true // not found by the search, read in a window: kept, but no window of its own
	// A hit that only mentions the owner: the message alone is read and kept.
	m := msgs[800]
	src.anchors[m.At.Year()] = append(src.anchors[m.At.Year()], connectorwire.HistoryAnchor{Chat: "g", Position: m.Position, At: m.At, Mention: true})
	ht := newHistoryTest(t, src)
	ht.plan(true, nil)
	ht.run()
	want := append(append(span(50, 210), span(550, 650)...), 800)
	if got := ht.kept("g"); !slices.Equal(got, want) {
		t.Fatalf("kept %v", got)
	}
	text := ht.text("g")
	for _, line := range []string{"[……此处省略 339 条]", "[……此处省略 149 条]", "[……此处省略 50 条]"} { // i18n-ignore: archive data
		if strings.Count(text, line) != 1 {
			t.Fatalf("want %q once in:\n%s", line, text)
		}
	}
	if strings.Count(text, "省略") != 3 { // i18n-ignore: archive data
		t.Fatalf("omission lines:\n%s", text)
	}
	// Every position was asked for at most once: the pages read overlap only where a source answers more.
	seen := map[int]int{}
	for _, q := range src.queries {
		if q.Around == "" && q.Before == "" && q.After == "" {
			t.Fatalf("the newest page was read: %+v", src.queries)
		}
	}
	for _, p := range want {
		seen[p]++
	}
	if src.count("page") > 26 { // 302 positions, 15 a page, from two anchors' pages around them
		t.Fatalf("%d pages for two windows: %+v", src.count("page"), src.queries)
	}
	st := ht.state("g")
	if st.State != kb.HistoryDone || st.Positions == nil || !st.Positions.WindowsDone {
		t.Fatalf("state %+v", st)
	}
	// The omission line between the windows sorts between them.
	i210 := strings.Index(text, "#210.")
	i339 := strings.Index(text, "省略 339") // i18n-ignore: archive data
	i550 := strings.Index(text, "#550.")
	if !(i210 < i339 && i339 < i550) {
		t.Fatalf("order %d %d %d:\n%s", i210, i339, i550, text)
	}
}

// A year the search found nothing in is not covered: its part of each anchor chat is read backwards through the
// filter, that year only.
func TestHistoryUncoveredYearFallsBack(t *testing.T) {
	// A fixed day late in the year, so this year has months of messages whenever the test runs.
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	y := now.Year()
	// Two messages a day from June two years before: the year before this one lies in the middle.
	start := time.Date(y-2, 6, 1, 8, 0, 0, 0, time.UTC)
	step := 12 * time.Hour
	n := int(now.Sub(start)/step) - 2
	msgs := historyConv("g", n, start, step)
	prevStart := time.Date(y-1, 1, 1, 0, 0, 0, 0, time.UTC)
	thisStart := time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC)
	firstPrev := int(prevStart.Sub(start)/step) + 1
	lastPrev := int(thisStart.Sub(start)/step) - 1
	mid := (firstPrev + lastPrev) / 2
	for _, p := range []int{50, mid, n - 100} {
		msgs[p].Mine = true
	}
	src := positionalSource("g", msgs, 50, n-100) // the search misses last year's
	ht := newHistoryTest(t, src)
	ht.clock.t = now
	ht.plan(true, nil)
	ht.run()
	want := append(append(span(0, 100), span(mid-50, mid+50)...), span(n-150, n-50)...)
	if got := ht.kept("g"); !slices.Equal(got, want) {
		t.Fatalf("kept %v, want windows around 50, %d and %d", got, mid, n-100)
	}
	st := ht.state("g")
	if st.State != kb.HistoryDone || len(st.Positions.Fallback) != 0 || st.Positions.Year != 0 {
		t.Fatalf("state %+v %+v", st, st.Positions)
	}
	if !strings.Contains(ht.text("g"), fmt.Sprintf("[……此处省略 %d 条]", mid-50-firstPrev)) { // i18n-ignore: archive data
		t.Fatalf("no omission line for the start of last year:\n%s", ht.text("g"))
	}
}

// An unreadable chat keeps only the anchors' own messages the search returned, and is not a failure.
func TestHistoryUnreadableChatKeepsTheHits(t *testing.T) {
	msgs := historyConv("gone", 300, recentStart(300, time.Hour), time.Hour, 120)
	src := positionalSource("gone", msgs, 120)
	delete(src.convs, "gone") // every read answers unreadable
	src.chats = nil           // and the chat list no longer has it: the search found it
	ht := newHistoryTest(t, src)
	ht.plan(true, nil)
	ht.run()
	// The search found a chat the plan does not list: it is added, the approval taken back and the owner told;
	// nothing of it is read before the owner approves the plan with it.
	p, _, _ := loadHistoryPlan(ht.data, testSource)
	sel, ret := historySelection(ht.e)
	if c := p.chat("gone"); c == nil || c.Kind != historyUnlisted || !historyNeed(sel, ret, testSource, *c).Personal || p.ApprovedAt != nil {
		t.Fatalf("plan %+v", p)
	}
	if src.count("page") != 0 || len(ht.notices) != 1 || !strings.Contains(ht.notices[0], "plan_changed") {
		t.Fatalf("read before approval: %v, notices %v", src.calls, ht.notices)
	}
	ht.approve(p)
	if err := saveHistoryPlan(ht.data, p); err != nil {
		t.Fatal(err)
	}
	ht.notices = nil
	ht.run()
	if got := ht.kept("gone"); !slices.Equal(got, []int{120}) {
		t.Fatalf("kept %v", got)
	}
	if st := ht.state("gone"); st.State != kb.HistoryUnreadable {
		t.Fatalf("state %+v", st)
	}
	if _, stopped := readHistoryStop(ht.data); stopped || len(ht.notices) > 0 {
		t.Fatal("an unreadable chat stopped the import")
	}
}

// ---- pace, stops and errors ----

// retry_after pauses that long (plus a little), the same page is read again, and the import goes on.
func TestHistoryRetryAfterPauses(t *testing.T) {
	src := &fakeHistory{chats: []connectorwire.HistoryChat{{ID: "a", Kind: "dm"}}, convs: map[string][]kb.SourceMessage{"a": historyConv("a", 40, recentStart(40, time.Hour), time.Hour)}}
	var pacedAt, nextAt time.Time
	src.hook = func(n int, q connectorwire.HistoryQuery) (connectorwire.HistoryPage, error, bool) {
		switch n {
		case 2:
			pacedAt = src.clock.Now()
			return connectorwire.HistoryPage{Pace: &connectorwire.Pace{RetryAfterMS: 600_000}}, nil, true
		case 3:
			nextAt = src.clock.Now()
		case 4:
			return connectorwire.HistoryPage{}, &rpc.Error{Code: "rate_limited", Message: "slow down"}, true
		}
		return connectorwire.HistoryPage{}, nil, false
	}
	ht := newHistoryTest(t, src)
	ht.plan(true, nil)
	ht.run()
	if nextAt.Sub(pacedAt) < 10*time.Minute {
		t.Fatalf("read again after %v", nextAt.Sub(pacedAt))
	}
	if q := src.queries; q[1].Before != q[2].Before || q[3].Before != q[4].Before {
		t.Fatalf("the paced page was not read again: %+v", q)
	}
	if got := ht.kept("a"); len(got) != 40 {
		t.Fatalf("kept %v", got)
	}
	st := ht.d.status(testSource)
	if st.LastPause == nil || st.LastPause.Code != "transient" || st.LastPause.Args["code"] != "rate_limited" {
		t.Fatalf("status %+v", st)
	}
	if _, stopped := readHistoryStop(ht.data); stopped {
		t.Fatal("a pause stopped the import")
	}
}

// Pace.Stop, a timeout, an unknown code and a pause past two hours each stop the whole import: STOP written with the
// reason, a push, the chat paused, and not one more call (nothing retried).
func TestHistoryStopsWithoutRetrying(t *testing.T) {
	for name, answer := range map[string]func() (connectorwire.HistoryPage, error){
		"pace stop": func() (connectorwire.HistoryPage, error) {
			return connectorwire.HistoryPage{Pace: &connectorwire.Pace{Stop: true, Reason: "sign in again"}}, nil
		},
		"timeout": func() (connectorwire.HistoryPage, error) {
			return connectorwire.HistoryPage{}, context.DeadlineExceeded
		},
		"unknown": func() (connectorwire.HistoryPage, error) {
			return connectorwire.HistoryPage{}, &rpc.Error{Code: "brand_new_code"}
		},
		"coded timeout": func() (connectorwire.HistoryPage, error) {
			return connectorwire.HistoryPage{}, &rpc.Error{Code: "UPSTREAM-TIMEOUT"}
		},
		"no code": func() (connectorwire.HistoryPage, error) {
			return connectorwire.HistoryPage{}, errors.New("something broke")
		},
		"long pause": func() (connectorwire.HistoryPage, error) {
			return connectorwire.HistoryPage{Pace: &connectorwire.Pace{RetryAfterMS: 3 * 3600_000}}, nil
		},
		"circuit": func() (connectorwire.HistoryPage, error) {
			return connectorwire.HistoryPage{}, &rpc.Error{Code: "circuit_open"}
		},
		"conn closed": func() (connectorwire.HistoryPage, error) { return connectorwire.HistoryPage{}, rpc.ErrClosed },
	} {
		t.Run(name, func(t *testing.T) {
			src := &fakeHistory{chats: []connectorwire.HistoryChat{{ID: "a", Kind: "dm"}, {ID: "b", Kind: "dm"}},
				convs: map[string][]kb.SourceMessage{"a": historyConv("a", 60, recentStart(60, time.Hour), time.Hour), "b": historyConv("b", 60, recentStart(60, time.Hour), time.Hour)}}
			src.hook = func(n int, q connectorwire.HistoryQuery) (connectorwire.HistoryPage, error, bool) {
				if n == 2 {
					p, err := answer()
					return p, err, true
				}
				return connectorwire.HistoryPage{}, nil, false
			}
			ht := newHistoryTest(t, src)
			ht.plan(true, nil)
			ht.run()
			if got := src.count("page"); got != 2 {
				t.Fatalf("%d pages: %v", got, src.calls)
			}
			stop, stopped := readHistoryStop(ht.data)
			if !stopped || stop.By != "driver" || stop.Code == "" || len(ht.notices) != 1 {
				t.Fatalf("STOP %+v %v, notices %v", stop, stopped, ht.notices)
			}
			chat := strings.TrimPrefix(src.calls[len(src.calls)-1], "page ")
			if st := ht.state(chat); st.State != kb.HistoryPaused {
				t.Fatalf("chat %s state %+v", chat, st)
			}
			// While STOP is there, nothing at all is asked.
			before := len(src.calls)
			ht.run()
			if len(src.calls) != before {
				t.Fatalf("calls after the stop: %v", src.calls[before:])
			}
		})
	}
}

// A STOP that appears while a call is in flight lets that call finish and be written, and no further call goes out.
func TestHistoryStopFileHonouredBeforeTheNextCall(t *testing.T) {
	src := &fakeHistory{chats: []connectorwire.HistoryChat{{ID: "a", Kind: "dm"}}, convs: map[string][]kb.SourceMessage{"a": historyConv("a", 100, recentStart(100, time.Hour), time.Hour)}}
	ht := newHistoryTest(t, src)
	src.hook = func(n int, q connectorwire.HistoryQuery) (connectorwire.HistoryPage, error, bool) {
		if n == 3 {
			if err := os.WriteFile(historyStopPath(ht.data), nil, 0o600); err != nil { // a person, by hand
				t.Error(err)
			}
		}
		return connectorwire.HistoryPage{}, nil, false
	}
	ht.plan(true, nil)
	ht.run()
	if got := src.count("page"); got != 3 {
		t.Fatalf("%d pages", got)
	}
	if got := ht.kept("a"); len(got) != 45 {
		t.Fatalf("the page in flight was not written: %d kept", len(got))
	}
	if len(ht.notices) != 0 {
		t.Fatal("a STOP made by hand was pushed about")
	}
}

// One very slow call rests half an hour; a second one soon after stops; three slow ones among twenty rest and slow
// down to the slowest pace.
func TestHistorySlowCalls(t *testing.T) {
	zone := time.UTC
	now := time.Date(2026, 11, 8, 10, 0, 0, 0, zone)
	pc := newHistoryPacer(historyProfile("standard"), mrand.New(mrand.NewPCG(3, 4)), zone, now, time.Time{})
	for i := 0; i < 20; i++ {
		if v := pc.observe("history", 300*time.Millisecond, now); v.slow || v.rest > 0 {
			t.Fatalf("normal call %d: %+v", i, v)
		}
	}
	if pc.baseline["history"] != 300*time.Millisecond {
		t.Fatalf("baseline %v", pc.baseline)
	}
	// An image's first chunk is slower by nature: its own baseline.
	for i := 0; i < 20; i++ {
		pc.observe("archive_media", 1200*time.Millisecond, now)
	}
	if v := pc.observe("archive_media", 1500*time.Millisecond, now); v.slow {
		t.Fatalf("an image measured against pages: %+v", v)
	}
	v := pc.observe("history", 2500*time.Millisecond, now)
	if !v.slow || v.rest != 0 || pc.mult != 2 {
		t.Fatalf("slow: %+v mult %v", v, pc.mult)
	}
	pc.observe("history", 2500*time.Millisecond, now)
	v = pc.observe("history", 2500*time.Millisecond, now)
	if v.rest < 10*time.Minute || v.rest > 20*time.Minute || pc.mult != pc.maxMult() {
		t.Fatalf("three slow: %+v mult %v of %v", v, pc.mult, pc.maxMult())
	}
	if d := pc.interval(false, now); d > time.Minute || d < 30*time.Second {
		t.Fatalf("slowest interval %v", d)
	}
	for i := 0; i < historyNormalRun; i++ {
		pc.observe("history", 300*time.Millisecond, now)
	}
	if pc.mult >= pc.maxMult() {
		t.Fatal("did not speed up again after 50 normal calls")
	}
	v = pc.observe("history", 6*time.Second, now)
	if !v.verySlow || v.rest != 30*time.Minute || v.stop.Code != "" {
		t.Fatalf("very slow: %+v", v)
	}
	if v := pc.observe("history", 6*time.Second, now.Add(40*time.Minute)); v.stop.Code == "" {
		t.Fatalf("a second very slow call soon after did not stop: %+v", v)
	}
	// Resumed slowly: the slowest pace for two hours, whatever comes.
	pc2 := newHistoryPacer(historyProfile("careful"), mrand.New(mrand.NewPCG(5, 6)), zone, now, now.Add(historySlowStart))
	for i := 0; i < 200; i++ {
		pc2.observe("history", 100*time.Millisecond, now)
	}
	if d := pc2.interval(false, now.Add(time.Hour)); d < 20*time.Second {
		t.Fatalf("slow start interval %v", d)
	}
}

// The careful profile: intervals drawn afresh and skewed short, rests between batches, quiet periods each day.
func TestHistoryCarefulPace(t *testing.T) {
	zone := time.UTC
	now := time.Date(2026, 11, 8, 0, 0, 0, 0, zone)
	pc := newHistoryPacer(historyProfile(""), mrand.New(mrand.NewPCG(7, 8)), zone, now, time.Time{})
	if pc.pace.Profile != "careful" {
		t.Fatalf("default profile %q", pc.pace.Profile)
	}
	seen := map[time.Duration]bool{}
	var sum time.Duration
	for i := 0; i < 1000; i++ {
		d := pc.interval(false, now)
		if d < 2*time.Second || d > 20*time.Second {
			t.Fatalf("interval %v", d)
		}
		seen[d.Truncate(time.Millisecond)] = true
		sum += d
	}
	if len(seen) < 700 {
		t.Fatalf("intervals too regular: %d distinct", len(seen))
	}
	if mean := sum / 1000; mean < 3*time.Second || mean > 5500*time.Millisecond {
		t.Fatalf("mean interval %v", mean)
	}
	quiet := 0
	for m := 0; m < 24*60; m++ {
		if !pc.quietUntil(now.Add(time.Duration(m) * time.Minute)).IsZero() {
			quiet++
		}
	}
	if quiet < 60 || quiet > 6*60 {
		t.Fatalf("%d quiet minutes in a day", quiet)
	}
	rests := 0
	for i := 0; i < 400; i++ {
		pc.observe("history", 200*time.Millisecond, now)
		if d, _ := pc.rest(now); d > 0 {
			if d < 3*time.Minute || d > 15*time.Minute {
				t.Fatalf("rest %v", d)
			}
			rests++
		}
	}
	if rests < 3 || rests > 10 {
		t.Fatalf("%d rests in 400 calls", rests)
	}
}

// ---- resuming ----

// A run stopped mid-chat goes on from the chat's state after a restart (a new process), and a state older than what
// was written (a crash between the write and the save) reads pages again without archiving anything twice.
func TestHistoryRestartResumes(t *testing.T) {
	const n = 200
	src := &fakeHistory{chats: []connectorwire.HistoryChat{{ID: "a", Kind: "dm"}}, convs: map[string][]kb.SourceMessage{"a": historyConv("a", n, recentStart(n, time.Hour), time.Hour)}}
	stopAt := 5
	src.hook = func(k int, q connectorwire.HistoryQuery) (connectorwire.HistoryPage, error, bool) {
		if k == stopAt {
			return connectorwire.HistoryPage{Pace: &connectorwire.Pace{Stop: true}}, nil, true
		}
		return connectorwire.HistoryPage{}, nil, false
	}
	ht := newHistoryTest(t, src)
	ht.plan(true, nil)
	var early []byte
	ht.d.notify = func(context.Context, string, historyReason) {}
	ht.run()
	early, _ = os.ReadFile(historyStatePathFor(ht.data, kb.ChatID(testSource, "a")))
	if st := ht.state("a"); st.Read != 60 || st.State != kb.HistoryPaused {
		t.Fatalf("after the stop %+v", st)
	}
	// A crash: the state of two pages back.
	var st kb.HistoryState
	_ = readJSONFileBytes(early, &st)
	st.Cursor = strconv.Itoa(n - 30)
	st.Read = 30
	if err := ht.e.Store.PutHistoryState(kb.ChatID(testSource, "a"), st); err != nil {
		t.Fatal(err)
	}
	// The owner resumes; a new process takes over.
	if err := removeHistoryStop(ht.data); err != nil {
		t.Fatal(err)
	}
	ht.d.instance = "test-2"
	before := src.count("page")
	ht.run()
	if q := src.queries[before]; q.Before != strconv.Itoa(n-30) {
		t.Fatalf("resumed from %+v", q)
	}
	if got := ht.kept("a"); !slices.Equal(got, span(0, n-1)) {
		t.Fatalf("kept %v", got)
	}
	if st := ht.state("a"); st.State != kb.HistoryDone {
		t.Fatalf("state %+v", st)
	}
}

// ---- the lock ----

// One import at a time: a lock held is not taken, a fresh lease of another process is respected (a file system
// without working locks), a stale one is taken over and logged.
func TestHistoryLock(t *testing.T) {
	data := t.TempDir()
	now := time.Now()
	l1, took, err := lockHistory(data, "one", now)
	if err != nil || took != nil {
		t.Fatal(err)
	}
	if _, _, err := lockHistory(data, "two", now); !errors.Is(err, errHistoryLocked) {
		t.Fatalf("second lock: %v", err)
	}
	if lease, ok := readHistoryLease(data); !ok || lease.Instance != "one" || lease.PID != os.Getpid() {
		t.Fatalf("lease %+v", lease)
	}
	l1.release()
	if _, ok := readHistoryLease(data); ok {
		t.Fatal("the lease outlived the lock")
	}
	path := filepath.Join(historyDir(data), historyLockName)
	write := func(beat time.Time) {
		raw := fmt.Sprintf(`{"pid":1,"instance":"gone","started_at":%q,"heartbeat":%q}`, beat.Format(time.RFC3339), beat.Format(time.RFC3339))
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(now.Add(-time.Minute))
	if _, _, err := lockHistory(data, "two", now); !errors.Is(err, errHistoryLeased) {
		t.Fatalf("over a fresh lease: %v", err)
	}
	write(now.Add(-11 * time.Minute))
	l2, took, err := lockHistory(data, "two", now)
	if err != nil || took == nil || took.Instance != "gone" {
		t.Fatalf("takeover: %v %+v", err, took)
	}
	l2.release()

	// The driver logs a takeover.
	src := &fakeHistory{chats: []connectorwire.HistoryChat{{ID: "a", Kind: "dm"}}, convs: map[string][]kb.SourceMessage{"a": historyConv("a", 10, recentStart(10, time.Hour), time.Hour)}}
	ht := newHistoryTest(t, src)
	ht.plan(true, nil)
	path = filepath.Join(historyDir(ht.data), historyLockName)
	write(ht.clock.Now().Add(-20 * time.Minute))
	ht.run()
	if !strings.Contains(readAllLogs(t, ht.data), `"event":"takeover"`) || len(ht.kept("a")) != 10 {
		t.Fatal("no takeover")
	}
	// While another process holds the lock, the driver does nothing.
	other, _, err := lockHistory(ht.data, "other", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer other.release()
	ht.plan(true, map[string]string{"a": "anchor"})
	before := len(src.calls)
	ht.run()
	if src.count("page") != countPrefix(src.calls[:before], "page") {
		t.Fatal("read while another process holds the lock")
	}
}

func countPrefix(list []string, prefix string) int {
	n := 0
	for _, c := range list {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// ---- backups ----

// Every run starts with a backup beside the data directory, attachments and lock files left out; three are kept,
// the oldest by name deleted, nothing else touched; a backup that fails starts nothing.
func TestHistoryBackups(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	for p, body := range map[string]string{
		"chats/c/messages/2026-01-01.md":         "- 10:00 a: hello\n",
		"chats/c/attachments/2026-01-01/big.jpg": "jpg",
		".lock":                                  "",
		"history/run.lock":                       "{}",
		"history/plan-x.json":                    "{}",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(data, p)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(data, p), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	dir := historyBackupDir(data)
	if err := os.MkdirAll(filepath.Join(dir, "kb-20200101000000.tar.gz"), 0o700); err != nil { // a directory by that name
		t.Fatal(err)
	}
	for _, name := range []string{"notes.txt", "kb-latest.tar.gz", "kb-20200101000001.tar.gz.keep"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	var made []string
	for i := 0; i < 4; i++ {
		p, err := backupKB(data, base.Add(time.Duration(i)*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		made = append(made, filepath.Base(p))
		if info, _ := os.Stat(p); info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %v", p, info.Mode())
		}
	}
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	want := []string{"kb-20200101000000.tar.gz", "kb-20200101000001.tar.gz.keep", "kb-20261108110000.tar.gz", "kb-20261108120000.tar.gz",
		"kb-20261108130000.tar.gz", "kb-latest.tar.gz", "notes.txt"}
	if !slices.Equal(names, want) {
		t.Fatalf("left %v (made %v)", names, made)
	}
	if info, _ := os.Stat(dir); info.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", info.Mode())
	}
	files := tarNames(t, filepath.Join(dir, made[3]))
	if !slices.Contains(files, "data/chats/c/messages/2026-01-01.md") || !slices.Contains(files, "data/history/plan-x.json") {
		t.Fatalf("archived %v", files)
	}
	for _, f := range files {
		if strings.Contains(f, "attachments") || strings.HasSuffix(f, ".lock") {
			t.Fatalf("archived %s", f)
		}
	}

	// A backup that fails: nothing runs.
	src := &fakeHistory{chats: []connectorwire.HistoryChat{{ID: "a", Kind: "dm"}}, convs: map[string][]kb.SourceMessage{"a": historyConv("a", 10, recentStart(10, time.Hour), time.Hour)}}
	ht := newHistoryTest(t, src)
	ht.plan(true, nil)
	if err := os.WriteFile(historyBackupDir(ht.data), []byte("in the way"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ht.d.runOnce(context.Background()); err == nil || !strings.Contains(err.Error(), "backup") {
		t.Fatalf("run without a backup: %v", err)
	}
	if src.count("page") != 0 {
		t.Fatal("read without a backup")
	}
	if err := os.Remove(historyBackupDir(ht.data)); err != nil {
		t.Fatal(err)
	}
	ht.run()
	if src.count("page") == 0 {
		t.Fatal("did not run once the backup could be made")
	}
}

// ---- one in flight, and the log ----

// Only one request is ever in flight: the driver's pages and images, and a tool's call at the same time.
func TestHistoryOneRequestInFlight(t *testing.T) {
	msgs := historyConv("a", 120, recentStart(120, time.Hour), time.Hour)
	for i := 0; i < len(msgs); i += 7 {
		msgs[i].Type, msgs[i].Media = "IMAGE", []string{fmt.Sprintf("img-%d", i)}
	}
	src := &fakeHistory{info: assistantwire.ArchiveSource{ID: testSource, History: true, Media: true}, real: time.Millisecond,
		chats: []connectorwire.HistoryChat{{ID: "a", Kind: "dm"}}, convs: map[string][]kb.SourceMessage{"a": msgs}}
	ht := newHistoryTest(t, src)
	ht.e.AttachmentDaysBySource = map[string]int{testSource: 3650}
	ht.plan(true, nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ht.run()
	}()
	tools := 0
	for running := true; running; {
		select {
		case <-done:
			running = false
		default:
			ht.d.calls.Lock() // what a tool's call does (historyRefresh)
			_, _ = src.Chats(context.Background(), testSource)
			ht.d.calls.Unlock()
			tools++
		}
	}
	if src.most.Load() != 1 {
		t.Fatalf("%d requests in flight at once", src.most.Load())
	}
	if tools == 0 || src.count("media") == 0 || len(ht.kept("a")) != 120 {
		t.Fatalf("tools %d, calls %d", tools, len(src.calls))
	}
}

// The log has a line per call (time, method, a keyed hash of the chat, duration, result) and never a message's text,
// a sender's or chat's name or a chat's id.
func TestHistoryLogHasNoContent(t *testing.T) {
	src := &fakeHistory{chats: []connectorwire.HistoryChat{{ID: "secret-chat-id", Name: "Secret Group Name", Kind: "dm"}},
		convs: map[string][]kb.SourceMessage{"secret-chat-id": historyConv("secret-chat-id", 40, recentStart(40, time.Hour), time.Hour)}}
	ht := newHistoryTest(t, src)
	ht.plan(true, nil)
	ht.run()
	logs := readAllLogs(t, ht.data)
	for _, bad := range []string{"SECRET", "Alice", "Secret Group", "secret-chat-id"} {
		if strings.Contains(logs, bad) {
			t.Fatalf("the log has %q:\n%s", bad, logs)
		}
	}
	calls := 0
	for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
		if strings.Contains(line, `"method":"history"`) {
			calls++
			if !regexp.MustCompile(`"chat":"[0-9a-f]{12}"`).MatchString(line) || !strings.Contains(line, `"result":"ok"`) {
				t.Fatalf("line %s", line)
			}
		}
	}
	if calls != src.count("page") {
		t.Fatalf("%d log lines for %d calls", calls, src.count("page"))
	}
	status, _ := os.ReadFile(historyStatusPath(ht.data))
	if strings.Contains(string(status), "secret-chat-id") || strings.Contains(string(status), "SECRET") {
		t.Fatalf("status %s", status)
	}
}

// A per-item read: false while the import runs takes effect at its next page (a narrower selection needs no new
// approval); reading it again, only personally, goes on from where the whole read was without archiving anything
// twice.
func TestHistoryModeChangeMidway(t *testing.T) {
	src := &fakeHistory{chats: []connectorwire.HistoryChat{{ID: "a", Kind: "dm"}}, convs: map[string][]kb.SourceMessage{"a": historyConv("a", 300, recentStart(300, time.Hour), time.Hour, 20)}}
	ht := newHistoryTest(t, src)
	src.hook = func(n int, q connectorwire.HistoryQuery) (connectorwire.HistoryPage, error, bool) {
		if n == 2 {
			ht.item("a", "off")
		}
		return connectorwire.HistoryPage{}, nil, false
	}
	ht.plan(true, nil)
	ht.run()
	if got := src.count("page"); got != 2 {
		t.Fatalf("%d pages after off", got)
	}
	src.hook = nil
	ht.plan(true, map[string]string{"a": "anchor"})
	ht.run()
	got := ht.kept("a")
	if !slices.Contains(got, 20) || !slices.Contains(got, 299) || slices.Contains(got, 150) {
		t.Fatalf("kept %v", got)
	}
}

func readAllLogs(t *testing.T, data string) string {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(historyDir(data), "log-*.jsonl"))
	var b strings.Builder
	for _, f := range files {
		raw, _ := os.ReadFile(f)
		b.Write(raw)
	}
	return b.String()
}

func readJSONFileBytes(raw []byte, v any) error { return json.Unmarshal(raw, v) }

// tarNames are the entries of a .tar.gz.
func tarNames(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var out []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, h.Name)
	}
}

// Calls that get very slow: a 30-minute rest after the first, a stop at the second soon after, written and pushed.
func TestHistoryVerySlowCallsStop(t *testing.T) {
	src := &fakeHistory{took: 6 * time.Second, chats: []connectorwire.HistoryChat{{ID: "a", Kind: "dm"}},
		convs: map[string][]kb.SourceMessage{"a": historyConv("a", 200, recentStart(200, time.Hour), time.Hour)}}
	ht := newHistoryTest(t, src)
	ht.plan(true, nil)
	start := ht.clock.Now()
	ht.run()
	if got := src.count("page"); got != 2 {
		t.Fatalf("%d pages", got)
	}
	if ht.clock.Now().Sub(start) < 30*time.Minute {
		t.Fatalf("no rest after the first very slow call: %v", ht.clock.Now().Sub(start))
	}
	stop, stopped := readHistoryStop(ht.data)
	if !stopped || stop.Code != "very_slow_twice" || len(ht.notices) != 1 {
		t.Fatalf("STOP %+v", stop)
	}
	st := ht.d.status(testSource)
	if st.State != "stopped" || st.LastSlow == nil || st.LastStop == nil {
		t.Fatalf("status %+v", st)
	}
}
