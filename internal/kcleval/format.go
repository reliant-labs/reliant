package kcleval

import (
	"encoding/json"
	"fmt"
	"strconv"

	"sigs.k8s.io/yaml"
)

// Format is how a Result is printed.
type Format string

const (
	// FormatJSON is the default: the value as JSON, indented.
	FormatJSON Format = "json"
	// FormatYAML is the value as YAML, which is what kcl's own `-S` prints.
	FormatYAML Format = "yaml"
	// FormatRaw is a scalar's own text, with no quoting and no trailing
	// newline. See Render.
	FormatRaw Format = "raw"
)

// Formats are the accepted --format values, for the flag's help and its
// refusal message.
func Formats() []string { return []string{string(FormatJSON), string(FormatYAML), string(FormatRaw)} }

// Render turns a Result into the bytes to print.
//
// # Why FormatRaw exists
//
// It is the format that removes the reason anyone reached for `kcl` directly.
// `kcl run -S <field>` prints YAML, and YAML QUOTES a scalar that would
// otherwise parse as something else — so every caller wanting a plain string
// wrote a cleanup pipeline. control-plane's build-workspace-sbd.sh carries
// six of them:
//
//	BUILDER_REPO="$(kcl run -S sbd.builder_repo "${KCL}" | tr -d "\n'\"")"
//
// That `tr` deletes every newline, single quote and double quote ANYWHERE in
// the value, so a value legitimately containing one comes back silently
// corrupted, and the pipeline is why nobody noticed the quoting rule differed
// per value. FormatRaw prints the scalar's own text and nothing else — no
// quotes, no trailing newline — so `$(...)` captures exactly the value:
//
//	BUILDER_REPO="$(forge kcl eval "${KCL}" -S sbd.builder_repo --format raw)"
//
// A number prints as KCL's own integer where it is integral, not Go's float64
// rendering: `-S sbd.disk_size_gb` must produce `200` for a caller passing it
// to `gcloud --boot-disk-size`, and `200` is what the KCL says. JSON has one
// number type, so an integral float64 is the only thing an integer can decode
// to and printing it as `200` is a restoration, not a guess.
//
// It refuses a composite rather than falling back to JSON. A caller asking
// for raw wants a value it can interpolate into a command line; handing it
// `{"a":1}` would interpolate cleanly and mean nothing, and the failure would
// surface as whatever that command did with a JSON blob. Naming the problem
// at the boundary is the whole value of a typed refusal here.
func Render(r Result, f Format) ([]byte, error) {
	switch f {
	case FormatRaw:
		if !r.Scalar {
			return nil, fmt.Errorf("--format raw prints a single scalar, and this selection is %s; "+
				"select one field, or use --format json", kindOf(r.Value))
		}
		return []byte(rawScalar(r.Value)), nil
	case FormatYAML:
		out, err := yaml.Marshal(r.Value)
		if err != nil {
			return nil, fmt.Errorf("encode as YAML: %w", err)
		}
		return out, nil
	case FormatJSON, "":
		out, err := json.MarshalIndent(r.Value, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("encode as JSON: %w", err)
		}
		return append(out, '\n'), nil
	}
	return nil, fmt.Errorf("unknown format %q (want one of: %v)", f, Formats())
}

// rawScalar is a scalar's own text.
//
// None prints as the empty string rather than "null": raw's consumer is
// `$(...)`, where an absent value is an empty capture and the literal text
// "null" is a value that looks present. A caller that must distinguish them
// has --format json, where None is null.
func rawScalar(v any) string {
	switch s := v.(type) {
	case nil:
		return ""
	case string:
		return s
	case bool:
		return strconv.FormatBool(s)
	case float64:
		if s == float64(int64(s)) {
			return strconv.FormatInt(int64(s), 10)
		}
		return strconv.FormatFloat(s, 'f', -1, 64)
	}
	return fmt.Sprint(v)
}
