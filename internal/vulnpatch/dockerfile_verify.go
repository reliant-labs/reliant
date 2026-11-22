package vulnpatch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/vulnscan"
)

// ScanImageFunc scans an image in the local docker daemon for
// vulnerabilities (grype's docker:<image> source).
type ScanImageFunc func(ctx context.Context, image string) ([]vulnscan.Finding, error)

// The outcome of an image rebuild for one result. A result's Dockerfiles
// are each reported: the worst outcome wins, and the Dockerfiles that share
// it are all named.
type imageOutcome struct {
	status  Status
	reason  string
	rank    int
	reasons []string
}

const (
	rankFixed = iota + 1
	rankUnconfirmed
	rankNoDocker
	rankStill
	rankFailed
)

// verifyImages rebuilds every Dockerfile Apply changed — `docker build
// --pull --no-cache`, with the tree as the context, forge's default for a
// DockerBuild — rescans the image, and settles the results whose fix is a
// Dockerfile's: fixed, still_present, failed (the build broke), or skipped
// when there is no docker to verify with. It returns the results it
// settled; none when scan is nil (the results stay bumped).
func (p *Plan) verifyImages(ctx context.Context, run vulnscan.Runner, scan ScanImageFunc, v *VerifyResult) map[int]bool {
	type image struct {
		manifest, abs string
		results       []int
	}
	var images []*image
	byManifest := map[string]*image{}
	for _, b := range p.bumps {
		if b.eco != EcosystemDocker || b.done == nil || b.done.Error != "" {
			continue
		}
		im := byManifest[b.manifest]
		if im == nil {
			im = &image{manifest: b.manifest, abs: filepath.Join(b.dir, filepath.Base(b.manifest))}
			byManifest[b.manifest] = im
			images = append(images, im)
		}
		for _, r := range b.results {
			im.results = appendInt(im.results, r)
		}
	}
	if len(images) == 0 || scan == nil {
		return nil
	}
	outcomes := map[int]imageOutcome{}
	record := func(results []int, o imageOutcome) {
		for _, r := range results {
			have, ok := outcomes[r]
			switch {
			case !ok || o.rank > have.rank:
				o.reasons = nonEmpty(o.reason)
				outcomes[r] = o
			case o.rank == have.rank && o.reason != "":
				have.reasons = appendUnique(have.reasons, o.reason)
				outcomes[r] = have
			}
		}
	}
	docker := func(out *bytes.Buffer, args ...string) error {
		return run(ctx, vulnscan.Command{Path: "docker", Args: args, Dir: p.Dir, Stdout: out, Stderr: out})
	}
	var probe bytes.Buffer
	if err := docker(&probe, "version", "--format", "{{.Server.Version}}"); err != nil {
		for _, im := range images {
			record(im.results, imageOutcome{status: StatusSkipped, rank: rankNoDocker,
				reason: fmt.Sprintf("no docker to verify: %s was changed, and rebuilding and rescanning its image needs a docker daemon", im.manifest)})
		}
	} else {
		for _, im := range images {
			tag := verifyTag(p.Dir, im.manifest)
			var out bytes.Buffer
			if err := docker(&out, "build", "--pull", "--no-cache", "-f", im.abs, "-t", tag, p.Dir); err != nil {
				record(im.results, imageOutcome{status: StatusFailed, rank: rankFailed,
					reason: fmt.Sprintf("the rebuild of %s failed (docker build): %v\n%s", im.manifest, err, Tail(out.String(), 1000))})
				continue
			}
			v.Rebuilt = append(v.Rebuilt, im.manifest)
			found, err := scan(ctx, tag)
			_ = docker(&bytes.Buffer{}, "image", "rm", "-f", tag)
			if err != nil {
				msg := fmt.Sprintf("%s (image): %v", im.manifest, err)
				if v.RescanError != "" {
					msg = v.RescanError + "; " + msg
				}
				v.RescanError = msg
				record(im.results, imageOutcome{status: StatusBumped, rank: rankUnconfirmed, reason: "not confirmed: the rescan of the rebuilt " + im.manifest + " image failed"})
				continue
			}
			for _, i := range im.results {
				o := imageOutcome{status: StatusFixed, rank: rankFixed}
				r := &p.Results[i]
				for _, f := range found {
					if !namesAny(f, r) || !samePackage(f.Package, r.Package) {
						continue
					}
					if inImage(r, f.Package.Version) {
						o = imageOutcome{status: StatusStillPresent, rank: rankStill,
							reason: fmt.Sprintf("the rebuilt %s image still reports it, in %s %s", im.manifest, f.Package.Name, f.Package.Version)}
						break
					}
					p.Notes = appendUnique(p.Notes, fmt.Sprintf("%s: the rebuilt %s image also reports it in %s %s, another copy than the one bumped", r.ID, im.manifest, f.Package.Name, f.Package.Version))
				}
				record([]int{i}, o)
			}
		}
	}
	settled := map[int]bool{}
	for i, o := range outcomes {
		if r := &p.Results[i]; r.Status == StatusBumped {
			r.Status, r.Reason = o.status, strings.Join(o.reasons, "; ")
			settled[i] = true
		}
	}
	return settled
}

// inImage reports whether a rebuilt image that reports r's advisory on r's
// package at version got still ships the copy r targeted. An image's distro
// package database holds one copy of each OS package, so whatever version
// it is at, it is the one the refreshed base did not move far enough. A Go
// standard library is the copy r targeted at the installed release or at
// the target or later (bumpedCopy); a binary built with another Go is
// another copy.
func inImage(r *Result, got string) bool {
	if kind, _ := classify(r.Package); kind == kindOS {
		return true
	}
	return bumpedCopy(r, got, r.Target)
}

// verifyTag is the local tag a verification build of manifest gets: stable
// per tree and Dockerfile, and removed after the rescan.
func verifyTag(tree, manifest string) string {
	sum := sha256.Sum256([]byte(tree + "\x00" + manifest))
	return "forge-vuln-patch-verify:" + hex.EncodeToString(sum[:6])
}
