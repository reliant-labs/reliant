package cli

// ROLLBACK FLOORS in the CLI (pkg/release floor.go has the model; the design
// is rollback-floor.md, owner decisions 2026-11-08).
//
// One seam, floorStore, type-asserted off envLedger.Bindings the way
// bindingHistoryReader is: the hosted backend implements it over the control
// plane's rpcs, the machine ledger over internal/ledgerfile, and the offline
// copy read-only (a floor changed offline would be a second ledger).
//
// WHO ENFORCES. The hosted server judges every promote under its env row lock
// (refusal below_rollback_floor), and the machine ledger judges inside its
// file lock (admitPromotion). The check this file runs before the plan is UX:
// it refuses before anything is built, pushed or confirmed, and it adds the
// one strengthening a server without git cannot — a target whose commit git
// proves is an ancestor of an order floor's commit is refused even if it was
// cut later (a hotfix from an old branch).

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/ledgerfile"
	"github.com/reliant-labs/forge/pkg/release"
)

// Control-plane rpcs for floors (controlplane.v1.DeployService).
const (
	procSetRollbackFloor   = "controlplane.v1.DeployService/SetRollbackFloor"
	procLiftRollbackFloor  = "controlplane.v1.DeployService/LiftRollbackFloor"
	procListRollbackFloors = "controlplane.v1.DeployService/ListRollbackFloors"
)

// floorStore is an env ledger that holds rollback floors.
type floorStore interface {
	// RollbackFloors are env's floors, oldest first: active only, or every
	// one with includeLifted. errFloorsUnsupported when the backend cannot
	// hold floors at all (a control plane that predates them).
	RollbackFloors(ctx context.Context, env string, includeLifted bool) ([]release.Floor, error)
	SetRollbackFloor(ctx context.Context, env string, req floorSetRequest) (release.Floor, error)
	LiftRollbackFloors(ctx context.Context, env string, req floorLiftRequest) (floorLiftResult, error)
}

// floorSetRequest is `forge env floor set`.
type floorSetRequest struct {
	// Exactly one: a version (manual), or the env's current release
	// (roll_forward_only).
	Version string
	Current bool
	Reason  string
	// Guard is the compare-and-set: the floor lands only if the env is
	// still on the promotion the command read.
	Guard appendGuard
	By    release.Actor
}

// floorLiftRequest is `forge env floor lift|lower|clear`.
type floorLiftRequest struct {
	FloorIDs     []string
	LowerTo      string
	Clear        bool
	Reason       string
	Acknowledged []release.DeclarationRef
	By           release.Actor
}

// floorLiftResult is what a lift changed.
type floorLiftResult struct {
	Lifted []release.Floor `json:"lifted"`
	Set    *release.Floor  `json:"set,omitempty"`
}

// floorsUnsupportedError is a backend that cannot hold floors.
type floorsUnsupportedError struct{ location string }

func (e *floorsUnsupportedError) Error() string {
	return fmt.Sprintf("rollback floors are not supported by %s (it predates them; upgrade the control plane)", e.location)
}

// ExitCode: the request cannot be honoured by this backend — wrong, not
// undetermined.
func (e *floorsUnsupportedError) ExitCode() int { return exitWrong }

func isFloorsUnsupported(err error) bool {
	var u *floorsUnsupportedError
	return errors.As(err, &u)
}

// floorRefusal is the below_rollback_floor refusal for a target, as the
// server words it.
func floorRefusal(env, version string, r *release.FloorRefusal) *promoteRefusedError {
	return &promoteRefusedError{
		Reason: reasonBelowRollbackFloor,
		Floors: r.Floors,
		Detail: fmt.Sprintf("%s's rollback floor refuses %s: %s", env, version, r.Error()),
	}
}

// casRefusal is the compare-and-set half of admitPromotion, for any write a
// guard covers (a promote, a `floor set --current`).
func casRefusal(history []release.Promotion, env string, guard appendGuard) *promoteRefusedError {
	if !guard.expects() {
		return nil
	}
	var current *release.Promotion
	if n := len(history); n > 0 {
		current = &history[n-1]
	}
	switch {
	case guard.ExpectUnbound && current == nil:
		return nil
	case !guard.ExpectUnbound && current != nil && current.ID == guard.ExpectedCurrentID:
		return nil
	}
	refusal := &promoteRefusedError{
		Reason:                     reasonPromotionConflict,
		ExpectedCurrentPromotionID: guard.ExpectedCurrentID,
		ExpectedUnbound:            guard.ExpectUnbound,
	}
	if current != nil {
		cur := *current
		refusal.ActualCurrent = &cur
		refusal.Detail = fmt.Sprintf("%s is on %s (promotion %s), not on the %s this write expected",
			env, cur.Release, cur.ID, guard.describe())
	} else {
		refusal.Detail = fmt.Sprintf("%s has no promotion, not the %s this write expected", env, guard.describe())
	}
	return refusal
}

