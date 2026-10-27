package wfyaml

import "testing"

func TestConcurrencyRoundTripAndUnknownFields(t *testing.T) {
	source := []byte("name: review\nautomation_only: true\nconcurrency:\n  max_running: 6\n  key: trigger.payload.subject\n  supersede: latest\n")
	wf, err := ParseWorkflow(source)
	if err != nil {
		t.Fatal(err)
	}
	if wf.GetConcurrency().GetMaxRunning() != 6 {
		t.Fatal("lost max_running")
	}
	out, err := MarshalWorkflow(wf)
	if err != nil {
		t.Fatal(err)
	}
	again, err := ParseWorkflow(out)
	if err != nil || again.GetConcurrency().GetKey() != "trigger.payload.subject" {
		t.Fatalf("round trip: %v", err)
	}
	if _, err = ParseWorkflow([]byte("concurrency: {max_running: 6, typo: true}")); err == nil {
		t.Fatal("unknown field accepted")
	}
	if _, err = ParseWorkflow([]byte("concurrency: {max_running: -1}")); err == nil {
		t.Fatal("negative unsigned capacity accepted")
	}
}
