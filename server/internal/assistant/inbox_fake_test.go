package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The inbox's tests (the manager's inbox*.go) run on fakeInbox: the hub's side of the inbox, its sources. Its
// connectors are real connector.Connector test types, read the way the hub reads them (hub/inbox.go readConnectors)
// and asked what the inbox asks of them (replies, settle, the group tools); mail, the hub's settings, the inbox's
// models and what the inbox told the hub are kept here. inboxHub is a Service on it, with the names the hub's inbox
// tests used.

// fakeInbox is the hub's side of the inbox.
type fakeInbox struct {
	mu        sync.Mutex
	svc       *Service
	conns     []connector.Connector
	extension map[string]bool   // the plugins' connectors
	kinds     map[string]string // "<connector>/<kind>" → how a plugin describes it
	off       map[string]bool   // read with nothing: "Send to inbox" off, not signed in, waiting for the triage model
	failing   map[string]error  // Collect fails
	cfg       assistantwire.InboxConfig
	// model is every inbox purpose's model (nil: none set up), models by purpose over it: manager.json's "ai" and its
	// triage / judge / kb connections in the hello (models_resolve.go's baselines; translation and drafts are kb's).
	model   *ai.Client
	models  map[string]*ai.Client
	mail    *fakeMail
	changed []assistantwire.InboxChanged
	kicks   int
	asked   []AppCall // the inbox methods asked, in order
	// sent are the send ids of the replies that went out (inbox.reply, inbox.reply_mail): the same one again is
	// answered already, as the hub does; inDoubt makes the next reply's answer cut off after it went out.
	sent    map[string]bool
	inDoubt bool
}

// fakeMail is the hub's mail: accounts with their latest messages, the full texts, the replies sent. fetched runs
// when a text was read, before it is answered (a round's mail step has let the inbox go meanwhile).
type fakeMail struct {
	accounts []assistantwire.InboxMailAccount
	bodies   map[uint32]assistantwire.InboxMailMessage
	reads    int
	sent     []map[string]any
	fetched  func(uid uint32)
}

func newFakeInbox() *fakeInbox {
	return &fakeInbox{extension: map[string]bool{}, kinds: map[string]string{}, off: map[string]bool{}, failing: map[string]error{},
		models: map[string]*ai.Client{}, sent: map[string]bool{}}
}

// sendOnce is the hub's send-once guard (hub/inbox.go sendInboxOnce): a send id that went out is already sent.
func (in *fakeInbox) sendOnce(sendID string, send func() error) (assistantwire.InboxSent, error) {
	in.mu.Lock()
	already, cut := sendID != "" && in.sent[sendID], in.inDoubt
	in.inDoubt = false
	in.mu.Unlock()
	if already {
		return assistantwire.InboxSent{Already: true}, nil
	}
	if err := send(); err != nil {
		return assistantwire.InboxSent{}, err
	}
	in.mu.Lock()
	in.sent[sendID] = sendID != ""
	in.mu.Unlock()
	if cut {
		return assistantwire.InboxSent{}, rpc.ErrDropped
	}
	return assistantwire.InboxSent{}, nil
}

