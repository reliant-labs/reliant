package testcollect

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// The reset baseline: how `forge test collect --reset` starts a live
// process's counts from zero without asking the process to change anything.
//
// serverkit's /debug/coverage is read-only (a counter-clearing endpoint is a
// write no auth can protect on a debug listener a web page can reach), so a
// reset pulls each live process's counters as they stand and records them,
// per process, as its baseline (in the workload's reset record, reset.go).
// Every later collect subtracts the baseline from that process's counts,
// block by block, clamped at zero. A -covermode=atomic counter only ever
// grows while its process runs, so what is left is exactly what the process
// counted since the reset.
//
// One process can leave several snapshots of its counters (a pull, then the
// file it flushes at exit; a pull of a pod that later exited): each is
// cumulative since the process started, so they are never summed. A collect
// keeps, per process and block, the MAXIMUM count across its snapshots,
// which for monotonic counters is the newest snapshot's, whatever the clocks
// that named the files or timed the pulls say. It subtracts the process's
// baseline from that, then sums the processes. Counter files with no process
// identity (compose: a restarted container reuses pid 1) are summed as they
// are.
//
// A process is (scope, build, pid): the workload's host processes share the
// scope "host" (a live pull and the file the same process flushes at exit
// are one process, told apart from others by pid), and a cluster pod is its
// own scope, "pod:<ns>/<name>". The build is the covmeta hash.
//
// The baseline applies only to the process it was taken from. A counter file
// with no baseline (a process started after the reset: another pid, another
// pod, a rebuild) is counted whole. So is one whose counts fell below the
// baseline in any block: that is a new process that reused the pid (pid 1
// after a container restart), whose counters started again from zero.
//
// Counter files flushed before the reset are left out by the fence
// (fence.go), not by the baseline. Compose has no live pull, so its reset is
// the fence alone.

// CombinedProfile is the file a collect writes, beside a build's covdata,
// when it combined the build's counter files process by process (a baseline
// applied, or one process left several snapshots): a Go text profile, the
// lane's coverage input in place of the covdata, which is kept as pulled.
const CombinedProfile = "combined.out"

// baselineProcess is one process's counts at the reset.
type baselineProcess struct {
	Scope    string `json:"scope"`
	MetaHash string `json:"meta_hash"`
	PID      string `json:"pid"`
	// Profile is the process's counters at the reset, as `go tool covdata
	// textfmt` prints them.
	Profile string `json:"profile"`
}

type processKey struct{ scope, hash, pid string }

func (p baselineProcess) key() processKey { return processKey{p.Scope, p.MetaHash, p.PID} }

// Scopes a counter file's process is told apart in.
const hostScope = "host"

func podScope(namespace, pod string) string { return "pod:" + namespace + "/" + pod }

// counterPID is the pid in a counter file's name.
func counterPID(name string) string {
	parts := strings.Split(strings.TrimPrefix(name, counterPrefix), ".")
	if len(parts) != 3 {
		return ""
	}
	return parts[1]
}

// processProfile converts one counter file, alone with its build's meta file,
// to a text profile: one process's counts.
func processProfile(ctx context.Context, opts Options, hash string, meta []byte, c covFile) ([]byte, error) {
	dir, err := os.MkdirTemp("", "forge-collect-process-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := atomicfile.WriteFile(filepath.Join(dir, metaPrefix+hash), meta, 0o644); err != nil {
		return nil, err
	}
	if err := atomicfile.WriteFile(filepath.Join(dir, c.name), c.data, 0o644); err != nil {
		return nil, err
	}
	builds, err := opts.Convert(ctx, "", dir)
	if err != nil {
		return nil, err
	}
	for _, b := range builds {
		if b.MetaHash == hash {
			return b.Profile, nil
		}
	}
	return nil, fmt.Errorf("converting %s produced no profile of build %s", c.name, short(hash))
}

// takeBaselines converts each live counter file in files to the baseline of
// its process.
func takeBaselines(ctx context.Context, opts Options, scope string, files []covFile) ([]baselineProcess, error) {
	metas := map[string][]byte{}
	for _, f := range files {
		if f.meta {
			metas[f.hash] = f.data
		}
	}
	var out []baselineProcess
	for _, f := range files {
		if f.meta {
			continue
		}
		meta, ok := metas[f.hash]
		if !ok {
			return nil, fmt.Errorf("counter file %s came without its build's meta file", f.name)
		}
		prof, err := processProfile(ctx, opts, f.hash, meta, f)
		if err != nil {
			return nil, err
		}
		out = append(out, baselineProcess{Scope: scope, MetaHash: f.hash, PID: counterPID(f.name), Profile: string(prof)})
	}
	return out, nil
}

// profile is a parsed Go text profile: its mode and each block's statements
// and count, keyed by the block ("file:sl.sc,el.ec"), in the order read.
type profile struct {
	mode   string
	order  []string
	blocks map[string]profBlock
}

type profBlock struct {
	stmts int
	count int64
}

