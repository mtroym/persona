package assistant

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

// Model profiles (docs/assistant/model-profiles.md): a named set of model choices the user switches between in one
// step — "performance" while the strong models' quota lasts, "economy" when it runs short. A profile holds each
// category's choice (the model configuration's records, models.go), the model fields of providers added in Persona
// (launchers.managed, models_launchers.go) and named tiers an Agent can be started on (agent_start, task_plan). A
// switch writes all of it in one transaction and keeps what it replaced, so one rollback undoes it; every switch,
// rollback and edit is in the history. Profiles are the user's to edit: the two built-in ones start as copies of the
// configuration they were made from.

const (
	modelProfilesKey     = "model_profiles"       // modelProfiles: the profiles
	modelProfileStateKey = "model_profiles.state" // modelProfileState: which one is in use, the rollback, the history

	maxModelProfiles    = 20
	maxModelTiers       = 12
	maxProfileEvents    = 50
	modelProfileName    = 40  // runes
	modelProfileAbout   = 300 // runes
	modelTierAbout      = 100 // runes: a tier's description is in every turn's snapshot
	modelProfileValue   = 200 // bytes of a provider, model or launcher name
	shownProfileEvents  = 20
	modelProfileRolling = "rollback" // not an id: POST /api/v1/model-profiles/rollback
	// seededProfile is the template marked in use when the templates are made: a copy of the configuration, so
	// nothing switches, and the tiers work from the start.
	seededProfile = "performance"
)

var (
	modelProfileID = lazyre.New(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	modelTierName  = lazyre.New(`^[a-z][a-z0-9_-]{0,23}$`)
)

// ModelProfile is one profile: what it sets, and nothing else (a category or field it leaves out stays as it is).
type ModelProfile struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Purposes: a category's choice (its record in the model configuration) by category id; an empty binding is
	// "follow the default". The main chat's runtime is not a profile's.
	Purposes map[string]ModelBinding `json:"purposes,omitempty"`
	// Launchers: model fields of providers added in Persona, by provider name.
	Launchers map[string]LauncherModels `json:"launchers,omitempty"`
	// Tiers: what an Agent started on a tier runs on (agent_start's and task_plan's tier).
	Tiers     map[string]ModelTier `json:"tiers,omitempty"`
	UpdatedAt time.Time            `json:"updated_at"`
}

// LauncherModels are the model fields of a managed launcher a profile sets: nil leaves a field as it is, "" clears it
// (the launcher's own default then applies; the main model cannot be cleared).
type LauncherModels struct {
	Model         *string `json:"model,omitempty"`
	SmallModel    *string `json:"small_model,omitempty"`
	SubagentModel *string `json:"subagent_model,omitempty"`
	OpusModel     *string `json:"opus_model,omitempty"`
	SonnetModel   *string `json:"sonnet_model,omitempty"`
	HaikuModel    *string `json:"haiku_model,omitempty"`
	FableModel    *string `json:"fable_model,omitempty"`
	Effort        *string `json:"effort,omitempty"`
}

// fields pairs each model field of a managed launcher a profile can set (by its JSON name) with its value in m and
// the field of l it sets (nil: only m's).
func (m *LauncherModels) fields(l *proto.ManagedLauncher) []launcherField {
	if l == nil {
		l = &proto.ManagedLauncher{}
	}
	return []launcherField{{"model", &m.Model, &l.Model}, {"small_model", &m.SmallModel, &l.SmallModel},
		{"subagent_model", &m.SubagentModel, &l.SubagentModel}, {"opus_model", &m.OpusModel, &l.OpusModel},
		{"sonnet_model", &m.SonnetModel, &l.SonnetModel}, {"haiku_model", &m.HaikuModel, &l.HaikuModel},
		{"fable_model", &m.FableModel, &l.FableModel}, {"effort", &m.Effort, &l.Effort}}
}

type launcherField struct {
	name     string
	set      **string // the profile's value
	launcher *string  // the launcher's
}

func profileValue(v string) *string { return &v }

// ModelTier is what an Agent started on a tier runs on: a launcher, and its model and effort ("" for its defaults).
// Description says when to use it (Paseo's "When to use"): the assistant reads it to pick a tier, the New Agent
// form shows it.
type ModelTier struct {
	Launcher    string `json:"launcher"`
	Model       string `json:"model,omitempty"`
	Effort      string `json:"effort,omitempty"`
	Description string `json:"description,omitempty"`
}

