package cli

import "testing"

func TestDeclaredImageTag(t *testing.T) {
	d := "@sha256:" + "a0b1c2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b1"
	for ref, want := range map[string]string{
		"nats:2.10.24-alpine" + d:                            "2.10.24-alpine",
		"docker.io/library/nats:2.10.24-alpine" + d:          "2.10.24-alpine",
		"gcr.io/cloud-sql-connectors/cloud-sql-proxy:2.14.1": "2.14.1",
		"registry.test:5000/team/image:v1" + d:               "v1",
		"registry.test:5000/team/image" + d:                  "", // a port is not a tag
		"docker.io/library/nats" + d:                         "",
		"nats":                                               "",
		"nats:-bad" + d:                                      "",
	} {
		if got := declaredImageTag(ref); got != want {
			t.Errorf("declaredImageTag(%q) = %q, want %q", ref, got, want)
		}
	}
}
