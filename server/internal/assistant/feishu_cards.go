package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/feishubot"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/transcript"
)

// Cards for what Persona and the agents ask (the bot's cards switch): a question, a plan, a permission or a terminal
// dialog shows in the chat as a card with the same choices as the app's card; answering it in either place answers it
// (interactions.resolve), and the card in Feishu then says how it was answered (interaction.ended). Cards go out for
// what the chat drives (its target, and the conversations of its runs), or for everything with all_cards. The card
// buttons, the chat's commands, and the pushes and sync mirror are in feishu_card_actions.go, feishu_commands.go and
// feishu_push.go.

const (
	maxFormQuestions     = 6     // more questions than this are answered in the app (keeps the form under ~200 components)
	maxFormOptions       = 20    // options shown per question
	maxQuestionRunes     = 600   // runes of one question's text
	maxOptionDescRunes   = 150   // runes of one option's description
	maxOptionDescBudget  = 800   // runes of all option descriptions of one question
	maxDialogRunes       = 2000  // runes of a screen dialog's detail and question
	maxDialogOptions     = 8     // option buttons of a screen dialog
	maxScreenRunes       = 2500  // runes of the terminal screen kept
	maxScreenLineRunes   = 300   // runes per terminal line
	maxPermissionReason  = 1000  // runes of a permission's reason
	maxPlanRunes         = 5000  // runes of a plan shown on its card (clipPlan)
	maxAnsweredQuestions = 3     // questions shown on an answered card
	maxAnsweredQRunes    = 150   // runes per question on an answered card
	maxCardJSONBytes     = 24000 // serialized size above which safeCard falls back (Feishu rejects ~30 KB)
)

// feishuInteraction: a new card (interaction.announced).
func (s *Service) feishuInteraction(ix proto.Interaction) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if !s.feishuCardWanted(ctx, ix) {
		return
	}
	s.postInteractionCard(ctx, ix)
}

// feishuCardWanted: the bot posts a card for ix (feishuInteraction): it is on with the cards switch, and the chat
// drives the conversation or "all cards" is on (not for a health conversation's).
func (s *Service) feishuCardWanted(ctx context.Context, ix proto.Interaction) bool {
	f, ready := s.feishuFeatures(ctx)
	if !ready || !f.Cards {
		return false
	}
	// "All cards" does not take a health conversation's: only one the chat drives itself (health.go).
	if drives := s.botDrives(ctx, ix.MachineID, ix.SessionID); !drives && (!f.AllCards || s.ixInHealth(ctx, ix)) {
		return false
	}
	return true
}

// feishuInteractionAlways posts ix's card whatever the cards switches say: a task's decision the report put on top
// (reports.go). Answering it goes as any card's (the hub checks the owner pressed it).
func (s *Service) feishuInteractionAlways(ix proto.Interaction) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s.postInteractionCard(ctx, ix)
}

// botCardRecheck is how long after a reconnection the cards that may still be waiting are looked at again (the
// manager restores its interactions meanwhile).
var botCardRecheck = 90 * time.Second

type cardEnd int

const (
	cardWaiting cardEnd = iota // still up
	cardOver                   // ended for good
	cardUnsure                 // not up, not known to be over (not restored yet, the history could not say)
)

// botCardOver is whether interaction id is over for good, as the hub's queue and its history say (InteractionsGet).
func (s *Service) botCardOver(ctx context.Context, id string) cardEnd {
	_, end := s.botCardLook(ctx, id)
	return end
}

// botCardLook is botCardOver with the interaction as the hub's queue has it, while it is up.
func (s *Service) botCardLook(ctx context.Context, id string) (proto.Interaction, cardEnd) {
	var got struct {
		OK          bool              `json:"ok"`
		Status      string            `json:"status"`
		Interaction proto.Interaction `json:"interaction"`
	}
	switch err := s.appCall(ctx, AppInteractions, InteractionsGet, map[string]any{"interaction_id": id}, &got); {
	case err != nil:
		return proto.Interaction{}, cardUnsure
	case got.OK:
		return got.Interaction, cardWaiting
	case finalStatus(got.Status):
		return proto.Interaction{}, cardOver
	}
	return proto.Interaction{}, cardUnsure
}

// finalStatus: how an interaction ended for good (InteractionsGet's status); "pending" (in the history, not up yet)
// and "" (no record, or the history could not be read) are not.
func finalStatus(status string) bool {
	switch status {
	case "resolved", "dismissed", "expired", "cancelled":
		return true
	}
	return false
}

