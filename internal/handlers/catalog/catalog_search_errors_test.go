// Copyright (c) 2025 Reliant Labs
package catalog

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/catalogindex"
)

// What a search failure tells the caller: catalogindex's sentinels are svcerr
// kinds and keep their text; anything unclassified is the handler's Internal,
// with its own text withheld.
func TestCatalogSearchErrorCodes(t *testing.T) {
	cases := map[string]struct {
		err  error
		code connect.Code
		msg  string
	}{
		"not found":      {catalogindex.ErrNotFound, connect.CodeNotFound, "catalog entry not found"},
		"bad page token": {catalogindex.ErrInvalidPageToken, connect.CodeInvalidArgument, "invalid page token"},
		"query too long": {catalogindex.ErrQueryTooLong, connect.CodeInvalidArgument, "query is too long"},
		"canceled":       {context.Canceled, connect.CodeCanceled, ""},
		"deadline":       {context.DeadlineExceeded, connect.CodeDeadlineExceeded, ""},
		"opaque":         {errors.New("dsn=postgres://u:secret@h"), connect.CodeInternal, "catalog search failed"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			search := &StubSearch{GetFunc: func(context.Context, string, string) (*catalogindex.Entry, bool, error) { return nil, false, c.err }}
			svc := NewTestCatalog(t, WithCatalogDeps(Deps{Search: search}))
			_, err := svc.GetCatalogEntry(asUser("alice"), connect.NewRequest(&reliantv1.GetCatalogEntryRequest{Ref: "acme.thing"}))
			require.Error(t, err)
			assert.Equal(t, c.code, svcerr.Code(err))
			msg := svcerr.ToConnect(err).Message()
			assert.NotContains(t, msg, "secret")
			if c.msg != "" {
				assert.Equal(t, c.msg, msg)
			}
		})
	}
}

// Over a real Connect client, through the svcerr interceptor.
func TestCatalogErrorsOnTheWire(t *testing.T) {
	search := &StubSearch{SearchFunc: func(context.Context, string, catalogindex.Query) (*catalogindex.Result, error) {
		return nil, catalogindex.ErrInvalidPageToken
	}}
	_, client := NewTestCatalogServer(t, WithCatalogDeps(Deps{Search: search}))
	_, err := client.SearchCatalog(context.Background(), connect.NewRequest(&reliantv1.SearchCatalogRequest{PageToken: "x"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
	assert.Equal(t, "invalid page token", svcerr.ToConnect(err).Message())
}
