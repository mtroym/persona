package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/lazyre"
)

// The inbox (docs/plans/plan-connectors.md "4 · Inbox"): GitHub's unread notifications that ask something of the user —
// a review request, an @, a new comment or review on their PR / issue, their CI failing, an assignment — one thing
// per PR / issue. Polled (the hub has no public address for webhooks) at the pace GitHub asks for.

type inboxState struct {
	mu           sync.Mutex
	things       map[string]*connector.InboxThing // by thread
	seen         map[string]string                // notification id -> the updated_at already read
	lastModified string                           // of the last notifications answer, for If-Modified-Since
	fullAt       time.Time                        // the last read without If-Modified-Since
	ci           map[string]*ciMeta               // CI items, by thread (rules.go)
	repos        map[string]repoMeta              // repositories' default branches (rules.go)
	marks        map[string]*readMark             // by thread: whether its thing was read on GitHub
}

// readMark is what says whether a thread's thing was read on GitHub (applyNotifications). It lives in memory only,
// like the things; a thread without one is never inferred read.
type readMark struct {
	// notified is the updated_at of the newest notification the thing came from. The thing's At is not always that:
	// the CI follow-up moves it to later runs GitHub sent no notification of. Zero is unknown: nothing is inferred.
	notified time.Time
	// missing counts the list reads in a row the thing was missing from while that notification would have been in
	// them (missedAgain).
	missing int
}

var (
	pollDefault  = 60 * time.Second   // when GitHub does not say (X-Poll-Interval)
	fullEvery    = 10 * time.Minute   // read everything now and then: a notification read on GitHub may not change Last-Modified
	maxDetails   = 30                 // threads read per poll; the rest wait for the next
	forgetAfter  = 8 * 24 * time.Hour // things kept for the hub to settle its items, like its own inboxForget
	contextMoves = 8                  // newest comments / reviews in a thing's context

	notificationsPage    = 50 // unread notifications per page
	maxNotificationPages = 5  // pages read per poll; past them the list is incomplete (missedAgain)
	readAfter            = 2  // list reads in a row a thing must be missing from to count as read on GitHub (readMark)
)

type notification struct {
	ID        string    `json:"id"`
	Reason    string    `json:"reason"`
	UpdatedAt time.Time `json:"updated_at"`
	Subject   struct {
		Title string `json:"title"`
		URL   string `json:"url"`
		Type  string `json:"type"` // PullRequest | Issue | CheckSuite | Release | Discussion…
	} `json:"subject"`
	Repository struct {
		FullName string `json:"full_name"`
		HTMLURL  string `json:"html_url"`
	} `json:"repository"`
}

