//go:build integration

package templates_test

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// TestScaffoldedMinIOReconcilesAnonymousAccess brings up the GENERATED
// docker-compose.yml's `minio` the way `forge env up` does (the compose
// process env forge.minio_workload declares) with `uploads` public, stores an
// object, then restarts it with `uploads` declared private again. The data
// volume keeps the bucket and the object, and with them the anonymous policy
// MinIO stored: the start script must reconcile it, so the object that was
// anonymously readable is refused afterwards.
//
// Run: go test -tags integration -run TestScaffoldedMinIO -count=1 ./internal/templates/
func TestScaffoldedMinIOReconcilesAnonymousAccess(t *testing.T) {
	if testing.Short() {
		t.Skip("scaffolds a project and starts its MinIO compose service twice; runs in the integration lane")
	}
	// `docker compose version` answers without a daemon; `docker info` needs
	// one, and is bounded because a wedged daemon never answers it.
	probe, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if exec.CommandContext(probe, "docker", "info").Run() != nil || exec.Command("docker", "compose", "version").Run() != nil {
		t.Skip("no docker daemon (or no compose plugin) to start MinIO on")
	}

	root := scaffoldForRender(t, "shop", []string{"orders"}, "")
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	project := "forge-minio-acl-" + strconv.Itoa(os.Getpid())
	compose := func(public string, args ...string) ([]byte, error) {
		cmd := exec.Command("docker", append([]string{"compose", "-p", project, "-f", filepath.Join(root, "docker-compose.yml")}, args...)...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(),
			"MINIO_PORT="+strconv.Itoa(port),
			"MINIO_ROOT_USER=minioadmin",
			"MINIO_ROOT_PASSWORD=minioadmin",
			"MINIO_BUCKETS=uploads",
			"MINIO_PUBLIC_BUCKETS="+public,
		)
		return cmd.CombinedOutput()
	}
	t.Cleanup(func() { _, _ = compose("", "down", "--volumes", "--remove-orphans") })
	up := func(public string) {
		t.Helper()
		if out, err := compose(public, "up", "-d", "--force-recreate", "--wait", "--wait-timeout", "180", "minio"); err != nil {
			logs, _ := compose(public, "logs", "--tail", "40", "minio")
			t.Fatalf("docker compose up --wait minio (public %q): %v\n%s\n--- logs ---\n%s", public, err, out, logs)
		}
	}

	ctx := context.Background()
	objectURL := fmt.Sprintf("http://127.0.0.1:%d/uploads/a.txt", port)
	anonymousGET := func() int {
		t.Helper()
		resp, err := http.Get(objectURL)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	up("uploads")
	admin, err := minio.New(fmt.Sprintf("127.0.0.1:%d", port), &minio.Options{
		Creds:  credentials.NewStaticV4("minioadmin", "minioadmin", ""),
		Region: "us-east-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("stored while public")
	if _, err := admin.PutObject(ctx, "uploads", "a.txt", bytes.NewReader(body), int64(len(body)), minio.PutObjectOptions{ContentType: "text/plain"}); err != nil {
		t.Fatal(err)
	}
	if code := anonymousGET(); code != http.StatusOK {
		t.Fatalf("anonymous GET of a public_read bucket's object = %d, want 200", code)
	}

	up("")
	if _, err := admin.StatObject(ctx, "uploads", "a.txt", minio.StatObjectOptions{}); err != nil {
		t.Fatalf("the object did not survive the restart (the volume should keep it): %v", err)
	}
	if code := anonymousGET(); code != http.StatusForbidden {
		t.Fatalf("anonymous GET after the bucket was declared private again = %d, want 403", code)
	}
}
