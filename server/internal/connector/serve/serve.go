// Package serve runs a connector in a program of its own, for the hub (connector/proc): the hub starts the program
// and talks to it over its stdin / stdout with the hub↔node JSON-lines RPC (internal/rpc). One connector per program.
// A connector whose dependencies would make personad much bigger lives this way (the Telegram extension, #1704).
//
// The hub calls the methods below; the program calls back for its secret (secrets.*: the hub binds them to this
// program's connector) and tells the hub what changed (the changed event). Nothing here logs what crosses the pipe.
// A write (actions.do) comes only once the hub's gate let it through; the program never starts one itself.
//
// A program answers two openings: init and the bare method names (connector protocol 2 on its own), and the plugin
// protocol's initialize with the same methods in the connector namespace (connector.status, …; docs/extensions/extensions.md).
// The extension manager runs connector programs as plugins (connector/proc with a Link). connector/proc still opens a
// program it starts itself (Config.Path) with init, as its tests and the conformance kit do, so both openings stay.
package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/kbwire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Methods the hub calls.
const (
	MInit        = "init" // InitParams → Manifest: build the connector and start it
	MManifest    = "manifest"
	MStatus      = "status"
	MLoginStep   = "login.step"
	MLogout      = "logout"
	MChanges     = "archive.changes"
	MCollect     = "inbox.collect"
	MReply       = "inbox.reply"
	MCatalog     = "catalog.list"
	MCatalogCall = "catalog.call"
	MTools       = "tools.list"
	MCall        = "tools.call"
	MActions     = "actions.list"
	MDescribe    = "actions.describe"
	MDo          = "actions.do" // only once the hub's gate let it through
	MRecall      = "recall"     // take a sent message back: only once the user agreed, and only for a connector that declares recall
	MThread      = "thread.page"
	MMedia       = "thread.media"
	// The knowledge base's items (connector.Items) and an archived message's attachment (connector.ArchiveMedia).
	MItems        = "items.list"
	MDocTitle     = "items.doc_title"
	MArchiveMedia = "archive.media"
	// The live state for the assistant's snapshot (connector.Snapshot): SnapshotParams → connector.SnapshotData.
	MSnapshot = "snapshot.get"
	// History import (connector.History, connector.HistoryAnchors; docs/protocol/connector-protocol.md "History"),
	// read-only: the conversations to import, one page of one of them, and the owner's messages to read around.
	MHistoryChats   = "history.chats"   // {} → HistoryChatsResult
	MHistory        = "archive.history" // HistoryParams → connectorwire.HistoryPage
	MHistoryAnchors = "history.anchors" // HistoryAnchorsParams → connectorwire.HistoryAnchors
	// Relating new messages to events (connector.Grouper): connector.GroupToolCall → any JSON,
	// connector.GroupFallback → connector.GroupFallbackResult; with meta (connector.EventWriter)
	// connector.GroupAppend → connector.InboxThing.
	MGroupTool     = "group.tool"
	MGroupAppend   = "group.append"
	MGroupFallback = "group.fallback"
	// The user's own settings of the connector (connector.Options): {} → OptionsResult, OptionsParams → {}.
	MOptionsGet = "options.get"
	MOptionsSet = "options.set"
	// Closing inbox items the hub still has open (connector.InboxSettler): SettleParams → SettleResult.
	MSettle = "inbox.settle"
	// One of the user's computers connected (connector.Machines): MachineParams → {}.
	MMachineOnline = "machines.online"
	// Where the connector's sign-in comes from, the hub keeping the user's choice (GitHub's "Authorized from",
	// docs/protocol/connector-protocol.md): {} → AuthView, AuthSource → AuthView.
	MAuthGet = "auth.get"
	MAuthSet = "auth.set"
)

// Methods the program calls on the hub, and its event.
const (
	MSecretsLoad   = "secrets.load"
	MSecretsSave   = "secrets.save"
	MSecretsDelete = "secrets.delete"
	// The knowledge base (connector.KnowledgeBase): PostsPutParams → PostsPutResult, PostsPendingParams →
	// PostsPendingResult, ArchiveChatsParams → ArchiveChatsResult.
	MPostsPut      = "posts.put"
	MPostsComments = "posts.comments"
	MPostsPending  = "posts.pending"
	MArchiveChats  = "archive.chats"
	EChanged       = "changed" // ChangedEvent
	// A computer's gh login (GitHub's token there), for a program the hub granted gh_login: GhTokenParams →
	// GhTokenResult. Errors carry the Code* below.
	MGhToken = "credentials.gh_token"
)

// The knowledge base's calls on the wire.
type (
	PostsPutParams struct {
		Posts []connector.PostPut `json:"posts"`
	}
	PostsPutResult struct {
		Statuses []string `json:"statuses"` // per post: connector.PostNew, PostChanged, PostSame, PostRefused
	}
	PostsCommentsParams struct {
		ID           string                  `json:"id"`
		CommentCount int                     `json:"comment_count"`
		Comments     []connector.PostComment `json:"comments"`
	}
	SnapshotParams struct {
		TZ string `json:"tz,omitempty"` // the user's time zone ("" the program's own)
	}
	PostsPendingParams struct {
		Limit int `json:"limit,omitempty"`
	}
	PostsPendingResult struct {
		Posts []connector.PendingPost `json:"posts"`
	}
	ArchiveChatsParams struct {
		Source string `json:"source,omitempty"` // "" is the connector's id
	}
	ArchiveChatsResult struct {
		Chats []connector.ArchivedChat `json:"chats"`
	}
)

