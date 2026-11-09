package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
)

// inboxConnector is a connector with an inbox, on made-up data.
type inboxConnector struct {
	things  []connector.InboxThing
	replies []string
}

func (*inboxConnector) ID() string   { return "example" }
func (*inboxConnector) Name() string { return "Example" }
func (*inboxConnector) Manifest() connector.Manifest {
	return connector.Manifest{Capabilities: []connector.Capability{connector.CapInbox}, Kinds: []string{"review_requested"}}
}
func (c *inboxConnector) Collect(context.Context) ([]connector.InboxThing, error) {
	return c.things, nil
}
func (c *inboxConnector) Reply(_ context.Context, r connector.InboxReply) error {
	c.replies = append(c.replies, r.Thread+": "+r.Text)
	return nil
}

func thing(last string, at time.Time) connector.InboxThing {
	return connector.InboxThing{Thread: "example-org/example-repo#12", Kind: "review_requested", Label: "请你 review",
		Title: "Add the example", Sender: "bob", Preview: "请看一下", Context: "上下文", At: at, Last: last,
		URL: "https://github.com/example-org/example-repo/pull/12", ReplyTo: "评论到 example-org/example-repo#12", CanReply: true, Tier: TierAction}
}

func openItems(h *inboxHub) []*InboxItem {
	var out []*InboxItem
	for _, it := range h.inbox.items {
		if it.open() {
			out = append(out, it)
		}
	}
	return out
}

// One open item per thread gathers its moves; done, a later move starts a new item, the same one does not; a thing
// that is over closes the open item.
func TestConnectorThingsBecomeItems(t *testing.T) {
	h := newInboxHub(t)
	now := time.Now()
	apply := func(things ...connector.InboxThing) []*InboxItem {
		h.inbox.mu.Lock()
		defer h.inbox.mu.Unlock()
		return h.inbox.applyConnector("example", "Example", things, now)
	}

	if ch := apply(thing("a", now.Add(-time.Hour))); len(ch) != 1 {
		t.Fatalf("changed %d", len(ch))
	}
	it := openItems(h)[0]
	if it.Source != "example" || it.AccountName != "Example" || it.Connector.Label != "请你 review" || !it.Connector.CanReply || it.Count != 1 {
		t.Fatalf("%+v %+v", it, it.Connector)
	}
	if h.inbox.ruleTier(it) != TierAction {
		t.Fatalf("rule tier %s", h.inbox.ruleTier(it))
	}
	if ch := apply(thing("a", now.Add(-time.Hour))); len(ch) != 0 {
		t.Fatal("the same move changed the item")
	}
	apply(thing("b", now.Add(-time.Minute)))
	if len(openItems(h)) != 1 || it.Count != 2 || it.Last != "b" {
		t.Fatalf("a later move did not join the open item: %d open, count %d", len(openItems(h)), it.Count)
	}

	it.done(now, "user")
	apply(thing("b", now.Add(-time.Minute)))
	if len(openItems(h)) != 0 {
		t.Fatal("a move already dealt with came back")
	}
	apply(thing("c", now))
	if len(openItems(h)) != 1 || openItems(h)[0] == it {
		t.Fatal("a move after done did not start a new item")
	}

	over := thing("d", now)
	over.Done = connector.DoneRead
	apply(over)
	if len(openItems(h)) != 0 {
		t.Fatal("a thread that is over left its item open")
	}

	// Old or already read: never an item.
	old := thing("e", now.Add(-inboxWindow-time.Hour))
	old.Thread = "example-org/example-repo#1"
	read := thing("f", now)
	read.Thread, read.Read = "example-org/example-repo#2", true
	if ch := apply(old, read); len(ch) != 0 {
		t.Fatalf("old or read things became items: %d", len(ch))
	}
}