// read is one round of the sources as the hub reads them.
func (in *fakeInbox) read(ctx context.Context, round string) assistantwire.InboxRound {
	in.mu.Lock()
	conns := slices.Clone(in.conns)
	in.mu.Unlock()
	r := assistantwire.InboxRound{Round: round, Sources: []assistantwire.InboxSource{}, Names: map[string]string{}}
	for _, c := range conns {
		r.Names[c.ID()] = c.Name()
	}
	if round == assistantwire.RoundFull && in.mail != nil {
		r.Mail = &assistantwire.InboxMailRead{Accounts: slices.Clone(in.mail.accounts)}
	}
	if round == assistantwire.RoundChats {
		return r
	}
	for _, c := range conns {
		inb, ok := connector.As[connector.Inbox](c)
		if !ok {
			continue
		}
		in.mu.Lock()
		src := assistantwire.InboxSource{ID: c.ID(), Name: c.Name(), Extension: in.extension[c.ID()],
			Migrated: slices.Contains(connector.MigratableConnectors, c.ID()), Things: []connector.InboxThing{}}
		off, failing := in.off[c.ID()], in.failing[c.ID()]
		in.mu.Unlock()
		if off {
			src.Off = true
			r.Sources = append(r.Sources, src)
			continue
		}
		if failing != nil {
			continue
		}
		things, err := inb.Collect(ctx)
		if err != nil {
			continue
		}
		things = slices.Clone(things)
		for i := range things { // the hub's vetThings
			things[i].Rules = clip(strings.TrimSpace(things[i].Rules), 4000)
			if things[i].Tier != "" && !things[i].Tier.Known() {
				things[i].Tier = ""
			}
		}
		src.Things = things
		_, src.Group = connector.As[connector.Grouper](c)
		_, src.Meta = connector.As[connector.EventWriter](c)
		src.MetaDescription = strings.TrimSpace(c.Manifest().MetaDescription)
		_, src.Settles = connector.As[connector.InboxSettler](c)
		in.mu.Lock()
		for _, kind := range c.Manifest().Kinds {
			if d := in.kinds[c.ID()+"/"+kind]; d != "" {
				if src.Kinds == nil {
					src.Kinds = map[string]string{}
				}
				src.Kinds[kind] = d
			}
		}
		in.mu.Unlock()
		r.Sources = append(r.Sources, src)
	}
	return r
}

// collect is one round handed to the inbox.
func (in *fakeInbox) collect(ctx context.Context, round string) error {
	return in.svc.InboxCollected(ctx, in.read(ctx, round))
}

func (in *fakeInbox) conn(id string) connector.Connector {
	in.mu.Lock()
	defer in.mu.Unlock()
	for _, c := range in.conns {
		if c.ID() == id {
			return c
		}
	}
	return nil
}

// setModel sets every inbox purpose's model; the hello reads it meanwhile (a started task's snapshot).
func (in *fakeInbox) setModel(m *ai.Client) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.model = m
}

// hubModels are the hello's models: model as manager.json's "ai", models' as the connections the purposes' categories
// fall back to (models.go legacyModelConfig).
func (in *fakeInbox) hubModels() HubModels {
	in.mu.Lock()
	defer in.mu.Unlock()
	var out HubModels
	if in.model != nil {
		out.AI = in.model.Config()
	}
	over := func(purposes ...string) *ai.Config {
		for _, p := range purposes {
			if m := in.models[p]; m != nil {
				c := m.Config()
				return &c
			}
		}
		return nil
	}
	out.TriageAI, out.JudgeAI = over(purposeTriage), over(purposeJudge)
	out.KBAI = over(purposeDraft, purposeTranslate)
	if out.KBAI == nil {
		out.KBAI = out.TriageAI
	}
	return out
}

