package voice

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// Nudging a model that owes the user an answer and says nothing (#2297, nudge.go, docs/assistant/voice.md 4.5 rule 9).

// fastNudge makes an answer owed long enough for a nudge in a test's time.
func fastNudge(t *testing.T) {
	setVar(t, &nudgeAfter, 600*time.Millisecond)
	setVar(t, &nudgeQuiet, 200*time.Millisecond)
}

// nudgeText is the nudge for an answer owed to cause (user, result), as a test's call says it (in Chinese).
func nudgeText(cause string) string {
	return prompts.RenderVoiceNudge(prompts.VoiceNudge{Lang: string(i18n.ZhHans), Cause: cause})
}

// nudging matches the nudge for cause, said.
func nudging(cause string) (string, func(map[string]any) bool) {
	text := nudgeText(cause)
	return "the nudge (" + cause + ")", func(f map[string]any) bool { return said(f) && content(f) == text }
}

// nudgesTo is how many nudges the service got so far.
func nudgesTo(up *fakeUpstream) int {
	user, result := nudgeText("user"), nudgeText("result")
	return up.count(func(f map[string]any) bool { return said(f) && (content(f) == user || content(f) == result) })
}

// watchNudges observes upstream frames for several nudge periods, failing as soon
// as the count changes. The full window is needed to prove the absence of a nudge.
func watchNudges(t *testing.T, up *fakeUpstream, want int, what string) {
	t.Helper()
	window := time.NewTimer(3 * nudgeAfter)
	defer window.Stop()
	for {
		if n := nudgesTo(up); n != want {
			t.Fatalf("%s: nudged %d times, want %d", what, n, want)
		}
		select {
		case <-up.frames:
		case <-window.C:
			if n := nudgesTo(up); n != want {
				t.Fatalf("%s: nudged %d times, want %d", what, n, want)
			}
			return
		}
	}
}

func noNudge(t *testing.T, up *fakeUpstream, what string) {
	t.Helper()
	watchNudges(t, up, 0, what)
}

// silentUpstream is a live-dialect service whose model only starts its sessions: it never says or does anything.
func silentUpstream(t *testing.T) *fakeUpstream {
	return newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
		if f["type"] == "session.start" {
			u.send(c, map[string]any{"type": "session.started", "session": map[string]any{}})
		}
	})
}

// The user types a line and the model says nothing: once the answer has been owed nudgeAfter (the user quiet
// nudgeQuiet), the model gets one nudge, and only one, whatever the wait. The next line the user types arms it again.
// The call's log says so, with nothing of what was typed or said.
func TestNudgeAfterATypedLine(t *testing.T) {
	fastNudge(t)
	const typed = "zebra-typed-41 what time is it"
	up := silentUpstream(t)
	pl := testPlugin(t, liveProvider(up), newFakeHub(t).server(t))
	logs := captureLog(t, pl)
	a := dialApp(t, pl, map[string]any{"type": "start"})
	a.until(ofType("ready"))
	began := time.Now()
	a.send(map[string]any{"type": "text", "text": typed})
	up.nextMatch(nudging("user"))
	if took := time.Since(began); took < nudgeAfter {
		t.Fatalf("nudged %v after the line, before nudgeAfter", took)
	}
	watchNudges(t, up, 1, "one answer owed")
	if n := nudgesTo(up); n != 1 {
		t.Fatalf("nudged %d times for one answer owed", n)
	}

	a.send(map[string]any{"type": "text", "text": "zebra-typed-42 hello?"})
	up.nextMatch(nudging("user"))
	if n := nudgesTo(up); n != 2 {
		t.Fatalf("nudged %d times for two answers owed", n)
	}

	logs.wait("call 1: nudge: answer owed for", "after a typed line")
	_ = a.c.Close(websocket.StatusNormalClosure, "bye")
	logs.wait("call 1: conversation closed", "nudges 2")
	for _, line := range logs.lines() {
		if strings.Contains(line, "zebra-typed") || strings.Contains(line, "用户在等") {
			t.Errorf("the log has what was typed or said: %s", line)
		}
	}
}

