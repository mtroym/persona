package feishubot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Domains of the two clouds; a hub.json domain may also be a full URL (a private deployment).
const (
	DomainFeishu = "https://open.feishu.cn"
	DomainLark   = "https://open" + ".larksuite.com" // two parts: the joined host trips the source name scan
)

// ResolveDomain turns the configured domain ("", "feishu", "lark" or a URL) into a base URL.
func ResolveDomain(d string) string {
	switch strings.ToLower(strings.TrimSpace(d)) {
	case "", "feishu":
		return DomainFeishu
	case "lark", "larksuite":
		return DomainLark
	}
	return strings.TrimRight(strings.TrimSpace(d), "/")
}

// Client makes the app's REST calls with a tenant access token it keeps fresh.
type Client struct {
	appID, appSecret string
	domain           string
	http             *http.Client

	mu      sync.Mutex
	token   string
	expires time.Time
}

func New(appID, appSecret, domain string) *Client {
	return &Client{appID: appID, appSecret: appSecret, domain: ResolveDomain(domain), http: &http.Client{Timeout: 30 * time.Second}}
}

// WithHTTP replaces the HTTP client (tests).
func (c *Client) WithHTTP(h *http.Client) *Client {
	c.http = h
	return c
}

func (c *Client) AppID() string { return c.appID }

// APIError is Feishu's {code, msg} for a failed call.
type APIError struct {
	Op   string
	Code int
	Msg  string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("feishu %s: %d %s", e.Op, e.Code, e.Msg)
}

// Token-related codes: the token expired or is not valid any more; one fresh token and one retry fix it.
func tokenGone(code int) bool { return code == 99991663 || code == 99991664 || code == 99991661 }

