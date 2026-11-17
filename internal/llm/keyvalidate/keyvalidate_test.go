// Copyright (c) 2025 Reliant Labs

package keyvalidate

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

const pastedKey = "sk-ant-api03-keyvalidate-test-secret-9f8e7d"

// stubDriver answers ValidateKey with err; nothing else of llm.Driver is called.
type stubDriver struct {
	llm.Driver
	err error
}

func (d stubDriver) ValidateKey(context.Context) error { return d.err }

// builder is a buildDriver that records the key the driver was built with and returns d, or
// buildErr.
func builder(gotKey *string, d llm.Driver, buildErr error) buildDriver {
	return func(_ models.Model, _ models.Family, opts ...llm.DriverOption) (llm.Driver, error) {
		var o llm.DriverOptions
		for _, opt := range opts {
			opt(&o)
		}
		*gotKey = o.ApiKey
		if buildErr != nil {
			return nil, buildErr
		}
		return d, nil
	}
}

// The message Validate returns reaches the caller of an rpc (AddOrgProviderAPIKey and the
// settings and account services return it as the InvalidArgument text), so a provider or
// driver error that quotes the key never carries it out.
func TestValidate_MessageNeverCarriesTheKey(t *testing.T) {
	t.Parallel()
	quoting := func(prefix string) error {
		return errors.New(prefix + ` GET "https://api.example.test/v1/models?key=` + pastedKey + `": dial tcp: i/o timeout (bearer ` + pastedKey + `)`)
	}
	for _, tc := range []struct {
		name       string
		pasted     string
		driverErr  error
		buildErr   error
		wantPrefix string
	}{
		{name: "a provider error that is not about auth", pasted: pastedKey, driverErr: quoting("upstream:"), wantPrefix: "Validation failed: "},
		{name: "a driver that cannot be built", pasted: pastedKey, buildErr: quoting("init:"), wantPrefix: "Failed to initialize provider: "},
		{name: "a pasted key with surrounding whitespace", pasted: "  " + pastedKey + "\n", driverErr: quoting("upstream:"), wantPrefix: "Validation failed: "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var gotKey string
			ok, msg := validate(t.Context(), "anthropic", tc.pasted, builder(&gotKey, stubDriver{err: tc.driverErr}, tc.buildErr))
			require.False(t, ok)
			assert.True(t, strings.HasPrefix(msg, tc.wantPrefix), "message %q", msg)
			assert.NotContains(t, msg, pastedKey)
			assert.NotContains(t, msg, "sk-ant-api03", "no fragment of the key")
			assert.Contains(t, msg, redacted, "the rest of the provider's error is kept, with the key redacted")
			assert.Contains(t, msg, "i/o timeout")
			assert.Equal(t, tc.pasted, gotKey, "the driver is still built with the key it checks")
		})
	}
}

// An auth refusal is the fixed "Invalid API key"; a working key is valid.
func TestValidate_AuthRefusalAndSuccess(t *testing.T) {
	t.Parallel()
	var gotKey string
	ok, msg := validate(t.Context(), "anthropic", pastedKey,
		builder(&gotKey, stubDriver{err: errors.New("401 Unauthorized: invalid x-api-key " + pastedKey)}, nil))
	assert.False(t, ok)
	assert.Equal(t, "Invalid API key", msg)

	ok, msg = validate(t.Context(), "anthropic", pastedKey, builder(&gotKey, stubDriver{}, nil))
	assert.True(t, ok)
	assert.Equal(t, "API key is valid", msg)
	assert.Equal(t, pastedKey, gotKey)
}

func TestRedact(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "a [redacted] b [redacted]", redact("a k1-secret b k2-secret", "k1-secret", " k2-secret\n", ""))
	assert.Equal(t, "nothing to hide", redact("nothing to hide", "", "  "), "an empty key redacts nothing")
}
