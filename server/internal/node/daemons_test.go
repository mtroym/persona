package node

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/runtimepkg"
)

// fakeDaemon is a daemon's WebSocket endpoint speaking the runtime protocol: what it was asked, and how it answers.
type fakeDaemon struct {
	srv      *httptest.Server
	token    string
	subproto string // the subprotocol it answers with ("" for none)
	hello    map[string]any

	mu       sync.Mutex
	conns    int
	peer     *rpc.Peer
	ws       *websocket.Conn
	calls    map[string][]json.RawMessage
	events   []string
	helloRes map[string]any
	helloErr error
	// helloConn is the connection (1 for the first) whose hello helloRes and helloErr are.
	helloConn int
	answer    func(method string, params json.RawMessage) (any, error)
}

func startFakeDaemon(t *testing.T, token string, hello map[string]any) *fakeDaemon {
	t.Helper()
	f := &fakeDaemon{token: token, subproto: DaemonSubprotocol, hello: hello, calls: map[string][]json.RawMessage{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeDaemon) url() string { return "ws" + strings.TrimPrefix(f.srv.URL, "http") + "/persona" }

func (f *fakeDaemon) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	token, sub := f.token, f.subproto
	f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+token {
		http.Error(w, "no", http.StatusUnauthorized)
		return
	}
	opts := &websocket.AcceptOptions{}
	if sub != "" {
		opts.Subprotocols = []string{sub}
	}
	c, err := websocket.Accept(w, r, opts)
	if err != nil {
		return
	}
	c.SetReadLimit(daemonMaxMessage)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := &wsStream{c: c, ctx: ctx}
	p := rpc.New("d", stream)
	p.OnRequest = func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		f.mu.Lock()
		f.calls[method] = append(f.calls[method], params)
		answer := f.answer
		f.mu.Unlock()
		if answer != nil {
			return answer(method, params)
		}
		return map[string]any{"ok": true}, nil
	}
	p.OnEvent = func(event string, params json.RawMessage) {
		f.mu.Lock()
		f.events = append(f.events, event)
		f.mu.Unlock()
	}
	f.mu.Lock()
	f.conns++
	conn := f.conns
	f.peer, f.ws = p, c
	f.mu.Unlock()
	go func() {
		hctx, hcancel := context.WithTimeout(ctx, 5*time.Second)
		defer hcancel()
		var res map[string]any
		err := p.Call(hctx, "runtime.hello", f.hello, &res)
		f.mu.Lock()
		if conn >= f.helloConn { // an older connection's late hello does not stand for this one's
			f.helloRes, f.helloErr, f.helloConn = res, err, conn
		}
		f.mu.Unlock()
	}()
	_ = p.Serve(ctx, stream)
	c.CloseNow()
}

func (f *fakeDaemon) got(method string) []json.RawMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]json.RawMessage(nil), f.calls[method]...)
}

func (f *fakeDaemon) connections() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.conns
}

func (f *fakeDaemon) hellod() (map[string]any, error, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.helloRes, f.helloErr, f.helloRes != nil || f.helloErr != nil
}

func (f *fakeDaemon) current() *rpc.Peer {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.peer
}

// push sends an event as the daemon, once it has said hello.
func (f *fakeDaemon) push(t *testing.T, event string, params any) {
	t.Helper()
	// After the hello of the connection it pushes on: a push the node gets before that connection's hello is not taken,
	// and the hello of the connection before a reconnect does not count.
	waitFor(t, "the daemon's hello", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.helloRes != nil && f.helloConn == f.conns
	})
	if err := f.current().Notify(event, params); err != nil {
		t.Fatal(err)
	}
}

// syncBuffer is a log the test reads while the host writes it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// daemonHost is a resident node's runtime host whose daemons.json holds daemons (written before it starts; nil: none).
func daemonHost(t *testing.T, daemons ...map[string]any) (*RuntimeHost, *syncBuffer) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "jdh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if daemons != nil {
		writeDaemons(t, dir, daemons...)
	}
	rh := NewRuntimeHost("test", filepath.Join(dir, "runtime.sock"))
	rh.dtimes = daemonTimings{reload: 40 * time.Millisecond, backoffMin: 20 * time.Millisecond, backoffMax: time.Second, ping: time.Second, pingWait: time.Second}
	logs := &syncBuffer{}
	rh.logger = log.New(logs, "", 0)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := rh.Listen(ctx); err != nil {
		t.Fatal(err)
	}
	return rh, logs
}