// runs is what the tier runs on, in a line: its launcher, then the model and effort it sets.
func (t ModelTier) runs() string {
	parts := []string{t.Launcher}
	for _, v := range []string{t.Model, t.Effort} {
		if v != "" {
			parts = append(parts, v)
		}
	}
	return strings.Join(parts, " · ")
}

// ProfileActor is who switched or edited a profile: a device (its name, from the hub), the hub's own token, the
// assistant (a conversation's tool call), or the manager itself (system: the templates' first use).
type ProfileActor struct {
	Kind string `json:"kind"` // device, hub, assistant, system
	Name string `json:"name,omitempty"`
}

// ModelProfileEvent is one entry of the history.
type ModelProfileEvent struct {
	At      time.Time    `json:"at"`
	Action  string       `json:"action"` // switch, rollback, save, delete, seed
	From    string       `json:"from,omitempty"`
	To      string       `json:"to,omitempty"` // switch, rollback: the profile now in use; save, delete: the profile
	By      ProfileActor `json:"by"`
	Changed []string     `json:"changed,omitempty"` // what a switch or rollback changed: categories, <launcher>.<field>; a tier edit: tiers.<name>
}

type modelProfiles struct {
	Revision int64          `json:"revision"`
	Profiles []ModelProfile `json:"profiles"`
}

type modelProfileState struct {
	Active   string              `json:"active,omitempty"` // the profile last switched to ("" none yet)
	At       time.Time           `json:"at,omitzero"`
	By       ProfileActor        `json:"by,omitzero"`
	Previous *modelProfileImage  `json:"previous,omitempty"` // what the last switch replaced: one rollback's worth
	Events   []ModelProfileEvent `json:"events,omitempty"`   // oldest first, the last maxProfileEvents
}

// modelProfileImage is what a switch replaced: the categories' and launcher fields' values before it, and the profile
// that was in use.
type modelProfileImage struct {
	Profile   string                    `json:"profile,omitempty"`
	At        time.Time                 `json:"at"`
	Purposes  map[string]ModelBinding   `json:"purposes,omitempty"`
	Launchers map[string]LauncherModels `json:"launchers,omitempty"`
}

// seed marks seededProfile in use when the templates are made (modelProfilesLocked said so): it is a copy of the
// configuration, so nothing switches and there is nothing to roll back to.
func (st *modelProfileState) seed() {
	if st.Active != "" {
		return
	}
	now := time.Now().UTC()
	st.Active, st.At, st.By = seededProfile, now, ProfileActor{Kind: "system"}
	st.record(ModelProfileEvent{At: now, Action: "seed", To: seededProfile, By: st.By})
}

func (st *modelProfileState) record(e ModelProfileEvent) {
	st.Events = append(st.Events, e)
	if n := len(st.Events) - maxProfileEvents; n > 0 {
		st.Events = slices.Delete(st.Events, 0, n)
	}
}

var (
	errModelProfileNotFound    = i18n.Err("assistant-model_profiles.err_not_found")
	errModelProfileConflict    = i18n.Err("assistant-model_profiles.err_conflict")
	errModelProfileNoRollback  = i18n.Err("assistant-model_profiles.err_no_rollback")
	errModelProfileAtomicStore = i18n.Err("assistant-model_profiles.err_atomic_store")
)

// ---- what is stored ----

func (s *Service) readModelProfiles(ctx context.Context) (modelProfiles, bool, error) {
	var ps modelProfiles
	st := s.store()
	if st == nil {
		return ps, false, i18n.Err("assistant-models.err_no_database")
	}
	ok, err := st.Setting(ctx, modelProfilesKey, &ps)
	return ps, ok, err
}

func (s *Service) readModelProfileState(ctx context.Context) (modelProfileState, error) {
	var state modelProfileState
	st := s.store()
	if st == nil {
		return state, i18n.Err("assistant-models.err_no_database")
	}
	_, err := st.Setting(ctx, modelProfileStateKey, &state)
	return state, err
}

// putModelProfileSettings requires an atomic store: a failed write must leave every setting untouched.
func (s *Service) putModelProfileSettings(ctx context.Context, values map[string]any) error {
	st := s.store()
	if st == nil {
		return i18n.Err("assistant-models.err_no_database")
	}
	if tx, ok := st.(interface {
		PutSettings(context.Context, map[string]any) error
	}); ok {
		return tx.PutSettings(ctx, values)
	}
	return errModelProfileAtomicStore
}