// The model that answers, in time, by its words, its voice, or a handover (whose result it then speaks about), owes
// nothing: no nudge.
func TestNoNudgeWhenTheModelAnswers(t *testing.T) {
	fastNudge(t)
	for _, tc := range []struct {
		name   string
		answer func(u *fakeUpstream, c *websocket.Conn, n int)
	}{
		{"caption", func(u *fakeUpstream, c *websocket.Conn, _ int) {
			u.send(c, map[string]any{"type": "session.output_transcript.delta", "delta": "现在下午三点。"})
		}},
		{"audio", func(u *fakeUpstream, c *websocket.Conn, _ int) {
			u.send(c, map[string]any{"type": "session.output_audio.delta", "delta": b64(make([]byte, 3200))})
		}},
		{"handover", func(u *fakeUpstream, c *websocket.Conn, n int) {
			if n == 1 {
				delegate(u, c, "dlg1", "明天有什么会")
				return
			}
			u.send(c, map[string]any{"type": "session.output_transcript.delta", "delta": "明天下午三点有会。"})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var n atomic.Int32
			up := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
				switch f["type"] {
				case "session.start":
					u.send(c, map[string]any{"type": "session.started", "session": map[string]any{}})
				case "session.commentary.append":
					if strings.Contains(content(f), "用户在等") || strings.Contains(content(f), "还没告诉用户") {
						return // a nudge: the test fails on it
					}
					k := int(n.Add(1))
					// Receipt of this frame establishes that the line or result was
					// handed over; answer it before the nudge deadline.
					tc.answer(u, c, k)
				}
			})
			hub := newFakeHub(t)
			pl := testPlugin(t, liveProvider(up), hub.server(t))
			logs := captureLog(t, pl)
			a := dialApp(t, pl, map[string]any{"type": "start"})
			a.until(ofType("ready"))
			a.send(map[string]any{"type": "text", "text": "明天有什么会"})
			if tc.name == "handover" {
				up.nextMatch(on("dlg1"))
			}
			noNudge(t, up, tc.name)
			if strings.Contains(logs.String(), "nudge:") {
				t.Fatalf("the log has a nudge:\n%s", logs.String())
			}
		})
	}
}

// What the user said (a caption) is owed an answer once their line is over and the model said nothing after it; not
// when the model began to answer before the caption came (the measured service sends it 1.2–1.4 s after they stop,
// often after the model's answer began, whose audio comes 9–10 times faster than it plays).
func TestNudgeAfterTheUsersCaption(t *testing.T) {
	fastNudge(t)
	setVar(t, &captionQuiet, 200*time.Millisecond)
	for _, answered := range []bool{false, true} {
		name := "silent"
		if answered {
			name = "answered before the caption"
		}
		t.Run(name, func(t *testing.T) {
			const inputFrames = 10
			var heard atomic.Int32
			voiceDone := make(chan *websocket.Conn, 1)
			up := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
				switch f["type"] {
				case "session.start":
					u.send(c, map[string]any{"type": "session.started", "session": map[string]any{}})
				case "session.input_audio.append":
					// speak sends ten 20 ms frames. Receiving the last one proves
					// that the user's whole line reached the model.
					if heard.Add(1) == inputFrames {
						voiceDone <- c
					}
				}
			})
			pl := testPlugin(t, liveProvider(up), newFakeHub(t).server(t))
			logs := captureLog(t, pl)
			a := dialApp(t, pl, map[string]any{"type": "start"})
			a.until(ofType("ready"))
			a.speak(inputFrames*20*time.Millisecond, true)
			var conn *websocket.Conn
			select {
			case conn = <-voiceDone:
			case <-time.After(5 * time.Second):
				t.Fatal("the model did not receive the user's whole line")
			}
			if answered {
				up.send(conn, map[string]any{"type": "session.output_audio.delta", "delta": b64(make([]byte, 3200))})
				a.heardAudio() // the model's answer reached the app before the late caption
			}
			up.send(conn, map[string]any{"type": "session.input_transcript.delta", "delta": "现在几点了"})
			a.until(finalLine("user", "现在几点了"))
			if answered {
				noNudge(t, up, name)
				return
			}
			up.nextMatch(nudging("user"))
			logs.wait("call 1: nudge: answer owed for", "after the user's caption")
		})
	}
}

