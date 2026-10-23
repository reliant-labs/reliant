package cli

// `forge cluster connect` / `forge cluster disconnect` — registering a
// Kubernetes cluster the control plane may deploy INTO, as one env's binding.
//
// THE SIBLING COMMANDS IN THIS NAMESPACE ARE LOCAL k3d LIFECYCLE. These two
// are the opposite direction: nothing is created, a cluster the owner already
// operates becomes addressable by name, and an environment points at it with
// `ClusterTarget.connected_cluster`. The verb is `connect` rather than `add`
// because the cluster already exists and keeps existing after `disconnect`.
//
// A BINDING IS ONE ENV × ONE CLUSTER. connect grants that binding's own
// identity exactly what the env's render applies there, in the scope the owner
// chose (`--apply-scope`): see cluster_connect_scope.go and
// cluster_connect_rbac.go. Nothing is shared between bindings.
//
// DECLARATIVE AND IDEMPOTENT BY NAME: ConnectCluster UPDATES an existing
// cluster of the same name rather than colliding, the in-cluster RBAC is a
// server-side apply, and a narrower re-run prunes what it no longer names.
//
// forge does not vendor the control plane's protos, so the wire shapes below
// are the proto3-JSON subset forge reads, declared locally — the same rule the
// hosted deploy target and `forge domain` follow.

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/cluster"
)

const (
	procConnectCluster = "controlplane.v1.ClusterService/ConnectCluster"
	procListClusters   = "controlplane.v1.ClusterService/ListClusters"
	procRemoveCluster  = "controlplane.v1.ClusterService/RemoveCluster"
)

// The namespace forge mints the token identity's ServiceAccount in.
//
// `forge-system` rather than a name carrying the hub's internals: this object
// group lives in a CUSTOMER's cluster, so it is named after the tool that
// created it. The namespace is shared by every binding on the cluster; the
// ServiceAccount and Secret inside it are one per binding.
const connectTokenNamespace = "forge-system"

// wireConnectedCluster is the subset of controlplane.v1.Cluster forge reads.
// A field forge does not use is a field forge cannot break on.
type wireConnectedCluster struct {
	ID      string `json:"id,omitempty"`
	Name    string `json:"name,omitempty"`
	Auth    string `json:"auth,omitempty"`
	Address string `json:"address,omitempty"`
	// CloudCluster is set iff auth is a workload-identity auth.
	CloudCluster string `json:"cloudCluster,omitempty"`
	// Connection is the ClusterConnection value name.
	Connection string `json:"connection,omitempty"`
	// EnvironmentIDs are the live environments targeting this cluster. A
	// non-empty list blocks removal server-side.
	EnvironmentIDs []string `json:"environmentIds,omitempty"`
	// Issuer is the secret-store issuer registered for the cluster, if any.
	Issuer *wireClusterIssuer `json:"issuer,omitempty"`
}

// wireBindingIdentity is the principal the CONTROL PLANE presents to this
// cluster for ONE env's binding — the half a caller cannot derive, and the
// reason the connect response is read at all. There is deliberately no shared
// identity on the wire: a response without a binding identity cannot be bound.
type wireBindingIdentity struct {
	GCPServiceAccount string `json:"gcpServiceAccount,omitempty"`
}

type wireConnectClusterResponse struct {
	Cluster         wireConnectedCluster `json:"cluster"`
	BindingIdentity wireBindingIdentity  `json:"bindingIdentity"`
}