// answer answers the inbox's app calls; ok false for any other.
func (in *fakeInbox) answer(ctx context.Context, p AppCall) (json.RawMessage, bool, error) {
	if p.AppID != AppInbox {
		return nil, false, nil
	}
	in.mu.Lock()
	in.asked = append(in.asked, p)
	in.mu.Unlock()
	var out any
	var err error
	switch p.Method {
	case assistantwire.InboxCollect:
		var a struct {
			Round string `json:"round"`
		}
		_ = json.Unmarshal(p.Params, &a)
		err = in.collect(ctx, a.Round)
		out = map[string]any{}
	case assistantwire.InboxKick:
		in.mu.Lock()
		in.kicks++
		in.mu.Unlock()
		out = map[string]any{}
	case assistantwire.InboxConfigMethod:
		in.mu.Lock()
		out = in.cfg
		in.mu.Unlock()
	case assistantwire.InboxMailBody:
		var ref assistantwire.InboxMailRef
		_ = json.Unmarshal(p.Params, &ref)
		in.mu.Lock()
		var fetched func(uint32)
		if in.mail == nil {
			err = errors.New("mail is not set up")
		} else if m, ok := in.mail.bodies[ref.UID]; ok {
			in.mail.reads++
			out, fetched = m, in.mail.fetched
		} else {
			err = fmt.Errorf("no mail %d", ref.UID)
		}
		in.mu.Unlock()
		if fetched != nil {
			fetched(ref.UID)
		}
	case assistantwire.InboxReplyMail:
		var a map[string]any
		_ = json.Unmarshal(p.Params, &a)
		sendID, _ := a["send_id"].(string)
		out, err = in.sendOnce(sendID, func() error {
			in.mu.Lock()
			defer in.mu.Unlock()
			if in.mail == nil {
				return errors.New("this item cannot be replied to")
			}
			in.mail.sent = append(in.mail.sent, a)
			return nil
		})
	case assistantwire.InboxReply:
		var a struct {
			Source string               `json:"source"`
			Reply  connector.InboxReply `json:"reply"`
			SendID string               `json:"send_id"`
		}
		_ = json.Unmarshal(p.Params, &a)
		inb, ok := connector.As[connector.Inbox](in.conn(a.Source))
		if !ok {
			return nil, true, errors.New("this item cannot be replied to")
		}
		out, err = in.sendOnce(a.SendID, func() error { return inb.Reply(ctx, a.Reply) })
	case assistantwire.InboxSettle:
		var a struct {
			Source  string   `json:"source"`
			Threads []string `json:"threads"`
		}
		_ = json.Unmarshal(p.Params, &a)
		over := map[string]string{}
		if st, ok := connector.As[connector.InboxSettler](in.conn(a.Source)); ok {
			over = st.Settle(ctx, a.Threads)
		}
		out = map[string]any{"over": over}
	case assistantwire.InboxGroupTool:
		var a struct {
			Source string                  `json:"source"`
			Call   connector.GroupToolCall `json:"call"`
		}
		_ = json.Unmarshal(p.Params, &a)
		g, ok := connector.As[connector.Grouper](in.conn(a.Source))
		if !ok {
			return nil, true, errors.New("no group")
		}
		var raw json.RawMessage
		if raw, err = g.GroupTool(ctx, a.Call); err == nil {
			var v any
			_ = json.Unmarshal(raw, &v)
			b, _ := json.Marshal(pruneAvatars(v)) // as extmgr cuts it for the model (compactJSON)
			out = map[string]any{"result": string(b)}
		}
	case assistantwire.InboxGroupFallback:
		var a struct {
			Source   string                  `json:"source"`
			Fallback connector.GroupFallback `json:"fallback"`
		}
		_ = json.Unmarshal(p.Params, &a)
		g, ok := connector.As[connector.Grouper](in.conn(a.Source))
		if !ok {
			return nil, true, errors.New("no group")
		}
		out, err = g.GroupFallback(ctx, a.Fallback)
	case assistantwire.InboxGroupAppend:
		var a struct {
			Source string                `json:"source"`
			Append connector.GroupAppend `json:"append"`
		}
		_ = json.Unmarshal(p.Params, &a)
		w, ok := connector.As[connector.EventWriter](in.conn(a.Source))
		if !ok {
			return nil, true, errors.New("no meta")
		}
		if string(a.Append.Meta) == "null" { // a new event's: none
			a.Append.Meta = nil
		}
		var thing connector.InboxThing
		if thing, err = w.GroupAppend(ctx, a.Append); err == nil {
			out = map[string]any{"thing": thing}
		}
	default:
		return nil, true, fmt.Errorf("unexpected inbox method %s", p.Method)
	}
	if err != nil {
		return nil, true, err
	}
	raw, _ := json.Marshal(out)
	return raw, true, nil
}

// changes are the inbox.changed the inbox told the hub, in order.
func (in *fakeInbox) changes() []assistantwire.InboxChanged {
	in.mu.Lock()
	defer in.mu.Unlock()
	return slices.Clone(in.changed)
}

// inboxPushes is how many {"type":"inbox"} the apps would have got (one per inbox.changed).
func (in *fakeInbox) pushes() int { return len(in.changes()) }

// inboxMemStore keeps the inbox's items besides the settings, and which items were written.
type inboxMemStore struct {
	memStore
	imu  sync.Mutex
	rows map[string][]byte
	done map[string]*time.Time
	ids  map[string]int
	fail error // InboxItems fails
	down error // PutInboxItem fails (writeFails)
}