// What is handed to the model, a turn's result on its delegation or a tool's result on its call, is owed an answer:
// the model that says nothing of it is nudged to.
func TestNudgeForAResult(t *testing.T) {
	fastNudge(t)
	t.Run("a delegation's result", func(t *testing.T) {
		up := delegatingUpstream(t) // it hands the line over, and says nothing of the result
		pl := testPlugin(t, liveProvider(up), newFakeHub(t).server(t))
		logs := captureLog(t, pl)
		a := dialApp(t, pl, map[string]any{"type": "start"})
		a.until(ofType("ready"))
		a.send(map[string]any{"type": "text", "text": "明天有什么会"})
		if got := content(up.nextMatch(on("dlg1"))); !strings.Contains(got, "明天下午三点有会") {
			t.Fatalf("result: %s", got)
		}
		up.nextMatch(nudging("result"))
		logs.wait("call 1: nudge: answer owed for", "after a result")
	})
	t.Run("a tool's result", func(t *testing.T) {
		up := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
			if f["type"] != "session.start" {
				return
			}
			u.send(c, map[string]any{"type": "session.started", "session": map[string]any{}})
			u.send(c, map[string]any{"type": "session.delegation.created", "delegation": map[string]any{"id": "dlg9", "target": "client"}})
			u.send(c, map[string]any{"type": "response.event", "delegation_id": "dlg9", "event": map[string]any{
				"type": "function_call", "call_id": "c1", "name": "search_knowledge", "arguments": `{"query":"咖啡"}`,
			}})
		})
		pl := testPlugin(t, Config{Providers: []Provider{{ID: "rt", URL: up.url(), Models: []string{"m1"}, Tools: new(true)}}},
			newFakeHub(t).server(t))
		logs := captureLog(t, pl)
		a := dialApp(t, pl, map[string]any{"type": "start"})
		a.until(ofType("ready"))
		up.nextMatch(on("dlg9"))
		up.nextMatch(nudging("result"))
		logs.wait("call 1: nudge: answer owed for", "after a tool result")
	})
}

// While a run of the call's is worked on, what the user says is not nudged for (they wait for the run; heartbeats tell
// them it goes on); the run's own result is, once the model says nothing of it.
func TestNudgeWhileARunWorks(t *testing.T) {
	fastNudge(t)
	hub := agentHub(t)
	hub.manual = true
	// Its model hands "跑…" over, and says nothing to anything else.
	up := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
		switch f["type"] {
		case "session.start":
			u.send(c, map[string]any{"type": "session.started", "session": map[string]any{}})
		case "session.commentary.append":
			if f["delegation_id"] == nil && strings.HasPrefix(content(f), "跑") {
				delegate(u, c, "dlg1", content(f))
			}
		}
	})
	pl := testPlugin(t, liveProvider(up), hub.server(t))
	logs := captureLog(t, pl)
	a := dialApp(t, pl, boundStart("a1", nil))
	a.until(ofType("ready"))
	a.send(map[string]any{"type": "text", "text": "跑一下全部测试"})
	m := hub.waitSent(1)
	hub.edit("a1", func(s *fakeSession) { s.status, s.activity = "working", "在跑 npm test" })
	a.runStates(m.ID, runWorking)
	a.send(map[string]any{"type": "text", "text": "好的谢谢"})
	noNudge(t, up, "the user's line while the run works")

	hub.add("a1", proto.Item{Role: "assistant", Kind: proto.KindText, Text: "全部通过。"})
	hub.end("a1")
	if got := content(up.nextMatch(on("dlg1"))); !strings.Contains(got, "全部通过") {
		t.Fatalf("result: %s", got)
	}
	up.nextMatch(nudging("result"))
	logs.wait("call 1: nudge: answer owed for", "after a result")
	if n := nudgesTo(up); n != 1 {
		t.Fatalf("nudged %d times", n)
	}
}

// While a card is asked by voice the model waits for the user's answer: no nudge. Answered on the screen, the model is
// told so, and is nudged when it says nothing of that.
func TestNoNudgeWhileACardIsAsked(t *testing.T) {
	fastNudge(t)
	hub := agentHub(t)
	hub.cards = []proto.Interaction{dbQuestion("q1", "a1")}
	up := boundUpstream(t, false)
	pl := testPlugin(t, liveProvider(up), hub.server(t))
	a := dialApp(t, pl, boundStart("a1", nil))
	a.until(ofType("ready"))
	up.nextMatch(saying("数据库用哪个"))
	a.until(askFrom("q1", askAsking, ""))
	noNudge(t, up, "a card asked")

	hub.subscribed("a1")
	hub.answerOnScreen("q1")
	a.until(askFrom("q1", askReleased, byElsewhere))
	up.nextMatch(saying("已经在屏幕上或者别的地方答了"))
	up.nextMatch(nudging("result"))
}

