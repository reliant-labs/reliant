package buildtarget

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/reliant-labs/forge/pkg/release"
)

// ReadInputs captures an optional predeclared input contract without running a
// generator. Dynamic builders may omit it, but cannot claim automatic reuse.
func ReadInputs(path string) (release.BuildInputs, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return release.BuildInputs{}, "", err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxReceiptBytes+1))
	if err != nil {
		return release.BuildInputs{}, "", err
	}
	if len(data) > maxReceiptBytes {
		return release.BuildInputs{}, "", fmt.Errorf("build inputs exceed size bound")
	}
	var in release.BuildInputs
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return in, "", err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return in, "", fmt.Errorf("build inputs contain trailing data")
	}
	digest, err := release.BuildInputDigest(in)
	return in, digest, err
}
