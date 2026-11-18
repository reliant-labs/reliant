package cli

import (
	"errors"
	"fmt"
	"testing"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

// A failed embedded-postgres download must fail generate even without
// --strict; downgrading it to a warning produced zero ORM/mock output.
func TestWarnOrFail_PostgresFetchFailureIsFatal(t *testing.T) {
	ctx := &pipelineContext{Strict: false}
	fe := &pgtest.FetchError{URL: "https://repo1.maven.org/x.txz", Status: 503, Attempts: 4}
	err := ctx.warnOrFail("ORM generation", fmt.Errorf("introspect: %w", fe))
	if err == nil {
		t.Fatal("warnOrFail swallowed a postgres download failure")
	}
	var got *pgtest.FetchError
	if !errors.As(err, &got) {
		t.Fatalf("error lost the FetchError: %v", err)
	}
	if err := ctx.warnOrFail("other", errors.New("boom")); err != nil {
		t.Fatalf("ordinary failures must stay warnings outside --strict: %v", err)
	}
}
