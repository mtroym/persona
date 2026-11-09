package hub

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
)

// Adding a computer (docs/assistant/onboarding.md 4.2 "Your computer ↔ hub", 5.3). The admin page adds one (admin_machines.go), and so
// does a signed-in full device or `personad hub pair --machine <id> --add` through POST /api/v1/auth/pair-codes with
// {"add": …}: one function writes the hub.json entry and hands it to the manager (addMachine). A computer a device
// adds is pending (machine_trust.go) until the user confirms it; one the master token adds over loopback, or the admin
// page adds, is confirmed (it has no trust row).

// maxPendingComputers is how many computers may wait to be confirmed at once: a device adds no more until the user
// confirms or rejects one, or one expires.
const maxPendingComputers = 3

// addMachineError is a refusal of addMachine or of a computer's code, with the code and status the API answers.
type addMachineError struct {
	status int
	code   string
	err    error
	extra  map[string]any
}

func (e *addMachineError) Error() string { return e.err.Error() }
func (e *addMachineError) Unwrap() error { return e.err }

func refuseAdd(status int, code string, err error) *addMachineError {
	return &addMachineError{status: status, code: code, err: err}
}

// writeAddMachineErr writes err: an addMachineError with its code (in the request's language), anything else a 500.
func writeAddMachineErr(w http.ResponseWriter, err error) {
	var e *addMachineError
	if !errors.As(err, &e) {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	body := errorBody(e.status, e.code, e.err)
	if l := i18n.OfWriter(w); l != "" {
		body["error"] = i18n.Localize(e.err, l)
	}
	for k, v := range e.extra {
		body[k] = v
	}
	writeErrorBody(w, e.status, body)
}

// addMachine checks mc's name and icon (filling in the defaults) and hands it to the carrier, which connects it now (no
// restart): put in its own list when it keeps one and is connected (machines_kept.go), else written to hub.json (it
// moves into the carrier's own list when that one reports). by is who added it, for the log. A machine with its id
// already there is 409 exists; for a new id, the Persona Link left under it is revoked first (revokeComputer).
func (h *Hub) addMachine(mc *MachineConfig, by string) error {
	h.devices.moving.Lock()
	defer h.devices.moving.Unlock()
	if h.cfgEntry(mc.ID) == nil {
		h.devices.mu.Lock()
		owner := h.devices.assign[mc.ID]
		h.devices.mu.Unlock()
		if owner != "" {
			return refuseAdd(http.StatusConflict, "ownership_reserved", errors.New("machine ID has a pending manager ownership reservation; retry dedicated local creation or choose another ID"))
		}
	}
	return h.addMachineMoving(mc, by)
}

// addMachineMoving publishes an entry with the ownership mutation lock held.
func (h *Hub) addMachineMoving(mc *MachineConfig, by string) error {
	if mc.Name == "" {
		mc.Name = mc.ID
	}
	if err := checkMachineName(mc.Name); err != nil {
		return err
	}
	switch mc.Icon {
	case "server", "laptop", "desktop":
	default:
		mc.Icon = "laptop"
	}
	exists := h.knownEntry(mc.ID) != nil
	if !h.machineIDTaken(mc.ID) {
		// A credential of a computer this id named before would connect as this one. Bounded: d.moving is held.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_, err := h.revokeComputer(ctx, mc.ID)
		cancel()
		if err != nil {
			return err
		}
	}
	d := &h.devices
	d.mu.Lock()
	target := cmp.Or(d.assign[mc.ID], devicesOwner) // a dedicated machine's reserved owner (machines_owned_local.go)
	d.mu.Unlock()
	if !exists && h.keepsList(target) {
		entry, err := marshalNoEscape(*mc)
		if err != nil {
			return err
		}
		err = h.putKept(context.Background(), target, mc.ID, entry) // d.moving is held
		if err == nil {
			h.logf("%s: machine %q (%s) added to %s's own list", by, mc.ID, mc.Kind, target)
			h.setLeaving(mc.ID, false) // added again after a reject (machine_trust.go)
			return nil
		}
		if !errors.Is(err, errKeeperAway) {
			return err
		}
		// Gone meanwhile: hub.json until it is back.
	}
	err := editHubJSON(h.cfg.dir, func(o *jsonObject) error {
		list, err := machineEntries(o)
		if err != nil {
			return err
		}
		for _, raw := range list {
			if machineEntryID(raw) == mc.ID {
				exists = true
			}
		}
		if exists {
			return nil
		}
		entry, err := marshalNoEscape(*mc)
		if err != nil {
			return err
		}
		return o.Set("machines", append(list, entry))
	})
	if err != nil {
		return err
	}
	if exists {
		return refuseAdd(http.StatusConflict, "exists", i18n.Err("hub-admin_machines.exists"))
	}
	h.logf("%s: machine %q (%s) added to hub.json", by, mc.ID, mc.Kind)
	h.addMachineLive(*mc) // the manager connects it now, no restart (devices_handover.go)
	return nil
}

// maxMachineName is how long a computer's name may be, in characters (link.MaxMachineName says the same).
const maxMachineName = 40

// checkMachineName refuses a computer's name that is too long or has line breaks or tabs.
func checkMachineName(name string) error {
	if len([]rune(name)) > maxMachineName || strings.ContainsAny(name, "\n\r\t") {
		return refuseAdd(http.StatusBadRequest, "invalid_name", i18n.Err("hub-admin_machines.invalid_name"))
	}
	return nil
}

// machineAddRequest is pair-codes' "add": the computer to add (a link one: it dials the hub).
type machineAddRequest struct {
	// ID is optional: `hub pair --machine <id> --add` names it; a device leaves it out and the hub makes one from
	// the name.
	ID   string `json:"id"`
	Name string `json:"name"`
	// OS (mac, linux, windows) only picks the install instructions a client shows.
	OS   string `json:"os"`
	Icon string `json:"icon"`
}

// machineSlug is an id for a computer named name (which may be Chinese): its ASCII letters and digits, lower case,
// the rest one "-" between them; with none, the system's name. Not checked against the ids there are.
func machineSlug(name, os string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
		default:
			dash = true
		}
		if b.Len() >= 40 {
			break
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		switch os {
		case "mac", "linux", "windows":
			return os
		}
		return "computer"
	}
	return s
}

