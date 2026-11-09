package feishubot

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Envelope is an event or callback as delivered (schema 2.0).
type Envelope struct {
	Schema string `json:"schema"`
	Header struct {
		EventID    string `json:"event_id"`
		EventType  string `json:"event_type"`
		CreateTime string `json:"create_time"`
		AppID      string `json:"app_id"`
	} `json:"header"`
	Event json.RawMessage `json:"event"`
}

// Event types the hub handles.
const (
	EventMessage    = "im.message.receive_v1"
	EventCardAction = "card.action.trigger"
	EventP2PEntered = "im.chat.access_event.bot_p2p_chat_entered_v1"
	EventRecalled   = "im.message.recalled_v1"
)

// Recalled is im.message.recalled_v1: a message in a chat the bot is in was recalled (message_owner: by its sender).
type Recalled struct {
	MessageID  string `json:"message_id"`
	ChatID     string `json:"chat_id"`
	RecallType string `json:"recall_type"`
}

func ParseEnvelope(payload []byte) (*Envelope, error) {
	var e Envelope
	if err := json.Unmarshal(payload, &e); err != nil {
		return nil, err
	}
	if e.Header.EventType == "" {
		return nil, fmt.Errorf("feishubot: not a 2.0 event")
	}
	return &e, nil
}

// UserID is how Feishu names a user; the hub keys on the open id (per app).
type UserID struct {
	OpenID  string `json:"open_id"`
	UnionID string `json:"union_id"`
	UserID  string `json:"user_id"`
}

// MessageEvent is im.message.receive_v1.
type MessageEvent struct {
	Sender struct {
		SenderID   UserID `json:"sender_id"`
		SenderType string `json:"sender_type"` // user | app
	} `json:"sender"`
	Message struct {
		MessageID   string    `json:"message_id"`
		RootID      string    `json:"root_id"`
		ParentID    string    `json:"parent_id"`
		ThreadID    string    `json:"thread_id"`
		CreateTime  string    `json:"create_time"` // Unix milliseconds
		ChatID      string    `json:"chat_id"`
		ChatType    string    `json:"chat_type"` // p2p | group
		MessageType string    `json:"message_type"`
		Content     string    `json:"content"`
		Mentions    []Mention `json:"mentions"`
	} `json:"message"`
}

type Mention struct {
	Key  string `json:"key"` // @_user_1, as it stands in the text
	ID   UserID `json:"id"`
	Name string `json:"name"`
}

func (m *MessageEvent) At() time.Time { return msTime(m.Message.CreateTime) }

// CardAction is card.action.trigger: a button pressed or a form submitted on a card the bot sent.
type CardAction struct {
	Operator struct {
		OpenID string `json:"open_id"`
	} `json:"operator"`
	Token  string `json:"token"`
	Action struct {
		Value      map[string]any `json:"value"`
		Tag        string         `json:"tag"`
		Name       string         `json:"name"`
		FormValue  map[string]any `json:"form_value"`
		InputValue string         `json:"input_value"`
		Option     string         `json:"option"`
	} `json:"action"`
	Context struct {
		MessageID string `json:"open_message_id"`
		ChatID    string `json:"open_chat_id"`
	} `json:"context"`
}

// Str is a string value of the action (its value map, then its form).
func (a *CardAction) Str(key string) string {
	if v, ok := a.Action.Value[key]; ok {
		return anyString(v)
	}
	if v, ok := a.Action.FormValue[key]; ok {
		return anyString(v)
	}
	return ""
}

// Form returns a form field: a string, or the picks of a multi-select joined with ", ".
func (a *CardAction) Form(key string) string { return anyString(a.Action.FormValue[key]) }

func anyString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		parts := make([]string, 0, len(x))
		for _, p := range x {
			parts = append(parts, anyString(p))
		}
		return strings.Join(parts, ", ")
	case nil:
		return ""
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	}
	return fmt.Sprint(v)
}

// CardResponse answers a card callback: a toast, and the card to show instead (raw card JSON).
type CardResponse struct {
	Toast *Toast       `json:"toast,omitempty"`
	Card  *CardPayload `json:"card,omitempty"`
}

