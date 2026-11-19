package release

import (
	"fmt"
	"strings"
)

// CHART-OWNED OBJECTS IN THE PLAN.
//
// A forge.HelmChart platform dep is rendered and applied on its own path and
// rides no bundle (internal/bundle's note on charts), so a plan built from two
// bundle shapes cannot see it: cert-manager's controller ConfigMap changed in a
// deploy and nothing in the plan said so, and a chart prune deleted a
// PriorityClass unlisted. A digest a human approves has to cover everything the
// deploy applies, so the caller OBSERVES what each chart would change and
// BuildPlan folds the observation into the same sections, with the same codes,
// under the same digest.

// ChartOp is how a deploy changes one chart-owned object.
type ChartOp string

const (
	ChartAdded   ChartOp = "added"
	ChartChanged ChartOp = "changed"
	// ChartRemoved is a chart prune: the object left the chart's render and
	// the deploy deletes it after the chart applies.
	ChartRemoved ChartOp = "removed"
)

// ChartChange is one chart-owned object a deploy adds, changes or prunes.
type ChartChange struct {
	// Chart is the forge.HelmChart name that owns the object.
	Chart string
	// Key names the object; Key.Cluster is the kubectl context it lives in.
	Key ObjectKey
	Op  ChartOp
	// Fields are the changed field paths of a ChartChanged object.
	Fields []string
	// Fingerprint is the hash of the desired document (for a removal, of the
	// chart and object). Digested, so an approval names this exact change.
	Fingerprint string
	// LoadBalancer marks a pruned Service holding a load balancer: its
	// external address goes with it.
	LoadBalancer bool
}

// OwnedRemoval is an object a deploy deletes because the env stopped rendering
// it: forge stamped it (forge.dev/owner), the render no longer carries it, and
// it is an inert kind. Key.Cluster is the kubectl context it lives in.
type OwnedRemoval struct {
	Key ObjectKey
	// Fingerprint is the hash of the object's identity and UID, so an approval
	// names this exact object and not a replacement of the same name.
	Fingerprint string
}

// OwnedKept is an owned object the prune found absent from the render and will
// NOT delete, with the reason. Informational: it is never digested.
type OwnedKept struct {
	Key ObjectKey
	Why string
}

// ChartUnplanned is a chart whose changes could not be computed. It is a
// finding, never silence: omitting a chart the plan could not read would read
// as "the chart does not change".
type ChartUnplanned struct {
	Chart  string
	Reason string
}

// ChartPlan is the observed effect of an env's charts.
type ChartPlan struct {
	Changes   []ChartChange
	Unplanned []ChartUnplanned
	// Pruned are the env's own objects the deploy deletes after its rollout
	// (the owned-object prune), and Kept the ones it leaves and why. They ride
	// the chart plan because they, too, are effects of the apply that no bundle
	// diff can see: the bundle says an object left the render, not that the
	// deploy deletes it.
	Pruned []OwnedRemoval
	Kept   []OwnedKept
	// PruneUnplanned says why the prune set could not be read, when it could not.
	PruneUnplanned string
}

// PruneMarker is the prefix an owned-object prune finding's detail carries.
const PruneMarker = "[prune]"

const prunedDetail = "forge stamped this object as the env's own and the render no longer carries it, so this deploy DELETES it after the rollout is healthy"

// ChartMarker is the prefix every chart finding's detail carries, so a reviewer
// can tell chart-owned objects from the release bundle's.
func ChartMarker(chart string) string { return "[chart " + chart + "]" }

// WithCharts returns p with c folded in and the digest recomputed, for a plan
// computed elsewhere (a control plane's) that cannot see charts. BaseDigest
// keeps the digest the plan had before, which is the one its computer can
// recompute and so the one a write must carry.
func (p Plan) WithCharts(c ChartPlan) (Plan, error) {
	if len(c.Changes) == 0 && len(c.Unplanned) == 0 && len(c.Pruned) == 0 && c.PruneUnplanned == "" {
		return p, nil
	}
	out := p
	out.Findings = append([]PlanFinding(nil), p.Findings...)
	if out.BaseDigest == "" {
		out.BaseDigest = p.Digest
	}
	c.fold(&out)
	sortFindings(out.Findings)
	d, err := out.computeDigest()
	if err != nil {
		return Plan{}, err
	}
	out.Digest = d
	return out, nil
}

// fold adds the chart findings to p. It also withdraws ConfigIdentical: that
// flag says "this deploy changes images at most", which a chart change falsifies.
func (c ChartPlan) fold(p *Plan) {
	add := func(code string, class FindingClass, section, subject, detail, fingerprint string) {
		p.Findings = append(p.Findings, PlanFinding{Code: code, Class: class, Section: section, Subject: subject, Detail: detail, Fingerprint: fingerprint})
	}
	for _, u := range c.Unplanned {
		add(FindingUnknown, ClassWarn, SectionObjects, "chart "+u.Chart,
			ChartMarker(u.Chart)+" its changes could not be computed: "+u.Reason, "")
	}
	if c.PruneUnplanned != "" {
		add(FindingUnknown, ClassWarn, SectionObjects, "prune",
			"[prune] the objects this deploy would delete could not be listed: "+c.PruneUnplanned, "")
	}
	for _, r := range c.Pruned {
		p.ConfigIdentical = false
		subject := r.Key.String()
		// The bundle diff already reports an object the release dropped as
		// object_removed. One object, one finding: the prune upgrades that
		// finding (it now says the deploy deletes it, and pins which object) and
		// adds one only for an object the bundle never held.
		merged := false
		for i := range p.Findings {
			if f := &p.Findings[i]; f.Code == FindingObjectRemoved && f.Subject == subject {
				f.Detail, f.Fingerprint, merged = PruneMarker+" "+prunedDetail, r.Fingerprint, true
				break
			}
		}
		if !merged {
			add(FindingObjectRemoved, ClassWarn, SectionObjects, subject, PruneMarker+" "+prunedDetail, r.Fingerprint)
		}
	}
	for _, ch := range c.Changes {
		p.ConfigIdentical = false
		subject := ch.Key.String()
		marker := ChartMarker(ch.Chart)
		switch ch.Op {
		case ChartAdded:
			add(FindingObjectAdded, ClassInfo, SectionObjects, subject, marker, ch.Fingerprint)
		case ChartChanged:
			detail := marker + " "
			if len(ch.Fields) > 0 {
				detail += "fields: " + summarizeChartFields(ch.Fields)
			}
			if RecreatableKind(ch.Key.Kind) {
				detail += "; " + recreateNote
			}
			add(FindingConfigChanged, ClassInfo, SectionConfig, subject, detail, ch.Fingerprint)
		case ChartRemoved:
			if ch.LoadBalancer {
				add(FindingLBIdentityChange, ClassStop, SectionLB, subject,
					fmt.Sprintf("%s pruned: %s %q is deleted after the chart applies and with it its external address", marker, ch.Key.Kind, ch.Key.Name), ch.Fingerprint)
				continue
			}
			add(FindingObjectRemoved, ClassWarn, SectionObjects, subject,
				marker+" pruned: the chart no longer renders it, so this deploy DELETES it after the chart applies", ch.Fingerprint)
		}
	}
}

// chartFieldsShown is how many changed paths a finding names before "+N more".
const chartFieldsShown = 6

func summarizeChartFields(fields []string) string {
	if len(fields) <= chartFieldsShown {
		return strings.Join(fields, ", ")
	}
	return fmt.Sprintf("%s (+%d more)", strings.Join(fields[:chartFieldsShown], ", "), len(fields)-chartFieldsShown)
}
