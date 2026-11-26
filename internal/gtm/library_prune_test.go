// Copyright (c) 2026 Reliant Labs

package gtm

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// token is a person acting through an access token: the handler builds it
// for ImportStrategyLibrary only.
var token = Actor{UserID: "u-person", OrgID: orgA, Kind: ActorPerson, ViaToken: true}

// seedLibrary imports keys as a person-class person, at the fixture's now.
func (f *fixture) seedLibrary(t *testing.T, keys ...string) {
	t.Helper()
	var entries []LibraryEntry
	for _, k := range keys {
		entries = append(entries, libEntry(k, CategoryLaunchesPR))
	}
	_, err := f.svc.ImportStrategyLibrary(ctx(), person, ImportStrategyLibraryInput{SourceRevision: "seed", Entries: entries})
	require.NoError(t, err)
}

func (f *fixture) libraryKeys(t *testing.T) []string {
	t.Helper()
	res, err := f.svc.ListStrategyLibrary(ctx(), person, ListStrategyLibraryInput{})
	require.NoError(t, err)
	var keys []string
	for _, e := range res.Entries {
		keys = append(keys, e.Key)
	}
	return keys
}

// stage has the token import alpha with prune over all_keys [alpha],
// staging a request to remove the rest.
func (f *fixture) stage(t *testing.T) ImportStrategyLibraryResult {
	t.Helper()
	res, err := f.svc.ImportStrategyLibrary(ctx(), token, ImportStrategyLibraryInput{
		SourceRevision: "ci-1", Entries: []LibraryEntry{libEntry("alpha", CategoryLaunchesPR)}, Prune: true, AllKeys: []string{"alpha"},
	})
	require.NoError(t, err)
	return res
}

