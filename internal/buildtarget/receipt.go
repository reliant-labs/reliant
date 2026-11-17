package buildtarget

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/reliant-labs/forge/pkg/release"
)

// ReceiptPathEnv is reserved for an invocation-specific builder output. A
// receipt is never discovered by scanning state or inherited from a prior build.
const ReceiptPathEnv = "FORGE_BUILD_RECEIPT_PATH"
const maxReceiptBytes = 4 << 20

// ReadReceipt requires a complete typed builder attestation. The expected input
// digest, when supplied, was captured before executing the build.
func ReadReceipt(path, expected string) (*release.BuildReceipt, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read build receipt: %w", err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxReceiptBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxReceiptBytes {
		return nil, fmt.Errorf("build receipt exceeds %d bytes", maxReceiptBytes)
	}
	var r release.BuildReceipt
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&r); err != nil {
		return nil, fmt.Errorf("decode build receipt: %w", err)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("build receipt must contain exactly one JSON object")
	}
	if err := ValidateReceipt(r, expected); err != nil {
		return nil, err
	}
	return &r, nil
}

// ValidateReceipt delegates input identity to the shared release contract. Cwd
// attribution cannot substitute for the exported sources in Inputs.Sources.
func ValidateReceipt(r release.BuildReceipt, expected string) error {
	if err := r.Validate(); err != nil {
		return err
	}
	if expected != "" && expected != r.InputDigest {
		return fmt.Errorf("build inputs changed during execution")
	}
	return nil
}

// WriteReceipt atomically creates one invocation receipt. A second write is a
// protocol error, not permission to replace the bytes an adapter already read.
func WriteReceipt(path string, r release.BuildReceipt) error {
	if path == "" {
		return fmt.Errorf("%s is not set", ReceiptPathEnv)
	}
	if err := ValidateReceipt(r, ""); err != nil {
		return err
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > maxReceiptBytes {
		return fmt.Errorf("build receipt too large")
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".receipt-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Link is an atomic no-replace publish on the same filesystem.
	if err = os.Link(tmp.Name(), path); err != nil {
		return fmt.Errorf("publish build receipt: %w", err)
	}
	return nil
}
