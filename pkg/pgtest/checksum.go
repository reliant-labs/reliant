package pgtest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
)

// Verifying the postgres archive before anything boots it.
//
// embedded-postgres checks a downloaded archive against its .sha256 sidecar
// only when the repository serves one: any non-200 for the sidecar and it
// unpacks and runs the archive unchecked (v1.34.0, remote_fetch.go). With
// the mirror fallback that is worse than it looks — when Maven Central 404s
// both files, the mirror's archive is "verified" against nothing at all.
//
// So the relay verifies, and fails closed: it hands the library an archive
// only once the archive's sha256 matches a known checksum, which is
//
//  1. forge's own pin (pinnedArchiveSHA256) when the archive is one of the
//     releases forge's supported majors resolve to — no repository is
//     trusted for it, so a mirror cannot substitute other bytes; else
//  2. the first .sha256 sidecar a repository serves, asked in the same
//     order as the archive (Maven Central first).
//
// When neither exists the fetch fails with a *ChecksumError naming every
// sidecar URL asked. An unverified binary is never booted.

// pinnedArchiveSHA256 is the sha256 of each archive forge's supported majors
// (supportedMajors) resolve to, keyed by the archive's file name, which
// carries both the platform and the release. Every value is the .sha256
// sidecar Maven Central publishes, checked identical on the Cloud Storage
// mirror, and for linux-amd64 18.3.0 against the archive itself (2026-11-19).
//
// A release with no entry here — an exact release a project declares, or a
// platform not listed — is verified against its published sidecar instead.
// TestPinnedChecksumsCoverSupportedMajors fails when an embedded-postgres
// bump moves a major to a release that has no pins yet.
var pinnedArchiveSHA256 = map[string]string{
	"embedded-postgres-binaries-linux-amd64-18.3.0.jar":          "1974c3a59c3ae010e9818e81637e425aad94eaac26e4ef533b42e081fe7ca345",
	"embedded-postgres-binaries-linux-arm64v8-18.3.0.jar":        "1fb555a6602c13a113ab1f0cc9224d599948e85e51ac5f001393605505eaf78e",
	"embedded-postgres-binaries-linux-amd64-alpine-18.3.0.jar":   "510fd314da39f1eeccef774a92428474168fedf55403342548e2adeaed66b483",
	"embedded-postgres-binaries-linux-arm64v8-alpine-18.3.0.jar": "e8204cd252eced653bacf680f7f0f3db2d71fb87186ffeaa97dddb4c9b9697d6",
	"embedded-postgres-binaries-darwin-amd64-18.3.0.jar":         "cb68c57232945f5e74db946e6077d70cec56bfed35959b21acc750cc80ca0977",
	"embedded-postgres-binaries-darwin-arm64v8-18.3.0.jar":       "a18ec9ec6a905e04f5a08a8f7870bddadd635feb54dee332a3daf80fada3cb1a",
	"embedded-postgres-binaries-windows-amd64-18.3.0.jar":        "e1e2bb484f775c0b70fbde39c69451a7a4925629a85d622f557fe6f60de37ba1",

	"embedded-postgres-binaries-linux-amd64-17.5.0.jar":          "646f3269075d1a23904127374e3cdbcef96beb32979fedee25d835877400fcbe",
	"embedded-postgres-binaries-linux-arm64v8-17.5.0.jar":        "e02f27ff50ff2d90b6541d54f3f108505aacd84ea53c2890e976a684bfea2752",
	"embedded-postgres-binaries-linux-amd64-alpine-17.5.0.jar":   "7122386c7f7a089568f05720ada63448093d4065abbc0a89e6dc660851a53e3c",
	"embedded-postgres-binaries-linux-arm64v8-alpine-17.5.0.jar": "f9cf1d22dd98bfdb63738846c330f2b9f18adc39c927b3a082d0ccbe51a0efa1",
	"embedded-postgres-binaries-darwin-amd64-17.5.0.jar":         "fb70f783928e77c3d6ef5ca8ed644f1ac502f740fcfc032a375c8347820f2bdd",
	"embedded-postgres-binaries-darwin-arm64v8-17.5.0.jar":       "e9d3398e10c2ec926395498b03e75ad1a24eeaed82895e756a7e173b202cf6de",
	"embedded-postgres-binaries-windows-amd64-17.5.0.jar":        "06ced3b3267c569ffb3f85544aac0075926dd81bf78bd5247abb1aa9fe994d6b",

	"embedded-postgres-binaries-linux-amd64-16.9.0.jar":          "d983cee24678490199e1684d6a93f49f84673696cb6f0db64e880a4cf233017b",
	"embedded-postgres-binaries-linux-arm64v8-16.9.0.jar":        "9d113402f70f4d134250916319ad83e87cba3796bd73a606c28dbabd326b4fa3",
	"embedded-postgres-binaries-linux-amd64-alpine-16.9.0.jar":   "2a82d0d9266d46976be463ecf76b89f72e614d867e26b7fe8cf038fe7e05f075",
	"embedded-postgres-binaries-linux-arm64v8-alpine-16.9.0.jar": "7dd53a332a46ea11b26863b61e6232c6ff02d31ee1ef2f423a2e18f93a6d5c18",
	"embedded-postgres-binaries-darwin-amd64-16.9.0.jar":         "0be7111599308e8a670ae72e9512a4b902dabe3e2e0cda0539a0447a5bdacb26",
	"embedded-postgres-binaries-darwin-arm64v8-16.9.0.jar":       "53b2672c602e16e4c94fb56b9aa68cc26a0bbb0df851f256f41a2cdbeccc9cb6",
	"embedded-postgres-binaries-windows-amd64-16.9.0.jar":        "f6c6499661eb7064d2be60833e0e53621e42a880309ae5b82aa0f93eaed02df2",

	"embedded-postgres-binaries-linux-amd64-15.13.0.jar":          "cc0177d8a22b85ed266f6204c0642fd5c24ab583d1ef221d4b60bce29d4fa4ef",
	"embedded-postgres-binaries-linux-arm64v8-15.13.0.jar":        "13793ea10007eede1205593c381d74d81b69bc16b6c8b7b937e9af0bb4444635",
	"embedded-postgres-binaries-linux-amd64-alpine-15.13.0.jar":   "73c825eba923c8faa8059a5f9af7d20c9800b7feba901110fdc3487400905a80",
	"embedded-postgres-binaries-linux-arm64v8-alpine-15.13.0.jar": "77b0c5ee4eefc15282b9ad5d9dfec17af48099c13b56f6d3121ac13dbb5d1e04",
	"embedded-postgres-binaries-darwin-amd64-15.13.0.jar":         "1befcdf1f4ef59aaac5efbff997de798984f7a03ab1fb6b32771fbfb21178dc5",
	"embedded-postgres-binaries-darwin-arm64v8-15.13.0.jar":       "e375dd68ba4d199801c88534ed907a72a99d22e1ca2ca563842d8f965c660d1d",
	"embedded-postgres-binaries-windows-amd64-15.13.0.jar":        "edac9787afc0dddae1b475c7ef8dc9b20e3b98e60dc41e957240ede980cc5a2c",

	"embedded-postgres-binaries-linux-amd64-14.18.0.jar":          "d08a4c4703faf3f78ed74af72f1794d32ad3798f7dedf46abd383f809a99262b",
	"embedded-postgres-binaries-linux-arm64v8-14.18.0.jar":        "859abe0ef1fa19936242801a6e357b056d297c0440582986953f82053dd99342",
	"embedded-postgres-binaries-linux-amd64-alpine-14.18.0.jar":   "c714033647d894b90f146aa24aad13d54a4851f76cdc3903e33807024614d449",
	"embedded-postgres-binaries-linux-arm64v8-alpine-14.18.0.jar": "000556c1720f9c542462940000d2e4a50f4dbbcf45b178764f4721bb70b4e2db",
	"embedded-postgres-binaries-darwin-amd64-14.18.0.jar":         "0e4764346ad227223e7ea0d679e508067e5809cf640a5146c1d3d701db90941c",
	"embedded-postgres-binaries-darwin-arm64v8-14.18.0.jar":       "ce44da1e0d64b3cc1cb1955a864af14edbf20f82c2fe40b5a96aba576dd77ef9",
	"embedded-postgres-binaries-windows-amd64-14.18.0.jar":        "5a68575adc899e0613197ae5ee9d3743f374b5e580dc994573013e5dee2db85c",
}

