package gtm

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
)

// ImportStrategyLibrary upserts a batch of the org's strategy library
// entries (up to 500 entries and 5000 all_keys per call; the user chain caps
// no request size, ReadMaxBytes -1).
//
// It is the one rpc an rlat_ access token calls as a Person, ViaToken: the
// growth import CLI signs in with an API token (reliant auth token create).
// The library is the org's configuration and never goes out anywhere; the
// domain makes importing a person's act only so a chat agent cannot rewrite
// it. A token only upserts: its prune is staged as a request
// (prune_request_id, would_remove) that a person confirms or cancels in the
// app (ConfirmLibraryPrune), and every import is audited
// (ListLibraryImports). Exactly CredentialAccessToken: every other software
// credential (an OAuth client, an identity token) stays an Agent, as on every
// other rpc (actor).
func (s *Service) ImportStrategyLibrary(
	ctx context.Context,
	req *connect.Request[pb.ImportStrategyLibraryRequest],
) (*connect.Response[pb.ImportStrategyLibraryResponse], error) {
	m := req.Msg
	a, err := s.begin(ctx, m.GetOrgId(), m)
	if err != nil {
		return nil, err
	}
	if !a.Person() && auth.CredentialFromContext(ctx) == auth.CredentialAccessToken {
		a.Kind, a.ViaToken = gtmsvc.ActorPerson, true
	}
	entries := make([]gtmsvc.LibraryEntry, 0, len(m.GetEntries()))
	for _, e := range m.GetEntries() {
		entries = append(entries, libraryEntryIn(e))
	}
	res, err := s.deps.Service.ImportStrategyLibrary(ctx, a, gtmsvc.ImportStrategyLibraryInput{
		Entries:        entries,
		SourceRevision: m.GetSourceRevision(),
		Prune:          m.GetPrune(),
		AllKeys:        m.GetAllKeys(),
	})
	if err != nil {
		return nil, wireErr(err)
	}
	return connect.NewResponse(&pb.ImportStrategyLibraryResponse{
		Created:        count32(res.Created),
		Updated:        count32(res.Updated),
		Unchanged:      count32(res.Unchanged),
		Pruned:         count32(res.Pruned),
		PruneRequestId: res.PruneRequestID,
		WouldRemove:    res.WouldRemove,
	}), nil
}
