package github

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
)

// healthyGitHub answers /user with a nearly full primary quota (4900 of 5000 left).
func healthyGitHub(t *testing.T) *Connector {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "4900")
		w.Header().Set("X-RateLimit-Reset", "9999999999")
		_, _ = w.Write([]byte(`{"login":"alice"}`))
	}))
	t.Cleanup(srv.Close)
	c := New(Config{Auth: AuthConfig{FromMachine: "mac"}, APIRoot: srv.URL}, "Mac", (&machine{tokens: []string{oldToken}}).fetch)
	c.check(context.Background())
	return c
}

func retryAfter(v string) http.Header {
	h := http.Header{}
	h.Set("Retry-After", v)
	return h
}

var secondaryLimit = errors.New("GitHub GET /x: 429 secondary rate limit")

// A Retry-After pause holds until it ends, whatever good answers come in between.
func TestPauseOutlastsGoodAnswers(t *testing.T) {
	c := healthyGitHub(t)
	c.notePause(retryAfter("600"), secondaryLimit)
	if !c.ratesLow() {
		t.Fatal("not paused after a secondary rate limit")
	}
	var u struct{ Login string }
	if _, err := c.Get(context.Background(), "/user", &u); err != nil {
		t.Fatal(err)
	}
	if !c.ratesLow() {
		t.Fatal("a good answer lifted the pause")
	}
	if st := c.Status(context.Background()); st.State != connector.StateRateLimited || !strings.Contains(st.Error, "约 10 分钟") {
		t.Fatalf("%+v", st)
	}
	if reset := c.PRChecksRateReset(); reset.Before(time.Now().Add(9 * time.Minute)) {
		t.Fatalf("checks resume at %v", reset)
	}

	// A shorter Retry-After later on does not shorten the pause.
	c.notePause(retryAfter("30"), secondaryLimit)
	if reset := c.PRChecksRateReset(); reset.Before(time.Now().Add(9 * time.Minute)) {
		t.Fatalf("a shorter Retry-After moved the end to %v", reset)
	}

	// Over, the pause no longer holds anything back.
	c.mu.Lock()
	c.pausedUntil = time.Now().Add(-time.Second)
	c.mu.Unlock()
	if c.ratesLow() || !c.PRChecksRateReset().IsZero() {
		t.Fatal("still paused after the pause ended")
	}
	if st := c.Status(context.Background()); st.State != connector.StateOK {
		t.Fatalf("%+v", st)
	}
}

// An error that is not a rate limit does not pause.
func TestPauseOnlyForRateLimits(t *testing.T) {
	c := healthyGitHub(t)
	c.notePause(http.Header{}, errors.New("GitHub GET /x: 403 Resource not accessible by integration"))
	c.notePause(http.Header{}, errors.New("GitHub GET /x: 500 boom"))
	if c.ratesLow() {
		t.Fatal("paused without a rate limit")
	}
}

// The primary quota works as before: a low quota saves requests, a good one ends that, and the later of a low
// quota's reset and a pause's end is when requests resume.
func TestPrimaryQuotaBesidePause(t *testing.T) {
	c := healthyGitHub(t)
	quota := func(left string, reset time.Time) {
		h := http.Header{}
		h.Set("X-RateLimit-Limit", "5000")
		h.Set("X-RateLimit-Remaining", left)
		h.Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
		c.noteRate(h)
	}
	reset := time.Now().Add(time.Hour)
	quota("100", reset)
	if !c.ratesLow() {
		t.Fatal("100 of 5000 left is not low")
	}
	quota("4900", reset)
	if c.ratesLow() {
		t.Fatal("4900 of 5000 left is still low")
	}

	quota("100", reset)
	c.notePause(retryAfter("60"), secondaryLimit)
	if got := c.PRChecksRateReset(); got.Unix() != reset.Unix() {
		t.Fatalf("resume at %v, not at the quota's reset %v", got, reset)
	}
	if st := c.Status(context.Background()); st.State != connector.StateRateLimited || !strings.Contains(st.Error, "约 60 分钟") {
		t.Fatalf("%+v", st)
	}
	quota("4900", reset)
	if got := c.PRChecksRateReset(); got.Before(time.Now().Add(50*time.Second)) || got.After(time.Now().Add(time.Minute)) {
		t.Fatalf("resume at %v, not at the pause's end", got)
	}
}

// Without Retry-After a limit pauses for a minute, as GitHub's docs say, unless the hour's core requests are used up
// (X-RateLimit-Remaining 0): X-RateLimit-Reset, which every answer carries, is only the end of that. Another quota
// used up (search) is noteRate's and pauses nothing.
func TestPauseWithoutRetryAfter(t *testing.T) {
	in := func(d time.Duration) string { return strconv.FormatInt(time.Now().Add(d).Unix(), 10) }
	quota := func(left, reset, resource string) http.Header {
		h := http.Header{}
		h.Set("X-RateLimit-Limit", "5000")
		h.Set("X-RateLimit-Remaining", left)
		h.Set("X-RateLimit-Reset", reset)
		if resource != "" {
			h.Set("X-RateLimit-Resource", resource)
		}
		return h
	}
	secondary := errors.New("GitHub GET /notifications: 403 You have exceeded a secondary rate limit")
	for _, tc := range []struct {
		name     string
		h        http.Header
		err      error
		min, max time.Duration
	}{
		{"secondary 403, quota left", quota("4000", in(50*time.Minute), "core"), secondary, 59 * time.Second, time.Minute},
		{"429 without headers", http.Header{}, secondaryLimit, 59 * time.Second, time.Minute},
		{"the hour used up", quota("0", in(5*time.Minute), ""), errors.New("GitHub GET /x: 403 API rate limit exceeded"),
			4*time.Minute + 58*time.Second, 5 * time.Minute},
		{"the hour used up, reset passed", quota("0", in(-time.Minute), "core"), secondary, 59 * time.Second, time.Minute},
		{"the search minute used up", quota("0", in(30*time.Second), "search"), errors.New("GitHub GET /search/issues: 403 API rate limit exceeded"), 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := healthyGitHub(t)
			c.notePause(tc.h, tc.err)
			if left := c.pauseLeft(); left < tc.min || left > tc.max {
				t.Fatalf("paused for %s, want [%s, %s]", left, tc.min, tc.max)
			}
		})
	}
}

