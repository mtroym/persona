package hub

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/link"
	"github.com/nwverse/persona/server/internal/proto"
)

// A computer the user has not said is theirs yet (docs/assistant/onboarding.md 4.2 "Your computer ↔ hub", 5.3). Persona-managed keys go
// to every node and the assistant falls back to the first machine, so a forwarded pairing link must not be enough to
// get them: a computer added from a signed-in device is pending until the user confirms it, and while it is
//   - it gets no Persona-managed keys and nothing it asks of the assistant is answered (assistant/machine_trust.go);
//   - it is not the assistant's machine (defaultAssistantMachine, the hello's machines) nor one the assistant lists
//     or hands work to (devices.list), and no agent starts there (POST /api/v1/sessions);
//   - it is removed after pendingFor (its hub.json entry, its row, its Persona Link credentials).
// The apps still see it, with trust "pending", to confirm or reject it. Postgres has the rows (machine_trust: no row is
// a confirmed computer, so every machine from before this and the ones /admin adds are confirmed) and an audit
// (machine_audit).

// pendingFor is how long a computer may wait to be confirmed.
const pendingFor = 24 * time.Hour

// trustSweepEvery is how often the computers that waited too long are looked for.
const trustSweepEvery = 10 * time.Minute

// trustRetryEvery is how often the pending computers are read again while the store has not answered.
const trustRetryEvery = 5 * time.Second

const trustPending = db.TrustPending

// machineTrustStore is the store's side of it (db.DB has it; a store without it has no pending computers).
type machineTrustStore interface {
	PendingMachines(ctx context.Context) ([]db.PendingMachine, error)
	SetMachinePending(ctx context.Context, id, actor string) error
	ConfirmMachine(ctx context.Context, id string) (bool, error)
	ForgetMachineTrust(ctx context.Context, id string) error
	credentialRevoker
	AddMachineAudit(ctx context.Context, machine, action, actor string, detail json.RawMessage) error
}

// credentialRevoker revokes a computer's Persona Link (db.DB has it): its credentials, and its pairing codes not used
// yet. The ids of the credentials revoked.
type credentialRevoker interface {
	RevokeMachineCredentials(ctx context.Context, machine string) ([]string, error)
}

// machineTrust is the pending computers as the database has them, kept in memory: the machine list asks often.
type machineTrust struct {
	mu      sync.Mutex
	pending map[string]time.Time // machine id -> since
	// leaving is the computers rejected or expired that the manager still reports: no row is a confirmed computer, so
	// until it lets them go they are kept out as a pending one is (applyDeviceStates drops them from here).
	leaving map[string]bool
	// unknown: the pending computers could not be read (loadMachineTrust); every computer is kept out until they are.
	// A hub that never read them (tests that do not run it) has none.
	unknown bool
	// change serializes confirming, rejecting and the sweep, so one computer is not confirmed while it is removed.
	change sync.Mutex
}

func (h *Hub) trustStore() machineTrustStore {
	ts, _ := h.store.(machineTrustStore)
	return ts
}

// loadMachineTrust reads the pending computers (at start, before the manager connects them). When the store does not
// answer, every computer is kept out (machineUntrusted) and machineTrustLoop reads them again until it does: a pending
// computer must not pass for a confirmed one. False when it did not answer.
func (h *Hub) loadMachineTrust(ctx context.Context) bool {
	ts := h.trustStore()
	if ts == nil {
		return true
	}
	list, err := ts.PendingMachines(ctx)
	t := &h.trust
	if err != nil {
		h.logf("machines: the pending computers: %v", err)
		t.mu.Lock()
		t.unknown = true
		t.mu.Unlock()
		return false
	}
	t.mu.Lock()
	was := t.unknown
	t.pending = map[string]time.Time{}
	for _, p := range list {
		t.pending[p.ID] = p.CreatedAt
	}
	t.unknown = false
	t.mu.Unlock()
	h.devicesConfigChanged() // the manager tells their Persona Link
	if was {
		h.logf("machines: the pending computers read again (%d)", len(list))
		h.broadcastMachines()
	}
	return true
}

