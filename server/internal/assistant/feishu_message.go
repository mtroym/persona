package assistant

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/feishubot"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// ---- a message to the bot ----

// FeishuMessage is feishu.message: the owner's message in the bot's private chat (the hub checked who it is from),
// taken once and handled in the background, one at a time in the order they came (feishuTurn).
//
// Handled each on its own, a message slower to get through (its reaction, a picture to download, a quote to read)
// was overtaken by the next one. The later message went into the conversation first, and the one answer covering
// both went under the earlier message. A message right after "/new …" could also read the target before the new
// conversation was set, and start one more.
func (s *Service) FeishuMessage(ctx context.Context, p FeishuMessage) error {
	var ev feishubot.MessageEvent
	if err := json.Unmarshal(p.Event, &ev); err != nil {
		return badRequest(err)
	}
	// It takes its place as it comes, before the bot's status is read (a call to the hub). One that is not handled
	// (refused, redelivered) still passes the turn on.
	b := &s.fbot
	turn := &feishuTurn{done: make(chan struct{}), wait: feishuTurnWait, now: time.Now}
	b.mu.Lock()
	if b.now != nil {
		turn.now = b.now
	}
	turn.since = turn.now()
	turn.prev, b.line = b.line, turn.done
	if b.turnWait > 0 {
		turn.wait = b.turnWait
	}
	b.mu.Unlock()
	queued := false
	defer func() {
		if !queued {
			turn.pass()
		}
	}()
	// What it is taken with first: a message taken is always answered, never dropped for want of it.
	st, err := s.feishuStatus(ctx)
	if err != nil {
		return &rpc.Error{Code: CodeTryAgain, Message: "the bot's status: " + err.Error()}
	}
	first, err := s.claimBotMessage(ev.Message.MessageID)
	if err != nil {
		return &rpc.Error{Code: CodeTryAgain, Message: err.Error()}
	}
	if !first {
		return nil // redelivered, or handed over again after a call that was cut off
	}
	queued = true
	go func() {
		defer turn.pass()
		turn.waitTurn()
		if p.From == FeishuFromElsewhere {
			s.feishuElsewhere(ev, st) // the owner's "@bot /ai …" from a group that is not opted in (feishu_elsewhere.go)
			return
		}
		s.feishuMessage(ev, st, p.Action)
	}()
	return nil
}

func (s *Service) feishuMessage(ev feishubot.MessageEvent, st FeishuStatus, action string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	msg := ev.Message
	if s.botRecalled(msg.MessageID) {
		s.logf("bots: a message recalled before its turn: not sent")
		return
	}
	content := feishubot.ParseContent(msg.MessageType, msg.Content, msg.Mentions)
	if content.Sticker {
		// A sticker carries no request: do not react, do not start an Agent turn.
		s.logf("bots: a sticker was accepted silently: no turn")
		return
	}
	if msg.ChatType != "p2p" {
		// A group chat's message never takes the private chat's road (its commands, its replies, the current target):
		// the hub does not hand any over yet, and this is the second line when it does (feishu_thread.go).
		s.threadMessage(ctx, ev, st, content)
		return
	}
	if !content.Forward && len(content.Media) == 0 && noReplyCommand(content.Text) && s.chatNoReply(ctx, ev) {
		return // "don't report back" for a to-do taken in this chat with no topic to say it in (todo_source.go)
	}
	if _, _, isCommand := parseBotCommand(content.Text); isCommand && !content.Forward && len(content.Media) == 0 {
		handled, rest := s.botCommand(ctx, st.Features, msg.MessageID, content.Text, msg.RootID, msg.ParentID)
		if handled {
			return
		}
		content.Text = rest
	}
	if !content.Forward && s.acceptReply(ctx, msg.MessageID, content.Text, len(content.Media) > 0, msg.ParentID, msg.RootID) {
		return // about a "Please verify" card (acceptance.go)
	}
	if !content.Forward && len(content.Media) == 0 && s.botAnswerQuestion(ctx, msg.MessageID, content.Text, action, msg.RootID, msg.ParentID) {
		return // the answer to the card waiting in this topic (feishu_text_answer.go)
	}
	target := s.botTargetFor(msg.RootID, msg.ParentID)
	if err := botAllowed(s.botLang(ctx), st.Features, target); err != nil {
		s.botNote(ctx, msg.MessageID, err.Error())
		return
	}
	if err := s.botSend(ctx, &ev, content, target); err != nil {
		s.logf("bots: send: %v", err)
		s.botNote(ctx, msg.MessageID, i18n.T(s.botLang(ctx), "assistant-feishu.not_sent", i18n.Args{"error": clip(oneLine(err.Error()), 200)}))
	}
}