// ─── hosted ─────────────────────────────────────────────────────────────────

// wireFloorDeclaration is controlplane.v1.DeployFloorDeclaration.
type wireFloorDeclaration struct {
	Source string `json:"source"`
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Reason string `json:"reason,omitempty"`
}

// wireRollbackFloor is controlplane.v1.DeployRollbackFloor.
type wireRollbackFloor struct {
	ID           string                 `json:"id"`
	Kind         string                 `json:"kind"`
	Source       string                 `json:"source"`
	Version      string                 `json:"version"`
	ReleaseCutAt time.Time              `json:"releaseCutAt"`
	Commit       string                 `json:"commit,omitempty"`
	Requires     []wireFloorDeclaration `json:"requires,omitempty"`
	Reason       string                 `json:"reason"`
	SetByUserID  string                 `json:"setByUserId,omitempty"`
	SetByTokenID string                 `json:"setByTokenId,omitempty"`
	SetByActor   string                 `json:"setByActor,omitempty"`
	SetAt        time.Time              `json:"setAt"`
	PromotionID  string                 `json:"promotionId,omitempty"`
	Lift         *struct {
		Reason          string    `json:"reason"`
		LiftedByUserID  string    `json:"liftedByUserId,omitempty"`
		LiftedByTokenID string    `json:"liftedByTokenId,omitempty"`
		LiftedByActor   string    `json:"liftedByActor,omitempty"`
		LiftedAt        time.Time `json:"liftedAt"`
		ReplacedBy      string    `json:"replacedByFloorId,omitempty"`
		Acknowledged    []struct {
			Source string `json:"source"`
			ID     string `json:"id"`
		} `json:"acknowledged,omitempty"`
	} `json:"lift,omitempty"`
}

func floorFromWire(env string, w wireRollbackFloor) release.Floor {
	f := release.Floor{
		ID: w.ID, Env: env, Kind: release.FloorKind(w.Kind), Source: release.FloorSource(w.Source),
		Release: w.Version, ReleaseCutAt: w.ReleaseCutAt.UTC(), Commit: w.Commit, Reason: w.Reason,
		SetBy: release.Actor{User: w.SetByUserID, Actor: firstNonEmpty(w.SetByActor, tokenActor(w.SetByTokenID))},
		SetAt: w.SetAt.UTC(), PromotionID: w.PromotionID,
	}
	for _, d := range w.Requires {
		f.Requires = append(f.Requires, release.FloorDeclaration{Source: d.Source, ID: d.ID, Kind: release.DeclarationKind(d.Kind), Reason: d.Reason})
	}
	if w.Lift != nil {
		lift := &release.FloorLift{
			Reason: w.Lift.Reason, At: w.Lift.LiftedAt.UTC(), ReplacedBy: w.Lift.ReplacedBy,
			By: release.Actor{User: w.Lift.LiftedByUserID, Actor: firstNonEmpty(w.Lift.LiftedByActor, tokenActor(w.Lift.LiftedByTokenID))},
		}
		for _, a := range w.Lift.Acknowledged {
			lift.Acknowledged = append(lift.Acknowledged, release.DeclarationRef{Source: a.Source, ID: a.ID})
		}
		f.Lift = lift
	}
	return f
}

func floorsFromWire(env string, ws []wireRollbackFloor) []release.Floor {
	out := make([]release.Floor, 0, len(ws))
	for _, w := range ws {
		out = append(out, floorFromWire(env, w))
	}
	return out
}

func tokenActor(tokenID string) string {
	if tokenID == "" {
		return ""
	}
	return "token:" + tokenID
}

