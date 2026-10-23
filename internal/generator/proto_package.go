package generator

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/naming"
)

// DefaultServiceProtoPackagePattern is the proto package a scaffolded service
// gets when the project has said nothing — no `api.proto_package` in
// forge.yaml and no majority convention among its existing service protos.
// {service} is the snake_case service package (naming.ServicePackage).
const DefaultServiceProtoPackagePattern = "services." + config.ProtoPackageServicePlaceholder + ".v1"

// ProtoPackageSource names which rule chose a scaffolded service's proto
// package, so the CLI can say why — and where to change it.
type ProtoPackageSource string

const (
	// ProtoPackageConfigured means forge.yaml `api.proto_package` declared it.
	ProtoPackageConfigured ProtoPackageSource = "forge.yaml api.proto_package"
	// ProtoPackageInferred means a strict majority of the project's existing
	// service protos agree on it.
	ProtoPackageInferred ProtoPackageSource = "inferred from existing service protos"
	// ProtoPackageDefault means neither of the above: forge's own default.
	ProtoPackageDefault ProtoPackageSource = "default"
)

// ProtoPackageResolution is the package chosen for one service, and why.
type ProtoPackageResolution struct {
	// Package is the concrete proto package to declare, e.g.
	// "controlplane.v1" or "services.billing.v1".
	Package string
	// Source is the rule that chose Package.
	Source ProtoPackageSource
	// Votes and Total describe an inference: Votes of Total existing
	// service protos agree on the winning convention. Zero otherwise.
	Votes, Total int
	// Dissenters lists the existing service protos that declared something
	// else ("proto/services/project/v1/project.proto (reliant.v1)"), so an
	// outlier is visible rather than silently overruled.
	Dissenters []string
}

// Describe renders the resolution as one line for the scaffold output.
func (r ProtoPackageResolution) Describe() string {
	switch r.Source {
	case ProtoPackageInferred:
		s := fmt.Sprintf("proto package %s (%s: %d of %d agree", r.Package, r.Source, r.Votes, r.Total)
		if len(r.Dissenters) > 0 {
			s += "; differs: " + strings.Join(r.Dissenters, ", ")
		}
		return s + ")"
	case ProtoPackageConfigured:
		return fmt.Sprintf("proto package %s (%s)", r.Package, r.Source)
	default:
		return fmt.Sprintf("proto package %s (%s — set api.proto_package in forge.yaml to choose another)", r.Package, r.Source)
	}
}

// ResolveServiceProtoPackage decides the proto package for a service being
// scaffolded into the project at root. serviceName is the CLI/display form;
// it is normalized with naming.ServicePackage.
//
// Precedence:
//
//  1. forge.yaml `api.proto_package` — the explicit declaration always wins.
//     It is a fixed package ("platform.v1": every service shares it) or a
//     pattern carrying {service} ("acme.{service}.v1").
//  2. The project's existing service protos (proto/services/*/v1/*.proto).
//     Each one votes for the CONVENTION it follows: a package that embeds
//     its own service name ("acme.orders.v1" under services/orders/) votes
//     for the pattern "acme.{service}.v1"; anything else ("controlplane.v1")
//     votes for that literal shared package. A convention held by a strict
//     majority wins.
//  3. DefaultServiceProtoPackagePattern.
//
// Majority, not unanimity, is deliberate. A real project keeps deliberate
// outliers — control-plane declares twenty services in `controlplane.v1`
// beside one `reliant.v1` stub that mirrors another product's wire API —
// and requiring every proto to agree would hand such a project the fallback
// forever. The outliers are reported (Dissenters), never hidden. A tie is not
// a convention, so it falls back.
//
// The service being scaffolded does not vote: on `--force` its own stub is
// already on disk, and the package forge wrote last time must not decide the
// package forge writes now.
func ResolveServiceProtoPackage(root, serviceName string) ProtoPackageResolution {
	svcPkg := naming.ServicePackage(serviceName)

	if cfg, err := config.LoadProjectDir(root); err == nil {
		if setting := strings.TrimSpace(cfg.API.ProtoPackage); setting != "" {
			return ProtoPackageResolution{
				Package: expandProtoPackagePattern(setting, svcPkg),
				Source:  ProtoPackageConfigured,
			}
		}
	}

	if res, ok := inferServiceProtoPackage(root, svcPkg); ok {
		return res
	}
	return ProtoPackageResolution{
		Package: expandProtoPackagePattern(DefaultServiceProtoPackagePattern, svcPkg),
		Source:  ProtoPackageDefault,
	}
}

