package release

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func decodeYAML(t *testing.T, doc string) (string, any) {
	t.Helper()
	var body map[string]any
	if err := yaml.Unmarshal([]byte(doc), &body); err != nil {
		t.Fatal(err)
	}
	return body["kind"].(string), body
}

const deploymentYAML = `
apiVersion: apps/v1
kind: Deployment
metadata: {name: workers, labels: {app: workers}}
spec:
  replicas: 2
  template:
    metadata: {annotations: {checksum: abc}}
    spec:
      serviceAccountName: workers
      containers:
      - name: workers
        image: ghcr.io/acme/control-plane@sha256:%s
        env:
        - {name: LOG_LEVEL, value: info}
        - {name: FORCE_RECONCILE, value: "true"}
        resources: {limits: {cpu: "1"}}
        ports: [{containerPort: 8080}]
`

func TestSummarizeDeployment(t *testing.T) {
	kind, body := decodeYAML(t, fmt.Sprintf(deploymentYAML, strings.Repeat("a", 64)))
	summary, parts := SummarizeObject(kind, body)
	if summary != "2 replicas · containers workers" {
		t.Fatalf("summary = %q", summary)
	}
	for _, want := range []string{"replicas", "workers.env.LOG_LEVEL", "workers.env.FORCE_RECONCILE", "workers.resources", "workers.ports", "pod.serviceAccountName", "pod.annotations", "metadata.labels"} {
		if len(parts[want]) != PartDigestLen {
			t.Errorf("part %q missing or malformed: %q (parts %v)", want, parts[want], parts)
		}
	}
	// No value reaches the record: not the env value, not the image.
	raw, _ := json.Marshal(ObjectSummary{Key: ObjectKey{Kind: kind, Name: "workers"}, Summary: summary, Parts: parts})
	for _, leaked := range []string{"info", "ghcr.io", "sha256"} {
		if strings.Contains(string(raw), leaked) {
			t.Errorf("summary record carries %q: %s", leaked, raw)
		}
	}
	// The image is not a part: a new digest moves no section.
	_, bumped := decodeYAML(t, fmt.Sprintf(deploymentYAML, strings.Repeat("b", 64)))
	if _, again := SummarizeObject(kind, bumped); fmt.Sprint(again) != fmt.Sprint(parts) {
		t.Fatalf("an image digest change moved parts:\n%v\n%v", parts, again)
	}
}

func TestSummarizeOtherKinds(t *testing.T) {
	for _, tc := range []struct{ doc, summary, part string }{
		{"kind: ConfigMap\nmetadata: {name: c}\ndata: {force_reconcile: 'true', mode: plan}", "keys force_reconcile, mode", "data.force_reconcile"},
		{"kind: Service\nmetadata: {name: s}\nspec: {ports: [{port: 80}, {port: 443}]}", "ClusterIP · ports 80, 443", "spec.ports"},
		{"kind: PrometheusRule\nmetadata: {name: r}\nspec: {groups: [{rules: [{alert: Down}, {record: x}]}]}", "alerts Down · 1 recording rule", "spec.groups"},
		{"kind: CronJob\nmetadata: {name: j}\nspec: {schedule: '*/5 * * * *', jobTemplate: {spec: {template: {spec: {containers: [{name: run, image: x}]}}}}}", "schedule */5 * * * * · containers run", "schedule"},
		{"kind: Widget\nmetadata: {name: w}\nspec: {size: 3, color: blue}", "spec color, size", "spec.size"},
	} {
		kind, body := decodeYAML(t, tc.doc)
		summary, parts := SummarizeObject(kind, body)
		if summary != tc.summary {
			t.Errorf("%s summary = %q, want %q", kind, summary, tc.summary)
		}
		if parts[tc.part] == "" {
			t.Errorf("%s has no part %q: %v", kind, tc.part, parts)
		}
		if err := (ObjectSummary{Key: ObjectKey{Kind: kind, Name: "x"}, Summary: summary, Parts: parts}).Validate(); err != nil {
			t.Errorf("%s: %v", kind, err)
		}
	}
}

func TestSummarizeFoldsPartsPastTheBound(t *testing.T) {
	data := map[string]any{}
	for i := range MaxObjectParts + 10 {
		data[fmt.Sprintf("k%03d", i)] = "v"
	}
	_, parts := SummarizeObject("ConfigMap", map[string]any{"kind": "ConfigMap", "data": data})
	if len(parts) != MaxObjectParts || parts[PartOverflow] == "" {
		t.Fatalf("%d parts, overflow %q", len(parts), parts[PartOverflow])
	}
}

func TestObjectSummaryValidate(t *testing.T) {
	key := ObjectKey{Kind: "ConfigMap", Name: "c"}
	digest := strings.Repeat("a", PartDigestLen)
	for _, tc := range []struct {
		name string
		s    ObjectSummary
		ok   bool
	}{
		{"ok", ObjectSummary{Key: key, Summary: "keys a", Parts: map[string]string{"data.a": digest}}, true},
		{"value-like part name", ObjectSummary{Key: key, Parts: map[string]string{"data": digest}}, false},
		{"part carries a value", ObjectSummary{Key: key, Parts: map[string]string{"data.a": "hunter2"}}, false},
		{"two lines", ObjectSummary{Key: key, Summary: "a\nb"}, false},
		{"no name", ObjectSummary{Key: ObjectKey{Kind: "ConfigMap"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.s.Validate(); (err == nil) != tc.ok {
				t.Fatalf("Validate() = %v, want ok=%v", err, tc.ok)
			}
		})
	}
	shape := Shape{Objects: []ShapeObject{{Kind: "ConfigMap", Name: "c"}}}
	if err := ValidateObjectSummaries(shape, []ObjectSummary{{Key: ObjectKey{Kind: "ConfigMap", Name: "other"}}}); err == nil {
		t.Fatal("a summary of an object the shape lacks must be refused")
	}
	if err := ValidateObjectSummaries(shape, []ObjectSummary{{Key: key}, {Key: key}}); err == nil {
		t.Fatal("a duplicate summary must be refused")
	}
}

func TestBoundObjectSummariesShedsPartsFirst(t *testing.T) {
	var sums []ObjectSummary
	parts := map[string]string{}
	for i := range MaxObjectParts {
		parts[fmt.Sprintf("data.key-number-%03d", i)] = strings.Repeat("a", PartDigestLen)
	}
	for i := 0; i < 600; i++ {
		sums = append(sums, ObjectSummary{Key: ObjectKey{Kind: "ConfigMap", Name: fmt.Sprintf("c%d", i)}, Summary: "keys …", Parts: parts})
	}
	bounded := BoundObjectSummaries(sums)
	if len(bounded) != len(sums) || bounded[0].Parts != nil || bounded[0].Summary == "" {
		t.Fatalf("over the bound, parts go first: %d summaries, parts %v", len(bounded), bounded[0].Parts)
	}
	if small := sums[:1]; len(BoundObjectSummaries(small)[0].Parts) == 0 {
		t.Fatal("under the bound nothing is shed")
	}
}