// botAllowed says, in l, why the chat may not send to t now (nil: it may).
func botAllowed(l i18n.Lang, f FeishuFeatures, t botTarget) error {
	switch {
	case t.agent() && !f.Agents:
		return errors.New(i18n.T(l, "assistant-feishu.agents_off"))
	case !t.agent() && !f.Main:
		if f.Agents {
			return errors.New(i18n.T(l, "assistant-feishu.main_off_agents_on"))
		}
		return errors.New(i18n.T(l, "assistant-feishu.chat_off"))
	}
	return nil
}

// resolvedTarget is a target as it is now: the machine and conversation, and what to call it.
type resolvedTarget struct {
	machine     string
	machineName string
	session     proto.Session
	label       string
	main        bool
	isNew       bool // no conversation yet: the message starts one
}

func (s *Service) resolveBotTarget(ctx context.Context, t botTarget) (resolvedTarget, error) {
	l := s.botLang(ctx)
	switch t.Kind {
	case targetAgent:
		d, sess, err := s.findAgent(ctx, t.MachineID, t.SessionID)
		if err != nil {
			return resolvedTarget{}, errors.New(i18n.T(l, "assistant-feishu.agent_not_found", i18n.Args{"id": short(t.SessionID)}))
		}
		if sess.SourceKind != "" && (sess.Capabilities == nil || !sess.Capabilities.Send) {
			return resolvedTarget{}, errors.New(i18n.T(l, "assistant-feishu.agent_read_only", i18n.Args{"reason": sess.ReadOnlyReason}))
		}
		return resolvedTarget{machine: d.ID, machineName: d.Name, session: sess, label: agentLabel(sess) + " · " + d.Name}, nil
	default:
		cfg := s.settings(ctx)
		d, ok := s.device(ctx, cfg.MachineID)
		if !ok {
			return resolvedTarget{}, errors.New(i18n.T(l, "assistant-feishu.no_assistant_machine"))
		}
		sid := t.SessionID
		if t.Kind == targetMain {
			sid = cfg.Main
		}
		r := resolvedTarget{machine: d.ID, machineName: d.Name, main: sid != "" && sid == cfg.Main}
		if sid == "" {
			r.isNew, r.main, r.label = true, t.Kind == targetMain, i18n.T(l, "assistant-feishu.new_chat")
			if r.main {
				r.label = i18n.T(l, "assistant-feishu.main_chat")
			}
			return r, nil
		}
		sess, _ := s.session(ctx, d.ID, sid)
		sess.SessionID, sess.MachineID = sid, d.ID
		r.session = sess
		r.label = conversationName(sess, cfg.Main)
		return r, nil
	}
}

func (s *Service) targetLabel(ctx context.Context, t botTarget) string {
	r, err := s.resolveBotTarget(ctx, t)
	if err != nil {
		return i18n.T(s.botLang(ctx), "assistant-feishu.not_found")
	}
	if t.Kind == targetPersona && r.isNew {
		return i18n.T(s.botLang(ctx), "assistant-feishu.new_chat_next")
	}
	return r.label
}

const (
	botAnswerPiece = 6000 // runes per card: a card holds about 30 KB
	botImagesMax   = 4
)

// botSend passes a message on to its target and starts the run that answers it.
func (s *Service) botSend(ctx context.Context, ev *feishubot.MessageEvent, content feishubot.Content, t botTarget) error {
	return s.botSendIn(ctx, ev, content, t, nil)
}

