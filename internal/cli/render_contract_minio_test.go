package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"

	"github.com/reliant-labs/forge/internal/hostinfra"
	"github.com/reliant-labs/forge/internal/secrets"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// The bucket-host-minio golden decodes into what the host-infra supervisor
// and the secret store need: the MinIO's buckets, CORS, console port and
// region, and the store KEYS of its root credentials, which the FileSecrets
// provider generates and the workload reads as managedSecret.
func TestRenderContract_HostMinIODecodes(t *testing.T) {
	e, _ := loadContract(t, "bucket-host-minio")
	if len(e.Infra) != 1 {
		t.Fatalf("infra = %+v", e.Infra)
	}
	hi := e.Infra[0]
	if hi.Engine != hostinfra.EngineMinIO || hi.Port != 9200 || hi.MinIOConsolePort != 9201 || hi.MinIORegion != "us-east-1" {
		t.Errorf("minio infra = %+v", hi)
	}
	if want := []HostInfraMinIOBucket{{Name: "uploads"}, {Name: "avatars", PublicRead: true}}; !slices.Equal(hi.MinIOBuckets, want) {
		t.Errorf("buckets = %+v, want %+v", hi.MinIOBuckets, want)
	}
	if hi.MinIOCORSAllowOrigin != "http://localhost:5173" {
		t.Errorf("cors = %q", hi.MinIOCORSAllowOrigin)
	}
	if hi.MinIORootUserSecret != "MINIO_ROOT_USER" || hi.MinIORootPasswordSecret != "MINIO_ROOT_PASSWORD" {
		t.Errorf("credential keys = %q / %q", hi.MinIORootUserSecret, hi.MinIORootPasswordSecret)
	}

	for _, k := range []string{"MINIO_ROOT_USER", "MINIO_ROOT_PASSWORD"} {
		g, ok := e.SecretProvider.Generate[k]
		if !ok || g.Encoding != "hex" {
			t.Errorf("FileSecrets does not generate %s as hex: %+v", k, e.SecretProvider.Generate)
		}
		if !slices.Contains(declaredSecretNames(e), k) || !slices.Contains(generatableSecretNames(e), k) {
			t.Errorf("%s is not a declared, generatable secret: `forge env up` would never mint it", k)
		}
	}

	env := map[string]string{}
	for _, v := range e.Workloads[0].Spec.Env {
		if v.ManagedSecret != nil {
			env[v.Name] = v.ManagedSecret.Name
		}
	}
	if env["UPLOADS_ACCESS_KEY_ID"] != "MINIO_ROOT_USER" || env["UPLOADS_SECRET_ACCESS_KEY"] != "MINIO_ROOT_PASSWORD" {
		t.Errorf("the workload does not read the generated credentials from the store: %v", env)
	}
}

// A MinIO with no bucket referenced by any workload still needs its
// credentials minted: the server reads them whether or not an app does.
func TestHostInfraSecretNamesAreDeclaredWithoutAReferrer(t *testing.T) {
	e := &KCLEntities{Infra: []HostInfraEntity{
		{Name: "postgres", Engine: "postgres"},
		{Name: "blobs", Engine: hostinfra.EngineMinIO, MinIORootUserSecret: "BLOBS_ROOT_USER", MinIORootPasswordSecret: "BLOBS_ROOT_PASSWORD"},
	}}
	got := declaredSecretNames(e)
	if !slices.Equal(got, []string{"BLOBS_ROOT_PASSWORD", "BLOBS_ROOT_USER"}) {
		t.Errorf("declared = %v", got)
	}
}

// B1 regression: a host MinIO whose buckets NO workload references still has
// its root credentials minted by `forge env up` (generateMissingSecrets, the
// FileSecrets generate step), because the server itself reads them.
func TestHostMinIOCredentialsAreGeneratedWithNoReferrer(t *testing.T) {
	e := &KCLEntities{
		Infra: []HostInfraEntity{{
			Name: "blobs", Engine: hostinfra.EngineMinIO, Port: 9200,
			MinIOBuckets:        []HostInfraMinIOBucket{{Name: "gtm-assets"}},
			MinIORootUserSecret: "BLOBS_ROOT_USER", MinIORootPasswordSecret: "BLOBS_ROOT_PASSWORD",
		}},
		SecretProvider: &SecretProviderEntity{Type: "file", Path: "secrets/dev.yaml", Generate: map[string]secrets.GenerateSpec{
			"BLOBS_ROOT_USER":     {Bytes: 16, Encoding: "hex"},
			"BLOBS_ROOT_PASSWORD": {Bytes: 32, Encoding: "hex"},
		}},
	}
	for _, k := range []string{"BLOBS_ROOT_USER", "BLOBS_ROOT_PASSWORD"} {
		if !slices.Contains(generatableSecretNames(e), k) {
			t.Errorf("%s is not generatable with no workload referencing the bucket: %v", k, generatableSecretNames(e))
		}
	}
	store := filepath.Join(t.TempDir(), "dev.yaml")
	var out bytes.Buffer
	generated, err := generateMissingSecrets(e, store, map[string]string{}, &out)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(generated)
	if !slices.Equal(generated, []string{"BLOBS_ROOT_PASSWORD", "BLOBS_ROOT_USER"}) {
		t.Fatalf("generated %v, want both MinIO root credentials", generated)
	}
	b, err := os.ReadFile(store) // #nosec G304 -- the test's own temp file
	if err != nil {
		t.Fatal(err)
	}
	for k, n := range map[string]int{"BLOBS_ROOT_USER": 32, "BLOBS_ROOT_PASSWORD": 64} {
		m := regexp.MustCompile(`(?m)^` + k + `: ([0-9a-f]+)$`).FindSubmatch(b)
		if m == nil || len(m[1]) != n {
			t.Errorf("%s is not a %d-hex value in the store", k, n)
			continue
		}
		if bytes.Contains(out.Bytes(), m[1]) {
			t.Errorf("generateMissingSecrets printed %s's value; it must print key names only", k)
		}
	}
}

// The dispatcher hands the supervisor the credential VALUES, looked up in the
// env's secret store under the keys the render named, plus the buckets.
func TestBuildDeployGroupsResolvesHostMinIOCredentialsFromTheStore(t *testing.T) {
	testkit.Slow(t, "execs git (the secret store's worktree anchor)")
	e, _ := loadContract(t, "bucket-host-minio")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte("name: acme\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "secrets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secrets", "dev.yaml"), []byte("MINIO_ROOT_USER: 0123abcd0123abcd\nMINIO_ROOT_PASSWORD: fedcba98fedcba98fedcba98\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	groups, err := buildDeployGroups("dev", e, "")
	if err != nil {
		t.Fatalf("buildDeployGroups: %v", err)
	}
	var found bool
	for _, g := range groups {
		for _, s := range g.Services {
			if s.HostInfra == nil || s.Name != "minio" {
				continue
			}
			found = true
			hi := s.HostInfra
			if hi.MinIORootUser != "0123abcd0123abcd" || hi.MinIORootPassword != "fedcba98fedcba98fedcba98" {
				t.Errorf("credentials not resolved from the store: user %q", hi.MinIORootUser)
			}
			if len(hi.MinIOBuckets) != 2 || !hi.MinIOBuckets[1].PublicRead || hi.MinIOCORSAllowOrigin != "http://localhost:5173" || hi.MinIOConsolePort != 9201 {
				t.Errorf("minio spec = %+v", hi)
			}
		}
	}
	if !found {
		t.Fatalf("no host-infra service for the MinIO in %+v", groups)
	}
}
