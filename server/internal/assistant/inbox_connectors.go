package assistant

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
)

// The inbox items of connectors (GitHub…): their things become items with one generic block the apps draw without
// knowing the source (docs/plans/plan-connectors.md "1 · 5. Inbox"). Feishu and mail keep their own blocks.

// InboxConnector is an item's thread in a connector, and how it is answered.
type InboxConnector struct {
	Thread   string `json:"thread"`
	Label    string `json:"label,omitempty"`    // the row's tag: "Review requested"
	URL      string `json:"url,omitempty"`      // the thread on the web, opened in the app
	OpenURL  string `json:"open_url,omitempty"` // a link the system opens (sms:…), not the app
	ReplyTo  string `json:"reply_to,omitempty"` // where a reply goes and who sees it
	CanReply bool   `json:"can_reply,omitempty"`
	// RuleTier is the connector's tier for triage without a model.
	RuleTier string `json:"rule_tier,omitempty"`
	// Tags are what the thread is about (its repository, chat, sender…): what it can be muted by.
	Tags []connector.Tag `json:"tags,omitempty"`
}

// settleConnector files the connector's open items its rules now leave out (made under older rules, a muted
// source…) as done, with why; nothing is deleted. The hub asks the connector (inbox.settle).
func (s *Service) settleConnector(ctx context.Context, id string) []*InboxItem {
	b := &s.inbox
	b.mu.Lock()
	var threads []string
	for _, it := range b.items {
		if it.Source == id && it.Connector != nil && it.open() {
			threads = append(threads, it.Connector.Thread)
		}
	}
	b.mu.Unlock()
	if len(threads) == 0 {
		return nil
	}
	var res struct {
		Over map[string]string `json:"over"`
	}
	if err := s.appCall(ctx, AppInbox, inboxSettle, map[string]any{"source": id, "threads": threads}, &res); err != nil {
		s.logf("inbox: %s: settle: %v", id, err)
		return nil
	}
	if len(res.Over) == 0 {
		return nil
	}
	now := time.Now()
	var changed []*InboxItem
	b.mu.Lock()
	defer b.mu.Unlock()
	defer b.settleRound()
	for _, it := range b.items {
		if it.Source != id || it.Connector == nil || !it.open() {
			continue
		}
		if why, ok := res.Over[it.Connector.Thread]; ok {
			b.roundWrite(it)
			b.file(it, now, "read")
			it.Reason = why
			changed = append(changed, it)
		}
	}
	return changed
}

// applyConnector turns a connector's things into items, under b.mu: one open item per thread gathers its later
// moves until it is done; a move after that starts a new item; a thing that is over closes its open item.
func (b *inboxBook) applyConnector(id, name string, things []connector.InboxThing, now time.Time) []*InboxItem {
	var changed []*InboxItem
	index := b.threads(func(it *InboxItem) (string, bool) {
		if it.Source != id || it.Connector == nil {
			return "", false
		}
		return it.Connector.Thread, true
	})
	for _, t := range things {
		open, done := index.openFor(t.Thread)
		if t.Done != "" {
			if open != nil && b.settleThing(open, t, now) {
				changed = append(changed, open)
			}
			continue
		}
		if open != nil {
			// A thing older than the item's newest move is an earlier move read again (a connector restarted, or
			// several notifications of one thread): not a new move, and it never winds the item back.
			if open.Last != t.Last && (t.At.IsZero() || !t.At.Before(open.UpdatedAt)) {
				prev := seenOf(open)
				b.roundWrite(open)
				open.Count++
				fillConnectorItem(open, t)
				b.noteReceived(open, prev, nil)
				if t.Tier != "" {
					b.noteTriaged(open)
				}
				open.Read = false
				b.offerParent(open, thingParent(t))
				changed = append(changed, open)
			} else if t.Tier != "" && (open.Tier != string(t.Tier) || open.Reason != t.Why) {
				// The rules decide this one now (an item tiered before, or under other options).
				b.roundWrite(open)
				setRuleTier(open, t)
				b.noteTriaged(open)
				changed = append(changed, open)
			}
			// Read elsewhere counts only for the item's newest move: an older thing read (a connector restarted after it
			// moved the item to a later step it was not notified of) says nothing of whether that step was seen.
			if t.Read && !open.Read && (t.At.IsZero() || !t.At.Before(open.UpdatedAt)) {
				b.roundWrite(open)
				open.Read = true
				if open.Tier != "" && open.Tier != TierAction { // untriaged ones are settled by triage
					b.file(open, now, "read")
				}
				if !containsItem(changed, open) {
					changed = append(changed, open)
				}
			}
			continue
		}
		// Nothing open: only a move not yet seen, unread, recent, starts an item.
		if t.Read || now.Sub(t.At) > inboxWindow || done != nil && (done.Last == t.Last || !t.At.After(done.UpdatedAt)) {
			continue
		}
		sum := sha1.Sum([]byte(t.Thread + "\n" + t.Last))
		itemID := id + ":" + hex.EncodeToString(sum[:8])
		if b.has(itemID) {
			continue // this move started an item already (one handled since): adding it again would replace that item
		}
		it := &InboxItem{ID: itemID, Source: id, Account: id, AccountName: name, StartedAt: t.At, Count: 1}
		fillConnectorItem(it, t)
		b.noteReceived(it, inboxSeen{}, nil)
		if t.Tier != "" {
			b.noteTriaged(it)
		}
		b.offerParent(it, thingParent(t))
		b.add(id, it)
		b.roundNew(it)
		index.note(it)
		changed = append(changed, it)
	}
	return changed
}