// botSendIn is botSend for a message said in a topic (topic given: the chat and thread of the todo it is bound to,
// feishu_thread.go): the message quotes and forwards nothing in (what others said there is not the owner's words),
// and the run's progress, answer and notes go back into that thread (runTo).
func (s *Service) botSendIn(ctx context.Context, ev *feishubot.MessageEvent, content feishubot.Content, t botTarget, topic *feishuTopic) error {
	rt, err := s.resolveBotTarget(ctx, t)
	if err != nil {
		return err
	}
	msg := ev.Message
	reaction, err := s.botReact(ctx, msg.MessageID, "OnIt")
	if err != nil {
		s.logf("bots: react: %v", err)
	}
	started := false
	defer func() {
		if !started && reaction != "" {
			s.botUnreact(context.WithoutCancel(ctx), msg.MessageID, reaction)
		}
	}()
	in := prompts.BotMessage{Persona: !t.agent(), Topic: topic != nil, Owner: s.owner(ctx), Text: content.Text}
	to := topic.target()
	s.fbot.mu.Lock()
	_, ours := s.fbot.st.Threads[msg.ParentID]
	s.fbot.mu.Unlock()
	// The media to pass on: this message's own, then the pictures and files of the message it quotes (the owner
	// replies to a picture to ask about it), each downloaded with the message it is in.
	type botMedia struct {
		of     string // the message it is in
		quoted bool   // not to be had is no reason to hold the message back
		feishubot.Media
	}
	var media []botMedia
	for _, md := range content.Media {
		media = append(media, botMedia{of: msg.MessageID, Media: md})
	}
	if msg.ParentID != "" && !ours && topic == nil {
		var quoted []feishubot.Media
		in.Quote, quoted = s.botQuote(ctx, msg.ParentID)
		for _, md := range quoted {
			media = append(media, botMedia{of: msg.ParentID, quoted: true, Media: md})
		}
	}
	if content.Forward && topic == nil {
		in.Forward = s.botForward(ctx, msg.MessageID)
	}
	var images []proto.Image
	var tooLarge []string // files too large to pass on
	for _, bm := range media {
		md := bm.Media
		if md.Kind == "image" {
			var res struct {
				Data     []byte `json:"data"`
				MIME     string `json:"mime"`
				TooLarge bool   `json:"too_large"`
			}
			if err := s.appCall(ctx, AppFeishu, FeishuResource, map[string]any{"message_id": bm.of, "key": md.Key, "kind": md.Kind}, &res); err != nil {
				if bm.quoted {
					s.logf("bots: the quoted message's image: %v", err)
					continue
				}
				return fmt.Errorf("%s%w", i18n.T(s.botLang(ctx), "assistant-feishu.download_image"), err)
			}
			mime := res.MIME
			if !strings.HasPrefix(mime, "image/") {
				mime = "image/png"
			}
			if len(images) < botImagesMax && !res.TooLarge {
				images = append(images, proto.Image{Mime: mime, Data: base64.StdEncoding.EncodeToString(res.Data)})
				continue
			}
			// More pictures than a message takes, or one too big for the model: as a file.
			md.Name = md.Key + map[string]string{"image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp"}[mime]
			if !strings.Contains(md.Name, ".") {
				md.Name += ".png"
			}
		}
		var saved struct {
			Path     string `json:"path"`
			TooLarge bool   `json:"too_large"`
		}
		if err := s.appCall(ctx, AppFeishu, FeishuSave, map[string]any{"message_id": bm.of, "key": md.Key, "kind": md.Kind, "name": md.Name,
			"machine_id": rt.machine}, &saved); err != nil {
			if bm.quoted {
				s.logf("bots: the quoted message's file: %v", err)
				continue
			}
			return fmt.Errorf("%s%w", i18n.T(s.botLang(ctx), "assistant-feishu.save_file", i18n.Args{"machine": rt.machineName}), err)
		}
		if saved.TooLarge || saved.Path == "" {
			tooLarge = append(tooLarge, md.Name) // left out: the rest of the message goes, and a line says so
			continue
		}
		in.Files = append(in.Files, saved.Path)
	}
	if s.botRecalled(msg.MessageID) { // recalled while its pictures and files came down
		s.logf("bots: a message recalled before it went in: not sent")
		return nil
	}
	text := prompts.RenderBotMessage(in)
	if strings.TrimSpace(content.Text) == "" && in.Quote == nil && in.Forward == "" && len(images) == 0 && len(in.Files) == 0 {
		if len(tooLarge) > 0 {
			return errors.New(i18n.T(s.botLang(ctx), "assistant-feishu.file_too_large_only", i18n.Args{"files": strings.Join(tooLarge, listSep(s.botLang(ctx)))}))
		}
		return errors.New(i18n.T(s.botLang(ctx), "assistant-feishu.cannot_read"))
	}
	if len(tooLarge) > 0 {
		defer func() {
			if started {
				s.botNoteTo(context.WithoutCancel(ctx), to, msg.MessageID, i18n.T(s.botLang(ctx), "assistant-feishu.file_too_large", i18n.Args{"files": strings.Join(tooLarge, listSep(s.botLang(ctx)))}))
			}
		}()
	}

	run := &channel.Run{ID: "fb_" + msg.MessageID, Channel: channel.Feishu, ReplyTo: msg.MessageID, Started: time.Now().UTC(), Label: rt.label,
		Probe: botProbe(content.Text, in), Ext: map[string]string{}}
	if reaction != "" {
		run.Ext["reaction"] = reaction
	}
	if topic != nil {
		run.Ext["chat"], run.Ext["thread"] = topic.Chat, topic.Thread
	}
	if rt.isNew {
		sess, err := s.Start(ctx, StartParams{Text: text, Images: images, Main: rt.main, Origin: OriginChannel, OriginRef: channel.Feishu})
		if err != nil {
			return err
		}
		run.MachineID, run.SessionID, run.TargetKind, run.Probe = sess.MachineID, sess.SessionID, targetPersona, ""
		if t.Kind == targetPersona { // /new: the chat goes on in it
			s.setBotCurrent(botTarget{Kind: targetPersona, MachineID: sess.MachineID, SessionID: sess.SessionID})
		}
	} else {
		sid := rt.session.SessionID
		run.MachineID, run.SessionID, run.TargetKind = rt.machine, sid, t.Kind
		if run.TargetKind == targetMain {
			run.TargetKind = targetPersona
		}
		run.Base = s.lastSeq(ctx, rt.machine, sid)
		id := channel.ClientMsgID(channel.Feishu, msg.MessageID)
		run.MsgIDs = []string{id}
		p := map[string]any{"session_id": sid, "text": text, "client_msg_id": id}
		if len(images) > 0 {
			p["images"] = images
		}
		if !t.agent() {
			s.UserSpoke(ctx, SessionRef{MachineID: rt.machine, SessionID: sid})
			if p, err = s.SendParams(ctx, SendParams{MachineID: rt.machine, SessionID: sid, Params: p}); err != nil {
				return err
			}
		}
		if err := s.sendOnce(ctx, rt.machine, p); err != nil {
			return err
		}
	}
	started = true
	s.startChannelRun(run)
	return nil
}