// serviceProtoVote is one existing service proto's declared package.
type serviceProtoVote struct {
	rel        string // project-relative path, for reporting
	pkg        string // the declared package
	convention string // the pattern it votes for
}

func inferServiceProtoPackage(root, targetSvcPkg string) (ProtoPackageResolution, bool) {
	votes := scanServiceProtoPackages(root, targetSvcPkg)
	if len(votes) == 0 {
		return ProtoPackageResolution{}, false
	}

	tally := map[string]int{}
	for _, v := range votes {
		tally[v.convention]++
	}
	winner, best := "", 0
	for conv, n := range tally {
		if n > best || (n == best && conv < winner) {
			winner, best = conv, n
		}
	}
	// Strict majority: more than half of the votes. A tie, or a plurality
	// among three conventions, is not a convention a new service can be
	// said to follow.
	if best*2 <= len(votes) {
		return ProtoPackageResolution{}, false
	}

	var dissent []string
	for _, v := range votes {
		if v.convention != winner {
			dissent = append(dissent, fmt.Sprintf("%s (%s)", v.rel, v.pkg))
		}
	}
	return ProtoPackageResolution{
		Package:    expandProtoPackagePattern(winner, targetSvcPkg),
		Source:     ProtoPackageInferred,
		Votes:      best,
		Total:      len(votes),
		Dissenters: dissent,
	}, true
}

// protoPackageLineRE matches a proto `package` statement at the start of a
// line. Comments and the syntax line precede it in every scaffolded file, so
// a line scan (rather than a descriptor compile) is enough — and it works
// before the first `buf generate`, which is exactly when `forge project new
// --service a --service b` scaffolds the second service.
var protoPackageLineRE = regexp.MustCompile(`^\s*package\s+([A-Za-z_][\w.]*)\s*;`)

// scanServiceProtoPackages reads the package of every
// proto/services/<svc>/v1/<svc>.proto except targetSvcPkg's own. A file with
// no package statement does not vote.
func scanServiceProtoPackages(root, targetSvcPkg string) []serviceProtoVote {
	matches, err := filepath.Glob(filepath.Join(root, "proto", "services", "*", "v1", "*.proto"))
	if err != nil {
		return nil
	}
	sort.Strings(matches)
	var out []serviceProtoVote
	for _, path := range matches {
		svcDir := filepath.Base(filepath.Dir(filepath.Dir(path)))
		if svcDir == targetSvcPkg {
			continue
		}
		// One vote per service: its canonical <svc>.proto. A service that
		// splits its messages into sibling files still declares one package.
		if filepath.Base(path) != svcDir+".proto" {
			continue
		}
		pkg := readProtoPackage(path)
		if pkg == "" {
			continue
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		out = append(out, serviceProtoVote{
			rel:        filepath.ToSlash(rel),
			pkg:        pkg,
			convention: protoPackageConvention(pkg, svcDir),
		})
	}
	return out
}

func readProtoPackage(path string) string {
	f, err := os.Open(path) //nolint:gosec // path comes from a glob under the project root
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if m := protoPackageLineRE.FindStringSubmatch(sc.Text()); m != nil {
			return m[1]
		}
	}
	return ""
}

// protoPackageConvention generalizes one service's package into the pattern
// it follows: a dot-segment equal to the service's own directory name
// becomes {service}. "acme.orders.v1" under services/orders/ is the pattern
// "acme.{service}.v1"; "controlplane.v1" under services/billing/ stays the
// literal shared package "controlplane.v1".
func protoPackageConvention(pkg, svcDir string) string {
	segs := strings.Split(pkg, ".")
	for i, s := range segs {
		if s == svcDir {
			segs[i] = config.ProtoPackageServicePlaceholder
		}
	}
	return strings.Join(segs, ".")
}

func expandProtoPackagePattern(pattern, svcPkg string) string {
	return strings.ReplaceAll(pattern, config.ProtoPackageServicePlaceholder, svcPkg)
}
