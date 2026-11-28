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
	t.Parallel()
	var rules registry
	plain := errors.New("plain")
	assert.Equal(t, svcerr.ClassServer, rules.classify(plain))

	rules.register(func(err error) svcerr.Class {
		if errors.As(err, new(marker)) {
			return svcerr.ClassUser
		}
		return svcerr.ClassNone
	})
	rules.register(func(err error) svcerr.Class {
		if errors.As(err, new(marker)) {
			return svcerr.ClassCanceled
		}
		return svcerr.ClassNone
	})
	assert.Equal(t, svcerr.ClassUser, rules.classify(marker{plain}), "first verdict wins")
	assert.Equal(t, svcerr.ClassUser, rules.library(marker{plain}))
	assert.Equal(t, svcerr.ClassNone, rules.library(plain), "no rule has an opinion")
	assert.Equal(t, svcerr.ClassServer, rules.classify(plain))
	assert.Equal(t, svcerr.ClassServer, Classify(marker{plain}), "isolated rules do not affect the global registry")
}
