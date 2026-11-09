package extmgr

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/feishubot"
	"github.com/nwverse/persona/server/internal/i18n"
)

// The Feishu bot (docs/extensions/bots.md): the user talks to Persona through a Feishu / Lark custom app's bot. The hub keeps its
// transport: the app's credentials, the long connection to the open platform, pairing with its owner, sending and
// changing messages and cards. What the chat does — where a message goes, following its turn and the answer cards,
// what is asked there, the commands, pushes and the sync mirror — is the assistant's (internal/assistant, feishu*.go):
// the owner's messages and card presses go to it over the bus (assistant_channels.go), and it posts through the hub.
// Only the paired user is answered: in the bot's private chat, and in the topics of the group chats the owner has
// opted in (TopicChats, bots_topics.go). Everything is off until switched on in the app (Settings › Connectors › Lark bot).

// BotsConfig is hub.json "bots".
type BotsConfig struct {
	Feishu FeishuBotConfig `json:"feishu,omitempty"`
}

// FeishuBotConfig: the app's credentials, for those who would rather keep them in manager.json (0600) than enter them in
// the app. What the app saves wins.
type FeishuBotConfig struct {
	AppID     string `json:"app_id,omitempty"`
	AppSecret string `json:"app_secret,omitempty"`
	Domain    string `json:"domain,omitempty"` // feishu (default) | lark | the open platform's URL
}

// FeishuBotSettings is what the app set, in settings (botSettingsKey).
type FeishuBotSettings struct {
	Enabled   bool        `json:"enabled"`
	AppID     string      `json:"app_id,omitempty"`
	SecretEnc string      `json:"secret_enc,omitempty"` // sealed with secret.key (secretkey.go sealKey)
	Domain    string      `json:"domain,omitempty"`
	Features  BotFeatures `json:"features"`
	Owner     *BotOwner   `json:"owner,omitempty"`
	Pairing   *botPairing `json:"pairing,omitempty"`
	// TopicChats are the group chats the owner opted in (bots_topics.go): their messages reach the assistant, and the
	// bot may send into them. The owner's own list: it goes when the owner does.
	TopicChats []BotChat `json:"topic_chats,omitempty"`
	// AISources are the messages the hub took as the owner's "@bot /ai …" in a group chat that is not opted in (#6146,
	// bots_topics.go): the only messages of such a group the bot may reply to, and so the only place it writes in one.
	// Kept across restarts (a to-do may be done weeks later, and its answer goes under the message); the oldest are
	// dropped past botAISourcesKeep. Like the topic chats, the owner's: it goes when the owner does.
	AISources []BotAISource `json:"ai_sources,omitempty"`
	// NudgeAfter is how many minutes a card the bot sent may wait unanswered before the assistant nudges under it once
	// (#6084, assistant/feishu_nudge.go): 0 is off, nil (never set) is assistantwire.NudgeAfterDefault.
	NudgeAfter *int `json:"nudge_after,omitempty"`
}

// BotFeatures are the parts that can be switched on one by one: kept here with the credentials, acted on by the
// assistant (assistantwire.FeishuFeatures is the same).
type BotFeatures struct {
	Main     bool `json:"main"`      // messages go to Persona: the main conversation, unless the chat switched to another
	Cards    bool `json:"cards"`     // questions and approvals of what the chat drives show there as cards
	AllCards bool `json:"all_cards"` // and those of every agent and conversation
	Pushes   bool `json:"pushes"`    // notify_user and scheduled tasks' results go there too
	Sync     bool `json:"sync"`      // the main conversation's turns from elsewhere (the app, a task) are mirrored there
	Agents   bool `json:"agents"`    // coding agents on every computer can be driven from the chat
	// Reports: how the main conversation's tasks go (done, waiting for the user, cut off) is told in the chat, a few
	// at a time (#2768). On by default, and for settings saved before it: they have no such key.
	Reports bool `json:"reports"`
	// Board: /tasks keeps one standing "waiting for you" card in the chat and edits it in place (#6052). Off by default
	// (it changes what /tasks does), and for settings saved before it: they have no such key.
	Board bool `json:"board"`
	// Workbench: the workbench (#1961): /home, /today, /inbox, the board pinned as a home card, a workbench button on
	// every card, a permission field in /start, companion bots. Off by default, and for settings saved before it.
	Workbench bool `json:"workbench"`
	// OnlyConclusions: of the task reports only the conclusions are pushed; a finished turn, waiting and interrupted stay
	// in the task list and the briefing (#1226). Off by default, and for settings saved before it: they have no such key.
	OnlyConclusions bool `json:"only_conclusions"`
}