func TestLibraryImportThroughAToken(t *testing.T) {
	t.Run("a token upserts, and its prune removes nothing: it stages a request", func(t *testing.T) {
		f := newFixture(t)
		f.seedLibrary(t, "alpha", "beta", "gamma")
		f.advance(time.Minute)
		res := f.stage(t)
		assert.Zero(t, res.Pruned, "a token never prunes directly")
		assert.Equal(t, 1, res.Unchanged)
		assert.NotEmpty(t, res.PruneRequestID)
		assert.Equal(t, []string{"beta", "gamma"}, res.WouldRemove)
		assert.Equal(t, []string{"alpha", "beta", "gamma"}, f.libraryKeys(t), "nothing was removed")

		reqs, err := f.svc.ListLibraryPruneRequests(ctx(), person, ListLibraryPruneRequestsInput{})
		require.NoError(t, err)
		require.Len(t, reqs.Requests, 1)
		r := reqs.Requests[0]
		assert.Equal(t, res.PruneRequestID, r.ID)
		assert.Equal(t, PruneRequestPending, r.Status)
		assert.Equal(t, CredentialToken, r.CredentialKind)
		assert.Equal(t, "ci-1", r.SourceRevision)
		assert.Equal(t, token.UserID, r.RequestedBy)
		assert.Equal(t, f.now.Add(LibraryPruneRequestTTL), r.ExpiresAt)
	})
	t.Run("a token prune with nothing to remove stages nothing", func(t *testing.T) {
		f := newFixture(t)
		f.seedLibrary(t, "alpha")
		res := f.stage(t)
		assert.Empty(t, res.PruneRequestID)
		assert.Empty(t, res.WouldRemove)
		reqs, err := f.svc.ListLibraryPruneRequests(ctx(), person, ListLibraryPruneRequestsInput{})
		require.NoError(t, err)
		assert.Empty(t, reqs.Requests)
	})
	t.Run("a person-class prune removes at once", func(t *testing.T) {
		f := newFixture(t)
		f.seedLibrary(t, "alpha", "beta")
		res, err := f.svc.ImportStrategyLibrary(ctx(), person, ImportStrategyLibraryInput{
			SourceRevision: "r", Entries: []LibraryEntry{libEntry("alpha", CategoryLaunchesPR)}, Prune: true, AllKeys: []string{"alpha"},
		})
		require.NoError(t, err)
		assert.Equal(t, 1, res.Pruned)
		assert.Empty(t, res.PruneRequestID)
		assert.Equal(t, []string{"alpha"}, f.libraryKeys(t))
	})
	t.Run("every call writes an audit row", func(t *testing.T) {
		f := newFixture(t)
		f.seedLibrary(t, "alpha", "beta")
		f.advance(time.Minute)
		staged := f.stage(t)
		f.advance(time.Minute)
		_, err := f.svc.ImportStrategyLibrary(ctx(), person, ImportStrategyLibraryInput{
			SourceRevision: "r3", Entries: []LibraryEntry{libEntry("alpha", CategoryLaunchesPR)}, Prune: true, AllKeys: []string{"alpha"},
		})
		require.NoError(t, err)

		res, err := f.svc.ListLibraryImports(ctx(), agent, ListLibraryImportsInput{})
		require.NoError(t, err)
		require.Len(t, res.Imports, 3)
		latest, tok, seed := res.Imports[0], res.Imports[1], res.Imports[2]
		assert.Equal(t, LibraryImport{ID: latest.ID, ActorUserID: person.UserID, CredentialKind: CredentialPerson, SourceRevision: "r3", Unchanged: 1, Pruned: 1, CreatedAt: t0.Add(2 * time.Minute)}, latest)
		assert.Equal(t, LibraryImport{ID: tok.ID, ActorUserID: token.UserID, CredentialKind: CredentialToken, SourceRevision: "ci-1", Unchanged: 1, PruneRequestID: staged.PruneRequestID, CreatedAt: t0.Add(time.Minute)}, tok)
		assert.Equal(t, LibraryImport{ID: seed.ID, ActorUserID: person.UserID, CredentialKind: CredentialPerson, SourceRevision: "seed", Created: 2, CreatedAt: t0}, seed)

		first, err := f.svc.ListLibraryImports(ctx(), person, ListLibraryImportsInput{PageSize: 2})
		require.NoError(t, err)
		require.Len(t, first.Imports, 2)
		require.NotEmpty(t, first.NextPageToken)
		rest, err := f.svc.ListLibraryImports(ctx(), person, ListLibraryImportsInput{PageSize: 2, PageToken: first.NextPageToken})
		require.NoError(t, err)
		require.Len(t, rest.Imports, 1)
		assert.Equal(t, seed.ID, rest.Imports[0].ID)
		other, err := f.svc.ListLibraryImports(ctx(), Actor{UserID: "u-b", OrgID: orgB, Kind: ActorPerson}, ListLibraryImportsInput{})
		require.NoError(t, err)
		assert.Empty(t, other.Imports, "each org's audit is its own")
	})
	t.Run("every import moves imported_at on each key it names, changed or not", func(t *testing.T) {
		f := newFixture(t)
		f.seedLibrary(t, "alpha")
		f.advance(time.Hour)
		res, err := f.svc.ImportStrategyLibrary(ctx(), token, ImportStrategyLibraryInput{SourceRevision: "ci-2", Entries: []LibraryEntry{libEntry("alpha", CategoryLaunchesPR)}})
		require.NoError(t, err)
		assert.Equal(t, 1, res.Unchanged)
		e, err := f.svc.GetStrategyLibraryEntry(ctx(), person, GetStrategyLibraryEntryInput{Key: "alpha"})
		require.NoError(t, err)
		assert.Equal(t, t0.Add(time.Hour), e.ImportedAt)
		assert.Equal(t, "ci-2", e.SourceRevision)
	})
	t.Run("a token is a person for the import alone", func(t *testing.T) {
		f := newFixture(t)
		f.seedLibrary(t, "alpha", "beta")
		staged := f.stage(t)
		_, err := f.svc.ConfirmLibraryPrune(ctx(), token, ConfirmLibraryPruneInput{RequestID: staged.PruneRequestID})
		require.ErrorIs(t, err, ErrPersonOnly, "a token cannot confirm its own prune")
		_, err = f.svc.CancelLibraryPrune(ctx(), token, CancelLibraryPruneInput{RequestID: staged.PruneRequestID})
		require.ErrorIs(t, err, ErrPersonOnly)

		// Every other person-only rule refuses it.
		i, err := f.svc.CreateIdea(ctx(), person, CreateIdeaInput{Kind: IdeaKindContent, Title: "t"})
		require.NoError(t, err)
		_, err = f.svc.SetIdeaStatus(ctx(), token, SetIdeaStatusInput{ID: i.ID, ExpectedRevision: i.Revision, Status: IdeaAccepted})
		require.ErrorIs(t, err, ErrPersonOnly)
		it := f.itemAt(t, FormatShortPost, ItemReview)
		_, err = f.svc.UpdateItem(ctx(), token, UpdateItemInput{ID: it.ID, ExpectedRevision: it.Revision, Status: ptr(ItemApproved)})
		require.ErrorIs(t, err, ErrPersonOnly)
		_, err = f.svc.CreateStrategy(ctx(), token, CreateStrategyInput{Name: "n", Status: StrategyActive})
		require.ErrorIs(t, err, ErrPersonOnly)
		_, err = f.svc.CreateChannel(ctx(), token, CreateChannelInput{Key: "gm", Name: "Mail", Mode: ChannelIntegrated, IntegrationKind: GmailIntegration})
		require.ErrorIs(t, err, ErrPersonOnly)
		require.ErrorIs(t, f.svc.DeleteAsset(ctx(), token, DeleteAssetInput{AssetID: "x"}), ErrPersonOnly)
		_, err = f.svc.ResolveInDoubtSend(ctx(), token, ResolveInDoubtSendInput{ItemID: "x", ApprovalHash: "h"})
		require.ErrorIs(t, err, ErrPersonOnly)

		_, err = f.svc.ListLibraryImports(ctx(), Actor{UserID: "u", OrgID: orgA, Kind: ActorAgent, ViaToken: true}, ListLibraryImportsInput{})
		require.ErrorIs(t, err, ErrNotFound, "only a person acts through a token")
	})
}

