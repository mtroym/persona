package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/feishubot"
	"github.com/nwverse/persona/server/internal/proto"
)

// quoteHub is a paired bot whose owner replies to om_pic, a picture they sent before; resource answers the download.
func quoteHub(t *testing.T, resource appAnswer) (*fakeHub, *Service) {
	t.Helper()
	f, _ := feishuHub("ou_a", time.Unix(100, 0))
	f.settings = &Settings{MachineID: "server", Main: "m1"}
	f.setSessions("server", proto.Session{SessionID: "m1", Assistant: true})
	f.answerApp(AppFeishu, FeishuReact, func(json.RawMessage) (any, error) { return map[string]any{"reaction_id": "r1"}, nil })
	f.answerApp(AppFeishu, FeishuMessages, func(json.RawMessage) (any, error) {
		return map[string]any{"items": []map[string]any{{"message_id": "om_pic", "msg_type": "image",
			"sender": map[string]any{"id": "ou_a", "sender_type": "user"}, "body": map[string]any{"content": `{"image_key":"img_q"}`}}}}, nil
	})
	f.answerApp(AppFeishu, FeishuUserName, func(json.RawMessage) (any, error) { return map[string]any{"name": "我"}, nil })
	f.answerApp(AppFeishu, FeishuResource, resource)
	return f, newTestService(t, f)
}

func quoteReply() *feishubot.MessageEvent {
	var ev feishubot.MessageEvent
	ev.Message.MessageID, ev.Message.ParentID, ev.Message.RootID = "om_ask", "om_pic", "om_pic"
	ev.Message.ChatType, ev.Message.MessageType, ev.Message.Content = "p2p", "text", `{"text":"这张图里写的什么？"}`
	return &ev
}

// sentImages are the images of the conversation's session.send_once call.
func sentImages(f *fakeHub) []any {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c.Method == DeviceSessionSendOnce {
			var p map[string]any
			_ = json.Unmarshal(c.Params, &p)
			imgs, _ := p["images"].([]any)
			return imgs
		}
	}
	return nil
}

// Replying to a picture to ask about it: the picture goes to the model with the question, downloaded with the
// message it is in, not only the "[image]" line in the quote.
func TestFeishuReplyToAPictureCarriesThePicture(t *testing.T) {
	var asked []string
	f, s := quoteHub(t, func(p json.RawMessage) (any, error) {
		var a map[string]any
		_ = json.Unmarshal(p, &a)
		asked = append(asked, a["message_id"].(string)+"/"+a["key"].(string))
		return map[string]any{"data": []byte("png"), "mime": "image/png"}, nil
	})
	ev := quoteReply()
	content := feishubot.ParseContent(ev.Message.MessageType, ev.Message.Content, nil)
	if err := s.botSend(context.Background(), ev, content, botTarget{Kind: targetMain}); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 1 || asked[0] != "om_pic/img_q" {
		t.Fatalf("downloads %v", asked)
	}
	if imgs := sentImages(f); len(imgs) != 1 {
		t.Fatalf("images sent %v", imgs)
	}
	if text := sendOnceText(f); !strings.Contains(text, "引用的消息") || !strings.Contains(text, "这张图里写的什么？") {
		t.Fatalf("text %q", text)
	}
}

// A quoted picture that can't be had does not hold the question back: it goes with the quote's words.
func TestFeishuQuotedPictureGoneStillSends(t *testing.T) {
	f, s := quoteHub(t, func(json.RawMessage) (any, error) { return nil, errors.New("resource gone") })
	ev := quoteReply()
	content := feishubot.ParseContent(ev.Message.MessageType, ev.Message.Content, nil)
	if err := s.botSend(context.Background(), ev, content, botTarget{Kind: targetMain}); err != nil {
		t.Fatalf("held back: %v", err)
	}
	if text := sendOnceText(f); !strings.Contains(text, "这张图里写的什么？") {
		t.Fatalf("text %q", text)
	}
	if imgs := sentImages(f); len(imgs) != 0 {
		t.Fatalf("images %v", imgs)
	}
}

// A quoted or forwarded message read back carries its own mentions: an @ in it reaches the model as the person's name,
// not as the message's @_user_N key.
func TestFeishuQuoteAndForwardNameTheirMentions(t *testing.T) {
	mentions := []map[string]any{{"key": "@_user_1", "name": "Alice"}}
	t.Run("quote", func(t *testing.T) {
		f, s := quoteHub(t, func(json.RawMessage) (any, error) { return nil, errors.New("no media") })
		f.answerApp(AppFeishu, FeishuMessages, func(json.RawMessage) (any, error) {
			return map[string]any{"items": []map[string]any{{"message_id": "om_pic", "msg_type": "text", "mentions": mentions,
				"sender": map[string]any{"id": "ou_a", "sender_type": "user"}, "body": map[string]any{"content": `{"text":"@_user_1 please check"}`}}}}, nil
		})
		ev := quoteReply()
		content := feishubot.ParseContent(ev.Message.MessageType, ev.Message.Content, nil)
		if err := s.botSend(context.Background(), ev, content, botTarget{Kind: targetMain}); err != nil {
			t.Fatal(err)
		}
		if text := sendOnceText(f); !strings.Contains(text, "@Alice please check") || strings.Contains(text, "@_user_1") {
			t.Fatalf("text %q", text)
		}
	})
	t.Run("forward", func(t *testing.T) {
		f, s := quoteHub(t, func(json.RawMessage) (any, error) { return nil, errors.New("no media") })
		f.answerApp(AppFeishu, FeishuMessages, func(json.RawMessage) (any, error) {
			return map[string]any{"items": []map[string]any{
				{"message_id": "om_fwd", "msg_type": "merge_forward", "body": map[string]any{"content": "Merged and Forwarded Message"}},
				{"message_id": "om_in", "upper_message_id": "om_fwd", "msg_type": "text", "create_time": "100000", "mentions": mentions,
					"sender": map[string]any{"id": "ou_a", "sender_type": "user"}, "body": map[string]any{"content": `{"text":"@_user_1 please check"}`}},
			}}, nil
		})
		var ev feishubot.MessageEvent
		ev.Message.MessageID, ev.Message.ChatType, ev.Message.MessageType = "om_fwd", "p2p", "merge_forward"
		ev.Message.Content = `{"content":"Merged and Forwarded Message"}`
		content := feishubot.ParseContent(ev.Message.MessageType, ev.Message.Content, nil)
		if err := s.botSend(context.Background(), &ev, content, botTarget{Kind: targetMain}); err != nil {
			t.Fatal(err)
		}
		if text := sendOnceText(f); !strings.Contains(text, "@Alice please check") || strings.Contains(text, "@_user_1") {
			t.Fatalf("text %q", text)
		}
	})
}