const clusterConnectLong = `Register a cluster you operate, by address, and grant ONE env's binding exactly
what that env's render applies there.

  forge cluster connect prod-us --context gke_acme_us-central1_prod --env prod
  forge cluster connect prod-us --context ... --env prod --apply-scope namespace:acme-prod,acme-jobs
  forge cluster connect hub --context ... --env prod --apply-scope cluster
  forge cluster connect prod-us --context ... --env prod --dry-run
  forge cluster disconnect prod-us --env prod --context ...

A BINDING is one env on one cluster, and it has its OWN identity: a Google
service account the control plane allocates for it (gcp), or a ServiceAccount
token forge mints for it (token). Nothing is shared across bindings, and the
apply runs AS that identity: no impersonation, no bootstrap writes. The only
objects connect writes in your cluster are the grant itself.

APPLY SCOPE — YOU choose where the identity may write; the env's render
chooses what:

  namespace:<ns>[,<ns>…]   a Role and RoleBinding in each named namespace. The
                           DEFAULT, over every namespace the render targets.
                           Refused if the render carries cluster-scoped kinds.
  cluster                  a ClusterRole and ClusterRoleBinding. Equivalent to
                           cluster-admin (it holds bind and escalate on RBAC),
                           and forge says so every time.

Either way the rules are derived from the render: one rule per resource it
applies, nothing else. A release that adds a kind is Forbidden by your
cluster's own access check until you re-run connect — review it first with
--dry-run, which prints the exact RBAC diff against what is on the cluster.

AUTH — two values, the two ends of a real trade-off rather than two clouds:

  gcp     GKE. The control plane presents the binding's own Google service
          account; no secret crosses the boundary. forge prints the one-time
          IAM grant you run; it cannot run it for you.
  token   A scoped ServiceAccount token forge mints in your cluster. Works on
          ANY Kubernetes cluster and needs no cloud identity. Strictly worse (a
          bearer token at rest), bounded by the RBAC forge applies, and revoked
          by disconnect.

--auth auto (the default) picks gcp for a GKE context and token for anything
else. Re-running connect UPDATES the cluster of that name and prunes grants the
new plan no longer names. --env names which control plane to talk to AND whose
binding this is; the render is read from the project in the current directory.

DRY RUN — contacts no control plane and changes nothing. It renders the env,
reads the binding's live RBAC through --context (read-only) — or from a capture
with --live-from <file> (kubectl get clusterrole,clusterrolebinding,role,
rolebinding -A -l <selector> -o yaml, selector printed by --dry-run) — and
prints the diff and the permissions this run would newly grant. On gcp the
subject is unknown until the control plane allocates it; pass --binding-gsa to
review the exact subject.

SECRET DELIVERY — opt-in, off unless named. A Secret the control plane supplies
and renews (forge.ControlPlaneSecret) needs write access to exactly that
Secret and no other. Name each one:

  forge cluster connect prod-us --context ... --env prod \
    --delivery-secret control-plane-prod/reliant-otel-auth

forge then applies, per namespace, a Role limited by resourceNames to get,
update, patch and delete on the named Secrets, plus create — which RBAC cannot
limit by name, so a ValidatingAdmissionPolicy admits only the named
namespace/name pairs, only as type Opaque, for the delivery identities only.
--delivery-subject (user:<name> or serviceaccount:<ns>/<name>) binds a second,
dedicated delivery identity alongside the binding's identity.

SECRET-STORE ISSUER — connect also tells the control plane which service-account
issuer the cluster's pods are signed by, so the secret store can verify the
projected token an External Secrets Operator logs in with, against the cluster's
OWN keys (no stored credential, no call back into the cluster). forge reads the
issuer and key set from the API server through --context:

  --issuer auto      (default) discover on GKE, upload elsewhere; if the issuer
                     cannot be read or the control plane refuses it, the cluster
                     still connects for deploys and forge says what is missing
  --issuer discover  the store fetches <issuer>/.well-known/openid-configuration
                     itself; the issuer must be reachable from the internet
  --issuer upload    forge sends /openid/v1/jwks: for a cluster the store cannot
                     reach (private, k3d). PUBLIC keys only; a set holding a
                     private or symmetric key is never sent. A snapshot: re-run
                     connect after a signing-key rotation to refresh it
  --issuer skip      leave the registration as it is
  --issuer withdraw  remove it; the store stops trusting the cluster

The binding's grant gains 'get' on /openid/v1/jwks. The store's id for the
cluster (its auth mount is jwt-<id>) is assigned by the control plane, is global
across organizations, and is printed and kept in
.forge/state/cluster-issuer-<name>.json; use it as the connected cluster name.`

func newClusterConnectCmd() *cobra.Command {
	var (
		kubeContext string
		authFlag    string
		envName     string
		token       string
		dryRun      bool
		applyScope  string
		liveFrom    string
		bindingGSA  string
		delivery    []string
		deliverySub string
		issuerFlag  string
	)
	cmd := &cobra.Command{
		Use:   "connect <name>",
		Short: "Register a Kubernetes cluster the control plane may deploy into, as one env's binding",
		Long:  clusterConnectLong,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			secrets, err := parseDeliverySecrets(delivery)
			if err != nil {
				return err
			}
			subject, err := parseDeliverySubject(deliverySub)
			if err != nil {
				return err
			}
			scope, err := parseApplyScope(applyScope)
			if err != nil {
				return err
			}
			issuerMode, err := parseIssuerMode(issuerFlag)
			if err != nil {
				return err
			}
			return runClusterConnect(cmd.Context(), clusterConnectOptions{
				Name:            args[0],
				KubeContext:     kubeContext,
				Auth:            authFlag,
				Env:             envName,
				Token:           token,
				DryRun:          dryRun,
				Scope:           scope,
				LiveFrom:        liveFrom,
				BindingGSA:      bindingGSA,
				DeliverySecrets: secrets,
				DeliverySubject: subject,
				Issuer:          issuerMode,
				Out:             cmd.OutOrStdout(),
				ErrOut:          cmd.ErrOrStderr(),
			})
		},
	}
	cmd.Flags().StringVar(&kubeContext, "context", "", "kubectl context addressing the cluster (required)")
	cmd.Flags().StringVar(&authFlag, "auth", "auto", "How the control plane authenticates: auto, gcp or token")
	cmd.Flags().StringVar(&envName, "env", "", "Environment whose binding this is, and whose control plane to talk to (required)")
	cmd.Flags().StringVar(&token, "token", "", "Credential to use, ahead of the env var and the credentials file")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print the RBAC diff against the cluster and what would be sent; contact no control plane and change nothing")
	cmd.Flags().StringVar(&applyScope, "apply-scope", "", "Where the binding's identity may write: namespace:<ns>[,<ns>…] (default: the namespaces the env renders) or cluster (cluster-admin equivalent)")
	cmd.Flags().StringVar(&liveFrom, "live-from", "", "With --dry-run: diff against this kubectl capture instead of reading the cluster")
	cmd.Flags().StringVar(&bindingGSA, "binding-gsa", "", "With --dry-run on gcp: the binding's GCP service account, to review the exact subject")
	cmd.Flags().StringArrayVar(&delivery, "delivery-secret", nil,
		"<namespace>/<name> of a Secret the control plane may write and renew (repeatable)")
	cmd.Flags().StringVar(&issuerFlag, "issuer", issuerAuto, "Register the cluster's service-account issuer with the secret store: auto, discover, upload, skip or withdraw")
	cmd.Flags().StringVar(&deliverySub, "delivery-subject", "",
		"Also bind the delivery grant to this dedicated identity: user:<name> or serviceaccount:<namespace>/<name>")
	return cmd
}

