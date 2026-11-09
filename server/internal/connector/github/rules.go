package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/lazyre"
)

// What of GitHub's notifications reaches the inbox, and how urgent it is (docs/assistant/inbox.md "Connector: GitHub"): the
// rules decide the tier, the model only what they cannot (a person's ordinary comment on my PR). CI failures are
// one item per repository and branch; branches pushed by automation are left out; the user picks which CI failures
// they want and mutes repositories (Options: Settings › Connectors › GitHub, or hub.json github.inbox).

// CI modes (InboxOptions.CI).
const (
	CIDefaultBranch = "default_branch" // only the default branch's failures
	CIMine          = "mine"           // and those of my own open PRs (the default); other branches as FYI
	CIAll           = "all"            // every branch's, as needing me
	CIOff           = "off"            // none
)

// DefaultIgnoreBranches are branches automation pushes all day (a training loop's train/<timestamp>).
var DefaultIgnoreBranches = []string{"train/*"}

// InboxOptions are the user's choices for GitHub's inbox.
type InboxOptions struct {
	CI         string   `json:"ci,omitempty"`
	MutedRepos []string `json:"muted_repos,omitempty"`
	// IgnoreBranches are path.Match patterns of branches whose CI never comes in; nil is DefaultIgnoreBranches.
	IgnoreBranches []string `json:"ignore_branches,omitempty"`
}

func (o InboxOptions) ci() string {
	if o.CI == "" {
		return CIMine
	}
	return o.CI
}

func (o InboxOptions) muted(repo string) bool {
	return slices.ContainsFunc(o.MutedRepos, func(m string) bool { return strings.EqualFold(m, repo) })
}

func (o InboxOptions) ignoredBranch(branch string) bool {
	patterns := o.IgnoreBranches
	if patterns == nil {
		patterns = DefaultIgnoreBranches
	}
	for _, p := range patterns {
		if ok, _ := path.Match(p, branch); ok {
			return true
		}
	}
	return false
}

func (o InboxOptions) check() error {
	switch o.CI {
	case "", CIDefaultBranch, CIMine, CIAll, CIOff:
	default:
		return fmt.Errorf("ci must be %s, %s, %s or %s", CIDefaultBranch, CIMine, CIAll, CIOff)
	}
	for _, r := range o.MutedRepos {
		if !validRepo(r) {
			return fmt.Errorf("muted repository %q is not owner/name", r)
		}
	}
	for _, p := range o.IgnoreBranches {
		if _, err := path.Match(p, ""); err != nil || p == "" {
			return fmt.Errorf("bad branch pattern %q", p)
		}
	}
	return nil
}

func (c *Connector) options() InboxOptions {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.opts
}

// Options are the inbox's choices as the app shows them (defaults filled in).
func (c *Connector) Options() json.RawMessage {
	o := c.options()
	o.CI = o.ci()
	if o.IgnoreBranches == nil {
		o.IgnoreBranches = DefaultIgnoreBranches
	}
	if o.MutedRepos == nil {
		o.MutedRepos = []string{}
	}
	b, _ := json.Marshal(o)
	return b
}

// SetOptions takes the user's choices; the notifications are read again under them at the next poll, and the hub
// settles what they leave out (Settle).
func (c *Connector) SetOptions(raw json.RawMessage) error {
	var o InboxOptions
	if err := json.Unmarshal(raw, &o); err != nil {
		return fmt.Errorf("options: %v", err)
	}
	if err := o.check(); err != nil {
		return err
	}
	c.mu.Lock()
	c.opts = o
	c.mu.Unlock()
	st := &c.inbox
	st.mu.Lock()
	st.seen, st.fullAt = map[string]string{}, time.Time{}
	for thread, t := range st.things {
		if why := c.over(thread, o); why != "" && t.Done == "" {
			t.Done, t.Why = connector.DoneRead, why
		}
	}
	st.mu.Unlock()
	return nil
}

