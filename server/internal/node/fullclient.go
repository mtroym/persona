package node

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/herdr"
	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The web's full herdr view (docs/integrations/terminal.md "Full herdr view"): on request the node runs the host's own client in a
// PTY (herdr: `herdr session attach`) and relays its bytes to the hub (herdr.client.data); keys, resizes and acks come
// back as events. One client per browser view. Whatever ends a view — the hub says so, its lease runs out, this
// connection to the hub ends, the browser stops reading, the client quits — ends the client process too; nothing is
// left running on the computer.

const (
	// fullMaxClients: full clients at once on this machine (each is a herdr client process and a PTY).
	fullMaxClients = 4
	// fullWindow: bytes given to the hub and not acknowledged by the browser yet; past it the PTY is not read, so the
	// client blocks on a full terminal buffer instead of this process buffering without bound.
	fullWindow = 256 << 10
	// fullInputQueue: keys and pastes waiting to be written to the client; a client that takes none is dropped.
	fullInputQueue = 256
	// fullMaxCols and fullMaxRows bound the grid a browser may ask for.
	fullMaxCols, fullMaxRows = 500, 300
)

var (
	// fullStall: how long a full window may wait for an ack before the browser counts as gone.
	fullStall = 60 * time.Second
	// fullLease: a client the hub said nothing about for this long is closed (the hub sends herdr.client.keepalive every
	// 20 s), in case the hub lost track of it.
	fullLease = 75 * time.Second
)

// fullClient is one running client.
type fullClient struct {
	n   *Node
	id  string
	ctx context.Context
	end context.CancelFunc

	in    chan []byte   // keys for the client, written in order off the event loop
	acked chan struct{} // signalled when the browser acknowledged something
	gone  chan struct{} // closed when the client is stopped and its process is gone

	mu    sync.Mutex
	hc    host.FullClient
	sent  int64
	ack   int64
	lease time.Time
	// activeAt: when the user last did something in the client (keys, a click; not the mouse passing over it), which
	// is how a focus change is attributed to it (focus.go).
	activeAt time.Time
	once     sync.Once

	// leaseFor is fullLease when the client opened: watch outlives the call that opened it, so it does not read the
	// package variable again.
	leaseFor time.Duration

	// session is the herdr session the client is attached to ("" the own server): it shows that server's panes only
	// (#2196, focusSession).
	session string
}

var errNoFullClient = fmt.Errorf("%w: this machine has no host that can run a full client (herdr on Linux or macOS)", host.ErrUnsupported)

// fullHost is the host that can run a full client on this machine.
func (n *Node) fullHost() host.FullClienter {
	for _, h := range n.hosts.All() {
		if f, ok := h.(host.FullClienter); ok && f.CanFullClient() {
			return f
		}
	}
	return nil
}

// fullOpen starts a full client for the hub's herdr.client.open: client_id names it in every later message; with a
// session or pane address and focus, the host shows that pane's tab first.
func (n *Node) fullOpen(ctx context.Context, params json.RawMessage) (any, error) {
	var a struct {
		ClientID  string `json:"client_id"`
		SessionID string `json:"session_id"`
		Cols      int    `json:"cols"`
		Rows      int    `json:"rows"`
		Focus     bool   `json:"focus"`
	}
	if err := json.Unmarshal(params, &a); err != nil || a.ClientID == "" || len(a.ClientID) > 64 {
		return nil, errors.New("bad params")
	}
	if a.Cols < 1 || a.Rows < 1 || a.Cols > fullMaxCols || a.Rows > fullMaxRows {
		return nil, fmt.Errorf("bad size %dx%d", a.Cols, a.Rows)
	}
	fh := n.fullHost()
	if fh == nil {
		return nil, errNoFullClient
	}
	opts := host.FullClientOptions{Cols: a.Cols, Rows: a.Rows}
	if a.Focus && a.SessionID != "" {
		if pane, err := n.terminalPane(ctx, a.SessionID); err == nil {
			if _, ref, err := n.paneHost(pane); err == nil {
				opts.Pane = &ref
			}
		}
	}

	cctx, cancel := context.WithCancel(context.Background())
	f := &fullClient{n: n, id: a.ClientID, ctx: cctx, end: cancel, in: make(chan []byte, fullInputQueue),
		acked: make(chan struct{}, 1), gone: make(chan struct{}), lease: time.Now(), leaseFor: fullLease}
	if opts.Pane != nil && opts.Pane.Kind == host.Herdr {
		f.session, _ = herdr.SplitID(opts.Pane.ID)
	}
	n.mu.Lock()
	switch {
	case n.fulls[f.id] != nil:
		n.mu.Unlock()
		cancel()
		return nil, errors.New("this client id is in use")
	case len(n.fulls) >= fullMaxClients:
		n.mu.Unlock()
		cancel()
		return nil, &rpc.Error{Code: "too_many", Message: fmt.Sprintf("this computer already runs %d full herdr views", fullMaxClients)}
	}
	if n.fulls == nil {
		n.fulls = map[string]*fullClient{}
	}
	n.fulls[f.id] = f
	n.mu.Unlock()

	hc, err := fh.StartFullClient(ctx, opts, f.emit)
	if err != nil {
		f.stop("", false)
		return nil, err
	}
	f.mu.Lock()
	f.hc = hc
	f.mu.Unlock()
	if cctx.Err() != nil { // stopped while it was starting: stop saw no client to close
		hc.Close()
		return nil, errors.New("closed while starting")
	}
	go f.writeLoop()
	go f.watch()
	return map[string]any{"cols": a.Cols, "rows": a.Rows}, nil
}

