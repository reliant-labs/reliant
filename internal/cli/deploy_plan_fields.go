package cli

// FIELD-LEVEL SUMMARIES on a forge-computed plan's changed objects.
//
// BuildPlan classifies by hash: it knows THAT prod/Deployment/app/api changed,
// not what in it. A reviewer approving "config_changed" on forty objects has
// to diff two renders by hand to know whether that is a replica count or a
// release label stamped on everything. Both bundles are content-addressed and
// fetchable, so the plan names the changed fields itself.
//
// DETAIL ONLY, NEVER THE DIGEST. A finding's Detail is excluded from the plan
// digest by design (pkg/release/plan.go), so this changes what a reviewer
// reads and never what an approval names — two machines that could and could
// not fetch the bundles compute the same digest.
//
// BEST-EFFORT. A bundle that cannot be fetched (no registry credential, an
// offline laptop) leaves every finding exactly as BuildPlan wrote it; the plan
// is still complete, only less specific.

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/bundle"
	"github.com/reliant-labs/forge/pkg/release"
)

// planFieldBudget bounds the two bundle fetches a field summary needs.
const planFieldBudget = 60 * time.Second

// planFieldsShown is how many changed paths a finding names before "+N more".
const planFieldsShown = 6

// annotateChangedFields names the changed fields of every config_changed and
// object_changed finding, by comparing the applied bundle's document for the
// subject with the candidate's.
func annotateChangedFields(ctx context.Context, projectDir, env string, plan *release.Plan, appliedRef, candidateRef string) {
	if plan == nil || appliedRef == "" || candidateRef == "" {
		return
	}
	var changed []int
	for i, f := range plan.Findings {
		if f.Code == release.FindingConfigChanged || f.Code == release.FindingObjectChanged {
			changed = append(changed, i)
		}
	}
	if len(changed) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, planFieldBudget)
	defer cancel()
	applied, err := planBundleDocuments(ctx, projectDir, env, appliedRef)
	if err != nil {
		return
	}
	candidate, err := planBundleDocuments(ctx, projectDir, env, candidateRef)
	if err != nil {
		return
	}
	for _, i := range changed {
		f := &plan.Findings[i]
		fields := bundle.ChangedFields(applied[f.Subject], candidate[f.Subject])
		if len(fields) == 0 {
			continue
		}
		summary := "fields: " + summarizeFields(fields)
		if f.Detail != "" {
			summary += "; " + f.Detail
		}
		f.Detail = summary
	}
}

// planBundleDocuments fetches one bundle and indexes its documents by the
// subject string a plan finding carries.
func planBundleDocuments(ctx context.Context, projectDir, env, ref string) (map[string]any, error) {
	fetched, err := fetchBundleRef(ctx, projectDir, env, ref)
	if err != nil {
		return nil, err
	}
	objects, err := bundle.Objects(fetched)
	if err != nil {
		return nil, err
	}
	out := make(map[string]any, len(objects))
	for key, doc := range objects {
		out[key.String()] = doc
	}
	return out, nil
}

func summarizeFields(fields []string) string {
	if len(fields) <= planFieldsShown {
		return strings.Join(fields, ", ")
	}
	return strings.Join(fields[:planFieldsShown], ", ") + " (+" + strconv.Itoa(len(fields)-planFieldsShown) + " more)"
}
