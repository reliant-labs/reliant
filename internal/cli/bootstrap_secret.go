package cli

import (
	"context"
	"fmt"
	"io"
	"sort"

	"github.com/reliant-labs/forge/pkg/release"
	"github.com/reliant-labs/forge/pkg/secretgen"
)

// BootstrapSecret.generate at deploy time.
//
// A bootstrap value (the OpenBao seal key, the Postgres password) lives in the
// env's bootstrap source, outside the store it bootstraps. When a
// BootstrapSecret declares `generate`, `forge env deploy` mints it ONCE, with
// the deployer's own Application Default Credentials, before anything reads it:
// the same create-if-absent rule FileSecrets.generate has, moved to a store the
// deployer can write.
//
// WHAT THE GUARANTEE IS. Secret Manager has no check-and-set, so "create if
// absent" is two calls: look for a usable version, add one if there is none.
// Two deployers racing can both see none and both add. The loser is the one
// whose version number is not 1: it DISABLES the version it just added, so
// "latest" and version 1 stay one value whoever won, and says so. The seal key
// is additionally read at version 1 and never "latest" (pkg/deploy/openbao
// SealSecretVersion), so even a loser that cannot disable (it holds
// versions.add but not versions.disable) changes nothing the store opens with.
// A password is read as "latest", so for it the disable is the guard, and a
// failed disable is an error that names the version to disable by hand.
//
// What this never does: overwrite, rotate, or delete. A container that already
// holds a usable version is left alone, whatever it holds. It prints names and
// version numbers, never a value.

// bootstrapMinter writes the generated bootstrap values.
type bootstrapMinter struct {
	api     gsmAPI
	project string
	out     io.Writer
}

// bootstrapMintResult is what one value's mint did.
type bootstrapMintResult struct {
	Name    string
	Created bool
	Version uint32
	// Raced is true when the deploy added a version but version 1 is not the
	// one it added: another writer minted first. Disabled is true when this
	// deploy then took its own version out of service.
	Raced    bool
	Disabled bool
}

// mintBootstrap creates every generated bootstrap value the bootstrap source
// does not hold. generate maps a secret id to how to mint it.
func (m bootstrapMinter) mintBootstrap(ctx context.Context, generate map[string]release.RenderedGeneratedSecret) ([]bootstrapMintResult, error) {
	names := make([]string, 0, len(generate))
	for n := range generate {
		names = append(names, n)
	}
	sort.Strings(names)

	var results []bootstrapMintResult
	for _, name := range names {
		r, err := m.mintOne(ctx, name, generate[name])
		if err != nil {
			return results, err
		}
		results = append(results, r)
	}
	return results, nil
}

func (m bootstrapMinter) mintOne(ctx context.Context, name string, g release.RenderedGeneratedSecret) (bootstrapMintResult, error) {
	res := bootstrapMintResult{Name: name}
	version, usable, exists, err := m.api.Latest(ctx, m.project, name)
	if err != nil {
		return res, fmt.Errorf("bootstrap secret %s: look for an existing value: %w", name, err)
	}
	if exists && usable {
		res.Version = version
		return res, nil
	}
	if exists && version > 0 {
		// A version exists but is disabled or destroyed. Adding another would
		// quietly replace a value somebody deliberately took out of service.
		return res, fmt.Errorf("bootstrap secret %s has version %d, which is disabled or destroyed, and no usable version. forge will not mint over it: enable the version, or add a replacement yourself with `forge secret import --to-bootstrap`", name, version)
	}
	if !exists {
		return res, fmt.Errorf("bootstrap secret %s does not exist in project %s. Create the container first: it holds no value until forge or `forge secret import --to-bootstrap` adds one, and the identity that reads it is granted on the container, so forge does not create one", name, m.project)
	}

	spec := secretgen.Spec{
		Kind: secretgen.Kind(g.Kind), Names: []string{secretNameFor(name)},
		Bytes: g.Bytes, Encoding: g.Encoding, Prefix: g.Prefix, Length: g.Length, Symbols: g.Symbols,
	}
	vals, err := secretgen.Generate(spec, secretgen.Options{})
	if err != nil {
		return res, fmt.Errorf("bootstrap secret %s: %w", name, err)
	}
	value := vals[spec.Names[0]]
	added, err := m.api.AddVersion(ctx, m.project, name, []byte(value), false)
	if err != nil {
		return res, redactValue(fmt.Errorf("bootstrap secret %s: add the generated value: %w", name, err), value)
	}
	res.Created, res.Version = true, added
	if added != 1 {
		// Another writer added a version between our look and our add. Ours is
		// the one that must not be "latest".
		res.Raced = true
		if derr := m.api.Disable(ctx, m.project, name, added); derr != nil {
			return res, fmt.Errorf("bootstrap secret %s: another writer minted version 1 first and this deploy added version %d beside it, which it could not disable (%v). Disable version %d yourself: `gcloud secrets versions disable %d --secret %s --project %s`. Until then \"latest\" is not the value the store was set up with", name, added, derr, added, added, name, m.project)
		}
		res.Disabled = true
	}
	return res, nil
}