// Namespace is the connector extension point's (docs/extensions/extensions.md): under the plugin protocol every method above is
// Namespace + "." + method, with the connector's id as the contribution.
const Namespace = "connector"

// InPlugin is a method's name under the plugin protocol.
func InPlugin(method string) string { return Namespace + "." + method }

// Protocol is the version of this protocol (docs/protocol/connector-protocol.md): the hub sends it with init, the program
// answers with its own in the manifest, and the hub refuses a program of another version.
const Protocol = 2

// Manifest is who the connector is and its connector.Manifest, on the wire.
type Manifest struct {
	Protocol     int      `json:"protocol"`
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Capabilities []string `json:"capabilities"`
	Hosts        []string `json:"hosts,omitempty"`
	AgentTools   []string `json:"agent_tools,omitempty"`
	Unlisted     []string `json:"unlisted,omitempty"`
	// ConfirmAlways: see connector.Manifest.
	ConfirmAlways []string `json:"confirm_always,omitempty"`
	PollSeconds   int      `json:"poll_seconds,omitempty"`
	Kinds         []string `json:"kinds,omitempty"`
	// ArchiveSources: connector.Manifest.ArchiveSources.
	ArchiveSources []string `json:"archive_sources,omitempty"`
	// MetaDescription: connector.Manifest.MetaDescription.
	MetaDescription string `json:"meta_description,omitempty"`
	// CLI: connector.Manifest.CLI.
	CLI string `json:"cli,omitempty"`
}

type (
	InitParams struct {
		Protocol int             `json:"protocol"`
		Config   json.RawMessage `json:"config,omitempty"`
	}
	LoginParams struct {
		Step   string            `json:"step,omitempty"`
		Values map[string]string `json:"values,omitempty"`
	}
	ChangesParams struct {
		Cursor string `json:"cursor,omitempty"`
		Limit  int    `json:"limit,omitempty"`
	}
	ChangesResult struct {
		Batches []connector.ChatBatch `json:"batches,omitempty"`
		Next    string                `json:"next,omitempty"`
		More    bool                  `json:"more,omitempty"`
	}
	// HistoryChatsResult is history.chats' answer.
	HistoryChatsResult struct {
		Chats []connectorwire.HistoryChat `json:"chats"`
	}
	CollectResult struct {
		Things []connector.InboxThing `json:"things,omitempty"`
	}
	// ReplyParams is the reply as the connector gets it.
	ReplyParams = connector.InboxReply
	ToolsResult struct {
		Tools []proto.Tool `json:"tools,omitempty"`
	}
	CallParams struct {
		Name string          `json:"name"`
		Args json.RawMessage `json:"args,omitempty"`
	}
	CallResult struct {
		Text string `json:"text,omitempty"`
		// MessageRef is actions.do's: the id of the message the write made on the other side
		// (connector.DoResult.MessageRef), when the connector knows one.
		MessageRef string `json:"message_ref,omitempty"`
	}
	// DescribeResult is what the card says about a write (connector.Actions.Describe).
	DescribeResult struct {
		Action  connector.Action `json:"action"`
		Summary string           `json:"summary"`
	}
	ThreadParams struct {
		Thread string `json:"thread"`
		Before string `json:"before,omitempty"`
		Limit  int    `json:"limit,omitempty"`
	}
	MediaParams struct {
		Ref string `json:"ref"`
	}
	// ItemsResult is the knowledge base's items (connector.Items: documents, meetings, events).
	ItemsResult struct {
		Items []kbwire.Item `json:"items"`
	}
	DocTitleParams struct {
		URL string `json:"url"`
	}
	DocTitleResult struct {
		Title string `json:"title"`
	}
	// DoParams is a write the gate let through: the very args the card was made from, and the hub's id for this one
	// run, the same if the hub ever sends it again (the program runs it at most once).
	DoParams struct {
		Name      string          `json:"name"`
		Args      json.RawMessage `json:"args,omitempty"`
		RequestID string          `json:"request_id"`
	}
	// RecallParams is a message to take back (connector.Recaller): what the ledger row of the send says, none of what the
	// message said. At is when the row was written, right after the send.
	RecallParams struct {
		Action     string         `json:"action"`
		MessageRef string         `json:"message_ref"`
		Target     map[string]any `json:"target,omitempty"`
		At         time.Time      `json:"at,omitzero"`
	}
	// RecallResult: Recalled true is the program's word that the service confirmed the message is no longer there.
	RecallResult struct {
		Recalled bool `json:"recalled"`
	}
	SecretData struct {
		Data []byte `json:"data,omitempty"` // nil: nothing stored
	}
	ChangedEvent struct {
		Contribution string `json:"contribution,omitempty"` // under the plugin protocol: the connector's id
		Scope        string `json:"scope"`
	}
)