// modelProfilesLocked reads profiles or builds two templates from the current configuration (models.mu held).
// The caller persists templates only after its operation has passed all checks, with the state seed() marks.
func (s *Service) modelProfilesLocked(ctx context.Context) (modelProfiles, bool, error) {
	ps, ok, err := s.readModelProfiles(ctx)
	if err != nil || ok {
		return ps, false, err
	}
	purposes, launchers, tiers, err := s.captureModels(ctx)
	if err != nil {
		return ps, false, err
	}
	l, now := s.requestCtxLang(ctx), time.Now().UTC()
	for _, id := range []string{"performance", "economy"} {
		ps.Profiles = append(ps.Profiles, ModelProfile{ID: id, Name: i18n.T(l, "assistant-model_profiles.name_"+id),
			Description: i18n.T(l, "assistant-model_profiles.about_"+id), Purposes: maps.Clone(purposes),
			Launchers: maps.Clone(launchers), Tiers: maps.Clone(tiers), UpdatedAt: now})
	}
	ps.Revision = 1
	return ps, true, nil
}

// captureModels is the configuration as a profile would set it: every category's own choice, the model fields of the
// providers added in Persona those categories run on, and two tiers (strong: the main chat's, standard: the Agents').
func (s *Service) captureModels(ctx context.Context) (map[string]ModelBinding, map[string]LauncherModels, map[string]ModelTier, error) {
	stored, err := s.readStoredLaunchers(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	ctx = withManagedSnapshot(ctx, stored)
	purposes := map[string]ModelBinding{}
	used := map[string]bool{}
	tiers := map[string]ModelTier{}
	l := s.requestCtxLang(ctx)
	for _, id := range modelCategoryIDs {
		r, err := s.modelRecord(ctx, id)
		if err != nil {
			return nil, nil, nil, err
		}
		b := r.Binding
		b.Runtime = ""
		purposes[id] = b
		effective, err := s.resolveModelRecord(ctx, id, &r)
		if err != nil {
			return nil, nil, nil, err
		}
		name, ok := strings.CutPrefix(effective.Provider, "launcher.")
		if !ok {
			continue
		}
		used[name] = true
		tier := map[string]string{"assistant": "strong", "agent.new": "standard"}[id]
		if tier != "" {
			model := effective.Model
			if !proto.ValidModelID(model) {
				model = "" // configured before #1644 held models to the rule: the launcher's default, as applyModel runs it
			}
			tiers[tier] = ModelTier{Launcher: name, Model: model, Effort: effective.Effort,
				Description: i18n.T(l, "assistant-model_profiles.tier_"+tier)}
		}
	}
	launchers := map[string]LauncherModels{}
	for _, l := range stored {
		if !used[l.Name] {
			continue
		}
		var m LauncherModels
		for _, f := range m.fields(&l.ManagedLauncher) {
			if l.Kind == "api" && f.name != "model" {
				continue
			}
			*f.set = profileValue(*f.launcher)
		}
		launchers[l.Name] = m
	}
	return purposes, launchers, tiers, nil
}

// ---- tiers ----

// tierModel is what an Agent started on tier runs on: the tier in the profile in use.
func (s *Service) tierModel(ctx context.Context, tier string) (ModelTier, error) {
	p, active, err := s.activeModelProfile(ctx)
	if err != nil {
		return ModelTier{}, err
	}
	if p == nil && active != "" {
		return ModelTier{}, errors.New("the model profile in use (" + active + ") no longer exists: switch to another with model_profile_switch, or leave tier out")
	}
	if p == nil {
		return ModelTier{}, errors.New("no model profile is in use: switch to one with model_profile_switch, or leave tier out")
	}
	t, ok := p.Tiers[tier]
	if !ok {
		return ModelTier{}, errors.New("the model profile in use (" + cmp.Or(p.Name, p.ID) + ") has no tier " + strings.TrimSpace(tier) + "; its tiers: " + strings.Join(slices.Sorted(maps.Keys(p.Tiers)), ", "))
	}
	return t, nil
}

// withTier fills a start's launcher, model and effort from tier: what the start names itself wins, and the tier's
// model and effort go only with the tier's launcher.
func withTier(t ModelTier, launcher, model, effort *string) {
	if *launcher != "" && *launcher != t.Launcher {
		return
	}
	*launcher = t.Launcher
	if *model == "" {
		*model = t.Model
	}
	if *effort == "" {
		*effort = t.Effort
	}
}