// A connector that reads its notifications again (after a restart) may hand an earlier move of an open thread: it
// is neither counted nor allowed to wind the item's time back, and the next real move still joins (#1527).
func TestConnectorOlderMoveDoesNotRewind(t *testing.T) {
	h := newInboxHub(t)
	day := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	now := day.Add(36 * time.Hour)
	ci := func(at time.Time) connector.InboxThing {
		th := thing(at.Format(time.RFC3339), at)
		th.Thread, th.Kind = "ci:example-org/example-app@main", "ci_failed"
		return th
	}
	apply := func(things ...connector.InboxThing) []*InboxItem {
		h.inbox.mu.Lock()
		defer h.inbox.mu.Unlock()
		return h.inbox.applyConnector("example", "Example", things, now)
	}

	apply(ci(day.Add(15*time.Hour + 11*time.Minute)))
	newest := day.Add(24*time.Hour + 39*time.Minute)
	apply(ci(newest))
	it := openItems(h)[0]
	if it.Count != 2 || !it.UpdatedAt.Equal(newest) {
		t.Fatalf("setup: count %d at %v", it.Count, it.UpdatedAt)
	}
	for _, earlier := range []time.Duration{22*time.Hour + 56*time.Minute, 21*time.Hour + 14*time.Minute} {
		if ch := apply(ci(day.Add(earlier))); len(ch) != 0 {
			t.Fatalf("an earlier move changed the item: %+v", ch[0])
		}
	}
	if it.Count != 2 || !it.UpdatedAt.Equal(newest) || it.Last != newest.Format(time.RFC3339) {
		t.Fatalf("an earlier move wound the item back: count %d, at %v, last %s", it.Count, it.UpdatedAt, it.Last)
	}
	later := newest.Add(time.Second)
	if ch := apply(ci(later)); len(ch) != 1 || it.Count != 3 || !it.UpdatedAt.Equal(later) {
		t.Fatalf("a later move did not join: count %d at %v", it.Count, it.UpdatedAt)
	}
}

// An item handled after its time was wound back (stored before the fix) is not replaced by a new item when its own
// move comes again: the new item's id would be the same (#1527).
func TestConnectorMoveOfAHandledItemDoesNotReplaceIt(t *testing.T) {
	h := newInboxHub(t)
	now := time.Now()
	newer, older := now.Add(-2*time.Hour), now.Add(-3*time.Hour)
	moveAt := func(at time.Time) connector.InboxThing { return thing(at.Format(time.RFC3339), at) }
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	h.inbox.applyConnector("example", "Example", []connector.InboxThing{moveAt(newer)}, now)
	var it *InboxItem
	for _, x := range h.inbox.items {
		it = x
	}
	it.Count, it.Last, it.UpdatedAt = 2, older.Format(time.RFC3339), older // wound back by an older build
	it.done(now, "read")

	if ch := h.inbox.applyConnector("example", "Example", []connector.InboxThing{moveAt(newer)}, now); len(ch) != 0 {
		t.Fatalf("the handled item's own move changed the inbox: %+v", ch[0])
	}
	if len(h.inbox.items) != 1 || h.inbox.items[it.ID] != it || it.open() || it.Count != 2 {
		t.Fatalf("the handled item was replaced or reopened: %d items, open %v, count %d", len(h.inbox.items), it.open(), it.Count)
	}
}

// Read elsewhere: an item that does not need the user is done; one that does stays.
func TestConnectorThingReadElsewhere(t *testing.T) {
	h := newInboxHub(t)
	now := time.Now()
	h.inbox.mu.Lock()
	fyi := thing("a", now)
	fyi.Tier = TierFYI // the rules make it FYI
	h.inbox.applyConnector("example", "Example", []connector.InboxThing{fyi}, now)
	it := openItems(h)[0]
	read := fyi
	read.Read = true
	h.inbox.applyConnector("example", "Example", []connector.InboxThing{read}, now)
	h.inbox.mu.Unlock()
	if it.open() || it.DoneBy != "read" {
		t.Fatalf("an FYI read on GitHub is still open: %+v", it)
	}
}

