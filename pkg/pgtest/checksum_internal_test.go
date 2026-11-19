package pgtest

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"sync/atomic"
	"testing"
)

// checksumRepository is a fake Maven repository for the checksum tests:
// archive and sidecar are served with 200 when non-empty and 404 otherwise.
type checksumRepository struct {
	archive string
	sidecar string
}

type checksumRepositoryHits struct {
	url              string
	archive, sidecar *atomic.Int32
}

func (c checksumRepository) start(t *testing.T) checksumRepositoryHits {
	t.Helper()
	hits := checksumRepositoryHits{archive: &atomic.Int32{}, sidecar: &atomic.Int32{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, counter := c.archive, hits.archive
		if strings.HasSuffix(r.URL.Path, ".sha256") {
			body, counter = c.sidecar, hits.sidecar
		}
		counter.Add(1)
		if body == "" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	hits.url = srv.URL
	return hits
}

// TestFetchRelay_VerifiesTheArchive pins the fail-closed rule: the relay
// hands the library an archive only once its sha256 matches forge's pin or a
// published .sha256, and refuses it — naming why and where it looked —
// otherwise. Before this, an archive no repository published a checksum for
// was booted unchecked, which the mirror fallback made reachable: a mirror
// serving the archive with no sidecar was trusted outright.
func TestFetchRelay_VerifiesTheArchive(t *testing.T) {
	const good, evil = "the published archive", "some other bytes"
	goodSum, evilSum := sha256Hex([]byte(good)), sha256Hex([]byte(evil))
	archiveName := path.Base(relayArtifact)

	for _, tc := range []struct {
		name  string
		repos []checksumRepository
		pins  map[string]string
		// want* describe a served archive; wantRefused a refusal.
		wantBody    string
		wantRefused bool
		check       func(t *testing.T, hits []checksumRepositoryHits, ce *ChecksumError)
	}{
		{
			name:     "published checksum matches",
			repos:    []checksumRepository{{archive: good, sidecar: goodSum}},
			wantBody: good,
		},
		{
			name:        "published checksum does not match",
			repos:       []checksumRepository{{archive: good, sidecar: evilSum}},
			wantRefused: true,
			check: func(t *testing.T, hits []checksumRepositoryHits, ce *ChecksumError) {
				if ce.Want != evilSum || ce.Got != goodSum || ce.Source != hits[0].url+relayArtifact+".sha256" {
					t.Errorf("ChecksumError = %+v, want Want=%s Got=%s from the published sidecar", ce, evilSum, goodSum)
				}
				if !strings.Contains(ce.Error(), "corrupt") {
					t.Errorf("a mismatch does not say the download is corrupt: %v", ce)
				}
			},
		},
		{
			name:        "no checksum published anywhere",
			repos:       []checksumRepository{{archive: good}, {archive: good}},
			wantRefused: true,
			check: func(t *testing.T, hits []checksumRepositoryHits, ce *ChecksumError) {
				if ce.Want != "" || len(ce.Tried) != 2 {
					t.Errorf("ChecksumError = %+v, want no checksum and both sidecar URLs tried", ce)
				}
				for _, h := range hits {
					if !strings.Contains(ce.Error(), h.url+relayArtifact+".sha256: HTTP 404") {
						t.Errorf("refusal does not name sidecar %s:\n%v", h.url, ce)
					}
				}
				if got := hits[1].archive.Load(); got != 0 {
					t.Errorf("mirror's archive fetched %d time(s), though no checksum exists to verify it against", got)
				}
			},
		},
		{
			name:     "only the mirror serves the archive and its checksum",
			repos:    []checksumRepository{{}, {archive: good, sidecar: goodSum}},
			wantBody: good,
		},
		{
			name:     "corrupt copy on the primary, good copy on the mirror",
			repos:    []checksumRepository{{archive: evil, sidecar: goodSum}, {archive: good, sidecar: goodSum}},
			wantBody: good,
		},
		{
			name:     "pinned checksum, nothing published",
			repos:    []checksumRepository{{archive: good}},
			pins:     map[string]string{archiveName: goodSum},
			wantBody: good,
			check: func(t *testing.T, hits []checksumRepositoryHits, _ *ChecksumError) {
				if got := hits[0].sidecar.Load(); got != 0 {
					t.Errorf("sidecar asked %d time(s) for a pinned archive", got)
				}
			},
		},
		{
			name:        "pinned checksum overrides a published one",
			repos:       []checksumRepository{{archive: evil, sidecar: evilSum}},
			pins:        map[string]string{archiveName: goodSum},
			wantRefused: true,
			check: func(t *testing.T, _ []checksumRepositoryHits, ce *ChecksumError) {
				if ce.Source != pinnedChecksumSource || ce.Want != goodSum || ce.Got != evilSum {
					t.Errorf("ChecksumError = %+v, want the pin to win over the repository's sidecar", ce)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hits []checksumRepositoryHits
			var urls []string
			for _, repo := range tc.repos {
				h := repo.start(t)
				hits = append(hits, h)
				urls = append(urls, h.url)
			}
			withFetchPolicy(t, urls[0], 2)
			if tc.pins != nil {
				pinnedArchiveSHA256 = tc.pins
			}

			relay, status, body := relayGet(t, urls...)
			fe := relay.lastFailure()

			if !tc.wantRefused {
				if status != http.StatusOK || body != tc.wantBody {
					t.Fatalf("relay answered %d %q, want 200 %q (failure: %v)", status, body, tc.wantBody, fe)
				}
				if fe != nil {
					t.Errorf("a verified archive is recorded as a failure: %v", fe)
				}
				// The library's own check then sees the checksum the relay
				// verified against, without another trip to a repository.
				before := totalSidecarHits(hits)
				resp, err := http.Get(relay.baseURL() + relayArtifact + ".sha256")
				if err != nil {
					t.Fatal(err)
				}
				sidecar, _ := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				if string(sidecar) != sha256Hex([]byte(tc.wantBody)) {
					t.Errorf("relay's sidecar = %q, want the verified %s", sidecar, sha256Hex([]byte(tc.wantBody)))
				}
				if after := totalSidecarHits(hits); after != before {
					t.Errorf("the library's sidecar request went upstream (%d more hit(s))", after-before)
				}
			} else {
				if status != http.StatusBadGateway {
					t.Errorf("relay answered %d %q for an unverifiable archive, want 502", status, body)
				}
				if fe == nil {
					t.Fatal("an unverifiable archive is not recorded as a failure, so StartEmbedded would report the library's error")
				}
				var ce *ChecksumError
				if !errors.As(fe, &ce) {
					t.Fatalf("failure is not a *ChecksumError: %v", fe)
				}
				var fetchErr *FetchError
				if !errors.As(fe, &fetchErr) || fetchErr.Err != ce {
					t.Errorf("failure does not unwrap to the *FetchError holding the *ChecksumError: %v", fe)
				}
				if ce.Archive != hits[0].url+relayArtifact {
					t.Errorf("ChecksumError.Archive = %s, want %s", ce.Archive, hits[0].url+relayArtifact)
				}
				msg := fe.Error()
				for _, unwanted := range []string{"transient", "no version found", "no published binary"} {
					if strings.Contains(msg, unwanted) {
						t.Errorf("a checksum refusal reads as %q:\n%s", unwanted, msg)
					}
				}
				if !strings.Contains(msg, "refuses to boot") {
					t.Errorf("refusal does not say forge will not boot it:\n%s", msg)
				}
			}
			if tc.check != nil {
				var ce *ChecksumError
				if fe != nil {
					errors.As(fe, &ce)
				}
				tc.check(t, hits, ce)
			}
		})
	}
}

func totalSidecarHits(hits []checksumRepositoryHits) int32 {
	var n int32
	for _, h := range hits {
		n += h.sidecar.Load()
	}
	return n
}

// TestParseSHA256 reads the sidecar formats in the wild: the bare digest
// Maven Central publishes, and sha256sum's "digest  name".
func TestParseSHA256(t *testing.T) {
	const sum = "1974c3a59c3ae010e9818e81637e425aad94eaac26e4ef533b42e081fe7ca345"
	for in, want := range map[string]string{
		sum:                      sum,
		sum + "\n":               sum,
		strings.ToUpper(sum):     sum,
		sum + "  postgres.jar\n": sum,
		"":                       "",
		"<html>not found</html>": "",
		sum[:62]:                 "",
	} {
		got, ok := parseSHA256([]byte(in))
		if got != want || ok != (want != "") {
			t.Errorf("parseSHA256(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}

// pinnedPlatforms are the archive platforms embedded-postgres resolves to on
// the machines forge runs on (its defaultVersionStrategy).
var pinnedPlatforms = []string{
	"linux-amd64", "linux-arm64v8", "linux-amd64-alpine", "linux-arm64v8-alpine",
	"darwin-amd64", "darwin-arm64v8", "windows-amd64",
}

// TestPinnedChecksumsCoverSupportedMajors holds the pin table to
// supportedMajors: every release a major resolves to is pinned for every
// platform, and nothing else is. It fails when an embedded-postgres bump
// moves a major to a new release; pin it from Maven Central's sidecars:
//
//	curl -s https://repo1.maven.org/maven2/io/zonky/test/postgres/embedded-postgres-binaries-<platform>/<release>/embedded-postgres-binaries-<platform>-<release>.jar.sha256
func TestPinnedChecksumsCoverSupportedMajors(t *testing.T) {
	want := map[string]bool{}
	for _, release := range supportedMajors {
		for _, platform := range pinnedPlatforms {
			name := fmt.Sprintf("embedded-postgres-binaries-%s-%s.jar", platform, release)
			want[name] = true
			sum, ok := pinnedArchiveSHA256[name]
			if !ok {
				t.Errorf("no pinned checksum for %s", name)
				continue
			}
			if got, ok := parseSHA256([]byte(sum)); !ok || got != sum {
				t.Errorf("pinned checksum for %s is not a lowercase sha256: %q", name, sum)
			}
		}
	}
	for name := range pinnedArchiveSHA256 {
		if !want[name] {
			t.Errorf("pinned checksum for %s, which no supported major resolves to: drop it", name)
		}
	}
}