func writeDaemons(t *testing.T, dir string, daemons ...map[string]any) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"daemons": daemons})
	if err := os.WriteFile(filepath.Join(dir, "daemons.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func dshLikeHello() map[string]any {
	return map[string]any{"runtime": "dsh", "name": "DSH", "agent": "", "version": "9", "instance": "spoofed", "launcher": "dsh", "launcher_model": "Model X",
		"caps": map[string]any{"start": true, "send": true, "interrupt": true, "assistant": true}}
}

func daemonConn(rh *RuntimeHost, id string) *runtimeConn {
	rh.reg.mu.Lock()
	defer rh.reg.mu.Unlock()
	return rh.reg.byRuntime(id)
}

func TestDaemonConfigIsChecked(t *testing.T) {
	good := map[string]any{"id": "acme", "url": "ws://127.0.0.1:1/x", "token": "t"}
	with := func(k string, v any) map[string]any {
		m := map[string]any{}
		for a, b := range good {
			m[a] = b
		}
		if v == nil {
			delete(m, k)
		} else {
			m[k] = v
		}
		return m
	}
	cases := []struct {
		entry map[string]any
		ok    bool
	}{
		{good, true},
		{with("id", "Acme"), false},
		{with("id", "a.b"), false}, // the first dot of a session id ends the daemon's id
		{with("id", strings.Repeat("a", 33)), false},
		{with("url", "http://127.0.0.1:1/x"), true},
		{with("url", "ftp://127.0.0.1/x"), false},
		{with("url", "ws://user:pw@127.0.0.1/x"), false},
		{with("token", nil), false},
		{with("token_file", "/tmp/x"), false}, // both
		{with("launcher", "has space"), false},
		{with("agent", "bad agent"), false},
	}
	for _, c := range cases {
		b, _ := json.Marshal(map[string]any{"daemons": []any{c.entry}})
		got, errs := parseDaemons(b)
		if (len(got) == 1) != c.ok || (len(errs) == 0) != c.ok {
			t.Errorf("%v: got %v, errors %v", c.entry, got, errs)
		}
		for _, e := range errs {
			if strings.Contains(e.Error(), `"t"`) {
				t.Errorf("an error shows the token: %v", e)
			}
		}
	}
	b, _ := json.Marshal(map[string]any{"daemons": []any{good, good, with("id", "other"), map[string]any{"id": "off", "url": "ws://h/x", "token": "t", "disabled": true}}})
	got, errs := parseDaemons(b)
	if _, ok := got["acme"]; ok || got["other"].ID != "other" || len(got) != 1 || len(errs) != 1 {
		t.Fatalf("a twice-listed id connects neither, a disabled one is left out: %v %v", got, errs)
	}
	// Whichever of the two is disabled, and in either order, neither connects.
	off := with("disabled", true)
	for _, pair := range [][2]map[string]any{{off, good}, {good, off}, {off, off}} {
		b, _ := json.Marshal(map[string]any{"daemons": []any{pair[0], pair[1]}})
		got, errs := parseDaemons(b)
		if len(got) != 0 || len(errs) != 1 {
			t.Errorf("%v then %v: got %v, errors %v", pair[0]["disabled"], pair[1]["disabled"], got, errs)
		}
	}
}

// A daemon is a runtime under the config's names: its id, name and launcher, its sessions namespaced, only the
// protocol's caps, nothing of a terminal.
func TestDaemonIsARuntimeUnderTheConfigNames(t *testing.T) {
	d := startFakeDaemon(t, "secret-1", dshLikeHello())
	rh, _ := daemonHost(t, map[string]any{"id": "acme", "name": "Acme", "url": d.url(), "token": "secret-1"})
	n, _ := runtimeNodeOn(t, rh)
	d.push(t, "runtime.sessions", map[string]any{"sessions": []map[string]any{
		{"session_id": "s1", "title": "fix", "cwd": "/w", "status": "working", "space_id": "/w", "space_name": "w",
			"pane_id": "w1:p1", "resume_argv": []string{"sh", "-c", "touch /tmp/pwned"}},
		{"session_id": "../bad", "status": "done"},
		{"session_id": "s3", "agent": "dsh", "agent_label": "Claude Code"}, // a platform running DSH, naming itself
		{"session_id": "s4", "agent": "claude"},
	}})
	waitFor(t, "listed", func() bool { _, ok := listed(t, n)["acme.s1"]; return ok })
	if s3, s4 := listed(t, n)["acme.s3"], listed(t, n)["acme.s4"]; s3.Agent != "dsh" || s3.AgentLabel != "Acme" || s4.Agent != "claude" || s4.AgentLabel != "" {
		t.Fatalf("labels: %+v / %+v (the config's name, except for Claude Code)", s3, s4)
	}
	s := listed(t, n)["acme.s1"]
	if s.Agent != "acme" || s.AgentLabel != "Acme" || s.SpaceKind != "acme" || s.SourceInstanceID != "acme" || s.SpaceID != "acme:/w" || s.PaneID != "" {
		t.Fatalf("row %+v", s)
	}
	if len(listed(t, n)) != 3 {
		t.Fatalf("an invalid id was listed: %v", listed(t, n))
	}
	h := daemonConn(rh, "acme")
	if h == nil || len(h.resumeArgv("acme.s1")) != 0 || rh.reg.sessionInPane("w1:p1", func(p string) string { return p }) != "" {
		t.Fatal("a daemon's resume_argv or pane was kept")
	}
	h.mu.Lock()
	hello := h.hello
	h.mu.Unlock()
	if hello.Runtime != "acme" || hello.Name != "Acme" || hello.Launcher != "acme" || hello.LauncherModel != "Model X" || !hello.Caps.Start || hello.Caps.Rename {
		t.Fatalf("hello %+v", hello)
	}
	if hello.Caps.Assistant || h.assistant() || hello.Instance != "daemon:acme" {
		t.Fatalf("a daemon ran the assistant, or chose its own instance: %+v", hello)
	}
	res, _, _ := d.hellod()
	if f, _ := json.Marshal(res["features"]); string(f) != `["prefer_running","start_params"]` {
		t.Fatalf("features told to the daemon: %s", f)
	}

	// The hub's calls reach it with its own ids.
	if _, err := nodeCall(t, n, "session.send", map[string]any{"session_id": "acme.s1", "text": "hi", "client_msg_id": "m-1"}); err != nil {
		t.Fatal(err)
	}
	var sent map[string]any
	_ = json.Unmarshal(d.got("session.send")[0], &sent)
	if sent["session_id"] != "s1" || sent["text"] != "hi" || sent["client_msg_id"] != "m-1" {
		t.Fatalf("send %v", sent)
	}
	d.answer = func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "session.start":
			return map[string]any{"session": map[string]any{"session_id": "s2", "cwd": "/w", "status": "idle", "pane_id": "w1:p2"}}, nil
		case "session.items":
			return map[string]any{"items": []map[string]any{{"id": "1#0", "seq": 1, "role": "user", "kind": "text", "text": "hi"}}, "to_seq": 1, "history": "h"}, nil
		}
		return map[string]any{"ok": true}, nil
	}
	raw, err := nodeCall(t, n, "session.start", map[string]any{"cwd": "/w", "launcher": "acme", "async": true})
	if err != nil {
		t.Fatal(err)
	}
	var started struct {
		Session proto.Session `json:"session"`
	}
	_ = json.Unmarshal(raw, &started)
	if started.Session.SessionID != "acme.s2" || started.Session.PaneID != "" {
		t.Fatalf("started %s", raw)
	}
	raw, err = nodeCall(t, n, "session.items", map[string]any{"session_id": "acme.s1", "turns": 1})
	if err != nil || !strings.Contains(string(raw), `"text":"hi"`) {
		t.Fatalf("items %s %v", raw, err)
	}
	// The message sent above is its first line (it had none before): the page ties it (echo.go).
	if !strings.Contains(string(raw), `"client_msg_id":"m-1"`) {
		t.Fatalf("the sent message is not tied: %s", raw)
	}
}