var defaultBotFeatures = BotFeatures{Main: true, Cards: true, Reports: true}

// BotOwner is the paired user: the only one the bot answers, and where pushes go.
type BotOwner struct {
	OpenID string    `json:"open_id"`
	ChatID string    `json:"chat_id"`
	Name   string    `json:"name,omitempty"`
	At     time.Time `json:"at"`
}

type botPairing struct {
	Code    string    `json:"code"`
	Expires time.Time `json:"expires"`
	Fails   int       `json:"fails,omitempty"` // wrong codes sent meanwhile; botPairTries end it
}

const (
	botSettingsKey = "bots.feishu"
	botPairFor     = 10 * time.Minute
	botPairTries   = 5
)

type botHub struct {
	mu       sync.Mutex
	loaded   bool
	set      FeishuBotSettings
	api      *feishubot.Client
	conn     *feishubot.Conn
	stop     context.CancelFunc
	info     feishubot.BotInfo
	base     context.Context
	told     map[string]time.Time // sender -> when the bot last refused them (10-minute window)
	pairTold map[string]time.Time // sender -> when the pairing path last answered (short redelivery window)
	names    map[string]string    // open id -> name
	seen     []string             // the messages taken lately (Feishu delivers some again), memory only
	// msgChat is the chat of the messages the bot may use, seen or sent lately (message id -> chat id): what a reply's
	// parent is checked against (checkReplyParent), memory only.
	msgChat idRing
	// handedGroup are the group messages handed to the assistant lately: a recall in a group is only passed on for one
	// of these (botRecalled), memory only.
	handedGroup idRing
	// line is closed once the last message taken has been handed to the assistant (botMessage): they go in the
	// order they came.
	line chan struct{}
	// now is replaced in tests.
	now func() time.Time
}

func (b *botHub) clock() time.Time {
	if b.now != nil {
		return b.now()
	}
	return time.Now()
}

// loadBotsLocked reads the settings once; b.mu is held. The chat's state (bots.feishu.state) is the assistant's.
func (h *Manager) loadBotsLocked(ctx context.Context) {
	b := &h.bots
	if b.loaded {
		return
	}
	b.loaded = true
	b.set = FeishuBotSettings{Features: defaultBotFeatures}
	if h.store != nil {
		if _, err := h.store.Setting(ctx, botSettingsKey, &b.set); err != nil {
			h.logf("bots: load settings: %v", err)
		}
	}
}

func (h *Manager) saveBotSettingsLocked() {
	if h.store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.store.PutSetting(ctx, botSettingsKey, h.bots.set); err != nil {
		h.logf("bots: save settings: %v", err)
	}
}

// botCredentialsLocked: the app's id, secret and domain, from the app's settings, else hub.json (h.bots.mu held).
func (h *Manager) botCredentialsLocked() (appID, secret, domain string, fromConfig bool, err error) {
	s := h.bots.set
	if s.AppID != "" && s.SecretEnc != "" {
		secret, err = h.openKey(s.SecretEnc)
		return s.AppID, secret, s.Domain, false, err
	}
	c := h.cfg.Bots.Feishu
	if c.AppID != "" && c.AppSecret != "" {
		return c.AppID, c.AppSecret, c.Domain, true, nil
	}
	return "", "", "", false, nil
}

