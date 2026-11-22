package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/bundle"
	"github.com/reliant-labs/forge/internal/exitcode"
	"github.com/reliant-labs/forge/pkg/fluxdelivery"
)

// deliveryObjects is deliveryStream parsed, the shape a release bundle's
// objects arrive in.
func deliveryObjects(t *testing.T, extra ...string) []bundle.RenderedObject {
	t.Helper()
	objs, err := bundle.ParseRenderedStream(deliveryStream(extra...))
	if err != nil {
		t.Fatal(err)
	}
	return objs
}

// recordOf is the record the bootstrap writes for objs, as a deploy reads it.
func recordOf(t *testing.T, objs []bundle.RenderedObject, fd FluxDeliveryEntity) fluxDeliveryGrantsRecord {
	t.Helper()
	ids, err := fluxDeliveryIdentityObjectsOf(objs, fd)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range ids {
		if o.Kind != "ConfigMap" || o.Name != fluxdelivery.GrantsRecord {
			continue
		}
		data := map[string]string{}
		for k, v := range o.Body["data"].(map[string]any) {
			data[k] = v.(string)
		}
		rec, err := parseFluxDeliveryGrantsRecord(data)
		if err != nil {
			t.Fatal(err)
		}
		return rec
	}
	t.Fatal("the bootstrap renders no grants record")
	return fluxDeliveryGrantsRecord{}
}

// TestFluxDeliveryGrantsRecordIsTheGrant: the record the bootstrap writes
// lists exactly the permissions its RBAC objects grant, so a release the
// bootstrap was derived from is covered by it, line for line.
func TestFluxDeliveryGrantsRecordIsTheGrant(t *testing.T) {
	t.Parallel()
	for _, scope := range []string{"namespace", "cluster"} {
		t.Run(scope, func(t *testing.T) {
			t.Parallel()
			fd := deliveryEntity(scope)
			objs := deliveryObjects(t)
			rec := recordOf(t, objs, fd)
			needs, err := fluxDeliveryNeeds(objs, fd)
			if err != nil {
				t.Fatal(err)
			}
			if len(needs) == 0 || len(rec.Permissions) != len(needs) {
				t.Fatalf("record holds %d permissions, the release needs %d", len(rec.Permissions), len(needs))
			}
			if missing := fluxDeliveryShortfall(needs, rec); len(missing) != 0 {
				t.Fatalf("the bootstrap's own release falls short of its record: %v", missing)
			}
			for line := range rec.Permissions {
				p, err := parseFluxDeliveryPermission(line)
				if err != nil || p.line() != line {
					t.Errorf("record line %q does not round-trip (%v)", line, err)
				}
			}
		})
	}
}

