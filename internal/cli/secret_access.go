package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cloud"
)

// A hosted secret's ACCESS LIST says which principals may use it: today, a
// Reliant workflow, named by (reliant project id, workflow name). The list is
// stored by the control plane (SecretStoreService); forge only edits and reads
// it. A workflow that references the secret in a run is authorized against this
// list, never against anything in the workflow file.

const (
	procAllowSecretUse      = "controlplane.v1.SecretStoreService/AllowSecretUse"
	procDenySecretUse       = "controlplane.v1.SecretStoreService/DenySecretUse"
	procListSecretAccess    = "controlplane.v1.SecretStoreService/ListSecretAccess"
	secretPrincipalWorkflow = "workflow"
)

// hostedSecretAccess is the access-list half of a hosted secret store. It is
// asserted on the writer newHostedSecretWriter returns instead of widening
// hostedSecretWriter: stores that hold values but have no access lists (and
// their test fakes) are not asked to implement it.
type hostedSecretAccess interface {
	Allow(ctx context.Context, name string, p secretPrincipal) error
	Deny(ctx context.Context, name string, p secretPrincipal) error
	// Access returns the active entries for one secret, or for every secret in
	// the env when name is empty.
	Access(ctx context.Context, name string) ([]secretAccessEntry, error)
}

// secretPrincipal is one thing that may use a secret. Only workflows exist.
type secretPrincipal struct {
	Kind         string `json:"kind"`
	ProjectID    string `json:"projectId"`
	WorkflowName string `json:"workflowName"`
}

// secretAccessEntry is one row of a secret's access list, in the shape
// `forge secret access --json` and `forge secret list --json` emit. Nothing in
// it can carry a secret value.
type secretAccessEntry struct {
	SecretName   string `json:"secret_name"`
	Kind         string `json:"kind"`
	ProjectID    string `json:"project_id"`
	WorkflowName string `json:"workflow_name"`
	CreatedBy    string `json:"created_by,omitempty"`
	CreatedAt    string `json:"created_at,omitempty"`
}

func (w cloudSecretWriter) Allow(ctx context.Context, name string, p secretPrincipal) error {
	return w.client.Call(ctx, procAllowSecretUse, accessChangeRequest(w.environmentID, name, p), nil)
}

func (w cloudSecretWriter) Deny(ctx context.Context, name string, p secretPrincipal) error {
	return w.client.Call(ctx, procDenySecretUse, accessChangeRequest(w.environmentID, name, p), nil)
}

func accessChangeRequest(envID, name string, p secretPrincipal) map[string]any {
	return map[string]any{"environmentId": envID, "secretName": name, "principal": p}
}

func (w cloudSecretWriter) Access(ctx context.Context, name string) ([]secretAccessEntry, error) {
	var resp struct {
		Entries []struct {
			SecretName string          `json:"secretName"`
			Principal  secretPrincipal `json:"principal"`
			CreatedBy  string          `json:"createdBy"`
			CreatedAt  string          `json:"createdAt"`
		} `json:"entries"`
	}
	req := map[string]any{"environmentId": w.environmentID, "secretName": name}
	if err := w.client.Call(ctx, procListSecretAccess, req, &resp); err != nil {
		return nil, err
	}
	out := make([]secretAccessEntry, 0, len(resp.Entries))
	for _, e := range resp.Entries {
		out = append(out, secretAccessEntry{
			SecretName: e.SecretName, Kind: e.Principal.Kind, ProjectID: e.Principal.ProjectID,
			WorkflowName: e.Principal.WorkflowName, CreatedBy: e.CreatedBy, CreatedAt: e.CreatedAt,
		})
	}
	sortAccessEntries(out)
	return out, nil
}

func sortAccessEntries(es []secretAccessEntry) {
	sort.SliceStable(es, func(i, j int) bool {
		a, b := es[i], es[j]
		if a.SecretName != b.SecretName {
			return a.SecretName < b.SecretName
		}
		if a.ProjectID != b.ProjectID {
			return a.ProjectID < b.ProjectID
		}
		return a.WorkflowName < b.WorkflowName
	})
}