func TestDaemonPushesAndAsksCarryTheHubsIDs(t *testing.T) {
	d := startFakeDaemon(t, "tok", map[string]any{"runtime": "x", "caps": map[string]any{"send": true}})
	rh, _ := daemonHost(t, map[string]any{"id": "acme", "url": d.url(), "token": "tok"})
	n, hub := runtimeNodeOn(t, rh)
	d.push(t, "runtime.sessions", map[string]any{"sessions": []map[string]any{{"session_id": "s1", "title": "t", "cwd": "/w"}}})
	waitFor(t, "listed", func() bool { _, ok := listed(t, n)["acme.s1"]; return ok })
	if _, err := nodeCall(t, n, "session.watch", map[string]any{"session_id": "acme.s1", "since_seq": 0}); err != nil {
		t.Fatal(err)
	}
	var watch map[string]any
	_ = json.Unmarshal(d.got("session.watch")[0], &watch)
	if watch["session_id"] != "s1" {
		t.Fatalf("watch %v", watch)
	}
	d.push(t, "session.items", map[string]any{"session_id": "s1", "items": []map[string]any{{"id": "1#0", "seq": 1, "role": "assistant", "kind": "text", "text": "ok"}}, "from_seq": 0, "to_seq": 1})
	d.push(t, "session.delta", map[string]any{"session_id": "s1", "text": "o"})
	d.push(t, "session.items", map[string]any{"session_id": "elsewhere", "items": []any{}, "to_seq": 1}) // not listed: dropped
	waitFor(t, "items and delta at the hub", func() bool {
		hub.mu.Lock()
		defer hub.mu.Unlock()
		return len(hub.items) == 1 && len(hub.deltas) == 1
	})
	hub.mu.Lock()
	items, delta := string(hub.items[0]), string(hub.deltas[0])
	hub.mu.Unlock()
	if !strings.Contains(items, `"session_id":"acme.s1"`) || !strings.Contains(delta, `"session_id":"acme.s1"`) {
		t.Fatalf("items %s delta %s", items, delta)
	}

	allow := true
	hub.mu.Lock()
	hub.resolve = func(proto.Interaction) proto.Resolution { return proto.Resolution{Allow: &allow} }
	hub.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var res proto.Resolution
	if err := d.current().Call(ctx, "interaction.create", map[string]any{"interaction": map[string]any{"id": "ask-1", "kind": "permission",
		"session_id": "s1", "tool_name": "Bash", "tool_input": map[string]any{"command": "ls"}}}, &res); err != nil {
		t.Fatal(err)
	}
	if res.Allow == nil || !*res.Allow {
		t.Fatalf("resolution %+v", res)
	}
	hub.mu.Lock()
	ix := hub.asked[0]
	hub.mu.Unlock()
	link := &daemonLink{cfg: daemonConfig{ID: "acme"}}
	if ix.SessionID != "acme.s1" || ix.ID != link.interactionID("ask-1") || ix.Agent != "acme" || ix.AgentLabel != "acme" || ix.SessionTitle != "t" {
		t.Fatalf("asked %+v", ix)
	}
	d.push(t, "interaction.cancel", map[string]any{"id": "ask-1"})
	waitFor(t, "the card taken off", func() bool {
		hub.mu.Lock()
		defer hub.mu.Unlock()
		return len(hub.cancels) == 1 && hub.cancels[0] == ix.ID
	})
	if err := d.current().Call(ctx, "interaction.create", map[string]any{"interaction": map[string]any{"kind": "permission", "session_id": "other.s9"}}, &res); err == nil {
		t.Fatal("an ask for a session it does not list went through")
	}
}

