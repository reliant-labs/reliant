package cli

// WHICH BACKEND A PRESENCE ROW GOES TO, and whether one is reported at all
// (doc §7.4, owner decision O-8).
//
// Two questions, and they are deliberately answered in one place because
// getting either of them wrong is silent. A session written to the wrong
// store does not fail — it lands somewhere nothing reads, and the env simply
// looks idle forever. A session reported for an env that is not LOCAL does
// not fail either: the control plane's own trigger drops it, so the client
// would spend a network round trip per heartbeat producing nothing.
//
// SELECTION IS NOT A SECOND RULE. It calls selectLedger, the SAME function
// ledgerFor calls, and takes the hosted branch exactly when that returned a
// hosted ledger. recordStoreFor's header asks for precisely this ("a records
// store chosen by a different rule than the ledger it records into would be
// able to put an env's bundles and its promotions in two different places"),
// and the cheapest way to honour it is to have no predicate of our own to
// drift: there is nothing here that could come to disagree with ledgerFor,
// because there is nothing here that decides.
//
// It uses selectLedger rather than ledgerFor on purpose. ledgerFor adds the
// unimported-checkout REFUSAL, which is right for a promotion — a deploy
// against a ledger missing history is a deploy that could stomp it — and
// wrong for presence. Refusing to start a dev stack because this checkout
// has not imported its promotion log would make a hosted dependency of the
// one workflow §7.4 says must not have one. The reporting is best-effort
// anyway, so the strongest thing a refusal could buy is a log line.

import (
	"context"
	"fmt"
)

// sessionTarget is where a stack's presence goes, and whether it goes
// anywhere.
//
// A struct rather than (sessionReporter, bool, error) because the two facts
// are read together and the boolean is the one a caller is most likely to
// drop: `reporter, _, err :=` compiles, reports sessions for a prod env, and
// nothing ever says so.
type sessionTarget struct {
	// Reporter is the backend. Nil exactly when Report is false.
	Reporter sessionReporter
	// Report is false for an env that has no presence to report — see
	// envReportsSessions.
	Report bool
	// Hosted says the reports go to a control plane. Carried for the log
	// line: "could not reach the control plane" and "could not write the
	// machine ledger" send a reader to different places.
	Hosted bool
	// Skip is why nothing is reported, for the one-line note. Empty when
	// Report is true.
	Skip string
}

// sessionTargetFor resolves the target for one env of one checkout.
//
// It renders the env's KCL, because both answers come from the declaration:
// which ledger (forge.ControlPlane) and which kind (hostedEnvKindOf). A
// render failure is an ERROR here rather than a fallback — but every caller
// treats it as "do not report", so a project whose KCL does not render still
// starts its stack.
func sessionTargetFor(ctx context.Context, projectDir, env string) (sessionTarget, error) {
	entities, err := RenderKCL(ctx, projectDir, env)
	if err != nil {
		return sessionTarget{}, fmt.Errorf("render deploy/kcl/%s to resolve where sessions are reported: %w", env, err)
	}
	return sessionTargetForEntities(env, entities, projectDir)
}

// sessionTargetForEntities is the render-free half, split out for the same
// reason ledgerForEntities is: the rule is then testable from a literal
// entity set, with no project on disk and no control plane.
func sessionTargetForEntities(env string, entities *KCLEntities, projectDir string) (sessionTarget, error) {
	if why, ok := envReportsSessions(entities); !ok {
		return sessionTarget{Skip: why}, nil
	}
	ledger, err := ledgerForEntities(env, entities, projectDir)
	if err != nil {
		return sessionTarget{}, err
	}
	if !ledger.Hosted {
		store, err := recordStoreFor(projectDir)
		if err != nil {
			return sessionTarget{}, err
		}
		return sessionTarget{Reporter: store, Report: true}, nil
	}
	// The hosted branch takes the client the SELECTED ledger is already
	// holding, rather than resolving an endpoint and a credential a second
	// time. Two resolutions could disagree — a different credential
	// precedence, a different endpoint for the same declaration — and the
	// disagreement would show up as presence rows landing on one control
	// plane while promotions went to another.
	hosted, ok := ledger.Bindings.(*hostedStore)
	if !ok {
		return sessionTarget{}, fmt.Errorf("env %q selected a hosted ledger whose store is %T, not *hostedStore", env, ledger.Bindings)
	}
	return sessionTarget{
		Reporter: hostedRecordStoreFor(hosted.client, hosted.project),
		Report:   true,
		Hosted:   true,
	}, nil
}

// envReportsSessions decides whether an env has presence to report, and says
// why when it does not.
//
// A SESSION IS A LOCAL FACT. It describes a `forge env up` stack running in
// one checkout on one machine, which is only a meaningful thing to say about
// an env whose workloads run on developer machines. For a PERSISTENT env the
// platform runs the workloads and the authoritative answer is the control
// plane's own apply records; for a SELF_MANAGED one it is the operator's
// cluster. In both cases a presence row would be a claim about a laptop that
// nobody should read as the state of the environment — which is exactly why
// the server's schema confines sessions to LOCAL with a trigger (§6.3).
//
// So the client does not report what the server would drop. That is not
// defence in depth for its own sake: reporting it anyway would spend a
// network round trip per heartbeat, every 60 s, for a row that is discarded,
// and the only visible symptom would be latency.
//
// IT READS THE FACTS, NOT THE KIND, and that distinction matters because
// hostedEnvKindOf answers "" for an env that declares no control plane. Most
// envs that report sessions are exactly that — a scaffolded dev env with a
// machine ledger and no control plane at all — so branching on the kind
// enum would have excluded the common case while looking correct.
//
// The facts are the two hostedEnvKindOf itself derives from (HasHosted,
// runsOnOwnCluster), read directly. So this agrees with hostedEnvKindOf on
// every env that HAS a kind, and extends the same rule to the envs that do
// not, rather than inventing a second notion of local.
func envReportsSessions(entities *KCLEntities) (why string, ok bool) {
	if entities == nil {
		return "the environment's declaration could not be read", false
	}
	switch {
	case entities.HasHosted():
		return "the platform runs this environment's workloads, so a local stack is not its state", false
	case runsOnOwnCluster(entities):
		return "this environment runs on a cluster, so a local stack is not its state", false
	}
	return "", true
}
