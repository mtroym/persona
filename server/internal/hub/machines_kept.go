package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Machines their manager keeps (docs/assistant/managers.md "Each manager's computers"): a manager that names
// machines.own keeps its own list of computers in its manager.json, and the hub hands it MachinesOwn. The hub then
// edits that list over the bus (devices.machines.*) instead of hub.json, and moves its entries out of hub.json once the
// manager reports them as its own: from then on the hub knows them only as
//   - who keeps each (machinesKeptKey, beside the owners of hub.json's machines: ownership_moves.go), and
//   - what the manager last reported of each (machine_entries.go: the entry without its secrets).
// hub.json stays the staging area for what the hub adds while the manager is away (or for a manager from before): the
// manager is put the entries hub.json gives it that its list lacks when it reports, and they move out then. An edit of
// a machine its manager keeps waits for nothing: with the manager away it is answered 503 assistant_offline.

// machinesKeptKey is the hub's own: machine → the manager that keeps it, for the machines no longer in hub.json.
const machinesKeptKey = "hub.machines_kept"

// tailcatKept stands in for a kept machine's tailcat address, which only its manager has (the hub never dials it):
// the hub shows that the machine has one.
const tailcatKept = "(kept by its manager)"

// ownsList: manager id keeps its own list (its hello named machines.own), as connected now.
func (h *Hub) ownsList(id string) bool {
	for _, p := range h.busRoster() {
		if p.ID == id {
			return slices.Contains(p.Capabilities, assistantwire.CapabilityMachinesOwn)
		}
	}
	return false
}

// listHeld: manager id's last report said it holds its own list in its manager.json (States.OwnList).
func (h *Hub) listHeld(id string) bool {
	d := &h.devices
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.ownListHeld[id]
}

// keepsList: manager id keeps its own list (ownsList) and its last report said it holds it in its manager.json
// (States.OwnList): edits go to it, and hub.json's machines it reports move out.
func (h *Hub) keepsList(id string) bool {
	d := &h.devices
	d.mu.Lock()
	held := d.ownListHeld[id]
	d.mu.Unlock()
	return held && h.ownsList(id)
}

// errKeepsMachines: a manager that keeps machines in its own list is removed only with them dropped on purpose
// (DELETE /api/v1/managers/{id}?drop_machines=1).
var errKeepsMachines = errors.New("this manager keeps computers in its own list")

// errDefaultMachine: the hub's own default machine (hub.json lists no machines) is in hub.json again at every start,
// so it is not removed (as before); "machines": [] in hub.json removes it.
var errDefaultMachine = i18n.Err("hub-admin_machines.default_machine")

// keepsAnyKnown is keepsAny from a record that was read (read now when it was not); known false: it cannot be.
func (h *Hub) keepsAnyKnown(ctx context.Context, id string) (keeps, known bool) {
	d := &h.devices
	d.mu.Lock()
	read := d.keptRead
	d.mu.Unlock()
	if !read && h.loadMachinesKept(ctx) != nil {
		return false, false
	}
	return h.keepsAny(id), true
}

// clearKept forgets an old record of who keeps machine mid, its hub.json entry removed (a stale record would bring
// it back from the manager the record names). Takes d.moving.
func (h *Hub) clearKept(ctx context.Context, mid string) {
	was := h.keptBy(mid)
	if was == "" {
		return
	}
	d := &h.devices
	d.moving.Lock()
	defer d.moving.Unlock()
	if err := h.setKept(ctx, mid, ""); err != nil {
		h.logf("machines: [%s] removed from hub.json, but its old record: %v", mid, err)
		return
	}
	h.dropReservation(ctx, mid)
	var gone devicewire.MachineRemoved // the manager it named lets it go too, when it is there
	if err := h.keeperCall(ctx, was, devicewire.MethodMachinesRemove, devicewire.MachineRef{MachineID: mid}, &gone); err != nil {
		h.logf("machines: [%s] removed from hub.json; %s, which kept it before, could not let it go now: %v", mid, was, err)
	}
}

