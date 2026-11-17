package cli

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/release"
)

// What a hosted bundle's document may carry is what its control plane says it
// decodes. A control plane built before forge#850 strict-decodes the document
// and refused every bundle with `unknown field "commit_subject"`; it serves no
// GetCapabilities at all, so "the RPC is missing" must read as "no feature",
// and a reply must be taken at its word.
func TestHostedBundleReaderIsWhatTheControlPlaneDecodes(t *testing.T) {
	cases := map[string]struct {
		caller   *fakeDSOTCaller
		want     []string
		wantFail bool
	}{
		"a control plane that predates GetCapabilities (Unimplemented)": {
			caller: &fakeDSOTCaller{errs: map[string]error{procDeployCapabilities: &cloud.Error{HTTPStatus: 404, Code: cloud.CodeUnimplemented}}},
			want:   []string{},
		},
		"a control plane that predates GetCapabilities (bare 404)": {
			caller: &fakeDSOTCaller{errs: map[string]error{procDeployCapabilities: &cloud.Error{HTTPStatus: 404}}},
			want:   []string{},
		},
		"a control plane with snapshot capabilities but not this one": {
			caller: &fakeDSOTCaller{replies: map[string]any{procDeployCapabilities: map[string]any{
				"snapshotSchemaVersions": []string{release.SnapshotSchema}, "immutableSnapshotSeals": true, "frozenPromotionBundles": true,
			}}},
			want: []string{},
		},
		"a control plane that decodes every feature": {
			caller: &fakeDSOTCaller{replies: map[string]any{procDeployCapabilities: map[string]any{
				"bundleDocumentFeatures": release.BundleDocFeatures(),
			}}},
			want: release.BundleDocFeatures(),
		},
		"a control plane that failed to answer": {
			caller:   &fakeDSOTCaller{errs: map[string]error{procDeployCapabilities: &cloud.Error{HTTPStatus: 503, Code: "unavailable"}}},
			wantFail: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			reader, err := hostedRecordStoreFor(tc.caller, "shop").BundleReader(context.Background())
			if tc.wantFail {
				var cerr *cloud.Error
				if err == nil || !errors.As(err, &cerr) {
					t.Fatalf("want the failure surfaced, got reader %+v err %v", reader, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if reader == nil {
				t.Fatal("a hosted reader must never be nil: nil means every feature")
			}
			got := reader.Features
			if got == nil {
				got = []string{}
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("features = %v, want %v", got, tc.want)
			}
			doc := reader.Limit(release.BundleDoc{
				Provenance: release.Provenance{Commit: "c", CommitSubject: "s", CommitAuthor: "a"},
				Objects:    []release.ObjectSummary{{}},
			})
			full := len(tc.want) == len(release.BundleDocFeatures())
			if (doc.Provenance.CommitSubject != "") != full || (len(doc.Objects) > 0) != full {
				t.Fatalf("limited document %+v for features %v", doc, tc.want)
			}
		})
	}
}