// The history queries go on the wire as they are (with the contribution under the plugin protocol).
type (
	HistoryParams        = connectorwire.HistoryQuery        // archive.history's
	HistoryAnchorsParams = connectorwire.HistoryAnchorsQuery // history.anchors'
)

// Error codes a program answers with (rpc.Error.Code), for callers that act on the kind of failure.
const (
	// Why credentials.gh_token has no token (the GitHub connector's errors of the same names).
	CodeNoMachine      = "no_machine"
	CodeOffline        = "offline"
	CodeNodeTooOld     = "node_too_old"
	CodeNoGh           = "no_gh"
	CodeNotSignedIn    = "not_signed_in"
	CodeNotPermitted   = "permission_denied"
	CodeNotInitialized = "not_initialized"
	CodeNoCapability   = "no_capability"
	CodeUnknownMethod  = "unknown_method"
	CodeWrongProtocol  = "wrong_protocol"
	// CodeRateLimited: tools.call / actions.do waits out a rate limit; data {retry_after} is how many seconds.
	CodeRateLimited = "rate_limited"
	// What recall answers besides those and CodeNotSignedIn / CodeOffline (it could not act now): nothing was sent
	// because the program cannot take that message back, or the request may have taken effect.
	CodeNotRecallable = "not_recallable"
	CodeRecallUnknown = "recall_unknown"
)

// Build makes the connector once the hub has sent its configuration (hub.json's section for it); secrets is the
// hub's store for this connector.
type Build func(config json.RawMessage, secrets connector.Secrets) (connector.Connector, error)

// Init is what a connector program is started with.
type Init struct {
	Config json.RawMessage
	// Declared are, under the plugin protocol, the secrets the plugin's manifest declares and the user filled in (an
	// API key), by name; the connector's own state still goes to the hub's store (secrets).
	Declared map[string]string
	// DataDir is the plugin's own directory for caches (plugin protocol only).
	DataDir string
	// Channel is the hub for a connector that is also a channel (docs/extensions/channels.md): it hands the owner's messages
	// over. Plugin protocol only; nil under the bare opening.
	Channel channel.Hub
	// Locale is the user's language as the hub said it at this start (plugin protocol only; "" under the bare
	// opening): what the connector writes for people reads in it.
	Locale string
}

// Listener is a connector that also hears the hub's events: a plugin that contributes the events extension point
// (docs/extensions/extensions.md "Events") next to its connector. Hear gets the event's name without its namespace
// ("agent.needs_you") and its parameters, on a goroutine of its own; nothing is answered.
type Listener interface {
	Hear(ctx context.Context, event string, params json.RawMessage)
}

// BuildWith is Build with all a program is started with.
type BuildWith func(init Init, secrets connector.Secrets) (connector.Connector, error)

// Restarts of a failing Watch inside the program (the hub's Registry restarts the hub side).
var (
	watchFirst = 5 * time.Second
	watchLast  = 5 * time.Minute
)

// Main serves build's connector on in / out until the hub closes them (the program then exits) or ctx ends. What the
// connector logs goes to the standard logger (the program's stderr, which the hub keeps).
func Main(ctx context.Context, in io.Reader, out io.Writer, build Build) error {
	return MainWith(ctx, in, out, func(i Init, s connector.Secrets) (connector.Connector, error) { return build(i.Config, s) })
}

// MainWith is Main for a program that needs its declared secrets or data directory (a plugin's connector).
func MainWith(ctx context.Context, in io.Reader, out io.Writer, build BuildWith) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	peer := rpc.New("c", out)
	s := &server{peer: peer, build: build, ctx: ctx}
	peer.OnRequest = s.handle
	peer.OnEvent = s.event
	return peer.Serve(ctx, in)
}

// event passes the hub's event.* notifications to a connector that listens (Listener); the others are dropped, as
// before there were any.
func (s *server) event(name string, params json.RawMessage) {
	topic, ok := strings.CutPrefix(name, "event.")
	if !ok {
		return
	}
	s.mu.Lock()
	l, ok := s.c.(Listener)
	s.mu.Unlock()
	if ok {
		go l.Hear(s.ctx, topic, params)
	}
}

// Serve serves c, built and running already (its Run and Watch are the caller's), on in / out: what a test holds a
// connector to the protocol with (connector/conformance). Its secrets are its own.
func Serve(ctx context.Context, in io.Reader, out io.Writer, c connector.Connector) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	peer := rpc.New("c", out)
	s := &server{peer: peer, ctx: ctx, c: c, running: true}
	peer.OnRequest = s.handle
	return peer.Serve(ctx, in)
}

type server struct {
	peer  *rpc.Peer
	build BuildWith
	ctx   context.Context
	// running: c was handed over built and running (Serve); init only answers its manifest.
	running bool

	mu sync.Mutex
	c  connector.Connector
	// inPlugin: opened with initialize (the plugin protocol), not init. Atomic, like cid: the secrets read them
	// while init holds mu (a connector may load its secret while it is built).
	inPlugin atomic.Bool
	// cid is the connector's id, for the secrets' calls under the plugin protocol: what initialize said is active
	// until the connector is built, then its own.
	cid atomic.Pointer[string]
	// once runs each actions.do once by its request id.
	once writes
	// chid is the channel the connector is (the plugin protocol's active channel; its own id when none).
	chid string
	// sent are the refs of the latest channel.send done (or still running), and what each answered: the hub sends
	// one again when it did not hear back.
	sent sentRefs
}

