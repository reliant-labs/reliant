package hostinfra

// MinIO as a HOST PROCESS: the dev object store behind forge.BucketOnMinIO
// {infra = ...}, supervised like every other supervised-binary engine
// (supervised.go). It is the same S3 API the compose `minio` service serves,
// so an app's forge/pkg/blobstore code does not know which one it talks to;
// a project whose dev env runs no container runtime gets its buckets anyway.
//
// # What is not obvious
//
//   - The release is a BARE executable, not an archive: MinIO publishes
//     `minio.<goos>-<goarch>.<release>` with a sha256sum beside it, so the
//     checksum covers the binary itself (binaryRelease.raw). Upstream stopped
//     publishing binaries after RELEASE.2025-09-07T16-13-09Z (later releases
//     are source only), which is why that is the pin. See MinIOVersion for
//     the security fix this pin predates and why it does not reach a dev
//     server, and for what replacing the pin will take.
//   - The root credentials are NOT in the declaration. They come from the
//     env's secret store (forge generates them on first `forge env up`), and
//     the server is handed them through its environment, never argv: the
//     process table is readable by every user on the machine. Every
//     inherited MINIO_* / _MINIO_* variable is dropped first, so a developer's shell
//     configuration for some other MinIO cannot reconfigure this one.
//   - The buckets are CONVERGED on every start, on a fresh server and on an
//     adopted one alike: created when missing (never deleted), and anonymous
//     access reconciled, download for a public_read bucket and none for the
//     rest. That is the compose service's start script, in Go, so a bucket
//     declared private again closes even though the data kept its policy.
//   - CORS is server-wide (MINIO_API_CORS_ALLOW_ORIGIN), set at launch. A
//     running server cannot take a new value, so a declaration that changed
//     since launch is reported as drift, with the restart that applies it.
//   - Readiness is MinIO's own unauthenticated /minio/health/ready.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// EngineMinIO matches the `engine` discriminator on the KCL HostInfra schema.
const EngineMinIO = "minio"

// MinIOVersion is the pinned MinIO server release: the last one upstream
// published binaries for.
//
// It PREDATES a security fix. RELEASE.2025-10-15T17-29-55Z fixes
// CVE-2025-62506 (GHSA-jjjj-jwhf-8rgr, high): a service account or STS
// account with a restricted session policy could create a new service
// account for itself that drops the restriction. That needs a restricted
// non-root identity to start from, and a forge dev MinIO has none: the only
// identity is the generated root user, which already holds every
// permission, no workload is handed anything else, and nothing here creates
// service accounts or STS sessions. The server binds 127.0.0.1 and holds a
// developer's test files. So the fix changes nothing this server can do.
//
// The pin can never take a fix, though: upstream publishes source only from
// that release on, so there is no later checksummed binary to move to. The
// next MinIO advisory that DOES reach a root-only loopback server, or an OS
// the pinned build stops running on, means replacing the source: a build
// forge publishes from a tagged MinIO source tree, or another distribution
// with checksummed binaries. Not a newer pin from github.com/minio/minio.
const MinIOVersion = "RELEASE.2025-09-07T16-13-09Z"

// minioDefaultRegion is the region a declaration that names none answers as,
// the same default forge.BucketOnMinIO has.
const minioDefaultRegion = "us-east-1"

// minioChecksums is each release binary's sha256, copied from the
// `<asset>.sha256sum` files the release publishes beside each one:
//
//	https://github.com/minio/minio/releases/tag/RELEASE.2025-09-07T16-13-09Z
//
// There is no windows-arm64 build.
var minioChecksums = map[string]string{
	"minio.darwin-amd64.RELEASE.2025-09-07T16-13-09Z":      "4759080aeef7385aaceaac1131c30aaeb99605921553967dc6d3ef4e16ac64f9",
	"minio.darwin-arm64.RELEASE.2025-09-07T16-13-09Z":      "7c3b3039b76e55a1b80935848ed83998d5e8d317374f87851f46a019ff5c0aa4",
	"minio.linux-amd64.RELEASE.2025-09-07T16-13-09Z":       "7c5bd8512c6e966455b1d198209358b2d191c77a83ab377c4073281065fb855f",
	"minio.linux-arm64.RELEASE.2025-09-07T16-13-09Z":       "5c83cd2cf151717ba0243f73e1c7802ff36e272b67144bdd7f1f7d684fd6f03d",
	"minio.windows-amd64.RELEASE.2025-09-07T16-13-09Z.exe": "af709e6ba68488404e85acdd22a3030d0f5e56a108d4b27d744f18ceb50861b4",
}