// The app that records without echo cancellation has the model's own speech captioned as the user's (#5789): the
// user's lines and their quiet cannot be told, and nothing is nudged.
func TestNoNudgeWithoutEchoCancellation(t *testing.T) {
	fastNudge(t)
	up := silentUpstream(t)
	pl := testPlugin(t, liveProvider(up), newFakeHub(t).server(t))
	a := dialApp(t, pl, map[string]any{"type": "start", "echo_cancellation": false})
	a.until(ofType("ready"))
	a.send(map[string]any{"type": "text", "text": "现在几点了"})
	up.next("session.commentary.append")
	noNudge(t, up, "no echo cancellation")
}

// A paused upstream is not nudged, nor woken to be: what the session that paused owed went with it.
func TestNoNudgeWhilePaused(t *testing.T) {
	quick(t)
	fastNudge(t)
	setVar(t, &nudgeAfter, 1500*time.Millisecond) // past pauseAfter
	hub := workingAgent(t)
	up := newSessionsUp(t, true, nil)
	pl := testPlugin(t, liveProvider(up.fakeUpstream), hub.server(t))
	a := dialApp(t, pl, boundStart("a1", nil))
	a.until(ofType("ready"))
	a.send(map[string]any{"type": "text", "text": "状态：到哪了"})
	if got := content(up.find(t, 0, "the status", func(f map[string]any) bool { return f["delegation_id"] == "dlg1" })); !strings.Contains(got, "还在做") {
		t.Fatalf("status: %s", got)
	}
	a.until(linkIn(linkPaused))
	noNudge(t, up.fakeUpstream, "paused")
	if n := up.count(); n != 1 {
		t.Fatalf("%d sessions: a nudge woke the upstream", n)
	}
}

// When a nudge is due, and every case that holds it back (nudge.go).
func TestNudgeDue(t *testing.T) {
	now := time.Now()
	owedAt := now.Add(-nudgeAfter - time.Second)
	base := func() *conversation {
		c := &conversation{ready: true, up: newFakeLink()}
		c.connectedAt = owedAt.Add(-time.Minute)
		c.gate.user = owedAt
		return c
	}
	for _, tc := range []struct {
		name   string
		result bool
		set    func(c *conversation)
		due    bool
	}{
		{"owed and quiet", false, nil, true},
		{"a result owed", true, nil, true},
		{"nothing owed", false, func(c *conversation) { c.owed = owedAnswer{} }, false},
		{"nudged already", false, func(c *conversation) { c.owed.nudged = true }, false},
		{"not owed long enough", false, func(c *conversation) { c.owed.at = now.Add(-nudgeAfter + time.Second) }, false},
		{"the model's speech played out lately", false, func(c *conversation) { c.pace.playEnd = now.Add(-time.Second) }, false},
		{"its session came up lately", false, func(c *conversation) { c.connectedAt = now.Add(-time.Second) }, false},
		{"the user's voice lately", false, func(c *conversation) { c.pace.voice = now.Add(-nudgeQuiet / 2) }, false},
		{"the user's caption grew lately", false, func(c *conversation) { c.userAt = now.Add(-nudgeQuiet / 2) }, false},
		{"the user typed lately", false, func(c *conversation) { c.gate.user = now.Add(-nudgeQuiet / 2) }, false},
		{"closed", false, func(c *conversation) { c.closed = true }, false},
		{"reconnecting", false, func(c *conversation) { c.ready, c.up = false, nil }, false},
		{"paused", false, func(c *conversation) { c.ready, c.up, c.sus.state = false, nil, linkPaused }, false},
		{"waking", false, func(c *conversation) { c.sus.state = linkWaking }, false},
		{"no echo cancellation", false, func(c *conversation) { c.gate.echoes = true }, false},
		{"a card asked", false, func(c *conversation) { c.askCur = "q1" }, false},
		{"something to say", false, func(c *conversation) { c.queue = []*sentWords{{}} }, false},
		{"the bound conversation works", false, func(c *conversation) { c.sus.works = true }, false},
		{"a run works", false, func(c *conversation) { c.open = []openTask{{id: "r1", task: "x", state: runWorking}} }, false},
		{"a run is held", false, func(c *conversation) { c.open = []openTask{{id: "r1", task: "x", state: runHeld}} }, false},
		{"a run waits on the user", false, func(c *conversation) { c.open = []openTask{{id: "r1", task: "x", state: runNeedsYou}} }, true},
		{"a result while a run works", true, func(c *conversation) {
			c.sus.works, c.open = true, []openTask{{id: "r1", task: "x", state: runWorking}}
		}, true},
	} {
		c := base()
		c.owed = owedAnswer{at: owedAt, cause: owedTyped, result: tc.result}
		if tc.set != nil {
			tc.set(c)
		}
		if got := c.nudgeDue(now); got != tc.due {
			t.Errorf("%s: due %v, want %v", tc.name, got, tc.due)
		}
	}
}