// A daemon never sees, and cannot claim, a session that is not its own; it gets no Persona tools.
func TestDaemonStaysInItsCorner(t *testing.T) {
	d := startFakeDaemon(t, "tok", map[string]any{"runtime": "x", "caps": map[string]any{"send": true}})
	rh, _ := daemonHost(t, map[string]any{"id": "acme", "url": d.url(), "token": "tok"})
	n, _ := runtimeNodeOn(t, rh)
	waitFor(t, "connected", func() bool { return daemonConn(rh, "acme") != nil })
	d.answer = func(string, json.RawMessage) (any, error) { return map[string]any{"items": []any{}}, nil } // claims anything
	_, _ = nodeCall(t, n, "session.items", map[string]any{"session_id": "0a1b2c3d-unknown", "limit": 1})
	if len(d.got("session.items")) != 0 {
		t.Fatal("the daemon was asked about a session id that is not its own")
	}
	if h, _, _ := n.runtimes.belongs("0a1b2c3d-unknown"); h != nil {
		t.Fatal("a daemon claimed a session that is not its own")
	}
	// Its own old session (not listed now) is still asked about, under its own id.
	if _, err := nodeCall(t, n, "session.items", map[string]any{"session_id": "acme.old", "limit": 1}); err != nil {
		t.Fatal(err)
	}
	var asked map[string]any
	_ = json.Unmarshal(d.got("session.items")[0], &asked)
	if asked["session_id"] != "old" {
		t.Fatalf("asked %v", asked)
	}
	// A send it hands over (Node.handOver) is refused, never offered to another runtime that might claim the id.
	other := dialRuntimeAt(t, rh.path, map[string]any{"runtime": "other", "caps": map[string]any{"send": true}})
	other.answer = func(string, json.RawMessage) (any, error) { return map[string]any{"items": []any{}}, nil }
	d.push(t, "runtime.sessions", map[string]any{"sessions": []map[string]any{{"session_id": "s1", "status": "offline"}}})
	waitFor(t, "listed", func() bool { _, ok := listed(t, n)["acme.s1"]; return ok })
	d.answer = func(method string, _ json.RawMessage) (any, error) {
		if method == "session.send" {
			return nil, &rpc.Error{Message: "starting it elsewhere", Code: runtimeHandover}
		}
		return map[string]any{"items": []any{}}, nil
	}
	if _, err := nodeCall(t, n, "session.send", map[string]any{"session_id": "acme.s1", "text": "x", "client_msg_id": "m-h"}); rpcCode(err) != "not_running" {
		t.Fatalf("a handed-over send: %v", err)
	}
	if len(other.got("session.items")) != 0 || len(other.got("session.send")) != 0 {
		t.Fatal("a daemon's send was handed to another runtime")
	}
	// It cannot point a session at a transcript of this machine's (runtimetranscript.go): its content stays its own.
	sdkTranscript(t, n, sdkSession, "local secrets")
	d.push(t, "runtime.sessions", map[string]any{"sessions": []map[string]any{{"session_id": "s2", "transcript": sdkSession, "status": "idle"}}})
	waitFor(t, "listed", func() bool { _, ok := listed(t, n)["acme.s2"]; return ok })
	before := len(d.got("session.items"))
	if raw, _ := nodeCall(t, n, "session.items", map[string]any{"session_id": "acme.s2", "limit": 5}); strings.Contains(string(raw), "local secrets") {
		t.Fatal("a daemon's session was read from this machine's transcript")
	}
	if len(d.got("session.items")) != before+1 {
		t.Fatal("the daemon was not asked for its own session's items")
	}
	freshOffline()
	if s, ok := listed(t, n)[sdkSession]; !ok || s.SourceKind != "" {
		t.Fatal("the transcript's own row was hidden by a daemon's")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var tools struct {
		Tools []any `json:"tools"`
	}
	if err := d.current().Call(ctx, "mcp.tools", map[string]any{}, &tools); err != nil || len(tools.Tools) != 0 {
		t.Fatalf("tools %v %v", tools, err)
	}
	err := d.current().Call(ctx, "mcp.call", map[string]any{"name": "phone_contacts"}, nil)
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != "unsupported" {
		t.Fatalf("mcp.call: %v", err)
	}
}

// The config's ids are reserved: a runtime on the socket cannot take one; a daemon whose id a connected runtime has
// waits until it leaves.
func TestDaemonIDsAreReserved(t *testing.T) {
	d := startFakeDaemon(t, "tok", map[string]any{"runtime": "x"})
	rh, logs := daemonHost(t, map[string]any{"id": "other", "url": d.url(), "token": "tok"})
	waitFor(t, "the daemon connected", func() bool { return daemonConn(rh, "other") != nil })

	c := dialRaw(t, rh.path)
	err := c.peer.Call(context.Background(), "runtime.hello", map[string]any{"runtime": "other"}, nil)
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != "runtime_conflict" {
		t.Fatalf("a socket runtime took a daemon's id: %v", err)
	}
	if c2 := dialRuntimeAt(t, rh.path, map[string]any{"runtime": "free"}); c2 == nil {
		t.Fatal("an unreserved id refused")
	}

	// The other way round: the socket runtime came first.
	d2 := startFakeDaemon(t, "tok", map[string]any{"runtime": "x"})
	taken := dialRuntimeAt(t, rh.path, map[string]any{"runtime": "late"})
	writeDaemons(t, filepath.Dir(rh.path), map[string]any{"id": "other", "url": d.url(), "token": "tok"}, map[string]any{"id": "late", "url": d2.url(), "token": "tok"})
	waitFor(t, "the daemon refused", func() bool { _, err, done := d2.hellod(); return done && err != nil })
	if _, err, _ := d2.hellod(); !strings.Contains(err.Error(), "in use") {
		t.Fatalf("refusal %v", err)
	}
	if !strings.Contains(logs.String(), "refused") {
		t.Fatalf("the refusal is not logged: %s", logs.String())
	}
	_ = taken.conn.Close() // it leaves: the daemon gets its id on a later dial
	waitFor(t, "the daemon in", func() bool {
		h := daemonConn(rh, "late")
		return h != nil && h.isDaemon()
	})
	if d2.connections() < 2 {
		t.Fatalf("connections %d", d2.connections())
	}
}

// dialRaw is a connection to the runtime socket that has not said hello.
func dialRaw(t *testing.T, path string) *fakeRuntime {
	t.Helper()
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	g := &fakeRuntime{conn: c, params: map[string][]json.RawMessage{}}
	g.peer = rpc.New("d", c)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = g.peer.Serve(ctx, c) }()
	t.Cleanup(func() { cancel(); _ = c.Close() })
	return g
}

