package tool_call_test

import (
	"context"
	"fmt"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/db"
	tool_call "github.com/reliant-labs/reliant/internal/handlers/tool_call"
)

// unknownToolCallRepo answers a tool call lookup the way db.Repo does for an
// id it has no row for. The generated harness's stub answers (nil, nil)
// instead, which the handlers read as found. Every other method is
// unreachable from the rows that use it: the embedded nil Repository makes
// calling one a panic, never a silent pass.
type unknownToolCallRepo struct{ tool_call.Repository }

func (unknownToolCallRepo) GetContentBlockByToolCallID(_ context.Context, id string) (*db.MessageContentBlock, error) {
	return nil, fmt.Errorf("%w: %s", svcerr.NotFound("tool call"), id)
}
