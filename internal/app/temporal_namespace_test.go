package app

import (
	"context"
	"testing"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	"github.com/reliant-labs/reliant/internal/temporal"
	"go.temporal.io/sdk/client"
)

func TestTemporalClientDialAndReaderNamespaceAgree(t *testing.T) {
	cases := []struct {
		name   string
		config *configv1.TemporalConfig
		want   string
	}{
		{"omitted namespace", &configv1.TemporalConfig{Host: "temporal.invalid"}, "reliant"},
		{"explicit empty namespace", &configv1.TemporalConfig{Host: "temporal.invalid", Namespace: ""}, "reliant"},
		{"custom namespace", &configv1.TemporalConfig{Host: "temporal.invalid", Namespace: "private-reviews"}, "private-reviews"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			dialNamespace := ""
			wrapper, err := newTemporalClient(TemporalClientDeps{Ctx: context.Background(), Temporal: tt.config}, func(_ context.Context, cfg temporal.ExternalClientConfig) (client.Client, error) {
				calls++
				dialNamespace = cfg.Namespace
				return nil, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || dialNamespace != tt.want || wrapper.Namespace != tt.want {
				t.Fatalf("dial=%q reader=%q calls=%d want %q", dialNamespace, wrapper.Namespace, calls, tt.want)
			}
		})
	}
}
