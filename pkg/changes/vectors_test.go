package changes

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

// The golden vectors. web-runtime/src/changes.test.ts reads the same file, so
// a vector that passes here and there is a result the twins agree on.
const vectorsPath = "testdata/vectors.json"

type vectorContext struct {
	TargetingKey string            `json:"targetingKey"`
	Attributes   map[string]string `json:"attributes"`
}

func (c vectorContext) eval() EvalContext {
	return EvalContext{TargetingKey: c.TargetingKey, Attributes: c.Attributes}
}

type vectorResult struct {
	Variant  string          `json:"variant"`
	Value    json.RawMessage `json:"value"` // absent: the caller's default
	Reason   Reason          `json:"reason"`
	Run      string          `json:"run"`
	Enrolled bool            `json:"enrolled"`
}

type vectorCase struct {
	Name     string        `json:"name"`
	Snapshot string        `json:"snapshot"`
	Key      string        `json:"key"`
	Context  vectorContext `json:"context"`
	Expect   vectorResult  `json:"expect"`
}

type splitVector struct {
	Variant  string `json:"variant"`
	WeightBP int    `json:"weight_bp"`
}

type vectors struct {
	Murmur3 []struct {
		Input string `json:"input"`
		Hash  uint32 `json:"hash"`
	} `json:"murmur3"`
	PreviewHash []struct {
		Key  string `json:"key"`
		Unit string `json:"unit"`
		Hash string `json:"hash"`
	} `json:"preview_hash"`
	Snapshots map[string]json.RawMessage `json:"snapshots"`
	Cases     []vectorCase               `json:"cases"`
	Invalid   struct {
		Snapshot string        `json:"snapshot"`
		Context  vectorContext `json:"context"`
		Keys     []string      `json:"keys"`
	} `json:"invalid"`
	Parse []struct {
		Name     string       `json:"name"`
		JSON     string       `json:"json"`
		OK       bool         `json:"ok"`
		Revision int64        `json:"revision"`
		Cases    []vectorCase `json:"cases"`
	} `json:"parse"`
	Properties struct {
		UnitPrefix string `json:"unit_prefix"`
		Ramp       struct {
			Key      string `json:"key"`
			Salt     string `json:"salt"`
			Units    int    `json:"units"`
			StepsBP  []int  `json:"steps_bp"`
			Enrolled []int  `json:"enrolled"`
		} `json:"ramp"`
		Resalt struct {
			Key       string   `json:"key"`
			Salts     []string `json:"salts"`
			PercentBP int      `json:"percent_bp"`
			Units     int      `json:"units"`
			Enrolled  []int    `json:"enrolled"`
			Overlap   int      `json:"overlap"`
		} `json:"resalt"`
		Split struct {
			Key    string         `json:"key"`
			Salt   string         `json:"salt"`
			Units  int            `json:"units"`
			Split  []splitVector  `json:"split"`
			Counts map[string]int `json:"counts"`
		} `json:"split"`
		Independence struct {
			Key        string `json:"key"`
			Salt       string `json:"salt"`
			Units      int    `json:"units"`
			PercentsBP []int  `json:"percents_bp"`
			Enrolled   []int  `json:"enrolled"`
			Treatment  []int  `json:"treatment"`
		} `json:"independence"`
	} `json:"properties"`
}

func loadVectors(t *testing.T) *vectors {
	t.Helper()
	data, err := os.ReadFile(vectorsPath)
	if err != nil {
		t.Fatal(err)
	}
	var v vectors
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("%s: %v", vectorsPath, err)
	}
	return &v
}

func (v *vectors) snapshot(t *testing.T, name string) *Snapshot {
	t.Helper()
	raw, ok := v.Snapshots[name]
	if !ok {
		t.Fatalf("no snapshot %q in %s", name, vectorsPath)
	}
	s, err := ParseSnapshot(raw)
	if err != nil {
		t.Fatalf("snapshot %q: %v", name, err)
	}
	return s
}