// floorsSupported asks the control plane once per process whether it holds
// rollback floors (GetCapabilities.rollback_floors). A server that does not
// serve the rpc predates every capability, floors included. Asked rather
// than inferred, because an older server silently DISCARDS fields it does not
// know: a `deploy --floor` sent to one would succeed and set no floor.
func (s *hostedStore) floorsSupported(ctx context.Context) (bool, error) {
	s.mu.Lock()
	known := s.floorsCap
	s.mu.Unlock()
	if known != nil {
		return *known, nil
	}
	var caps struct {
		RollbackFloors bool `json:"rollbackFloors"`
	}
	supported := false
	if err := s.client.Call(ctx, procDeployCapabilities, map[string]any{}, &caps); err != nil {
		var cerr *cloud.Error
		if !errors.As(err, &cerr) || !cerr.HasCode(cloud.CodeUnimplemented) {
			return false, fmt.Errorf("ask %s whether it holds rollback floors (GetCapabilities): %w", s.endpoint, err)
		}
	} else {
		supported = caps.RollbackFloors
	}
	s.mu.Lock()
	s.floorsCap = &supported
	s.mu.Unlock()
	return supported, nil
}

func (s *hostedStore) requireFloors(ctx context.Context) error {
	ok, err := s.floorsSupported(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return &floorsUnsupportedError{location: s.endpoint}
	}
	return nil
}

// RollbackFloors reads env's floors from the control plane. An env the
// control plane has never heard of has none.
func (s *hostedStore) RollbackFloors(ctx context.Context, env string, includeLifted bool) ([]release.Floor, error) {
	if err := s.requireFloors(ctx); err != nil {
		return nil, err
	}
	id, err := s.envID(ctx, env)
	if err != nil {
		var cerr *cloud.Error
		if errors.As(err, &cerr) && cerr.HasCode(cloud.CodeNotFound) {
			return nil, nil
		}
		return nil, err
	}
	var resp struct {
		Floors []wireRollbackFloor `json:"floors"`
	}
	req := map[string]any{"environmentId": id}
	if includeLifted {
		req["includeLifted"] = true
	}
	if err := s.client.Call(ctx, procListRollbackFloors, req, &resp); err != nil {
		return nil, fmt.Errorf("read %s's rollback floors from %s: %w", env, s.endpoint, err)
	}
	return floorsFromWire(env, resp.Floors), nil
}

// SetRollbackFloor sets an order floor on the control plane.
func (s *hostedStore) SetRollbackFloor(ctx context.Context, env string, req floorSetRequest) (release.Floor, error) {
	if err := s.requireFloors(ctx); err != nil {
		return release.Floor{}, err
	}
	id, err := s.envID(ctx, env)
	if err != nil {
		return release.Floor{}, err
	}
	wire := map[string]any{"environmentId": id, "reason": req.Reason}
	if req.Current {
		wire["current"] = true
	} else {
		wire["version"] = req.Version
	}
	guardWireFields(appendGuard{ExpectedCurrentID: req.Guard.ExpectedCurrentID, ExpectUnbound: req.Guard.ExpectUnbound}, wire)
	if req.By.Actor != "" {
		wire["setByActor"] = req.By.Actor
	}
	var resp struct {
		Floor wireRollbackFloor `json:"floor"`
	}
	if err := s.client.Call(ctx, procSetRollbackFloor, wire, &resp); err != nil {
		if refused := s.refusalFromWire(env, err); refused != nil {
			return release.Floor{}, refused
		}
		return release.Floor{}, fmt.Errorf("set a rollback floor on %s (%s): %w", env, s.endpoint, err)
	}
	s.rememberFloors(ctx, env)
	return floorFromWire(env, resp.Floor), nil
}