// channelHub is the hub as the connector calls it, as a channel.
type channelHub struct{ s *server }

func (c channelHub) id() string {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	if c.s.chid != "" {
		return c.s.chid
	}
	if p := c.s.cid.Load(); p != nil {
		return *p
	}
	return ""
}

func (c channelHub) Receive(ctx context.Context, m channel.Message) error {
	return c.s.peer.Call(ctx, channel.InPlugin(channel.MReceive), channel.ReceiveParams{Contribution: c.id(), Message: m}, nil)
}

func (c channelHub) Status(ctx context.Context, p channel.StatusParams) error {
	p.Contribution = c.id()
	return c.s.peer.Call(ctx, channel.InPlugin(channel.MStatus), p, nil)
}

// channelCall answers the hub's calls in the channel namespace (method without it) with the connector's Transport.
func (s *server) channelCall(ctx context.Context, method string, params json.RawMessage) (any, error) {
	s.mu.Lock()
	c, chid := s.c, s.chid
	s.mu.Unlock()
	if c == nil {
		return nil, &rpc.Error{Code: CodeNotInitialized, Message: "not initialized"}
	}
	if chid == "" {
		chid = c.ID()
	}
	if want := contributionOf(params); want != "" && want != chid {
		return nil, &rpc.Error{Code: extension.CodeNoContribution, Message: "no channel " + want}
	}
	t, ok := c.(channel.Transport)
	if !ok {
		return nil, noCapability(c, "channel")
	}
	cannot := func(err error) error {
		if errors.Is(err, channel.ErrCannotSend) {
			return &rpc.Error{Code: channel.CodeCannotSend, Message: err.Error()}
		}
		return err
	}
	switch method {
	case channel.MSend:
		var p channel.SendParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		if p.Ref == "" {
			return nil, errors.New("channel.send without a ref")
		}
		res, err := s.sent.do(p.Ref, func() (channel.SendResult, error) { return t.Send(ctx, p) })
		return res, cannot(err)
	case channel.MTyping:
		var p channel.TypingParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		if ty, ok := c.(channel.Typer); ok {
			return struct{}{}, ty.Typing(ctx, p.On)
		}
		return struct{}{}, nil
	case channel.MEdit:
		var p channel.EditParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		ed, ok := c.(channel.Editor)
		if !ok {
			return nil, noCapability(c, "edit")
		}
		return struct{}{}, ed.Edit(ctx, p)
	}
	return nil, &rpc.Error{Code: CodeUnknownMethod, Message: "unknown method channel." + method}
}

// sentRefs runs each channel.send once by its ref (a failed one may go again): the latest doneMost.
// A send still running holds its ref, so the hub's retry of it (it timed out waiting) waits for that send
// instead of sending the message again.
type sentRefs struct {
	mu   sync.Mutex
	sent map[string]*sentRef
	// order is the refs sent successfully, oldest first; a running or failed send is not in it.
	order []string
}

type sentRef struct {
	done chan struct{}
	res  channel.SendResult
	err  error
}

func (r *sentRefs) do(ref string, send func() (channel.SendResult, error)) (channel.SendResult, error) {
	r.mu.Lock()
	if prev, ok := r.sent[ref]; ok {
		r.mu.Unlock()
		<-prev.done
		return prev.res, prev.err
	}
	if r.sent == nil {
		r.sent = map[string]*sentRef{}
	}
	cur := &sentRef{done: make(chan struct{})}
	r.sent[ref] = cur
	r.mu.Unlock()
	cur.res, cur.err = send()
	r.mu.Lock()
	if cur.err != nil {
		// Forget a failed send before waking its waiters, so the ref may be sent again.
		delete(r.sent, ref)
	} else {
		r.order = append(r.order, ref)
		if len(r.order) > doneMost {
			delete(r.sent, r.order[0])
			r.order = r.order[1:]
		}
	}
	r.mu.Unlock()
	close(cur.done)
	return cur.res, cur.err
}