// pinnedChecksumSource is ChecksumError.Source for a pinned checksum.
const pinnedChecksumSource = "forge's pinned checksum"

// ChecksumError is a postgres binary archive forge downloaded and refused to
// hand to embedded-postgres because it could not be verified. It reaches the
// caller as the Err of a *FetchError, so errors.As finds either type.
type ChecksumError struct {
	// Archive is the URL the archive was downloaded from.
	Archive string
	// Want is the sha256 the archive must have, and Source where it came
	// from: a .sha256 URL or forge's pin. Both are "" when no checksum was
	// found anywhere.
	Want   string
	Source string
	// Got is the downloaded archive's sha256.
	Got string
	// Tried is each .sha256 URL asked and what it answered, when no
	// repository served a usable checksum.
	Tried []string
}

func (e *ChecksumError) Error() string {
	if e.Want == "" {
		return fmt.Sprintf("%s cannot be verified: no repository serves its sha256 (tried %s), "+
			"so forge refuses to boot it. Re-run when the repository serves the checksum, or pin it in "+
			"pkg/pgtest/checksum.go", e.Archive, strings.Join(e.Tried, "; "))
	}
	return fmt.Sprintf("%s has sha256 %s but %s is %s: the download is corrupt or not the published "+
		"archive, so forge refuses to boot it", e.Archive, e.Got, e.Source, e.Want)
}