// LiftRollbackFloors lifts floors on the control plane.
func (s *hostedStore) LiftRollbackFloors(ctx context.Context, env string, req floorLiftRequest) (floorLiftResult, error) {
	if err := s.requireFloors(ctx); err != nil {
		return floorLiftResult{}, err
	}
	id, err := s.envID(ctx, env)
	if err != nil {
		return floorLiftResult{}, err
	}
	wire := map[string]any{"environmentId": id, "reason": req.Reason}
	if len(req.FloorIDs) > 0 {
		wire["floorIds"] = req.FloorIDs
	}
	if req.LowerTo != "" {
		wire["lowerToVersion"] = req.LowerTo
	}
	if req.Clear {
		wire["clearOrderFloors"] = true
	}
	if len(req.Acknowledged) > 0 {
		acks := make([]map[string]string, 0, len(req.Acknowledged))
		for _, a := range req.Acknowledged {
			acks = append(acks, map[string]string{"source": a.Source, "id": a.ID})
		}
		wire["acknowledged"] = acks
	}
	if req.By.Actor != "" {
		wire["liftedByActor"] = req.By.Actor
	}
	var resp struct {
		Lifted []wireRollbackFloor `json:"lifted"`
		Set    *wireRollbackFloor  `json:"set,omitempty"`
	}
	if err := s.client.Call(ctx, procLiftRollbackFloor, wire, &resp); err != nil {
		return floorLiftResult{}, fmt.Errorf("lift %s's rollback floors (%s): %w", env, s.endpoint, err)
	}
	s.rememberFloors(ctx, env)
	out := floorLiftResult{Lifted: floorsFromWire(env, resp.Lifted)}
	if resp.Set != nil {
		f := floorFromWire(env, *resp.Set)
		out.Set = &f
	}
	return out, nil
}

// ─── machine ledger ─────────────────────────────────────────────────────────

// RollbackFloors reads the machine ledger's floor log.
func (s machineBindingStore) RollbackFloors(_ context.Context, env string, includeLifted bool) ([]release.Floor, error) {
	all, err := s.store.Floors(env)
	if err != nil || includeLifted {
		return all, err
	}
	return release.ActiveFloors(all), nil
}

// machineReleases reads every release of the machine ledger, by version,
// BEFORE a locked floor change: the change's decision runs inside the store's
// lock, which a release read would take again.
func (s machineBindingStore) machineReleases() (map[string]release.Release, error) {
	rels, err := s.store.Releases()
	if err != nil {
		return nil, err
	}
	out := make(map[string]release.Release, len(rels))
	for _, r := range rels {
		out[r.Version] = r
	}
	return out, nil
}

func currentRelease(history []release.Promotion, rels map[string]release.Release) *release.Release {
	if len(history) == 0 {
		return nil
	}
	if r, ok := rels[history[len(history)-1].Release]; ok {
		return &r
	}
	return nil
}

// SetRollbackFloor records an order floor in the machine ledger, under its
// lock and the guard, refusing one the env's current release would fail.
func (s machineBindingStore) SetRollbackFloor(_ context.Context, env string, req floorSetRequest) (release.Floor, error) {
	rels, err := s.machineReleases()
	if err != nil {
		return release.Floor{}, err
	}
	change, err := s.store.ChangeFloors(env, func(history []release.Promotion, _ []release.Floor) (ledgerfile.FloorChange, error) {
		if refusal := casRefusal(history, env, req.Guard); refusal != nil {
			return ledgerfile.FloorChange{}, refusal
		}
		current := currentRelease(history, rels)
		source, promotionID := release.FloorSourceManual, ""
		var at release.Release
		switch {
		case req.Current && current == nil:
			return ledgerfile.FloorChange{}, exitCodeError{code: exitWrong, msg: fmt.Sprintf("%s has never been promoted, so it has no current release to set a floor at", env)}
		case req.Current:
			at, source, promotionID = *current, release.FloorSourceRollForwardOnly, history[len(history)-1].ID
		default:
			r, ok := rels[req.Version]
			if !ok {
				return ledgerfile.FloorChange{}, exitCodeError{code: exitWrong, msg: fmt.Sprintf("release %q is not in %s", req.Version, s.Location())}
			}
			at = r
		}
		f := release.NewOrderFloor(env, at, source, req.Reason, req.By, time.Time{}, promotionID)
		if err := release.CheckNewFloor(f, current); err != nil {
			return ledgerfile.FloorChange{}, exitCodeError{code: exitWrong, msg: err.Error()}
		}
		return ledgerfile.FloorChange{Floors: []release.Floor{f}}, nil
	})
	if err != nil {
		return release.Floor{}, err
	}
	return change.Floors[0], nil
}