// machinePending: the computer is waiting to be confirmed (what the apps see as trust "pending").
func (h *Hub) machinePending(id string) bool {
	t := &h.trust
	t.mu.Lock()
	defer t.mu.Unlock()
	_, ok := t.pending[id]
	return ok
}

// machineUntrusted: the computer is kept out of what is the assistant's and of starting agents: it is pending, or
// was just rejected and the manager has not let it go, or the pending computers could not be read.
func (h *Hub) machineUntrusted(id string) bool {
	t := &h.trust
	t.mu.Lock()
	defer t.mu.Unlock()
	_, ok := t.pending[id]
	return ok || t.leaving[id] || t.unknown
}

// machineLeaving: rejected or expired, still reported by the manager.
func (h *Hub) machineLeaving(id string) bool {
	t := &h.trust
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.leaving[id]
}

// setLeaving keeps a computer out until the manager lets it go (on), or not any more (off: it was added again).
func (h *Hub) setLeaving(id string, on bool) {
	t := &h.trust
	t.mu.Lock()
	defer t.mu.Unlock()
	if !on {
		delete(t.leaving, id)
		return
	}
	if t.leaving == nil {
		t.leaving = map[string]bool{}
	}
	t.leaving[id] = true
}

// fleetLeft drops from leaving the computers the manager does not report any more (in is the ids it does, the
// machines and the hub.json entries another one absorbed).
func (h *Hub) fleetLeft(in func(id string) bool) {
	t := &h.trust
	t.mu.Lock()
	defer t.mu.Unlock()
	for id := range t.leaving {
		if !in(id) {
			delete(t.leaving, id)
		}
	}
}

// setPendingLocal records the change and hands it to the manager: the computer's Persona Link shows it.
func (h *Hub) setPendingLocal(id string, since time.Time, on bool) {
	t := &h.trust
	t.mu.Lock()
	if !on {
		delete(t.pending, id)
	} else {
		if t.pending == nil {
			t.pending = map[string]time.Time{}
		}
		t.pending[id] = since
	}
	t.mu.Unlock()
	h.devicesConfigChanged()
}

// pendingMachineIDs are the computers waiting to be confirmed, sorted (the manager's handover).
func (h *Hub) pendingMachineIDs() []string {
	t := &h.trust
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.pending) == 0 {
		return nil
	}
	return slices.Sorted(maps.Keys(t.pending))
}

// markMachinePending makes a computer pending (one a signed-in device added; actor is who).
func (h *Hub) markMachinePending(ctx context.Context, id, actor string) error {
	ts := h.trustStore()
	if ts == nil {
		return errors.New("no database")
	}
	if err := ts.SetMachinePending(ctx, id, actor); err != nil {
		return err
	}
	h.setPendingLocal(id, time.Now(), true)
	h.setLeaving(id, false)
	h.machineAudit(ctx, id, "pending", actor, nil)
	h.broadcastMachines()
	return nil
}

// confirmMachine makes a pending computer the user's; false when it was not pending. The assistant gives it the
// managed launchers once the hello lists it (assistant/machine_trust.go trustChanged).
func (h *Hub) confirmMachine(ctx context.Context, id, actor string) (bool, error) {
	ts := h.trustStore()
	if ts == nil {
		return false, nil
	}
	h.trust.change.Lock()
	defer h.trust.change.Unlock()
	ok, err := ts.ConfirmMachine(ctx, id)
	if err != nil || !ok {
		return false, err
	}
	h.setPendingLocal(id, time.Time{}, false)
	h.machineAudit(ctx, id, "confirm", actor, nil)
	h.logf("[%s] confirmed by %s", id, actor)
	h.broadcastMachines()
	return true, nil
}

// errNotPending: reject is only for a computer waiting to be confirmed.
var errNotPending = i18n.Err("hub-machine_trust.not_pending")

