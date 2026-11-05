package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gofrs/flock"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/credentials"
)

// Bindings identify a server project, not an org or a selected credential.
// The server verifies the immutable owner and live caller membership every use.
const hostedProjectBindingFile = ".forge/hosted-projects.json"

// prepareHostedProjectForWrite is the common write-path bootstrap, before
// capacity/ledger/build operations can resolve an unbound project implicitly.
func prepareHostedProjectForWrite(ctx context.Context, projectDir, env string, f *deployCmdFlags) error {
	if f.dryRun || f.explain || f.promote.plan || f.promote.planOnly || f.claimable || f.offlineLedger {
		return nil
	}
	entities, err := RenderKCL(ctx, projectDir, env)
	if err != nil {
		return err
	}
	if entities == nil || !entities.HasHosted() {
		return nil
	}
	decl := declarationFromEntities(entities)
	ep, err := projectEndpoint(env, decl)
	if err != nil {
		return err
	}
	cred, err := cloud.ResolveCredential("", ep)
	if err != nil {
		return err
	}
	if cred.Kind != "human" {
		return nil
	}
	if _, err := bindHostedProject(ctx, env, decl, f.organization); err != nil {
		return err
	}
	if entities.ControlPlane != nil {
		entities.ControlPlane.org = &orgState{}
	}
	// --org authorizes only this invocation's first unbound creation. The
	// binding is now persisted; late publish must resolve it without trying
	// to create it again. A pre-existing binding with --org was already
	// refused by bindHostedProject above, before this selector is consumed.
	f.organization = ""
	return nil
}

func projectEndpoint(env string, decl *cloud.Declaration) (cloud.Endpoint, error) {
	ep, err := cloud.ResolveEndpoint(env, decl)
	if err != nil {
		return ep, err
	}
	ep.ProjectName = hostedProjectName()
	file, err := findProjectConfigFile()
	if errors.Is(err, ErrProjectConfigNotFound) {
		return ep, nil
	}
	if err != nil {
		return ep, err
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), hostedProjectBindingFile))
	if errors.Is(err, os.ErrNotExist) {
		return ep, nil
	}
	if err != nil {
		return ep, err
	}
	var ids map[string]string
	if err := json.Unmarshal(raw, &ids); err != nil {
		return ep, fmt.Errorf("read hosted project binding: %w", err)
	}
	key, err := credentials.Normalize(ep.URL)
	if err != nil {
		return ep, err
	}
	ep.ProjectID = ids[key]
	return ep, nil
}

func bindHostedProject(ctx context.Context, env string, decl *cloud.Declaration, org string) (cloud.Endpoint, error) {
	file, err := findProjectConfigFile()
	if err != nil {
		return cloud.Endpoint{}, err
	}
	path := filepath.Join(filepath.Dir(file), hostedProjectBindingFile)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return cloud.Endpoint{}, err
	}
	lock := flock.New(path+".lock", flock.SetPermissions(0600))
	if err := lock.Lock(); err != nil {
		return cloud.Endpoint{}, err
	}
	defer func() { _ = lock.Unlock() }()
	return bindHostedProjectLocked(ctx, env, decl, org)
}

func bindHostedProjectLocked(ctx context.Context, env string, decl *cloud.Declaration, org string) (cloud.Endpoint, error) {
	ep, err := projectEndpoint(env, decl)
	if err != nil {
		return ep, err
	}
	cred, err := cloud.ResolveCredential("", ep)
	if err != nil {
		return ep, err
	}
	if cred.Kind != "human" {
		if org != "" {
			return ep, fmt.Errorf("--org cannot retarget a delegated capability")
		}
		// A delegated capability is already scoped server-side to its org
		// and keeps the GetTenant contract: there is no project to bind,
		// and asking the server to create one would be a human-only act.
		return ep, nil
	}
	if ep.ProjectID != "" {
		if org != "" {
			return ep, fmt.Errorf("--org is only for unbound creation; this project already has server identity %s", ep.ProjectID)
		}
		return ep, nil
	}
	var reply struct {
		ProjectID string `json:"projectId"`
		OrgID     string `json:"orgId"`
		Name      string `json:"name"`
	}
	request := map[string]any{"projectId": ep.ProjectID, "name": ep.ProjectName, "create": true}
	if org != "" {
		request["orgId"] = org
	}
	if err := cloud.NewClient(ep, cred).Call(ctx, "controlplane.v1.DeployService/ResolveHostedProject", request, &reply); err != nil {
		return ep, err
	}
	if reply.ProjectID == "" || reply.OrgID == "" || reply.Name != ep.ProjectName {
		return ep, fmt.Errorf("control plane returned incomplete project identity")
	}
	file, err := findProjectConfigFile()
	if err != nil {
		return ep, err
	}
	path := filepath.Join(filepath.Dir(file), hostedProjectBindingFile)
	ids := map[string]string{}
	if raw, e := os.ReadFile(path); e == nil {
		if err := json.Unmarshal(raw, &ids); err != nil {
			return ep, err
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return ep, e
	}
	key, err := credentials.Normalize(ep.URL)
	if err != nil {
		return ep, err
	}
	if prior := ids[key]; prior != "" && prior != reply.ProjectID {
		return ep, fmt.Errorf("refusing to replace bound hosted project")
	}
	ids[key] = reply.ProjectID
	raw, err := json.MarshalIndent(ids, "", "  ")
	if err != nil {
		return ep, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return ep, err
	}
	ignore := filepath.Join(filepath.Dir(path), ".gitignore")
	ignored, readErr := os.ReadFile(ignore)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return ep, readErr
	}
	if !strings.Contains("\n"+string(ignored), "\n/hosted-projects.json\n") {
		if err := atomicfile.WriteFile(ignore, append(ignored, []byte("\n/hosted-projects.json\n/hosted-projects.json.lock\n/.hosted-projects-*\n")...), 0600); err != nil {
			return ep, err
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".hosted-projects-*")
	if err != nil {
		return ep, err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err = tmp.Write(append(raw, '\n')); err != nil {
		_ = tmp.Close()
		return ep, err
	}
	if err := tmp.Close(); err != nil {
		return ep, err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return ep, err
	}
	ep.ProjectID = reply.ProjectID
	return ep, nil
}