type Toast struct {
	Type    string `json:"type"` // info | success | error | warning
	Content string `json:"content"`
}

type CardPayload struct {
	Type string `json:"type"` // raw
	Data any    `json:"data"`
}

// P2PEntered is the user opening the chat with the bot.
type P2PEntered struct {
	ChatID     string `json:"chat_id"`
	OperatorID UserID `json:"operator_id"`
}

func msTime(s string) time.Time {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(n)
}

// --- message content ---

// Content is a message read into what Persona passes on: its text (Markdown-ish) and the media in it to download.
type Content struct {
	Text  string
	Media []Media
	// Forward: a merged forward; its messages are read with Client.Message.
	Forward bool
	// Sticker: a sticker carries no request; accept it silently instead of starting an Agent turn.
	Sticker bool
}

// Media is an image or file in a message.
type Media struct {
	Kind string // image | file
	Key  string
	Name string // files
}

// ParseContent reads a message's content by its type. mentions turn @_user_N back into names.
func ParseContent(msgType, content string, mentions []Mention) Content {
	var c Content
	switch msgType {
	case "text":
		var v struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal([]byte(content), &v)
		c.Text = v.Text
	case "post":
		c = parsePost(content, mentions)
	case "image":
		var v struct {
			ImageKey string `json:"image_key"`
		}
		_ = json.Unmarshal([]byte(content), &v)
		if v.ImageKey != "" {
			c.Media = append(c.Media, Media{Kind: "image", Key: v.ImageKey})
		}
	case "file", "audio", "media":
		var v struct {
			FileKey  string `json:"file_key"`
			FileName string `json:"file_name"`
		}
		_ = json.Unmarshal([]byte(content), &v)
		name := v.FileName
		if name == "" {
			name = map[string]string{"audio": "语音.opus", "media": "视频.mp4"}[msgType]
			if name == "" {
				name = "文件"
			}
		}
		if v.FileKey != "" {
			c.Media = append(c.Media, Media{Kind: "file", Key: v.FileKey, Name: name})
		}
		if msgType == "audio" {
			c.Text = "[语音消息]"
		}
	case "merge_forward":
		c.Forward = true
	case "sticker":
		c.Text = "[表情]"
		c.Sticker = true
	case "interactive":
		c.Text = cardText(content)
	case "share_chat":
		c.Text = "[分享了一个群]"
	case "share_user":
		c.Text = "[分享了一张名片]"
	case "location":
		var v struct {
			Name      string `json:"name"`
			Longitude string `json:"longitude"`
			Latitude  string `json:"latitude"`
		}
		_ = json.Unmarshal([]byte(content), &v)
		c.Text = strings.TrimSpace(fmt.Sprintf("[位置] %s %s,%s", v.Name, v.Latitude, v.Longitude))
	case "todo":
		c.Text = "[任务] " + cardText(content)
	default:
		c.Text = "[" + msgType + " 消息]"
	}
	c.Text = strings.TrimSpace(nameMentionKeys(c.Text, mentions))
	return c
}

// nameMentionKeys puts "@<name>" in place of each mention's key in text, in one pass and a key only as a whole:
// "@_user_1" is not the start of "@_user_10" (the key is not followed by another digit). A mention without a key is
// no placeholder, and one without a name leaves its key as it stands.
func nameMentionKeys(text string, mentions []Mention) string {
	var b strings.Builder
	done := 0 // text before this is in b
	for i := 0; i < len(text); i++ {
		for _, m := range mentions {
			if m.Key == "" || m.Name == "" || !strings.HasPrefix(text[i:], m.Key) {
				continue
			}
			if end := i + len(m.Key); end < len(text) && isDigit(text[end]) {
				continue
			}
			b.WriteString(text[done:i])
			b.WriteString("@" + m.Name)
			done = i + len(m.Key)
			i = done - 1
			break
		}
	}
	if done == 0 {
		return text
	}
	b.WriteString(text[done:])
	return b.String()
}

func isDigit(c byte) bool { return '0' <= c && c <= '9' }

