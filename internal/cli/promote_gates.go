package cli

// `promote --gate`: attach pre-promote evidence to the promotion entry
// (control-plane docs/design/hosted-deploy-primitives.md §3.3).
//
// OWNED BY F4. F2 declares the flag and the hook in promote.go, which this
// file fills; promote.go is not edited again.
//
// WHY PRE-PROMOTE EVIDENCE IS A SEPARATE THING FROM `gate record`. These
// gates are frozen INTO the promotion entry, and the promotion row is
// append-only, so they answer a question nothing else can: what had already
// passed at the moment somebody moved the environment. That is the first
// question asked about a bad release, and it cannot be reconstructed from
// evidence recorded afterwards — a gate appended later proves only that the
// check eventually ran, not that anyone knew its result before deciding.
//
// EVIDENCE, NOT ENFORCEMENT. A `--gate name=test,status=failed` promote
// SUCCEEDS, and records that the promoter knew the tests were failing. forge
// refuses nothing on a gate's status (owner deferral, §3.3), because a client
// that declined to promote on its own evidence would be enforcement a caller
// can bypass by omitting the flag — a rule that only binds the honest.
// Gates-as-policy is explicitly deferred (§7); the server is where it would
// have to live.
//
// What IS refused, before the plan and therefore before any write, is a gate
// forge cannot record: a status outside the closed set, a missing name, an
// unreadable file. resolvePromoteGates runs ahead of applyPromotePlan for
// exactly that reason — a typo must not move the pointer and then fail on the
// evidence, leaving an environment promoted with the gates missing.

import (
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/pkg/release"
)

// resolvePromoteGates turns the repeatable --gate values into the gates
// frozen onto the promotion.
//
// Two forms, because the two producers are different:
//
//   - A FILE PATH, for a check that emitted a document: forge's own
//     `--gate-json` output, or any forge `--json` document (gateFromDocument).
//     This is the normal case in CI and carries counts and timing no flag
//     would.
//   - The INLINE `name=…,status=…` form, for a check that is not a forge verb
//     and emits nothing forge can read — an e2e suite, a manual sign-off.
//     Without it, every non-forge check would need a wrapper script to
//     become recordable, and the evidence trail would stop at forge's own
//     verbs.
//
// The two are told apart by shape, not by a flag: a value containing `=` is
// inline, anything else is a path. A path cannot contain `=` in any form this
// would meet (and a file named `a=b` is refused with a message naming both
// forms rather than silently read as a malformed inline spec).
func resolvePromoteGates(specs []string) ([]release.Gate, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	gates := make([]release.Gate, 0, len(specs))
	seen := make(map[string]int, len(specs))
	for _, spec := range specs {
		gate, err := parsePromoteGate(spec)
		if err != nil {
			return nil, err
		}
		// A REPEATED NAME IS REFUSED. Two gates called "test" in one
		// promotion is a pipeline bug — a loop that appended twice, or
		// a copy-pasted flag — and the entry has no way to express
		// which one a reader should believe. Promote-time gates are
		// frozen, so unlike `gate record` (append-only, latest wins)
		// there is no later row to supersede the earlier one.
		if first, dup := seen[gate.Name]; dup {
			return nil, fmt.Errorf(
				"--gate %q was given twice (first as %q): one promotion records one result per check.\n"+
					"  fix: drop the duplicate, or give them distinct names (e.g. test-unit, test-e2e)",
				gate.Name, specs[first])
		}
		seen[gate.Name] = len(gates)
		gates = append(gates, gate)
	}
	return gates, nil
}

// parsePromoteGate reads one --gate value.
func parsePromoteGate(spec string) (release.Gate, error) {
	trimmed := strings.TrimSpace(spec)
	if trimmed == "" {
		return release.Gate{}, fmt.Errorf("--gate was given an empty value: expected a gate JSON file, or name=…,status=…")
	}
	if !strings.Contains(trimmed, "=") {
		return gateFromDocumentFile(trimmed, "")
	}
	return parseInlineGate(trimmed)
}

// parseInlineGate reads the `name=…,status=…[,url=…,summary=…,…]` form.
//
// VALIDATED CLIENT-SIDE WITH THE SAME CLOSED SET THE SERVER USES
// (release.ParseGateStatus), so `status=pass` fails here, naming the four
// legal values, rather than arriving at the control plane as an
// InvalidArgument a caller has to decode. The client and the server share the
// set because they share pkg/release — there is no second list to drift.
func parseInlineGate(spec string) (release.Gate, error) {
	var gate release.Gate
	var sawStatus bool
	for _, field := range splitInlineGateFields(spec) {
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			return release.Gate{}, fmt.Errorf(
				"--gate %q: %q is not key=value.\n  fix: %s", spec, field, inlineGateUsage)
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		switch key {
		case "name":
			gate.Name = value
		case "status":
			status, err := release.ParseGateStatus(value)
			if err != nil {
				return release.Gate{}, fmt.Errorf("--gate %q: %w", spec, err)
			}
			gate.Status = status
			sawStatus = true
		case "url":
			gate.URL = value
		case "summary":
			gate.Summary = value
		default:
			// An unknown key is REFUSED, not ignored. A silently
			// dropped `state=passed` would record a gate with no
			// status and no complaint, and the misspelling is
			// invisible in the result.
			return release.Gate{}, fmt.Errorf(
				"--gate %q: unknown field %q.\n  fix: %s", spec, key, inlineGateUsage)
		}
	}
	switch {
	case gate.Name == "":
		return release.Gate{}, fmt.Errorf("--gate %q: name is required.\n  fix: %s", spec, inlineGateUsage)
	case !sawStatus:
		return release.Gate{}, fmt.Errorf(
			"--gate %q: status is required — a check that reported no verdict has not reported.\n  fix: %s",
			spec, inlineGateUsage)
	}
	if err := gate.Validate(); err != nil {
		return release.Gate{}, fmt.Errorf("--gate %q: %w", spec, err)
	}
	return gate, nil
}

const inlineGateUsage = "name=<check>,status=passed|failed|skipped|error[,url=<report>][,summary=<one line>] " +
	"— or pass a gate JSON file instead (`forge lint --gate-json lint.json`)"

// splitInlineGateFields splits on commas, EXCEPT inside a summary value.
//
// A summary is prose ("412 passed, 0 failed"), and prose contains commas. A
// plain strings.Split would cut it into fragments and then reject "0 failed"
// as a field that is not key=value — rejecting the one field most likely to
// contain the separator. So `summary=` runs to the end of the spec, which
// also means it must be written last; the error above says so by example.
func splitInlineGateFields(spec string) []string {
	var fields []string
	rest := spec
	for rest != "" {
		if lower := strings.ToLower(rest); strings.HasPrefix(lower, "summary=") {
			fields = append(fields, rest)
			break
		}
		field, remainder, found := strings.Cut(rest, ",")
		if field = strings.TrimSpace(field); field != "" {
			fields = append(fields, field)
		}
		if !found {
			break
		}
		rest = strings.TrimSpace(remainder)
	}
	return fields
}