// postInteractionCard posts ix's card once, unless it ended meanwhile.
func (s *Service) postInteractionCard(ctx context.Context, ix proto.Interaction) {
	if s.interactionEnded(ix.ID) { // ended while this looked at the bot: not asked (s.chans before s.fbot, never in it)
		return
	}
	b := &s.fbot
	b.mu.Lock()
	s.loadBotLocked(ctx)
	if _, ok := b.cards[ix.ID]; ok {
		b.mu.Unlock()
		return
	}
	s.keepBotCardLocked(ix.ID, &botCard{}) // claimed: a second announce of the same card does not post twice
	b.mu.Unlock()
	reply, to, topic := s.interactionPlace(ix)
	fallback := ""
	switch ix.Kind {
	case proto.IxQuestion:
		fallback = "❓ " + clip(firstQuestion(ix), 100)
	case proto.IxPermission:
		fallback = "🔐 " + toolDisplayName(ix.ToolName)
	case proto.IxPlan:
		fallback = "📋 " + clip(firstLine(ix.Plan), 100)
	case proto.IxScreen:
		fallback = "🖥 " + i18n.T(s.botLang(ctx), "assistant-feishu_cards.screen_summary")
	}
	card := s.interactionCard(ctx, ix)
	// A card for a topic is a reply to its run's message and belongs under it: if that message is gone it would be a
	// stray card in the group (the hub would send it into the chat), and the owner may not see it there. It goes to the
	// owner instead, as with a chat that refused it.
	to.NoDetour = to.ChatID != ""
	mid, err := s.botPostTo(ctx, to, reply, card, fallback, "")
	if err != nil && to.ChatID != "" && (chatRefused(err) || feishubot.MessageGone(err)) {
		// The topic was refused outright (the group was taken off the owner's list, the bot is not in it, it was
		// dissolved) or the message to reply under is gone: nothing went out there, and the owner's DM gets the card
		// instead, as a message of its own (a reply to the topic's message would land in the topic). Any other failure
		// (a timeout, a gateway error) may have gone out, and a second card would show the same question twice: it is
		// only logged.
		s.logf("bots: card for %s refused in the topic: %v; sending it to the owner", ix.ID, err)
		reply, to = "", feishuTarget{}
		mid, err = s.botPostTo(ctx, to, "", card, fallback, "")
	}
	ended := s.interactionEnded(ix.ID)
	b.mu.Lock()
	if err != nil {
		s.logf("bots: card for %s: %v", ix.ID, err)
		s.keepBotCardLocked(ix.ID, nil)
		b.mu.Unlock()
		return
	}
	if _, still := b.cards[ix.ID]; !still || ended { // answered while it was being posted
		s.keepBotCardLocked(ix.ID, nil)
		b.mu.Unlock()
		s.botCardDone(ctx, mid, ix, i18n.T(s.botLang(ctx), "assistant-feishu_cards.handled"))
		return
	}
	s.keepBotCardLocked(ix.ID, &botCard{MessageID: mid, Ix: ix, At: b.clock(), Thread: reply, ChatID: to.ChatID})
	b.mu.Unlock()
	if topic != nil {
		// The run was asked in a topic and the card went to the DM: the topic is told where it is.
		s.botNoteTo(ctx, topic.to, topic.reply, i18n.T(s.botLang(ctx), "assistant-feishu_cards.sent_to_dm"))
	}
}

// chatRefused: the hub or Feishu refused the send into a chat outright, so nothing went out: the hub's own refusal
// (a code on the bus) or Feishu's "the bot is not in the chat" / "the chat was dissolved".
func chatRefused(err error) bool {
	var re *rpc.Error
	if errors.As(err, &re) && re.Code == CodeChatDenied {
		return true
	}
	return feishubot.ChatRefused(err)
}

// interactionPlace is where the card of ix goes: the message of the run it answers to reply to, and the topic if that
// run was asked in one. A conversation can have a private chat's run and a topic's at once (they are not folded into
// one); then the owner's DM wins, where only the owner sees the card (an approval or a plan must not be shown to a
// group), and of several runs of one place the one asked last decides.
func (s *Service) interactionPlace(ix proto.Interaction) (reply string, to feishuTarget, note *topicNote) {
	private, topic := s.interactionRuns(ix)
	switch {
	case private != nil && topic != nil:
		// The card is not in the topic that asked: a short line there says it went to the DM.
		return private.ReplyTo, feishuTarget{}, &topicNote{reply: topic.ReplyTo, to: runTo(s.runExts(topic, "chat", "thread"))}
	case private != nil:
		return private.ReplyTo, feishuTarget{}, nil
	case topic != nil:
		return topic.ReplyTo, runTo(s.runExts(topic, "chat", "thread")), nil
	}
	return "", feishuTarget{}, nil
}

// interactionRuns are the runs that decide where ix's card goes: the latest asked in the owner's DM and the latest
// asked in a topic, of the conversation it belongs to (either may be nil).
func (s *Service) interactionRuns(ix proto.Interaction) (private, topic *channel.Run) {
	for _, r := range s.channelRuns(channel.Feishu) {
		if r.MachineID != ix.MachineID || r.SessionID != ix.SessionID {
			continue
		}
		if runTo(s.runExts(&r, "chat", "thread")).ChatID == "" {
			if private == nil || !r.Started.Before(private.Started) {
				private = &r
			}
		} else if topic == nil || !r.Started.Before(topic.Started) {
			topic = &r
		}
	}
	return private, topic
}

