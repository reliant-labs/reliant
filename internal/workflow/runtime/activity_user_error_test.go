// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"

	"github.com/reliant-labs/reliant/internal/errclass"
)

// A user error crossing the activity boundary loses every Go type and svcerr
// marker to serialization. In prod the workflow side then logged it at ERROR —
// "[StepExecutor] Activity failed after retry exhaustion: … AI provider usage
// limit reached" — and paged on a spent subscription. The category is what
// survives: Benign, read back by errclass as a user error.
func TestWrappedActivity_UserErrorsCrossTheBoundaryAsBenign(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		failure error
		want    svcerr.Class
	}{
		"marked user error": {
			failure: fmt.Errorf("resolve daemon: %w", svcerr.WithClass(errors.New("no daemon connected for user"), svcerr.ClassUser)),
			want:    svcerr.ClassUser,
		},
		// Recognised only by text, the way classifyError already recognises
		// it to stop retrying.
		"provider credit exhaustion": {
			failure: fmt.Errorf("failed to stream LLM response: %w", errors.New(`openai: 429 {"error":{"code":"insufficient_quota"}}`)),
			want:    svcerr.ClassUser,
		},
		"server fault": {
			failure: fmt.Errorf("persist message: %w", errors.New("pq: connection refused")),
			want:    svcerr.ClassServer,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := runWrappedFailure(t, func(context.Context, string) (string, error) { return "", tc.failure })

			var appErr *temporal.ApplicationError
			require.True(t, errors.As(err, &appErr), "the workflow must still see an ApplicationError")
			assert.Equal(t, tc.want, errclass.Classify(err), "workflow-side class of %v", err)
			wantCategory := temporal.ApplicationErrorCategoryUnspecified
			if tc.want == svcerr.ClassUser {
				wantCategory = temporal.ApplicationErrorCategoryBenign
			}
			assert.Equal(t, wantCategory, appErr.Category())
		})
	}
}

func TestMarkTextOnlyUserError(t *testing.T) {
	t.Parallel()
	usageLimit := errors.New("AI provider usage limit reached [RELIANT_PROVIDER_USAGE_LIMIT:2026-10-10T18:00:00Z]: 429")
	creditOut := errors.New("insufficient_quota: you exceeded your current quota")
	plain := errors.New("pq: connection refused")

	for name, err := range map[string]error{"usage limit": usageLimit, "credit exhausted": creditOut} {
		marked := markTextOnlyUserError(err)
		assert.Equal(t, svcerr.ClassUser, svcerr.Classify(marked), name)
		assert.ErrorIs(t, marked, err, "%s: the original must stay reachable", name)
		assert.Equal(t, err.Error(), marked.Error(), "%s: the text must not change", name)
	}
	assert.Same(t, plain, markTextOnlyUserError(plain), "a server fault is left alone")
	assert.Nil(t, markTextOnlyUserError(nil))
}
