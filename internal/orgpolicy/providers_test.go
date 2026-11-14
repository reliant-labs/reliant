// Copyright (c) 2025 Reliant Labs

package orgpolicy

import (
	"context"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	controlplanev1 "github.com/reliant-labs/reliant/gen/controlplane/controlplane/v1"
)

const (
	srcOrgKeys  = controlplanev1.ProviderSource_PROVIDER_SOURCE_ORG_ACCOUNTS
	srcSeats    = controlplanev1.ProviderSource_PROVIDER_SOURCE_ORG_SEATS
	srcReliant  = controlplanev1.ProviderSource_PROVIDER_SOURCE_RELIANT
	srcPersonal = controlplanev1.ProviderSource_PROVIDER_SOURCE_PERSONAL
	srcLocal    = controlplanev1.ProviderSource_PROVIDER_SOURCE_LOCAL
)

func TestProviderAdmission(t *testing.T) {
	providers := func(mode controlplanev1.OrgPolicyMode, drivers []string, sources ...controlplanev1.ProviderSource) *controlplanev1.ProvidersPolicy {
		return &controlplanev1.ProvidersPolicy{Mode: mode, Allowed: drivers, AllowedSources: sources}
	}
	automations := func(mode controlplanev1.OrgPolicyMode, sources ...controlplanev1.ProviderSource) *controlplanev1.AutomationsPolicy {
		return &controlplanev1.AutomationsPolicy{Mode: mode, AllowedSources: sources}
	}
	for name, tc := range map[string]struct {
		doc        *controlplanev1.OrgPolicy
		driver     string
		source     controlplanev1.ProviderSource
		unattended bool
		governed   bool
		admitted   bool
		mode       controlplanev1.OrgPolicyMode
	}{
		"no section governs nothing": {doc: &controlplanev1.OrgPolicy{}, driver: "claude", source: srcPersonal},
		"automations alone does not govern an attended run": {
			doc: &controlplanev1.OrgPolicy{Automations: automations(modeEnforce, srcOrgKeys)}, driver: "claude", source: srcPersonal},
		"a listed driver is admitted": {
			doc: &controlplanev1.OrgPolicy{Providers: providers(modeEnforce, []string{"anthropic", "reliant"})}, driver: "anthropic", source: srcPersonal,
			governed: true, admitted: true},
		"an unlisted driver is refused, by its section's mode": {
			doc: &controlplanev1.OrgPolicy{Providers: providers(modeEnforce, []string{"anthropic"})}, driver: "claude", source: srcSeats,
			governed: true, mode: modeEnforce},
		"empty allowed admits every driver; sources still narrow": {
			doc: &controlplanev1.OrgPolicy{Providers: providers(modeAudit, nil, srcOrgKeys, srcSeats, srcReliant)}, driver: "claude", source: srcPersonal,
			governed: true, mode: modeAudit},
		"a seat is not a personal account": {
			doc: &controlplanev1.OrgPolicy{Providers: providers(modeEnforce, nil, srcOrgKeys, srcSeats, srcReliant)}, driver: "claude", source: srcSeats,
			governed: true, admitted: true},
		"local is its own source": {
			doc: &controlplanev1.OrgPolicy{Providers: providers(modeEnforce, nil, srcPersonal)}, driver: "local", source: srcLocal,
			governed: true, mode: modeEnforce},
		"automations narrow an unattended run": {
			doc:    &controlplanev1.OrgPolicy{Providers: providers(modeEnforce, nil), Automations: automations(modeEnforce, srcOrgKeys, srcReliant)},
			driver: "claude", source: srcPersonal, unattended: true, governed: true, mode: modeEnforce},
		"automations admit what they list": {
			doc:    &controlplanev1.OrgPolicy{Automations: automations(modeEnforce, srcOrgKeys, srcReliant)},
			driver: "reliant", source: srcReliant, unattended: true, governed: true, admitted: true},
		"an audited automations rule never relaxes an enforced providers one": {
			doc:    &controlplanev1.OrgPolicy{Providers: providers(modeEnforce, []string{"anthropic"}), Automations: automations(modeAudit, srcOrgKeys)},
			driver: "claude", source: srcPersonal, unattended: true, governed: true, mode: modeEnforce},
		"an enforced automations rule wins over an audited providers one": {
			doc:    &controlplanev1.OrgPolicy{Providers: providers(modeAudit, []string{"anthropic"}), Automations: automations(modeEnforce, srcOrgKeys)},
			driver: "claude", source: srcPersonal, unattended: true, governed: true, mode: modeEnforce},
	} {
		t.Run(name, func(t *testing.T) {
			adm := ProviderAdmission(tc.doc, tc.driver, tc.source, tc.unattended)
			if adm.Governed != tc.governed || adm.Admitted != tc.admitted || (!adm.Admitted && adm.Governed && adm.Mode != tc.mode) {
				t.Fatalf("admission = %+v, want governed=%v admitted=%v mode=%v", adm, tc.governed, tc.admitted, tc.mode)
			}
		})
	}
}