var minioRelease = binaryRelease{
	engine:    EngineMinIO,
	version:   MinIOVersion,
	checksums: minioChecksums,
	asset: func(goos, goarch string) string {
		name := fmt.Sprintf("minio.%s-%s.%s", goos, goarch, MinIOVersion)
		if goos == "windows" {
			name += ".exe"
		}
		return name
	},
	url: func(asset string) string {
		return fmt.Sprintf("https://github.com/minio/minio/releases/download/%s/%s", MinIOVersion, asset)
	},
	binary: func(goos string) string {
		if goos == "windows" {
			return "minio.exe"
		}
		return "minio"
	},
	versionArgs:   []string{"--version"},
	versionNeedle: "minio version " + MinIOVersion,
	approxSize:    "~110 MB",
	raw:           true,
}

var minioEngine = supervisedEngine{
	release:      minioRelease,
	args:         minioArgs,
	env:          minioEnv,
	ready:        func(ctx context.Context, spec Spec, _ string) bool { return minioReady(ctx, spec.Port) },
	converge:     convergeMinIOBuckets,
	launched:     writeMinIOLaunch,
	drift:        minioDrift,
	readyTimeout: time.Minute,
}

// MinIOBucket is one bucket the env declares on a host MinIO.
type MinIOBucket struct {
	Name       string
	PublicRead bool
}

// MinIOEndpoint is the S3 API's address, the one the env's workloads and
// browsers dial.
func MinIOEndpoint(spec Spec) string { return "http://localhost:" + strconv.Itoa(spec.Port) }

// printMinIOAddresses prints where the S3 API and, when declared, the web
// console answer.
func printMinIOAddresses(spec Spec) {
	if spec.MinIOConsolePort > 0 {
		fmt.Printf("  %s: S3 API at %s, console at http://localhost:%d\n", spec.Name, MinIOEndpoint(spec), spec.MinIOConsolePort)
		return
	}
	fmt.Printf("  %s: S3 API at %s\n", spec.Name, MinIOEndpoint(spec))
}

// minioRegion is the region the instance answers as.
func minioRegion(spec Spec) string {
	if spec.MinIORegion != "" {
		return spec.MinIORegion
	}
	return minioDefaultRegion
}

// minioCORS is the S3 API's allowed browser origins.
func minioCORS(spec Spec) string {
	if spec.MinIOCORSAllowOrigin != "" {
		return spec.MinIOCORSAllowOrigin
	}
	return "*"
}

// validateMinIO refuses a declaration the server could not start with. The
// credentials are checked here, by name, because a missing one is a store
// that has not been filled yet, and MinIO's own message for it ("Unable to
// validate credentials") names neither the store nor the fix.
func validateMinIO(spec Spec) error {
	if spec.MinIORootUser == "" || spec.MinIORootPassword == "" {
		return fmt.Errorf("host-infra %s: MinIO's root credentials are missing from the env's secret store\n"+
			"  forge generates them on `forge env up`; to mint them by hand: `forge secret ensure --env %s`", spec.Name, envOrPlaceholder(spec.Env))
	}
	if len(spec.MinIORootUser) < 3 || len(spec.MinIORootPassword) < 8 {
		return fmt.Errorf("host-infra %s: MinIO needs a root user of at least 3 characters and a root password of at least 8; the env's secret store holds shorter ones", spec.Name)
	}
	if spec.MinIOConsolePort != 0 && spec.MinIOConsolePort == spec.Port {
		return fmt.Errorf("host-infra %s: the console port %d is the S3 API's port", spec.Name, spec.Port)
	}
	return nil
}

func envOrPlaceholder(env string) string {
	if env == "" {
		return "<env>"
	}
	return env
}