// rejectMachine removes a pending computer: its hub.json entry (the manager lets it go), its Persona Link
// credentials and pairing codes, its row. action is the audit's: reject, or expire after pendingFor.
func (h *Hub) rejectMachine(ctx context.Context, id, actor, action string) error {
	ts := h.trustStore()
	if ts == nil {
		return errNotPending
	}
	h.trust.change.Lock()
	defer h.trust.change.Unlock()
	if !h.machinePending(id) {
		return errNotPending
	}
	// Out until the manager has let it go: from ForgetMachineTrust on it has no row, which is a confirmed one.
	h.setLeaving(id, true)
	if kept, err := h.removeKeptMachine(ctx, id); kept {
		if err != nil { // its manager away: tried again (the expiry loop, the user)
			h.setLeaving(id, false)
			return err
		}
	} else {
		if h.cfg != nil && h.cfg.dir != "" {
			if _, err := removeMachineEntry(h.cfg.dir, id); err != nil {
				return err
			}
		}
		h.clearKept(ctx, id) // an old record of a manager that kept it (machines_kept.go)
		h.removeMachineLive(id)
	}
	revoked, err := h.revokeComputer(ctx, id)
	if err != nil {
		return err
	}
	if err := ts.ForgetMachineTrust(ctx, id); err != nil {
		return err
	}
	h.setPendingLocal(id, time.Time{}, false)
	detail, _ := json.Marshal(map[string]any{"credentials_revoked": revoked})
	h.machineAudit(ctx, id, action, actor, detail)
	h.logf("[%s] %s by %s: removed", id, action, actor)
	h.broadcastMachines()
	return nil
}

// machineAudit writes one audit row; a failure is logged, the change stands.
func (h *Hub) machineAudit(ctx context.Context, id, action, actor string, detail json.RawMessage) {
	ts := h.trustStore()
	if ts == nil {
		return
	}
	if err := ts.AddMachineAudit(context.WithoutCancel(ctx), id, action, actor, detail); err != nil {
		h.logf("[%s] audit %s: %v", id, action, err)
	}
}

// expirePending removes the computers that waited longer than pendingFor.
func (h *Hub) expirePending(ctx context.Context, now time.Time) {
	t := &h.trust
	t.mu.Lock()
	if t.unknown { // not read yet: which are due is not known
		t.mu.Unlock()
		return
	}
	var due []string
	for id, since := range t.pending {
		if now.Sub(since) >= pendingFor {
			due = append(due, id)
		}
	}
	t.mu.Unlock()
	slices.Sort(due)
	for _, id := range due {
		if err := h.rejectMachine(ctx, id, "hub", "expire"); err != nil && !errors.Is(err, errNotPending) {
			h.logf("[%s] remove after waiting a day to be confirmed: %v", id, err)
		}
	}
}