// startBots connects the bot when it is switched on (at start, and after the settings change).
func (h *Manager) startBots(ctx context.Context) {
	h.bots.mu.Lock()
	h.bots.base = ctx
	h.loadBotsLocked(ctx)
	h.bots.mu.Unlock()
	h.restartBot()
}

// restartBot drops the connection and, if the bot is on and configured, opens a new one. A member hub keeps it closed:
// Feishu hands each event to one of the connections, and the center's bot serves the group (mesh_group.go).
func (h *Manager) restartBot() {
	member := h.isMember()
	b := &h.bots
	b.mu.Lock()
	defer b.mu.Unlock()
	b.info = feishubot.BotInfo{} // the bot may be another app now: its own id is read again
	if b.stop != nil {
		b.stop()
		b.stop, b.conn = nil, nil
	}
	if !b.set.Enabled || b.base == nil || member {
		b.api = nil
		return
	}
	appID, secret, domain, _, err := h.botCredentialsLocked()
	if err != nil {
		h.logf("bots: open the app secret: %v", err)
		return
	}
	if appID == "" {
		b.api = nil
		return
	}
	b.api = feishubot.New(appID, secret, domain)
	b.conn = feishubot.NewConn(b.api, h.botEvent, h.logf)
	ctx, cancel := context.WithCancel(b.base)
	b.stop = cancel
	conn, api := b.conn, b.api
	go conn.Run(ctx)
	go func() {
		ictx, icancel := context.WithTimeout(ctx, 15*time.Second)
		defer icancel()
		info, err := api.BotInfo(ictx)
		if err != nil {
			h.logf("bots: bot info: %v", err)
			return
		}
		b.mu.Lock()
		if b.api == api {
			b.info = info
		}
		b.mu.Unlock()
	}()
}

// botAPI is the client while the bot is on and paired (nil otherwise), with the settings it runs under (a copy).
func (h *Manager) botAPI() (*feishubot.Client, FeishuBotSettings) {
	b := &h.bots
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.loaded {
		h.loadBotsLocked(context.Background())
	}
	set := b.set.copy()
	if b.api == nil || !b.set.Enabled || b.set.Owner == nil {
		return nil, set
	}
	return b.api, set
}

// nudgeAfter is the setting in minutes: the default while never set, 0 off.
func (s FeishuBotSettings) nudgeAfter() int {
	if s.NudgeAfter == nil {
		return assistantwire.NudgeAfterDefault
	}
	return *s.NudgeAfter
}

// copy is the settings with nothing shared: the owner is changed in place under h.bots.mu.
func (s FeishuBotSettings) copy() FeishuBotSettings {
	s.TopicChats = slices.Clone(s.TopicChats)
	s.AISources = slices.Clone(s.AISources)
	if s.NudgeAfter != nil {
		n := *s.NudgeAfter
		s.NudgeAfter = &n
	}
	if s.Owner != nil {
		o := *s.Owner
		s.Owner = &o
	}
	if s.Pairing != nil {
		p := *s.Pairing
		s.Pairing = &p
	}
	return s
}

// --- the app's settings page ---

type feishuBotView struct {
	Enabled     bool          `json:"enabled"`
	AppID       string        `json:"app_id"`
	Domain      string        `json:"domain"`
	HasSecret   bool          `json:"has_secret"`
	FromConfig  bool          `json:"from_config"` // the credentials are manager.json's
	Features    BotFeatures   `json:"features"`
	Connected   bool          `json:"connected"`
	Error       string        `json:"error,omitempty"`
	LastEventAt int64         `json:"last_event_at,omitempty"`
	BotName     string        `json:"bot_name,omitempty"`
	Owner       *botOwnerView `json:"owner,omitempty"`
	Pairing     *botPairView  `json:"pairing,omitempty"`
	TopicChats  []botChatView `json:"topic_chats,omitempty"` // the group chats the owner opted in
	NudgeAfter  int           `json:"nudge_after"`           // minutes before the one nudge under an unanswered card, 0 off
	// Target and Agents are the assistant's (feishu.view): where the chat's messages go now, the agents bound to
	// threads of the chat; left out while it is not connected.
	Target string                      `json:"target,omitempty"`
	Agents []assistantwire.FeishuAgent `json:"agents,omitempty"`
}

