package assistant

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/feishubot"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// The Feishu bot's chat (docs/extensions/bots.md): the owner talks to Persona in the bot's private chat — the main conversation,
// another one picked with /new and /list, or an agent picked with /agents — and the answer comes back as cards;
// what is asked shows as cards there too. The hub keeps the bot's transport (its credentials, the long connection,
// pairing, the open platform's REST): it hands the owner's messages and presses here (feishu.message,
// feishu.card_action), and posts what is given it (app.call feishu.*). The bot's switches (bots.feishu features) are
// the hub's, kept with its credentials; the chat's state (bots.feishu.state) is the assistant's.

// botStateKey is the chat's state: where messages go, the threads bound to targets, the messages already taken.
const botStateKey = "bots.feishu.state"

// botTarget is where a message goes: the main conversation (whichever it is when the message is sent), another
// Persona conversation, or a coding agent.
type botTarget struct {
	Kind      string `json:"kind"` // main | persona | agent
	MachineID string `json:"machine_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

const (
	targetMain    = "main"
	targetPersona = "persona"
	targetAgent   = "agent"
)

func (t botTarget) agent() bool { return t.Kind == targetAgent }

// botThread is a message the bot posted for a target (an agent picked with /agents): replies to it, and messages in its
// thread, go to that target.
type botThread struct {
	Target botTarget `json:"target"`
	Title  string    `json:"title,omitempty"`
	At     time.Time `json:"at"`
}

// botState is the chat's state (botStateKey).
type botState struct {
	Current botTarget             `json:"current"`
	Threads map[string]*botThread `json:"threads,omitempty"`
	// Runs are an older hub's turns being answered: moved to the channels' runs (takeLegacyBotRuns).
	Runs []*botRun `json:"runs,omitempty"`
	Seen []string  `json:"seen,omitempty"`
	// Cards are the cards asking what is asked that are still open in the chat, by interaction id: only where each
	// is and what it answers (never what it says), so a card answered elsewhere after a restart is still closed.
	Cards map[string]*botCardRef `json:"cards,omitempty"`
	// Pairing is the pairing the state belongs to (the owner and when they paired, feishu.status; "" not paired; nil:
	// not recorded yet, a state from before). Another one, or none, starts the chat afresh (feishuReconcile).
	Pairing *string `json:"pairing,omitempty"`
	// Topics are the latest message the owner said in each topic (chat/thread -> message), what a reply into the thread
	// goes by (feishu_thread_posts.go).
	Topics map[string]*topicAnchor `json:"topics,omitempty"`
	// Board is the standing "waiting for you" card: its message and when it was sent (feishu_board.go). Only the bot's
	// `board` switch uses it.
	Board *botBoardRef `json:"board,omitempty"`
	// Companions are the other Lark bots an extension registered for the workbench (feishu_companions.go).
	Companions []CompanionBot `json:"companions,omitempty"`
	// NudgeOffAt is when the nudge sweep first saw nudges off (nudge_after 0), cleared when it sees them on again: the
	// first sweep with them on lets the cards already due go (feishu_nudge.go). Kept here so a restart while they are
	// off does not nudge the backlog.
	NudgeOffAt time.Time `json:"nudge_off_at,omitzero"`
}

// The bot's messages a reply is routed by: each turn binds several (its progress card or agent message, each answer
// card), and pushes and synced turns more, so a count of 100 forgot a card of a few days ago. They are kept by age,
// with a cap so the state, written on every message, stays small (about 110 KB at 500).
const (
	botSeenKeep       = 300
	botThreadsKeep    = 500
	botThreadsKeepFor = 30 * 24 * time.Hour
	botCardsKeep      = 100
	botCardsDays      = 7
)

// botCardRef is a card kept in the chat's state: its message and the interaction it asks.
type botCardRef struct {
	MessageID string    `json:"message_id"`
	Kind      string    `json:"kind,omitempty"`
	MachineID string    `json:"machine_id,omitempty"`
	SessionID string    `json:"session_id,omitempty"`
	Thread    string    `json:"thread,omitempty"`  // the topic it was posted into (botCard.Thread)
	ChatID    string    `json:"chat_id,omitempty"` // the group chat of that topic (botCard.ChatID); "" in the owner's DM
	At        time.Time `json:"at"`
	// NudgedAt is when the card was nudged under (feishu_nudge.go): once set, never again, also after a restart.
	NudgedAt time.Time `json:"nudged_at,omitzero"`
	// SkippedAt is when the nudge for this card was given up (not due any more when nudges came on again, the chat
	// refused it, too many tries): never nudged, and not shown as nudged. Tries and TriedAt are the sends tried and
	// when the first went out, for the retry limits.
	SkippedAt time.Time `json:"nudge_skipped_at,omitzero"`
	Tries     int       `json:"nudge_tries,omitempty"`
	TriedAt   time.Time `json:"nudge_tried_at,omitzero"`
}

// feishuBook is the chat's state in memory, the cards asking what is asked, and the bot's status a moment.
type feishuBook struct {
	mu       sync.Mutex
	loaded   bool
	st       botState
	cards    map[string]botCard      // interaction id -> the card asking it
	detached map[string]detachedCard // interaction id -> its card closed when its node went away (feishuDetach)
	// ended: interaction id -> a question card that ended without being answered in this chat (feishuInteractionDone),
	// kept for endedAskFor so an option typed for it late is not sent on as a new message (botAnswerQuestion).
	ended    map[string]endedCard
	status   *FeishuStatus
	statusAt time.Time
	now      func() time.Time     // tests
	startAt  map[string]time.Time // recent /start form presses, for one press one agent (feishu_card_actions.go)
	// line is closed when the last message taken is through (FeishuMessage): the chat's messages are handled one at
	// a time, in the order they came.
	line chan struct{}
	// turnWait is how long a message waits for the one before it (0: feishuTurnWait; tests change it).
	turnWait time.Duration
	// recalled are the messages the owner recalled lately (FeishuRecalled): one not in its conversation yet is not sent.
	recalled map[string]time.Time
	// hinted are the topics told lately that they are tied to no open to-do (threadHint), by chat and thread.
	hinted map[string]time.Time
	// lookupWait is how long the topic of a message just sent waits between two reads of it (0: topicLookupWait;
	// tests change it).
	lookupWait time.Duration
}

// feishuRecalledKeep is how long a recall is remembered: a message waits at most feishuTurnWait for the one before
// it and a few minutes for its downloads.
const feishuRecalledKeep = 10 * time.Minute

// FeishuRecalled is feishu.recalled: the owner recalled a message in the bot's chat. One not in its conversation yet
// is not sent; one being answered stops its turn (recallChannelRun).
func (s *Service) FeishuRecalled(p FeishuRecalled) {
	if p.MessageID == "" {
		return
	}
	s.noteRecalled(p.MessageID)
	go s.recallChannelRun(channel.Feishu, p.MessageID, channel.ClientMsgID(channel.Feishu, p.MessageID))
}

// noteRecalled remembers that the owner recalled message id, for feishuRecalledKeep.
func (s *Service) noteRecalled(id string) {
	b := &s.fbot
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.clock()
	for id, at := range b.recalled {
		if now.Sub(at) > feishuRecalledKeep {
			delete(b.recalled, id)
		}
	}
	if b.recalled == nil {
		b.recalled = map[string]time.Time{}
	}
	b.recalled[id] = now
}

// botRecalled says whether the owner recalled message id.
func (s *Service) botRecalled(id string) bool {
	b := &s.fbot
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.recalled[id]
	return ok
}

// feishuTurnWait is how long a message waits for the one before it to go into its conversation. Most get there in a
// second or two; one stuck on a slow download or a new conversation that is slow to start must not hold up the chat
// for the 3 minutes it may take, so after this the next message goes on without it.
const feishuTurnWait = 20 * time.Second

// feishuTurn is one message's place in the chat's line: it waits for prev, and done is closed when it is through.
type feishuTurn struct {
	prev, done chan struct{}
	wait       time.Duration
	since      time.Time        // when it came
	now        func() time.Time // the chat's clock (feishuBook.now)
	once       sync.Once
}

// waitTurn waits for the message before it until t.wait after it came (the hub's toAssistantSince): one that waited
// behind others already does not wait the whole time again, and one past it goes on at once.
func (t *feishuTurn) waitTurn() {
	if t.prev == nil {
		return
	}
	left := t.wait - t.now().Sub(t.since)
	if left <= 0 {
		return
	}
	timer := time.NewTimer(left)
	defer timer.Stop()
	select {
	case <-t.prev:
	case <-timer.C:
	}
}

// pass lets the next message go once this one's turn has come: right away when it waited already, else after the
// message before it (a message not handled still keeps its place, so the ones after it do not overtake that one).
func (t *feishuTurn) pass() {
	t.once.Do(func() {
		go func() {
			t.waitTurn()
			close(t.done)
		}()
	})
}

type botCard struct {
	MessageID string
	Thread    string            // the topic the card was posted into (the run anchor it replies to); "" in the root chat
	ChatID    string            // the group chat the card is in (a topic's card); "" in the owner's DM
	Ix        proto.Interaction // what it asks (to close it when it ended unseen)
	At        time.Time         // when it was posted: the latest waiting card is the one a typed answer answers
	NudgedAt  time.Time         // when it was nudged under, zero before (feishu_nudge.go)
	SkippedAt time.Time         // when its nudge was given up (botCardRef.SkippedAt)
	Tries     int               // nudge sends tried
	TriedAt   time.Time         // when the first nudge send was tried
}

// nudgeSettled: the card has had its nudge, or the nudge was given up; either way there is no more to do.
func (c botCard) nudgeSettled() bool { return !c.NudgedAt.IsZero() || !c.SkippedAt.IsZero() }

func (b *feishuBook) clock() time.Time {
	if b.now != nil {
		return b.now()
	}
	return time.Now()
}

// loadBotLocked reads the chat's state once; b.mu is held.
func (s *Service) loadBotLocked(ctx context.Context) {
	b := &s.fbot
	if b.loaded {
		return
	}
	if st := s.store(); st != nil {
		if _, err := st.Setting(ctx, botStateKey, &b.st); err != nil {
			s.logf("bots: load state: %v", err)
			return // the next look tries again
		}
	}
	b.loaded = true
	if b.st.Threads == nil {
		b.st.Threads = map[string]*botThread{}
	}
	pruneBotThreads(b.st.Threads, b.clock()) // a long quiet chat: the stale ones go before the first route
	if b.st.Current.Kind == "" {
		b.st.Current = botTarget{Kind: targetMain}
	}
	s.trimBotCardsLocked()
	for id, ref := range b.st.Cards { // cards posted before a restart
		if _, ok := b.cards[id]; !ok && ref != nil && ref.MessageID != "" {
			if b.cards == nil {
				b.cards = map[string]botCard{}
			}
			b.cards[id] = botCard{MessageID: ref.MessageID, Thread: ref.Thread, ChatID: ref.ChatID, At: ref.At, NudgedAt: ref.NudgedAt, SkippedAt: ref.SkippedAt, Tries: ref.Tries, TriedAt: ref.TriedAt,
				Ix: proto.Interaction{ID: id, Kind: ref.Kind, MachineID: ref.MachineID, SessionID: ref.SessionID}}
		}
	}
}

// trimBotCardsLocked keeps the cards' records small: none older than botCardsDays (long over, or Feishu no longer
// lets them be changed), at most botCardsKeep, the newest; b.mu is held.
func (s *Service) trimBotCardsLocked() {
	b := &s.fbot
	for id, ref := range b.st.Cards {
		if ref == nil || b.clock().Sub(ref.At) > botCardsDays*24*time.Hour {
			delete(b.st.Cards, id)
		}
	}
	for len(b.st.Cards) > botCardsKeep {
		oldest := ""
		for id, ref := range b.st.Cards {
			if oldest == "" || ref.At.Before(b.st.Cards[oldest].At) {
				oldest = id
			}
		}
		delete(b.st.Cards, oldest)
	}
}

// keepBotCardLocked records (or, card empty, forgets) the card asking interaction id, in memory and in the chat's
// state; b.mu is held.
func (s *Service) keepBotCardLocked(id string, card *botCard) {
	b := &s.fbot
	s.loadBotLocked(context.Background())
	if card == nil {
		delete(b.cards, id)
		if _, ok := b.st.Cards[id]; ok {
			delete(b.st.Cards, id)
			s.saveBotStateLocked()
		}
		return
	}
	if b.cards == nil {
		b.cards = map[string]botCard{}
	}
	b.cards[id] = *card
	if card.MessageID == "" {
		return // claimed, not posted yet
	}
	if b.st.Cards == nil {
		b.st.Cards = map[string]*botCardRef{}
	}
	at := card.At
	if at.IsZero() {
		at = b.clock()
	}
	b.st.Cards[id] = &botCardRef{MessageID: card.MessageID, Kind: card.Ix.Kind, MachineID: card.Ix.MachineID, SessionID: card.Ix.SessionID,
		Thread: card.Thread, ChatID: card.ChatID, At: at.UTC(), NudgedAt: card.NudgedAt.UTC(),
		SkippedAt: card.SkippedAt.UTC(), Tries: card.Tries, TriedAt: card.TriedAt.UTC()}
	s.saveBotStateLocked()
}

// saveBotStateLocked writes the chat's state, once it was read: what is in memory after a failed read would be
// written over the stored target, threads and pairing.
func (s *Service) saveBotStateLocked() {
	b := &s.fbot
	if !b.loaded {
		return
	}
	if len(b.st.Seen) > botSeenKeep {
		b.st.Seen = b.st.Seen[len(b.st.Seen)-botSeenKeep:]
	}
	pruneBotThreads(b.st.Threads, b.clock())
	pruneTopicAnchors(b.st.Topics, b.clock())
	s.trimBotCardsLocked()
	st := s.store()
	if st == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := st.PutSetting(ctx, botStateKey, b.st); err != nil {
		s.logf("bots: save state: %v", err)
	}
}

// pruneBotThreads drops the threads older than botThreadsKeepFor, then the oldest beyond botThreadsKeep. A thread
// without a time (a state written by hand or by a test) is not judged by age; it counts as the oldest for the cap.
func pruneBotThreads(threads map[string]*botThread, now time.Time) {
	for id, t := range threads {
		if t == nil || !t.At.IsZero() && now.Sub(t.At) > botThreadsKeepFor {
			delete(threads, id)
		}
	}
	if len(threads) <= botThreadsKeep {
		return
	}
	ids := make([]string, 0, len(threads))
	for id := range threads {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b string) int { return threads[a].At.Compare(threads[b].At) })
	for _, id := range ids[:len(ids)-botThreadsKeep] {
		delete(threads, id)
	}
}

// feishuStatus is the bot as the hub has it now (feishu.status): on and paired, its switches.
func (s *Service) feishuStatus(ctx context.Context) (FeishuStatus, error) {
	var st FeishuStatus
	if err := s.appCall(ctx, AppFeishu, FeishuStatusGet, map[string]any{}, &st); err != nil {
		return FeishuStatus{}, err
	}
	b := &s.fbot
	b.mu.Lock()
	b.status, b.statusAt = &st, time.Now()
	b.mu.Unlock()
	s.feishuReconcile(st)
	return st, nil
}

// feishuReconcile starts the chat afresh when the bot was paired with someone else, paired again or unpaired since
// the state was written (the hub's feishu.owner may have come while the assistant was away): back to the main
// conversation, no threads, no cards, nothing waiting. A state from before pairings were recorded takes the one now.
func (s *Service) feishuReconcile(st FeishuStatus) {
	key := ""
	if st.Owner != "" {
		key = st.Owner + "@" + strconv.FormatInt(st.PairedAt.Unix(), 10)
	}
	b := &s.fbot
	b.mu.Lock()
	s.loadBotLocked(context.Background())
	if !b.loaded || b.st.Pairing != nil && *b.st.Pairing == key {
		b.mu.Unlock()
		return
	}
	fresh := b.st.Pairing != nil // nil: recorded for the first time, the state stays
	if fresh {
		b.st = botState{Current: botTarget{Kind: targetMain}, Threads: map[string]*botThread{}, Seen: b.st.Seen}
		b.cards, b.ended = nil, nil
	}
	b.st.Pairing = &key
	s.saveBotStateLocked()
	b.mu.Unlock()
	if fresh {
		s.logf("bots: the bot's pairing changed: the chat starts afresh")
		s.forgetChannelTurns(channel.Feishu)
	}
}

// feishuStatusKept is feishuStatus kept a moment: for what looks every second (a run's sink, the cards' links).
func (s *Service) feishuStatusKept(ctx context.Context) (FeishuStatus, error) {
	b := &s.fbot
	b.mu.Lock()
	if b.status != nil && time.Since(b.statusAt) < 3*time.Second {
		st := *b.status
		b.mu.Unlock()
		return st, nil
	}
	b.mu.Unlock()
	return s.feishuStatus(ctx)
}

func (s *Service) feishuForgetStatus() {
	s.fbot.mu.Lock()
	s.fbot.status = nil
	s.fbot.mu.Unlock()
}

func (s *Service) feishuFeatures(ctx context.Context) (FeishuFeatures, bool) {
	st, err := s.feishuStatus(ctx)
	ready := err == nil && st.Ready
	s.workbenchOn(st.Features, ready) // what the posts' workbench button follows (feishu_workbench_pin.go)
	return st.Features, ready
}

// ---- the chat's state ----

// claimBotMessage: the first time this message is taken (Feishu delivers again what it thinks was missed); an error
// while the chat's state can't be read.
func (s *Service) claimBotMessage(id string) (bool, error) {
	b := &s.fbot
	b.mu.Lock()
	defer b.mu.Unlock()
	s.loadBotLocked(context.Background())
	if !b.loaded {
		return false, errBotUnread
	}
	if id == "" || containsString(b.st.Seen, id) {
		return false, nil
	}
	b.st.Seen = append(b.st.Seen, id)
	s.saveBotStateLocked()
	return true, nil
}

var errBotUnread = errors.New("the chat's state could not be read")

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func (s *Service) setBotCurrent(t botTarget) {
	b := &s.fbot
	b.mu.Lock()
	s.loadBotLocked(context.Background())
	b.st.Current = t
	s.saveBotStateLocked()
	b.mu.Unlock()
}

// botTargetFor is where a message goes: the target of the bot message it replies to (or of its thread), else the
// chat's current one.
func (s *Service) botTargetFor(root, parent string) botTarget {
	b := &s.fbot
	b.mu.Lock()
	defer b.mu.Unlock()
	s.loadBotLocked(context.Background())
	for _, id := range []string{parent, root} {
		if t := b.st.Threads[id]; id != "" && t != nil {
			return t.Target
		}
	}
	return b.st.Current
}

// rememberBotThread binds a message the bot posted to a target: replying to it talks to that target.
func (s *Service) rememberBotThread(messageID string, t botTarget, title string) {
	if messageID == "" || t.Kind == "" {
		return
	}
	b := &s.fbot
	b.mu.Lock()
	defer b.mu.Unlock()
	s.loadBotLocked(context.Background())
	if !b.loaded {
		return // its threads could not be read: nothing to add to
	}
	b.st.Threads[messageID] = &botThread{Target: t, Title: title, At: b.clock().UTC()}
	s.saveBotStateLocked()
}

// feishuReset starts the chat afresh (unpaired from the channels' page): back to the main conversation, no threads,
// no cards, nothing waiting; the pairing it belongs to is read again.
func (s *Service) feishuReset() {
	b := &s.fbot
	b.mu.Lock()
	s.loadBotLocked(context.Background())
	pairing := b.st.Pairing
	b.st = botState{Current: botTarget{Kind: targetMain}, Threads: map[string]*botThread{}, Seen: b.st.Seen, Pairing: pairing}
	b.cards, b.ended = nil, nil
	b.status = nil
	s.saveBotStateLocked()
	b.mu.Unlock()
}

// feishuUnpaired: the owner went: the chat's state and what it waited on go.
func (s *Service) feishuUnpaired() {
	s.feishuReset()
	s.forgetChannelTurns(channel.Feishu)
}

// FeishuOwner is feishu.owner: the bot was paired (it says hello, with what it can do) or unpaired. The pairing now
// is read from the hub (feishuReconcile starts the chat afresh), the same as on any later look when this event was
// missed.
func (s *Service) FeishuOwner(_ context.Context, p FeishuOwner) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		st, err := s.feishuStatus(ctx)
		if err != nil {
			s.feishuUnpaired() // the hub could not be asked: start afresh anyway
		}
		if !p.Paired {
			return
		}
		l := s.botLang(ctx)
		card := feishubot.NewCard(i18n.T(l, "assistant-feishu.paired"), feishubot.Green, i18n.T(l, "assistant-feishu.paired")).Markdown(i18n.T(l, "assistant-feishu.paired_body") + botHelp(l, st.Features))
		if _, err := s.botPost(ctx, "", card, ""); err != nil {
			s.logf("bots: paired note: %v", err)
		}
	}()
}

// takeLegacyBotRuns are the runs an older hub kept in the chat's state, taken out of it (once).
func (s *Service) takeLegacyBotRuns(ctx context.Context) []*channel.Run {
	b := &s.fbot
	b.mu.Lock()
	defer b.mu.Unlock()
	s.loadBotLocked(ctx)
	old := b.st.Runs
	if len(old) == 0 {
		return nil
	}
	b.st.Runs = nil
	s.saveBotStateLocked()
	var out []*channel.Run
	for _, r := range old {
		if r != nil && r.Target.SessionID != "" {
			out = append(out, r.channelRun())
		}
	}
	return out
}

// botRun is a run as bots.feishu.state kept it before the channels' pipeline: read once and moved over.
type botRun struct {
	ID        string      `json:"id"`
	MessageID string      `json:"message_id"`
	Target    botTarget   `json:"target"`
	Label     string      `json:"label"`
	Base      int         `json:"base"`
	StartedAt time.Time   `json:"started_at"`
	Card      string      `json:"card,omitempty"`
	Reaction  string      `json:"reaction,omitempty"`
	Seen      bool        `json:"seen,omitempty"`
	Held      bool        `json:"held,omitempty"`
	Probe     string      `json:"probe,omitempty"`
	Folded    []botFolded `json:"folded,omitempty"`
}

type botFolded struct {
	MessageID string `json:"message_id"`
	Reaction  string `json:"reaction,omitempty"`
}

// channelRun is a legacy run as the pipeline's.
func (r *botRun) channelRun() *channel.Run {
	out := &channel.Run{ID: r.ID, Channel: channel.Feishu, ReplyTo: r.MessageID, MachineID: r.Target.MachineID, SessionID: r.Target.SessionID,
		TargetKind: r.Target.Kind, Label: r.Label, Base: r.Base, Probe: r.Probe, Started: r.StartedAt, Seen: r.Seen, Held: r.Held,
		Ext: map[string]string{}}
	if r.MessageID != "" && !r.Held {
		out.MsgIDs = []string{channel.ClientMsgID(channel.Feishu, r.MessageID)}
	}
	if r.Card != "" {
		out.Ext["card"] = r.Card
	}
	if r.Reaction != "" {
		out.Ext["reaction"] = r.Reaction
	}
	for _, f := range r.Folded {
		fo := channel.Folded{ReplyTo: f.MessageID}
		if f.Reaction != "" {
			fo.Ext = map[string]string{"reaction": f.Reaction}
		}
		out.Folded = append(out.Folded, fo)
	}
	return out
}

// ---- what the hub shows of the chat ----

// FeishuView is feishu.view: where the chat's messages go now, and the agents bound to its threads.
func (s *Service) FeishuView(ctx context.Context) FeishuView {
	b := &s.fbot
	b.mu.Lock()
	s.loadBotLocked(ctx)
	cur := b.st.Current
	var v FeishuView
	for _, t := range b.st.Threads {
		if t.Target.agent() {
			v.Agents = append(v.Agents, FeishuAgent{Title: t.Title, MachineID: t.Target.MachineID, SessionID: t.Target.SessionID})
		}
	}
	b.mu.Unlock()
	v.Target = s.targetLabel(ctx, cur)
	return v
}

// feishuChannelTarget is the Feishu channel's target as the channels' page shows it.
func (s *Service) feishuChannelTarget(ctx context.Context) channel.Target {
	b := &s.fbot
	b.mu.Lock()
	s.loadBotLocked(ctx)
	cur := b.st.Current
	b.mu.Unlock()
	t := channel.Target{Kind: channel.TargetMain}
	switch cur.Kind {
	case targetPersona:
		t = channel.Target{Kind: channel.TargetConversation, MachineID: cur.MachineID, SessionID: cur.SessionID}
		if cur.SessionID == "" {
			t = channel.Target{Kind: channel.TargetNew}
		}
	case targetAgent:
		t = channel.Target{Kind: channel.TargetAgent, MachineID: cur.MachineID, SessionID: cur.SessionID}
	}
	if t.Kind != channel.TargetMain {
		t.Title = s.targetLabel(ctx, cur)
	}
	return t
}

// updateFeishuChannel maps the channel's settings onto the bot's: its switch and features are the hub's
// (feishu.configure), where its messages go is the chat's.
func (s *Service) updateFeishuChannel(ctx context.Context, enabled *bool, target *channel.Target, features map[string]bool) error {
	if enabled != nil || len(features) > 0 {
		p := map[string]any{}
		if enabled != nil {
			p["enabled"] = *enabled
		}
		if len(features) > 0 {
			p["features"] = features
		}
		if err := s.appCall(ctx, AppFeishu, FeishuConfigure, p, nil); err != nil {
			return err
		}
		s.feishuForgetStatus()
	}
	if target != nil {
		t := botTarget{Kind: targetMain}
		switch target.Kind {
		case channel.TargetNew:
			t = botTarget{Kind: targetPersona}
		case channel.TargetConversation:
			t = botTarget{Kind: targetPersona, MachineID: target.MachineID, SessionID: target.SessionID}
		}
		s.setBotCurrent(t)
	}
	return nil
}

func (s *Service) feishuCardsWaiting() int {
	b := &s.fbot
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.cards)
}
