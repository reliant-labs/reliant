package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"sigs.k8s.io/yaml"
)

// SecretCopy is one existing Secret a chart install copies into another
// namespace, by value (HelmChartSpec.SecretCopies). The copy keeps the
// source's type and data and nothing else of its metadata but Labels.
type SecretCopy struct {
	FromNamespace, FromName string
	ToNamespace, ToName     string
	Labels                  map[string]string
}

func (c SecretCopy) String() string {
	return fmt.Sprintf("%s/%s -> %s/%s", c.FromNamespace, c.FromName, c.ToNamespace, c.ToName)
}

// readSecretForCopy reads a Secret's type and data from the cluster. A var so
// tests state the source without a cluster.
var readSecretForCopy = func(ctx context.Context, kctx, namespace, name string) (string, map[string]string, error) {
	out, err := kubectlCmd(ctx, kctx, "get", "secret", name, "-n", namespace, "-o", "json").CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "NotFound") || strings.Contains(string(out), "not found") {
			return "", nil, fmt.Errorf("secret %s/%s does not exist on %s", namespace, name, kctx)
		}
		return "", nil, fmt.Errorf("kubectl get secret %s/%s: %v: %s", namespace, name, err, strings.TrimSpace(string(out)))
	}
	var parsed struct {
		Type string            `json:"type"`
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return "", nil, fmt.Errorf("parse secret %s/%s: %w", namespace, name, err)
	}
	return parsed.Type, parsed.Data, nil
}

// applySecretCopyFn writes one copy. A var so tests capture the document.
var applySecretCopyFn = KubectlApplyNamespaced

// secretCopyDocument is the copy as the apply sends it. data stays base64, as
// the source holds it, so no value is decoded in forge's memory.
func secretCopyDocument(c SecretCopy, secretType string, data map[string]string) (string, error) {
	meta := map[string]any{"name": c.ToName, "namespace": c.ToNamespace}
	if len(c.Labels) > 0 {
		meta["labels"] = c.Labels
	}
	doc := map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": meta, "data": data}
	if secretType != "" {
		doc["type"] = secretType
	}
	b, err := yaml.Marshal(doc)
	return string(b), err
}

// applySecretCopies copies each Secret, in order. A missing source fails the
// install, naming it: an install that went on would leave the copy's reader
// (a registry pull) failing later with an unrelated-looking auth error.
func applySecretCopies(ctx context.Context, kctx string, copies []SecretCopy) error {
	for _, c := range copies {
		secretType, data, err := readSecretForCopy(ctx, kctx, c.FromNamespace, c.FromName)
		if err != nil {
			return fmt.Errorf("copy secret %s: %w", c, err)
		}
		doc, err := secretCopyDocument(c, secretType, data)
		if err != nil {
			return fmt.Errorf("copy secret %s: %w", c, err)
		}
		if err := applySecretCopyFn(ctx, kctx, c.ToNamespace, doc); err != nil {
			return fmt.Errorf("copy secret %s: %w", c, err)
		}
	}
	return nil
}
