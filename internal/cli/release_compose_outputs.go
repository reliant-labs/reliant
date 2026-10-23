package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/pkg/release"
)

// carryCompositionBinding copies only the captured base; local build state is
// neither a fallback nor an inventory source.
func carryCompositionBinding(candidate, base *release.Snapshot, key release.ConsumerKey, slot string) error {
	if base == nil {
		return fmt.Errorf("missing captured base")
	}
	found := false
	for _, b := range base.Bindings {
		if b.Consumer == key && b.Slot == slot {
			candidate.Bindings = append(candidate.Bindings, b)
			candidate.Artifacts[b.Artifact] = base.Artifacts[b.Artifact]
			found = true
		}
	}
	if !found {
		return fmt.Errorf("base lacks consumer slot %v/%s", key, slot)
	}
	return nil
}

// compositionPrimaryOutput associates immutable outputs with their requesting
// consumer before provenance validation. Build-backed slots cannot use a tag.
func compositionPrimaryOutput(ctx context.Context, w WorkloadEntity, slot string, opts buildOptions, results []buildResult, compositionSource release.Source) (string, *release.BuildReceipt, *release.Source, error) {
	ref := w.Spec.Image
	if ref == "" {
		ref = externalPushedRef(w, opts.release, opts.pushPlan.pushBase)
	}
	var receipt *release.BuildReceipt
	matchedOutput := false
	for _, r := range results {
		if !compositionResultMatches(r, w) {
			continue
		}
		if r.repository != "" && release.ValidDigest(r.digest) {
			ref = r.repository + "@" + r.digest
			matchedOutput = true
		}
		receipt = r.receipt
	}
	if w.Build.Type != "" && !matchedOutput {
		return "", nil, nil, fmt.Errorf("selected build-backed slot %s/%s lacks this invocation's immutable output", w.Name, slot)
	}
	if w.Build.Type == "shell" && receipt == nil {
		return "", nil, nil, fmt.Errorf("selected ShellBuild %s lacks an invocation receipt", w.Name)
	}
	if receipt != nil {
		if err := validateCompositionReceiptSource(ctx, projectDirForKCL(), w, *receipt, opts.composition.Entities); err != nil {
			return "", nil, nil, err
		}
	}
	var source *release.Source
	if w.Build.Type != "" && receipt == nil {
		source = &compositionSource
	}
	return ref, receipt, source, nil
}
func compositionResultMatches(r buildResult, w WorkloadEntity) bool {
	if r.err != nil {
		return false
	}
	if r.workload != "" {
		return r.workload == w.Name
	}
	return r.image == w.Image || strings.TrimSuffix(r.name, " (external)") == w.Name
}