// botProbe picks what of a message to look for in the transcript: its first line, else something only it has.
func botProbe(text string, in prompts.BotMessage) string {
	var fallbacks []string
	if len(in.Files) > 0 {
		fallbacks = append(fallbacks, in.Files[0])
	}
	fallbacks = append(fallbacks, in.Forward)
	if in.Quote != nil {
		fallbacks = append(fallbacks, in.Quote.Text)
	}
	return channel.Probe(text, fallbacks...)
}

// botQuote is the message a message replies to, and the pictures and files in it.
func (s *Service) botQuote(ctx context.Context, id string) (*prompts.BotQuote, []feishubot.Media) {
	items, err := s.botMessages(ctx, id)
	if err != nil || len(items) == 0 {
		return nil, nil
	}
	it := items[0]
	c := feishubot.ParseContent(it.MsgType, it.Body.Content, it.MentionList())
	who := "Persona"
	if it.Sender.SenderType == "user" {
		who = s.botName(ctx, it.Sender.ID)
	}
	if c.Text == "" {
		c.Text = "[" + it.MsgType + "]"
	}
	return &prompts.BotQuote{Who: who, Text: clip(c.Text, 4000)}, c.Media
}

// botForward reads a merged forward: one line per message, oldest first.
func (s *Service) botForward(ctx context.Context, id string) string {
	items, err := s.botMessages(ctx, id)
	if err != nil {
		return "（读不到转发的内容：" + err.Error() + "）"
	}
	loc := s.location(ctx)
	var lines []string
	n := 0
	for _, it := range items {
		if it.UpperMessageID != id {
			continue
		}
		c := feishubot.ParseContent(it.MsgType, it.Body.Content, it.MentionList())
		text := c.Text
		if text == "" {
			text = "[" + it.MsgType + "]"
		}
		for _, md := range c.Media {
			if md.Kind == "image" && !strings.Contains(text, "[图片]") {
				text += " [图片]"
			}
		}
		who := "某人"
		if it.Sender.SenderType == "user" {
			who = s.botName(ctx, it.Sender.ID)
		} else if it.Sender.SenderType == "app" {
			who = "机器人"
		}
		line := fmt.Sprintf("%s %s：%s", it.At().In(loc).Format("01-02 15:04"), who, text)
		n += len([]rune(line))
		if n > 30000 {
			lines = append(lines, "（后面的太长，略）")
			break
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return "（转发里没有能读的消息）"
	}
	return strings.Join(lines, "\n")
}