// LiftRollbackFloors lifts floors in the machine ledger, under its lock, by
// the same rules as the server (release.CheckLift, OrderFloorsAbove).
func (s machineBindingStore) LiftRollbackFloors(_ context.Context, env string, req floorLiftRequest) (floorLiftResult, error) {
	rels, err := s.machineReleases()
	if err != nil {
		return floorLiftResult{}, err
	}
	var result floorLiftResult
	_, err = s.store.ChangeFloors(env, func(history []release.Promotion, all []release.Floor) (ledgerfile.FloorChange, error) {
		lift, set, err := decideFloorLift(env, release.ActiveFloors(all), req, rels, currentRelease(history, rels))
		if err != nil {
			return ledgerfile.FloorChange{}, err
		}
		change := ledgerfile.FloorChange{}
		replacedBy := ""
		if set != nil {
			set.ID = ledgerfile.NewFloorID()
			replacedBy = set.ID
			change.Floors = append(change.Floors, *set)
		}
		for _, f := range lift {
			l := release.FloorLift{Reason: strings.TrimSpace(req.Reason), By: req.By, ReplacedBy: replacedBy, Acknowledged: req.Acknowledged}
			change.Lifts = append(change.Lifts, ledgerfile.FloorLift{FloorID: f.ID, FloorLift: l})
			f.Lift = &l
			result.Lifted = append(result.Lifted, f)
		}
		result.Set = set
		return change, nil
	})
	return result, err
}

// decideFloorLift selects and checks the floors a lift request lifts, and the
// floor a `lower` sets — the server's rules (deploystore selectFloorsToLift),
// for the backends without one.
func decideFloorLift(env string, active []release.Floor, req floorLiftRequest, rels map[string]release.Release, current *release.Release) (lift []release.Floor, set *release.Floor, err error) {
	byID := make(map[string]release.Floor, len(active))
	for _, f := range active {
		byID[f.ID] = f
	}
	selected := map[string]bool{}
	pick := func(f release.Floor) {
		if !selected[f.ID] {
			selected[f.ID] = true
			lift = append(lift, f)
		}
	}
	for _, id := range req.FloorIDs {
		f, ok := byID[id]
		if !ok {
			return nil, nil, exitCodeError{code: exitWrong, msg: fmt.Sprintf("%s has no active rollback floor %q (forge env floor show %s)", env, id, env)}
		}
		pick(f)
	}
	if req.Clear {
		for _, f := range active {
			if f.Kind == release.FloorOrder {
				pick(f)
			}
		}
	}
	if req.LowerTo != "" {
		r, ok := rels[req.LowerTo]
		if !ok {
			return nil, nil, exitCodeError{code: exitWrong, msg: fmt.Sprintf("release %q is not in this ledger", req.LowerTo)}
		}
		for _, f := range release.OrderFloorsAbove(active, r) {
			pick(f)
		}
		floor := release.NewOrderFloor(env, r, release.FloorSourceManual, req.Reason, req.By, time.Time{}, "")
		if err := release.CheckNewFloor(floor, current); err != nil {
			return nil, nil, exitCodeError{code: exitWrong, msg: err.Error()}
		}
		set = &floor
	}
	for _, f := range lift {
		if err := release.CheckLift(f, req.Reason, req.Acknowledged); err != nil {
			return nil, nil, exitCodeError{code: exitWrong, msg: err.Error()}
		}
	}
	return lift, set, nil
}

// ─── offline copy ───────────────────────────────────────────────────────────

// RollbackFloors are the floors the offline copy recorded when it was last
// refreshed. A copy that predates floors says so rather than claiming none.
func (b *offlineBindings) RollbackFloors(_ context.Context, env string, includeLifted bool) ([]release.Floor, error) {
	if !b.snap.FloorsRecorded {
		return nil, fmt.Errorf("the offline copy of %s's ledger (refreshed %s) predates rollback floors, so its floors are unknown",
			env, b.snap.RefreshedAt.Format(time.RFC3339))
	}
	if includeLifted {
		return b.snap.Floors, nil
	}
	return release.ActiveFloors(b.snap.Floors), nil
}

// SetRollbackFloor is refused offline: the floor would exist only here.
func (b *offlineBindings) SetRollbackFloor(context.Context, string, floorSetRequest) (release.Floor, error) {
	return release.Floor{}, exitCodeError{code: exitWrong, msg: "--offline-ledger: rollback floors change only on the control plane"}
}

// LiftRollbackFloors is refused offline, for SetRollbackFloor's reason.
func (b *offlineBindings) LiftRollbackFloors(context.Context, string, floorLiftRequest) (floorLiftResult, error) {
	return floorLiftResult{}, exitCodeError{code: exitWrong, msg: "--offline-ledger: rollback floors change only on the control plane"}
}