// openHostedSecretAccess renders the env, refuses anything but a hosted env
// (access lists live on the control plane; a FileSecrets store has none), and
// opens the access half of its writer. ensure follows the hosted rule: allow
// creates the env by name so access can be granted before the first deploy,
// as `forge secret set` can; deny and access are not creators.
func openHostedSecretAccess(ctx context.Context, envName string, ensure bool) (hostedSecretAccess, cloud.Endpoint, error) {
	entities, err := renderEntitiesForSecrets(ctx, envName)
	if err != nil {
		return nil, cloud.Endpoint{}, fmt.Errorf("render KCL: %w", err)
	}
	if !isHostedSecretEnv(entities) {
		provider := "no secret_provider"
		if sp := entities.SecretProvider; sp != nil {
			provider = fmt.Sprintf("a %q secret_provider", sp.Type)
		}
		return nil, cloud.Endpoint{}, fmt.Errorf(
			"env %q declares %s: secret access lists are a hosted feature\n"+
				"fix: they exist only for forge.HostedSecrets envs (a workflow can use only a secret stored on the control plane); "+
				"a local FileSecrets store has no access list", envName, provider)
	}
	w, ep, err := newHostedSecretWriter(ctx, envName, entities, ensure)
	if err != nil {
		return nil, ep, err
	}
	acc, ok := w.(hostedSecretAccess)
	if !ok {
		return nil, ep, fmt.Errorf("the secret store of env %q does not support access lists", envName)
	}
	return acc, ep, nil
}

// requireAccessProject resolves the Reliant project id. There is no reliable
// ambient source for it: a checkout can be registered as several Reliant
// projects (each worktree is its own), and forge may not call into Reliant
// to ask. So it is always named.
func requireAccessProject(project, usage string) (string, error) {
	project = strings.TrimSpace(project)
	if project == "" {
		return "", fmt.Errorf("--project is required: the Reliant project id that owns the workflow\n"+
			"fix: find it with `reliant project list` (the ID column; pick the project whose PATH is the checkout holding "+
			".reliant/workflows/<name>.yaml), then forge secret %s", usage)
	}
	return project, nil
}

func requireAccessWorkflow(workflow, usage string) (string, error) {
	workflow = strings.TrimSpace(workflow)
	if workflow == "" {
		return "", fmt.Errorf("--workflow is required: the name of the workflow to allow (the `name:` in its .reliant/workflows/<name>.yaml)\n"+
			"fix: forge secret %s", usage)
	}
	return workflow, nil
}

func runSecretAllow(ctx context.Context, envName, name, workflow, project string, out io.Writer) error {
	acc, ep, err := openHostedSecretAccess(ctx, envName, true)
	if err != nil {
		return err
	}
	if err := acc.Allow(ctx, name, secretPrincipal{Kind: secretPrincipalWorkflow, ProjectID: project, WorkflowName: workflow}); err != nil {
		return err
	}
	fmt.Fprintf(out, "allowed workflow %q (Reliant project %s) to use %s in hosted env %q at %s\n", workflow, project, name, envName, ep.URL)
	return nil
}

func runSecretDeny(ctx context.Context, envName, name, workflow, project string, out io.Writer) error {
	acc, ep, err := openHostedSecretAccess(ctx, envName, false)
	if errors.Is(err, errHostedEnvNotFound) {
		fmt.Fprintf(out, "hosted env %q is unknown to the control plane: nothing to deny\n", envName)
		return nil
	}
	if err != nil {
		return err
	}
	if err := acc.Deny(ctx, name, secretPrincipal{Kind: secretPrincipalWorkflow, ProjectID: project, WorkflowName: workflow}); err != nil {
		return err
	}
	fmt.Fprintf(out, "denied workflow %q (Reliant project %s) use of %s in hosted env %q at %s\n", workflow, project, name, envName, ep.URL)
	return nil
}

// secretAccessReport is the `forge secret access --json` document.
type secretAccessReport struct {
	Env     string              `json:"env"`
	Secret  string              `json:"secret,omitempty"`
	Entries []secretAccessEntry `json:"entries"`
}

func runSecretAccess(ctx context.Context, envName, name string, jsonOut bool, out io.Writer) error {
	report := secretAccessReport{Env: envName, Secret: name, Entries: []secretAccessEntry{}}
	acc, ep, err := openHostedSecretAccess(ctx, envName, false)
	switch {
	case errors.Is(err, errHostedEnvNotFound):
		// A read: an env the control plane has not seen has no access entries.
	case err != nil:
		return err
	default:
		if report.Entries, err = acc.Access(ctx, name); err != nil {
			return err
		}
	}
	if jsonOut {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}
	where := ""
	if ep.URL != "" {
		where = " at " + ep.URL
	}
	subject := "every secret"
	if name != "" {
		subject = name
	}
	fmt.Fprintf(out, "access to %s in hosted env %q%s\n\n", subject, envName, where)
	if len(report.Entries) == 0 {
		fmt.Fprintln(out, "no access entries: no workflow may use it")
		fmt.Fprintf(out, "fix: forge secret allow --env %s --workflow <name> --project <reliant project id> %s\n", envName, orPlaceholder(name))
		return nil
	}
	writeAccessEntries(out, report.Entries, name == "", "  ")
	return nil
}