// Watch polls the notifications until ctx ends, saying ScopeInbox when a thing changed.
func (c *Connector) Watch(ctx context.Context, changed func(connector.Scope)) error {
	for {
		wait := pollDefault
		if c.signedIn() {
			w, moved, err := c.poll(ctx)
			if err != nil {
				return err
			}
			if moved {
				changed(connector.ScopeInbox)
			}
			wait = max(w, pollDefault)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
	}
}

// poll reads the unread notifications (not at all when nothing changed: 304) and returns how long GitHub wants
// the next poll to wait and whether a thing changed. The first page decides: only it carries If-Modified-Since,
// and its Last-Modified and X-Poll-Interval are kept. A rate limit is waited out, not returned: watchOne would
// restart Watch long before GitHub takes another request.
func (c *Connector) poll(ctx context.Context) (time.Duration, bool, error) {
	if left := c.pauseLeft(); left > 0 {
		return left, false, nil // GitHub asked for no requests until then (notePause)
	}
	st := &c.inbox
	st.mu.Lock()
	if st.things == nil {
		st.things, st.seen = map[string]*connector.InboxThing{}, map[string]string{}
	}
	hdr := http.Header{}
	// A thing missing from the last list read is settled by the next one: read in full, not answered with a 304.
	full := time.Since(st.fullAt) >= fullEvery || st.unsure()
	if !full && st.lastModified != "" {
		hdr.Set("If-Modified-Since", st.lastModified)
	}
	st.mu.Unlock()

	var ns []notification
	code, h, err := c.request(ctx, http.MethodGet, notificationsPath(1), hdr, nil, &ns)
	if err != nil {
		if left := c.pauseLeft(); left > 0 { // a rate limit (request records it: notePause)
			return max(left, pollDefault), false, nil
		}
		return 0, false, err
	}
	wait := pollDefault
	if s, err := strconv.Atoi(h.Get("X-Poll-Interval")); err == nil && s > 0 {
		wait = time.Duration(s) * time.Second
	}
	if code == http.StatusNotModified {
		return wait, false, nil
	}
	st.mu.Lock()
	st.lastModified = h.Get("Last-Modified")
	if full {
		st.fullAt = time.Now()
	}
	st.mu.Unlock()
	ns, complete := c.laterNotifications(ctx, ns)
	moved := c.applyNotifications(ctx, ns, complete, time.Now())
	if c.followUpCI(ctx) {
		moved = true
	}
	return wait, moved, nil
}

func notificationsPath(page int) string {
	return fmt.Sprintf("/notifications?per_page=%d&page=%d", notificationsPage, page)
}

// laterNotifications reads the pages after the first one, ns, while they come full, up to maxNotificationPages, and
// says whether the list is complete (it ended on a short page). A page that fails ends the list incomplete without
// an error: what was read still counts, and the next poll reads again; a rate limit is recorded (request) and
// holds back the threads' details too. So does a pause another read recorded meanwhile: no page is asked for in it.
func (c *Connector) laterNotifications(ctx context.Context, ns []notification) ([]notification, bool) {
	batch := len(ns)
	for page := 2; page <= maxNotificationPages && batch == notificationsPage; page++ {
		if c.pauseLeft() > 0 {
			return ns, false
		}
		var more []notification
		if _, _, err := c.request(ctx, http.MethodGet, notificationsPath(page), nil, nil, &more); err != nil {
			return ns, false
		}
		ns, batch = append(ns, more...), len(more)
	}
	return ns, batch < notificationsPage
}

// applyNotifications reads the threads that moved (at most maxDetails, none while requests run low or GitHub asks
// for a pause) and marks those no longer in ns, the unread list newest first, as read elsewhere (missedAgain).
func (c *Connector) applyNotifications(ctx context.Context, ns []notification, complete bool, now time.Time) bool {
	st := &c.inbox
	me := c.Status(ctx).Account
	budget := maxDetails
	var oldest time.Time
	for _, n := range ns {
		if oldest.IsZero() || n.UpdatedAt.Before(oldest) {
			oldest = n.UpdatedAt
		}
	}
	unread := map[string]bool{}
	moved := false
	o := c.options()
	for _, n := range ns {
		kind, label := classify(n)
		thread, run, parsed := threadOf(n), ciRun{}, false
		if kind == "ci_failed" {
			if run, parsed = parseCI(n.Subject.Title); parsed {
				thread = ciThread(n.Repository.FullName, run.branch)
			}
		}
		// Unread on GitHub, whatever the inbox makes of it below (a reason it leaves out, a muted repository).
		unread[thread] = true
		if kind == "" || o.muted(n.Repository.FullName) || kind == "ci_failed" && (!parsed || o.ignoredBranch(run.branch) || o.ci() == CIOff) {
			continue
		}
		stamp := n.UpdatedAt.UTC().Format(time.RFC3339)
		st.mu.Lock()
		known := st.seen[n.ID] == stamp
		// Notifications come newest first and a CI thread gathers many: one older than the thread's thing is an
		// earlier move, and the thing keeps the newest (InboxThing.At).
		older := !known && st.things[thread] != nil && n.UpdatedAt.Before(st.things[thread].At)
		if older {
			st.seen[n.ID] = stamp
		}
		st.mu.Unlock()
		if known || older || budget == 0 {
			continue
		}
		if c.ratesLow() { // checked per thread: a detail read may have run into a rate limit
			budget = 0
			continue
		}
		budget--
		var thing connector.InboxThing
		var err error
		switch {
		case kind == "ci_failed" && !run.failed():
			// A run of the branch that did not fail (succeeded, cancelled): after a success the follow-up checks in
			// this poll whether the branch's item is over (followUpCI: every workflow's newest run).
			st.mu.Lock()
			st.seen[n.ID] = stamp
			if m := st.ci[thread]; m != nil && run.status == "succeeded" {
				m.checked = time.Time{}
			}
			st.mu.Unlock()
			continue
		case kind == "ci_failed":
			var ok bool
			thing, ok, err = c.describeCI(ctx, n, run, me, o)
			if err == nil && !ok {
				st.mu.Lock()
				st.seen[n.ID] = stamp
				st.mu.Unlock()
				continue
			}
		default:
			thing, err = c.describe(ctx, n, me)
		}
		if err != nil {
			continue // read again next time
		}
		thing.Thread, thing.Kind, thing.Label, thing.Last = thread, kind, label, stamp
		thing.Tags = tagsOf(n, run, kind, thing.Sender)
		st.mu.Lock()
		st.seen[n.ID] = stamp
		st.things[thread] = &thing
		if st.marks == nil {
			st.marks = map[string]*readMark{}
		}
		st.marks[thread] = &readMark{notified: n.UpdatedAt}
		st.mu.Unlock()
		moved = true
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	for thread, t := range st.things {
		if now.Sub(t.At) > forgetAfter {
			delete(st.things, thread)
			delete(st.marks, thread)
			continue
		}
		if m := st.marks[thread]; m != nil && m.missedAgain(t, !unread[thread] && !t.Read && t.Done == "", complete, oldest) {
			t.Read, moved = true, true
		}
	}
	return moved
}

// missedAgain counts the unread list just read (newest first; oldest is its oldest notification) for t, the thread's
// thing, gone from it (not in it, nor read or done already), and says whether t now counts as read on GitHub: gone
// from readAfter list reads in a row that would have had its own newest notification, being complete or reaching
// past that notification. GitHub pages by offset, not from a snapshot: a notification read, or moved to the top by
// new activity, while the poll is between pages shifts the rest, so one still unread can be on no page once (the next
// poll reads the list in full at once: unsure). A thing the CI follow-up moved past its notification never counts:
// GitHub sent no notification of the later run, so reading an earlier one says nothing of it. st.mu is held.
func (m *readMark) missedAgain(t *connector.InboxThing, gone, complete bool, oldest time.Time) bool {
	listed := complete || !oldest.IsZero() && m.notified.After(oldest)
	if !gone || !listed || m.notified.IsZero() || t.At.After(m.notified) {
		m.missing = 0
		return false
	}
	if m.missing++; m.missing < readAfter {
		return false
	}
	m.missing = 0
	return true
}

// unsure says whether a thing was missing from the last list read without counting as read yet (missedAgain). st.mu
// is held.
func (st *inboxState) unsure() bool {
	for _, m := range st.marks {
		if m.missing > 0 {
			return true
		}
	}
	return false
}

// tagsOf is what a notification's thing is about: its repository, who made the newest move (a person's PR / issue
// move; CI has nobody), a CI failure's branch.
func tagsOf(n notification, run ciRun, kind, sender string) []connector.Tag {
	repo := n.Repository.FullName
	tags := []connector.Tag{{Key: connector.TagRepo, Value: strings.ToLower(repo), Name: repo}}
	if kind == "ci_failed" {
		return append(tags, connector.Tag{Key: connector.TagBranch, Value: run.branch, Name: run.branch})
	}
	if sender != "" {
		tags = append(tags, connector.Tag{Key: connector.TagSender, Value: strings.ToLower(sender), Name: sender})
	}
	return tags
}

// classify says whether a notification is for the inbox: its kind and its tag. The tier is the rules' (describe,
// describeCI).
func classify(n notification) (kind, label string) {
	switch n.Reason {
	case "review_requested":
		return "review_requested", "请你 review"
	case "mention":
		return "mention", "@我"
	case "team_mention":
		return "team_mention", "@了你的团队"
	case "assign":
		return "assign", "分配给你"
	case "author":
		if n.Subject.Type == "PullRequest" {
			return "author", "你的 PR 有新动静"
		}
		if n.Subject.Type == "Issue" {
			return "author", "你的 issue 有新动静"
		}
	case "ci_activity":
		if n.Subject.Type == "CheckSuite" || n.Subject.URL == "" {
			return "ci_failed", "CI 失败"
		}
	}
	return "", ""
}

var subjectURL = lazyre.New(`/repos/([^/]+/[^/]+)/(?:issues|pulls)/(\d+)$`)

// threadOf is the PR / issue a notification is about ("example-org/example-repo#12"), so a review request and an @
// on the same PR are one thing; anything else is its own notification.
func threadOf(n notification) string {
	if m := subjectURL.FindStringSubmatch(n.Subject.URL); m != nil {
		return m[1] + "#" + m[2]
	}
	return "notification:" + n.ID
}

// move is a comment or a review, for the preview and the context.
type move struct {
	who, verb, text string
	at              time.Time
	bot             bool
	state           string // a review's: APPROVED, CHANGES_REQUESTED, COMMENTED
}

// isBot: an app's account (dependabot[bot], a CI bot).
func isBot(u user) bool { return u.Type == "Bot" || strings.HasSuffix(u.Login, "[bot]") }

// describe reads what the notification is about: the PR / issue, its newest comments and reviews, whether it is
// over (merged, closed, the review request withdrawn) or the user has answered.
func (c *Connector) describe(ctx context.Context, n notification, me string) (connector.InboxThing, error) {
	t := connector.InboxThing{Title: n.Subject.Title, At: n.UpdatedAt}
	m := subjectURL.FindStringSubmatch(n.Subject.URL)
	if m == nil { // a CI run: there is nothing to read or answer, only the failure to look at
		t.Sender, t.Preview, t.URL = "GitHub Actions", n.Subject.Title, n.Repository.HTMLURL+"/actions"
		t.Context = fmt.Sprintf("仓库 %s 的 CI（GitHub Actions）：%s", n.Repository.FullName, n.Subject.Title)
		return t, nil
	}
	repo, number := m[1], m[2]
	var it struct {
		Title     string `json:"title"`
		State     string `json:"state"`
		User      user   `json:"user"`
		Body      string `json:"body"`
		HTMLURL   string `json:"html_url"`
		Comments  int    `json:"comments"`
		Merged    bool   `json:"merged"`
		Locked    bool   `json:"locked"`
		Reviewers []user `json:"requested_reviewers"`
		Teams     []struct {
			Slug string `json:"slug"`
		} `json:"requested_teams"`
	}
	pr := n.Subject.Type == "PullRequest"
	path := fmt.Sprintf("/repos/%s/issues/%s", repo, number)
	if pr {
		path = fmt.Sprintf("/repos/%s/pulls/%s", repo, number)
	}
	if _, err := c.Get(ctx, path, &it); err != nil {
		return t, err
	}
	if pr { // a PR's conversation comments are counted on its issue
		var iss struct {
			Comments int `json:"comments"`
		}
		if _, err := c.Get(ctx, fmt.Sprintf("/repos/%s/issues/%s", repo, number), &iss); err != nil {
			return t, err
		}
		it.Comments = iss.Comments
	}
	var moves []move
	var cs []comment
	page := max(1, (it.Comments+99)/100)
	if _, err := c.Get(ctx, fmt.Sprintf("/repos/%s/issues/%s/comments?per_page=100&page=%d", repo, number, page), &cs); err != nil {
		return t, err
	}
	for _, cm := range cs {
		moves = append(moves, move{who: who(cm.User), text: strings.TrimSpace(cm.Body), at: cm.CreatedAt, bot: isBot(cm.User)})
	}
	reviewed := false
	if pr {
		var rs []comment
		if _, err := c.Get(ctx, fmt.Sprintf("/repos/%s/pulls/%s/reviews?per_page=100", repo, number), &rs); err != nil {
			return t, err
		}
		for _, r := range rs {
			verb := map[string]string{"APPROVED": "批准了", "CHANGES_REQUESTED": "要求修改", "COMMENTED": "评审了"}[r.State]
			if verb == "" || r.State == "COMMENTED" && strings.TrimSpace(r.Body) == "" {
				continue
			}
			if strings.EqualFold(r.User.Login, me) {
				reviewed = true
			}
			moves = append(moves, move{who: who(r.User), verb: verb, text: strings.TrimSpace(r.Body), at: r.SubmittedAt, bot: isBot(r.User), state: r.State})
		}
	}
	sort.SliceStable(moves, func(i, j int) bool { return moves[i].at.Before(moves[j].at) })

	what := "issue"
	if pr {
		what = "PR"
	}
	ref := repo + "#" + number
	t.Title, t.URL = it.Title, it.HTMLURL
	t.ReplyTo, t.CanReply = "评论到 "+ref+"（仓库可见的人都能看到）", !it.Locked
	t.Sender, t.Preview = it.User.String(), firstLine(it.Body)
	if len(moves) > 0 {
		last := moves[len(moves)-1]
		t.Sender, t.Preview = last.who, strings.TrimSpace(last.verb+" "+firstLine(last.text))
	}

	switch {
	case it.Merged || it.State == "closed":
		t.Done = connector.DoneRead
	case len(moves) > 0 && strings.EqualFold(strings.TrimSuffix(moves[len(moves)-1].who, " (bot)"), me):
		t.Done = connector.DoneReplied
	case n.Reason == "review_requested" && len(it.Teams) == 0 && !hasLogin(it.Reviewers, me):
		// no longer asked: reviewed (answered) or the request withdrawn
		if reviewed {
			t.Done = connector.DoneReplied
		} else {
			t.Done = connector.DoneRead
		}
	}

	t.Tier, t.Why = tierOf(n.Reason, it.User, moves)

	var b strings.Builder
	state := it.State
	if it.Merged {
		state = "merged"
	}
	fmt.Fprintf(&b, "GitHub %s %s：%s\n状态：%s · 作者 %s", what, ref, it.Title, state, it.User)
	if rv := logins(it.Reviewers); rv != "" {
		fmt.Fprintf(&b, " · 等这些人 review：%s", rv)
	}
	fmt.Fprintf(&b, "\n%s\n\n描述：\n%s", it.HTMLURL, cut(strings.TrimSpace(it.Body), 1500, "（没有描述）"))
	if len(moves) > 0 {
		from := max(0, len(moves)-contextMoves)
		fmt.Fprintf(&b, "\n\n最近的评论和 review（旧的在前，共 %d 条）：", len(moves))
		for _, mv := range moves[from:] {
			fmt.Fprintf(&b, "\n\n%s %s", day(mv.at), mv.who)
			if mv.verb != "" {
				b.WriteString(" " + mv.verb)
			}
			if mv.text != "" {
				b.WriteString("：\n" + cut(mv.text, 600, ""))
			}
		}
	}
	t.Context = b.String()
	return t, nil
}

func hasLogin(us []user, login string) bool {
	for _, u := range us {
		if strings.EqualFold(u.Login, login) {
			return true
		}
	}
	return false
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if r := []rune(s); len(r) > 140 {
		s = string(r[:140]) + "…"
	}
	return s
}

// Collect is every thing seen lately, from memory (Watch keeps it).
func (c *Connector) Collect(context.Context) ([]connector.InboxThing, error) {
	st := &c.inbox
	st.mu.Lock()
	defer st.mu.Unlock()
	out := make([]connector.InboxThing, 0, len(st.things))
	for _, t := range st.things {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out, nil
}

var threadRef = lazyre.New(`^([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)#(\d+)$`)

// Reply comments the text on the PR / issue (the user pressed send on it).
func (c *Connector) Reply(ctx context.Context, r connector.InboxReply) error {
	thread, text := r.Thread, r.Text
	m := threadRef.FindStringSubmatch(thread)
	if m == nil || !validRepo(m[1]) {
		return fmt.Errorf("%s cannot be replied to", thread)
	}
	body, _ := json.Marshal(map[string]string{"body": text})
	if _, _, err := c.request(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/issues/%s/comments", m[1], m[2]), nil, body, nil); err != nil {
		return err
	}
	n, _ := strconv.Atoi(m[2])
	c.forget(m[1], n)
	st := &c.inbox
	st.mu.Lock()
	if t := st.things[thread]; t != nil {
		t.Done = connector.DoneReplied
	}
	st.mu.Unlock()
	return nil
}

// tierOf is the tier the rules give a PR / issue notification (reason) by who made the newest move, "" when they
// cannot tell (a person's ordinary comment on my PR: the model decides).
func tierOf(reason string, author user, moves []move) (tier connector.Tier, why string) {
	var last *move
	if len(moves) > 0 {
		last = &moves[len(moves)-1]
	}
	byBot := isBot(author)
	if last != nil {
		byBot = last.bot
	}
	switch reason {
	case "review_requested":
		if isBot(author) {
			return connector.TierFYI, "规则：机器人的 PR 请你 review"
		}
		return connector.TierAction, "规则：请你 review"
	case "mention":
		if byBot {
			return connector.TierFYI, "规则：机器人 @ 了你"
		}
		return connector.TierAction, "规则：真人 @ 了你"
	case "assign":
		return connector.TierAction, "规则：分配给你"
	case "team_mention":
		return connector.TierFYI, "规则：@ 的是你所在的团队"
	case "author":
		switch {
		case last == nil:
			return "", ""
		case last.bot:
			return connector.TierFYI, "规则：机器人在你的 PR / issue 上留言"
		case last.state == "CHANGES_REQUESTED":
			return connector.TierAction, "规则：要求修改"
		case last.state == "APPROVED":
			return connector.TierFYI, "规则：批准了"
		}
	}
	return "", ""
}