var (
	_ floorStore = (*hostedStore)(nil)
	_ floorStore = machineBindingStore{}
	_ floorStore = (*offlineBindings)(nil)
)

// ─── the deploy's early check ───────────────────────────────────────────────

// promoteFloorVerdict is the plan's `floor` line.
type promoteFloorVerdict struct {
	// Verdict is admitted | raises | unknown | unsupported. A target below a
	// floor never reaches a plan: it is refused before one is shown.
	Verdict string `json:"verdict"`
	// Active are the env's active floors.
	Active []release.Floor `json:"active,omitempty"`
	// Raises are the floors this promote will raise.
	Raises []release.Floor `json:"raises,omitempty"`
	// Note explains unknown and unsupported.
	Note string `json:"note,omitempty"`
}

// checkDeployFloors judges target against env's rollback floors before
// anything is built, confirmed or written, and returns the plan's verdict and
// the target release (for the write's guard). A target below a floor is a
// *promoteRefusedError (below_rollback_floor, exit 1). A ledger that cannot
// say is a note, never a refusal: the backend that writes still judges.
func checkDeployFloors(ctx context.Context, out io.Writer, projectDir, env, version string, ledger envLedger, git promoteGitReader, raiseReason string) (*promoteFloorVerdict, *release.Release, error) {
	target, err := ledger.Releases.Get(ctx, version)
	if err != nil || target == nil {
		// The plan reports an unknown release on its own terms.
		return &promoteFloorVerdict{Verdict: "unknown", Note: "the target release could not be read"}, nil, nil
	}
	store, ok := ledger.Bindings.(floorStore)
	if !ok {
		if raiseReason != "" {
			return nil, nil, exitCodeError{code: exitWrong, msg: fmt.Sprintf("--floor: %s holds no rollback floors", ledger.Bindings.Location())}
		}
		return &promoteFloorVerdict{Verdict: "unknown", Note: "this ledger holds no rollback floors"}, target, nil
	}
	floors, err := store.RollbackFloors(ctx, env, false)
	switch {
	case isFloorsUnsupported(err):
		if raiseReason != "" {
			return nil, nil, fmt.Errorf("--floor: %w", err)
		}
		return &promoteFloorVerdict{Verdict: "unsupported", Note: err.Error()}, target, nil
	case err != nil:
		if raiseReason != "" {
			return nil, nil, fmt.Errorf("--floor: %w", err)
		}
		fmt.Fprintf(out, "[deploy] Note: %s's rollback floors could not be read (%v); the ledger still judges the write\n", env, err)
		return &promoteFloorVerdict{Verdict: "unknown", Note: err.Error()}, target, nil
	}
	if r := release.AdmitOverFloors(floors, *target); r != nil {
		return nil, nil, floorRefusal(env, version, r)
	}
	if refusal := floorAncestryRefusal(ctx, git, projectDir, env, *target, floors); refusal != nil {
		return nil, nil, refusal
	}
	v := &promoteFloorVerdict{Verdict: "admitted", Active: floors}
	if raises := release.PromoteRaises(floors, env, *target, raiseReason, release.Actor{}, time.Now().UTC(), ""); len(raises) > 0 {
		v.Verdict, v.Raises = "raises", raises
	}
	if target.Migrations == nil || !target.Migrations.DeclarationsRecorded {
		v.Note = fmt.Sprintf("%s records no rollback-floor declarations (cut by a forge that predates them): it raises no automatic floor; set one by hand if it breaks rollback", version)
	}
	return v, target, nil
}

// floorAncestryRefusal is the client-side strengthening of an order floor: a
// target whose commit git proves is a strict ancestor of the floor's commit
// is refused even when its cut time is later — a hotfix cut from a branch
// that predates the floor. Anything git cannot say is no refusal.
func floorAncestryRefusal(ctx context.Context, git promoteGitReader, dir, env string, target release.Release, floors []release.Floor) *promoteRefusedError {
	if git == nil {
		git = gitCommitReader{}
	}
	tgt := target.Git.Commit
	if target.Provenance != nil && target.Provenance.Commit != "" {
		tgt = target.Provenance.Commit
	}
	if tgt == "" || target.Git.Dirty || !git.HasCommit(ctx, dir, tgt) {
		return nil
	}
	for _, f := range floors {
		if !f.Active() || f.Kind != release.FloorOrder || f.Commit == "" || f.Commit == tgt || !git.HasCommit(ctx, dir, f.Commit) {
			continue
		}
		if older, err := git.IsAncestor(ctx, dir, tgt, f.Commit); err == nil && older {
			return &promoteRefusedError{
				Reason: reasonBelowRollbackFloor,
				Floors: []release.Floor{f},
				Detail: fmt.Sprintf("%s's rollback floor is %s (%s floor: %q); %s was cut later, but its commit %s is an ancestor of the floor's %s, so it is older code",
					env, f.Release, f.Source, f.Reason, target.Version, shortSHA(tgt), shortSHA(f.Commit)),
			}
		}
	}
	return nil
}