func orPlaceholder(name string) string {
	if name == "" {
		return "<NAME>"
	}
	return name
}

// writeAccessEntries prints entries, one per line. withSecret prefixes the
// secret name for listings that span several secrets.
func writeAccessEntries(out io.Writer, entries []secretAccessEntry, withSecret bool, indent string) {
	for _, e := range entries {
		line := fmt.Sprintf("%s%s %q (project %s)", indent, e.Kind, e.WorkflowName, e.ProjectID)
		if withSecret {
			line = fmt.Sprintf("%s%-34s %s %q (project %s)", indent, e.SecretName, e.Kind, e.WorkflowName, e.ProjectID)
		}
		if e.CreatedBy != "" {
			line += " granted by " + e.CreatedBy
		}
		if e.CreatedAt != "" {
			line += " at " + e.CreatedAt
		}
		fmt.Fprintln(out, line)
	}
}

func newSecretAllowCmd() *cobra.Command {
	var env, workflow, project string
	cmd := &cobra.Command{
		Use:   "allow --env <environment> --workflow <name> --project <id> <NAME>",
		Short: "Allow a workflow to use a hosted secret",
		Args:  cobra.ExactArgs(1),
		Long: `Add a workflow to a hosted secret's access list. A Reliant workflow that
references the secret ({{ secrets.NAME }} in an http/request header) runs only
if its (project, workflow name) is on the list; any run or trigger of that
workflow may then use it. Idempotent.

The list is the whole policy: it names no destination, path or step.
Only org owners and admins may edit it, and a protected environment asks for
step-up; the control plane's refusal is printed as it is sent.

  forge secret allow --env staging --workflow trader-bot --project <id> TRADER_KEY

--project is the Reliant project id that owns the workflow (find it with
` + "`reliant project list`" + `). Hosted secrets only: a FileSecrets store has no access
lists.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSecretAccessChange(cmd, "allow", env, workflow, project, args[0], runSecretAllow)
		},
	}
	addSecretEnvFlag(cmd, &env)
	addAccessPrincipalFlags(cmd, &workflow, &project)
	return cmd
}

func newSecretDenyCmd() *cobra.Command {
	var env, workflow, project string
	cmd := &cobra.Command{
		Use:   "deny --env <environment> --workflow <name> --project <id> <NAME>",
		Short: "Revoke a workflow's use of a hosted secret",
		Args:  cobra.ExactArgs(1),
		Long: `Remove a workflow from a hosted secret's access list. Its runs lose the
secret at their next use. Idempotent; the same authorization as 'allow'.

  forge secret deny --env staging --workflow trader-bot --project <id> TRADER_KEY`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSecretAccessChange(cmd, "deny", env, workflow, project, args[0], runSecretDeny)
		},
	}
	addSecretEnvFlag(cmd, &env)
	addAccessPrincipalFlags(cmd, &workflow, &project)
	return cmd
}

func addAccessPrincipalFlags(cmd *cobra.Command, workflow, project *string) {
	cmd.Flags().StringVar(workflow, "workflow", "", "Workflow name (required)")
	cmd.Flags().StringVar(project, "project", "", "Reliant project id that owns the workflow (required; run 'reliant project list')")
}

func runSecretAccessChange(cmd *cobra.Command, verb, env, workflow, project, name string,
	run func(ctx context.Context, envName, name, workflow, project string, out io.Writer) error) error {
	usage := verb + " --env <env> --workflow <name> --project <id> " + name
	envName, err := requireSecretEnv(env, usage)
	if err != nil {
		return err
	}
	if workflow, err = requireAccessWorkflow(workflow, usage); err != nil {
		return err
	}
	if project, err = requireAccessProject(project, usage); err != nil {
		return err
	}
	return run(cmd.Context(), envName, name, workflow, project, cmd.OutOrStdout())
}

func newSecretAccessCmd() *cobra.Command {
	var (
		env     string
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "access --env <environment> [NAME]",
		Short: "Show who may use a hosted secret",
		Args:  cobra.MaximumNArgs(1),
		Long: `Show the access list of one hosted secret, or of every secret in the env
when NAME is omitted. Metadata only; values are never printed.

  forge secret access --env staging TRADER_KEY
  forge secret access --env staging --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			envName, err := requireSecretEnv(env, "access --env <env> "+name)
			if err != nil {
				return err
			}
			return runSecretAccess(cmd.Context(), envName, name, jsonOut, cmd.OutOrStdout())
		},
	}
	addSecretEnvFlag(cmd, &env)
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Emit machine-readable JSON")
	return cmd
}
