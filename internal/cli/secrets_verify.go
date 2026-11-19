package cli

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/pkg/release"
)

// `forge env secrets verify` and `adopt`: steps 2 and 3 of the per-Secret
// migration (design §7.1).
//
//	verify   compares the Secret that is live in the cluster with the value the
//	         store holds, per key, and prints match / no-match. Any no-match
//	         stops the migration.
//	adopt    hands a verified Secret to ESO: strips the stale
//	         last-applied-configuration annotation (it holds every value in
//	         clear) and asserts nothing owns the Secret by ownerReference.
//
// WHAT "THE STORE'S VALUE" IS. The stores are write-only from forge, so the
// store's value is observed where ESO delivers it:
//
//   - default: the shadow Secret <name>-eso-shadow, which a StoreSource with
//     `shadow = True` makes ESO sync alongside the live one. Both are in the
//     same cluster, and the comparison happens in this process.
//   - --against gsm:<project>: Google Secret Manager's copy, through the
//     operator's Application Default Credentials. This is the parity check for
//     the GSM duplicates cloud-secrets.sh seeded (design P9).
//
// NO COMMAND HERE PRINTS A VALUE OR A HASH. Values are compared as SHA-256
// digests in memory (sameValue) and the only output is a verdict per key. A
// digest is a stable, offline-guessable fingerprint of a low-entropy secret,
// so it is as unprintable as the value.

// shadowSuffix names the Secret ESO syncs into while a StoreSource has
// `shadow = True`.
const shadowSuffix = "-eso-shadow"

// Verdicts. "no-match" and the missing forms all fail the run.
const (
	verdictMatch   = "match"
	verdictNoMatch = "no-match"
	// verdictSkipped is a key that cannot be compared (a templated key against
	// GSM). It does not fail the run.
	verdictSkipped = "skipped"
)

type secretsVerifyOptions struct {
	// Against is "" (compare with the shadow Secret) or "gsm:<project>".
	Against string
	// Map overrides the GSM id for a Secret key: key=gsm-id.
	Map         []string
	KubeContext string
}