// A limit that comes back right after a pause GitHub named no time for doubles it, up to an hour; reads that run into
// it during the pause add nothing, and a quiet while later it starts over at a minute.
func TestPauseBacksOff(t *testing.T) {
	c := healthyGitHub(t)
	limit := func() time.Duration {
		c.notePause(http.Header{}, secondaryLimit)
		return c.pauseLeft()
	}
	ended := func(ago time.Duration) {
		c.mu.Lock()
		c.pausedUntil, c.backoffUntil = time.Now().Add(-ago), time.Now().Add(-ago)
		c.mu.Unlock()
	}
	near := func(got, want time.Duration) bool { return got > want-2*time.Second && got <= want }
	if got := limit(); !near(got, time.Minute) {
		t.Fatalf("first pause %s", got)
	}
	if got := limit(); !near(got, time.Minute) {
		t.Fatalf("a second read during the pause made it %s", got)
	}
	for _, want := range []time.Duration{2 * time.Minute, 4 * time.Minute, 8 * time.Minute} {
		ended(time.Second)
		if got := limit(); !near(got, want) {
			t.Fatalf("again right after the pause: %s, want %s", got, want)
		}
	}
	c.mu.Lock()
	c.pauseStep = 40 * time.Minute
	c.mu.Unlock()
	ended(time.Second)
	if got := limit(); !near(got, time.Hour) {
		t.Fatalf("not capped at an hour: %s", got)
	}
	ended(2 * time.Hour)
	if got := limit(); !near(got, time.Minute) {
		t.Fatalf("long after the last pause: %s, want a minute", got)
	}
}

// The backoff doubles only while the limit keeps coming back right after a pause it chose itself (#2149): a pause
// GitHub named (Retry-After) is not one of its steps, and three quiet hours end it.
func TestPauseBackoffIgnoresGitHubsPauses(t *testing.T) {
	c := healthyGitHub(t)
	c.mu.Lock()
	c.pauseStep, c.pausedUntil = time.Hour, time.Now().Add(-3*time.Hour) // the step reached an hour, three quiet hours ago
	c.mu.Unlock()
	c.notePause(retryAfter("60"), secondaryLimit)
	c.mu.Lock()
	c.pausedUntil = time.Now().Add(-time.Second) // GitHub's pause ended a second ago
	c.mu.Unlock()
	c.notePause(http.Header{}, secondaryLimit)
	if left := c.pauseLeft(); left < time.Minute-2*time.Second || left > time.Minute {
		t.Fatalf("a limit without Retry-After after three quiet hours pauses for %s, want a minute", left)
	}
}

// A secondary limit that comes back right after every pause keeps doubling it, though each poll's first page is
// answered before a thread's details run into the limit again: GitHub asks for longer and longer waits while it
// lasts, and an answer in between does not mean it is over (#2207 review).
func TestPauseBackoffGrowsWhileTheLimitComesBack(t *testing.T) {
	var mu sync.Mutex
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "4000")
		switch r.URL.Path {
		case "/user":
			_, _ = w.Write([]byte(`{"login":"me"}`))
		case "/notifications":
			_, _ = w.Write([]byte("[" + note("7", "assign", "Issue", "https://api.github.com"+api7, at.Format(time.RFC3339)) + "]"))
		default: // every thread's details run into a secondary limit GitHub names no time for
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"You have exceeded a secondary rate limit."}`))
		}
	}))
	t.Cleanup(srv.Close)
	c := New(Config{Auth: AuthConfig{FromMachine: "mac"}}, "Mac", (&machine{tokens: []string{oldToken}}).fetch)
	c.api = srv.URL
	c.check(context.Background())
	var got []time.Duration
	for range 4 {
		mu.Lock()
		at = at.Add(time.Minute) // a new move on #7: its details are read again
		mu.Unlock()
		pollOnce(t, c)
		got = append(got, c.pauseLeft().Round(time.Second))
		c.mu.Lock() // the pause runs out: the next poll comes a second after it
		shift := time.Until(c.pausedUntil) + time.Second
		c.pausedUntil, c.backoffUntil = c.pausedUntil.Add(-shift), c.backoffUntil.Add(-shift)
		c.mu.Unlock()
	}
	if want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute}; !slices.Equal(got, want) {
		t.Fatalf("pauses %v, want %v: the limit came back right after every pause", got, want)
	}
}