func TestDaemonTokenAndSubprotocolAreRequired(t *testing.T) {
	d := startFakeDaemon(t, "right", map[string]any{"runtime": "x"})
	rh, logs := daemonHost(t, map[string]any{"id": "acme", "url": d.url(), "token": "wrong"})
	waitFor(t, "the refusal logged", func() bool { return strings.Contains(logs.String(), "refused the token (HTTP 401)") })
	if strings.Contains(logs.String(), "wrong") {
		t.Fatal("the token reached the log")
	}
	if daemonConn(rh, "acme") != nil {
		t.Fatal("connected with a wrong token")
	}
	if n := strings.Count(logs.String(), "refused the token"); n != 1 {
		t.Fatalf("the same failure logged %d times", n)
	}

	d.mu.Lock()
	d.subproto = "" // a WebSocket endpoint that does not speak the protocol
	d.mu.Unlock()
	writeDaemons(t, filepath.Dir(rh.path), map[string]any{"id": "acme", "url": d.url(), "token": "right"})
	waitFor(t, "the subprotocol refusal", func() bool { return strings.Contains(logs.String(), "did not answer with subprotocol") })
	if daemonConn(rh, "acme") != nil {
		t.Fatal("connected without the subprotocol")
	}

	tokenFile := filepath.Join(filepath.Dir(rh.path), "token")
	_ = os.WriteFile(tokenFile, []byte("right\n"), 0o600)
	d.mu.Lock()
	d.subproto = DaemonSubprotocol
	d.mu.Unlock()
	writeDaemons(t, filepath.Dir(rh.path), map[string]any{"id": "acme", "url": d.url(), "token_file": tokenFile})
	waitFor(t, "connected with the token file", func() bool { return daemonConn(rh, "acme") != nil })
}