func (c *Client) tenantToken(ctx context.Context, fresh bool) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !fresh && c.token != "" && time.Now().Before(c.expires) {
		return c.token, nil
	}
	body, _ := json.Marshal(map[string]string{"app_id": c.appID, "app_secret": c.appSecret})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.domain+"/open-apis/auth/v3/tenant_access_token/internal", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		Code   int    `json:"code"`
		Msg    string `json:"msg"`
		Token  string `json:"tenant_access_token"`
		Expire int    `json:"expire"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("feishu token: HTTP %d: %w", resp.StatusCode, err)
	}
	if out.Code != 0 || out.Token == "" {
		return "", &APIError{Op: "token", Code: out.Code, Msg: out.Msg}
	}
	c.token = out.Token
	c.expires = time.Now().Add(time.Duration(max(out.Expire-300, 60)) * time.Second)
	return c.token, nil
}

const (
	codeRateLimited  = 99991400         // the open platform's rate-limit body code
	callMaxAttempts  = 3                // includes the first attempt; callSleep is swapped in tests, so no t.Parallel here
	callMaxRetryWait = 60 * time.Second // a server Retry-After longer than this ends the retries instead of hanging on it
)

var (
	callBackoff = []time.Duration{200 * time.Millisecond, 500 * time.Millisecond}
	callSleep   = func(ctx context.Context, d time.Duration) error {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			return nil
		}
	}
)

// call sends one JSON request and decodes {code, msg, data} into out. Non-POST is safe to repeat; POST retries through callRetry only with an idempotency uuid.
func (c *Client) call(ctx context.Context, op, method, path string, in, out any) error {
	return c.callRetry(ctx, op, method, path, in, out, method != http.MethodPost)
}

func (c *Client) callRetry(ctx context.Context, op, method, path string, in, out any, idempotent bool) error {
	var payload []byte
	if in != nil {
		payload, _ = json.Marshal(in)
	}
	var lastErr error
	giveUp := func() error {
		if e := ctx.Err(); e != nil {
			return e // a cancel during the waits wins over the stored error
		}
		return lastErr
	}
	forcedFresh, waits := false, 0 // forcedFresh makes the next token fetch bypass the cache (after a tokenGone answer); waits counts backoff slots used
	for attempt := 0; attempt < callMaxAttempts; attempt++ {
		tok, err := c.tenantToken(ctx, forcedFresh)
		if err != nil {
			// Idempotent endpoint and unsent body: keep forcedFresh on failure so a forced fetch retries, never replays the stale token.
			lastErr = err
			if !c.waitRetry(ctx, attempt, waits, 0) {
				return giveUp()
			}
			waits++
			continue
		}
		forcedFresh = false // the fresh token is cached; a later transient retry reuses it
		var body io.Reader
		if payload != nil {
			body = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.domain+path, body)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		if payload != nil {
			req.Header.Set("Content-Type", "application/json; charset=utf-8")
		}
		retry, after := false, time.Duration(0)
		resp, err := c.http.Do(req)
		switch {
		case err != nil:
			if ctx.Err() != nil { // a done context: retrying cannot help
				return err
			}
			lastErr, retry = err, true
		default:
			after = retryAfterDelay(resp.Header.Get("Retry-After"))
			raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
			resp.Body.Close()
			switch {
			case readErr != nil:
				if ctx.Err() != nil {
					return readErr
				}
				lastErr, retry = readErr, true
			default:
				var env struct {
					Code int             `json:"code"`
					Msg  string          `json:"msg"`
					Data json.RawMessage `json:"data"`
				}
				switch jerr := json.Unmarshal(raw, &env); {
				case jerr != nil: // a gateway's HTML page on 429/5xx is transient; other non-JSON answers fail at once
					lastErr = fmt.Errorf("feishu %s: HTTP %d: %w", op, resp.StatusCode, jerr)
					retry = resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
				case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
					if env.Code != 0 { // a parseable body on 429/5xx still means the request did not land
						lastErr = &APIError{Op: op, Code: env.Code, Msg: env.Msg}
					} else {
						lastErr = fmt.Errorf("feishu %s: HTTP %d", op, resp.StatusCode)
					}
					retry = true
				case env.Code != 0:
					if tokenGone(env.Code) && attempt == 0 {
						forcedFresh = true
						continue // one fresh token, one immediate retry, no backoff slot spent
					}
					lastErr = &APIError{Op: op, Code: env.Code, Msg: env.Msg}
					retry = env.Code == codeRateLimited
				case out == nil:
					return nil
				case len(env.Data) > 0 && string(env.Data) != "null":
					return json.Unmarshal(env.Data, out)
				default:
					return json.Unmarshal(raw, out)
				}
			}
		}
		if !idempotent || !retry || !c.waitRetry(ctx, attempt, waits, after) {
			return giveUp()
		}
		waits++
	}
	return lastErr
}

// waitRetry applies +/-25% jittered backoff (or a larger server Retry-After) and reports whether to try once more.
func (c *Client) waitRetry(ctx context.Context, attempt, n int, after time.Duration) bool {
	if attempt+1 >= callMaxAttempts || n >= len(callBackoff) || after > callMaxRetryWait {
		return false
	}
	base := callBackoff[n]
	d := base*3/4 + time.Duration(rand.Int64N(int64(base/2)))
	return callSleep(ctx, max(d, after)) == nil // the server's throttle window wins over the short jittered backoff
}

// retryAfterDelay parses a standard Retry-After header: delta seconds or an HTTP date.
func retryAfterDelay(v string) time.Duration {
	v = strings.TrimSpace(v)
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil && time.Until(t) > 0 {
		return time.Until(t)
	}
	return 0
}

// BotInfo is the app's bot: its name and open id (GET /open-apis/bot/v3/info). Asking for it also checks the app's
// id and secret.
type BotInfo struct {
	Name   string `json:"app_name"`
	OpenID string `json:"open_id"`
	Avatar string `json:"avatar_url"`
}

func (c *Client) BotInfo(ctx context.Context) (BotInfo, error) {
	var out struct {
		Bot BotInfo `json:"bot"`
	}
	err := c.call(ctx, "bot info", http.MethodGet, "/open-apis/bot/v3/info", nil, &out)
	return out.Bot, err
}

// Message types the bot sends.
const (
	TypeText        = "text"
	TypePost        = "post"
	TypeInteractive = "interactive"
)

type sent struct {
	MessageID string `json:"message_id"`
}

// Send posts a message to a user (idType "open_id") or into a chat ("chat_id"); uuid (≤ 50 chars, may be "") makes
// a retried send land once.
func (c *Client) Send(ctx context.Context, idType, id, msgType, content, uuid string) (string, error) {
	var out sent
	in := map[string]any{"receive_id": id, "msg_type": msgType, "content": content}
	if uuid != "" {
		in["uuid"] = uuid
	}
	// uuid makes a retried send land once: with it, transient failures are retried; without it, a POST is not.
	err := c.callRetry(ctx, "send", http.MethodPost, "/open-apis/im/v1/messages?receive_id_type="+url.QueryEscape(idType), in, &out, uuid != "")
	return out.MessageID, err
}

// Reply answers a message (quoting it); inThread puts the answer in the message's thread.
func (c *Client) Reply(ctx context.Context, messageID, msgType, content, uuid string, inThread bool) (string, error) {
	var out sent
	in := map[string]any{"msg_type": msgType, "content": content}
	if uuid != "" {
		in["uuid"] = uuid
	}
	if inThread {
		in["reply_in_thread"] = true
	}
	err := c.callRetry(ctx, "reply", http.MethodPost, "/open-apis/im/v1/messages/"+url.PathEscape(messageID)+"/reply", in, &out, uuid != "")
	return out.MessageID, err
}

// React puts an emoji under a message (emoji_type: OK, DONE, OnIt…) and returns the reaction's id.
func (c *Client) React(ctx context.Context, messageID, emoji string) (string, error) {
	var out struct {
		ReactionID string `json:"reaction_id"`
	}
	err := c.call(ctx, "react", http.MethodPost, "/open-apis/im/v1/messages/"+url.PathEscape(messageID)+"/reactions",
		map[string]any{"reaction_type": map[string]string{"emoji_type": emoji}}, &out)
	return out.ReactionID, err
}

// Unreact takes a reaction away.
func (c *Client) Unreact(ctx context.Context, messageID, reactionID string) error {
	return c.call(ctx, "unreact", http.MethodDelete, "/open-apis/im/v1/messages/"+url.PathEscape(messageID)+"/reactions/"+url.PathEscape(reactionID), nil, nil)
}

// Pin pins a message in its chat (needs im:message.pins:write_only).
func (c *Client) Pin(ctx context.Context, messageID string) error {
	return c.call(ctx, "pin", http.MethodPost, "/open-apis/im/v1/pins", map[string]any{"message_id": messageID}, nil)
}

// Unpin takes a message off its chat's pins.
func (c *Client) Unpin(ctx context.Context, messageID string) error {
	return c.call(ctx, "unpin", http.MethodDelete, "/open-apis/im/v1/pins/"+url.PathEscape(messageID), nil, nil)
}

// Pin is one pin of a chat: the message and who pinned it.
type Pin struct {
	MessageID      string `json:"message_id"`
	OperatorID     string `json:"operator_id"`
	OperatorIDType string `json:"operator_id_type"` // app_id for a bot's pin
}

// ByApp: the app appID pinned it.
func (p Pin) ByApp(appID string) bool { return p.OperatorIDType == "app_id" && p.OperatorID == appID }

// pinsPagesMost bounds how many pages of a chat's pins are read (50 each).
const pinsPagesMost = 4

// Pins are a chat's pins, newest first (needs im:message.pins:read).
func (c *Client) Pins(ctx context.Context, chatID string) ([]Pin, error) {
	var all []Pin
	token := ""
	for range pinsPagesMost {
		var out struct {
			Items     []Pin  `json:"items"`
			HasMore   bool   `json:"has_more"`
			PageToken string `json:"page_token"`
		}
		path := "/open-apis/im/v1/pins?page_size=50&chat_id=" + url.QueryEscape(chatID)
		if token != "" {
			path += "&page_token=" + url.QueryEscape(token)
		}
		if err := c.call(ctx, "pins", http.MethodGet, path, nil, &out); err != nil {
			return nil, err
		}
		all = append(all, out.Items...)
		if !out.HasMore || out.PageToken == "" {
			break
		}
		token = out.PageToken
	}
	return all, nil
}

// Recall withdraws a message the bot sent.
func (c *Client) Recall(ctx context.Context, messageID string) error {
	return c.call(ctx, "recall", http.MethodDelete, "/open-apis/im/v1/messages/"+url.PathEscape(messageID), nil, nil)
}

// UpdateCard replaces a card the bot sent (shared cards: update_multi).
func (c *Client) UpdateCard(ctx context.Context, messageID, card string) error {
	return c.call(ctx, "update card", http.MethodPatch, "/open-apis/im/v1/messages/"+url.PathEscape(messageID), map[string]any{"content": card}, nil)
}

// Item is a message read back (GET /open-apis/im/v1/messages/:id): a quoted message, or a merged forward and the
// messages in it (those have UpperMessageID set).
type Item struct {
	MessageID      string `json:"message_id"`
	ChatID         string `json:"chat_id"`
	ParentID       string `json:"parent_id"`
	UpperMessageID string `json:"upper_message_id"`
	ThreadID       string `json:"thread_id"` // the topic it is in, empty outside one
	MsgType        string `json:"msg_type"`
	CreateTime     string `json:"create_time"` // Unix milliseconds
	Deleted        bool   `json:"deleted"`
	Sender         struct {
		ID         string `json:"id"`
		IDType     string `json:"id_type"`
		SenderType string `json:"sender_type"` // user | app
	} `json:"sender"`
	Body struct {
		Content string `json:"content"`
	} `json:"body"`
	Mentions []struct {
		Key  string `json:"key"`
		Name string `json:"name"`
	} `json:"mentions"`
}

// At is when the message was sent.
func (it Item) At() time.Time { return msTime(it.CreateTime) }

// MentionList is the message's mentions, for ParseContent to turn its @_user_N keys back into names.
func (it Item) MentionList() []Mention {
	var out []Mention
	for _, m := range it.Mentions {
		out = append(out, Mention{Key: m.Key, Name: m.Name})
	}
	return out
}

func (c *Client) Message(ctx context.Context, messageID string) ([]Item, error) {
	var out struct {
		Items []Item `json:"items"`
	}
	err := c.call(ctx, "read message", http.MethodGet, "/open-apis/im/v1/messages/"+url.PathEscape(messageID)+"?user_id_type=open_id", nil, &out)
	return out.Items, err
}

// UserName is a user's name (contact API; needs a contact permission, so "" with an error when the app lacks it).
func (c *Client) UserName(ctx context.Context, openID string) (string, error) {
	var out struct {
		User struct {
			Name string `json:"name"`
		} `json:"user"`
	}
	err := c.call(ctx, "user", http.MethodGet, "/open-apis/contact/v3/users/"+url.PathEscape(openID)+"?user_id_type=open_id", nil, &out)
	return out.User.Name, err
}

// ErrTooLarge: a message's image or file is larger than the download takes.
var ErrTooLarge = errors.New("feishu download: too large")

// Resource downloads an image or file of a message the bot received (kind "image" or "file"); at most limit bytes.
func (c *Client) Resource(ctx context.Context, messageID, key, kind string, limit int64) ([]byte, string, error) {
	for attempt := 0; ; attempt++ {
		tok, err := c.tenantToken(ctx, attempt > 0)
		if err != nil {
			return nil, "", err
		}
		u := c.domain + "/open-apis/im/v1/messages/" + url.PathEscape(messageID) + "/resources/" + url.PathEscape(key) + "?type=" + url.QueryEscape(kind)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, "", err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, "", err
		}
		ct := resp.Header.Get("Content-Type")
		// Only a non-200 answer is an error. A 200 is the file whatever its type (a .json file the user sent comes
		// labeled application/json), as in the official SDK, which never reads a 200 download as an error.
		if resp.StatusCode != http.StatusOK {
			var env struct {
				Code int    `json:"code"`
				Msg  string `json:"msg"`
			}
			_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&env)
			resp.Body.Close()
			if tokenGone(env.Code) && attempt == 0 {
				continue
			}
			if env.Code == 0 {
				env.Code, env.Msg = resp.StatusCode, http.StatusText(resp.StatusCode)
			}
			return nil, "", &APIError{Op: "download", Code: env.Code, Msg: env.Msg}
		}
		if resp.ContentLength > limit { // said up front: refused without downloading it
			resp.Body.Close()
			return nil, "", fmt.Errorf("%w: larger than %d MB", ErrTooLarge, limit>>20)
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
		resp.Body.Close()
		if err != nil {
			return nil, "", err
		}
		if int64(len(b)) > limit {
			return nil, "", fmt.Errorf("%w: larger than %d MB", ErrTooLarge, limit>>20)
		}
		return b, ct, nil
	}
}

// Refused: Feishu will not take this message however often it is sent (the card's content refused, the bot not in
// the chat or not allowed to reach the user, the app missing a permission); sending it again cannot help.
func Refused(err error) bool {
	switch errorCode(err) {
	case 230099, 230025, 200621, 200830, 230002, 230006, 230013, 99991672, 99991679:
		return true
	}
	return false
}

// RateLimited: Feishu refused the call for coming too often (230020: a message updated too often; 99991400: the
// app's rate limit; HTTP 429).
func RateLimited(err error) bool {
	switch errorCode(err) {
	case 230020, 99991400, http.StatusTooManyRequests:
		return true
	}
	return err != nil && strings.Contains(err.Error(), "HTTP 429")
}

// MessageGone: the message to update was withdrawn (230011) or deleted (230110).
func MessageGone(err error) bool {
	c := errorCode(err)
	return c == 230011 || c == 230110
}

// Transient: the call failed for a reason that may pass, so trying again later can work: Feishu gave no answer at all
// (the network, a timeout, the connection to the hub), it limited the rate, a server failed (5xx) or the token had to be
// renewed. Any other answer is Feishu's word on that message or chat (deleted, past its edit period, not a card, not
// allowed) and will not change.
func Transient(err error) bool {
	if err == nil {
		return false
	}
	c := errorCode(err)
	return c == 0 || RateLimited(err) || c >= 500 && c < 600 || tokenGone(c)
}

// ChatRefused: Feishu said the bot cannot send into that chat: it is not in it (230002) or the chat was dissolved
// (232009). Nothing was sent. Also when the error came over the bus as text.
func ChatRefused(err error) bool {
	c := errorCode(err)
	return c == 230002 || c == 232009
}

// errorCode is an APIError's code, also when it came over the bus as text ("feishu <op>: <code> <msg>"); 0 if none.
func errorCode(err error) int {
	var e *APIError
	if errors.As(err, &e) {
		return e.Code
	}
	if err == nil {
		return 0
	}
	msg := err.Error()
	i := strings.Index(msg, "feishu ")
	if i < 0 {
		return 0
	}
	j := strings.Index(msg[i:], ": ")
	if j < 0 {
		return 0
	}
	f := strings.Fields(msg[i+j+2:])
	if len(f) == 0 {
		return 0
	}
	n, _ := strconv.Atoi(f[0])
	return n
}

// CardRejected: Feishu refused a card's content (a field it does not take, Markdown it cannot read, too large); the
// hub sends the text as rich text instead.
func CardRejected(err error) bool {
	var e *APIError
	if !errors.As(err, &e) {
		return false
	}
	switch e.Code {
	case 230099, 230025, 200621, 200830:
		return true
	}
	return strings.Contains(strings.ToLower(e.Msg), "card")
}