func (s *server) handle(ctx context.Context, method string, params json.RawMessage) (any, error) {
	if method == extension.MInitialize {
		return s.initialize(params)
	}
	if rest, ok := strings.CutPrefix(method, channel.Namespace+"."); ok {
		return s.channelCall(ctx, rest, params)
	}
	if rest, ok := strings.CutPrefix(method, Namespace+"."); ok {
		s.mu.Lock()
		c := s.c
		s.mu.Unlock()
		if c != nil {
			if want := contributionOf(params); want != "" && want != c.ID() {
				return nil, &rpc.Error{Code: extension.CodeNoContribution, Message: "no connector " + want}
			}
		}
		if rest == MInit {
			return nil, &rpc.Error{Code: CodeUnknownMethod, Message: "unknown method " + method}
		}
		method = rest
	} else if method == extension.MPing {
		s.mu.Lock()
		c := s.c
		s.mu.Unlock()
		if c == nil {
			return nil, &rpc.Error{Code: CodeNotInitialized, Message: "not initialized"}
		}
		return struct{}{}, nil
	}
	if method == MInit {
		var p InitParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		if p.Protocol != Protocol {
			return nil, &rpc.Error{Code: CodeWrongProtocol, Message: fmt.Sprintf("protocol %d, not %d", p.Protocol, Protocol)}
		}
		return s.init(Init{Config: p.Config})
	}
	s.mu.Lock()
	c := s.c
	s.mu.Unlock()
	if c == nil {
		return nil, &rpc.Error{Code: CodeNotInitialized, Message: "not initialized"}
	}
	switch method {
	case MManifest:
		return ManifestOf(c), nil
	case MStatus:
		return connector.StatusOf(ctx, c), nil
	case MLoginStep:
		l, ok := connector.As[connector.Login](c)
		if !ok {
			return nil, noCapability(c, "login")
		}
		var p LoginParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return l.LoginStep(ctx, p.Step, p.Values)
	case MLogout:
		l, ok := connector.As[connector.Login](c)
		if !ok {
			return nil, noCapability(c, "login")
		}
		return struct{}{}, l.Logout(ctx)
	case MChanges:
		a, ok := connector.As[connector.Archive](c)
		if !ok {
			return nil, noCapability(c, "archive")
		}
		var p ChangesParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		b, next, more, err := a.Changes(ctx, p.Cursor, p.Limit)
		return ChangesResult{Batches: b, Next: next, More: more}, err
	case MHistoryChats, MHistory, MHistoryAnchors:
		return historyCall(ctx, c, method, params)
	case MCollect:
		in, ok := connector.As[connector.Inbox](c)
		if !ok {
			return nil, noCapability(c, "inbox")
		}
		things, err := in.Collect(ctx)
		return CollectResult{Things: things}, err
	case MReply:
		in, ok := connector.As[connector.Inbox](c)
		if !ok {
			return nil, noCapability(c, "inbox")
		}
		var p ReplyParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return struct{}{}, in.Reply(ctx, p)
	case MCatalog:
		sources, err := connector.ReadCatalog(ctx, c)
		return connector.CatalogResult{Sources: sources}, err
	case MCatalogCall:
		var p connector.CatalogQuery
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		text, err := connector.CallCatalog(ctx, c, p)
		return CallResult{Text: text}, err
	case MTools:
		t, ok := connector.As[connector.ReadTools](c)
		if !ok {
			return ToolsResult{}, nil
		}
		return ToolsResult{Tools: t.Tools()}, nil
	case MCall:
		t, ok := connector.As[connector.ReadTools](c)
		if !ok {
			return nil, noCapability(c, "tools")
		}
		var p CallParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		text, err := t.Call(ctx, p.Name, p.Args)
		return CallResult{Text: text}, err
	case MActions:
		a, ok := connector.As[connector.Actions](c)
		if !ok {
			return ToolsResult{}, nil
		}
		return ToolsResult{Tools: a.Actions()}, nil
	case MDescribe:
		a, ok := connector.As[connector.Actions](c)
		if !ok {
			return nil, noCapability(c, "actions")
		}
		var p CallParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		act, summary, err := a.Describe(ctx, p.Name, p.Args)
		return DescribeResult{Action: act, Summary: summary}, err
	case MThread:
		th, ok := connector.As[connector.Threads](c)
		if !ok {
			return nil, noCapability(c, "threads")
		}
		var p ThreadParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return th.Thread(ctx, p.Thread, p.Before, min(max(p.Limit, 1), connector.ThreadMost))
	case MGroupAppend:
		w, ok := connector.As[connector.EventWriter](c)
		if !ok {
			return nil, noCapability(c, "meta")
		}
		var p connector.GroupAppend
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return w.GroupAppend(ctx, p)
	case MGroupTool, MGroupFallback:
		g, ok := connector.As[connector.Grouper](c)
		if !ok {
			return nil, noCapability(c, "group")
		}
		return groupCall(ctx, g, method, params)
	case MAuthGet:
		a, ok := c.(Auther)
		if !ok {
			return nil, noCapability(c, "auth")
		}
		return a.Auth(ctx), nil
	case MAuthSet:
		a, ok := c.(Auther)
		if !ok {
			return nil, noCapability(c, "auth")
		}
		var p AuthSource
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return a.SetAuth(ctx, p)
	case MOptionsGet:
		o, ok := connector.As[connector.Options](c)
		if !ok {
			return nil, noCapability(c, "options")
		}
		return OptionsResult{Options: o.Options()}, nil
	case MOptionsSet:
		o, ok := connector.As[connector.Options](c)
		if !ok {
			return nil, noCapability(c, "options")
		}
		var p OptionsParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return struct{}{}, o.SetOptions(p.Options)
	case MSettle:
		st, ok := connector.As[connector.InboxSettler](c)
		if !ok {
			return nil, noCapability(c, "settle")
		}
		var p SettleParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return SettleResult{Settled: st.Settle(ctx, p.Threads)}, nil
	case MMachineOnline:
		m, ok := connector.As[connector.Machines](c)
		if !ok {
			return nil, noCapability(c, "machines")
		}
		var p MachineParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		m.MachineOnline(p.Machine)
		return struct{}{}, nil
	case MSnapshot:
		sn, ok := connector.As[connector.Snapshot](c)
		if !ok {
			return nil, noCapability(c, "snapshot")
		}
		var p SnapshotParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return sn.Snapshot(ctx, p.TZ)
	case MItems:
		is, ok := connector.As[connector.Items](c)
		if !ok {
			return nil, noCapability(c, "items")
		}
		items, err := is.ItemSource().Items(ctx)
		if items == nil {
			items = []kbwire.Item{}
		}
		return ItemsResult{Items: items}, err
	case MDocTitle:
		is, ok := connector.As[connector.Items](c)
		if !ok {
			return nil, noCapability(c, "items")
		}
		var p DocTitleParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return DocTitleResult{Title: is.ItemSource().DocTitle(ctx, p.URL)}, nil
	case MArchiveMedia:
		am, ok := c.(connector.ArchiveMedia)
		if _, archive := connector.As[connector.Archive](c); !ok || !archive {
			return nil, noCapability(c, "archive")
		}
		var p MediaParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		m, err := am.ArchiveMedia(ctx, p.Ref)
		if err == nil && len(m.Data) > connector.ArchiveMediaMost {
			return nil, fmt.Errorf("attachment %d bytes, more than %d", len(m.Data), connector.ArchiveMediaMost)
		}
		return m, err
	case MMedia:
		th, ok := connector.As[connector.Threads](c)
		if !ok {
			return nil, noCapability(c, "threads")
		}
		var p MediaParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		m, err := th.Media(ctx, p.Ref)
		if err == nil && len(m.Data) > connector.MediaMost {
			return nil, fmt.Errorf("media %d bytes, more than %d", len(m.Data), connector.MediaMost)
		}
		return m, err
	case MDo:
		a, ok := connector.As[connector.Actions](c)
		if !ok {
			return nil, noCapability(c, "actions")
		}
		var p DoParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		if p.RequestID == "" {
			return nil, errors.New("actions.do without a request_id")
		}
		return s.once.do(p.RequestID, func() (CallResult, error) {
			done, err := connector.Write(ctx, a, p.Name, p.Args)
			return CallResult{Text: done.Text, MessageRef: done.MessageRef}, err
		})
	case MRecall:
		r, ok := connector.RecallerOf(c)
		if !ok || !c.Manifest().Has(connector.CapRecall) {
			return nil, noCapability(c, "recall")
		}
		var p RecallParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		if p.MessageRef == "" {
			return nil, errors.New("recall without a message_ref")
		}
		target := json.RawMessage(`{}`)
		if len(p.Target) > 0 {
			target, _ = json.Marshal(p.Target)
		}
		if err := r.Recall(ctx, connector.SentMessage{Action: p.Action, Ref: p.MessageRef, Target: target, At: p.At}); err != nil {
			return nil, recallError(err)
		}
		return RecallResult{Recalled: true}, nil
	}
	return nil, &rpc.Error{Code: CodeUnknownMethod, Message: "unknown method " + method}
}