// archiveChecksum is the sha256 the archive at urlPath (as the library asks
// for it, rooted at the repository) must have, and where that came from: the
// pin, else the first repository that serves a well-formed sidecar. On none
// it returns a *ChecksumError with Tried filled in.
func (r *fetchRelay) archiveChecksum(ctx context.Context, urlPath string) (want, source string, _ *ChecksumError) {
	if sum, ok := pinnedArchiveSHA256[path.Base(urlPath)]; ok {
		return sum, pinnedChecksumSource, nil
	}
	ce := &ChecksumError{}
	for _, repo := range r.repositories {
		target := repo + urlPath + ".sha256"
		resp, _, err := r.getWithRetry(ctx, target)
		if err != nil {
			ce.Tried = append(ce.Tried, target+": "+err.Error())
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
		_ = resp.Body.Close()
		switch {
		case resp.StatusCode != http.StatusOK:
			ce.Tried = append(ce.Tried, fmt.Sprintf("%s: HTTP %d %s", target, resp.StatusCode, http.StatusText(resp.StatusCode)))
		case err != nil:
			ce.Tried = append(ce.Tried, target+": "+err.Error())
		default:
			sum, ok := parseSHA256(body)
			if !ok {
				ce.Tried = append(ce.Tried, target+": not a sha256")
				continue
			}
			return sum, target, nil
		}
		if ctx.Err() != nil {
			break
		}
	}
	return "", "", ce
}

// parseSHA256 reads a .sha256 sidecar: the hex digest, optionally followed
// by a file name (sha256sum's format). It returns the digest in lowercase.
func parseSHA256(body []byte) (string, bool) {
	fields := strings.Fields(string(body))
	if len(fields) == 0 {
		return "", false
	}
	sum := strings.ToLower(fields[0])
	if b, err := hex.DecodeString(sum); err != nil || len(b) != sha256.Size {
		return "", false
	}
	return sum, true
}

// sha256Hex is the lowercase hex sha256 of b, the form the library compares.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