// secretNameFor maps a Secret Manager secret id onto the identifier form
// secretgen validates (letters, digits and '_'), so a hyphenated id can be
// generated. The name only labels the value inside secretgen; it never reaches
// Secret Manager.
func secretNameFor(id string) string {
	b := []byte(id)
	for i, c := range b {
		if c == '-' {
			b[i] = '_'
		}
	}
	if len(b) > 0 && b[0] >= '0' && b[0] <= '9' {
		return "_" + string(b)
	}
	return string(b)
}

// report prints names and version numbers.
func (m bootstrapMinter) report(results []bootstrapMintResult) {
	for _, r := range results {
		switch {
		case r.Created && r.Raced:
			fmt.Fprintf(m.out, "bootstrap secret %s: another writer minted first; this deploy added version %d and disabled it (the store opens with version 1)\n", r.Name, r.Version)
		case r.Created:
			fmt.Fprintf(m.out, "bootstrap secret %s: minted (version %d, in project %s)\n", r.Name, r.Version, m.project)
		default:
			fmt.Fprintf(m.out, "bootstrap secret %s: present (version %d), left alone\n", r.Name, r.Version)
		}
	}
}

// bootstrapGenerateFor returns the project and the values an env's bootstrap
// source asks the deploy to mint, or nothing.
func bootstrapGenerateFor(e *KCLEntities) (project string, generate map[string]release.RenderedGeneratedSecret) {
	if e == nil || e.OpenBao == nil || e.BootstrapSource == nil || e.BootstrapSource.Type != "gcpsm" {
		return "", nil
	}
	if len(e.OpenBao.BootstrapGenerate) == 0 {
		return "", nil
	}
	return e.BootstrapSource.Project, e.OpenBao.BootstrapGenerate
}

// ensureBootstrapSecrets is the deploy step: mint what the bootstrap source
// lacks. A dry run reports what it would do and writes nothing; an env with
// nothing to mint costs nothing and needs no credentials.
func ensureBootstrapSecrets(ctx context.Context, e *KCLEntities, dryRun bool, out io.Writer) error {
	project, generate := bootstrapGenerateFor(e)
	if len(generate) == 0 {
		return nil
	}
	api, err := newGSMAPI(ctx)
	if err != nil {
		return fmt.Errorf("bootstrap secrets: %w\nThe seal key and database password are minted into Google Secret Manager with the deployer's own credentials; or provide them yourself with `forge secret import --to-bootstrap` and drop `generate` from the BootstrapSecret", err)
	}
	m := bootstrapMinter{api: api, project: project, out: out}
	if dryRun {
		names := make([]string, 0, len(generate))
		for n := range generate {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			_, usable, exists, err := api.Latest(ctx, project, n)
			if err != nil {
				return fmt.Errorf("bootstrap secret %s: %w", n, err)
			}
			switch {
			case exists && usable:
				fmt.Fprintf(out, "bootstrap secret %s: present, would be left alone (dry run)\n", n)
			case exists:
				fmt.Fprintf(out, "bootstrap secret %s: would be minted (dry run)\n", n)
			default:
				fmt.Fprintf(out, "bootstrap secret %s: does not exist in project %s (dry run); the deploy would stop here\n", n, project)
			}
		}
		return nil
	}
	results, err := m.mintBootstrap(ctx, generate)
	m.report(results)
	if err != nil {
		return err
	}
	return nil
}