// machineIDTaken: something answers to id already (an entry the hub has or hub.json has, a computer the manager
// reports, the local one).
func (h *Hub) machineIDTaken(id string) bool {
	if h.knownEntry(id) != nil || h.machineByID(id) != nil {
		return true
	}
	for _, m := range h.machineList() {
		if m.ID == id {
			return true
		}
	}
	if disk, err := h.diskConfig(); err == nil {
		for _, m := range disk.Machines {
			if m.ID == id {
				return true
			}
		}
	}
	return false
}

// neverConnected: the manager reports no connection the computer ever had.
func (h *Hub) neverConnected(id string) bool {
	for _, m := range h.machineList() {
		if m.ID == id {
			return !m.Online && m.LastSeen.IsZero() && m.Link == nil
		}
	}
	return true
}

func (h *Hub) pendingCount() int {
	t := &h.trust
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.pending)
}

// computerFor is the link computer a code is made for: the one in machine, or the one add describes (added now,
// unless one of that id or name can be used again). fromDevice: a signed-in device asked, so a computer it adds is
// pending, and it makes codes only for pending ones (one already confirmed gets its keys: a code a device hands out
// for it must not let another computer be it). added: it was added now.
func (h *Hub) computerFor(ctx context.Context, machine string, add *machineAddRequest, fromDevice bool, actor string) (id string, added bool, err error) {
	if add == nil {
		mc := h.knownEntry(machine)
		if mc == nil || mc.Kind != kindLink {
			if fromDevice {
				return "", false, refuseAdd(http.StatusNotFound, "not_found", i18n.Err("hub-pairing.no_computer"))
			}
			return "", false, refuseAdd(http.StatusBadRequest, "unknown_machine", i18n.Err("hub-pairing.unknown_machine", i18n.Args{"id": machine}))
		}
		if fromDevice {
			if !h.machinePending(machine) {
				return "", false, refuseAdd(http.StatusConflict, "not_pending", i18n.Err("hub-pairing.confirmed_computer"))
			}
			// Its code asked again: the earlier one did not reach the right computer (or was forwarded), and whatever
			// traded it must not stay in and become the confirmed computer along with the one that trades the new code.
			if _, err := h.revokeComputer(ctx, machine); err != nil {
				return "", false, err
			}
		}
		return machine, false, nil
	}
	name := strings.TrimSpace(add.Name)
	osName := strings.TrimSpace(add.OS)
	switch osName {
	case "", "mac", "linux", "windows":
	default:
		return "", false, refuseAdd(http.StatusBadRequest, "invalid_os", i18n.Err("hub-pairing.invalid_os"))
	}
	if name != "" {
		if err := checkMachineName(name); err != nil {
			return "", false, err
		}
	}
	reusable := func(mc *MachineConfig) bool {
		return mc != nil && mc.Kind == kindLink && (!fromDevice || (h.machinePending(mc.ID) && h.neverConnected(mc.ID)))
	}
	// reuse is the code for a computer there is already: a device's revokes what an earlier code of it gave.
	reuse := func(id string) (string, bool, error) {
		if fromDevice {
			if _, err := h.revokeComputer(ctx, id); err != nil {
				return "", false, err
			}
		}
		return id, false, nil
	}
	if id = strings.TrimSpace(add.ID); id != "" {
		if !adminMachineID.MatchString(id) {
			return "", false, refuseAdd(http.StatusBadRequest, "invalid_id", i18n.Err("hub-admin_machines.invalid_id"))
		}
		if mc := h.knownEntry(id); reusable(mc) {
			return reuse(id)
		}
		if h.machineIDTaken(id) {
			return "", false, refuseAdd(http.StatusConflict, "exists", i18n.Err("hub-admin_machines.exists"))
		}
	} else {
		if name == "" {
			return "", false, refuseAdd(http.StatusBadRequest, "invalid_name", i18n.Err("hub-pairing.name_required"))
		}
		base := machineSlug(name, osName)
		for n := 1; ; n++ {
			id = base
			if n > 1 {
				id = base + "-" + strconv.Itoa(n)
			}
			// The one this name made before and that never connected: its code again, not a second computer.
			if mc := h.knownEntry(id); mc != nil && mc.Name == name && reusable(mc) {
				return reuse(id)
			}
			if !h.machineIDTaken(id) {
				break
			}
		}
	}
	mc := MachineConfig{ID: id, Name: name, Kind: kindLink, Icon: add.Icon}
	if !fromDevice {
		return id, true, h.addMachine(&mc, actor)
	}
	if h.pendingCount() >= maxPendingComputers {
		return "", false, refuseAdd(http.StatusTooManyRequests, "too_many_pending",
			i18n.Err("hub-pairing.too_many_pending", i18n.Args{"count": maxPendingComputers}))
	}
	ts := h.trustStore()
	if ts == nil {
		return "", false, refuseAdd(http.StatusServiceUnavailable, "internal", errors.New("no database"))
	}
	// Pending before the entry is there: there is no moment it is a confirmed computer.
	if err := h.markMachinePending(ctx, id, actor); err != nil {
		return "", false, err
	}
	if err := h.addMachine(&mc, "device "+actor); err != nil {
		if ferr := ts.ForgetMachineTrust(context.WithoutCancel(ctx), id); ferr != nil {
			h.logf("[%s] could not be added, and its pending row stays: %v", id, ferr)
		}
		h.setPendingLocal(id, time.Time{}, false)
		return "", false, err
	}
	return id, true, nil
}

