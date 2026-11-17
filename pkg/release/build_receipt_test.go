package release

import "testing"

func TestReceiptRejectsDuplicateAndUndeclaredOutputContract(t *testing.T) {
	inputs := testBuildInputs()
	inputs.Outputs = []string{"x", "x"}
	if _, err := BuildInputDigest(inputs); err == nil {
		t.Fatal("duplicate declared outputs accepted")
	}
	receipt := BuildReceipt{Inputs: inputs, InputDigest: SpecDigest([]byte("forged")), Outputs: map[string]string{"x": SpecDigest([]byte("a")), "y": SpecDigest([]byte("b"))}}
	if err := receipt.Validate(); err == nil {
		t.Fatal("equal-length output map hid undeclared y")
	}
	inputs = testBuildInputs()
	inputs.Outputs = []string{"x", "z"}
	digest, err := BuildInputDigest(inputs)
	if err != nil {
		t.Fatal(err)
	}
	receipt.Inputs = inputs
	receipt.InputDigest = digest
	if err := receipt.Validate(); err == nil {
		t.Fatal("missing z accepted because map lengths matched")
	}
	receipt.Outputs = map[string]string{"x": SpecDigest([]byte("a")), "z": SpecDigest([]byte("b"))}
	if err := receipt.Validate(); err != nil {
		t.Fatal(err)
	}
}
