package cli

// The bundle recorder seam F4 left narrow, now that it carries what the
// hosted RPC requires — plus the one apply property that survives.
//
// WHAT IS ACTUALLY UNDER TEST. The widening exists to close a failure that
// COMPILES: a seam that cannot carry the bundle's own bytes forces a client
// to DESCRIBE the bundle instead, and every reader downstream then trusts the
// description over the artifact. So the first test asserts on the REQUEST
// BODY, not only the return value — both what is present and what must be
// absent.
//
// The apply-guard tests that stood here are GONE, not moved. They pinned a
// compare-and-set forge sent when forge was the actuator; forge does not
// apply, so there is no such request to assert on. What remains is F-2, which
// is a property of the RECORD rather than of any apply forge performs: an
// apply that began and never reported must not block the next one.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// TestRecordBundle_HostedSeamSendsBytesNotTheDescription is the bundle half.
// The server records what it VERIFIES: it checks digest = sha256(manifest),
// checks the manifest's config descriptor names sha256(config), and takes the
// shape, provenance, config digest and release from its own strict decode of
// that blob (doc §6.3).
//
// So the assertion is as much about what is ABSENT as what is present. A
// client that could state a shape could state one that disagrees with the
// bytes it pushed, and every reader downstream would then trust the
// description over the artifact.
func TestRecordBundle_HostedSeamSendsBytesNotTheDescription(t *testing.T) {
	t.Parallel()
	f := &fakeDSOTCaller{replies: map[string]any{
		procEnsureEnv:    map[string]any{"environment": map[string]any{"id": "env_prod"}},
		procRecordBundle: map[string]any{"bundle": json.RawMessage(cpBundleFixture), "created": true},
	}}
	var recorder bundleRecorder = hostedRecordStore{
		client: f, project: "app", resolver: stubEnvResolver{id: "env_prod"},
	}

	rec, created, err := recorder.RecordBundle(context.Background(),
		release.BundleRecord{
			Env: "prod", Shape: dsotTestShape(),
			// A DELIBERATELY WRONG description. The record that comes
			// back must carry the server's values, not these — which is
			// the whole point of the server deriving them from the bytes.
			Digest:       "sha256:" + rep64('0'),
			ConfigDigest: "sha256:" + rep64('0'),
			Release:      "v0.0.0-not-this",
		},
		bundleBlobs{
			Repository: "ghcr.io/acme/app/bundle.v1/prod",
			Manifest:   []byte(`{"schemaVersion":2}`),
			Config:     []byte(`{"schema":"forge.dev/bundle/v1"}`),
		})
	if err != nil {
		t.Fatalf("RecordBundle: %v", err)
	}
	if !created || rec.ID != "bnd_1" {
		t.Errorf("created=%v id=%q, want created and bnd_1", created, rec.ID)
	}
	if rec.Release != "v1.4.0" || rec.ConfigDigest != "sha256:"+rep64('e') {
		t.Errorf("the record must come from the SERVER's verified decode, not the caller's description: %+v", rec)
	}

	body := f.body(t, procRecordBundle)
	wantFields(t, body, map[string]any{
		"environmentId": "env_prod",
		"repository":    "ghcr.io/acme/app/bundle.v1/prod",
		"manifest":      "eyJzY2hlbWFWZXJzaW9uIjoyfQ==",
		"config":        "eyJzY2hlbWEiOiJmb3JnZS5kZXYvYnVuZGxlL3YxIn0=",
	})
	for _, described := range []string{"shape", "provenance", "configDigest", "digest", "releaseVersion"} {
		if _, present := body[described]; present {
			t.Errorf("RecordBundle must not send %q: the server derives it from the verified config blob, "+
				"and a client that can state it can state one that disagrees with the bytes it pushed", described)
		}
	}
}

// TestMachineLedgerApply_AnAbandonedApplyDoesNotBlockTheNextOne is failure
// mode F-2, in the machine ledger's storage.
//
// An apply that began and never reported is a DIFFERENT fact from one that
// failed: past its deadline nobody is ever going to report, so the row reads
// ABANDONED. The next must not be refused by it. The opposite — an in-flight
// refusal that outlives the writer — would wedge an env permanently after one
// crash, with no way through but a flag whose whole meaning is "override a
// live apply".
//
// Exercised against the store rather than a seam, because the applyRecorder
// seam was deleted as dead code. The file semantics still matter — a stale
// in-flight row must not be able to wedge a reader or a future import.
func TestMachineLedgerApply_AnAbandonedApplyDoesNotBlockTheNextOne(t *testing.T) {
	dir := newLedgerTestProject(t, "abandoned-apply-project")
	store := testRecordStore(t, dir).store

	// An apply whose deadline has already passed, and which never
	// reported — the shape a crashed or network-partitioned writer leaves.
	past := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	abandoned, err := store.BeginApply(release.Apply{
		Env: "prod", BundleID: "bundle-1",
		CreatedAt: past, DeadlineAt: past.Add(10 * time.Minute),
	}, false)
	if err != nil {
		t.Fatalf("BeginApply: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	next, err := store.BeginApply(release.Apply{
		Env: "prod", BundleID: "bundle-2",
		CreatedAt: now, DeadlineAt: now.Add(10 * time.Minute),
	}, false)
	if err != nil {
		t.Fatalf("an apply past its deadline with no outcome is ABANDONED and must not block the next: %v", err)
	}
	if next.ID == abandoned.ID {
		t.Fatal("the second apply must be its own record")
	}
	// And it must NOT be marked as having superseded one: nothing was
	// overridden, because nothing was in flight. A false supersede mark
	// would put an override in the audit trail that never happened.
	if next.SupersededInFlight {
		t.Error("stepping past an abandoned apply is not a supersede — nothing was in flight to override")
	}
}

// stubEnvResolver answers the env-id lookup without a control plane. The
// resolution is not what these tests are about; what forge SENDS once it has
// an id is.
type stubEnvResolver struct{ id string }

func (s stubEnvResolver) ResolveEnvironmentID(context.Context, string) (string, error) {
	return s.id, nil
}
