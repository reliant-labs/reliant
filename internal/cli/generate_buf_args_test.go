package cli

import (
	"reflect"
	"testing"
)

func TestBufGenerateArgsDisablesTimeout(t *testing.T) {
	got := bufGenerateArgs("--template", "buf.gen.yaml", "--path", "proto")
	want := []string{"generate", "--timeout", "0", "--template", "buf.gen.yaml", "--path", "proto"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("bufGenerateArgs = %v, want %v", got, want)
	}
	if got := bufGenerateArgs(); !reflect.DeepEqual(got, []string{"generate", "--timeout", "0"}) {
		t.Fatalf("bufGenerateArgs() = %v", got)
	}
}
