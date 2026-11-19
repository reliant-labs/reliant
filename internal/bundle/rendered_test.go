package bundle

import "testing"

func TestParseRenderedStream(t *testing.T) {
	stream := "# cluster: a, b\napiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: api\n  namespace: app\n---\n" +
		"# cluster: (none declared)\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cfg\n---\n" +
		"# cluster: a\napiVersion: v1\nkind: Secret\nmetadata:\n  name: db\n  namespace: app\nstringData:\n  k: v\n"
	got, err := ParseRenderedStream(stream)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d objects, want 3", len(got))
	}
	if d := got[0]; d.Kind != "Deployment" || d.Namespace != "app" || len(d.Clusters) != 2 || d.Body == nil {
		t.Errorf("deployment = %+v", d)
	}
	if got[1].Clusters != nil {
		t.Errorf("an unattributed document got clusters %v", got[1].Clusters)
	}
	if got[2].Kind != "Secret" || got[2].Body != nil {
		t.Errorf("a Secret's body must not be retained: %+v", got[2])
	}
	if _, err := ParseRenderedStream("# cluster: a\napiVersion: v1\nkind: ConfigMap\n"); err == nil {
		t.Error("a document with no name was accepted")
	}
}
