package cluster

import (
	"context"
	"errors"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// TestApplySecretCopies: the copy keeps the source's type and data and
// nothing of its metadata but the given labels, and a missing source fails
// the install, naming it.
func TestApplySecretCopies(t *testing.T) {
	prevRead, prevApply := readSecretForCopy, applySecretCopyFn
	t.Cleanup(func() { readSecretForCopy, applySecretCopyFn = prevRead, prevApply })
	readSecretForCopy = func(_ context.Context, kctx, ns, name string) (string, map[string]string, error) {
		if name != "pull" {
			return "", nil, errors.New("secret " + ns + "/" + name + " does not exist on " + kctx)
		}
		return "kubernetes.io/dockerconfigjson", map[string]string{".dockerconfigjson": "e30="}, nil
	}
	var docs []string
	applySecretCopyFn = func(_ context.Context, kctx, ns, manifests string) error {
		docs = append(docs, kctx+"|"+ns+"|"+manifests)
		return nil
	}
	c := SecretCopy{FromNamespace: "prod", FromName: "pull", ToNamespace: "forge-delivery-p-prod", ToName: "forge-delivery-pull",
		Labels: map[string]string{"forge.dev/delivery-env": "prod"}}
	if err := applySecretCopies(context.Background(), "ctx", []SecretCopy{c}); err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 || !strings.HasPrefix(docs[0], "ctx|forge-delivery-p-prod|") {
		t.Fatalf("applied %v", docs)
	}
	var got map[string]any
	if err := yaml.Unmarshal([]byte(strings.SplitN(docs[0], "|", 3)[2]), &got); err != nil {
		t.Fatal(err)
	}
	md := got["metadata"].(map[string]any)
	if got["type"] != "kubernetes.io/dockerconfigjson" || got["data"].(map[string]any)[".dockerconfigjson"] != "e30=" ||
		md["name"] != "forge-delivery-pull" || md["namespace"] != "forge-delivery-p-prod" ||
		md["labels"].(map[string]any)["forge.dev/delivery-env"] != "prod" {
		t.Errorf("copy = %v", got)
	}

	c.FromName = "missing"
	err := applySecretCopies(context.Background(), "ctx", []SecretCopy{c})
	if err == nil || !strings.Contains(err.Error(), "prod/missing") {
		t.Errorf("missing source: err = %v", err)
	}
}