func checkResult(t *testing.T, name string, got Result, want vectorResult) {
	t.Helper()
	if got.Variant != want.Variant || got.Reason != want.Reason || got.Run != want.Run || got.Enrolled != want.Enrolled {
		t.Errorf("%s: got {variant %q reason %q run %q enrolled %v}, want {variant %q reason %q run %q enrolled %v}",
			name, got.Variant, got.Reason, got.Run, got.Enrolled, want.Variant, want.Reason, want.Run, want.Enrolled)
	}
	switch {
	case want.Value == nil && got.Value != nil:
		t.Errorf("%s: value %s, want the caller's default (nil)", name, got.Value)
	case want.Value != nil:
		var w bytes.Buffer
		if err := json.Compact(&w, want.Value); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got.Value, w.Bytes()) {
			t.Errorf("%s: value %s, want %s", name, got.Value, w.Bytes())
		}
	}
	if got.Enrolled != (got.Reason == ReasonRollout || got.Reason == ReasonExperiment) {
		t.Errorf("%s: enrolled %v with reason %q", name, got.Enrolled, got.Reason)
	}
}

func TestVectorsMurmur3(t *testing.T) {
	v := loadVectors(t)
	for _, c := range v.Murmur3 {
		if got := murmur3([]byte(c.Input), 0); got != c.Hash {
			t.Errorf("murmur3(%q) = %d, want %d", c.Input, got, c.Hash)
		}
	}
	for _, c := range v.PreviewHash {
		if got := PreviewHash(c.Key, c.Unit); got != c.Hash {
			t.Errorf("PreviewHash(%q, %q) = %s, want %s", c.Key, c.Unit, got, c.Hash)
		}
	}
}

func TestVectorsEvaluate(t *testing.T) {
	v := loadVectors(t)
	snaps := map[string]*Snapshot{}
	for _, c := range v.Cases {
		s, ok := snaps[c.Snapshot]
		if !ok {
			s = v.snapshot(t, c.Snapshot)
			snaps[c.Snapshot] = s
		}
		checkResult(t, c.Name, Evaluate(s, c.Key, c.Context.eval()), c.Expect)
	}
	if got := v.snapshot(t, "main").Revision(); got != 42 {
		t.Errorf("main revision = %d, want 42", got)
	}
}

// TestVectorsInvalidIsolated proves every malformed change in the main
// snapshot is invalid on its own: the valid changes beside them are the
// cases above, which pass against the same snapshot.
func TestVectorsInvalidIsolated(t *testing.T) {
	v := loadVectors(t)
	s := v.snapshot(t, v.Invalid.Snapshot)
	for _, key := range v.Invalid.Keys {
		checkResult(t, key, Evaluate(s, key, v.Invalid.Context.eval()), vectorResult{Reason: ReasonInvalid})
	}
}

func TestVectorsParse(t *testing.T) {
	v := loadVectors(t)
	for _, c := range v.Parse {
		s, err := ParseSnapshot([]byte(c.JSON))
		if (err == nil) != c.OK {
			t.Errorf("%s: err = %v, want ok %v", c.Name, err, c.OK)
			continue
		}
		if !c.OK {
			continue
		}
		if s.Revision() != c.Revision {
			t.Errorf("%s: revision %d, want %d", c.Name, s.Revision(), c.Revision)
		}
		for _, e := range c.Cases {
			checkResult(t, c.Name+": "+e.Key, Evaluate(s, e.Key, e.Context.eval()), e.Expect)
		}
	}
}

func TestEvaluateNilSnapshotIsUnknown(t *testing.T) {
	if got := Evaluate(nil, "x", EvalContext{TargetingKey: "u"}); got.Reason != ReasonUnknown || got.Value != nil {
		t.Errorf("Evaluate(nil) = %+v, want unknown with the caller's default", got)
	}
	var s *Snapshot
	if s.Revision() != 0 {
		t.Error("nil Snapshot revision is not 0")
	}
}