// ---- CI ----

// ciTitle is a CheckSuite notification's title: "<workflow> workflow run[, Attempt #N] <status> for <branch> branch"
// (it has no URL: the branch is only in the title).
var ciTitle = lazyre.New(`^(.+) workflow run(?:, Attempt #\d+)? (.+?) for (.+) branch$`)

type ciRun struct {
	workflow, status, branch string
}

func parseCI(title string) (ciRun, bool) {
	m := ciTitle.FindStringSubmatch(strings.TrimSpace(title))
	if m == nil {
		return ciRun{}, false
	}
	return ciRun{workflow: m[1], status: strings.ToLower(m[2]), branch: m[3]}, true
}

func (r ciRun) failed() bool {
	return r.status == "failed" || r.status == "timed out" || r.status == "failure"
}

// ciThread is the one item of a repository's branch: reruns and later failures join it.
func ciThread(repo, branch string) string { return "ci:" + repo + "@" + branch }

func splitCIThread(thread string) (repo, branch string, ok bool) {
	rest, ok := strings.CutPrefix(thread, "ci:")
	if !ok {
		return "", "", false
	}
	repo, branch, ok = strings.Cut(rest, "@")
	return repo, branch, ok
}

// ciItemTitle names the repository as well as the branch: the items of several repositories' main differ.
func ciItemTitle(repo, branch string) string {
	return path.Base(repo) + " · " + branch + " 的 CI 失败"
}

// ciContext is a CI item's context, with its newest failure.
func ciContext(repo, branch string, pr int, failure string) string {
	where := branch + " 分支"
	if pr > 0 {
		where += fmt.Sprintf("（PR #%d）", pr)
	}
	return fmt.Sprintf("仓库 %s 的 CI（GitHub Actions）在 %s 上失败了：%s", repo, where, failure)
}

// ciMeta is what the follow-up check of a CI item needs.
type ciMeta struct {
	repo, branch string
	pr           int       // my open PR of the branch, 0 when none
	checked      time.Time // last follow-up check
}

var (
	ciRecheck   = 10 * time.Minute // how often an open CI item is checked for a later failure or success, a deleted branch, a closed PR
	ciChecks    = 10               // follow-up checks per poll
	repoMetaFor = 24 * time.Hour   // how long a repository's default branch is remembered
	// ciFoldAfter is how long a failed run waits before the follow-up adds it to the item: GitHub notifies the
	// account that started a run about 20 seconds after it ends, and that notification is the same failure.
	ciFoldAfter = 3 * time.Minute
)

type repoMeta struct {
	defaultBranch string
	at            time.Time
}

// defaultBranch is the repository's default branch (main, master…), remembered for a day.
func (c *Connector) defaultBranch(ctx context.Context, repo string) (string, error) {
	st := &c.inbox
	st.mu.Lock()
	m, ok := st.repos[repo]
	st.mu.Unlock()
	if ok && time.Since(m.at) < repoMetaFor {
		return m.defaultBranch, nil
	}
	var r struct {
		DefaultBranch string `json:"default_branch"`
	}
	if _, err := c.Get(ctx, "/repos/"+repo, &r); err != nil {
		return "", err
	}
	st.mu.Lock()
	if st.repos == nil {
		st.repos = map[string]repoMeta{}
	}
	st.repos[repo] = repoMeta{defaultBranch: r.DefaultBranch, at: time.Now()}
	st.mu.Unlock()
	return r.DefaultBranch, nil
}

// myOpenPR is my open PR from branch in repo, 0 when there is none.
func (c *Connector) myOpenPR(ctx context.Context, repo, branch, me string) (int, error) {
	owner, _, _ := strings.Cut(repo, "/")
	var prs []struct {
		Number int  `json:"number"`
		User   user `json:"user"`
	}
	q := url.Values{"head": {owner + ":" + branch}, "state": {"open"}}
	if _, err := c.Get(ctx, "/repos/"+repo+"/pulls?"+q.Encode(), &prs); err != nil {
		return 0, err
	}
	for _, p := range prs {
		if strings.EqualFold(p.User.Login, me) {
			return p.Number, nil
		}
	}
	return 0, nil
}

