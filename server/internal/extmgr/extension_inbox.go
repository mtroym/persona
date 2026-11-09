package extmgr

import (
	"context"
	"slices"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/i18n"
)

// The inbox's common rules for plugins (#1730, docs/extensions/extensions.md "Common inbox rules"). A plugin connector with an
// inbox and no fallback of its own (capability fallback) leaves sorting to the assistant's model (inbox.triage,
// "message triage"): its new items without a tier are not let in while the model cannot sort them (the manager's
// inbox_admit.go). So such a plugin is not switched on while that model cannot be called, and the model is not cleared
// while one is on. A hub.json plugin cannot be switched: it runs, but its inbox is not collected until the model is
// there.

// errNeedsTriageModel refuses to switch on (or install, or upgrade while on) a plugin that needs the triage model.
var errNeedsTriageModel = i18n.Err("hub-extension_inbox.needs_triage_model")

// needsTriageModelSetup is what Settings › Extensions says of a hub.json plugin waiting for the model (needs_setup).
func needsTriageModelSetup(l i18n.Lang) string {
	return i18n.T(l, "hub-extension_inbox.needs_triage_setup")
}

// canEnableExtension is the registry's CanEnable: a plugin with an inbox connector without a fallback needs the triage
// model. Built-in plugins are the hub's own sources (#1690's line) and keep their way.
func (h *Manager) canEnableExtension(ctx context.Context, source string, m *extension.Manifest) error {
	if source == extension.SourceBuiltin {
		return nil
	}
	if _, needs := extension.InboxConnectors(m); len(needs) == 0 {
		return nil
	}
	usable, err := h.triageUsable(ctx)
	switch {
	case err != nil:
		return err // the assistant, whose model configuration it is, can't be asked now
	case !usable:
		return errNeedsTriageModel
	}
	return nil
}

// extensionsNeedingTriage are the names of the plugins that are on and need the triage model: installed ones whose
// inbox connector has no fallback (a hub.json one just waits for the model; a built-in one has its fallbacks).
func (h *Manager) extensionsNeedingTriage() []string {
	if h.extensions == nil {
		return nil
	}
	var names []string
	for _, p := range h.extensions.reg.List() {
		if !p.Record.Enabled || p.Record.Source != extension.SourceInstalled {
			continue
		}
		if _, needs := extension.InboxConnectors(p.Manifest); len(needs) > 0 {
			name := p.Record.ID
			if p.Manifest != nil && p.Manifest.Name != "" {
				name = p.Manifest.Name
			}
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

// waitsForTriageModel says whether a plugin connector's inbox is not collected now: a hub.json plugin's without a
// fallback while the triage model cannot be called.
func (h *Manager) waitsForTriageModel(ctx context.Context, c connector.Connector) bool {
	return extensionSource(c.ID()) == extension.SourceHubJSON && c.Manifest().Has(connector.CapInbox) &&
		!c.Manifest().Has(connector.CapFallback) && !h.triageUsableNow(ctx)
}

// triageUsableNow is triageUsable, false while the assistant can't be asked.
func (h *Manager) triageUsableNow(ctx context.Context) bool {
	usable, err := h.triageUsable(ctx)
	return err == nil && usable
}

// hubJSONWaitsForTriageModel is waitsForTriageModel for a plugin (its status in Settings › Extensions).
func (h *Manager) hubJSONWaitsForTriageModel(ctx context.Context, p *extension.Extension) bool {
	if p.Record.Source != extension.SourceHubJSON {
		return false
	}
	_, needs := extension.InboxConnectors(p.Manifest)
	if len(needs) == 0 {
		return false
	}
	usable, err := h.triageUsable(ctx)
	return err == nil && !usable // while the assistant can't be asked, it is not said to need setting up
}