// historyCall answers history.chats, archive.history and history.anchors for a connector declaring them. The query
// is checked and its limit cut to connectorwire.HistoryMost before the connector sees it; what the connector answers,
// error included, goes back as it is.
func historyCall(ctx context.Context, c connector.Connector, method string, params json.RawMessage) (any, error) {
	h, ok := connector.As[connector.History](c)
	if !ok {
		return nil, noCapability(c, "history")
	}
	switch method {
	case MHistoryChats:
		chats, err := h.HistoryChats(ctx)
		if chats == nil {
			chats = []connectorwire.HistoryChat{}
		}
		return HistoryChatsResult{Chats: chats}, err
	case MHistory:
		var q HistoryParams
		if err := json.Unmarshal(params, &q); err != nil {
			return nil, err
		}
		if err := q.Check(); err != nil {
			return nil, err
		}
		return h.History(ctx, q.Clamped())
	}
	a, ok := connector.As[connector.HistoryAnchors](c)
	if !ok {
		return nil, noCapability(c, "history_anchors")
	}
	var q HistoryAnchorsParams
	if err := json.Unmarshal(params, &q); err != nil {
		return nil, err
	}
	if err := q.Check(); err != nil {
		return nil, err
	}
	return a.HistoryAnchors(ctx, q.Clamped())
}

// recallError is a connector's failed Recall as the wire says it: the code of the connector.Err* it wraps (one it
// could not act now is not_signed_in: the hub reads that like offline), none for the other side's refusal.
func recallError(err error) error {
	var re *rpc.Error
	switch {
	case errors.As(err, &re):
		return re
	case errors.Is(err, connector.ErrCannotRecall):
		return &rpc.Error{Code: CodeNotRecallable, Message: err.Error()}
	case errors.Is(err, connector.ErrRecallUnavailable):
		return &rpc.Error{Code: CodeNotSignedIn, Message: err.Error()}
	case errors.Is(err, connector.ErrRecallUnknown):
		return &rpc.Error{Code: CodeRecallUnknown, Message: err.Error()}
	}
	return &rpc.Error{Message: err.Error()}
}

