package reviewpublisheradapter

import (
	"context"
	"strings"

	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/integrations/writeledger"
	"github.com/reliant-labs/reliant/internal/prhandoff"
	"github.com/reliant-labs/reliant/internal/prhandoffadapter"
	"github.com/reliant-labs/reliant/internal/reviewpublisher"
)

// GitHub implements core HeadSource using existing authenticated catalog reads.
// Construct for a server-loaded assignment: owner/site/name are not model input.
type GitHub struct {
	OwnerID, RepoName string
	Site              httpaction.CallSite
	Catalog           *catalog.Catalog
	Runner            writeledger.Runner
	Credentials       httpaction.CredentialSource
}

// CurrentHead refetches stable repository and PR facts; unavailable/error cannot
// become a guessed current SHA. Forks, closed PRs and name/ID ambiguity reject.
func (g GitHub) CurrentHead(ctx context.Context, repoID, number int64) (reviewpublisher.Head, error) {
	if repoID <= 0 || number <= 0 || !strings.Contains(g.RepoName, "/") {
		return reviewpublisher.Head{}, reviewpublisher.ErrUnauthorized
	}
	provider := prhandoffadapter.GitHub{OwnerID: g.OwnerID, Site: g.Site, Catalog: g.Catalog, Runner: g.Runner, Credentials: g.Credentials}
	p, err := provider.Pull(ctx, g.OwnerID, prhandoff.Repository{ID: repoID, Name: strings.ToLower(g.RepoName)}, number)
	if err != nil {
		return reviewpublisher.Head{}, reviewpublisher.ErrUnavailable
	}
	if !p.Open || p.Number != number || p.Repo.ID != repoID || p.HeadRepoID != repoID || len(p.HeadSHA) != 40 {
		return reviewpublisher.Head{}, reviewpublisher.ErrUnauthorized
	}
	return reviewpublisher.Head{RepositoryID: p.Repo.ID, PullNumber: p.Number, SHA: p.HeadSHA}, nil
}