// describeCI makes a failed CI run into its branch's item, with the tier the options give; ok false: not for the
// inbox (a branch the user does not want).
func (c *Connector) describeCI(ctx context.Context, n notification, run ciRun, me string, o InboxOptions) (connector.InboxThing, bool, error) {
	repo := n.Repository.FullName
	t := connector.InboxThing{Kind: "ci_failed", Label: "CI 失败", Sender: "GitHub Actions", Preview: n.Subject.Title, At: n.UpdatedAt,
		Title: ciItemTitle(repo, run.branch), URL: n.Repository.HTMLURL + "/actions?query=" + url.QueryEscape("branch:"+run.branch)}
	def, err := c.defaultBranch(ctx, repo)
	if err != nil {
		return t, false, err
	}
	pr := 0
	switch {
	case run.branch == def:
		t.Tier, t.Why = connector.TierAction, "规则：默认分支的 CI 失败"
	case o.ci() == CIDefaultBranch:
		return t, false, nil
	case o.ci() == CIAll:
		t.Tier, t.Why = connector.TierAction, "规则：CI 失败（设置为全部提醒）"
	default: // mine
		if pr, err = c.myOpenPR(ctx, repo, run.branch, me); err != nil {
			return t, false, err
		}
		if pr > 0 {
			t.Tier, t.Why = connector.TierAction, "规则：你开着的 PR 的 CI 失败"
		} else {
			t.Tier, t.Why = connector.TierFYI, "规则：功能分支的 CI 失败"
		}
	}
	t.Context = ciContext(repo, run.branch, pr, n.Subject.Title)
	st := &c.inbox
	st.mu.Lock()
	if st.ci == nil {
		st.ci = map[string]*ciMeta{}
	}
	st.ci[ciThread(repo, run.branch)] = &ciMeta{repo: repo, branch: run.branch, pr: pr, checked: time.Now()}
	st.mu.Unlock()
	return t, true, nil
}

// followUpCI checks open CI items (at most ciChecks, each every ciRecheck). The branch green again (no workflow's
// newest run failed), the branch deleted, the PR merged or closed ends the item; a newer failed run joins it, since
// GitHub notifies only the account that started a run.
func (c *Connector) followUpCI(ctx context.Context) bool {
	st := &c.inbox
	st.mu.Lock()
	var due []string
	for thread, m := range st.ci {
		if t := st.things[thread]; t != nil && t.Done == "" && time.Since(m.checked) >= ciRecheck {
			due = append(due, thread)
		}
	}
	st.mu.Unlock()
	slices.Sort(due)
	moved := false
	for i, thread := range due {
		if i >= ciChecks || c.ratesLow() {
			break
		}
		st.mu.Lock()
		m := *st.ci[thread]
		st.ci[thread].checked = time.Now()
		st.mu.Unlock()
		check, err := c.ciCheck(ctx, m, time.Now())
		if err != nil {
			continue
		}
		st.mu.Lock()
		if t := st.things[thread]; t != nil && t.Done == "" {
			switch {
			case check.over != "":
				t.Done, t.Why, moved = connector.DoneRead, check.over, true
			case check.failed.at.After(t.At):
				// News the user has not seen, even if they read the item's notification: GitHub sent none of this run.
				t.At, t.Last, t.Read = check.failed.at, check.failed.at.UTC().Format(time.RFC3339), false
				t.Preview = check.failed.workflow + " workflow run failed for " + m.branch + " branch" // as GitHub titles its notification
				t.Context = ciContext(m.repo, m.branch, m.pr, t.Preview)
				moved = true
			}
		}
		st.mu.Unlock()
	}
	return moved
}