func newClusterDisconnectCmd() *cobra.Command {
	var (
		kubeContext string
		envName     string
		token       string
	)
	cmd := &cobra.Command{
		Use:   "disconnect <name>",
		Short: "Deregister a connected cluster and remove every grant forge created in it",
		Long: `Deregister the connected cluster of this exact name.

Two halves, and the order matters: the control plane revokes the cluster's
credential first, then forge deletes the grant it created in the cluster — the
Roles, ClusterRoles and bindings of every binding of this cluster, each
binding's token ServiceAccount and Secret, and the delivery policy. They are
found by label, so nothing else in the cluster is touched. A credential revoked
server-side is already powerless, so a failure to reach the cluster afterwards
leaves litter rather than a live credential.

REFUSED while any live environment targets the cluster — that check is the
control plane's, and it is why this is not a local delete.

  forge cluster disconnect prod-us --env prod --context gke_acme_us-central1_prod

--context is optional and names where to clean up. Without it, forge
deregisters the cluster and prints the commands to delete the grant by hand.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runClusterDisconnect(cmd.Context(), args[0], kubeContext, envName, token, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&kubeContext, "context", "", "kubectl context to delete forge's objects through")
	cmd.Flags().StringVar(&envName, "env", "", "Environment whose control plane to talk to (required)")
	cmd.Flags().StringVar(&token, "token", "", "Credential to use, ahead of the env var and the credentials file")
	return cmd
}

type clusterConnectOptions struct {
	Name        string
	KubeContext string
	Auth        string
	Env         string
	Token       string
	DryRun      bool
	Scope       applyScope
	// LiveFrom is a `--dry-run` capture of the binding's live RBAC.
	LiveFrom string
	// BindingGSA reviews a gcp subject in --dry-run.
	BindingGSA string
	// DeliverySecrets / DeliverySubject: see connectRBAC.
	DeliverySecrets []deliverySecret
	DeliverySubject *deliverySubject
	// Issuer is the --issuer mode; empty is auto.
	Issuer string
	Out    io.Writer
	ErrOut io.Writer
}

// clusterConnectClient resolves the endpoint and credential for an env and
// builds the client. A var so tests point it at an httptest control plane.
//
// The organization it resolves is a preflight, not an input: the credential is
// proven before anything is written to the owner's cluster.
var clusterConnectClient = func(ctx context.Context, envName, token string) (cloudCaller, string, error) {
	envName = strings.TrimSpace(envName)
	if envName == "" {
		return nil, "", fmt.Errorf("--env is required: it names which control plane to talk to, from that " +
			"env's forge.ControlPlane declaration\nfix: re-run with `--env <env>` (the environments under deploy/kcl/)")
	}
	ep, cred, err := resolveCloudTarget(ctx, envName, token)
	if err != nil {
		return nil, "", err
	}
	client := cloud.NewClient(ep, cred)
	org, err := cloud.ResolveOrganization(ctx, client)
	if err != nil {
		return nil, "", err
	}
	return client, org, nil
}

// connectEnvStream renders the env's annotated manifest stream: the SAME
// projection the bundle is built from, so the grant and the bundle cannot
// disagree about what gets applied. A var so tests supply a stream.
var connectEnvStream = func(ctx context.Context, errOut io.Writer, envName string) (string, error) {
	doc, err := projectEnvShapeFn(ctx, errOut, envName)
	if err != nil {
		return "", err
	}
	return doc.manifests, nil
}

// planConnectRBAC renders the env, derives what it needs on this cluster and
// resolves the owner's scope against it. No cluster, no control plane.
func planConnectRBAC(ctx context.Context, opts clusterConnectOptions, target connectTarget, rbac connectRBAC) (connectRBAC, error) {
	errOut := opts.ErrOut
	if errOut == nil {
		errOut = io.Discard
	}
	stream, err := connectEnvStream(ctx, errOut, opts.Env)
	if err != nil {
		return rbac, fmt.Errorf("derive the grant from env %q's render: %w\n"+
			"connect grants exactly what the render applies, so it reads the env's render from the project in the current directory", opts.Env, err)
	}
	needs, err := deriveGrantNeeds(stream, target.Context)
	if err != nil {
		return rbac, fmt.Errorf("derive the grant from env %q's render: %w", opts.Env, err)
	}
	plan, err := planGrant(needs, opts.Scope)
	if err != nil {
		return rbac, fmt.Errorf("env %q on context %q: %w", opts.Env, target.Context, err)
	}
	rbac.Scope, rbac.Plan = opts.Scope, plan
	return rbac, nil
}

func runClusterConnect(ctx context.Context, opts clusterConnectOptions) error {
	name := strings.TrimSpace(opts.Name)
	if strings.TrimSpace(opts.Env) == "" {
		return fmt.Errorf("--env is required: it names the env whose binding this is and which control plane to talk to\n" +
			"fix: re-run with `--env <env>` (the environments under deploy/kcl/)")
	}
	if opts.ErrOut == nil {
		opts.ErrOut = io.Discard
	}
	target, err := readConnectTarget(opts.KubeContext, opts.Auth)
	if err != nil {
		return err
	}
	gke, _ := parseGKEContext(target.Context)
	rbac := connectRBAC{
		ClusterName:     name,
		Env:             opts.Env,
		KubeContext:     target.Context,
		Auth:            target.Auth,
		TokenNamespace:  connectTokenNamespace,
		DeliverySecrets: opts.DeliverySecrets,
		DeliverySubject: opts.DeliverySubject,
	}
	if err := rbac.validateDelivery(); err != nil {
		return err
	}
	if target.AddressNote != "" {
		fmt.Fprintf(opts.Out, "registering %s: %s\n", target.Address, target.AddressNote)
	}
	if rbac, err = planConnectRBAC(ctx, opts, target, rbac); err != nil {
		return err
	}
	issuerMode, err := parseIssuerMode(opts.Issuer)
	if err != nil {
		return err
	}
	// Read through the owner's context before anything is written, so an
	// explicit --issuer that cannot be satisfied stops the run with the cluster
	// untouched.
	issuerPlan, err := planConnectIssuer(ctx, issuerMode, target.Context, target.Auth == authGCP, opts.LiveFrom != "")
	if err != nil {
		return err
	}
	rbac.IssuerJWKS = issuerMode != issuerSkip && issuerMode != issuerWithdraw

	if opts.DryRun {
		return runConnectDryRun(ctx, opts, name, target, rbac, issuerPlan)
	}
	if opts.LiveFrom != "" || opts.BindingGSA != "" {
		return fmt.Errorf("--live-from and --binding-gsa review a grant and apply nothing: they are --dry-run only")
	}

	client, _, err := clusterConnectClient(ctx, opts.Env, opts.Token)
	if err != nil {
		return err
	}
	if rbac.Plan.Cluster {
		fmt.Fprintf(opts.Out, "%s\n\n", clusterAdminWarning)
	}
	fmt.Fprintf(opts.Out, "binding %s/%s: scope %s, %s\n", name, opts.Env, rbac.Scope, rbac.Plan.describe())

	// ORDER DEPENDS ON THE AUTH.
	//
	// token: the in-cluster write comes first, because the token does not
	// exist until the SA and its Secret do, so there is nothing to send before it.
	//
	// gcp: registration comes first, because the binding's subject is the GSA
	// email and the control plane's response is the only place it is reported.
	// Applying earlier rendered `name: ` — a binding that authorizes nobody,
	// with an apply that still "succeeded".
	applyGrant := func() error {
		if err := rbac.validateConnectIdentity(); err != nil {
			return err
		}
		fmt.Fprintf(opts.Out, "applying the grant to %s (server-side, field manager forge)\n", target.Context)
		for _, stage := range rbac.bootstrapStages() {
			if err := connectApply(ctx, target.Context, stage); err != nil {
				hint := ""
				if rbac.deliveryEnabled() {
					hint = "\nThe delivery grant needs each --delivery-secret namespace to exist and the cluster to " +
						"support ValidatingAdmissionPolicy (Kubernetes 1.30+); nothing past the failed stage was applied"
				}
				return fmt.Errorf("apply the grant to context %q: %w\n"+
					"This needs cluster-admin on the target. It is the one write only you can make: it gives the "+
					"binding the permissions its deploys need, which no deploy can grant itself%s", target.Context, err, hint)
			}
		}
		return nil
	}
	if target.Auth == authToken {
		if err := applyGrant(); err != nil {
			return err
		}
	}

	req := map[string]any{
		"name":        name,
		"auth":        target.Auth,
		"address":     target.Address,
		"caPem":       target.CAPEM,
		"environment": opts.Env,
		"project":     hostedProjectName(),
	}
	if target.Auth == authGCP {
		req["cloudCluster"] = target.CloudCluster
	}
	if wire, ok := issuerPlan.wire(); ok {
		req["issuer"] = wire
	}
	if target.Auth == authToken {
		// Read AFTER the apply, because the token controller populates the
		// Secret asynchronously — a read immediately after creation finds it
		// empty, which is the flake that gets diagnosed as "just run it
		// again".
		token, err := connectReadToken(ctx, target.Context, rbac)
		if err != nil {
			return err
		}
		// WRITE-ONLY, and never printed: not in a summary, not in a debug
		// line, not on failure. The control plane has no field to return it
		// in either.
		req["token"] = token
	}

	var resp wireConnectClusterResponse
	err = client.Call(ctx, procConnectCluster, req, &resp)
	if err != nil && issuerPlan.registers() && issuerMode == issuerAuto && issuerRefusal(err) {
		// The control plane checks the issuer BEFORE it connects anything, so
		// nothing was registered: retry for deploys alone and say why the
		// store was left out. An explicit --issuer stays an error.
		issuerPlan = connectIssuerPlan{Requested: issuerMode, Mode: issuerSkip, Note: "the control plane declined it:\n" + indentIssuerNote(err.Error(), "    ") + issuerHint(issuerPlan.Issuer)}
		delete(req, "issuer")
		resp = wireConnectClusterResponse{}
		err = client.Call(ctx, procConnectCluster, req, &resp)
	}
	if err != nil {
		return fmt.Errorf("connect cluster %q: %w", name, err)
	}
	if resp.Cluster.ID == "" {
		return fmt.Errorf("connect cluster %q: the control plane returned no cluster id", name)
	}

	if target.Auth == authGCP {
		rbac.BindingGSA = resp.BindingIdentity.GCPServiceAccount
		// No GSA means no principal to bind (dev control planes have none);
		// writeGrantInstructions below says so rather than binding nobody. A
		// SHARED identity is never substituted.
		if strings.TrimSpace(rbac.BindingGSA) != "" {
			if err := applyGrant(); err != nil {
				return err
			}
		}
	}
	if rbac.Auth == authToken || strings.TrimSpace(rbac.BindingGSA) != "" {
		pruneConnectGrant(ctx, opts, rbac)
	}

	writeConnectSummary(opts.Out, resp.Cluster, target)
	reportConnectIssuer(opts.Out, name, opts.Env, issuerPlan, resp.Cluster.Issuer)
	writeGrantInstructions(opts.Out, rbac, gke)
	fmt.Fprintf(opts.Out, "\nPoint an environment at it in deploy/kcl/<env>/main.k:\n\n"+
		"    forge.ClusterTarget {\n        cluster = %q\n        namespace = \"...\"\n        connected_cluster = %q\n    }\n",
		target.Context, name)
	return nil
}

// runConnectDryRun prints the RBAC diff and what would be sent. It contacts
// no control plane and writes nothing; the only cluster contact is the
// read-only get of the binding's own labelled objects (skipped by --live-from).
func runConnectDryRun(ctx context.Context, opts clusterConnectOptions, name string, target connectTarget, rbac connectRBAC, issuer connectIssuerPlan) error {
	if target.Auth == authGCP {
		rbac.BindingGSA = strings.TrimSpace(opts.BindingGSA)
		if rbac.BindingGSA == "" {
			rbac.BindingGSA = "<this binding's GCP service account, allocated by the control plane on connect>"
		}
	} else if opts.BindingGSA != "" {
		return fmt.Errorf("--binding-gsa is for --auth gcp; the token auth's subject is the ServiceAccount forge mints")
	}
	out := opts.Out
	fmt.Fprintf(out, "[dry-run] would connect %q for env %q\n", name, opts.Env)
	fmt.Fprintf(out, "  auth         %s\n", connectAuthLabel(target.Auth))
	fmt.Fprintf(out, "  address      %s\n", target.Address)
	if target.CloudCluster != "" {
		fmt.Fprintf(out, "  cloudCluster %s\n", target.CloudCluster)
	}
	fmt.Fprintf(out, "  caPem        %d bytes, read from context %s\n", len(target.CAPEM), target.Context)
	if target.Auth == authToken {
		fmt.Fprintf(out, "  token        minted on apply, sent write-only, never printed\n")
	}
	writeIssuerDryRun(out, issuer)
	fmt.Fprintf(out, "  selector     %s\n\n", rbac.bindingSelector())

	var (
		live   []liveObject
		source string
		err    error
	)
	if opts.LiveFrom != "" {
		live, err = readLiveFile(opts.LiveFrom)
		source = "capture " + opts.LiveFrom
	} else {
		live, err = connectReadLive(ctx, target.Context, rbac)
		source = "context " + target.Context + " (read-only)"
	}
	if err != nil {
		return err
	}
	entries, widened := rbacDiff(rbac.grantObjects(), live)
	writeRBACDiff(out, rbac, source, entries, widened)

	fmt.Fprintf(out, "\n[dry-run] would apply to %s:\n\n", target.Context)
	for _, line := range strings.Split(strings.TrimRight(rbac.bootstrapManifests(), "\n"), "\n") {
		fmt.Fprintf(out, "    %s\n", line)
	}
	return nil
}

// pruneConnectGrant removes this binding's apply-grant objects the new plan no
// longer names, so narrowing the scope narrows the cluster, and the legacy
// pre-binding grant (shared identity, impersonation) this one replaces. Best
// effort: the new grant is already in place, so a failure here leaves litter
// that is reported rather than a missing permission.
func pruneConnectGrant(ctx context.Context, opts clusterConnectOptions, rbac connectRBAC) {
	live, err := connectReadLive(ctx, rbac.KubeContext, rbac)
	if err != nil {
		fmt.Fprintf(opts.Out, "note: could not list the binding's live RBAC to prune stale grants: %v\n", err)
		return
	}
	for _, stale := range stalePrune(rbac.grantObjects(), live) {
		args := []string{"delete", strings.ToLower(stale.Kind), stale.Name, "--ignore-not-found"}
		if stale.Namespace != "" {
			args = append(args, "-n", stale.Namespace)
		}
		if _, err := connectKubectl(ctx, rbac.KubeContext, args...); err != nil {
			fmt.Fprintf(opts.Out, "note: could not prune stale %s %s: %v\n", stale.Kind, stale.Name, err)
			continue
		}
		fmt.Fprintf(opts.Out, "pruned %s %s (the new scope no longer names it)\n", stale.Kind, stale.Name)
	}
	removeLegacyGrant(ctx, opts, rbac)
}

// The pre-binding grant: one ClusterRole per cluster name carrying
// impersonate and bootstrap writes, bound to a shared identity, plus a shared
// token ServiceAccount per physical cluster. Identified by the absence of the
// binding label.
const (
	legacyTokenServiceAccount = "forge-connect"
	legacyTokenSecret         = "forge-connect-token"
)

func legacySelector(clusterName string) string {
	return forgeManagedLabelKey + "=" + forgeManagedLabelValue + "," + connectClusterLabelKey + "=" + clusterName + ",!" + connectBindingLabelKey
}

func removeLegacyGrant(ctx context.Context, opts clusterConnectOptions, rbac connectRBAC) {
	sel := legacySelector(rbac.ClusterName)
	out, err := connectKubectl(ctx, rbac.KubeContext, "get", "clusterrolebinding,clusterrole,rolebinding,role",
		"--all-namespaces", "-l", sel, "-o", "name")
	if err != nil || strings.TrimSpace(out) == "" {
		return
	}
	// The legacy delivery policy goes with its Roles: it names the shared
	// identity, which this grant replaces.
	for _, args := range [][]string{
		{"delete", "validatingadmissionpolicybinding,validatingadmissionpolicy", "-l", sel, "--ignore-not-found"},
		{"delete", "rolebinding,role", "--all-namespaces", "-l", sel, "--ignore-not-found"},
		{"delete", "clusterrolebinding,clusterrole", "-l", sel, "--ignore-not-found"},
	} {
		if text, err := connectKubectl(ctx, rbac.KubeContext, args...); err != nil {
			if strings.Contains(args[1], "validatingadmissionpolicy") && strings.Contains(text+err.Error(), "doesn't have a resource type") {
				continue
			}
			fmt.Fprintf(opts.Out, "note: could not remove the legacy shared-identity grant (%s): %v\n", args[1], err)
			return
		}
	}
	fmt.Fprintf(opts.Out, "removed the legacy grant for %s (shared identity, impersonation, bootstrap writes)\n", rbac.ClusterName)
	// The shared token ServiceAccount serves every legacy name on this
	// physical cluster: it goes only when no legacy binding is left.
	rest, err := connectKubectl(ctx, rbac.KubeContext, "get", "clusterrolebinding", "-l",
		forgeManagedLabelKey+"="+forgeManagedLabelValue+",!"+connectBindingLabelKey+","+connectClusterLabelKey, "-o", "name")
	if err != nil || strings.TrimSpace(rest) != "" {
		return
	}
	for _, args := range [][]string{
		{"delete", "secret", legacyTokenSecret, "-n", rbac.TokenNamespace, "--ignore-not-found"},
		{"delete", "serviceaccount", legacyTokenServiceAccount, "-n", rbac.TokenNamespace, "--ignore-not-found"},
	} {
		if _, err := connectKubectl(ctx, rbac.KubeContext, args...); err != nil {
			fmt.Fprintf(opts.Out, "note: could not remove the legacy shared token identity: %v\n", err)
			return
		}
	}
	fmt.Fprintf(opts.Out, "removed the legacy shared token identity %s/%s\n", rbac.TokenNamespace, legacyTokenServiceAccount)
}

// writeConnectSummary is what the author reads first: the cluster as the
// control plane now holds it.
func writeConnectSummary(out io.Writer, c wireConnectedCluster, target connectTarget) {
	fmt.Fprintf(out, "\nconnected %s\n", c.Name)
	fmt.Fprintf(out, "  id        %s\n", c.ID)
	fmt.Fprintf(out, "  auth      %s\n", connectAuthLabel(target.Auth))
	fmt.Fprintf(out, "  address   %s\n", c.Address)
	if c.CloudCluster != "" {
		fmt.Fprintf(out, "  cloud     %s\n", c.CloudCluster)
	}
	fmt.Fprintf(out, "  context   %s\n", target.Context)
}

// connectAuthLabel renders an auth value in forge's lower-case vocabulary.
func connectAuthLabel(wire string) string {
	switch wire {
	case authGCP:
		return "gcp (GKE workload identity — no secret crosses the boundary)"
	case authToken:
		return "token (scoped ServiceAccount token, minted in your cluster)"
	default:
		return "unknown"
	}
}

// The cluster-contacting steps, as seams. Tests drive the real command against
// a fake control plane without a cluster; nothing else substitutes them.
var (
	connectApply     = cluster.KubectlApply
	connectReadToken = readConnectToken
)

// readConnectToken reads the minted ServiceAccount token, waiting for the
// token controller to populate the Secret it was just handed.
func readConnectToken(ctx context.Context, kctx string, rbac connectRBAC) (string, error) {
	const (
		timeout = 60 * time.Second
		every   = time.Second
	)
	deadline := time.Now().Add(timeout)
	for {
		if b64 := kubectlSecretField(ctx, kctx, rbac.TokenNamespace, rbac.tokenSecretName(), "token"); b64 != "" {
			tok, err := base64.StdEncoding.DecodeString(b64)
			if err != nil {
				return "", fmt.Errorf("decode the minted ServiceAccount token: %w", err)
			}
			return string(tok), nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("the ServiceAccount token Secret %s/%s on %q was not populated within %s.\n"+
				"The token controller fills it asynchronously; an empty Secret this long usually means "+
				"the cluster's token controller is not running",
				rbac.TokenNamespace, rbac.tokenSecretName(), kctx, timeout)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(every):
		}
	}
}

func runClusterDisconnect(ctx context.Context, name, kubeContext, envName, token string, out io.Writer) error {
	name = strings.TrimSpace(name)
	client, _, err := clusterConnectClient(ctx, envName, token)
	if err != nil {
		return err
	}
	// Resolved by exact name, because that is what the author typed and the
	// RPC is keyed on an id. A prefix or fuzzy match here would be a
	// deletion that hit the wrong cluster.
	id, err := lookupConnectedCluster(ctx, client, name)
	if err != nil {
		return err
	}
	// THE REVOKE COMES FIRST. A credential the control plane has revoked is
	// already powerless, so a cleanup that then fails leaves litter rather
	// than a live credential. The other order leaves a window where the
	// token is deleted but still registered, which reads as an outage.
	if err := client.Call(ctx, procRemoveCluster, map[string]any{"id": id}, &struct{}{}); err != nil {
		return fmt.Errorf("disconnect cluster %q: %w", name, err)
	}
	fmt.Fprintf(out, "deregistered %s (%s)\n", name, id)
	_ = removeConnectIssuerRecord(name)

	if strings.TrimSpace(kubeContext) == "" {
		fmt.Fprintf(out, "\nNo --context given, so nothing was deleted in the cluster. To clean up, every object forge\n"+
			"created for this cluster carries the labels %s:\n\n", disconnectSelector(name))
		for _, args := range disconnectCommands(name) {
			fmt.Fprintf(out, "    kubectl --context <ctx> %s\n", strings.Join(args, " "))
		}
		return nil
	}
	return deleteConnectObjects(ctx, kubeContext, name, out)
}

// disconnectSelector selects everything forge created for one connected
// cluster: every binding's grant and identity, and the delivery grant, plus
// the legacy pre-binding grant, which carries the same two labels.
func disconnectSelector(clusterName string) string {
	return forgeManagedLabelKey + "=" + forgeManagedLabelValue + "," + connectClusterLabelKey + "=" + clusterName
}

// disconnectCommands are the deletes, in order. The admission policy goes
// first: it only ever narrows. Cluster-scoped kinds that a cluster too old for
// ValidatingAdmissionPolicy lacks are tolerated by the caller.
func disconnectCommands(clusterName string) [][]string {
	sel := disconnectSelector(clusterName)
	return [][]string{
		{"delete", "validatingadmissionpolicybinding,validatingadmissionpolicy", "-l", sel, "--ignore-not-found"},
		{"delete", "rolebinding,role", "--all-namespaces", "-l", sel, "--ignore-not-found"},
		{"delete", "clusterrolebinding,clusterrole", "-l", sel, "--ignore-not-found"},
		{"delete", "secret,serviceaccount", "--all-namespaces", "-l", sel, "--ignore-not-found"},
	}
}

// connectKubectl runs kubectl through a context and returns stdout. A var so
// tests record the commands and answer the reads.
var connectKubectl = func(ctx context.Context, kctx string, args ...string) (string, error) {
	return runKubectl(ctx, cluster.KubectlArgs(kctx, args...))
}

// deleteConnectObjects removes what forge created in the target, found by
// label rather than by name: disconnect is not told which bindings, scopes or
// namespaces were ever applied. A missing object is not an error — a
// disconnect run twice, or after a partial connect, must converge.
func deleteConnectObjects(ctx context.Context, kctx, clusterName string, out io.Writer) error {
	for _, args := range disconnectCommands(clusterName) {
		if outText, err := connectKubectl(ctx, kctx, args...); err != nil {
			// A cluster too old for ValidatingAdmissionPolicy never had a
			// delivery policy to delete; that is convergence, not failure.
			if strings.Contains(args[1], "validatingadmissionpolicy") && strings.Contains(outText+err.Error(), "doesn't have a resource type") {
				continue
			}
			return fmt.Errorf("delete %s on context %q: %w", args[1], kctx, err)
		}
	}
	// THE NAMESPACES ARE LEFT ALONE, deliberately: forge created them only if
	// absent, they are shared by construction, and deleting a namespace deletes
	// everything anyone else put in it. The objects forge owns inside them are gone.
	fmt.Fprintf(out, "deleted every grant, token identity and policy forge created for %s through context %s\n", clusterName, kctx)
	fmt.Fprintf(out, "  namespaces were left in place (shared; forge deletes only the objects it owns)\n")
	return nil
}

// lookupConnectedCluster resolves a cluster NAME to the id the RPCs are keyed
// on, so the author never has to know a uuid.
func lookupConnectedCluster(ctx context.Context, c cloudCaller, name string) (string, error) {
	var resp struct {
		Clusters []wireConnectedCluster `json:"clusters"`
	}
	if err := c.Call(ctx, procListClusters, map[string]any{}, &resp); err != nil {
		return "", fmt.Errorf("list clusters: %w", err)
	}
	for _, cl := range resp.Clusters {
		if cl.Name == name {
			return cl.ID, nil
		}
	}
	known := make([]string, 0, len(resp.Clusters))
	for _, cl := range resp.Clusters {
		known = append(known, cl.Name)
	}
	if len(known) == 0 {
		return "", fmt.Errorf("no cluster named %q: this organization has no connected clusters", name)
	}
	return "", fmt.Errorf("no cluster named %q (this organization has: %s)", name, strings.Join(known, ", "))
}

// clusterConnectCommands are the two commands this file adds to `forge
// cluster`. Named so dev.go's newClusterCmd registers them without this file
// needing to know the parent's shape.
func clusterConnectCommands() []*cobra.Command {
	return []*cobra.Command{newClusterConnectCmd(), newClusterDisconnectCmd()}
}

// writeIssuerDryRun says what the run would send about the issuer.
func writeIssuerDryRun(out io.Writer, p connectIssuerPlan) {
	switch {
	case p.registers() && p.Issuer != "":
		keys := "the store fetches the keys itself"
		if p.Mode == issuerUpload {
			keys = fmt.Sprintf("%d public key(s) uploaded, set %s", p.KeyCount, p.Fingerprint)
		}
		fmt.Fprintf(out, "  issuer       %s (%s; %s)\n", p.Issuer, p.Mode, keys)
	case p.Mode == issuerWithdraw:
		fmt.Fprintf(out, "  issuer       would be withdrawn\n")
	case p.Note != "":
		fmt.Fprintf(out, "  issuer       none sent: %s\n", p.Note)
	default:
		fmt.Fprintf(out, "  issuer       left as it is\n")
	}
}

func indentIssuerNote(s, prefix string) string {
	return prefix + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n"+prefix)
}