// What makes an answer owed, and what answers it (nudge.go).
func TestAnswerOwed(t *testing.T) {
	now := time.Now()
	spoke := now.Add(-2 * time.Second) // the user stopped saying their line
	for _, tc := range []struct {
		name string
		set  func(c *conversation)
		owed bool
	}{
		{"nothing since", nil, true},
		{"the model spoke before they stopped", func(c *conversation) { c.modelAt = spoke.Add(-time.Second) }, true},
		{"the model answered after they stopped", func(c *conversation) { c.modelAt = spoke.Add(time.Second) }, false},
		{"the model spoke on over them", func(c *conversation) { c.pace.playEnd = spoke.Add(time.Second) }, false},
	} {
		c := &conversation{}
		if tc.set != nil {
			tc.set(c)
		}
		c.userLineDone(spoke)
		if owed := !c.owed.at.IsZero(); owed != tc.owed || owed && (c.owed.cause != owedCaption || c.owed.result) {
			t.Errorf("a caption, %s: %+v", tc.name, c.owed)
		}
	}

	c := &conversation{ready: true, up: newFakeLink()}
	for _, tc := range []struct {
		name  string
		s     *sentWords
		cause string // "" none
	}{
		{"a result", &sentWords{delegation: "dlg1"}, owedResult},
		{"a say", &sentWords{}, owedResult},
		{"a tool's result", &sentWords{delegation: "dlg1", onCall: true}, owedToolResult},
		{"a heartbeat", &sentWords{prio: prioHeartbeat}, ""},
		{"the nudge", &sentWords{nudge: true}, ""},
	} {
		c.owed = owedAnswer{}
		c.handedOver(tc.s, now)
		if c.owed.cause != tc.cause || tc.cause != "" && !c.owed.result {
			t.Errorf("handed over %s: %+v", tc.name, c.owed)
		}
	}
	c.ready, c.up = false, nil
	c.owed = owedAnswer{}
	if c.handedOver(&sentWords{}, now); !c.owed.at.IsZero() {
		t.Errorf("owed by a session that is not up: %+v", c.owed)
	}

	// The rest of an answer the app cut off (interrupt) is dropped: the user never hears it, so it answers nothing.
	// Audio that goes to the app does.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cut := &conversation{ctx: ctx, out: make(chan outFrame, 1), dropAudio: true, lastAudio: time.Now()}
	cut.owe(time.Now(), owedTyped, false)
	if cut.handle(0, event{kind: evAudio, audio: make([]byte, 320)}); cut.owed.at.IsZero() || !cut.modelAt.IsZero() {
		t.Errorf("answered by audio that was dropped: %+v", cut.owed)
	}
	cut.dropAudio = false
	if cut.handle(0, event{kind: evAudio, audio: make([]byte, 320)}); !cut.owed.at.IsZero() || cut.modelAt.IsZero() {
		t.Errorf("not answered by audio the app got: %+v", cut.owed)
	}

	c.owe(now, owedTyped, false)
	c.owed.nudged = true
	c.owe(now, owedCaption, false)
	if c.owed.nudged {
		t.Error("a new answer owed is not armed again")
	}
	c.modelActed(now)
	if !c.owed.at.IsZero() || !c.modelAt.Equal(now) {
		t.Errorf("answered: %+v", c.owed)
	}
}