// topicNote is where a line goes that tells a topic its run's card went to the DM: the run's message, in its topic.
type topicNote struct {
	reply string
	to    feishuTarget
}

// botDrives: the chat drives this conversation (its target now, a run on it, or a thread bound to it).
func (s *Service) botDrives(ctx context.Context, machine, session string) bool {
	for _, r := range s.channelRuns(channel.Feishu) {
		if r.MachineID == machine && r.SessionID == session {
			return true
		}
		// The hub's own approvals (mail_send) do not name the conversation: one on the assistant's machine while a
		// Persona run goes on is most likely that run's.
		if session == "" && r.TargetKind != targetAgent && r.MachineID == machine {
			return true
		}
	}
	b := &s.fbot
	b.mu.Lock()
	s.loadBotLocked(ctx)
	cur := b.st.Current
	for _, t := range b.st.Threads {
		if t.Target.MachineID == machine && t.Target.SessionID == session && b.clock().Sub(t.At) < 24*time.Hour {
			b.mu.Unlock()
			return true
		}
	}
	b.mu.Unlock()
	if session == "" {
		return false
	}
	if cur.Kind == targetMain {
		return s.isMainOn(ctx, machine, session)
	}
	return cur.MachineID == machine && cur.SessionID == session
}

// feishuInteractionDone: a card is over (answered anywhere, withdrawn, expired): its card in Feishu says so.
func (s *Service) feishuInteractionDone(ix proto.Interaction, how string) {
	s.feishuInteractionEnded(ix, how, false)
}

// feishuInteractionEnded is feishuInteractionDone; inChat: the card was taken back because the user wrote in the
// conversation, so their next words are no late answer to it.
func (s *Service) feishuInteractionEnded(ix proto.Interaction, how string, inChat bool) {
	b := &s.fbot
	b.mu.Lock()
	s.loadBotLocked(context.Background()) // a card posted before a restart is in the chat's state
	c, ok := b.cards[ix.ID]
	s.keepBotCardLocked(ix.ID, nil)
	if ok && typedAnswerable(c) && !inChat {
		b.noteEndedLocked(c)
	}
	b.mu.Unlock()
	if !ok || c.MessageID == "" {
		return
	}
	if ix.Kind == "" { // the catch-up knows only what was kept of it
		ix = c.Ix
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		s.botCardDone(ctx, c.MessageID, ix, how)
	}()
}

// detachedCard is a Feishu card closed because its node's connection ended (cancelMachineInteractions): the same card
// read again once the node is back opens it again (feishuCardBack). closed is shut once the close went out, so the
// reopening lands after it.
type detachedCard struct {
	card   botCard
	at     time.Time
	closed chan struct{}
}

// feishuDetach: the cards ids are about to end because their node went away. Their Feishu cards say "Ended" as any
// ended card's, and are kept for resumeGrace in case the same card comes back (#4583).
func (s *Service) feishuDetach(ids []string) {
	b := &s.fbot
	b.mu.Lock()
	now := b.clock()
	for id, d := range b.detached {
		if now.Sub(d.at) > resumeGrace {
			delete(b.detached, id)
		}
	}
	var closing []detachedCard
	for _, id := range ids {
		c, ok := b.cards[id]
		if !ok || c.MessageID == "" {
			continue
		}
		s.keepBotCardLocked(id, nil) // closed here, not again by feishuInteractionDone
		if b.detached == nil {
			b.detached = map[string]detachedCard{}
		}
		d := detachedCard{card: c, at: now, closed: make(chan struct{})}
		b.detached[id] = d
		closing = append(closing, d)
	}
	b.mu.Unlock()
	for _, d := range closing {
		go func() {
			defer close(d.closed)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			s.botCardDone(ctx, d.card.MessageID, d.card.Ix, i18n.T(s.botLang(ctx), "assistant-channels.ended"))
		}()
	}
}

// feishuCardBack: ix is a card back on the apps after its node reconnected (shown, not announced). Its Feishu card,
// closed when the node went away, asks it again, in the same message and without a new one. A card that did not come
// back (answered, moved on, away past resumeGrace) stays closed.
func (s *Service) feishuCardBack(ix proto.Interaction) {
	b := &s.fbot
	b.mu.Lock()
	d, ok := b.detached[ix.ID]
	delete(b.detached, ix.ID)
	_, posted := b.cards[ix.ID]
	if !ok || posted || b.clock().Sub(d.at) > resumeGrace {
		b.mu.Unlock()
		return
	}
	s.keepBotCardLocked(ix.ID, &botCard{MessageID: d.card.MessageID, Thread: d.card.Thread, ChatID: d.card.ChatID, Ix: ix, At: b.clock(),
		NudgedAt: d.card.NudgedAt, SkippedAt: d.card.SkippedAt, Tries: d.card.Tries, TriedAt: d.card.TriedAt})
	b.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	select {
	case <-d.closed:
	case <-ctx.Done():
		return
	}
	if err := s.botUpdateCard(ctx, d.card.MessageID, s.interactionCard(ctx, ix)); err != nil {
		s.logf("bots: reopen card %s: %v", ix.ID, err)
		b.mu.Lock()
		if c, still := b.cards[ix.ID]; still && c.MessageID == d.card.MessageID {
			s.keepBotCardLocked(ix.ID, nil)
		}
		b.mu.Unlock()
		return
	}
	b.mu.Lock()
	_, still := b.cards[ix.ID]
	b.mu.Unlock()
	if _, pending := s.pendingIx(ix.ID); !still || !pending { // answered or ended meanwhile: it says so again
		b.mu.Lock()
		s.keepBotCardLocked(ix.ID, nil)
		b.mu.Unlock()
		s.botCardDone(ctx, d.card.MessageID, ix, i18n.T(s.botLang(ctx), "assistant-feishu_cards.handled"))
	}
}