// renderFloorVerdict is the plan's floor line(s).
func renderFloorVerdict(out io.Writer, v *promoteFloorVerdict) {
	if v == nil {
		return
	}
	switch v.Verdict {
	case "admitted":
		if len(v.Active) == 0 {
			fmt.Fprintf(out, "  floor     admitted (no rollback floor)\n")
		} else {
			fmt.Fprintf(out, "  floor     admitted (%d active rollback floor(s))\n", len(v.Active))
		}
	case "raises":
		for _, f := range v.Raises {
			fmt.Fprintf(out, "  floor     raises a %s floor: %s\n", f.Source, describeFloor(f))
		}
	default:
		fmt.Fprintf(out, "  floor     %s — %s\n", v.Verdict, v.Note)
	}
	if v.Verdict != "unknown" && v.Verdict != "unsupported" && v.Note != "" {
		fmt.Fprintf(out, "            note: %s\n", v.Note)
	}
}

// describeFloor is one floor in one line.
func describeFloor(f release.Floor) string {
	if f.Kind == release.FloorDeclared {
		refs := make([]string, 0, len(f.Requires))
		for _, d := range f.Requires {
			refs = append(refs, fmt.Sprintf("%s %s", d.Kind, d.ID))
		}
		return fmt.Sprintf("after it, no release without %s can deploy here", strings.Join(refs, ", "))
	}
	return fmt.Sprintf("after it, no release cut before %s can deploy here (%q)", f.Release, f.Reason)
}

// floorsRaisedBy reads back the floors the recorded promotion raised: the
// env's floors that name it. Display only — a ledger that cannot say leaves
// the list empty.
func floorsRaisedBy(ctx context.Context, ledger envLedger, env string, recorded *release.Promotion) []release.Floor {
	store, ok := ledger.Bindings.(floorStore)
	if !ok || recorded == nil || recorded.ID == "" {
		return nil
	}
	floors, err := store.RollbackFloors(ctx, env, false)
	if err != nil {
		return nil
	}
	var out []release.Floor
	for _, f := range floors {
		if f.PromotionID == recorded.ID {
			out = append(out, f)
		}
	}
	return out
}

// statusFloors is `env status`'s floor read: the active floors, and whether
// the ledger could say (an unreadable or unsupported one is nil, false).
func statusFloors(ctx context.Context, bindings bindingStore, env string) ([]release.Floor, bool) {
	store, ok := bindings.(floorStore)
	if !ok {
		return nil, false
	}
	floors, err := store.RollbackFloors(ctx, env, false)
	if err != nil {
		return nil, false
	}
	if floors == nil {
		floors = []release.Floor{}
	}
	return floors, true
}

// printStatusFloors is `env status`'s floor line(s).
func printStatusFloors(out io.Writer, floors []release.Floor, supported bool) {
	switch {
	case !supported:
		return
	case len(floors) == 0:
		fmt.Fprintf(out, "  floor         none (any release can be deployed)\n")
	default:
		for _, f := range floors {
			fmt.Fprintf(out, "  floor         %s %s: %s [%s]\n", f.Kind, f.Source, describeFloorShort(f), f.ID)
		}
	}
}

func describeFloorShort(f release.Floor) string {
	if f.Kind == release.FloorDeclared {
		ids := make([]string, 0, len(f.Requires))
		for _, d := range f.Requires {
			ids = append(ids, d.ID)
		}
		return fmt.Sprintf("must carry %s (since %s)", strings.Join(ids, ", "), f.Release)
	}
	return fmt.Sprintf("nothing cut before %s — %q", f.Release, f.Reason)
}
