// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
)

// A valid token narrower than the call was told to re-authenticate, which
// changes nothing. scope_missing now names the scope and the fix
// (north-star AW-49).
func TestAnnotateScopeMissing(t *testing.T) {
	t.Parallel()
	scoped := func(scopes string) error {
		e := connect.NewError(connect.CodePermissionDenied, errors.New("this credential lacks the scope"))
		e.Meta().Set(svcerr.ReasonHeader, fat.ReasonScopeMissing)
		if scopes != "" {
			e.Meta().Set(fat.ScopesHeader, scopes)
		}
		return e
	}
	c := &connection{ServerURL: "https://api.example.test", Token: "rlat_x", TokenFrom: envToken}

	got := c.annotate(scoped("reliant:api")).Error()
	assert.Contains(t, got, "needs reliant:api, the whole API")
	assert.Contains(t, got, "runs:start cover the RunService")
	assert.NotContains(t, got, "re-authenticate")

	got = c.annotate(scoped("triggers:fire")).Error()
	assert.Contains(t, got, "--scope triggers:fire")

	got = c.annotate(connect.NewError(connect.CodePermissionDenied, errors.New("no"))).Error()
	assert.Contains(t, got, "re-authenticate", "any other denial keeps its hint")
}