// minioArgs is the server's argv: the S3 API on the declared loopback port,
// the console beside it only when one is declared, objects under the data
// directory. No credential is ever an argument (see minioEnv). The config
// and certs directories are named so the server keeps them beside its data:
// left to their defaults, it creates ~/.minio in the developer's home.
func minioArgs(spec Spec, dataDir string) []string {
	args := []string{
		"server", filepath.Join(dataDir, "data"),
		"--address", "127.0.0.1:" + strconv.Itoa(spec.Port),
		"--config-dir", filepath.Join(dataDir, "config"),
		"--certs-dir", filepath.Join(dataDir, "certs"),
	}
	if spec.MinIOConsolePort > 0 {
		args = append(args, "--console-address", "127.0.0.1:"+strconv.Itoa(spec.MinIOConsolePort))
	}
	return args
}

// minioEnv is the server's environment: the inherited one without any
// variable the server reads as its own configuration (minioOwnedVar), then
// the root credentials, the region, the CORS origins, and the update check
// off (a dev loop does not reach the network to start).
func minioEnv(spec Spec, inherited []string) []string {
	return minioEnvFor(runtime.GOOS, spec, inherited)
}

func minioEnvFor(goos string, spec Spec, inherited []string) []string {
	env := make([]string, 0, len(inherited)+6)
	for _, kv := range inherited {
		if !minioOwnedVar(goos, kv) {
			env = append(env, kv)
		}
	}
	env = append(env,
		"MINIO_ROOT_USER="+spec.MinIORootUser,
		"MINIO_ROOT_PASSWORD="+spec.MinIORootPassword,
		"MINIO_SITE_REGION="+minioRegion(spec),
		"MINIO_API_CORS_ALLOW_ORIGIN="+minioCORS(spec),
		"MINIO_UPDATE=off",
	)
	if spec.MinIOConsolePort == 0 {
		env = append(env, "MINIO_BROWSER=off")
	}
	return env
}

// minioOwnedVar reports whether an inherited KEY=value is configuration the
// MinIO server reads: MINIO_* and the _MINIO_* internals it also honours.
// Windows environment names are case-insensitive, so there `minio_root_user`
// is the same variable as MINIO_ROOT_USER and is dropped too.
func minioOwnedVar(goos, kv string) bool {
	key, _, _ := strings.Cut(kv, "=")
	if goos == "windows" {
		key = strings.ToUpper(key)
	}
	return strings.HasPrefix(key, "MINIO_") || strings.HasPrefix(key, "_MINIO_")
}