func (s *Service) botCardDone(ctx context.Context, messageID string, ix proto.Interaction, how string) {
	if err := s.botUpdateCard(ctx, messageID, s.answeredCard(ctx, ix, how)); err != nil {
		s.logf("bots: close card %s: %v", ix.ID, err)
		s.botNote(ctx, messageID, "✓ "+how)
	}
}

// endedText says in l why a card is no longer pending, from interactions.get's status: it expired, or it was
// withdrawn (cancelled); otherwise (answered, dismissed, unknown) it is the text of fallback, a key.
func endedText(l i18n.Lang, status, fallback string) string {
	switch status {
	case "expired":
		return i18n.T(l, "assistant-feishu_cards.card_expired")
	case "cancelled":
		return i18n.T(l, "assistant-feishu_cards.card_withdrawn")
	}
	return i18n.T(l, fallback)
}

// resolutionText says in l how an interaction was answered, for its closed card.
func resolutionText(l i18n.Lang, ix proto.Interaction, r proto.Resolution) string {
	switch {
	case r.Dismiss && r.Reason == proto.DismissedInChat && r.AnsweredBy == "":
		return i18n.T(l, "assistant-feishu_cards.replied_in_chat")
	case r.Dismiss && r.Reason == proto.DismissedExpired && r.AnsweredBy == "":
		return i18n.T(l, "assistant-feishu_cards.moved_to_todos")
	case r.Dismiss:
		return i18n.T(l, "assistant-feishu_cards.dismissed")
	case r.Approve != nil && *r.Approve:
		switch r.Mode {
		case "bypassPermissions":
			return i18n.T(l, "assistant-feishu_cards.approved_run")
		case "acceptEdits":
			return i18n.T(l, "assistant-feishu_cards.approved_edits")
		}
		return i18n.T(l, "assistant-feishu_cards.approved")
	case r.Approve != nil:
		if r.Feedback != "" {
			return i18n.T(l, "assistant-feishu_cards.sent_back_note", i18n.Args{"note": clip(r.Feedback, 200)})
		}
		return i18n.T(l, "assistant-feishu_cards.sent_back")
	case r.Allow != nil && *r.Allow:
		if r.Always {
			return i18n.T(l, "assistant-feishu_cards.allowed_always")
		}
		return i18n.T(l, "assistant-feishu_cards.allowed")
	case r.Allow != nil:
		if r.Message != "" {
			return i18n.T(l, "assistant-feishu_cards.denied_note", i18n.Args{"note": clip(r.Message, 200)})
		}
		return i18n.T(l, "assistant-feishu_cards.denied")
	case len(r.Answers) > 0:
		var parts []string
		for _, q := range ix.Questions {
			if a, ok := r.Answers[q.Question]; ok {
				parts = append(parts, a)
			}
		}
		if len(parts) == 0 {
			for _, a := range r.Answers {
				parts = append(parts, a)
			}
		}
		return i18n.T(l, "assistant-feishu_cards.answered_with", i18n.Args{"answers": clip(strings.Join(parts, i18n.T(l, "assistant-feishu_cards.answer_sep")), 300)})
	case r.Option != "" && ix.Dialog != nil:
		if o := ix.Dialog.Option(r.Option); o != nil {
			return i18n.T(l, "assistant-feishu_cards.picked", i18n.Args{"option": o.Label})
		}
	case len(r.Keys) > 0 || r.Text != "":
		return i18n.T(l, "assistant-feishu_cards.responded")
	}
	return i18n.T(l, "assistant-feishu_cards.handled")
}