func newSecretsVerifyCmd() *cobra.Command {
	var opts secretsVerifyOptions
	cmd := &cobra.Command{
		Use:   "verify <environment> [<secret>]",
		Short: "Compare a live Secret with its store copy, per key (match / no-match; never a value or hash)",
		Args:  cobra.RangeArgs(1, 2),
		Long: `Compare, key by key, the Secret that is live in the cluster with the value
the store holds. This is step 2 of migrating a Secret onto ESO: any no-match
stops the migration.

  forge env secrets verify prod control-plane-secrets

compares <ns>/<secret> with <ns>/<secret>-eso-shadow, the copy ESO syncs from
the store while the Secret's StoreSource declares shadow = True.

  forge env secrets verify prod control-plane-secrets --against gsm:reliant-labs-475814 \
      --map github_app_private_key=github-app-private-key-prod

compares the live Secret with Google Secret Manager, using your Application
Default Credentials. A key reads the GSM secret of its store name unless --map
names another id.

The output is a verdict per key (match / no-match, with which side is missing)
and nothing else. No value is printed, and no hash of one: a digest of a
low-entropy secret is as good as the secret to anyone who can guess. The exit
status is non-zero if any key is not a match.

<secret> is a Secret name, or <namespace>/<name>; omit it to verify every
store-backed Secret the env declares.

With --against gsm: and an explicit --map, <secret> need not be declared by any
store-backed ExternalSecret: the --map keys are the Secret's keys and their
values the GSM ids to compare with. This is the pre-migration escrow check:

  forge env secrets verify prod control-plane-prod/control-plane-openbao-unseal \
      --against gsm:reliant-labs-475814 --map static-key=openbao-unseal-prod`,
		RunE: func(cmd *cobra.Command, args []string) error {
			secret := ""
			if len(args) == 2 {
				secret = args[1]
			}
			return runSecretsVerify(cmd.Context(), args[0], secret, opts, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&opts.Against, "against", "", "Compare with another store instead of the shadow Secret: gsm:<project>")
	cmd.Flags().StringSliceVar(&opts.Map, "map", nil, "With --against gsm: key=gsm-secret-id for a key whose GSM id is not its store name (repeatable)")
	cmd.Flags().StringVar(&opts.KubeContext, "kube-context", "", "Kube context to read the live Secrets from (default: the env's declared cluster)")
	return cmd
}

// sameValue reports whether two values are equal by comparing their SHA-256
// digests in constant time. The digests exist only inside this call.
func sameValue(a, b []byte) bool {
	da, db := sha256.Sum256(a), sha256.Sum256(b)
	return hmac.Equal(da[:], db[:])
}

// verifyRow is one key's verdict. There is no field that can hold a value or a
// digest.
type verifyRow struct {
	Secret  string // <namespace>/<name>
	Key     string
	Verdict string
	// Detail says which side is missing; never a value.
	Detail string
}

func (r verifyRow) ok() bool { return r.Verdict == verdictMatch || r.Verdict == verdictSkipped }

// findStoreSecrets selects the store-backed ExternalSecrets named by arg
// (empty = all, "name", or "namespace/name").
func findStoreSecrets(e *KCLEntities, arg string) ([]ExternalSecretEntity, error) {
	var all, picked []ExternalSecretEntity
	for _, es := range e.RequiredSecrets {
		if es.Source != nil {
			all = append(all, es)
		}
	}
	if len(all) == 0 {
		return nil, errors.New("the env declares no store-backed ExternalSecret (forge.StoreSource): there is nothing to verify")
	}
	if arg == "" {
		return all, nil
	}
	ns, name, qualified := strings.Cut(arg, "/")
	if !qualified {
		ns, name = "", arg
	}
	for _, es := range all {
		if es.Name == name && (ns == "" || es.Namespace == ns) {
			picked = append(picked, es)
		}
	}
	if len(picked) == 0 {
		names := make([]string, len(all))
		for i, es := range all {
			names[i] = es.Namespace + "/" + es.Name
		}
		return nil, fmt.Errorf("no store-backed ExternalSecret %q in the env (declared: %s)", arg, orNone(names))
	}
	return picked, nil
}

func runSecretsVerify(ctx context.Context, envName, secretArg string, opts secretsVerifyOptions, out io.Writer) error {
	e, err := renderEntitiesForSecrets(ctx, envName)
	if err != nil {
		return fmt.Errorf("render KCL: %w", err)
	}
	targets, err := findStoreSecrets(e, secretArg)
	if err != nil {
		// An explicit --map against GSM names the Secret's keys and their GSM
		// ids itself, so it needs no declared ExternalSecret: this is the
		// escrow check made before a Secret is migrated onto a store.
		synth, ok, serr := explicitGSMTarget(e, secretArg, opts)
		if serr != nil {
			return serr
		}
		if !ok {
			return err
		}
		targets = []ExternalSecretEntity{synth}
	}
	kube, err := newKubeAccess(kubeContextFor(e, storeOptions{KubeContext: opts.KubeContext}))
	if err != nil {
		return err
	}

	var rows []verifyRow
	switch {
	case opts.Against == "":
		rows, err = verifyAgainstShadow(ctx, kube, targets)
	case strings.HasPrefix(opts.Against, "gsm:") && len(opts.Against) > len("gsm:"):
		var overrides map[string]string
		overrides, err = parseGSMMap(opts.Map)
		if err != nil {
			return err
		}
		var api gsmAPI
		api, err = newGSMAPI(ctx)
		if err != nil {
			return err
		}
		rows, err = verifyAgainstGSM(ctx, kube, api, strings.TrimPrefix(opts.Against, "gsm:"), targets, overrides)
	default:
		return fmt.Errorf("--against %q is not gsm:<project>", opts.Against)
	}
	if err != nil {
		return err
	}
	return printVerify(out, envName, rows, opts.Against)
}

// explicitGSMTarget builds the Secret to verify from --against gsm: and --map
// alone, for a Secret no store-backed ExternalSecret declares. ok is false
// when the flags do not ask for that, and the caller reports the original
// "no such ExternalSecret" error.
func explicitGSMTarget(e *KCLEntities, secretArg string, opts secretsVerifyOptions) (es ExternalSecretEntity, ok bool, err error) {
	if !strings.HasPrefix(opts.Against, "gsm:") || len(opts.Against) == len("gsm:") || len(opts.Map) == 0 || secretArg == "" {
		return ExternalSecretEntity{}, false, nil
	}
	overrides, err := parseGSMMap(opts.Map)
	if err != nil {
		return ExternalSecretEntity{}, false, err
	}
	ns, name, qualified := strings.Cut(secretArg, "/")
	if !qualified {
		ns, name = "", secretArg
		if e.ClusterTarget != nil {
			ns = e.ClusterTarget.Namespace
		}
	}
	if ns == "" || name == "" || strings.Contains(name, "/") {
		return ExternalSecretEntity{}, false, fmt.Errorf("%q is not a Secret no ExternalSecret declares: name it <namespace>/<name>", secretArg)
	}
	keys := make([]string, 0, len(overrides))
	for k := range overrides {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return ExternalSecretEntity{
		Name: name, Namespace: ns, Keys: keys,
		Source: &StoreSourceEntity{RenderedStoreSource: release.RenderedStoreSource{Data: overrides}},
	}, true, nil
}

// verifyAgainstShadow compares each store-backed Secret with its shadow.
func verifyAgainstShadow(ctx context.Context, kube kubeAccess, targets []ExternalSecretEntity) ([]verifyRow, error) {
	var rows []verifyRow
	for _, es := range targets {
		id := es.Namespace + "/" + es.Name
		live, err := kube.GetSecret(ctx, es.Namespace, es.Name)
		if err != nil && !errors.Is(err, errKubeSecretNotFound) {
			return nil, err
		}
		liveMissing := errors.Is(err, errKubeSecretNotFound)
		shadow, err := kube.GetSecret(ctx, es.Namespace, es.Name+shadowSuffix)
		if errors.Is(err, errKubeSecretNotFound) {
			return nil, fmt.Errorf("%s has no shadow Secret %s%s\n"+
				"fix: declare the ExternalSecret's source with shadow = True and deploy, so ESO syncs the store's value beside the live Secret",
				id, es.Name, shadowSuffix)
		}
		if err != nil {
			return nil, err
		}
		for _, key := range sortedCopy(es.Keys) {
			rows = append(rows, compareKey(id, key,
				live.Data, liveMissing, "live Secret",
				shadow.Data, false, "shadow Secret (the store's copy)"))
		}
	}
	return rows, nil
}

// verifyAgainstGSM compares each store-backed Secret's data keys with GSM.
func verifyAgainstGSM(ctx context.Context, kube kubeAccess, api gsmAPI, project string, targets []ExternalSecretEntity, overrides map[string]string) ([]verifyRow, error) {
	var rows []verifyRow
	for _, es := range targets {
		id := es.Namespace + "/" + es.Name
		live, err := kube.GetSecret(ctx, es.Namespace, es.Name)
		if err != nil && !errors.Is(err, errKubeSecretNotFound) {
			return nil, err
		}
		liveMissing := errors.Is(err, errKubeSecretNotFound)
		for _, key := range sortedCopy(es.Keys) {
			storeName, isData := es.Source.Data[key]
			if !isData {
				rows = append(rows, verifyRow{Secret: id, Key: key, Verdict: verdictSkipped,
					Detail: "templated from other keys; verify the keys it reads"})
				continue
			}
			gsmID := storeName
			if o, ok := overrides[key]; ok {
				gsmID = o
			}
			_, usable, exists, err := api.Latest(ctx, project, gsmID)
			if err != nil {
				return nil, err
			}
			var remote map[string][]byte
			remoteMissing := !exists || !usable
			if !remoteMissing {
				val, err := api.Access(ctx, project, gsmID)
				if err != nil {
					return nil, err
				}
				remote = map[string][]byte{key: val}
			}
			rows = append(rows, compareKey(id, key,
				live.Data, liveMissing, "live Secret",
				remote, remoteMissing, "GSM secret "+gsmID))
		}
	}
	return rows, nil
}

// compareKey produces one verdict. aMissing/bMissing say the whole Secret is
// absent on that side.
func compareKey(secret, key string, a map[string][]byte, aMissing bool, aName string, b map[string][]byte, bMissing bool, bName string) verifyRow {
	row := verifyRow{Secret: secret, Key: key}
	av, aok := a[key]
	bv, bok := b[key]
	aok = aok && !aMissing && len(av) > 0
	bok = bok && !bMissing && len(bv) > 0
	switch {
	case !aok && !bok:
		row.Verdict, row.Detail = verdictNoMatch, "missing from the "+aName+" and the "+bName
	case !aok:
		row.Verdict, row.Detail = verdictNoMatch, "missing from the "+aName
	case !bok:
		row.Verdict, row.Detail = verdictNoMatch, "missing from the "+bName
	case sameValue(av, bv):
		row.Verdict = verdictMatch
	default:
		row.Verdict = verdictNoMatch
	}
	return row
}

func printVerify(out io.Writer, envName string, rows []verifyRow, against string) error {
	target := "the shadow Secrets"
	if against != "" {
		target = against
	}
	fmt.Fprintf(out, "verify env %s against %s\n\n", envName, target)
	failed := 0
	last := ""
	for _, r := range rows {
		if r.Secret != last {
			fmt.Fprintf(out, "%s\n", r.Secret)
			last = r.Secret
		}
		line := fmt.Sprintf("  %-40s %s", r.Key, r.Verdict)
		if r.Detail != "" {
			line += " (" + r.Detail + ")"
		}
		fmt.Fprintln(out, line)
		if !r.ok() {
			failed++
		}
	}
	if len(rows) == 0 {
		fmt.Fprintln(out, "nothing to compare")
		return nil
	}
	if failed > 0 {
		fmt.Fprintf(out, "\n%d of %d key(s) are not a match: stop the migration here\n", failed, len(rows))
		return fmt.Errorf("%d key(s) are not a match", failed)
	}
	fmt.Fprintf(out, "\nall %d key(s) match\n", len(rows))
	return nil
}

func parseGSMMap(entries []string) (map[string]string, error) {
	out := map[string]string{}
	for _, entry := range entries {
		key, id, ok := strings.Cut(entry, "=")
		key, id = strings.TrimSpace(key), strings.TrimSpace(id)
		if !ok || key == "" || id == "" {
			return nil, fmt.Errorf("--map %q is not key=gsm-secret-id", entry)
		}
		out[key] = id
	}
	return out, nil
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// --- adopt ---------------------------------------------------------------

func newSecretsAdoptCmd() *cobra.Command {
	var (
		dryRun      bool
		kubeContext string
	)
	cmd := &cobra.Command{
		Use:   "adopt <environment> <secret>",
		Short: "Hand a verified, store-backed Secret to ESO (strip the stale last-applied annotation)",
		Args:  cobra.ExactArgs(2),
		Long: `Step 3 of migrating a Secret onto ESO. Run it after 'forge env secrets verify'
reports every key a match and the ExternalSecret's source has shadow = False.

It does two things to the live Secret <namespace>/<secret>:

  - asserts nothing owns it by ownerReference (a controller that does would
    garbage-collect it, or fight ESO for it), and
  - removes the kubectl.kubernetes.io/last-applied-configuration annotation.
    'kubectl apply' keeps a full copy of the applied manifest there, so a
    Secret seeded that way carries every value in clear in its metadata, and a
    later 'kubectl apply' would treat the fields as its own.

It does not touch the Secret's data, and prints no value. ESO takes the Secret
over on its next sync (it adds its managed label and rebuilds the data from the
store). Re-running is a no-op. <secret> is <namespace>/<name>, or a name unique
among the env's store-backed Secrets.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSecretsAdopt(cmd.Context(), args[0], args[1], kubeContext, dryRun, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Report what would change; patch nothing")
	cmd.Flags().StringVar(&kubeContext, "kube-context", "", "Kube context holding the Secret (default: the env's declared cluster)")
	return cmd
}

func runSecretsAdopt(ctx context.Context, envName, secretArg, kubeContext string, dryRun bool, out io.Writer) error {
	e, err := renderEntitiesForSecrets(ctx, envName)
	if err != nil {
		return fmt.Errorf("render KCL: %w", err)
	}
	targets, err := findStoreSecrets(e, secretArg)
	if err != nil {
		return err
	}
	if len(targets) != 1 {
		return fmt.Errorf("%q matches %d store-backed Secrets; name one as <namespace>/<name>", secretArg, len(targets))
	}
	es := targets[0]
	id := es.Namespace + "/" + es.Name
	if es.Source.Shadow {
		return fmt.Errorf("%s still declares shadow = True, so ESO is syncing %s%s, not the Secret itself\n"+
			"fix: verify the shadow, then set shadow = False and deploy, then adopt", id, es.Name, shadowSuffix)
	}
	kube, err := newKubeAccess(kubeContextFor(e, storeOptions{KubeContext: kubeContext}))
	if err != nil {
		return err
	}
	sec, err := kube.GetSecret(ctx, es.Namespace, es.Name)
	if err != nil {
		return err
	}
	if sec.OwnerRefs > 0 {
		return fmt.Errorf("%s has %d ownerReference(s): something else owns it, and ESO adopting it would fight that owner or lose it to garbage collection\n"+
			"fix: remove the ownerReference (or the owner) first", id, sec.OwnerRefs)
	}
	_, stale := sec.Annotations[lastAppliedAnnotation]
	switch {
	case !stale:
		fmt.Fprintf(out, "%s: no %s annotation; nothing to strip\n", id, lastAppliedAnnotation)
	case dryRun:
		fmt.Fprintf(out, "%s: would strip %s\n", id, lastAppliedAnnotation)
	default:
		if err := kube.RemoveAnnotations(ctx, es.Namespace, es.Name, []string{lastAppliedAnnotation}); err != nil {
			return err
		}
		fmt.Fprintf(out, "%s: stripped %s\n", id, lastAppliedAnnotation)
	}
	fmt.Fprintf(out, "%s: no ownerReferences\n", id)
	return nil
}
