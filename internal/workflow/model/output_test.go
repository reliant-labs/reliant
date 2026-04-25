package model

import (
	"reflect"
	"testing"
)

func TestIsSkippedOutput(t *testing.T) {
	tests := []struct {
		name   string
		output interface{}
		want   bool
	}{
		{"nil", nil, false},
		{"skipped map", map[string]interface{}{"skipped": true}, true},
		{"not skipped map", map[string]interface{}{"skipped": false}, false},
		{"no skipped field", map[string]interface{}{"result": "ok"}, false},
		{"skipped string field", map[string]interface{}{"skipped": "yes"}, false},
		{"struct with skipped", struct {
			Skipped bool `json:"skipped"`
		}{Skipped: true}, true},
		{"struct without skipped", struct {
			Result string `json:"result"`
		}{Result: "ok"}, false},
		{"non-serializable", func() {}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsSkippedOutput(tt.output); got != tt.want {
				t.Errorf("IsSkippedOutput() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSkippedOutputMap(t *testing.T) {
	m := SkippedOutputMap()
	if m["skipped"] != true {
		t.Errorf("skipped = %v, want true", m["skipped"])
	}
	if !IsSkippedOutput(m) {
		t.Error("SkippedOutputMap() should be detected as skipped")
	}
}

func TestSkippedRunOutputMap(t *testing.T) {
	m := SkippedRunOutputMap()
	if m["skipped"] != true {
		t.Errorf("skipped = %v, want true", m["skipped"])
	}
	if m["exit_code"] != 0 {
		t.Errorf("exit_code = %v, want 0", m["exit_code"])
	}
	if m["stdout"] != "" {
		t.Errorf("stdout = %v, want empty", m["stdout"])
	}
	if m["stderr"] != "" {
		t.Errorf("stderr = %v, want empty", m["stderr"])
	}
	if m["log_file"] != "" {
		t.Errorf("log_file = %v, want empty", m["log_file"])
	}
	if !IsSkippedOutput(m) {
		t.Error("SkippedRunOutputMap() should be detected as skipped")
	}
}

func TestLoopOutputToMap(t *testing.T) {
	outputs := map[string]interface{}{
		"exit_code": 0,
		"stdout":    "ok",
	}
	m := LoopOutputToMap(3, outputs)
	if m["_iterations"] != 3 {
		t.Errorf("_iterations = %v, want 3", m["_iterations"])
	}
	if m["exit_code"] != 0 {
		t.Errorf("exit_code = %v, want 0", m["exit_code"])
	}
	if m["stdout"] != "ok" {
		t.Errorf("stdout = %v, want ok", m["stdout"])
	}
}

func TestLoopOutputToMapEmpty(t *testing.T) {
	m := LoopOutputToMap(0, nil)
	if m["_iterations"] != 0 {
		t.Errorf("_iterations = %v, want 0", m["_iterations"])
	}
}

func TestJoinOutputToMap(t *testing.T) {
	sources := []map[string]interface{}{
		{"result": "a"},
		{"result": "b"},
	}
	m := JoinOutputToMap(sources)
	got := m["_sources"].([]map[string]interface{})
	if len(got) != 2 {
		t.Fatalf("sources len = %d, want 2", len(got))
	}
	if got[0]["result"] != "a" {
		t.Errorf("sources[0].result = %v", got[0]["result"])
	}
}

func TestJoinOutputToMapNil(t *testing.T) {
	m := JoinOutputToMap(nil)
	// A nil slice stored in interface{} is not nil — it's a typed nil.
	// The field should exist but hold a nil slice.
	if _, ok := m["_sources"]; !ok {
		t.Error("_sources key should exist")
	}
}

func TestWorkflowOutputToMap(t *testing.T) {
	outputs := map[string]interface{}{
		"summary": "done",
		"count":   42,
	}
	m := WorkflowOutputToMap(outputs)
	if m["summary"] != "done" {
		t.Errorf("summary = %v", m["summary"])
	}
	if m["count"] != 42 {
		t.Errorf("count = %v", m["count"])
	}
}

func TestParseAttachments(t *testing.T) {
	tests := []struct {
		name string
		s    string
		want []string
	}{
		{"empty", "", nil},
		{"single", "file1.txt", []string{"file1.txt"}},
		{"json array", `["file1.txt","file2.txt"]`, []string{"file1.txt", "file2.txt"}},
		{"empty json array", `[]`, []string{}},
		{"json single", `["file1.txt"]`, []string{"file1.txt"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseAttachments(tt.s)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseAttachments(%q) = %v, want %v", tt.s, got, tt.want)
			}
		})
	}
}

func TestFieldNameConstants(t *testing.T) {
	if LoopOutputIterationsField != "_iterations" {
		t.Errorf("LoopOutputIterationsField = %q", LoopOutputIterationsField)
	}
	if JoinOutputSourcesField != "_sources" {
		t.Errorf("JoinOutputSourcesField = %q", JoinOutputSourcesField)
	}
	if SkippedOutputField != "skipped" {
		t.Errorf("SkippedOutputField = %q", SkippedOutputField)
	}
}