// TestFluxDeliveryShortfall is the grant diff: a new kind, a removed kind, and
// a scope change.
func TestFluxDeliveryShortfall(t *testing.T) {
	t.Parallel()
	fd := deliveryEntity("namespace")
	granted := recordOf(t, deliveryObjects(t), fd)

	t.Run("a new cluster-scoped kind is pre's", func(t *testing.T) {
		sc := onCtx("apiVersion: storage.k8s.io/v1\nkind: StorageClass\nmetadata:\n  name: fast\nprovisioner: x\n")
		needs, err := fluxDeliveryNeeds(deliveryObjects(t, sc), fd)
		if err != nil {
			t.Fatal(err)
		}
		rows := fluxDeliveryGrantRows(fluxDeliveryShortfall(needs, granted))
		if len(rows) != 1 || rows[0].Identity != fluxdelivery.PreServiceAccount ||
			rows[0].Resource != "storage.k8s.io/storageclasses" || rows[0].Scope != "(cluster)" {
			t.Fatalf("rows = %+v, want pre's StorageClass grant, cluster-wide", rows)
		}
	})
	t.Run("a new workload kind is the apply identity's, in its namespace", func(t *testing.T) {
		sts := onCtx("apiVersion: apps/v1\nkind: StatefulSet\nmetadata:\n  name: cache\n  namespace: acme-prod\n")
		needs, err := fluxDeliveryNeeds(deliveryObjects(t, sts), fd)
		if err != nil {
			t.Fatal(err)
		}
		rows := fluxDeliveryGrantRows(fluxDeliveryShortfall(needs, granted))
		if len(rows) != 1 {
			t.Fatalf("rows = %+v, want the one StatefulSet grant", rows)
		}
		r := rows[0]
		if r.Identity != fluxdelivery.ApplyServiceAccount || r.Resource != "apps/statefulsets" || r.Scope != "(namespace acme-prod)" {
			t.Fatalf("row = %+v", r)
		}
	})
	t.Run("a removed kind is no shortfall", func(t *testing.T) {
		objs := deliveryObjects(t)
		var fewer []bundle.RenderedObject
		for _, o := range objs {
			if o.Kind != "Widget" {
				fewer = append(fewer, o)
			}
		}
		needs, err := fluxDeliveryNeeds(fewer, fd)
		if err != nil {
			t.Fatal(err)
		}
		if missing := fluxDeliveryShortfall(needs, granted); len(missing) != 0 {
			t.Fatalf("a release that needs less is short of %v", missing)
		}
	})
	t.Run("a namespace the grant never reached", func(t *testing.T) {
		other := onCtx("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cfg\n  namespace: shop\n")
		needs, err := fluxDeliveryNeeds(deliveryObjects(t, other), fd)
		if err != nil {
			t.Fatal(err)
		}
		rows := fluxDeliveryGrantRows(fluxDeliveryShortfall(needs, granted))
		if len(rows) == 0 || rows[0].Scope != "(namespace shop)" {
			t.Fatalf("rows = %+v, want the pre identity's ConfigMap grant in shop", rows)
		}
	})
	t.Run("a cluster-scoped grant covers every namespace", func(t *testing.T) {
		wide := deliveryEntity("cluster")
		grantedWide := recordOf(t, deliveryObjects(t), wide)
		other := onCtx("apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: worker\n  namespace: shop\n")
		needs, err := fluxDeliveryNeeds(deliveryObjects(t, other), wide)
		if err != nil {
			t.Fatal(err)
		}
		if missing := fluxDeliveryShortfall(needs, grantedWide); len(missing) != 0 {
			t.Fatalf("delivery_scope = cluster grants deployments everywhere, yet short of %v", missing)
		}
	})
	t.Run("no record grants nothing", func(t *testing.T) {
		needs, err := fluxDeliveryNeeds(deliveryObjects(t), fd)
		if err != nil {
			t.Fatal(err)
		}
		if missing := fluxDeliveryShortfall(needs, fluxDeliveryGrantsRecord{}); len(missing) != len(needs) {
			t.Fatalf("an absent record covered %d of %d permissions", len(needs)-len(missing), len(needs))
		}
	})
}

// stubGrants states the record and the access review for planFluxDeliveryGrants.
func stubGrants(t *testing.T, rec fluxDeliveryGrantsRecord, deny func(fluxDeliveryAccessCheck) bool) *[]fluxDeliveryAccessCheck {
	t.Helper()
	prevRead, prevReview := fluxDeliveryReadGrants, fluxDeliveryAccessReview
	t.Cleanup(func() { fluxDeliveryReadGrants, fluxDeliveryAccessReview = prevRead, prevReview })
	fluxDeliveryReadGrants = func(context.Context, FluxDeliveryEntity) (fluxDeliveryGrantsRecord, error) { return rec, nil }
	asked := &[]fluxDeliveryAccessCheck{}
	fluxDeliveryAccessReview = func(_ context.Context, _ string, checks []fluxDeliveryAccessCheck) ([]fluxDeliveryAccessCheck, error) {
		*asked = append(*asked, checks...)
		var denied []fluxDeliveryAccessCheck
		for _, c := range checks {
			if deny(c) {
				denied = append(denied, c)
			}
		}
		return denied, nil
	}
	return asked
}