// parseProfile reads a Go text profile. A block repeated (textfmt over
// several counter files) is summed.
func parseProfile(data []byte) (profile, error) {
	p := profile{blocks: map[string]profBlock{}}
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if m, ok := strings.CutPrefix(line, "mode: "); ok && p.mode == "" {
			p.mode = m
			continue
		}
		f := strings.Fields(line)
		if len(f) != 3 {
			return profile{}, fmt.Errorf("profile line %d: %q is not <block> <statements> <count>", i+1, line)
		}
		stmts, err1 := strconv.Atoi(f[1])
		count, err2 := strconv.ParseInt(f[2], 10, 64)
		if err1 != nil || err2 != nil {
			return profile{}, fmt.Errorf("profile line %d: %q is not <block> <statements> <count>", i+1, line)
		}
		b, seen := p.blocks[f[0]]
		if !seen {
			p.order = append(p.order, f[0])
		}
		p.blocks[f[0]] = profBlock{stmts: stmts, count: b.count + count}
	}
	if p.mode == "" {
		return profile{}, errors.New("profile has no mode line")
	}
	return p, nil
}

func (p profile) bytes() []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "mode: %s\n", p.mode)
	for _, k := range p.order {
		fmt.Fprintf(&b, "%s %d %d\n", k, p.blocks[k].stmts, p.blocks[k].count)
	}
	return b.Bytes()
}

// below reports whether any block of base counted more than live did: live
// is then not the process base was taken from.
func below(live, base profile) bool {
	for k, b := range base.blocks {
		if live.blocks[k].count < b.count {
			return true
		}
	}
	return false
}

// subtract takes base's counts off p, block by block, clamped at zero.
func (p profile) subtract(base profile) {
	for k, b := range base.blocks {
		if cur, ok := p.blocks[k]; ok {
			cur.count = max(cur.count-b.count, 0)
			p.blocks[k] = cur
		}
	}
}

// add sums o's counts into p, block by block.
func (p *profile) add(o profile) { p.merge(o, func(a, b int64) int64 { return a + b }) }

// maxWith keeps, per block, the larger of p's and o's counts.
func (p *profile) maxWith(o profile) { p.merge(o, func(a, b int64) int64 { return max(a, b) }) }

func (p *profile) merge(o profile, f func(a, b int64) int64) {
	if p.blocks == nil {
		p.blocks = map[string]profBlock{}
	}
	if p.mode == "" {
		p.mode = o.mode
	}
	for _, k := range o.order {
		cur, seen := p.blocks[k]
		if !seen {
			p.order = append(p.order, k)
			cur.stmts = o.blocks[k].stmts
		}
		cur.count = f(cur.count, o.blocks[k].count)
		p.blocks[k] = cur
	}
}

// combined is a build's profile, combined process by process (the rule at
// the top of this file).
type combined struct {
	profile []byte
	// subtracted counts the processes whose baseline came off; restarted
	// names the processes whose baseline did not apply.
	subtracted int
	restarted  []string
}

// combineProcesses combines b's counter files process by process. It
// returns nil when there is nothing to combine: no baseline applies and no
// process left more than one snapshot, so the covdata as pulled is the
// build's coverage.
func combineProcesses(ctx context.Context, opts Options, b *build, bases map[processKey]baselineProcess) (*combined, error) {
	groups := map[processKey][]covFile{}
	var keys []processKey
	var loose []covFile
	need := false
	for _, c := range b.counters {
		if c.scope == "" {
			loose = append(loose, c)
			continue
		}
		k := processKey{c.scope, b.hash, counterPID(c.name)}
		if _, ok := groups[k]; !ok {
			keys = append(keys, k)
		}
		groups[k] = append(groups[k], c)
		_, based := bases[k]
		need = need || based || len(groups[k]) > 1
	}
	if !need {
		return nil, nil
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		return a.scope+"\x00"+a.pid < b.scope+"\x00"+b.pid
	})
	out := &combined{}
	var total profile
	for _, k := range keys {
		var proc profile
		for _, c := range groups[k] {
			p, err := parsedProcessProfile(ctx, opts, b, c)
			if err != nil {
				return nil, err
			}
			proc.maxWith(p)
		}
		if base, ok := bases[k]; ok {
			bp, err := parseProfile([]byte(base.Profile))
			if err != nil {
				return nil, fmt.Errorf("baseline of %s pid %s: %w", k.scope, k.pid, err)
			}
			if below(proc, bp) {
				out.restarted = append(out.restarted, k.scope+" pid "+k.pid)
			} else {
				proc.subtract(bp)
				out.subtracted++
			}
		}
		total.add(proc)
	}
	for _, c := range loose {
		p, err := parsedProcessProfile(ctx, opts, b, c)
		if err != nil {
			return nil, err
		}
		total.add(p)
	}
	out.profile = total.bytes()
	return out, nil
}

func parsedProcessProfile(ctx context.Context, opts Options, b *build, c covFile) (profile, error) {
	raw, err := processProfile(ctx, opts, b.hash, b.meta, c)
	if err != nil {
		return profile{}, err
	}
	return parseProfile(raw)
}
