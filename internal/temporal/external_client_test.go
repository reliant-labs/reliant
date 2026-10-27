// Copyright (c) 2025 Reliant Labs
package temporal

import (
	"testing"

	"go.temporal.io/sdk/interceptor"
)

func TestExternalClientEffectiveNamespace(t *testing.T) {
	for _, tt := range []struct {
		name string
		cfg  ExternalClientConfig
		want string
	}{
		{"omitted", ExternalClientConfig{Host: "temporal.invalid"}, "reliant"},
		{"empty", ExternalClientConfig{Host: "temporal.invalid", Namespace: ""}, "reliant"},
		{"custom", ExternalClientConfig{Host: "temporal.invalid", Namespace: "private-reviews"}, "private-reviews"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			opts, err := externalClientOptions(tt.cfg)
			if err != nil {
				t.Fatal(err)
			}
			if opts.Namespace != tt.want || EffectiveNamespace(tt.cfg.Namespace) != tt.want {
				t.Fatalf("effective dial namespace %q want %q", opts.Namespace, tt.want)
			}
		})
	}
}

// The tracing interceptor goes on the CLIENT, once: there it injects the
// caller's span into each workflow it starts, and — because it is also a
// worker interceptor — every worker built from the client continues that
// trace. Installed only on a worker it never sees the header.
func TestExternalClientInstallsTracingOnTheClient(t *testing.T) {
	opts, err := externalClientOptions(ExternalClientConfig{Host: "temporal.invalid"})
	if err != nil {
		t.Fatalf("externalClientOptions: %v", err)
	}
	if len(opts.Interceptors) != 1 {
		t.Fatalf("client interceptors: got %d, want exactly the tracing one", len(opts.Interceptors))
	}
	if _, ok := opts.Interceptors[0].(interceptor.WorkerInterceptor); !ok {
		t.Errorf("%T is not also a worker interceptor, so workers built from the client would not continue the trace", opts.Interceptors[0])
	}
}
