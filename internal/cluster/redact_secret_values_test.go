package cluster

import (
	"strings"
	"testing"
)

func TestRedactSecretValuesKeepsShapeDropsValues(t *testing.T) {
	stream := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cfg\ndata:\n  k: visible\n---\n" +
		"apiVersion: v1\nkind: Secret\nmetadata:\n  name: creds\nstringData:\n  token: hunter2\ndata:\n  b64: aHVudGVyMg==\n"
	got := RedactSecretValues(stream)
	for _, leaked := range []string{"hunter2", "aHVudGVyMg=="} {
		if strings.Contains(got, leaked) {
			t.Errorf("%q survived redaction:\n%s", leaked, got)
		}
	}
	for _, kept := range []string{"visible", "name: creds", "token:", "b64:"} {
		if !strings.Contains(got, kept) {
			t.Errorf("%q should be kept:\n%s", kept, got)
		}
	}
	if RedactSecretValues("kind: ConfigMap\nmetadata:\n  name: x\n") != "kind: ConfigMap\nmetadata:\n  name: x\n" {
		t.Error("a stream with no Secret must come back untouched")
	}
}