// A thing older than the open item's newest move says nothing of whether that move was read: a GitHub connector that
// restarted after the CI follow-up folded in a later run (one GitHub sent no notification of) reports the item's
// older notification read on GitHub. The item at the later run stays unread and open, whatever its tier (#2207
// review); the same move read elsewhere still counts.
func TestConnectorOlderThingReadLeavesTheNewerMoveUnread(t *testing.T) {
	for _, tier := range []connector.Tier{TierAction, TierFYI} {
		t.Run(string(tier), func(t *testing.T) {
			h := newInboxHub(t)
			now := time.Now()
			notified, later := now.Add(-3*time.Hour), now.Add(-time.Hour)
			ci := func(at time.Time, read bool) connector.InboxThing {
				th := thing(at.UTC().Format(time.RFC3339), at)
				th.Thread, th.Kind, th.Tier, th.Read = "ci:example-org/example-repo@main", "ci_failed", tier, read
				return th
			}
			h.inbox.mu.Lock()
			defer h.inbox.mu.Unlock()
			h.inbox.applyConnector("example", "Example", []connector.InboxThing{ci(notified, false)}, now)
			h.inbox.applyConnector("example", "Example", []connector.InboxThing{ci(later, false)}, now) // the follow-up folded a later run
			it := openItems(h)[0]
			if it.Last != ci(later, false).Last || it.Read {
				t.Fatalf("setup: %+v", it)
			}
			h.inbox.applyConnector("example", "Example", []connector.InboxThing{ci(notified, true)}, now) // after a restart
			if it.Read || !it.open() {
				t.Fatalf("the item at the later run (Last %s) is read %v, open %v: an older thing read on GitHub marked it", it.Last, it.Read, it.open())
			}
			h.inbox.applyConnector("example", "Example", []connector.InboxThing{ci(later, true)}, now)
			if !it.Read || it.open() != (tier == TierAction) {
				t.Fatalf("the later run itself read on GitHub: read %v, open %v", it.Read, it.open())
			}
		})
	}
}

// A new move is unread even when an earlier action in the same open thread was read elsewhere.
func TestConnectorNewMoveIsUnreadAfterReadElsewhere(t *testing.T) {
	h := newInboxHub(t)
	h.in.model, _ = captureModel(t, `{"tier":"fyi","summary":"A new update","reason":"FYI"}`)
	now := time.Now()
	first := thing("a", now.Add(-time.Minute))
	h.inbox.mu.Lock()
	h.inbox.applyConnector("example", "Example", []connector.InboxThing{first}, now)
	it := openItems(h)[0]
	first.Read = true
	h.inbox.applyConnector("example", "Example", []connector.InboxThing{first}, now)
	if !it.Read || !it.open() {
		h.inbox.mu.Unlock()
		t.Fatal("an action read elsewhere should stay open")
	}
	fresh := thing("b", now)
	fresh.Tier = "" // An ordinary comment leaves its tier to the model.
	h.inbox.applyConnector("example", "Example", []connector.InboxThing{fresh}, now)
	h.inbox.mu.Unlock()

	if !h.triageOne(context.Background(), it) {
		t.Fatal("the new move was not triaged")
	}
	if it.Read || !it.open() || it.Tier != TierFYI {
		t.Fatalf("new unread content was treated as read: read=%v done_by=%q tier=%q", it.Read, it.DoneBy, it.Tier)
	}
	fresh.Read = true
	h.inbox.mu.Lock()
	h.inbox.applyConnector("example", "Example", []connector.InboxThing{fresh}, now)
	h.inbox.mu.Unlock()
	if !it.Read || it.open() || it.DoneBy != "read" {
		t.Fatalf("the new move read elsewhere stayed open: %+v", it)
	}
}

// Old apps never see a connector's items (they would draw a PR as a mail); new ones ask for them. Reply goes to the
// connector once, and files the item as replied.
func TestConnectorItemsListAndReply(t *testing.T) {
	h := newInboxHub(t)
	c := &inboxConnector{things: []connector.InboxThing{thing("a", time.Now())}}
	h.addConnector(c, false)
	h.inboxReady(context.Background())
	h.inbox.mu.Lock()
	h.inbox.items["mail:1"] = &InboxItem{ID: "mail:1", Source: "mail", Kind: KindMail, UpdatedAt: time.Now()}
	h.inbox.mu.Unlock()
	h.collectInbox(context.Background(), roundPoll)
	if n := len(openItems(h)); n != 2 {
		t.Fatalf("collected %d", n-1)
	}
	mux := h.routes()
	_, old := inboxCall(t, mux, "GET", "/api/v1/inbox", "")
	_, all := inboxCall(t, mux, "GET", "/api/v1/inbox?sources=all", "")
	if len(old["items"].([]any)) != 1 || len(all["items"].([]any)) != 2 {
		t.Fatalf("old %v\nall %v", old, all)
	}
	id := ""
	for _, it := range openItems(h) { // a map's order: read it once
		if it.Connector != nil {
			id = it.ID
		}
	}
	for range 2 { // pressed twice (phone and watch): sent once
		code, out := inboxCall(t, mux, "POST", "/api/v1/inbox/"+id+"/reply", `{"text":"看过了"}`)
		if code != 200 || out["done_by"] != "replied" {
			t.Fatalf("%d %v", code, out)
		}
	}
	if len(c.replies) != 1 || c.replies[0] != "example-org/example-repo#12: 看过了" {
		t.Fatalf("replies %v", c.replies)
	}
}

