package cli

// The observability files of a release bundle (console v2 contract K4).
//
// A bundle carries, beside its manifests, every dashboard the project's
// release should show and the metrics schema of the binary it ships:
//
//	observability/dashboards/<name>.json   see obsdash.CollectBundleSet
//	observability/metrics-schema.json      bin/observability/metrics-schema.json
//
// Nothing applies them; the control plane reads them when the release is
// promoted. They are validated when the release is CUT, with file:line errors,
// because a dashboard that fails in the console is found by a customer.

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/reliant-labs/forge/internal/bundle"
	"github.com/reliant-labs/forge/internal/obsdash"
)

// collectObservabilityFiles assembles the bundle's observability set from the
// project and the build output directory ("" means bin).
func collectObservabilityFiles(projectDir, outputDir string) (obsdash.BundleSet, error) {
	if outputDir == "" {
		outputDir = "bin"
	}
	if !filepath.IsAbs(outputDir) {
		outputDir = filepath.Join(projectDir, outputDir)
	}
	in := obsdash.BundleSetInput{ProjectDir: projectDir}
	raw, err := os.ReadFile(filepath.Join(outputDir, filepath.FromSlash(MetricsSchemaArtifact)))
	switch {
	case err == nil:
		in.MetricsSchema = raw
	case os.IsNotExist(err):
	default:
		return obsdash.BundleSet{}, err
	}
	return obsdash.CollectBundleSet(in)
}

// bundleObservabilityFiles is the set as bundle files. A set that does not
// validate fails a strict write (the build and cut) and is dropped with a
// warning otherwise.
func bundleObservabilityFiles(projectDir string, in bundleBuildInputs) ([]bundle.ObservabilityFile, error) {
	set, err := collectObservabilityFiles(projectDir, in.OutputDir)
	if err != nil {
		if in.StrictObservability {
			return nil, err
		}
		fmt.Fprintf(in.errWriter(), "[bundle] Warning: the bundle carries no dashboards, because they do not validate:\n%v\n", err)
		return nil, nil
	}
	for _, n := range set.Notes {
		fmt.Fprintf(in.errWriter(), "[bundle] Note: %s\n", n)
	}
	files := obsdash.BundleFiles(set)
	out := make([]bundle.ObservabilityFile, 0, len(files))
	for _, f := range files {
		out = append(out, bundle.ObservabilityFile{Path: f.Path, Data: f.Data})
	}
	return out, nil
}
