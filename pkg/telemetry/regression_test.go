package telemetry

import (
	"fmt"
	"reflect"
	"testing"
)

func TestDecodeNormalizeLabelLimits(t *testing.T) {
	for _, tc := range []struct {
		name, fields   string
		labels, length int
	}{
		{"omitted", "", DefaultLabelLimit, DefaultLabelValueLengthLimit},
		{"disabled", `,"label_limit":0,"label_value_length_limit":0`, 0, 0},
		{"custom", `,"label_limit":12,"label_value_length_limit":64`, 12, 64},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in, err := Decode([]byte(fmt.Sprintf(`[{"metrics":[{"port":9090%s}]}]`, tc.fields)))
			if err != nil {
				t.Fatal(err)
			}
			out := Normalize(in)
			m := out[0].Metrics[0]
			if *m.LabelLimit != tc.labels || *m.LabelValueLengthLimit != tc.length {
				t.Fatalf("limits = %d, %d", *m.LabelLimit, *m.LabelValueLengthLimit)
			}
			if !reflect.DeepEqual(out, Normalize(out)) {
				t.Fatal("not idempotent")
			}
		})
	}
	// Custom decoding must not hide misspelled keys.
	if _, err := Decode([]byte(`[{"metrics":[{"label_limt":0}]}]`)); err == nil {
		t.Fatal("unknown field accepted")
	}
}

func TestSecretMountPathComponents(t *testing.T) {
	for _, value := range []string{"", ".", "..", "../token", "a/b", `a\b`, "/absolute", `C:\secret`, "bad\x00key"} {
		for _, component := range []string{"secret", "key"} {
			for _, signal := range []string{"metrics", "profiles"} {
				for _, credential := range []string{"bearer_token", "tls.ca"} {
					t.Run(fmt.Sprintf("%s/%s/%s/%q/%s", signal, credential, component, value, "rejected"), func(t *testing.T) {
						mount := &SecretMount{Secret: "token-secret", Key: "token", Path: "injected/path"}
						if component == "secret" {
							mount.Secret = value
						} else {
							mount.Key = value
						}
						in := workloadTarget()
						in.Metrics = nil
						in.Profiles = nil
						var bearer *SecretMount
						var tls *TLS
						if credential == "bearer_token" {
							bearer = mount
						} else {
							tls = &TLS{CA: mount}
						}
						if signal == "metrics" {
							in.Metrics = []MetricsScrape{{Port: 9090, Scheme: "https", BearerToken: bearer, TLS: tls}}
						} else {
							in.Profiles = []PprofScrape{{Port: 9090, Scheme: "https", BearerToken: bearer, TLS: tls}}
						}
						out := Normalize([]Target{in})
						requireProblem(t, Validate(out, Options{Clusters: []string{"k3d-acme"}}), credential, "safe single path components")
						var got *SecretMount
						if signal == "metrics" {
							got = out[0].Metrics[0].BearerToken
							if tls != nil {
								got = out[0].Metrics[0].TLS.CA
							}
						} else {
							got = out[0].Profiles[0].BearerToken
							if tls != nil {
								got = out[0].Profiles[0].TLS.CA
							}
						}
						if got.Path != "" {
							t.Fatalf("unsafe mount has path %q", got.Path)
						}
					})
				}
			}
		}
	}
	for _, mount := range []*SecretMount{{Secret: "token-secret", Key: "token"}, {Secret: "ca-secret", Key: "ca.crt"}} {
		in := workloadTarget()
		in.Metrics[0].Scheme = "https"
		in.Metrics[0].BearerToken = mount
		in.Metrics[0].TLS = &TLS{CA: mount}
		out := Normalize([]Target{in})
		if err := Validate(out, Options{Clusters: []string{"k3d-acme"}}); err != nil {
			t.Fatal(err)
		}
		if out[0].Metrics[0].BearerToken.Path != mount.Secret+"/"+mount.Key {
			t.Fatal("valid mount path changed")
		}
	}
}