func TestModelAdmission(t *testing.T) {
	doc := &controlplanev1.OrgPolicy{Models: &controlplanev1.ModelsPolicy{Mode: modeEnforce, Allowed: []string{"claude-opus-4-6"}}}
	if adm := ModelAdmission(doc, "claude-opus-4-6"); !adm.Governed || !adm.Admitted {
		t.Fatalf("listed: %+v", adm)
	}
	if adm := ModelAdmission(doc, "claude-sonnet-4-6"); !adm.Governed || adm.Admitted || adm.Mode != modeEnforce {
		t.Fatalf("unlisted: %+v", adm)
	}
	if adm := ModelAdmission(doc, ""); adm.Admitted {
		t.Fatalf("a model the gateway does not serve is not admitted by a list: %+v", adm)
	}
	allowAll := &controlplanev1.OrgPolicy{Models: &controlplanev1.ModelsPolicy{Mode: modeEnforce, AllowAll: true}}
	if adm := ModelAdmission(allowAll, "anything"); !adm.Admitted {
		t.Fatalf("allow_all: %+v", adm)
	}
	if adm := ModelAdmission(&controlplanev1.OrgPolicy{}, "anything"); adm.Governed {
		t.Fatalf("no section: %+v", adm)
	}
}

// Audit mode counts without refusing: a use the providers rule does not
// admit is denied and counted (reliant.org_policy.denials, mode audit) while
// the point audits, and refused and counted (mode enforce) once it enforces.
// A quiet look is never counted. The counts are deltas over one reader the
// process installs once (denialsReader), so the test repeats in one process.
func TestJudgeProvider_AuditCountsWithoutRefusing(t *testing.T) {
	reader := denialsReader()
	count := func(mode string) int64 {
		var rm metricdata.ResourceMetrics
		if err := reader.Collect(context.Background(), &rm); err != nil {
			t.Fatal(err)
		}
		var n int64
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				sum, ok := m.Data.(metricdata.Sum[int64])
				if m.Name != "reliant.org_policy.denials" || !ok {
					continue
				}
				for _, dp := range sum.DataPoints {
					if attrIs(dp.Attributes, "policy", string(PointProviders)) &&
						attrIs(dp.Attributes, "reason", ReasonProviderNotAllowed) && attrIs(dp.Attributes, "mode", mode) {
						n += dp.Value
					}
				}
			}
		}
		return n
	}
	ctx := WithResolver(context.Background(), New(policyTable{docs: map[string]*controlplanev1.OrgPolicy{
		"org-a": {Providers: &controlplanev1.ProvidersPolicy{Mode: modeEnforce, Allowed: []string{"anthropic"}}},
	}}))

	withSwitch(t, PointProviders, EnforcementAudit)
	audits, enforces := count("audit"), count("enforce")
	d, err := JudgeProvider(ctx, "org-a", "claude", srcPersonal, false, false)
	if err != nil || !d.Denied || d.Refused || d.Version != 7 || d.Subject != "claude/personal" {
		t.Fatalf("audit: %+v %v; want denied, not refused, version 7", d, err)
	}
	if got := count("audit") - audits; got != 1 {
		t.Fatalf("audit count = %d, want 1", got)
	}
	if _, err := JudgeProvider(ctx, "org-a", "claude", srcPersonal, false, true); err != nil {
		t.Fatal(err)
	}
	if got := count("audit") - audits; got != 1 {
		t.Fatalf("a quiet look was counted: %d", got)
	}
	if d, _ := JudgeProvider(ctx, "org-a", "anthropic", srcPersonal, false, false); d.Denied {
		t.Fatalf("an allowed driver is not denied: %+v", d)
	}

	SetProcessEnforcement(PointProviders, EnforcementEnforce)
	if d, err := JudgeProvider(ctx, "org-a", "claude", srcPersonal, false, false); err != nil || !d.Refused || d.Reason != ReasonProviderNotAllowed {
		t.Fatalf("enforce: %+v %v; want refused", d, err)
	}
	if got := count("enforce") - enforces; got != 1 {
		t.Fatalf("enforce count = %d, want 1", got)
	}
}

// denialsReader is the reader of the process's one meter provider, installed
// on first use: otel's global delegates to the first provider set, and the
// denials counter is built once (denialsOnce), so a second provider would
// never see it.
var denialsReader = sync.OnceValue(func() *sdkmetric.ManualReader {
	reader := sdkmetric.NewManualReader()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	return reader
})

func attrIs(set attribute.Set, key, want string) bool {
	v, ok := set.Value(attribute.Key(key))
	return ok && v.AsString() == want
}

func TestJudgeModel_GovernsByTheGatewaysSpelling(t *testing.T) {
	withSwitch(t, PointProviders, EnforcementEnforce)
	ctx := WithResolver(context.Background(), New(policyTable{docs: map[string]*controlplanev1.OrgPolicy{
		"org-a": {Models: &controlplanev1.ModelsPolicy{Mode: modeEnforce, Allowed: []string{"claude-opus-4-6"}}},
	}}))
	if d, err := JudgeModel(ctx, "org-a", "claude-sonnet-4-6", true); err != nil || !d.Refused || d.Reason != ReasonModelNotAllowed {
		t.Fatalf("unlisted: %+v %v", d, err)
	}
	if d, err := JudgeModel(ctx, "org-a", "claude-opus-4-6", true); err != nil || d.Denied {
		t.Fatalf("listed: %+v %v", d, err)
	}
}