// keepsAny: manager id keeps at least one machine out of hub.json.
func (h *Hub) keepsAny(id string) bool {
	d := &h.devices
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, o := range d.kept {
		if o == id {
			return true
		}
	}
	return false
}

// warnKeeperOld says once that manager id keeps machines in its own list but its build no longer names
// machines.own: they are not connected (nor its list written over) until it is switched forward.
func (h *Hub) warnKeeperOld(id string) {
	d := &h.devices
	d.mu.Lock()
	said := d.warnedOld[id]
	if d.warnedOld == nil {
		d.warnedOld = map[string]bool{}
	}
	d.warnedOld[id] = true
	d.mu.Unlock()
	if !said && h.busConnOf(id) != nil {
		h.logf("machines: %s keeps machines in its own list, but its build does not (no %s): they are not connected, "+
			"and its list is handed nothing, until it is switched forward", id, assistantwire.CapabilityMachinesOwn)
	}
}

// loadMachinesKept reads machinesKeptKey (startDevices, with the owners): a machine whose manager the roster no longer
// has is gone with it. An error leaves it unread: the next change reads it first.
func (h *Hub) loadMachinesKept(ctx context.Context) error {
	var m map[string]string
	if s := h.managerStore(); s != nil {
		if _, err := s.Setting(ctx, machinesKeptKey, &m); err != nil {
			h.logf("machines: which manager keeps which machine: %v", err)
			return err
		}
	}
	kept := map[string]string{}
	for mid, o := range m {
		if o == "" {
			continue
		}
		if has, sure := h.rosterHas(ctx, o); sure && !has {
			continue
		}
		kept[mid] = o
	}
	d := &h.devices
	d.mu.Lock()
	d.kept, d.keptRead = kept, true
	d.mu.Unlock()
	return nil
}

// keptBy is the manager that keeps machine mid, "" when none does (a hub.json machine, or none at all).
func (h *Hub) keptBy(mid string) string {
	d := &h.devices
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.kept[mid]
}

// setKept records that manager o keeps machine mid ("": none does any more), written before anything relies on it.
// d.moving is held.
func (h *Hub) setKept(ctx context.Context, mid, o string) error {
	d := &h.devices
	d.mu.Lock()
	read := d.keptRead
	d.mu.Unlock()
	if !read {
		if err := h.loadMachinesKept(ctx); err != nil {
			return fmt.Errorf("%w: %v", errOwnersUnknown, err)
		}
	}
	d.mu.Lock()
	next := make(map[string]string, len(d.kept)+1)
	for k, v := range d.kept {
		next[k] = v
	}
	if o == "" {
		delete(next, mid)
	} else {
		next[mid] = o
	}
	d.mu.Unlock()
	if s := h.managerStore(); s != nil {
		if err := s.PutSetting(ctx, machinesKeptKey, next); err != nil {
			return err
		}
	}
	d.mu.Lock()
	d.kept = next
	d.mu.Unlock()
	return nil
}

// knownEntry is machine mid's entry: hub.json's, else what its keeper last reported of it (secrets left out), else
// only its id; nil for a machine the hub does not have.
func (h *Hub) knownEntry(mid string) *MachineConfig {
	if mc := h.cfgEntry(mid); mc != nil {
		return mc
	}
	o := h.keptBy(mid)
	if o == "" {
		return nil
	}
	mc := MachineConfig{ID: mid, Name: mid}
	if e, ok := h.reportedEntryOf(o, mid); ok {
		_ = json.Unmarshal(e.Cfg, &mc)
		mc.ID = mid
		if e.Tailcat {
			mc.TailcatAddr = tailcatKept
		}
	}
	return &mc
}