// interactionCard is the card asking ix, with the app's choices.
func (s *Service) interactionCard(ctx context.Context, ix proto.Interaction) *feishubot.Card {
	l := s.botLang(ctx)
	where := feishubot.EscapeMarkdown(s.ixWhere(ctx, ix))
	val := func(extra map[string]any) map[string]any {
		v := map[string]any{"a": "ix", "id": ix.ID}
		for k, x := range extra {
			v[k] = x
		}
		return v
	}
	open := s.openButton(ctx, ix.MachineID, ix.SessionID)
	switch ix.Kind {
	case proto.IxQuestion:
		c := feishubot.NewCard("❓ "+i18n.T(l, "assistant-feishu_cards.question_title", i18n.Args{"where": s.ixWhere(ctx, ix)}), feishubot.Orange, i18n.T(l, "assistant-feishu_cards.question_summary", i18n.Args{"question": firstQuestion(ix)}))
		if len(ix.Questions) == 1 && !ix.Questions[0].MultiSelect && len(ix.Questions[0].Options) <= 4 {
			q := ix.Questions[0]
			c.Markdown(questionText(l, q))
			var bs []feishubot.Button
			for _, o := range q.Options {
				bs = append(bs, feishubot.Btn(clip(o.Label, 30), feishubot.Default, val(map[string]any{"q": "0", "v": o.Label})))
			}
			c.Buttons(bs...)
			c.Form("other", []any{feishubot.Input("text", "", i18n.T(l, "assistant-feishu_cards.or_write_answer"), false)},
				feishubot.Submit(i18n.T(l, "assistant-feishu_cards.answer"), feishubot.Primary, "q_submit", val(map[string]any{"q": "0", "free": "1"})))
			if len(q.Options) > 0 {
				c.Note(i18n.T(l, "assistant-feishu_cards.answer_by_text"))
			}
		} else if len(ix.Questions) > maxFormQuestions {
			// The submit handler needs an answer to every question, so a form clipped to the first few could never be
			// sent: that many questions are answered in the app.
			c.Note(i18n.T(l, "assistant-feishu_cards.questions_in_app", i18n.Args{"count": len(ix.Questions)}))
		} else {
			var fields []any
			for i, q := range ix.Questions {
				fields = append(fields, feishubot.MarkdownBlock(questionText(l, q)))
				var opts []feishubot.Option
				for j, o := range q.Options {
					if j >= maxFormOptions {
						break
					}
					opts = append(opts, feishubot.Option{Label: clip(o.Label, 60), Value: o.Label})
				}
				if len(opts) > 0 {
					fields = append(fields, feishubot.Select("q"+strconv.Itoa(i), map[bool]string{false: i18n.T(l, "assistant-feishu_cards.pick_one"), true: i18n.T(l, "assistant-feishu_cards.pick_some")}[q.MultiSelect], opts, q.MultiSelect))
				}
				fields = append(fields, feishubot.Input("t"+strconv.Itoa(i), "", i18n.T(l, "assistant-feishu_cards.or_write"), false))
			}
			c.Form("answers", fields, feishubot.Submit(i18n.T(l, "assistant-feishu_cards.submit"), feishubot.Primary, "qs_submit", val(map[string]any{"form": "1"})))
			// A form of one question with options also takes a plain reply; a form of several is submitted as one (a
			// reply is not split over its questions), and a free-text question has no options.
			if len(ix.Questions) == 1 && len(ix.Questions[0].Options) > 0 {
				c.Note(i18n.T(l, "assistant-feishu_cards.answer_by_text"))
			}
		}
		return safeCard(l, c.Buttons(open, feishubot.Btn(i18n.T(l, "assistant-feishu_cards.dismiss"), feishubot.Default, val(map[string]any{"dismiss": "1"}))), open)
	case proto.IxPlan:
		c := feishubot.NewCard("📋 "+i18n.T(l, "assistant-feishu_cards.plan_title", i18n.Args{"where": s.ixWhere(ctx, ix)}), feishubot.Blue, i18n.T(l, "assistant-feishu_cards.plan_summary"))
		c.Markdown(feishubot.CardMarkdown(clipPlan(ix.Plan, i18n.T(l, "assistant-feishu_cards.plan_clipped"))))
		c.Buttons(
			feishubot.Btn(i18n.T(l, "assistant-feishu_cards.approve_run"), feishubot.Primary, val(map[string]any{"approve": "1", "mode": "bypassPermissions"})),
			feishubot.Btn(i18n.T(l, "assistant-feishu_cards.approve_edits"), feishubot.Default, val(map[string]any{"approve": "1", "mode": "acceptEdits"})),
		)
		c.Form("reject", []any{feishubot.Input("feedback", "", i18n.T(l, "assistant-feishu_cards.feedback_placeholder"), true)},
			feishubot.Submit(i18n.T(l, "assistant-feishu_cards.send_back"), feishubot.Danger, "plan_reject", val(map[string]any{"approve": "0"})))
		return safeCard(l, c.Buttons(open), open)
	case proto.IxPermission:
		c := feishubot.NewCard("🔐 "+i18n.T(l, "assistant-feishu_cards.permission_title", i18n.Args{"where": s.ixWhere(ctx, ix)}), feishubot.Orange, i18n.T(l, "assistant-feishu_cards.permission_summary", i18n.Args{"tool": toolDisplayName(ix.ToolName)}))
		var b strings.Builder
		fmt.Fprintf(&b, "**%s**", feishubot.EscapeMarkdown(toolDisplayName(ix.ToolName)))
		if ix.Reason != "" {
			fmt.Fprintf(&b, "\n%s", feishubot.EscapeMarkdown(clipRunes(ix.Reason, maxPermissionReason)))
		}
		c.Markdown(b.String())
		if sum := strings.TrimSpace(ix.Summary); sum != "" {
			c.Markdown("```\n" + strings.ReplaceAll(clip(sum, 2500), "```", "'''") + "\n```")
		} else if in := proto.RedactArgs(ix.ToolInput); in != "" { // a credential's value never goes to the chat (#4734)
			c.Markdown("```json\n" + strings.ReplaceAll(clip(in, 2500), "```", "'''") + "\n```")
		}
		if len(ix.Fields) > 0 {
			// A password or a code is typed in the app, never in a chat.
			c.Note(i18n.T(l, "assistant-feishu_cards.fields_in_app"))
			return safeCard(l, c.Buttons(open), open)
		}
		bs := []feishubot.Button{
			feishubot.Btn(i18n.T(l, "assistant-feishu_cards.allow"), feishubot.Primary, val(map[string]any{"allow": "1"})),
			feishubot.Btn(i18n.T(l, "assistant-feishu_cards.deny"), feishubot.Danger, val(map[string]any{"allow": "0"})),
		}
		// As the app's card (InteractionViews.swift's BUIPermissionForm, web's PermissionCard): Always allow for this session for
		// Claude on a conversation, and for another agent only what it says that always allows — a runtime cannot keep
		// Claude's session rule (node/runtimes.go), so the button there would only allow once and ask again.
		label := ix.AlwaysLabel
		if label == "" && (ix.Agent == "" || ix.Agent == "claude") && ix.SessionID != "" {
			label = i18n.T(l, "assistant-feishu_cards.always_allow")
		}
		if label != "" {
			bs = append(bs, feishubot.Btn(clip(label, 20), feishubot.Default, val(map[string]any{"allow": "1", "always": "1"})))
		}
		return safeCard(l, c.Buttons(bs...).Buttons(open), open)
	case proto.IxScreen:
		c := feishubot.NewCard("🖥 "+i18n.T(l, "assistant-feishu_cards.screen_title", i18n.Args{"where": s.ixWhere(ctx, ix)}), feishubot.Orange, i18n.T(l, "assistant-feishu_cards.screen_summary"))
		if d := ix.Dialog; d != nil {
			q := d.Question
			if d.Detail != "" {
				q = d.Detail + "\n" + q
			}
			if r := []rune(q); len(r) > maxDialogRunes {
				q = string(r[:maxDialogRunes]) + i18n.T(l, "assistant-feishu_cards.clipped")
			}
			c.Markdown(feishubot.EscapeMarkdown(q))
			var bs []feishubot.Button
			for _, o := range d.Options {
				if o.Kind == "type_something" || o.Kind == "chat" {
					continue
				}
				if len(bs) >= maxDialogOptions {
					break
				}
				bs = append(bs, feishubot.Btn(clip(o.Label, 30), feishubot.Default, val(map[string]any{"opt": o.Key})))
			}
			c.Buttons(bs...)
		} else {
			screen := ix.Screen
			lines := strings.Split(screen, "\n")
			if len(lines) > 20 {
				lines = lines[len(lines)-20:]
			}
			for i, line := range lines {
				if r := []rune(line); len(r) > maxScreenLineRunes {
					lines[i] = string(r[:maxScreenLineRunes]) + "…"
				}
			}
			screen = strings.Join(lines, "\n")
			if r := []rune(screen); len(r) > maxScreenRunes {
				screen = "…\n" + string(r[len(r)-maxScreenRunes:])
			}
			c.Markdown("```\n" + strings.ReplaceAll(screen, "```", "'''") + "\n```")
			c.Buttons(
				feishubot.Btn(i18n.T(l, "assistant-feishu_cards.enter"), feishubot.Default, val(map[string]any{"keys": "enter"})),
				feishubot.Btn("Esc", feishubot.Default, val(map[string]any{"keys": "esc"})),
				feishubot.Btn("1", feishubot.Default, val(map[string]any{"keys": "1"})),
				feishubot.Btn("2", feishubot.Default, val(map[string]any{"keys": "2"})),
			)
		}
		if !ix.Secret {
			c.Form("type", []any{feishubot.Input("text", "", i18n.T(l, "assistant-feishu_cards.type_placeholder"), false)},
				feishubot.Submit(i18n.T(l, "assistant-feishu_cards.send"), feishubot.Primary, "screen_text", val(map[string]any{"typed": "1"})))
		} else {
			c.Note(i18n.T(l, "assistant-feishu_cards.secret_in_app"))
		}
		return safeCard(l, c.Buttons(open), open)
	case proto.IxElicitation:
		return safeCard(l, s.elicitationCard(ctx, ix, open), open)
	}
	return safeCard(l, feishubot.NewCard(where, feishubot.Grey, "").Buttons(open), open)
}