// contributionOf is the contribution a namespaced call names ("" when it names none).
func contributionOf(params json.RawMessage) string {
	var t extension.Target
	_ = json.Unmarshal(params, &t)
	return t.Contribution
}

// initialize opens the plugin protocol: the connector is built as with init, and the answer says this program is the
// plugin the hub started (a first-party program is the hub's build: it takes the id and version it is given) and
// contributes the connector.
func (s *server) initialize(params json.RawMessage) (any, error) {
	var p extension.InitializeParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	if p.Protocol != extension.Protocol {
		return nil, &rpc.Error{Code: extension.CodeWrongProtocol, Message: fmt.Sprintf("plugin protocol %d, not %d", p.Protocol, extension.Protocol)}
	}
	s.mu.Lock()
	if s.c == nil {
		s.inPlugin.Store(true)
		if ids := p.Active["connectors"]; len(ids) == 1 {
			s.cid.Store(&ids[0])
		}
		if ids := p.Active["channel"]; len(ids) == 1 {
			s.chid = ids[0]
		}
	}
	s.mu.Unlock()
	m, err := s.init(Init{Config: p.Config, Declared: p.Secrets, DataDir: p.Extension.DataDir, Channel: channelHub{s}, Locale: p.Hub.Locale})
	if err != nil {
		return nil, err
	}
	item, _ := json.Marshal([]extension.ConnectorItem{ItemOf(m)})
	return extension.InitializeResult{Protocol: extension.Protocol, ID: p.Extension.ID, Version: p.Extension.Version,
		Contributes: map[string]json.RawMessage{"connectors": item}}, nil
}

// ItemOf is a connector's manifest as a contribution to the connectors extension point.
func ItemOf(m Manifest) extension.ConnectorItem {
	return extension.ConnectorItem{ID: m.ID, Name: m.Name, Capabilities: m.Capabilities, Hosts: m.Hosts, AgentTools: m.AgentTools,
		Unlisted: m.Unlisted, ConfirmAlways: m.ConfirmAlways, PollSeconds: m.PollSeconds, Kinds: m.Kinds, ArchiveSources: m.ArchiveSources,
		MetaDescription: m.MetaDescription, CLI: m.CLI}
}

// groupCall answers group.tool and group.fallback.
func groupCall(ctx context.Context, g connector.Grouper, method string, params json.RawMessage) (any, error) {
	switch method {
	case MGroupTool:
		var p connector.GroupToolCall
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return g.GroupTool(ctx, p)
	default:
		var p connector.GroupFallback
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return g.GroupFallback(ctx, p)
	}
}

// init builds the connector the first time and starts its Run and Watch; a second init answers the same.
func (s *server) init(init Init) (Manifest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.c != nil {
		if s.running {
			return ManifestOf(s.c), connector.Check(s.c)
		}
		return ManifestOf(s.c), nil
	}
	c, err := s.build(init, &secrets{s: s})
	if err != nil {
		return Manifest{}, err
	}
	if err := connector.Check(c); err != nil {
		return Manifest{}, err
	}
	s.c = c
	id := c.ID()
	s.cid.Store(&id)
	if r, ok := c.(connector.Runner); ok {
		go r.Run(s.ctx)
	}
	if e, ok := c.(connector.Events); ok {
		go s.watch(e)
	}
	return ManifestOf(c), nil
}