// minioReady performs one readiness probe: MinIO's own readiness endpoint.
func minioReady(ctx context.Context, port int) bool {
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(port)+"/minio/health/ready", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// minioClient is an S3 client of the instance, signed with its root
// credentials.
func minioClient(spec Spec) (*minio.Client, error) {
	return minio.New("127.0.0.1:"+strconv.Itoa(spec.Port), &minio.Options{
		Creds:        credentials.NewStaticV4(spec.MinIORootUser, spec.MinIORootPassword, ""),
		Region:       minioRegion(spec),
		BucketLookup: minio.BucketLookupPath,
	})
}

// convergeMinIOBuckets creates every declared bucket that does not exist and
// reconciles each one's anonymous access. It never deletes a bucket: removing
// a declaration says nothing about whether the files in it should exist.
func convergeMinIOBuckets(ctx context.Context, spec Spec) error {
	if len(spec.MinIOBuckets) == 0 {
		return nil
	}
	client, err := minioClient(spec)
	if err != nil {
		return fmt.Errorf("minio client: %w", err)
	}
	for _, b := range spec.MinIOBuckets {
		exists, err := client.BucketExists(ctx, b.Name)
		if err != nil {
			return minioConvergeError(spec, b.Name, "check", err)
		}
		if !exists {
			if err := client.MakeBucket(ctx, b.Name, minio.MakeBucketOptions{Region: minioRegion(spec)}); err != nil {
				resp := minio.ToErrorResponse(err)
				if resp.Code != "BucketAlreadyOwnedByYou" && resp.Code != "BucketAlreadyExists" {
					return minioConvergeError(spec, b.Name, "create", err)
				}
			}
		}
		policy := ""
		if b.PublicRead {
			policy = minioPublicReadPolicy(b.Name)
		}
		if err := client.SetBucketPolicy(ctx, b.Name, policy); err != nil {
			return minioConvergeError(spec, b.Name, "set anonymous access on", err)
		}
	}
	return nil
}

// minioConvergeError names a failed bucket operation, and the one cause a
// developer can fix: a server started with other root credentials than the
// store now holds (rotated by `forge secret set`, or the store replaced).
func minioConvergeError(spec Spec, bucket, op string, err error) error {
	switch minio.ToErrorResponse(err).Code {
	case "InvalidAccessKeyId", "SignatureDoesNotMatch", "AccessDenied":
		return fmt.Errorf("%s bucket %q: %w\n"+
			"  the running MinIO does not accept the root credentials in the env's secret store; it was started with others.\n"+
			"  Fix — restart it so it takes the store's: `forge env down`, then `forge env up`", op, bucket, err)
	}
	return fmt.Errorf("%s bucket %q: %w", op, bucket, err)
}

// minioPublicReadPolicy is the anonymous policy of a public_read bucket: the
// one `mc anonymous set download` writes (the compose service's), so a
// bucket behaves the same on either MinIO.
func minioPublicReadPolicy(bucket string) string {
	type statement struct {
		Effect    string              `json:"Effect"`
		Principal map[string][]string `json:"Principal"`
		Action    []string            `json:"Action"`
		Resource  []string            `json:"Resource"`
	}
	anyone := map[string][]string{"AWS": {"*"}}
	doc := struct {
		Version   string      `json:"Version"`
		Statement []statement `json:"Statement"`
	}{
		Version: "2012-10-17",
		Statement: []statement{
			{Effect: "Allow", Principal: anyone, Action: []string{"s3:GetBucketLocation", "s3:ListBucket"}, Resource: []string{"arn:aws:s3:::" + bucket}},
			{Effect: "Allow", Principal: anyone, Action: []string{"s3:GetObject"}, Resource: []string{"arn:aws:s3:::" + bucket + "/*"}},
		},
	}
	b, _ := json.Marshal(doc) // a fixed shape of strings cannot fail to marshal
	return string(b)
}

// minioLaunch is what an instance was launched with that it cannot change
// while running, recorded beside its pidfile for minioDrift.
type minioLaunch struct {
	CORSAllowOrigin string `json:"cors_allow_origin"`
	Region          string `json:"region"`
	ConsolePort     int    `json:"console_port"`
}

func minioLaunchFile(dataDir string) string { return filepath.Join(dataDir, "minio.launch.json") }

func minioLaunchOf(spec Spec) minioLaunch {
	return minioLaunch{CORSAllowOrigin: minioCORS(spec), Region: minioRegion(spec), ConsolePort: spec.MinIOConsolePort}
}

// writeMinIOLaunch records the launch-time configuration. No credential is
// in it.
func writeMinIOLaunch(spec Spec, dataDir string) error {
	b, err := json.Marshal(minioLaunchOf(spec))
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(minioLaunchFile(dataDir), append(b, '\n'), 0o644) // #nosec G306 -- launch flags, no secret
}

// minioDrift names what the running instance was launched with that the
// declaration no longer says. "" when they agree, or when there is no record
// to compare (an instance forge did not record is not reported on a guess).
func minioDrift(spec Spec, dataDir string) string {
	b, err := os.ReadFile(minioLaunchFile(dataDir)) // #nosec G304 -- path derived from the project's own data dir
	if err != nil {
		return ""
	}
	var was minioLaunch
	if json.Unmarshal(b, &was) != nil {
		return ""
	}
	now := minioLaunchOf(spec)
	var changed []string
	if was.CORSAllowOrigin != now.CORSAllowOrigin {
		changed = append(changed, fmt.Sprintf("CORS origins %q (running: %q)", now.CORSAllowOrigin, was.CORSAllowOrigin))
	}
	if was.Region != now.Region {
		changed = append(changed, fmt.Sprintf("region %q (running: %q)", now.Region, was.Region))
	}
	if was.ConsolePort != now.ConsolePort {
		changed = append(changed, fmt.Sprintf("console port %d (running: %d)", now.ConsolePort, was.ConsolePort))
	}
	if len(changed) == 0 {
		return ""
	}
	return "the declaration now asks for " + strings.Join(changed, ", ") +
		", which a running MinIO cannot take; restart it: `forge env down`, then `forge env up`"
}
