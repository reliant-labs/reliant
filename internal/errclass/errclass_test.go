// Copyright (c) 2025 Reliant Labs
package errclass

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want svcerr.Class
	}{
		{"nil", nil, svcerr.ClassNone},
		{"svcerr user kind", svcerr.FailedPrecondition("no machine"), svcerr.ClassUser},
		{"marked user", svcerr.WithClass(errors.New("machine offline"), svcerr.ClassUser), svcerr.ClassUser},
		{"connect 4xx", connect.NewError(connect.CodeInvalidArgument, errors.New("bad")), svcerr.ClassUser},
		{"context canceled", context.Canceled, svcerr.ClassCanceled},
		{"raw error", errors.New("pq: connection refused"), svcerr.ClassServer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, Classify(tc.err))
			assert.Equal(t, tc.want == svcerr.ClassServer, IsServerError(tc.err))
		})
	}
}

type marker struct{ error }

// TestRegisteredRulesAreConsulted pins the seam: a registered rule's verdict
// wins over svcerr, ClassNone defers, and registration order breaks ties.
func TestRegisteredRulesAreConsulted(t *testing.T) {
	// Keep this test non-parallel so registration cannot overlap parallel tests.
	// Leave existing rules intact; these rules only recognise this test's marker.
	plain := errors.New("plain")
	assert.Equal(t, svcerr.ClassServer, Classify(plain))

	Register(func(err error) svcerr.Class {
		if errors.As(err, new(marker)) {
			return svcerr.ClassUser
		}
		return svcerr.ClassNone
	})
	Register(func(err error) svcerr.Class {
		if errors.As(err, new(marker)) {
			return svcerr.ClassCanceled
		}
		return svcerr.ClassNone
	})
	assert.Equal(t, svcerr.ClassUser, Classify(marker{plain}), "first verdict wins")
	assert.Equal(t, svcerr.ClassUser, Library(marker{plain}))
	assert.Equal(t, svcerr.ClassNone, Library(plain), "no rule has an opinion")
	assert.Equal(t, svcerr.ClassServer, Classify(plain))
}