// The config is read again while the node runs: a new daemon connects, a changed one reconnects under its new
// name, a removed one leaves with its sessions and its id is free again.
func TestDaemonConfigReloads(t *testing.T) {
	d := startFakeDaemon(t, "tok", map[string]any{"runtime": "x"})
	rh, _ := daemonHost(t)
	n, _ := runtimeNodeOn(t, rh)
	dir := filepath.Dir(rh.path)
	writeDaemons(t, dir, map[string]any{"id": "acme", "name": "One", "url": d.url(), "token": "tok"})
	d.push(t, "runtime.sessions", map[string]any{"sessions": []map[string]any{{"session_id": "s1"}}})
	waitFor(t, "listed", func() bool { return listed(t, n)["acme.s1"].AgentLabel == "One" })

	writeDaemons(t, dir, map[string]any{"id": "acme", "name": "Two", "url": d.url(), "token": "tok"})
	waitFor(t, "reconnected", func() bool { return d.connections() == 2 })
	waitFor(t, "renamed", func() bool { h := daemonConn(rh, "acme"); return h != nil && h.label() == "Two" })

	_ = os.Remove(filepath.Join(dir, "daemons.json"))
	waitFor(t, "gone", func() bool { return daemonConn(rh, "acme") == nil && len(listed(t, n)) == 0 })
	if rh.reservedID("acme") {
		t.Fatal("a removed daemon's id is still reserved")
	}
}