// TestPlanFluxDeliveryGrants is the preflight's decision: covered, re-grant
// with the caller's own authority, or refuse.
//
// Not parallel: it swaps package seams.
func TestPlanFluxDeliveryGrants(t *testing.T) {
	fd := deliveryEntity("namespace")
	granted := recordOf(t, deliveryObjects(t), fd)
	pdb := onCtx("apiVersion: policy/v1\nkind: PodDisruptionBudget\nmetadata:\n  name: api\n  namespace: acme-prod\n")

	t.Run("covered: no review, no grant", func(t *testing.T) {
		asked := stubGrants(t, granted, func(fluxDeliveryAccessCheck) bool { return true })
		plan, err := planFluxDeliveryGrants(context.Background(), "prod", "v2", fd, deliveryObjects(t))
		if err != nil || len(plan.Missing) != 0 || plan.Regrant {
			t.Fatalf("plan = %+v, err = %v", plan, err)
		}
		if len(*asked) != 0 {
			t.Errorf("a covered release asked the API server %d questions", len(*asked))
		}
	})
	t.Run("short, and the caller can grant: re-grant", func(t *testing.T) {
		asked := stubGrants(t, granted, func(fluxDeliveryAccessCheck) bool { return false })
		plan, err := planFluxDeliveryGrants(context.Background(), "prod", "v2", fd, deliveryObjects(t, pdb))
		if err != nil || !plan.Regrant || len(plan.Missing) == 0 {
			t.Fatalf("plan = %+v, err = %v", plan, err)
		}
		var sawEscalate bool
		for _, c := range *asked {
			if c.Verb == "escalate" && c.Resource == "roles" && c.Namespace == "acme-prod" && c.Name == deliveryNS+"-pre" {
				sawEscalate = true
			}
		}
		if !sawEscalate {
			t.Errorf("the review never asked for escalate on the pre identity's Role: %v", *asked)
		}
	})
	t.Run("short, and the caller cannot grant: refuse with exit 9", func(t *testing.T) {
		stubGrants(t, granted, func(c fluxDeliveryAccessCheck) bool { return c.Verb == "escalate" })
		_, err := planFluxDeliveryGrants(context.Background(), "prod", "v2", fd, deliveryObjects(t, pdb))
		var refusal *fluxDeliveryGrantRefusal
		if !errors.As(err, &refusal) {
			t.Fatalf("err = %v, want the grant refusal", err)
		}
		if got := exitcode.Of(err); got != exitNeedsHuman {
			t.Errorf("exit code = %d, want %d", got, exitNeedsHuman)
		}
		for _, want := range []string{
			"release v2 for env prod needs permissions the delivery identities were not granted:",
			fluxdelivery.PreServiceAccount, "policy/poddisruptionbudgets", "(namespace acme-prod)",
			"this credential cannot grant them (it may not escalate",
			"Ask a cluster admin to run:   forge env deploy prod --target flux-delivery",
			"Nothing was recorded, pushed or pointed.",
		} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("refusal lacks %q:\n%s", want, err)
			}
		}
	})
	t.Run("no apply identities: nothing to fall short of", func(t *testing.T) {
		stubGrants(t, fluxDeliveryGrantsRecord{}, func(fluxDeliveryAccessCheck) bool { return true })
		open := fd
		open.ApplyIdentities = false
		plan, err := planFluxDeliveryGrants(context.Background(), "prod", "v2", open, deliveryObjects(t, pdb))
		if err != nil || len(plan.Missing) != 0 {
			t.Fatalf("plan = %+v, err = %v", plan, err)
		}
	})
}

// TestFluxDeliveryGrantRefusalFromTheReconciler: the same message, reported by
// Flux after the pointer moved, is exit 1 and says nothing needs re-deploying.
func TestFluxDeliveryGrantRefusalFromTheReconciler(t *testing.T) {
	t.Parallel()
	r := newFluxDeliveryGrantRefusal("prod", "v2", []fluxDeliveryPermission{{
		Identity: fluxdelivery.PreServiceAccount, Scope: fluxDeliveryScopeCluster,
		Group: "apiextensions.k8s.io", Resource: "customresourcedefinitions", Verb: "patch",
	}})
	r.Reporter, r.Detail = "acme.env.prod-ctx-default-pre", "CustomResourceDefinition/gadgets.acme.dev dry-run failed: forbidden"
	if got := exitcode.Of(r); got != exitWrong {
		t.Errorf("exit code = %d, want %d", got, exitWrong)
	}
	for _, want := range []string{
		"env prod's in-cluster Flux cannot apply release v2",
		"forge-delivery-pre  apiextensions.k8s.io/customresourcedefinitions  (cluster)",
		"Kustomization acme.env.prod-ctx-default-pre reports:",
		"Ask a cluster admin to run:   forge env deploy prod --target flux-delivery",
		"nothing needs re-deploying",
	} {
		if !strings.Contains(r.Error(), want) {
			t.Errorf("message lacks %q:\n%s", want, r.Error())
		}
	}
	if strings.Contains(r.Error(), "Nothing was recorded") {
		t.Errorf("the reconciler's report claims nothing was recorded:\n%s", r.Error())
	}
}