// emit is the client's output on its way to the hub. It waits while the browser is a window behind, which is what
// stops the client (the PTY is not read meanwhile).
func (f *fullClient) emit(b []byte) {
	for {
		f.mu.Lock()
		room := f.sent-f.ack < fullWindow
		f.mu.Unlock()
		if room {
			break
		}
		select {
		case <-f.acked:
		case <-f.ctx.Done():
			return
		case <-time.After(fullStall):
			go f.stop("the browser stopped reading the terminal", true)
			return
		}
	}
	f.mu.Lock()
	f.sent += int64(len(b))
	f.mu.Unlock()
	if f.n.peer != nil {
		_ = f.n.peer.Notify("herdr.client.data", map[string]any{"client_id": f.id, "data": base64.StdEncoding.EncodeToString(b)})
	}
}

// writeLoop types what the hub forwarded, in order. It is its own goroutine because a client that stopped reading
// would block the write, and the node's event loop must not wait for it.
func (f *fullClient) writeLoop() {
	for {
		select {
		case <-f.ctx.Done():
			return
		case b := <-f.in:
			f.mu.Lock()
			hc := f.hc
			f.mu.Unlock()
			if hc != nil {
				_ = hc.Write(b)
			}
		}
	}
}

// watch ends the client's story: it quit, the hub's lease ran out, or it was stopped from outside.
func (f *fullClient) watch() {
	f.mu.Lock()
	hc := f.hc
	f.mu.Unlock()
	t := time.NewTicker(f.leaseFor / 5)
	defer t.Stop()
	for {
		select {
		case <-f.ctx.Done():
			return
		case <-hc.Done():
			why := "the herdr client ended"
			if err := hc.Err(); err != nil {
				why = err.Error()
			}
			f.stop(why, true)
			return
		case <-t.C:
			f.mu.Lock()
			over := time.Since(f.lease) > f.leaseFor
			f.mu.Unlock()
			if over {
				f.stop("the hub stopped renewing the full herdr view", true)
				return
			}
		}
	}
}

// stop ends the client and its process, once. With why, the hub is told (herdr.client.end); a close the hub asked
// for needs no telling.
func (f *fullClient) stop(why string, tell bool) {
	f.once.Do(func() {
		f.end()
		f.n.mu.Lock()
		if f.n.fulls[f.id] == f {
			delete(f.n.fulls, f.id)
		}
		f.n.mu.Unlock()
		f.mu.Lock()
		hc := f.hc
		f.mu.Unlock()
		if hc != nil {
			hc.Close()
		}
		close(f.gone)
		if tell && f.n.peer != nil {
			_ = f.n.peer.Notify("herdr.client.end", map[string]any{"client_id": f.id, "error": why})
		}
	})
	<-f.gone
}

func (f *fullClient) renew() {
	f.mu.Lock()
	f.lease = time.Now()
	f.mu.Unlock()
}

func (n *Node) fullOf(id string) *fullClient {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.fulls[id]
}

// closeFulls ends every full client of this hub connection (the connection ended): none outlives it.
func (n *Node) closeFulls() {
	n.mu.Lock()
	all := make([]*fullClient, 0, len(n.fulls))
	for _, f := range n.fulls {
		all = append(all, f)
	}
	n.mu.Unlock()
	var wg sync.WaitGroup
	for _, f := range all {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f.stop("", false)
		}()
	}
	wg.Wait()
}

// onFullEvent handles what the hub forwards about full clients; true when the event was one. Events come in order.
func (n *Node) onFullEvent(event string, params json.RawMessage) bool {
	switch event {
	case "herdr.client.input", "herdr.client.resize", "herdr.client.ack", "herdr.client.close", "herdr.client.keepalive":
	case "herdr.client.focus": // a view shows another session or pane (focus.go)
		n.onFocusRequest(params)
		return true
	default:
		return false
	}
	var p struct {
		ClientID  string   `json:"client_id"`
		ClientIDs []string `json:"client_ids"`
		Data      string   `json:"data"`
		B64       string   `json:"b64"`
		Cols      int      `json:"cols"`
		Rows      int      `json:"rows"`
		Bytes     int64    `json:"bytes"`
	}
	if json.Unmarshal(params, &p) != nil {
		return true
	}
	if event == "herdr.client.keepalive" {
		for _, id := range p.ClientIDs {
			if f := n.fullOf(id); f != nil {
				f.renew()
			}
		}
		return true
	}
	f := n.fullOf(p.ClientID)
	if f == nil {
		return true
	}
	f.renew()
	switch event {
	case "herdr.client.input":
		b := []byte(p.Data)
		if p.B64 != "" {
			var err error
			if b, err = base64.StdEncoding.DecodeString(p.B64); err != nil {
				return true
			}
		}
		if len(b) == 0 {
			return true
		}
		f.markActive(b)
		select {
		case f.in <- b:
		default: // it takes nothing: not a client to keep
			go f.stop("the herdr client takes no input", true)
		}
	case "herdr.client.resize":
		f.mu.Lock()
		hc := f.hc
		f.mu.Unlock()
		if hc != nil && p.Cols > 0 && p.Rows > 0 && p.Cols <= fullMaxCols && p.Rows <= fullMaxRows {
			_ = hc.Resize(p.Cols, p.Rows)
		}
	case "herdr.client.ack":
		f.mu.Lock()
		if p.Bytes > f.ack && p.Bytes <= f.sent { // cumulative, and never more than was sent
			f.ack = p.Bytes
		}
		f.mu.Unlock()
		select {
		case f.acked <- struct{}{}:
		default:
		}
	case "herdr.client.close":
		go f.stop("", false)
	}
	return true
}