// A dropped connection is dialed again; a resident node keeps its daemons while the hub reconnects.
func TestDaemonReconnectsAndOutlivesHubConnections(t *testing.T) {
	d := startFakeDaemon(t, "tok", map[string]any{"runtime": "x", "caps": map[string]any{"send": true}})
	rh, _ := daemonHost(t, map[string]any{"id": "acme", "url": d.url(), "token": "tok"})
	a, _ := runtimeNodeOn(t, rh)
	d.push(t, "runtime.sessions", map[string]any{"sessions": []map[string]any{{"session_id": "s1"}}})
	waitFor(t, "listed", func() bool { _, ok := listed(t, a)["acme.s1"]; return ok })

	rh.detach(a)
	b, _ := runtimeNodeOn(t, rh)
	if _, ok := listed(t, b)["acme.s1"]; !ok || d.connections() != 1 {
		t.Fatalf("the next hub connection lost the daemon (connections %d)", d.connections())
	}
	waitFor(t, "told about the hub", func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return strings.Count(strings.Join(d.events, ","), "runtime.hub") >= 2
	})

	d.mu.Lock()
	ws := d.ws
	d.mu.Unlock()
	ws.CloseNow() // the daemon restarts
	waitFor(t, "dialed again", func() bool { return d.connections() == 2 })
	d.push(t, "runtime.sessions", map[string]any{"sessions": []map[string]any{{"session_id": "s1"}}})
	waitFor(t, "listed again", func() bool { _, ok := listed(t, b)["acme.s1"]; return ok })
	if _, err := nodeCall(t, b, "session.send", map[string]any{"session_id": "acme.s1", "text": "back", "client_msg_id": "m-2"}); err != nil {
		t.Fatal(err)
	}
}

// wsStream turns messages into lines and lines into messages, whatever the sizes.
func TestDaemonStreamFramesLines(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		ctx := context.Background()
		_ = c.Write(ctx, websocket.MessageText, []byte(`{"a":1}`))
		_ = c.Write(ctx, websocket.MessageText, []byte(`{"b":"`+strings.Repeat("x", 100000)+`"}`))
		_, got, err := c.Read(ctx)
		if err == nil {
			_ = c.Write(ctx, websocket.MessageText, got) // echo what the stream wrote
		}
		_, _, _ = c.Read(ctx)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.SetReadLimit(daemonMaxMessage)
	s := &wsStream{c: c, ctx: ctx}
	if _, err := s.Write([]byte("{\"c\":3}\n")); err != nil {
		t.Fatal(err)
	}
	var lines []string
	buf := make([]byte, 7) // smaller than any message
	var cur []byte
	for len(lines) < 3 {
		n, err := s.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range buf[:n] {
			if b == '\n' {
				lines = append(lines, string(cur))
				cur = nil
			} else {
				cur = append(cur, b)
			}
		}
	}
	if lines[0] != `{"a":1}` || len(lines[1]) != 100008 || lines[2] != `{"c":3}` {
		t.Fatalf("lines %q / %d / %q", lines[0], len(lines[1]), lines[2])
	}
	_ = s.Close()
	_, err = s.Read(buf)
	if err == nil || errors.Is(err, io.ErrShortBuffer) {
		t.Fatalf("read after close: %v", err)
	}
}