// settlingConnector settles every "old:" thread.
type settlingConnector struct {
	inboxConnector
}

func (*settlingConnector) Manifest() connector.Manifest {
	return connector.Manifest{Capabilities: []connector.Capability{connector.CapInbox, connector.CapSettle},
		Kinds: []string{"review_requested"}}
}

func (*settlingConnector) Settle(_ context.Context, threads []string) map[string]string {
	out := map[string]string{}
	for _, t := range threads {
		if strings.HasPrefix(t, "old:") {
			out[t] = "示例：按新规则合并"
		}
	}
	return out
}

// Items made under older rules are filed as done with why (not deleted); a rule's tier wins over the model's, also
// for an item already open.
func TestConnectorRulesSettleAndTier(t *testing.T) {
	h := newInboxHub(t)
	now := time.Now()
	c := &settlingConnector{}
	h.addConnector(c, false)
	old := thing("a", now)
	old.Thread, old.Tier = "old:1", ""
	h.inbox.mu.Lock()
	h.inbox.applyConnector("example", "Example", []connector.InboxThing{old}, now)
	h.inbox.mu.Unlock()
	modelTiered := openItems(h)[0]
	modelTiered.Tier, modelTiered.TriagedAt = TierAction, "a" // what the model said before

	c.things = []connector.InboxThing{thing("b", now)}
	c.things[0].Tier, c.things[0].Why = TierFYI, "示例规则：知悉即可"
	h.collectInbox(context.Background(), roundPoll)
	if modelTiered.open() || modelTiered.DoneBy != "read" || modelTiered.Reason != "示例：按新规则合并" {
		t.Fatalf("old item: %+v", modelTiered)
	}
	if _, ok := h.inbox.items[modelTiered.ID]; !ok {
		t.Fatal("the old item was deleted")
	}
	var fresh *InboxItem
	for _, it := range openItems(h) {
		fresh = it
	}
	if fresh == nil || fresh.Tier != TierFYI || fresh.Reason != "示例规则：知悉即可" || fresh.TriagedAt != fresh.Last {
		t.Fatalf("rule tier: %+v", fresh)
	}
	// Same move, the rule now says otherwise (other options): the open item follows.
	c.things[0].Tier, c.things[0].Why = TierAction, "示例规则：需要你"
	h.collectInbox(context.Background(), roundPoll)
	if fresh.Tier != TierAction || fresh.Reason != "示例规则：需要你" {
		t.Fatalf("re-tiered: %+v", fresh)
	}

}

// A connector that took the hub's GitHub over (#2830) hands its things with extra carrying only the rule tier: they
// are generic connector items like the hub's own made, not the items' own blocks a Feishu thing keeps.
func TestMigratedThingsWithoutABlockAreGeneric(t *testing.T) {
	h := newInboxHub(t)
	gh := thing("a", time.Now().Add(-time.Hour))
	gh.Tier = ""
	gh.Extra = &connector.ThingExtra{RuleTier: string(TierAction)}
	h.collectSource(context.Background(), assistantwire.InboxSource{ID: "github", Name: "GitHub", Migrated: true, Extension: true,
		Things: []connector.InboxThing{gh}})
	items := openItems(h)
	if len(items) != 1 || items[0].Source != "github" || items[0].Connector == nil || items[0].Connector.Label != "请你 review" {
		t.Fatalf("%d items: %+v", len(items), items)
	}
	if h.inbox.ruleTier(items[0]) != TierAction {
		t.Fatalf("rule tier %s", h.inbox.ruleTier(items[0]))
	}
}
