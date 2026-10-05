// Package catalog embeds the curated integration manifests and resolves
// `uses: <integration>/<action>@<major>` references against them.
package catalog

import (
	"embed"
	"fmt"
	"regexp"
	"strconv"
	"sync"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/manifest"
)

//go:embed */manifest.yaml
var embedded embed.FS

// Action is a resolved action with the manifest it belongs to.
type Action struct {
	Manifest *reliantv1.IntegrationManifest
	Spec     *reliantv1.ActionSpec
}

// Catalog indexes manifests by id and version.
type Catalog struct {
	manifests []*reliantv1.IntegrationManifest
	byKey     map[string]*reliantv1.IntegrationManifest
}

var (
	once    sync.Once
	builtin *Catalog
	loadErr error
)

// Builtin returns the embedded catalog, loading it on first use.
func Builtin() (*Catalog, error) {
	once.Do(func() {
		var ms []*reliantv1.IntegrationManifest
		ms, loadErr = manifest.LoadFS(embedded, manifest.TrustCurated)
		if loadErr == nil {
			builtin = New(ms)
		}
	})
	return builtin, loadErr
}

// MustBuiltin is Builtin for init-time callers; a broken embedded manifest is
// a build defect (the load-all test catches it first).
func MustBuiltin() *Catalog {
	c, err := Builtin()
	if err != nil {
		panic(fmt.Sprintf("integrations catalog: %v", err))
	}
	return c
}

// New indexes the given manifests.
func New(ms []*reliantv1.IntegrationManifest) *Catalog {
	c := &Catalog{manifests: ms, byKey: map[string]*reliantv1.IntegrationManifest{}}
	for _, m := range ms {
		c.byKey[key(m.GetId(), m.GetVersion())] = m
	}
	return c
}

func key(id string, version int32) string { return id + "@" + strconv.Itoa(int(version)) }

// Manifests lists every manifest, sorted by id then version.
func (c *Catalog) Manifests() []*reliantv1.IntegrationManifest { return c.manifests }

// Manifest returns the manifest for id at a major version.
func (c *Catalog) Manifest(id string, version int32) (*reliantv1.IntegrationManifest, error) {
	m, ok := c.byKey[key(id, version)]
	if !ok {
		return nil, fmt.Errorf("unknown integration %s@%d", id, version)
	}
	return m, nil
}

var usesPattern = regexp.MustCompile(`^([a-z][a-z0-9_]*)/([a-z][a-z0-9_]*(?:\.[a-z][a-z0-9_]*)*)@([0-9]+)$`)

// ParseUses splits "<integration>/<action>@<major>".
func ParseUses(uses string) (integration, action string, version int32, err error) {
	m := usesPattern.FindStringSubmatch(uses)
	if m == nil {
		return "", "", 0, fmt.Errorf("uses %q must look like <integration>/<action>@<major>, e.g. http/request@1", uses)
	}
	v, convErr := strconv.ParseInt(m[3], 10, 32)
	if convErr != nil || v < 1 {
		return "", "", 0, fmt.Errorf("uses %q: major version must be >= 1", uses)
	}
	return m[1], m[2], int32(v), nil
}

// Resolve finds the action named by a uses reference.
func (c *Catalog) Resolve(uses string) (*Action, error) {
	integration, action, version, err := ParseUses(uses)
	if err != nil {
		return nil, err
	}
	m, ok := c.byKey[key(integration, version)]
	if !ok {
		return nil, fmt.Errorf("unknown integration %s@%d in uses %q", integration, version, uses)
	}
	for _, a := range m.GetActions() {
		if a.GetId() == action {
			return &Action{Manifest: m, Spec: a}, nil
		}
	}
	return nil, fmt.Errorf("integration %s@%d has no action %q", integration, version, action)
}