// knownEntries are hub.json's machines and those their managers keep (by id).
func (h *Hub) knownEntries() []MachineConfig {
	out := h.cfgEntries()
	d := &h.devices
	d.mu.Lock()
	var ids []string
	for mid := range d.kept {
		ids = append(ids, mid)
	}
	d.mu.Unlock()
	sort.Strings(ids)
	for _, mid := range ids {
		if slices.ContainsFunc(out, func(mc MachineConfig) bool { return mc.ID == mid }) {
			continue
		}
		if mc := h.knownEntry(mid); mc != nil {
			out = append(out, *mc)
		}
	}
	return out
}

// errKeeperAway: the manager that keeps the machine is not connected (503 assistant_offline).
var errKeeperAway = errManagerOffline

// keeperCall calls a devices.machines.* method on manager o: errKeeperAway when it is not connected.
func (h *Hub) keeperCall(ctx context.Context, o, method string, params, out any) error {
	if h.busConnOf(o) == nil {
		return errKeeperAway
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return h.callBus(cctx, o, method, params, out)
}

// keptEntryFull is machine mid's entry as its keeper has it, secrets included (a move hands it on).
func (h *Hub) keptEntryFull(ctx context.Context, o, mid string) (json.RawMessage, error) {
	var out devicewire.MachineEntry
	if err := h.keeperCall(ctx, o, devicewire.MethodMachinesGet, devicewire.MachineRef{MachineID: mid}, &out); err != nil {
		return nil, err
	}
	if len(out.Entry) == 0 || machineEntryID(out.Entry) != mid {
		return nil, fmt.Errorf("%s gave no entry for %q", o, mid)
	}
	return out.Entry, nil
}

// putKept has manager o keep entry (its id mid): put first (a put is idempotent: done again, it changes nothing), then
// recorded, so a failure in between leaves the entry with o rather than a record of nothing. What the hub shows of it
// until o reports it is the entry itself. d.moving is held.
func (h *Hub) putKept(ctx context.Context, o, mid string, entry json.RawMessage) error {
	if err := h.keeperCall(ctx, o, devicewire.MethodMachinesPut, devicewire.MachinePut{Entry: entry}, nil); err != nil {
		return err
	}
	if err := h.setKept(ctx, mid, o); err != nil {
		h.logf("machines: [%s] put in %s's own list, but recording that failed: %v (its reports are refused until the "+
			"record is written by the next edit)", mid, o, err)
		return err
	}
	h.seedMachineEntry(o, mid, entry)
	return nil
}

// dropKept has manager o let machine mid go and forgets it. d.moving is held.
func (h *Hub) dropKept(ctx context.Context, o, mid string) error {
	var out devicewire.MachineRemoved
	if err := h.keeperCall(ctx, o, devicewire.MethodMachinesRemove, devicewire.MachineRef{MachineID: mid}, &out); err != nil {
		return err
	}
	if err := h.setKept(ctx, mid, ""); err != nil {
		return err
	}
	h.forgetMachineEntry(mid)
	h.dropReservation(ctx, mid) // a dedicated machine's, so its id can be added again
	return nil
}

// dropReservation forgets a removed machine's owner record (a dedicated machine's reservation); one hub.json still
// lists keeps it. d.moving is held.
func (h *Hub) dropReservation(ctx context.Context, mid string) {
	d := &h.devices
	d.mu.Lock()
	reserved := d.assign[mid] != ""
	d.mu.Unlock()
	if !reserved || h.cfgEntry(mid) != nil {
		return
	}
	if err := h.assignOwner(ctx, mid, devicesOwner); err != nil {
		h.logf("machines: [%s] removed, but its owner record: %v", mid, err)
	}
}

// removeKeptMachine is a removal (the admin page, a rejected computer) of a machine its manager keeps: it lets it go,
// and the hub's view of it goes now. false: no manager keeps mid.
func (h *Hub) removeKeptMachine(ctx context.Context, mid string) (bool, error) {
	d := &h.devices
	d.moving.Lock()
	defer d.moving.Unlock()
	o := h.keptBy(mid)
	if o == "" || h.cfgEntry(mid) != nil { // hub.json's entry is the one in effect (machines_kept.go)
		return false, nil
	}
	if mc := h.knownEntry(mid); mc != nil && mc.Kind == "local" {
		if _, listed, err := hubJSONEntry(h.cfg.dir, mid); err == nil && !listed {
			return true, errDefaultMachine // it comes back at every start
		}
	}
	if err := h.dropKept(ctx, o, mid); err != nil {
		return true, err
	}
	h.logf("machines: [%s] removed: %s let it go", mid, o)
	h.machineGone(mid)
	return true, nil
}

// editKeptMachine changes keys of a kept machine's entry (the admin page's connector address): got from its keeper,
// changed, put back. false: no manager keeps mid.
func (h *Hub) editKeptMachine(ctx context.Context, mid string, set map[string]any) (bool, error) {
	d := &h.devices
	d.moving.Lock()
	defer d.moving.Unlock()
	o := h.keptBy(mid)
	if o == "" || h.cfgEntry(mid) != nil {
		return false, nil
	}
	entry, err := h.keptEntryFull(ctx, o, mid)
	if err != nil {
		return true, err
	}
	obj, err := parseJSONObject(entry)
	if err != nil {
		return true, err
	}
	for k, v := range set {
		if err := obj.Set(k, v); err != nil {
			return true, err
		}
	}
	return true, h.putKept(ctx, o, mid, obj.Marshal())
}

// machineGone takes a machine the hub no longer has out of what it shows (its manager's next report would too).
func (h *Hub) machineGone(mid string) {
	m := h.localMachine(mid)
	if m == nil || m.cfg.ID != mid {
		return
	}
	h.fleet.mu.Lock()
	h.machines = slices.DeleteFunc(h.machines, func(x *Machine) bool { return x == m })
	delete(h.byID, mid)
	h.fleet.mu.Unlock()
	h.connectionEnded(m, "removed")
	h.broadcastMachines()
	h.meshTouch()
}

// settleOwnList, after a report of manager o that keeps its own list (its report says it holds it in its
// manager.json: States.OwnList), settles what is staged in hub.json for it, each machine as o has it (asked:
// devices.machines.get), never as the hub showed it last:
//   - a hub.json machine of o's that o has as hub.json has it moves out of hub.json (o keeps it now);
//   - one o lacks, or has otherwise (hub.json was edited while it was staged), is put to o as hub.json has it, and
//     moves out at o's next report; the same entry is put once;
//   - a machine o's report names that is no longer o's (moved away while o could not let it go) is taken out of o's
//     list again;
//   - a machine the hub has as kept by o that o's report lacks is said in the log (o lost its manager.json).
//
// One at a time with the moves (d.moving); one waiting per manager.
func (h *Hub) settleOwnList(o string) {
	d := &h.devices
	d.mu.Lock()
	if d.settleDue[o] {
		d.mu.Unlock()
		return
	}
	if d.settleDue == nil {
		d.settleDue = map[string]bool{}
	}
	d.settleDue[o] = true
	d.mu.Unlock()
	h.moving.managers.RLock() // not while a manager is being removed (removingManager): as a move
	defer h.moving.managers.RUnlock()
	d.moving.Lock()
	defer d.moving.Unlock()
	d.mu.Lock()
	delete(d.settleDue, o)
	named := d.lastNamed[o]
	read := d.assignRead && d.keptRead // whose each machine is: unread, every one would look the carrier's
	d.mu.Unlock()
	if !read || !h.keepsList(o) || h.cfg == nil || h.cfg.dir == "" {
		return
	}
	ctx := context.Background()
	var out []string
	for _, mc := range h.cfgEntries() {
		if h.entryOwner(mc.ID) != o {
			continue
		}
		staged, listed, err := hubJSONEntry(h.cfg.dir, mc.ID)
		if err != nil {
			continue
		}
		if staged == nil {
			if listed || mc.Kind != "local" {
				continue // left the file (removed there; gone at the restart): nothing to settle
			}
			// hub.json lists no machines: the hub's default one, as it has it
			if staged, err = marshalNoEscape(mc); err != nil {
				continue
			}
		}
		var got devicewire.MachineEntry
		err = h.keeperCall(ctx, o, devicewire.MethodMachinesGet, devicewire.MachineRef{MachineID: mc.ID}, &got)
		var re *rpc.Error
		switch {
		case err == nil && sameEntry(got.Entry, staged):
			out = append(out, mc.ID)
			continue
		case err != nil && !(errors.As(err, &re) && re.Code == devicewire.CodeNoDevice):
			h.logf("machines: [%s] staged in hub.json for %s: asking it: %v", mc.ID, o, err)
			continue
		}
		key := o + "\x00" + canonical(staged)
		d.mu.Lock()
		tried := d.putTried[mc.ID] == key
		d.mu.Unlock()
		if tried {
			continue // put already: its next report settles it
		}
		if err := h.keeperCall(ctx, o, devicewire.MethodMachinesPut, devicewire.MachinePut{Entry: staged}, nil); err != nil {
			h.logf("machines: [%s] staged in hub.json for %s: put there: %v", mc.ID, o, err)
			continue
		}
		d.mu.Lock()
		if d.putTried == nil {
			d.putTried = map[string]string{}
		}
		d.putTried[mc.ID] = key
		d.mu.Unlock()
		h.logf("machines: [%s] staged in hub.json for %s, which lacked it as hub.json has it: put there", mc.ID, o)
	}
	for mid := range named {
		if h.entryOwner(mid) == o || h.knownEntry(mid) == nil && h.keptBy(mid) == "" {
			continue
		}
		var gone devicewire.MachineRemoved
		if err := h.keeperCall(ctx, o, devicewire.MethodMachinesRemove, devicewire.MachineRef{MachineID: mid}, &gone); err != nil {
			h.logf("machines: [%s] is not %s's now, but it still has it: taking it out: %v", mid, o, err)
			continue
		}
		if h.keptBy(mid) == o { // an old record (hub.json has it with another owner)
			if err := h.setKept(ctx, mid, ""); err != nil {
				h.logf("machines: [%s] its old record of %s: %v", mid, o, err)
			}
		}
		h.logf("machines: [%s] is not %s's now: taken out of its own list", mid, o)
	}
	d.mu.Lock()
	for mid := range named {
		delete(d.saidLost, mid) // it has it again: a later loss is said again
	}
	var lost []string
	for mid, k := range d.kept {
		if k == o && !named[mid] && !d.saidLost[mid] {
			lost = append(lost, mid)
			if d.saidLost == nil {
				d.saidLost = map[string]bool{}
			}
			d.saidLost[mid] = true
		}
	}
	d.mu.Unlock()
	for _, mid := range lost {
		h.logf("machines: [%s] is kept by %s, but its list does not have it (its manager.json lost?): put it back from "+
			"a hub.json.machines-kept-* copy beside hub.json, or remove it", mid, o)
	}
	if len(out) == 0 {
		return
	}
	if err := h.moveOutOfHubJSON(ctx, o, out); err != nil {
		h.logf("machines: %s keeps %v now, but moving them out of hub.json failed: %v", o, out, err)
	}
}

// sameEntry: a and b are the same entry (as JSON values).
func sameEntry(a, b json.RawMessage) bool { return len(a) > 0 && canonical(a) == canonical(b) }

// canonical is raw as one JSON text whatever its key order and spacing ("" when it does not read).
func canonical(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// moveOutOfHubJSON makes ids manager o's to keep, out of hub.json: recorded first (a stop in between leaves them in
// both, which reads the same), hub.json's entries kept in a copy beside it (0600) for an older build, then taken out.
// d.moving is held.
func (h *Hub) moveOutOfHubJSON(ctx context.Context, o string, ids []string) error {
	for _, mid := range ids {
		if was := h.keptBy(mid); was != "" && was != o { // an old record (hub.json had it back): that one lets it go
			var gone devicewire.MachineRemoved
			if err := h.keeperCall(ctx, was, devicewire.MethodMachinesRemove, devicewire.MachineRef{MachineID: mid}, &gone); err != nil {
				h.logf("machines: [%s] was kept by %s, which could not let it go now: %v", mid, was, err)
			}
		}
		if err := h.setKept(ctx, mid, o); err != nil {
			return err
		}
	}
	d0 := &h.devices
	d0.mu.Lock()
	for _, mid := range ids {
		delete(d0.putTried, mid)
		delete(d0.saidLost, mid)
	}
	d0.mu.Unlock()
	var moved []json.RawMessage
	err := editHubJSON(h.cfg.dir, func(obj *jsonObject) error {
		list, err := machineEntries(obj)
		if err != nil {
			return err
		}
		kept := make([]json.RawMessage, 0, len(list))
		for _, raw := range list {
			if slices.Contains(ids, machineEntryID(raw)) {
				moved = append(moved, raw)
				continue
			}
			kept = append(kept, raw)
		}
		if len(moved) == 0 {
			return nil
		}
		if err := keepMovedCopy(h.cfg.dir, moved); err != nil {
			return err
		}
		return obj.Set("machines", kept)
	})
	if err != nil {
		return err
	}
	var removed []string // out of the file: the record is their owner now; the default one stays by its owner
	for _, raw := range moved {
		removed = append(removed, machineEntryID(raw))
	}
	d := &h.devices
	d.mu.Lock()
	d.entries = slices.DeleteFunc(d.entries, func(mc MachineConfig) bool { return slices.Contains(ids, mc.ID) })
	next := map[string]string{}
	for k, v := range d.assign {
		if !slices.Contains(removed, k) {
			next[k] = v
		}
	}
	changed := len(next) != len(d.assign)
	d.mu.Unlock()
	if changed { // their owner is the record's now
		if s := h.managerStore(); s != nil {
			if err := s.PutSetting(ctx, machineOwnersKey, next); err != nil {
				return err
			}
		}
		d.mu.Lock()
		d.assign = next
		d.mu.Unlock()
	}
	if len(moved) > 0 {
		h.logf("machines: %s keeps %d of hub.json's machines in its own list now: moved out of hub.json (a copy is beside it)", o, len(moved))
	}
	return nil
}

// keepMovedCopy writes the entries moved out of hub.json beside it, for an older build switched back to (copy them
// back into hub.json's machines): hub.json.machines-kept-<time>, 0600.
func keepMovedCopy(dir string, moved []json.RawMessage) error {
	b, err := json.MarshalIndent(map[string]any{"machines": moved}, "", "  ")
	if err != nil {
		return err
	}
	name := filepath.Join(dir, "hub.json.machines-kept-"+time.Now().UTC().Format("20060102T150405.000000000Z"))
	return os.WriteFile(name, append(b, '\n'), 0o600)
}

// hubJSONEntry is machine mid's entry in hub.json as the file has it (keys the hub does not read too); nil: none.
// listed: the file has a machines list at all (without one, the hub's machines are its default one).
func hubJSONEntry(dir, mid string) (raw json.RawMessage, listed bool, err error) {
	b, err := os.ReadFile(filepath.Join(dir, "hub.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	o, err := parseJSONObject(b)
	if err != nil {
		return nil, false, err
	}
	if _, listed = o.Get("machines"); !listed {
		return nil, false, nil
	}
	list, err := machineEntries(o)
	if err != nil {
		return nil, true, err
	}
	for _, raw := range list {
		if machineEntryID(raw) == mid {
			return raw, true, nil
		}
	}
	return nil, true, nil
}

// moveKept moves a machine its manager from keeps to manager to: its entry got from from (secrets too), put in to's
// own list when to keeps one, else written to hub.json (to's from there, as for any hub.json machine), then let go by
// from. from away: errKeeperAway, nothing moved. d.moving is held.
func (h *Hub) moveKept(ctx context.Context, mid, from, to string) error {
	entry, err := h.keptEntryFull(ctx, from, mid)
	if err != nil {
		return err
	}
	var full MachineConfig
	if json.Unmarshal(entry, &full) == nil && full.OwnerRequired && to == devicesOwner { // its keeper's entry decides
		return errors.New("a dedicated local machine requires an explicit non-carrier manager")
	}
	if h.keepsList(to) {
		if err := h.putKept(ctx, to, mid, entry); err != nil {
			return err
		}
		d := &h.devices
		d.mu.Lock()
		reserved := d.assign[mid] != ""
		d.mu.Unlock()
		if reserved { // a dedicated machine's reservation moves with it (machines_owned_local.go)
			if err := h.assignOwner(ctx, mid, to); err != nil {
				h.logf("machines: [%s] moved to %s, but its reservation: %v", mid, to, err)
			}
		}
		// The hub's default machine (hub.json lists no machines) is in hub.json again at every start, where its
		// owner goes by the owners of hub.json's machines: that one follows the move too.
		if _, listed, err := hubJSONEntry(h.cfg.dir, mid); err == nil && !listed {
			if err := h.assignOwner(ctx, mid, to); err != nil { // the move is done: its settle repairs this meanwhile
				h.logf("machines: [%s] moved to %s, but recording its owner failed: %v (a hub restart gives it back to "+
					"the one before)", mid, to, err)
			}
		}
	} else {
		if err := editHubJSON(h.cfg.dir, func(o *jsonObject) error {
			list, err := machineEntries(o)
			if err != nil {
				return err
			}
			for _, raw := range list {
				if machineEntryID(raw) == mid {
					return nil
				}
			}
			return o.Set("machines", append(list, entry))
		}); err != nil {
			return err
		}
		d := &h.devices
		d.mu.Lock()
		next := map[string]string{}
		for k, v := range d.assign {
			next[k] = v
		}
		if to == devicesOwner {
			delete(next, mid)
		} else {
			next[mid] = to
		}
		d.mu.Unlock()
		if s := h.managerStore(); s != nil {
			if err := s.PutSetting(ctx, machineOwnersKey, next); err != nil {
				return err
			}
		}
		d.mu.Lock()
		d.assign = next
		d.mu.Unlock()
		if err := h.setKept(ctx, mid, ""); err != nil {
			return err
		}
		var mc MachineConfig
		if err := json.Unmarshal(entry, &mc); err == nil {
			h.addMachineLive(mc)
		}
	}
	var out devicewire.MachineRemoved
	if err := h.keeperCall(ctx, from, devicewire.MethodMachinesRemove, devicewire.MachineRef{MachineID: mid}, &out); err != nil {
		h.logf("machines: [%s] moved to %s; %s could not take it out of its own list: %v", mid, to, from, err)
	}
	return nil
}

// assignOwner records to as the owner of hub.json's machine mid (devicesOwner: none recorded). d.moving is held.
func (h *Hub) assignOwner(ctx context.Context, mid, to string) error {
	d := &h.devices
	d.mu.Lock()
	next := map[string]string{}
	for k, v := range d.assign {
		next[k] = v
	}
	d.mu.Unlock()
	if to == devicesOwner {
		delete(next, mid)
	} else {
		next[mid] = to
	}
	if s := h.managerStore(); s != nil {
		if err := s.PutSetting(ctx, machineOwnersKey, next); err != nil {
			return err
		}
	}
	d.mu.Lock()
	d.assign = next
	d.mu.Unlock()
	return nil
}

// keeperErr is how an edit relayed to a keeper is answered: away is 503 assistant_offline, a refusal 409.
func keeperErr(err error) (int, string) {
	var re *rpc.Error
	switch {
	case errors.Is(err, errDefaultMachine):
		return 409, "default_machine"
	case errors.Is(err, errKeeperAway):
		return 503, "assistant_offline"
	case errors.As(err, &re) && re.Code == devicewire.CodeNotOwnList:
		return 409, devicewire.CodeNotOwnList
	}
	return 502, "manager_failed"
}
