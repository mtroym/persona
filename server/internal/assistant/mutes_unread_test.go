package assistant

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// mutes hands the service the mutes as the hub does (fleet.mutes, after a handshake and after every change).
func (ft *fleetTest) mutes(p Pins) {
	ft.t.Helper()
	raw, err := json.Marshal(assistantwire.FleetMutes{Mutes: &assistantwire.Mutes{Spaces: p.Spaces, Sessions: p.Sessions, Allowed: p.Allowed}})
	if err != nil {
		ft.t.Fatal(err)
	}
	ft.s.FleetEvent(context.Background(), assistantwire.EventMutes, raw)
}

// The mutes are the hub's: the manager takes them from fleet.mutes, never from its own settings, and has none before
// the hub hands them over.
func TestMutesComeFromTheHub(t *testing.T) {
	st := newFleetMem()
	_ = st.PutSetting(context.Background(), "mutes", Pins{Spaces: []string{}, Sessions: []string{"mac/s1"}}) // a copy left in the shared database
	ft := newFleetTestOn(t, st)
	if ft.s.muted("mac", "s1") {
		t.Fatal("muted by the manager's own settings before the hub said anything")
	}
	ft.mutes(Pins{Sessions: []string{"mac/s2"}})
	if ft.s.muted("mac", "s1") || !ft.s.muted("mac", "s2") {
		t.Fatal("not the hub's mutes")
	}
	ft.s.FleetEvent(context.Background(), assistantwire.EventMutes, json.RawMessage(`{}`)) // an older hub's: nothing to take
	if !ft.s.muted("mac", "s2") {
		t.Fatal("an event without the mutes dropped them")
	}
	ft.mutes(Pins{})
	if ft.s.muted("mac", "s2") {
		t.Fatal("unmuting did not take")
	}
	ft.s.tell(permission("ix_1"))
	if got := ft.pushesOf(assistantwire.NotifyInput, 1, time.Second); len(got) != 1 {
		t.Fatalf("an unmuted agent's card was not pushed: %+v", got)
	}
}