type botOwnerView struct {
	Name     string `json:"name"`
	PairedAt int64  `json:"paired_at"`
}

type botPairView struct {
	Code      string `json:"code"`
	ExpiresAt int64  `json:"expires_at"`
}

func (h *Manager) botView(ctx context.Context) feishuBotView {
	b := &h.bots
	b.mu.Lock()
	h.loadBotsLocked(ctx)
	s := b.set
	appID, _, domain, fromConfig, _ := h.botCredentialsLocked()
	v := feishuBotView{Enabled: s.Enabled, AppID: appID, Domain: feishubot.ResolveDomain(domain), HasSecret: appID != "",
		FromConfig: fromConfig, Features: s.Features, BotName: b.info.Name, NudgeAfter: s.nudgeAfter()}
	if b.conn != nil {
		st := b.conn.State()
		v.Connected, v.Error = st.Connected, st.Error
		if !st.LastEvent.IsZero() {
			v.LastEventAt = st.LastEvent.Unix()
		}
	} else if s.Enabled && appID == "" {
		v.Error = i18n.T(h.botLang(ctx), "hub-bots.no_credentials")
	}
	if s.Owner != nil {
		v.Owner = &botOwnerView{Name: s.Owner.Name, PairedAt: s.Owner.At.Unix()}
	}
	if p := s.Pairing; p != nil && b.clock().Before(p.Expires) {
		v.Pairing = &botPairView{Code: p.Code, ExpiresAt: p.Expires.Unix()}
	}
	for _, c := range s.TopicChats {
		v.TopicChats = append(v.TopicChats, botChatView{ChatID: c.ChatID, Name: c.Name, AddedAt: c.At.Unix(), Home: c.ChatID == s.homeChat()})
	}
	b.mu.Unlock()
	if chat, err := h.feishuChatView(ctx); err == nil {
		v.Target, v.Agents = chat.Target, chat.Agents
	}
	return v
}