// settleThing ends an open item a thing says is over: the user's own reply in the source is recorded as such (and the
// processing conversation, if one works on it, is told), anything else as filed. Under b.mu.
// A "replied" that is the reply Persona just sent for a confirmed suggestion is not the user's: the event stays open,
// waiting for the other side (false: nothing changed).
func (b *inboxBook) settleThing(open *InboxItem, t connector.InboxThing, now time.Time) bool {
	if t.Done == connector.DoneReplied && ownSend(open, t.Last) {
		return false
	}
	b.roundWrite(open)
	if t.Done == connector.DoneReplied {
		b.noteSelfReplied(open, now)
		open.done(now, t.Done)
		b.closeHandling(open, "self_replied", now) // told on the AI twin's next pass (deliverPending)
	} else {
		b.file(open, now, t.Done)
		b.closeHandling(open, t.Done, now)
	}
	if t.Why != "" {
		open.Reason = t.Why
	}
	return true
}

func fillConnectorItem(it *InboxItem, t connector.InboxThing) {
	it.Kind, it.Title, it.Sender, it.Preview, it.Context = t.Kind, t.Title, t.Sender, t.Preview, t.Context
	it.UpdatedAt, it.Last, it.Rules = t.At, t.Last, t.Rules
	rule := t.Tier
	if rule == "" && t.Extra != nil && connector.Tier(t.Extra.RuleTier).Known() {
		rule = connector.Tier(t.Extra.RuleTier) // its fallback (capability fallback): the model still sorts it first
	}
	it.Connector = &InboxConnector{Thread: t.Thread, Label: t.Label, URL: t.URL, OpenURL: t.OpenURL, ReplyTo: t.ReplyTo, CanReply: t.CanReply, RuleTier: string(rule), Tags: t.Tags}
	if t.Tier != "" {
		setRuleTier(it, t)
	}
}

// setRuleTier gives an item the tier its connector's rules decided: triage leaves it alone (it is triaged at this
// move), the reason is the rule.
func setRuleTier(it *InboxItem, t connector.InboxThing) {
	it.Tier, it.Reason, it.TriagedAt = string(t.Tier), t.Why, it.Last
	if it.Connector != nil {
		it.Connector.RuleTier = string(t.Tier)
	}
}

func containsItem(list []*InboxItem, it *InboxItem) bool {
	for _, x := range list {
		if x == it {
			return true
		}
	}
	return false
}

// replyConnectorItem sends the user's reply to a connector's item, to its thread through the connector (the hub's);
// the same text on an item already replied to went out already.
func (s *Service) replyConnectorItem(ctx context.Context, r inboxReplyReq) (already bool, err error) {
	c := r.item
	b := &s.inbox
	b.mu.Lock()
	sent := r.once.repeated(r.live, r.text)
	b.mu.Unlock()
	if sent {
		return true, nil
	}
	if !c.Connector.CanReply {
		return false, errCannotReply
	}
	if err = s.connectorReply(ctx, c.Source, connector.InboxReply{Thread: c.Connector.Thread, ItemID: c.ID, Last: c.Last, Text: r.text}, r.sendID); err != nil {
		return false, err
	}
	r.once.clientID = r.text
	return false, nil
}