// parsePost flattens rich text: paragraphs become lines, links Markdown links, code blocks fenced; images are media.
// A received post is {"title","content":[[…]]}; one read back may be keyed by language ({"zh_cn":{…}}).
// mentions give names for at nodes that carry a user_id without a user_name.
func parsePost(content string, mentions []Mention) Content {
	type node struct {
		Tag      string `json:"tag"`
		Text     string `json:"text"`
		Href     string `json:"href"`
		UserName string `json:"user_name"`
		UserID   string `json:"user_id"`
		ImageKey string `json:"image_key"`
		FileKey  string `json:"file_key"`
		Language string `json:"language"`
		Emoji    string `json:"emoji_type"`
	}
	type post struct {
		Title   string   `json:"title"`
		Content [][]node `json:"content"`
	}
	var p post
	if err := json.Unmarshal([]byte(content), &p); err != nil || (p.Title == "" && p.Content == nil) {
		var byLang map[string]post
		if json.Unmarshal([]byte(content), &byLang) == nil {
			for _, k := range []string{"zh_cn", "en_us", "ja_jp"} {
				if v, ok := byLang[k]; ok {
					p = v
					break
				}
			}
			if p.Content == nil {
				for _, v := range byLang {
					p = v
					break
				}
			}
		}
	}
	var c Content
	var lines []string
	if p.Title != "" {
		lines = append(lines, "**"+p.Title+"**")
	}
	for _, para := range p.Content {
		var b strings.Builder
		for _, n := range para {
			switch n.Tag {
			case "text", "md":
				b.WriteString(n.Text)
			case "a":
				fmt.Fprintf(&b, "[%s](%s)", n.Text, n.Href)
			case "at":
				switch {
				case n.UserName != "":
					b.WriteString("@" + n.UserName)
				case n.UserID == "all":
					b.WriteString("@所有人")
				case n.UserID != "":
					if name := mentionName(n.UserID, mentions); name != "" {
						b.WriteString("@" + name)
					} else {
						b.WriteString("@" + n.UserID)
					}
				default:
					b.WriteString("@某人")
				}
			case "img":
				if n.ImageKey != "" {
					c.Media = append(c.Media, Media{Kind: "image", Key: n.ImageKey})
					b.WriteString("[图片]")
				}
			case "media":
				if n.FileKey != "" {
					c.Media = append(c.Media, Media{Kind: "file", Key: n.FileKey, Name: "视频.mp4"})
					b.WriteString("[视频]")
				}
			case "code_block":
				fmt.Fprintf(&b, "\n```%s\n%s\n```\n", strings.ToLower(n.Language), strings.TrimRight(n.Text, "\n"))
			case "emotion":
				b.WriteString("[" + n.Emoji + "]")
			case "hr":
				b.WriteString("---")
			}
		}
		lines = append(lines, b.String())
	}
	c.Text = strings.Join(lines, "\n")
	return c
}

// mentionName resolves an at node's user_id through the event's mentions; the id may be an open_id,
// a user_id or a union_id depending on the tenant's id type, or the mention's own key ("@_user_1").
func mentionName(id string, mentions []Mention) string {
	for _, m := range mentions {
		if m.ID.OpenID == id || m.ID.UserID == id || m.ID.UnionID == id || m.Key != "" && m.Key == id {
			return m.Name
		}
	}
	return ""
}

// cardText pulls the readable text out of a card someone forwarded (any shape: every "content" / "text" string).
func cardText(content string) string {
	var v any
	if json.Unmarshal([]byte(content), &v) != nil {
		return "[卡片]"
	}
	var parts []string
	var walk func(any)
	walk = func(x any) {
		switch t := x.(type) {
		case map[string]any:
			for _, k := range []string{"title", "content", "text"} {
				if s, ok := t[k].(string); ok && strings.TrimSpace(s) != "" {
					parts = append(parts, strings.TrimSpace(s))
				}
			}
			for k, sub := range t {
				if _, isStr := sub.(string); isStr && (k == "title" || k == "content" || k == "text") {
					continue
				}
				walk(sub)
			}
		case []any:
			for _, sub := range t {
				walk(sub)
			}
		}
	}
	walk(v)
	if len(parts) == 0 {
		return "[卡片]"
	}
	return "[卡片] " + strings.Join(parts, " ")
}