// clipPlan is a plan longer than maxPlanRunes cut to its first channel.Split piece, then note. The piece ends at a line
// break where it can and closes a code block it cuts into, so the note reads as text, not as the code's last line.
func clipPlan(plan, note string) string {
	if len([]rune(plan)) <= maxPlanRunes {
		return plan
	}
	head := channel.Split(plan, maxPlanRunes)[0]
	if r := []rune(head); len(r) > maxPlanRunes { // Split hands back a plan of only line breaks whole
		head = string(r[:maxPlanRunes])
	}
	return head + note
}

// safeCard returns c, or, when it would still be too big for Feishu after the clipping above, a short card that sends
// the owner to the app (with the open button, if any).
func safeCard(l i18n.Lang, c *feishubot.Card, open feishubot.Button) *feishubot.Card {
	if len(c.JSON()) <= maxCardJSONBytes {
		return c
	}
	title := ""
	if c.Header != nil {
		title = c.Header.Title.Content
	}
	fallback := feishubot.NewCard(title, feishubot.Orange, "")
	fallback.Note(i18n.T(l, "assistant-feishu_cards.too_long_in_app"))
	if len(open) > 0 {
		fallback.Buttons(open)
	}
	return fallback
}

// elicitationTarget is the link an elicitation may open and its host: an absolute http(s) URL with a host and no
// user or password (like the apps' urlTarget, web/src/lib/elicitation.ts), exactly as given; "" otherwise. A link
// such as https://github.com@evil.example/ reads as one site and opens another.
func elicitationTarget(raw string) (string, string) {
	target := strings.TrimSpace(raw)
	if !elicitationURLRe.MatchString(target) {
		return "", ""
	}
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return "", ""
	}
	return target, u.Hostname()
}

