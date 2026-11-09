package assistant

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
)

// ---- messages about a task ----

// messageCard puts the card into the conversation that opened the task (none when the task is gone or done).
func (s *Service) messageCard(ctx context.Context, itemID string, body MessageCard) {
	t, ok, err := s.todo(ctx, body.TodoID)
	if err != nil || !ok || t.DoneAt != nil || t.Origin == "" {
		return
	}
	if _, err := s.appendFeed(ctx, t.Origin, feedMessage, itemID, body); err != nil {
		s.logf("task %s: message card: %v", body.TodoID, err)
	}
}

// markCardsSent records on the cards about an inbox item put in by at that its reply went out (text: what it said;
// empty: not known) and tells the clients (tell), which draw them sent; the cards it changed. The inbox says when a
// reply went out (replyInboxAs).
func (s *Service) markCardsSent(ctx context.Context, itemID string, at time.Time, text string, tell bool) []FeedItem {
	at = at.UTC()
	patch, _ := json.Marshal(struct {
		SentAt time.Time `json:"sent_at"`
		Sent   string    `json:"sent,omitempty"`
	}{at, text})
	var out []FeedItem
	if st := s.store(); st == nil {
		s.tasks.mu.Lock()
		for c, list := range s.tasks.feed {
			for i, it := range list {
				if it.Kind != feedMessage || it.Ref != itemID || it.At.After(at) || cardSentAt(it) != nil {
					continue
				}
				var body map[string]json.RawMessage
				_ = json.Unmarshal(it.Body, &body)
				if body == nil {
					body = map[string]json.RawMessage{}
				}
				var p map[string]json.RawMessage
				_ = json.Unmarshal(patch, &p)
				for k, v := range p {
					body[k] = v
				}
				it.Body, _ = json.Marshal(body)
				s.tasks.feed[c][i] = it
				out = append(out, it)
			}
		}
		s.tasks.mu.Unlock()
	} else {
		rows, err := st.MarkFeedSent(ctx, feedMessage, itemID, at, patch)
		if err != nil {
			s.logf("inbox %s: mark the cards sent: %v", itemID, err)
		}
		for _, r := range rows {
			out = append(out, FeedItem{ID: r.ID, Conversation: r.Conversation, At: r.At, Kind: r.Kind, Ref: r.Ref, Body: r.Body})
		}
	}
	if tell {
		for _, it := range out {
			s.event(ctx, map[string]any{"type": "assistant_feed", "conversation": it.Conversation, "item": it})
		}
	}
	return out
}

// cardSentAt is when a message card's reply went out; nil while it has not.
func cardSentAt(it FeedItem) *time.Time {
	var b struct {
		SentAt *time.Time `json:"sent_at"`
	}
	_ = json.Unmarshal(it.Body, &b)
	return b.SentAt
}

// settleCards fills in the cards among items whose item was replied to after they were put in but which do not say
// so: cards from before it was recorded on them (the reply went out, the card still offered "Send"). When the items
// were replied to is the inbox's (inboxRepliedAt).
func (s *Service) settleCards(ctx context.Context, items []FeedItem) {
	var ids []string
	for _, it := range items {
		if it.Kind == feedMessage && cardSentAt(it) == nil && !slices.Contains(ids, it.Ref) {
			ids = append(ids, it.Ref)
		}
	}
	if len(ids) == 0 {
		return
	}
	replied := s.inboxRepliedAt(ctx, ids)
	for _, it := range items {
		if it.Kind != feedMessage || cardSentAt(it) != nil {
			continue
		}
		at, ok := replied[it.Ref]
		if !ok || at.Before(it.At) {
			continue
		}
		for _, m := range s.markCardsSent(ctx, it.Ref, at, "", false) {
			for j := range items {
				if items[j].ID == m.ID {
					items[j] = m
				}
			}
		}
	}
}

// fileCards adds filed_at to the cards among items that do not say sent_at and whose item is filed (Done, Dismiss, read
// elsewhere, archived: not open), as of when it was filed: Send on them is refused (inbox_item_done), so the apps
// hide it. It is worked out on each read and not stored: moved back to the inbox, the item is open and the card
// offers Send again.
func (s *Service) fileCards(ctx context.Context, items []FeedItem) {
	var ids []string
	for _, it := range items {
		if it.Kind == feedMessage && cardSentAt(it) == nil && !slices.Contains(ids, it.Ref) {
			ids = append(ids, it.Ref)
		}
	}
	if len(ids) == 0 {
		return
	}
	filed := s.inboxFiledAt(ctx, ids)
	for i, it := range items {
		at, ok := filed[it.Ref]
		if it.Kind != feedMessage || !ok || cardSentAt(it) != nil {
			continue
		}
		var body map[string]json.RawMessage
		_ = json.Unmarshal(it.Body, &body)
		if body == nil {
			body = map[string]json.RawMessage{}
		}
		body["filed_at"], _ = json.Marshal(at.UTC())
		items[i].Body, _ = json.Marshal(body)
	}
}

// cardSent is whether card cardID, about inbox item itemID, says its reply went out ("Send" on the card sends once per
// card: sendFromCard).
func (s *Service) cardSent(ctx context.Context, cardID, itemID string) (bool, error) {
	card, err := s.feedItem(ctx, cardID)
	if err != nil {
		return false, err
	}
	return card != nil && card.Kind == feedMessage && card.Ref == itemID && cardSentAt(*card) != nil, nil
}

// feedItem is one feed item by its id; nil when there is none.
func (s *Service) feedItem(ctx context.Context, id string) (*FeedItem, error) {
	st := s.store()
	if st == nil {
		s.tasks.mu.Lock()
		defer s.tasks.mu.Unlock()
		for _, list := range s.tasks.feed {
			for _, it := range list {
				if it.ID == id {
					return &it, nil
				}
			}
		}
		return nil, nil
	}
	r, err := st.FeedItem(ctx, id)
	if err != nil || r == nil {
		return nil, err
	}
	return &FeedItem{ID: r.ID, Conversation: r.Conversation, At: r.At, Kind: r.Kind, Ref: r.Ref, Body: r.Body}, nil
}

// activeTasks are the tasks going on, as the inbox's judge and AI twin see them.
func (s *Service) activeTasks(ctx context.Context) []prompts.InboxTask {
	var out []prompts.InboxTask
	for _, t := range s.snapshotTasks(ctx, "") {
		if t.State == taskStateLabel("done") || t.State == taskStateLabel("auto_done") || t.State == taskStateLabel("failed") ||
			t.State == taskStateLabel("cancelled") {
			continue
		}
		out = append(out, prompts.InboxTask{ID: t.ID, Title: t.Title, State: t.State, Last: t.Last})
	}
	return out
}