// A daemon's subagents carry the hub's ids too: "<daemon>.<session>@<child>" outside, "<session>@<child>" inside.
func TestDaemonSubagentIDs(t *testing.T) {
	d := startFakeDaemon(t, "tok", map[string]any{"runtime": "x", "caps": map[string]any{"send": true}})
	rh, _ := daemonHost(t, map[string]any{"id": "acme", "url": d.url(), "token": "tok"})
	n, hub := runtimeNodeOn(t, rh)
	d.push(t, "runtime.sessions", map[string]any{"sessions": []map[string]any{{"session_id": "s1", "subagents": []map[string]any{{"id": "c1", "running": true}}}}})
	waitFor(t, "listed", func() bool { _, ok := listed(t, n)["acme.s1"]; return ok })
	if subs := listed(t, n)["acme.s1"].Subagents; len(subs) != 1 || subs[0].SessionID != "acme.s1@c1" {
		t.Fatalf("subagents %+v", subs)
	}
	if _, err := nodeCall(t, n, "session.watch", map[string]any{"session_id": "acme.s1@c1", "since_seq": 0}); err != nil {
		t.Fatal(err)
	}
	var watch map[string]any
	_ = json.Unmarshal(d.got("session.watch")[0], &watch)
	if watch["session_id"] != "s1@c1" {
		t.Fatalf("watch %v", watch)
	}
	d.push(t, "session.items", map[string]any{"session_id": "s1@c1", "items": []any{}, "from_seq": 0, "to_seq": 1})
	waitFor(t, "the subagent's items at the hub", func() bool {
		hub.mu.Lock()
		defer hub.mu.Unlock()
		return len(hub.items) == 1 && strings.Contains(string(hub.items[0]), `"session_id":"acme.s1@c1"`)
	})
	link := &daemonLink{cfg: daemonConfig{ID: "acme"}}
	for _, id := range []string{"other.s1@c1", "acme.s1@", "acme.@c1"} {
		if in, ok := link.inner(id); ok {
			t.Fatalf("%q is the daemon's %q", id, in)
		}
	}
}

// A daemon's code is not this machine's to replace: its hello gets no package offer, and runtime.package is refused.
func TestDaemonGetsNoPackage(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(runtimepkg.OverrideEnv, dir)
	_ = os.MkdirAll(filepath.Join(dir, "persona-dsh-runtime", "dsh"), 0o755)
	if err := os.WriteFile(filepath.Join(dir, "persona-dsh-runtime", "dsh", "a.js"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := startFakeDaemon(t, "tok", map[string]any{"runtime": "x", "caps": map[string]any{"send": true},
		"package": map[string]any{"name": "persona-dsh-runtime", "hash": "an-older-one"}})
	rh, _ := daemonHost(t, map[string]any{"id": "acme", "url": d.url(), "token": "tok"})
	runtimeNodeOn(t, rh)
	waitFor(t, "the daemon's hello", func() bool { res, _, _ := d.hellod(); return res != nil })
	if res, _, _ := d.hellod(); res["update"] != nil {
		t.Fatalf("a daemon was offered a package: %v", res)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out map[string]any
	if err := d.current().Call(ctx, "runtime.package", map[string]any{"name": "persona-dsh-runtime"}, &out); err == nil {
		t.Fatalf("a daemon fetched a package: %v", out)
	}
}

// A message is one JSON object whatever its whitespace (docs/integrations/daemons.md "Frames"): a daemon that pretty-prints its JSON
// is still heard, and a message that ends without a newline is still its own line.
func TestDaemonMessagesMaySpanLines(t *testing.T) {
	got := make(chan json.RawMessage, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		ctx := r.Context()
		_ = c.Write(ctx, websocket.MessageText, []byte("{\n  \"id\": \"d1\",\n  \"method\": \"ping\",\r\n  \"params\": {\"a\": [1,\n 2]}\n}"))
		_ = c.Write(ctx, websocket.MessageText, []byte(`{"id":"d2","method":"ping","params":{"b":true}}`))
		for range 2 {
			_, b, err := c.Read(ctx)
			if err != nil {
				return
			}
			got <- b
		}
		<-ctx.Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	stream := &wsStream{c: ws, ctx: ctx}
	p := rpc.New("n", stream)
	p.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
		return map[string]any{"method": method, "params": params}, nil
	}
	go func() { _ = p.Serve(ctx, stream) }()
	want := map[string]string{"d1": `{"a":[1,2]}`, "d2": `{"b":true}`}
	for range 2 {
		select {
		case b := <-got:
			var m struct {
				ID     string `json:"id"`
				Result struct {
					Params json.RawMessage `json:"params"`
				} `json:"result"`
			}
			if err := json.Unmarshal(b, &m); err != nil {
				t.Fatalf("%s: %v", b, err)
			}
			var c bytes.Buffer
			_ = json.Compact(&c, m.Result.Params)
			if w, ok := want[m.ID]; !ok || c.String() != w {
				t.Fatalf("answer %s", b)
			}
			delete(want, m.ID)
		case <-time.After(5 * time.Second):
			t.Fatalf("no answer to %v: a message with newlines in it was dropped", want)
		}
	}
}