func TestLibraryPruneRequests(t *testing.T) {
	t.Run("confirm removes the request's keys, skipping one imported again since", func(t *testing.T) {
		f := newFixture(t)
		f.seedLibrary(t, "alpha", "beta", "gamma")
		f.advance(time.Minute)
		staged := f.stage(t)
		f.advance(time.Hour)
		// gamma comes back in a later import, before anyone confirms.
		_, err := f.svc.ImportStrategyLibrary(ctx(), token, ImportStrategyLibraryInput{SourceRevision: "ci-2", Entries: []LibraryEntry{libEntry("gamma", CategoryLaunchesPR)}})
		require.NoError(t, err)
		f.advance(time.Hour)

		_, err = f.svc.ConfirmLibraryPrune(ctx(), agent, ConfirmLibraryPruneInput{RequestID: staged.PruneRequestID})
		require.ErrorIs(t, err, ErrPersonOnly)
		res, err := f.svc.ConfirmLibraryPrune(ctx(), person, ConfirmLibraryPruneInput{RequestID: staged.PruneRequestID})
		require.NoError(t, err)
		assert.Equal(t, 1, res.Removed, "beta only")
		assert.Equal(t, []string{"alpha", "gamma"}, f.libraryKeys(t))
		assert.Equal(t, PruneRequestConfirmed, res.Request.Status)
		assert.Equal(t, person.UserID, res.Request.DecidedBy)
		assert.Equal(t, f.now, res.Request.DecidedAt)
		assert.Equal(t, []string{"beta", "gamma"}, res.Request.RemoveKeys)

		_, err = f.svc.ConfirmLibraryPrune(ctx(), person, ConfirmLibraryPruneInput{RequestID: staged.PruneRequestID})
		require.ErrorIs(t, err, ErrPruneRequestNotPending)
		assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
		_, err = f.svc.CancelLibraryPrune(ctx(), person, CancelLibraryPruneInput{RequestID: staged.PruneRequestID})
		require.ErrorIs(t, err, ErrPruneRequestNotPending)
	})
	t.Run("cancel removes nothing and closes the request", func(t *testing.T) {
		f := newFixture(t)
		f.seedLibrary(t, "alpha", "beta")
		staged := f.stage(t)
		_, err := f.svc.CancelLibraryPrune(ctx(), agent, CancelLibraryPruneInput{RequestID: staged.PruneRequestID})
		require.ErrorIs(t, err, ErrPersonOnly)
		r, err := f.svc.CancelLibraryPrune(ctx(), person, CancelLibraryPruneInput{RequestID: staged.PruneRequestID})
		require.NoError(t, err)
		assert.Equal(t, PruneRequestCancelled, r.Status)
		assert.Equal(t, person.UserID, r.DecidedBy)
		assert.Equal(t, []string{"alpha", "beta"}, f.libraryKeys(t))
		_, err = f.svc.ConfirmLibraryPrune(ctx(), person, ConfirmLibraryPruneInput{RequestID: staged.PruneRequestID})
		require.ErrorIs(t, err, ErrPruneRequestNotPending)
	})
	t.Run("a request older than 7 days is EXPIRED: listed so, refused, and recorded", func(t *testing.T) {
		f := newFixture(t)
		f.seedLibrary(t, "alpha", "beta")
		staged := f.stage(t)
		list := func(status PruneRequestStatus) []LibraryPruneRequest {
			res, err := f.svc.ListLibraryPruneRequests(ctx(), person, ListLibraryPruneRequestsInput{Status: status})
			require.NoError(t, err)
			return res.Requests
		}
		f.advance(LibraryPruneRequestTTL - time.Second)
		assert.Len(t, list(PruneRequestPending), 1, "still pending")
		f.advance(time.Second)
		assert.Empty(t, list(PruneRequestPending))
		expired := list(PruneRequestExpired)
		require.Len(t, expired, 1, "answered EXPIRED before anyone writes it")
		assert.Equal(t, PruneRequestExpired, expired[0].Status)

		_, err := f.svc.ConfirmLibraryPrune(ctx(), person, ConfirmLibraryPruneInput{RequestID: staged.PruneRequestID})
		require.ErrorIs(t, err, ErrPruneRequestExpired)
		assert.Equal(t, []string{"alpha", "beta"}, f.libraryKeys(t), "nothing removed")
		assert.Equal(t, string(PruneRequestExpired), f.store.r2().prunes[staged.PruneRequestID].Status, "written EXPIRED once noticed")
		assert.Nil(t, f.store.r2().prunes[staged.PruneRequestID].DecidedBy, "an expiry is no one's decision")
		_, err = f.svc.CancelLibraryPrune(ctx(), person, CancelLibraryPruneInput{RequestID: staged.PruneRequestID})
		require.ErrorIs(t, err, ErrPruneRequestExpired)
		assert.Len(t, list(PruneRequestExpired), 1)
		assert.Len(t, list(""), 1)
	})
	t.Run("requests are the org's own and validate their input", func(t *testing.T) {
		f := newFixture(t)
		f.seedLibrary(t, "alpha", "beta")
		staged := f.stage(t)
		b := Actor{UserID: "u-b", OrgID: orgB, Kind: ActorPerson}
		_, err := f.svc.ConfirmLibraryPrune(ctx(), b, ConfirmLibraryPruneInput{RequestID: staged.PruneRequestID})
		require.ErrorIs(t, err, ErrNotFound)
		_, err = f.svc.ConfirmLibraryPrune(ctx(), person, ConfirmLibraryPruneInput{})
		require.ErrorIs(t, err, ErrInvalidArgument)
		_, err = f.svc.ListLibraryPruneRequests(ctx(), person, ListLibraryPruneRequestsInput{Status: "BOGUS"})
		require.ErrorIs(t, err, ErrInvalidArgument)
		res, err := f.svc.ListLibraryPruneRequests(ctx(), b, ListLibraryPruneRequestsInput{})
		require.NoError(t, err)
		assert.Empty(t, res.Requests)
		res, err = f.svc.ListLibraryPruneRequests(ctx(), agent, ListLibraryPruneRequestsInput{Status: PruneRequestPending})
		require.NoError(t, err, "an agent may read them")
		assert.Len(t, res.Requests, 1)
	})
	t.Run("a concurrent decision wins once", func(t *testing.T) {
		f := newFixture(t)
		f.seedLibrary(t, "alpha", "beta")
		staged := f.stage(t)
		require.NoError(t, f.store.DecidePruneRequest(context.Background(), orgA, staged.PruneRequestID, PruneRequestCancelled, "someone", f.now))
		require.ErrorIs(t, f.store.DecidePruneRequest(context.Background(), orgA, staged.PruneRequestID, PruneRequestConfirmed, "someone", f.now), ErrPruneRequestNotPending)
	})
}