func (h *Manager) botRoutes(api *http.ServeMux) {
	h.channelBindRoute(api) // binding a channel carries its secrets: the hub's, never relayed
	api.HandleFunc("GET /api/v1/bots/feishu", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, h.botView(r.Context()))
	})
	api.HandleFunc("PUT /api/v1/bots/feishu", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Enabled   *bool           `json:"enabled"`
			AppID     *string         `json:"app_id"`
			AppSecret *string         `json:"app_secret"`
			Domain    *string         `json:"domain"`
			Features  json.RawMessage `json:"features"`
			// TopicChats replaces the list of group chats the owner opted in; absent leaves it, [] clears it.
			// An entry may carry home: true (the home topic group, "/topics home"); when no entry says anything about home,
			// the one the list has stays.
			TopicChats *[]struct {
				ChatID string `json:"chat_id"`
				Name   string `json:"name"`
				Home   *bool  `json:"home"`
			} `json:"topic_chats"`
			// NudgeAfter is minutes before the one nudge under an unanswered card; 0 turns nudges off, absent leaves it.
			// Read as raw JSON so that anything but a whole number in range gets the same answer (nudgeAfterValue).
			NudgeAfter json.RawMessage `json:"nudge_after"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		nudge, err := nudgeAfterValue(in.NudgeAfter)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		var fs *BotFeatures
		if len(in.Features) > 0 && string(in.Features) != "null" {
			// Over the switches as they are: an app from before a switch (reports) sends the others only, and leaves
			// that one as it was.
			f := h.botFeatures()
			if err := json.Unmarshal(in.Features, &f); err != nil {
				writeErr(w, http.StatusBadRequest, err)
				return
			}
			fs = &f
		}
		var topics []BotChat
		keepHome := true // no entry says anything about home: the list's own stays
		if in.TopicChats != nil {
			topics = []BotChat{}
			for _, c := range *in.TopicChats {
				if c.Home != nil {
					keepHome = false
				}
				topics = append(topics, BotChat{ChatID: c.ChatID, Name: c.Name, Home: c.Home != nil && *c.Home})
			}
			// Everything is checked before anything is written: a list sent with a change that makes it impossible
			// (a new App ID unpairs the owner) must not leave the rest of the request applied.
			if err := checkTopicChats(topics); err != nil {
				writeErr(w, http.StatusBadRequest, err)
				return
			}
			if len(topics) > 0 {
				if err := h.topicChatsPossible(r.Context(), in.AppID); err != nil {
					writeErr(w, http.StatusBadRequest, err)
					return
				}
			}
		}
		if err := h.updateBot(r.Context(), in.Enabled, in.AppID, in.AppSecret, in.Domain, fs); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if topics != nil {
			if err := h.setTopicChats(r.Context(), topics, keepHome); err != nil {
				writeErr(w, http.StatusBadRequest, err)
				return
			}
		}
		if nudge != nil {
			h.setNudgeAfter(r.Context(), *nudge)
		}
		writeJSON(w, h.botView(r.Context()))
	})
	// pair: a code the user sends the bot from Feishu within 10 minutes; whoever sends it becomes the owner.
	api.HandleFunc("POST /api/v1/bots/feishu/pair", func(w http.ResponseWriter, r *http.Request) {
		b := &h.bots
		b.mu.Lock()
		h.loadBotsLocked(r.Context())
		b.set.Pairing = &botPairing{Code: botPairCode(), Expires: b.clock().Add(botPairFor)}
		h.saveBotSettingsLocked()
		b.mu.Unlock()
		h.auditBot(r.Context(), botRecord{action: auditBotPairCode})
		writeJSON(w, h.botView(r.Context()))
	})
	api.HandleFunc("DELETE /api/v1/bots/feishu/owner", func(w http.ResponseWriter, r *http.Request) {
		h.unpairBot(r.Context())
		writeJSON(w, h.botView(r.Context()))
	})
	// test: a message to the owner, to see that the bot reaches them.
	api.HandleFunc("POST /api/v1/bots/feishu/test", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		if err := h.testFeishuBot(ctx); err != nil {
			code := http.StatusBadGateway
			if errors.Is(err, errBotOff) {
				code = http.StatusConflict
			}
			writeErr(w, code, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	})
}

// botPairCode is a new six-digit pairing code.
func botPairCode() string {
	n, _ := rand.Int(rand.Reader, big.NewInt(900000))
	return fmt.Sprintf("%06d", 100000+n.Int64())
}

// unpairBot forgets the owner; the assistant forgets the chat's state (feishu.owner).
func (h *Manager) unpairBot(ctx context.Context) {
	b := &h.bots
	b.mu.Lock()
	h.loadBotsLocked(ctx)
	had := b.set.Owner != nil
	b.set.Owner, b.set.Pairing, b.set.TopicChats, b.set.AISources = nil, nil, nil, nil // the topic chats and the /ai sources were the owner's
	h.saveBotSettingsLocked()
	b.mu.Unlock()
	if had {
		h.auditBot(ctx, botRecord{action: auditBotUnpaired})
	}
	_ = h.notifyAssistant(assistantwire.EventFeishuOwner, assistantwire.FeishuOwner{})
}

var errBotOff = i18n.Err("hub-bots.bot_off")

// botLang is the language of what the bots and the channels write: the request's when the context carries one, else
// the user's (docs/protocol/i18n.md "Protocol").
func (h *Manager) botLang(ctx context.Context) i18n.Lang {
	return i18n.From(ctx).Or(h.userLang())
}

// testFeishuBot sends the owner a card.
func (h *Manager) testFeishuBot(ctx context.Context) error {
	api, set := h.botAPI()
	if api == nil {
		return errBotOff
	}
	l := h.botLang(ctx)
	body := i18n.T(l, "hub-bots.test_body")
	card := feishubot.NewCard("", "", i18n.T(l, "hub-bots.test_title")).Markdown(body)
	_, err := h.botPost(ctx, api, set.Owner.ChatID, "", card, body)
	return err
}

// updateBot applies the app's changes; new credentials are tried (the bot's info) before they are saved.
func (h *Manager) updateBot(ctx context.Context, enabled *bool, appID, secret, domain *string, features *BotFeatures) error {
	b := &h.bots
	b.mu.Lock()
	h.loadBotsLocked(ctx)
	next := b.set.copy()
	b.mu.Unlock()
	reconnect := false
	creds, credsChanged := false, false
	if appID != nil || secret != nil || domain != nil {
		id, dom := next.AppID, next.Domain
		if appID != nil {
			id = strings.TrimSpace(*appID)
		}
		if domain != nil {
			dom = strings.TrimSpace(*domain)
		}
		newSecret := secret != nil && strings.TrimSpace(*secret) != ""
		// The secret kept now: what a new one is compared with (for the audit), and what is used without one.
		var prev string
		if next.SecretEnc != "" {
			var err error
			if prev, err = h.openKey(next.SecretEnc); err != nil && !newSecret {
				return err
			}
		}
		plain := prev
		if newSecret {
			plain = strings.TrimSpace(*secret)
		}
		credsChanged = id != next.AppID || dom != next.Domain || (id != "" && plain != prev)
		if id == "" {
			// Cleared: back to hub.json's, if any.
			next.AppID, next.SecretEnc, next.Domain = "", "", ""
		} else {
			if plain == "" {
				return i18n.Err("hub-bots.needs_secret")
			}
			vctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			_, err := feishubot.New(id, plain, dom).BotInfo(vctx)
			cancel()
			if err != nil {
				return errors.New(i18n.T(h.botLang(ctx), "hub-bots.cannot_reach_app", i18n.Args{"error": err.Error()}))
			}
			sealed, err := h.sealKey(plain)
			if err != nil {
				return err
			}
			next.AppID, next.SecretEnc, next.Domain = id, sealed, dom
		}
		reconnect, creds = true, true
	}
	b.mu.Lock()
	// Onto the settings as they are now: the credentials were checked over the network meanwhile, and a pairing may
	// have come in.
	cur := &b.set
	var records []botRecord
	if creds {
		if credsChanged {
			d := map[string]any{"app_id": next.AppID}
			if next.Domain != "" {
				d["domain"] = next.Domain
			}
			if next.AppID == "" {
				d["cleared"] = true
			}
			if next.AppID != cur.AppID && cur.Owner != nil {
				d["owner_reset"] = true
			}
			records = append(records, botRecord{auditBotCredentials, d})
		}
		if next.AppID != cur.AppID {
			cur.Owner, cur.Pairing, cur.TopicChats, cur.AISources = nil, nil, nil, nil
		}
		cur.AppID, cur.SecretEnc, cur.Domain = next.AppID, next.SecretEnc, next.Domain
	}
	if enabled != nil && *enabled != cur.Enabled {
		cur.Enabled = *enabled
		reconnect = true
		records = append(records, botRecord{auditBotEnabled, map[string]any{"enabled": *enabled}})
	}
	if features != nil {
		if *features != cur.Features {
			records = append(records, botRecord{auditBotFeatures, map[string]any{"features": *features}})
		}
		cur.Features = *features
	}
	h.saveBotSettingsLocked()
	b.mu.Unlock()
	h.auditBot(ctx, records...)
	if reconnect {
		h.restartBot()
	}
	h.sendReport()
	return nil
}
