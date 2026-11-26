package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/buildtarget"
	"github.com/reliant-labs/forge/pkg/release"
)

// newBuildReceiptCmd is a builder-side protocol helper, not a build or release
// command. The builder supplies actual exported inputs; cwd is only attribution.
func newBuildReceiptCmd() *cobra.Command {
	var inputs string
	var outputs []string
	cmd := &cobra.Command{Use: "receipt", Short: "Emit a typed receipt for actual exported build inputs", Args: cobra.NoArgs, SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			path := os.Getenv(buildtarget.ReceiptPathEnv)
			if path == "" {
				return fmt.Errorf("%s is not set; receipt must run inside its build invocation", buildtarget.ReceiptPathEnv)
			}
			f, err := os.Open(inputs)
			if err != nil {
				return err
			}
			defer func() { _ = f.Close() }()
			data, err := io.ReadAll(io.LimitReader(f, (4<<20)+1))
			if err != nil {
				return err
			}
			if len(data) > 4<<20 {
				return fmt.Errorf("build inputs exceed 4 MiB")
			}
			var in release.BuildInputs
			decoder := json.NewDecoder(bytes.NewReader(data))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&in); err != nil {
				return fmt.Errorf("decode build inputs: %w", err)
			}
			if err := decoder.Decode(new(any)); err != io.EOF {
				return fmt.Errorf("build inputs must contain exactly one JSON object")
			}
			digest, err := release.BuildInputDigest(in)
			if err != nil {
				return err
			}
			out := map[string]string{}
			for _, v := range outputs {
				name, value, ok := strings.Cut(v, "=")
				if !ok || name == "" || out[name] != "" {
					return fmt.Errorf("output must be unique NAME=sha256:DIGEST")
				}
				out[name] = value
			}
			receipt := release.BuildReceipt{Inputs: in, InputDigest: digest, Outputs: out}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			source, err := buildtarget.CaptureSource(cmd.Context(), cwd, projectDirForKCL())
			if err != nil {
				return err
			}
			receipt.WorkingDirectory = source.ForRelease()
			return buildtarget.WriteReceipt(path, receipt)
		}}
	cmd.Flags().StringVar(&inputs, "inputs", "", "Typed BuildInputs JSON from the actual builder's exported source/recipe/tools (no secrets)")
	cmd.Flags().StringArrayVar(&outputs, "output", nil, "Resolved output NAME=sha256:DIGEST (repeatable; exactly covers inputs.outputs)")
	_ = cmd.MarkFlagRequired("inputs")
	return cmd
}