// watch passes the connector's changes on to the hub, starting a Watch that fails again after a backoff.
func (s *server) watch(e connector.Events) {
	wait := watchFirst
	for s.ctx.Err() == nil {
		start := time.Now()
		err := e.Watch(s.ctx, func(scope connector.Scope) { s.notifyChanged(scope) })
		if s.ctx.Err() != nil {
			return
		}
		if time.Since(start) > time.Minute {
			wait = watchFirst
		}
		log.Printf("watch stopped (%v), again in %s", err, wait)
		select {
		case <-s.ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(wait*2, watchLast)
	}
}

// doneMost is how many writes the program remembers by request id.
const doneMost = 256

// writes runs each write (actions.do) once by its request id: the same id again answers what the first run did,
// waiting for it if it is still running.
type writes struct {
	mu   sync.Mutex
	runs map[string]*write
	// order is the finished writes (failed ones too), oldest first; a running write is not in it.
	order []string
}

type write struct {
	done chan struct{}
	res  CallResult
	err  error
}

func (w *writes) do(id string, run func() (CallResult, error)) (CallResult, error) {
	w.mu.Lock()
	if prev, ok := w.runs[id]; ok {
		w.mu.Unlock()
		<-prev.done
		return prev.res, prev.err
	}
	if w.runs == nil {
		w.runs = map[string]*write{}
	}
	cur := &write{done: make(chan struct{})}
	w.runs[id] = cur
	w.mu.Unlock()
	cur.res, cur.err = run()
	// Count the write toward doneMost only once it finished, so a write still running is never forgotten.
	w.mu.Lock()
	w.order = append(w.order, id)
	if len(w.order) > doneMost {
		delete(w.runs, w.order[0])
		w.order = w.order[1:]
	}
	w.mu.Unlock()
	close(cur.done)
	return cur.res, cur.err
}

// ManifestOf is c's manifest on the wire.
func ManifestOf(c connector.Connector) Manifest {
	cm := c.Manifest()
	m := Manifest{Protocol: Protocol, ID: c.ID(), Name: c.Name(), Capabilities: []string{}, Hosts: cm.Hosts, AgentTools: cm.AgentTools,
		Unlisted: cm.Unlisted, ConfirmAlways: cm.ConfirmAlways, PollSeconds: int(cm.Poll / time.Second), Kinds: cm.Kinds,
		ArchiveSources: cm.ArchiveSources, MetaDescription: cm.MetaDescription, CLI: cm.CLI}
	for _, cp := range cm.Capabilities {
		m.Capabilities = append(m.Capabilities, string(cp))
	}
	return m
}

// Of is the connector.Manifest a program sent (a proxy's: it answers for the program).
func (m Manifest) Of() connector.Manifest {
	out := connector.Manifest{Hosts: m.Hosts, AgentTools: m.AgentTools, Unlisted: m.Unlisted, ConfirmAlways: m.ConfirmAlways,
		Poll: time.Duration(m.PollSeconds) * time.Second, Kinds: m.Kinds, ArchiveSources: m.ArchiveSources,
		MetaDescription: m.MetaDescription, CLI: m.CLI, Proxy: true}
	for _, cp := range m.Capabilities {
		out.Capabilities = append(out.Capabilities, connector.Capability(cp))
	}
	return out
}

func noCapability(c connector.Connector, what string) error {
	return &rpc.Error{Code: CodeNoCapability, Message: fmt.Sprintf("%s has no %s", c.Name(), what)}
}

// notifyChanged tells the hub something changed, in the opening's words.
func (s *server) notifyChanged(scope connector.Scope) {
	s.mu.Lock()
	c := s.c
	s.mu.Unlock()
	if s.inPlugin.Load() && c != nil {
		_ = s.peer.Notify(InPlugin(EChanged), ChangedEvent{Contribution: c.ID(), Scope: string(scope)})
		return
	}
	_ = s.peer.Notify(EChanged, ChangedEvent{Scope: string(scope)})
}

// secrets is the hub's store for this program's connector, over the pipe.
type secrets struct{ s *server }

func (x *secrets) call(ctx context.Context, method string, params, out any) error {
	if !x.s.inPlugin.Load() {
		return x.s.peer.Call(ctx, method, params, out)
	}
	id := ""
	if p := x.s.cid.Load(); p != nil {
		id = *p
	}
	p, err := extension.Targeted(params, id)
	if err != nil {
		return err
	}
	return x.s.peer.Call(ctx, InPlugin(method), p, out)
}

func (x *secrets) Load(ctx context.Context) ([]byte, error) {
	var out SecretData
	err := x.call(ctx, MSecretsLoad, struct{}{}, &out)
	return out.Data, err
}

func (x *secrets) Save(ctx context.Context, data []byte) error {
	return x.call(ctx, MSecretsSave, SecretData{Data: data}, nil)
}

func (x *secrets) Delete(ctx context.Context) error {
	return x.call(ctx, MSecretsDelete, struct{}{}, nil)
}

// Auther is a connector whose sign-in comes from where the user picks, the hub keeping the choice (auth.get /
// auth.set): GitHub's computer or device flow.
type Auther interface {
	Auth(ctx context.Context) AuthView
	SetAuth(ctx context.Context, a AuthSource) (AuthView, error)
}

// OptionsResult is options.get's answer; OptionsParams is options.set's.
type OptionsResult struct {
	Options json.RawMessage `json:"options,omitempty"`
}

type OptionsParams struct {
	Options json.RawMessage `json:"options"`
}

// SettleParams and SettleResult are inbox.settle's: the open items' threads, and why each that is over is.
type SettleParams struct {
	Threads []string `json:"threads"`
}

type SettleResult struct {
	Settled map[string]string `json:"settled,omitempty"`
}

// MachineParams is machines.online's: the machine that connected.
type MachineParams struct {
	Machine string `json:"machine"`
}

// AuthSource is the user's choice of where the sign-in comes from: Source "machine" (a computer's gh login,
// Machine its id and MachineName what people call it) or "device" (the device flow).
type AuthSource struct {
	Source      string `json:"source"`
	Machine     string `json:"machine,omitempty"`
	MachineName string `json:"machine_name,omitempty"`
}

// AuthView is auth.get's and auth.set's answer: the choice in effect, and what the token in use may do (Scopes,
// once ScopesKnown).
type AuthView struct {
	Source      string   `json:"source"`
	Machine     string   `json:"machine,omitempty"`
	Scopes      []string `json:"scopes,omitempty"`
	ScopesKnown bool     `json:"scopes_known"`
}

// GhTokenParams and GhTokenResult are credentials.gh_token's.
type GhTokenParams struct {
	Machine string `json:"machine"`
	Host    string `json:"host,omitempty"`
}

type GhTokenResult struct {
	Token string `json:"token"`
}