// revokeComputer revokes every Persona Link credential of computer id and its pairing codes not used yet, closing what
// they have open; how many credentials it revoked. A store that keeps no credentials has none to revoke.
func (h *Hub) revokeComputer(ctx context.Context, id string) (int, error) {
	cr, _ := h.store.(credentialRevoker)
	if cr == nil {
		return 0, nil
	}
	revoked, err := cr.RevokeMachineCredentials(ctx, id)
	if err != nil {
		return 0, err
	}
	for _, cred := range revoked {
		h.revoked(cred) // its open link goes too
	}
	return len(revoked), nil
}

// siteURL is where this hub's build was published (the site serving install.sh: the config's publish.site_url, the
// build's update URL without its dl/); "" when it was not (a local or Docker build).
func (h *Hub) siteURL() string {
	raw := strings.TrimRight(h.build.UpdateURL, "/")
	if raw == "" {
		return ""
	}
	raw = strings.TrimSuffix(raw, "/dl")
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return ""
	}
	return raw
}

// computerInstall is how a computer installs the node and pairs with pairURL, by system; only Linux has one line
// (install.sh), and only when the hub knows its site.
func (h *Hub) computerInstall(pairURL string) map[string]string {
	site := h.siteURL()
	if site == "" {
		return nil
	}
	return map[string]string{"linux": fmt.Sprintf("curl -fsSL %s/install.sh | sh -s -- --node --pair '%s'", site, pairURL)}
}

