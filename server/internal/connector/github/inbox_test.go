package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
)

// fakeNotifications is GitHub's notifications and the threads they are about, on made-up data. The test changes
// notifications, prs and comments between polls; the server counts what it was asked.
type fakeNotifications struct {
	mu            sync.Mutex
	notifications string
	prs           map[string]string // path -> JSON
	comments      map[string]string // path -> JSON
	notModified   bool              // answer 304 when If-Modified-Since comes
	sinceSeen     []string
	requests      []string
	posted        []string
}

func (f *fakeNotifications) server(t *testing.T) *Connector {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/" { // a node's port probe (node/ports.go), not this test's client
			http.NotFound(w, r)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requests = append(f.requests, r.Method+" "+r.URL.Path)
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "4000")
		switch {
		case r.URL.Path == "/user":
			_, _ = w.Write([]byte(`{"login":"me"}`))
		case r.URL.Path == "/notifications":
			f.sinceSeen = append(f.sinceSeen, r.Header.Get("If-Modified-Since"))
			w.Header().Set("X-Poll-Interval", "90")
			if f.notModified && r.Header.Get("If-Modified-Since") != "" {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("Last-Modified", "Mon, 02 Nov 2026 10:00:00 GMT")
			_, _ = w.Write([]byte(f.notifications))
		case r.Method == http.MethodPost:
			b, _ := io.ReadAll(r.Body)
			f.posted = append(f.posted, r.URL.Path+" "+string(b))
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{}`))
		case f.prs[r.URL.Path] != "":
			_, _ = w.Write([]byte(f.prs[r.URL.Path]))
		case f.comments[r.URL.Path] != "":
			_, _ = w.Write([]byte(f.comments[r.URL.Path]))
		case strings.HasSuffix(r.URL.Path, "/comments") || strings.HasSuffix(r.URL.Path, "/reviews"):
			_, _ = w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		}
	}))
	t.Cleanup(srv.Close)
	c := New(Config{Auth: AuthConfig{FromMachine: "mac"}}, "Mac", (&machine{tokens: []string{oldToken}}).fetch)
	c.api = srv.URL
	c.check(context.Background())
	return c
}

func note(id, reason, typ, url, updated string) string {
	b, _ := json.Marshal(map[string]any{"id": id, "reason": reason, "updated_at": updated,
		"subject":    map[string]string{"title": "Subject " + id, "url": url, "type": typ},
		"repository": map[string]string{"full_name": "example-org/example-repo", "html_url": "https://github.com/example-org/example-repo"}})
	return string(b)
}

const (
	api12 = "/repos/example-org/example-repo/pulls/12"
	api7  = "/repos/example-org/example-repo/issues/7"
)

// fixtureDay is midnight UTC the day before the test run, and fixture times are offsets from it (from dayBefore for
// times on the day before that). Fixed dates would age past the windows the code measures against the clock
// (forgetAfter, archiveFirstDays, archiveForget) and fail the tests once the real date moves on.
var fixtureDay = time.Now().UTC().Truncate(24 * time.Hour).Add(-24 * time.Hour)

const dayBefore = -24 * time.Hour

// fixtureAt is fixtureDay plus d, as GitHub writes times.
func fixtureAt(d time.Duration) string { return fixtureDay.Add(d).Format(time.RFC3339) }

var (
	t1 = fixtureAt(9 * time.Hour)
	t2 = fixtureAt(9*time.Hour + 30*time.Minute)
)

func openPR(reviewers string) string {
	return `{"title":"Add the example","state":"open","merged":false,"user":{"login":"alice"},"body":"示例描述\n第二行",
		"html_url":"https://github.com/example-org/example-repo/pull/12","requested_reviewers":[` + reviewers + `]}`
}

func things(t *testing.T, c *Connector) map[string]connector.InboxThing {
	out := map[string]connector.InboxThing{}
	list, _ := c.Collect(context.Background())
	for _, th := range list {
		out[th.Thread] = th
	}
	return out
}

func TestNotificationsBecomeThings(t *testing.T) {
	f := &fakeNotifications{prs: map[string]string{
		api12: openPR(`{"login":"me"}`),
		"/repos/example-org/example-repo/issues/12": `{"comments":1}`,
		api7: `{"title":"Example bug","state":"open","user":{"login":"bob"},"body":"出错了","html_url":"https://github.com/example-org/example-repo/issues/7","comments":0}`,
	}, comments: map[string]string{
		"/repos/example-org/example-repo/issues/12/comments": `[{"user":{"login":"bob"},"body":"请看一下示例","created_at":"` + fixtureAt(8*time.Hour+59*time.Minute) + `"}]`,
	}}
	f.notifications = "[" + strings.Join([]string{
		note("1", "review_requested", "PullRequest", "https://api.github.com"+api12, t1),
		note("2", "assign", "Issue", "https://api.github.com"+api7, t1),
		note("4", "subscribed", "Issue", "https://api.github.com/repos/example-org/example-repo/issues/8", t1),
	}, ",") + "]" // CI: ci_test.go
	c := f.server(t)

	wait, moved, err := c.poll(context.Background())
	if err != nil || !moved || wait != 90*time.Second {
		t.Fatalf("poll: %v %v %v", wait, moved, err)
	}
	got := things(t, c)
	if len(got) != 2 {
		t.Fatalf("%d things: %+v", len(got), got)
	}
	pr := got["example-org/example-repo#12"]
	if pr.Kind != "review_requested" || pr.Label != "请你 review" || pr.Tier != "action" || pr.Done != "" || !pr.CanReply ||
		pr.Sender != "bob" || pr.Preview != "请看一下示例" || pr.URL != "https://github.com/example-org/example-repo/pull/12" ||
		pr.ReplyTo != "评论到 example-org/example-repo#12（仓库可见的人都能看到）" || pr.Last != t1 || pr.Why != "规则：请你 review" {
		t.Errorf("review request: %+v", pr)
	}
	if !strings.Contains(pr.Context, "GitHub PR example-org/example-repo#12：Add the example") || !strings.Contains(pr.Context, "等这些人 review：me") ||
		!strings.Contains(pr.Context, "bob：\n请看一下示例") {
		t.Errorf("context:\n%s", pr.Context)
	}
	if is := got["example-org/example-repo#7"]; is.Kind != "assign" || is.Sender != "bob" || is.Preview != "出错了" || is.Tier != "action" {
		t.Errorf("assignment: %+v", is)
	}

	// Nothing changed: GitHub answers 304 to If-Modified-Since, nothing is read.
	f.notModified = true
	before := len(f.requests)
	if _, moved, _ := c.poll(context.Background()); moved || len(f.requests) != before+1 || f.sinceSeen[len(f.sinceSeen)-1] == "" {
		t.Fatalf("304: moved %v, %d requests, since %q", moved, len(f.requests)-before, f.sinceSeen)
	}

	// Answered: my comment is the newest; the review request withdrawn without a review: read; the issue is gone
	// from the unread list: read elsewhere, at the second read it is missing from (inbox_read_test.go).
	f.notModified = false
	c.inbox.fullAt = time.Time{}
	f.comments["/repos/example-org/example-repo/issues/12/comments"] = `[{"user":{"login":"bob"},"body":"请看一下示例","created_at":"` + fixtureAt(8*time.Hour+59*time.Minute) + `"},
		{"user":{"login":"me"},"body":"看过了","created_at":"` + fixtureAt(9*time.Hour+29*time.Minute) + `"}]`
	f.notifications = "[" + note("1", "review_requested", "PullRequest", "https://api.github.com"+api12, t2) + "]"
	c.poll(context.Background())
	got = things(t, c)
	if got["example-org/example-repo#12"].Done != connector.DoneReplied {
		t.Errorf("answered: %+v", got["example-org/example-repo#12"])
	}
	if got["example-org/example-repo#7"].Read {
		t.Errorf("read at the first list it is missing from: %+v", got["example-org/example-repo#7"])
	}
	c.poll(context.Background())
	if got = things(t, c); !got["example-org/example-repo#7"].Read {
		t.Errorf("no longer unread: %+v", got)
	}
	if since := f.sinceSeen[len(f.sinceSeen)-1]; since != "" {
		t.Errorf("the read that settles #7 asked If-Modified-Since %q", since)
	}
}

func TestReviewRequestWithdrawnOrMerged(t *testing.T) {
	f := &fakeNotifications{prs: map[string]string{api12: openPR(""), "/repos/example-org/example-repo/issues/12": `{"comments":0}`},
		comments: map[string]string{}}
	f.notifications = "[" + note("1", "review_requested", "PullRequest", "https://api.github.com"+api12, t1) + "]"
	c := f.server(t)
	c.poll(context.Background())
	if th := things(t, c)["example-org/example-repo#12"]; th.Done != connector.DoneRead {
		t.Errorf("withdrawn: %+v", th)
	}

	f.comments["/repos/example-org/example-repo/pulls/12/reviews"] = `[{"user":{"login":"me"},"state":"APPROVED","body":"","submitted_at":"` + fixtureAt(9*time.Hour+10*time.Minute) + `"}]`
	f.notifications = "[" + note("1", "review_requested", "PullRequest", "https://api.github.com"+api12, t2) + "]"
	c.inbox.fullAt = time.Time{}
	c.poll(context.Background())
	if th := things(t, c)["example-org/example-repo#12"]; th.Done != connector.DoneReplied {
		t.Errorf("reviewed: %+v", th)
	}

	f.prs[api12] = strings.Replace(openPR(`{"login":"me"}`), `"merged":false`, `"merged":true`, 1)
	f.notifications = "[" + note("1", "review_requested", "PullRequest", "https://api.github.com"+api12, fixtureAt(10*time.Hour)) + "]"
	c.inbox.fullAt = time.Time{}
	c.poll(context.Background())
	if th := things(t, c)["example-org/example-repo#12"]; th.Done != connector.DoneRead {
		t.Errorf("merged: %+v", th)
	}
}

// With requests running low only the notifications are read.
func TestFewRequestsLeftReadsNoThreads(t *testing.T) {
	f := &fakeNotifications{prs: map[string]string{api12: openPR(`{"login":"me"}`)}, comments: map[string]string{}}
	f.notifications = "[" + note("1", "review_requested", "PullRequest", "https://api.github.com"+api12, t1) + "]"
	c := f.server(t)
	h := http.Header{}
	h.Set("X-RateLimit-Limit", "5000")
	h.Set("X-RateLimit-Remaining", "100")
	h.Set("X-RateLimit-Reset", "9999999999")
	c.noteRate(h)
	if st := c.Status(context.Background()); st.State != connector.StateRateLimited || !strings.Contains(st.Error, "只看通知") {
		t.Fatalf("%+v", st)
	}
	f.mu.Lock()
	before := len(f.requests)
	f.mu.Unlock()
	var ns []notification
	_ = json.Unmarshal([]byte(f.notifications), &ns)
	c.applyNotifications(context.Background(), ns, true, time.Now())
	if len(f.requests) != before {
		t.Fatalf("threads were read with few requests left: %v", f.requests[before:])
	}
}

func TestReplyComments(t *testing.T) {
	f := &fakeNotifications{prs: map[string]string{}, comments: map[string]string{}}
	f.notifications = "[]"
	c := f.server(t)
	if err := c.Reply(context.Background(), connector.InboxReply{Thread: "example-org/example-repo#12", Text: "看过了"}); err != nil {
		t.Fatal(err)
	}
	if len(f.posted) != 1 || f.posted[0] != `/repos/example-org/example-repo/issues/12/comments {"body":"看过了"}` {
		t.Fatalf("%v", f.posted)
	}
	for _, bad := range []string{"notification:3", "../x#1", "example-org/example-repo#x"} {
		if err := c.Reply(context.Background(), connector.InboxReply{Thread: bad, Text: "x"}); err == nil {
			t.Errorf("%s was replied to", bad)
		}
	}
}