func newInboxMemStore() *inboxMemStore {
	return &inboxMemStore{rows: map[string][]byte{}, done: map[string]*time.Time{}, ids: map[string]int{}}
}

func (s *inboxMemStore) PutInboxItem(_ context.Context, id string, data []byte, _ time.Time, doneAt *time.Time) error {
	s.imu.Lock()
	defer s.imu.Unlock()
	if s.down != nil {
		return s.down
	}
	s.rows[id], s.done[id] = data, doneAt
	s.ids[id]++
	return nil
}

func (s *inboxMemStore) InboxItems(_ context.Context, doneSince time.Time) ([][]byte, error) {
	s.imu.Lock()
	defer s.imu.Unlock()
	if s.fail != nil {
		return nil, s.fail
	}
	var out [][]byte
	for id, raw := range s.rows {
		if d := s.done[id]; d == nil || d.After(doneSince) {
			out = append(out, raw)
		}
	}
	return out, nil
}

func (s *inboxMemStore) DeleteInboxItems(_ context.Context, ids []string) error {
	s.imu.Lock()
	defer s.imu.Unlock()
	for _, id := range ids {
		delete(s.rows, id)
		delete(s.done, id)
	}
	return nil
}

// writeFails makes every row write fail (on) or succeed again.
func (s *inboxMemStore) writeFails(on bool) {
	s.imu.Lock()
	defer s.imu.Unlock()
	s.down = nil
	if on {
		s.down = errMuteStoreDown
	}
}

func (s *inboxMemStore) saved(id string) bool {
	s.imu.Lock()
	defer s.imu.Unlock()
	return s.ids[id] > 0
}

// inboxHub is an assistant whose inbox runs on a fakeInbox, under the names the hub's inbox tests used: h.inbox is
// the inbox's state, h.collectInbox a round of the sources. Its knowledge base is its own (newInboxHubKB).
type inboxHub struct {
	*Service
	f  *fakeHub
	in *fakeInbox
	st *inboxMemStore
}

// newInboxHub is an assistant with an inbox on a fake hub (the machine "server", no connectors yet), its state in a
// memory store.
func newInboxHub(t *testing.T) *inboxHub {
	t.Helper()
	return newInboxHubWith(t, nil)
}

// newInboxHubKB is newInboxHub with the assistant's knowledge base open in a temp directory.
func newInboxHubKB(t *testing.T) (*inboxHub, *kb.Engine) {
	t.Helper()
	h := newInboxHubWith(t, &KBOptions{Dir: t.TempDir()})
	return h, kbEng(t, h.Service)
}

func newInboxHubWith(t *testing.T, kbo *KBOptions) *inboxHub {
	t.Helper()
	f := newFakeHub().withMachine()
	if kbo != nil {
		f.hello.KB = &KBSettings{}
	}
	in := newFakeInbox()
	f.inbox = in
	st := newInboxMemStore()
	s := New(Options{Hub: f, Store: func() Store { return st }, Logf: testLogf(t), KB: kbo})
	if kbo != nil {
		t.Cleanup(s.kbClose)
	}
	in.svc = s
	h := &inboxHub{Service: s, f: f, in: in, st: st}
	h.inbox.init()
	return h
}

// addConnector gives the hub a connector (a plugin's when extension).
func (h *inboxHub) addConnector(c connector.Connector, extension bool) {
	h.in.mu.Lock()
	defer h.in.mu.Unlock()
	h.in.conns = append(h.in.conns, c)
	h.in.extension[c.ID()] = extension
}

// asExtension makes a connector one a plugin provides (or not), as the next round says.
func (h *inboxHub) asExtension(id string, on bool) {
	h.in.mu.Lock()
	h.in.extension[id] = on
	h.in.mu.Unlock()
	h.inbox.init()
	h.inbox.srcMu.Lock()
	if on {
		h.inbox.extensions[id] = true
	} else {
		delete(h.inbox.extensions, id)
	}
	h.inbox.srcMu.Unlock()
}