// elicitationURLRe: http(s):// and then a host at once (not https:///host or https:host, which parsers forgive).
var elicitationURLRe = lazyre.New(`(?i)^https?://[^/\\?#@]`)

// elicitationCard is an MCP server asking to authorize (a link) or to fill in a form: what it asks on the card, its
// link as a button; a form is filled in the app (its values may be secrets), so only its fields' names are shown.
func (s *Service) elicitationCard(ctx context.Context, ix proto.Interaction, open feishubot.Button) *feishubot.Card {
	l := s.botLang(ctx)
	var msg, link, host string
	var fields []string
	if e := ix.Elicitation; e != nil {
		msg = strings.TrimSpace(e.Message)
		link, host = elicitationTarget(e.URL)
		var schema struct {
			Properties map[string]struct {
				Title string `json:"title"`
			} `json:"properties"`
		}
		if json.Unmarshal(e.RequestedSchema, &schema) == nil {
			for k, p := range schema.Properties {
				if t := strings.TrimSpace(p.Title); t != "" {
					k = t
				}
				fields = append(fields, k)
			}
			slices.Sort(fields)
		}
	}
	c := feishubot.NewCard("🔑 "+i18n.T(l, "assistant-feishu_cards.elicitation_title", i18n.Args{"where": s.ixWhere(ctx, ix)}), feishubot.Orange,
		i18n.T(l, "assistant-feishu_cards.elicitation_summary", i18n.Args{"message": clip(oneLine(msg), 60)}))
	if msg != "" {
		c.Markdown(feishubot.EscapeMarkdown(clip(msg, 2000)))
	}
	if link != "" {
		// The host on its own line: the link's text could look like another site's (as the apps show it).
		c.Markdown(i18n.T(l, "assistant-feishu_cards.elicitation_host", i18n.Args{"host": feishubot.Code(host)}))
		c.Note(i18n.T(l, "assistant-feishu_cards.elicitation_confirm_in_app"))
		return c.Buttons(feishubot.LinkBtn(i18n.T(l, "assistant-feishu_cards.elicitation_open"), link), open)
	}
	if len(fields) > 0 {
		c.Note(i18n.T(l, "assistant-feishu_cards.elicitation_fields", i18n.Args{"fields": clip(strings.Join(fields, "、"), 200)}))
	}
	c.Note(i18n.T(l, "assistant-feishu_cards.elicitation_in_app"))
	return c.Buttons(open)
}

func firstQuestion(ix proto.Interaction) string {
	if len(ix.Questions) > 0 {
		return ix.Questions[0].Question
	}
	return ""
}

func questionText(l i18n.Lang, q proto.Question) string {
	s := ""
	if q.Header != "" {
		s = "**" + feishubot.EscapeMarkdown(clipRunes(q.Header, 60)) + "**\n"
	}
	s += feishubot.EscapeMarkdown(clipRunes(q.Question, maxQuestionRunes))
	descBudget := maxOptionDescBudget
	clippedOptions := 0
	for i, o := range q.Options {
		if i >= maxFormOptions {
			clippedOptions = len(q.Options) - i
			break
		}
		if o.Description != "" && descBudget > 0 {
			desc := clipRunes(o.Description, maxOptionDescRunes)
			if len([]rune(desc)) > descBudget {
				desc = clipRunes(desc, descBudget)
			}
			descBudget -= len([]rune(desc))
			s += "\n- " + feishubot.EscapeMarkdown(clip(o.Label, 40)) + "：" + feishubot.EscapeMarkdown(desc)
		}
	}
	if clippedOptions > 0 {
		s += i18n.T(l, "assistant-feishu_cards.clipped")
	}
	return s
}