// notifyComputerCode tells the other paired devices that one made a code for a computer: a code was handed out
// that lets a computer in (docs/assistant/onboarding.md 4.2 "Receipt"). It is a security receipt, so it goes out on purpose without
// the devices' notification prefs, mute or desktop-open suppression (sendBuilt, not sendLocalized).
func (h *Hub) notifyComputerCode(by principal, machine string) {
	name := machine
	if mc := h.knownEntry(machine); mc != nil && mc.Name != "" {
		name = mc.Name
	}
	if !h.push.ready() {
		h.logf("device %q (%s) made a pairing code for computer %q (push unavailable)", by.Name, by.ID, machine)
		return
	}
	h.push.sendBuilt(func(l i18n.Lang) map[string]any {
		device := by.Name
		if device == "" { // never the credential id: ids only go in `pair`
			device = i18n.T(l, "hub-pairing.some_device")
		}
		return computerCodePayload(l, device, name, by.ID, machine)
	}, "", "alert", "10", func(d Device) bool { return d.Credential != by.ID && d.CredentialID != by.ID })
}

// Pairing receipts (docs/protocol/protocol.md "Pairing receipt pushes"): aps.category picks the notification's buttons
// ("Not you? Revoke", "My computer" / "Not mine"), and the ids those buttons act on are only in `pair`, never in the alert text.
const (
	pairCategoryReceipt = "pair_receipt"
	pairCategoryJoining = "computer_joining"
)

// computerCodePayload is the receipt for a code a device made for a computer: device_id is the issuing device's
// credential id (DELETE /api/v1/auth/tokens/{id}), machine the computer's id (POST /api/v1/machines/{id}/reject).
func computerCodePayload(l i18n.Lang, device, name, credential, machine string) map[string]any {
	return map[string]any{
		"aps": map[string]any{
			"alert":    map[string]any{"title": "Persona", "body": i18n.T(l, "hub-pairing.computer_code", i18n.Args{"device": device, "name": name})},
			"sound":    "default",
			"category": pairCategoryReceipt,
		},
		"pair": map[string]any{"kind": "computer_code", "device_id": credential, "machine": machine},
	}
}

// computerJoiningPayload asks whether a pending computer that just traded its code is the user's (confirm / reject).
func computerJoiningPayload(l i18n.Lang, name, machine string) map[string]any {
	return map[string]any{
		"aps": map[string]any{
			"alert":    map[string]any{"title": "Persona", "body": i18n.T(l, "hub-pairing.computer_joining", i18n.Args{"name": name})},
			"sound":    "default",
			"category": pairCategoryJoining,
		},
		"pair": map[string]any{"kind": "computer_joining", "machine": machine},
	}
}

// devicePairedPayload is the receipt for a new credential: device_id is its id, the one to revoke.
func devicePairedPayload(l i18n.Lang, name, credential string) map[string]any {
	return map[string]any{
		"aps": map[string]any{
			"alert":    map[string]any{"title": "Persona", "body": i18n.T(l, "hub-pairing.new_device", i18n.Args{"name": name})},
			"sound":    "default",
			"category": pairCategoryReceipt,
		},
		"pair": map[string]any{"kind": "device", "device_id": credential},
	}
}

// notifyComputerJoining tells the devices a pending computer's Persona Link just traded its code: it is the one to
// confirm.
func (h *Hub) notifyComputerJoining(machine string) {
	name := machine
	if mc := h.knownEntry(machine); mc != nil && mc.Name != "" {
		name = mc.Name
	}
	if !h.push.ready() {
		h.logf("[%s] new computer %q wants to join (push unavailable)", machine, name)
		return
	}
	h.push.sendLocalized(func(l i18n.Lang) map[string]any {
		return computerJoiningPayload(l, name, machine)
	}, "", nil)
}