// ciState is what a follow-up check found: why the item is over ("" while it is not), else the branch's newest
// failed run settled long enough to add (zero when none).
type ciState struct {
	over   string
	failed struct {
		workflow string
		at       time.Time
	}
}

// ciCheck reads where a CI item's branch stands now (now: the check's time).
func (c *Connector) ciCheck(ctx context.Context, m ciMeta, now time.Time) (ciState, error) {
	var s ciState
	if m.pr > 0 {
		var pr struct {
			State  string `json:"state"`
			Merged bool   `json:"merged"`
		}
		if _, err := c.Get(ctx, fmt.Sprintf("/repos/%s/pulls/%d", m.repo, m.pr), &pr); err != nil {
			return s, err
		}
		if pr.Merged {
			s.over = "GitHub：PR 已合并"
			return s, nil
		}
		if pr.State == "closed" {
			s.over = "GitHub：PR 已关闭"
			return s, nil
		}
	}
	var runs struct {
		Runs []struct {
			Name       string    `json:"name"`
			Status     string    `json:"status"`
			Conclusion string    `json:"conclusion"`
			UpdatedAt  time.Time `json:"updated_at"`
		} `json:"workflow_runs"`
	}
	if _, err := c.Get(ctx, "/repos/"+m.repo+"/actions/runs?"+url.Values{"branch": {m.branch}, "per_page": {"20"}}.Encode(), &runs); err != nil {
		return s, err
	}
	// Each workflow stands where its newest run that succeeded or failed left it (newest first; runs still going,
	// cancelled or skipped say neither).
	settled, failing := map[string]bool{}, false
	for _, r := range runs.Runs {
		if settled[r.Name] || r.Status != "completed" {
			continue
		}
		switch r.Conclusion {
		case "success":
			settled[r.Name] = true
		case "failure", "timed_out", "startup_failure":
			settled[r.Name], failing = true, true
			if r.UpdatedAt.After(s.failed.at) && now.Sub(r.UpdatedAt) >= ciFoldAfter {
				s.failed.workflow, s.failed.at = r.Name, r.UpdatedAt
			}
		}
	}
	if len(settled) > 0 && !failing {
		s.over = "GitHub：后来成功了"
		return s, nil
	}
	code, _, err := c.request(ctx, http.MethodGet, "/repos/"+m.repo+"/branches/"+url.PathEscape(m.branch), nil, nil, nil)
	if code == http.StatusNotFound {
		s.over = "GitHub：分支已删除"
		return s, nil
	}
	if err != nil {
		return s, err
	}
	return s, nil
}

// ---- settling what the hub still has open ----

// over says why a thread is out of the inbox under the options without asking GitHub ("" while it is in).
func (c *Connector) over(thread string, o InboxOptions) string {
	if strings.HasPrefix(thread, "notification:") {
		return "GitHub 降噪：CI 失败改为按分支合并成一条"
	}
	repo := thread
	if r, branch, ok := splitCIThread(thread); ok {
		repo = r
		switch {
		case o.ignoredBranch(branch):
			return "GitHub：自动分支的 CI 不提醒"
		case o.ci() == CIOff:
			return "GitHub：CI 失败已设为不提醒"
		}
	} else if r, _, ok := strings.Cut(thread, "#"); ok {
		repo = r
	}
	if o.muted(repo) {
		return "GitHub：仓库已静音"
	}
	return ""
}

// Settle closes the hub's open GitHub items the current rules leave out: CI items made one per notification
// (before CI failures were merged per branch), muted repositories, branches and CI the user does not want.
func (c *Connector) Settle(_ context.Context, threads []string) map[string]string {
	o := c.options()
	out := map[string]string{}
	for _, t := range threads {
		if why := c.over(t, o); why != "" {
			out[t] = why
		}
	}
	return out
}