// setConfig changes the hub's inbox settings; the inbox reads them again at once.
func (h *inboxHub) setConfig(fn func(*assistantwire.InboxConfig)) {
	h.in.mu.Lock()
	fn(&h.in.cfg)
	h.in.mu.Unlock()
	h.inbox.cfgMu.Lock()
	h.inbox.cfgAt = time.Time{}
	h.inbox.cfgMu.Unlock()
}

// The hub's rounds, by the names its tests used.
const (
	roundChats = assistantwire.RoundChats
	roundPoll  = assistantwire.RoundPoll
	roundFull  = assistantwire.RoundFull
)

// collectInbox is one round of the hub's sources, handed to the inbox.
func (h *inboxHub) collectInbox(ctx context.Context, round string) { _ = h.in.collect(ctx, round) }

// routes are the inbox's routes as the hub relays them.
func (h *inboxHub) routes() http.Handler { return h.Service }

// inboxCall is a request on the inbox's routes (as a full device; role: the device's role, "" full); the status and
// the JSON object answered.
func inboxCall(t *testing.T, h http.Handler, method, path, body string, role ...string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if len(role) > 0 {
		req.Header.Set(RoleHeader, role[0])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	out := map[string]any{}
	raw, _ := io.ReadAll(w.Body)
	_ = json.Unmarshal(raw, &out)
	return w.Code, out
}

// rawJSON is v as JSON.
func rawJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// capturedCall is one request a model got.
type capturedCall struct{ System, User string }

// captureModel is a model that records what it is asked and answers reply.
func captureModel(t *testing.T, reply string) (*ai.Client, func() []capturedCall) {
	t.Helper()
	var mu sync.Mutex
	var calls []capturedCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/" { // a node's port probe (node/ports.go), not this test's client
			http.NotFound(w, r)
			return
		}
		var in struct {
			System   string `json:"system"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		c := capturedCall{System: in.System}
		if len(in.Messages) > 0 {
			c.User = in.Messages[0].Content
		}
		mu.Lock()
		calls = append(calls, c)
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"content": []any{map[string]any{"type": "text", "text": reply}}})
	}))
	t.Cleanup(srv.Close)
	return modelAt(t, srv.URL), func() []capturedCall {
		mu.Lock()
		defer mu.Unlock()
		return append([]capturedCall(nil), calls...)
	}
}

// modelAt is a model client on a test server.
func modelAt(t *testing.T, url string) *ai.Client {
	t.Helper()
	key := filepath.Join(t.TempDir(), "key")
	_ = os.WriteFile(key, []byte("k"), 0o600)
	return ai.New(ai.Config{BaseURL: url, APIKeyFile: key, Model: "m"})
}

// fakeModel answers every Messages API call with reply.
func fakeModel(t *testing.T, reply string) *ai.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"content": []any{map[string]any{"type": "text", "text": reply}}})
	}))
	t.Cleanup(srv.Close)
	return modelAt(t, srv.URL)
}

func onlyCall(t *testing.T, calls []capturedCall) capturedCall {
	t.Helper()
	if len(calls) != 1 {
		t.Fatalf("%d model calls, want 1", len(calls))
	}
	return calls[0]
}

// sourceOf is a connector as a round names it (what the inbox may ask of it), without its things.
func sourceOf(c connector.Connector) assistantwire.InboxSource {
	src := assistantwire.InboxSource{ID: c.ID(), Name: c.Name(), MetaDescription: strings.TrimSpace(c.Manifest().MetaDescription)}
	_, src.Group = connector.As[connector.Grouper](c)
	_, src.Meta = connector.As[connector.EventWriter](c)
	_, src.Settles = connector.As[connector.InboxSettler](c)
	return src
}

// pruneAvatars drops what the hub leaves out of a connector tool's answer for the model (its avatars).
func pruneAvatars(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if strings.Contains(strings.ToLower(k), "avatar") {
				delete(t, k)
				continue
			}
			t[k] = pruneAvatars(val)
		}
	case []any:
		for i := range t {
			t[i] = pruneAvatars(t[i])
		}
	}
	return v
}