// answeredCard is an interaction's card once it is over.
func (s *Service) answeredCard(ctx context.Context, ix proto.Interaction, how string) *feishubot.Card {
	title := map[string]string{proto.IxQuestion: "❓ ", proto.IxPlan: "📋 ", proto.IxPermission: "🔐 ", proto.IxScreen: "🖥 "}[ix.Kind] + s.ixWhere(ctx, ix)
	c := feishubot.NewCard(title, feishubot.Grey, how)
	switch ix.Kind {
	case proto.IxQuestion:
		qs := ix.Questions
		more := 0
		if len(qs) > maxAnsweredQuestions {
			more = len(qs) - maxAnsweredQuestions
			qs = qs[:maxAnsweredQuestions]
		}
		for _, q := range qs {
			c.Markdown(feishubot.EscapeMarkdown(clipRunes(q.Question, maxAnsweredQRunes)))
		}
		if more > 0 {
			c.Note(strings.TrimSpace(i18n.T(s.botLang(ctx), "assistant-feishu_cards.clipped")))
		}
	case proto.IxPermission:
		c.Markdown(feishubot.EscapeMarkdown(ix.ToolName + " " + clip(oneLine(ix.Summary), 200)))
	case proto.IxPlan:
		c.Markdown(feishubot.EscapeMarkdown(clip(planTitle(ix.Plan), 200)))
	}
	// open rides on the card and is handed to safeCard too, so the short fallback card keeps it when this one
	// would exceed Feishu's size budget.
	open := s.openButton(ctx, ix.MachineID, ix.SessionID)
	return safeCard(s.botLang(ctx), c.Note("✓ "+how).Buttons(open), open)
}

func (s *Service) ixWhere(ctx context.Context, ix proto.Interaction) string {
	where := transcript.Title(ix.SessionTitle)
	if where == "" {
		where = "Persona"
	}
	if d, ok := s.device(ctx, ix.MachineID); ok && where != "Persona" {
		where += " · " + d.Name
	}
	return where
}

// openButton opens the conversation in the web app the hub serves; nil (Card.Buttons leaves it out) when the hub has
// no http(s) address for it. Card links are documented for http(s) and AppLink
// only: a persona:// link (the iPhone app) might get the whole card refused, so it is not used.
func (s *Service) openButton(ctx context.Context, machine, session string) feishubot.Button {
	st, _ := s.feishuStatusKept(ctx)
	web := strings.TrimRight(st.PublicURL, "/")
	if session != "" {
		web = sessionLink(web, machine, session, s.isAssistantConversation(ctx, machine, session))
	}
	return s.openLinkButton(ctx, web)
}

// openLinkButton is the "open on web" button for a link already built (sessionLink); nil for an empty or non-http(s)
// one, so a card never carries a dead empty link (#4073).
func (s *Service) openLinkButton(ctx context.Context, web string) feishubot.Button {
	return safeLinkButton(i18n.T(s.botLang(ctx), "assistant-feishu_cards.open_web"), web)
}

// safeLinkButton is a labeled open_url button for web, nil when web is empty or not http(s) with a host: card links
// are documented for http(s) only, so a dirty address (no scheme, empty host) never becomes a dead button.
func safeLinkButton(label, web string) feishubot.Button {
	if u, err := url.Parse(web); web == "" || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil
	}
	return feishubot.LinkBtn(label, web)
}

// webBase is the hub's web app (hub.json's public_url) without the trailing slash, "" when the hub does not say.
// It only reads feishu.status (which carries the address whether the bot is on or not): unlike feishuStatus it
// does not reconcile the bot's chat state, so the snapshot does not touch it.
func (s *Service) webBase(ctx context.Context) string {
	var st FeishuStatus
	if err := s.appCall(ctx, AppFeishu, FeishuStatusGet, map[string]any{}, &st); err != nil {
		return ""
	}
	return strings.TrimRight(st.PublicURL, "/")
}

// sessionLink is a conversation's page in the web app at web (web/src/lib/router.ts): a Persona conversation is
// /persona/<id>, an agent /s/<machine>/<id>; "" when web is.
func sessionLink(web, machine, session string, assistant bool) string {
	if web == "" {
		return ""
	}
	if assistant {
		return web + "/persona/" + url.PathEscape(session)
	}
	return web + "/s/" + url.PathEscape(machine) + "/" + url.PathEscape(session)
}

// todoLink is a todo's page in the web app at web, "" when web is.
func todoLink(web, id string) string {
	if web == "" {
		return ""
	}
	return web + "/todos?id=" + url.QueryEscape(id)
}
