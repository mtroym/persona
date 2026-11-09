package extmgr

import (
	"context"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/i18n"
)

// marketAdd registers a market after reading its index once (a market that cannot be read is not added).
func (h *Manager) marketAdd(ctx context.Context, specText, token string) (MarketView, error) {
	if err := h.marketsLoad(ctx); err != nil {
		return MarketView{}, err
	}
	spec, err := extension.ParseMarket(specText)
	if err != nil {
		return MarketView{}, err
	}
	if len(token) > extension.MarketTokenMost {
		return MarketView{}, i18n.Err("hub-extension_market.token_too_long")
	}
	id := extension.MarketID(spec)
	ms := h.marketsState()
	ms.mu.Lock()
	n, dup := len(ms.byID), ms.byID[id] != nil || ms.withSpec(spec.String()) != nil || (extension.IsOfficialMarket(spec.String()) && ms.official() != nil)
	ms.mu.Unlock()
	if dup {
		return MarketView{}, i18n.Err("hub-extension_market.duplicate")
	}
	if n >= extension.MarketsMost {
		return MarketView{}, i18n.Err("hub-extension_market.too_many")
	}
	mk := &extension.Market{Spec: spec, Token: token, Client: h.marketClient(), GitHubAPI: ms.githubAPI, Dir: h.marketsDir(), Cache: h.packageCache()}
	if err := h.configureMarket(mk); err != nil {
		return MarketView{}, err
	}
	f, err := mk.FetchSigned(ctx)
	if err != nil {
		return MarketView{}, err
	}
	now := time.Now()
	// Official identity determines verification; unofficial markets acquire no persistent pin.
	rec, ix, err := signedIndex(extension.MarketRecord{ID: id, Spec: spec.String(), AddedAt: now}, f, now)
	if err != nil {
		mk.RemoveCheckout()
		return MarketView{}, err
	}
	if token != "" {
		if rec.TokenSealed, err = (hubSealer{h}).Seal(token); err != nil {
			return MarketView{}, err
		}
	}
	ms.mu.Lock()
	if len(ms.byID) >= extension.MarketsMost || ms.byID[id] != nil || ms.withSpec(spec.String()) != nil || extension.IsOfficialMarket(spec.String()) && ms.official() != nil {
		ms.mu.Unlock()
		return MarketView{}, i18n.Err("hub-extension_market.duplicate")
	}
	if err := h.marketStore().PutExtensionMarket(ctx, rec); err != nil {
		ms.mu.Unlock()
		return MarketView{}, err
	}
	s := &marketState{rec: rec, index: ix}
	ms.byID[id] = s
	view := h.marketView(s)
	ms.mu.Unlock()
	h.logf("extension market %s added: %s (%d extensions, %s)", id, rec.Spec, len(ix.Extensions), signatureText(rec.Signature))
	return view, nil
}

// marketTokenFor is the token market id was registered with, for a move to target: an error when it has none or
// target may not get it.
func (h *Manager) marketTokenFor(id string, target extension.MarketSpec) (string, error) {
	ms := h.marketsState()
	ms.mu.Lock()
	state := ms.byID[id]
	var rec extension.MarketRecord
	if state != nil {
		rec = state.rec
	}
	ms.mu.Unlock()
	if state == nil {
		return "", errNoMarket
	}
	if rec.TokenSealed == "" {
		return "", i18n.Err("hub-extension_market.no_token")
	}
	mk, err := h.market(rec)
	if err != nil {
		return "", err
	}
	if !mk.SharesTokenWith(target) {
		return "", i18n.Err("extension-market_security.credential_move")
	}
	return mk.Token, nil
}

// marketSetSpec moves market id to another address, keeping its id: what was installed from it (source
// market:<id>/<extension>) stays its own, upgrades and all. The new address is read once first, with the market's
// token; one that cannot be read changes nothing.
func (h *Manager) marketSetSpec(ctx context.Context, id, specText string) (MarketView, error) {
	if err := h.marketsLoad(ctx); err != nil {
		return MarketView{}, err
	}
	spec, err := extension.ParseMarket(specText)
	if err != nil {
		return MarketView{}, err
	}
	ms := h.marketsState()
	ms.mu.Lock()
	s := ms.byID[id]
	var snapshot extension.MarketRecord
	if s != nil {
		snapshot = s.rec
	}
	other := ms.moveTargetTaken(id, spec.String())
	ms.mu.Unlock()
	if s == nil {
		return MarketView{}, errNoMarket
	}
	if other {
		return MarketView{}, i18n.Err("hub-extension_market.address_taken")
	}
	if extension.IsOfficialMarket(snapshot.Spec) && !extension.IsOfficialMarket(spec.String()) {
		return MarketView{}, i18n.Err("extension-market_security.move")
	}
	old, err := h.market(snapshot)
	if err != nil {
		return MarketView{}, err
	}
	if old.Token != "" && !old.SharesTokenWith(spec) {
		return MarketView{}, i18n.Err("extension-market_security.credential_move")
	}
	mk := &extension.Market{Spec: spec, Token: old.Token, Client: h.marketClient(), GitHubAPI: ms.githubAPI, Dir: h.marketsDir(), Cache: h.packageCache()}
	if err := h.configureMarket(mk); err != nil {
		return MarketView{}, err
	}
	f, err := mk.FetchSigned(ctx)
	if err != nil {
		return MarketView{}, err
	}
	now := time.Now()
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if s = ms.byID[id]; s == nil {
		return MarketView{}, errNoMarket
	}
	if s.rec.Spec != snapshot.Spec || s.rec.SigningKey != snapshot.SigningKey || s.rec.TokenSealed != snapshot.TokenSealed {
		return MarketView{}, i18n.Err("extension-market_security.changed")
	}
	// Another add or move may have claimed the target during the network read.
	if ms.moveTargetTaken(id, spec.String()) {
		return MarketView{}, i18n.Err("hub-extension_market.address_taken")
	}
	// Source and credential snapshots are checked again after the network read.
	candidate := s.rec
	candidate.Spec = spec.String()
	// Moving away from an official identity never turns its artifacts into unsigned ones.
	if extension.IsOfficialMarket(s.rec.Spec) && !extension.IsOfficialMarket(candidate.Spec) {
		return MarketView{}, i18n.Err("extension-market_security.catalog")
	}
	rec, ix, err := signedIndex(candidate, f, now)
	if err != nil {
		if old.Spec.String() != spec.String() {
			mk.RemoveCheckout()
		}
		return MarketView{}, err
	}
	rec.Spec = spec.String()
	if err := h.marketStore().PutExtensionMarket(ctx, rec); err != nil {
		return MarketView{}, err
	}
	if old.Spec.String() != spec.String() {
		old.RemoveCheckout()
	}
	s.rec, s.index = rec, ix
	h.logf("extension market %s moved: %s", id, rec.Spec)
	return h.marketView(s), nil
}