func (h *Hub) machineTrustLoop(ctx context.Context) {
	for {
		wait := trustSweepEvery
		if h.trustUnknown() && !h.loadMachineTrust(ctx) {
			wait = trustRetryEvery
		} else {
			h.expirePending(ctx, time.Now())
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

func (h *Hub) trustUnknown() bool {
	t := &h.trust
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.unknown
}

// trustActor is who confirmed or rejected: the device's credential id, or "master" for hub.json's token.
func trustActor(p principal) string {
	if p.ID == "" {
		return "master"
	}
	return p.ID
}

// mayTrust: a signed-in full device (not a pairing code's stand-in) or hub.json's token.
func mayTrust(p principal) bool { return p.Role == roleFull && p.PairHash == "" }

func (h *Hub) machineTrustRoutes(api *http.ServeMux) {
	api.HandleFunc("POST /api/v1/machines/{id}/confirm", func(w http.ResponseWriter, r *http.Request) {
		p := principalOf(r.Context())
		if !mayTrust(p) {
			writeErrCode(w, http.StatusForbidden, "forbidden", i18n.Err("hub-machine_trust.confirm_forbidden"))
			return
		}
		id := r.PathValue("id")
		ok, err := h.confirmMachine(r.Context(), id, trustActor(p))
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if !ok && h.localMachine(id) == nil && h.knownEntry(id) == nil {
			writeErrCode(w, http.StatusNotFound, "not_found", i18n.Err("hub-machine_trust.no_machine"))
			return
		}
		writeJSON(w, map[string]any{"ok": true}) // a confirmed one again is fine
	})
	api.HandleFunc("POST /api/v1/machines/{id}/reject", func(w http.ResponseWriter, r *http.Request) {
		p := principalOf(r.Context())
		if !mayTrust(p) {
			writeErrCode(w, http.StatusForbidden, "forbidden", i18n.Err("hub-machine_trust.reject_forbidden"))
			return
		}
		id := r.PathValue("id")
		err := h.rejectMachine(r.Context(), id, trustActor(p), "reject")
		switch {
		case errors.Is(err, errNotPending):
			if h.localMachine(id) == nil && h.knownEntry(id) == nil {
				writeErrCode(w, http.StatusNotFound, "not_found", i18n.Err("hub-machine_trust.no_machine"))
				return
			}
			writeErrCode(w, http.StatusConflict, "not_pending", err)
		case errors.Is(err, errKeeperAway): // the manager that keeps it is away (machines_kept.go)
			writeErrCode(w, http.StatusServiceUnavailable, "assistant_offline", err)
		case err != nil:
			writeErr(w, http.StatusInternalServerError, err)
		default:
			writeJSON(w, map[string]any{"ok": true})
		}
	})
}

// ---- what the apps see of a machine ----

// Machine error codes (docs/assistant/onboarding.md 5.3): what the error is about, for a client's own words.
const (
	errCodeNodeServiceDown = "node_service_down" // the computer's link is up, its resident node is not
	errCodeLinkOffline     = "link_offline"      // it was connected and is not now
	errCodeNeverConnected  = "never_connected"   // a computer that connects to the hub never has
	errCodeNodeOutdated    = "node_outdated"     // its node is another build and too old to be updated from here
)

// machineErrorCode is the code for what s shows (kind is its hub.json entry's): "" when none fits.
func machineErrorCode(s proto.Machine, kind string) string {
	if s.Online {
		if s.NodeMismatch && kind != "local" && s.CanDeploy != nil && !*s.CanDeploy {
			return errCodeNodeOutdated
		}
		return ""
	}
	e := s.Error
	if strings.Contains(e, "node-attach") || strings.Contains(e, link.NodeDownMarker) {
		return errCodeNodeServiceDown
	}
	if kind != kindLink && kind != kindService {
		return ""
	}
	if s.LastSeen.IsZero() && s.Link == nil {
		return errCodeNeverConnected
	}
	return errCodeLinkOffline
}

// withTrust fills in s's trust and error_code; a pending one's error says to confirm it (an app from before trust
// shows only error), its own error after it.
func (h *Hub) withTrust(s *proto.Machine, kind string) {
	s.ErrorCode = machineErrorCode(*s, kind)
	if !h.machinePending(s.ID) {
		return
	}
	s.Trust = trustPending
	if s.Error == "" {
		s.Error = i18n.T(h.userLang(), "hub-machine_trust.pending")
	} else {
		s.Error = i18n.T(h.userLang(), "hub-machine_trust.pending_error", i18n.Args{"error": s.Error})
	}
}

// ---- hub.json ----

// removeMachineEntry takes the machine out of hub.json's machines; false when it was not there.
func removeMachineEntry(dir, id string) (bool, error) {
	found := false
	err := editHubJSON(dir, func(o *jsonObject) error {
		list, err := machineEntries(o)
		if err != nil {
			return err
		}
		kept := make([]json.RawMessage, 0, len(list))
		for _, raw := range list {
			if machineEntryID(raw) == id {
				found = true
				continue
			}
			kept = append(kept, raw)
		}
		if !found {
			return nil
		}
		return o.Set("machines", kept)
	})
	return found, err
}
