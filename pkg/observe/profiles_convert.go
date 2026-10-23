// File: profiles_convert.go — runtime/pprof bytes to OTLP profiles.
//
// PINNED TOGETHER. The OTLP profiles wire format is v1development and still
// changes between releases. The converter (contrib pkg/translator/pprof) and the
// pdata it emits (collector pdata/pprofile) are pinned to the SAME release,
// v0.162.0, and the local collector that receives them (docker-compose.yml's
// `otel-profiles`) runs contrib v0.162.0 too. Bump all three in one change,
// and say so in the PR title (the release script writes CHANGELOG.md from it). Wire compatibility from a newer producer to an
// older receiver was observed (0.162 into 0.155), but is not promised.
//
// What the translator leaves to us:
//
//   - It emits one OTLP profile per pprof sample type. Go's CPU profile carries
//     `samples/count` beside `cpu/nanoseconds`; the first only restates the
//     second, so it is dropped here and storage per CPU profile halves.
//   - It never sets the profile ID, which per-profile queries key on.
//   - pprof has no link concept. The `trace_id` / `span_id` labels that
//     DoWithProfileLabels puts on request goroutines become the OTLP link table
//     entries, so a sample can be joined to its span.

package observe

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"slices"

	"github.com/google/pprof/profile"
	pproftrans "github.com/open-telemetry/opentelemetry-collector-contrib/pkg/translator/pprof"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pprofile"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
)

const (
	profileLabelTraceID = "trace_id"
	profileLabelSpanID  = "span_id"

	// profilesScopeName names the producer on every exported profile.
	profilesScopeName = "github.com/reliant-labs/forge/pkg/observe/profiler"

	// redundantSampleType is the Go CPU profile's count-only twin of cpu/ns.
	redundantSampleType = "samples"
)

// pprofToOTLP converts one runtime/pprof profile (gzip or raw protobuf) into
// OTLP profiles carrying res as their resource. It returns an empty Profiles
// (SampleCount() == 0) for a profile that recorded nothing. Sample types named
// in drop are left out (the heap profile's types that are not selected); the
// last remaining type is never dropped.
func pprofToOTLP(r io.Reader, res *resource.Resource, drop ...string) (pprofile.Profiles, error) {
	p, err := profile.Parse(r)
	if err != nil {
		return pprofile.Profiles{}, fmt.Errorf("parse pprof: %w", err)
	}
	if len(p.Sample) == 0 {
		return pprofile.NewProfiles(), nil
	}
	dropSampleType(p, redundantSampleType)
	if len(drop) > 0 {
		for _, name := range drop {
			dropSampleType(p, name)
		}
		// A stack that is zero in every kept type would only become zero rows,
		// which the sparse filter removes anyway. Removing it here lets the
		// locations, functions and strings only it referenced go too: without
		// that, a live-heap snapshot still carries the dictionary of every
		// allocation site the process ever had.
		p.Sample = slices.DeleteFunc(p.Sample, func(s *profile.Sample) bool {
			return !slices.ContainsFunc(s.Value, func(v int64) bool { return v != 0 })
		})
		if len(p.Sample) == 0 {
			return pprofile.NewProfiles(), nil
		}
		pruneUnreferenced(p)
	}
	links := extractLinks(p)

	out, err := pproftrans.ConvertPprofToProfiles(p)
	if err != nil {
		return pprofile.Profiles{}, fmt.Errorf("convert pprof to OTLP profiles: %w", err)
	}
	pd := *out

	linkTable := pd.Dictionary().LinkTable()
	if linkTable.Len() == 0 {
		// Index 0 means "no link" in OTLP.
		linkTable.AppendEmpty()
	}
	linkIdx := map[linkRef]int32{}
	for i := 0; i < pd.ResourceProfiles().Len(); i++ {
		rp := pd.ResourceProfiles().At(i)
		if res != nil {
			putResourceAttributes(rp.Resource().Attributes(), res)
		}
		for j := 0; j < rp.ScopeProfiles().Len(); j++ {
			sp := rp.ScopeProfiles().At(j)
			sp.Scope().SetName(profilesScopeName)
			for k := 0; k < sp.Profiles().Len(); k++ {
				prof := sp.Profiles().At(k)
				prof.SetProfileID(newProfileID())
				// Attach links BEFORE sparsifying: the translator preserves pprof
				// sample order, but each sample type has different zero rows.
				if prof.Samples().Len() != len(links) {
					return pprofile.Profiles{}, fmt.Errorf("pprof translator changed sample order/length")
				}
				for s := 0; s < prof.Samples().Len(); s++ {
					ref := links[s]
					if ref == (linkRef{}) {
						continue
					}
					idx, seen := linkIdx[ref]
					if !seen {
						l := linkTable.AppendEmpty()
						l.SetTraceID(ref.trace)
						l.SetSpanID(ref.span)
						idx = int32(linkTable.Len() - 1)
						linkIdx[ref] = idx
					}
					prof.Samples().At(s).SetLinkIndex(idx)
				}
				// A zero contributes nothing to a sampled stack distribution.
				// Keep signed nonzero values (e.g. a legitimate differential
				// profile); don't mistake an inactive inuse stack for an
				// inactive alloc stack. Never subtract cumulative counters here.
				prof.Samples().RemoveIf(func(s pprofile.Sample) bool {
					for _, v := range s.Values().AsRaw() {
						if v != 0 {
							return false
						}
					}
					return true
				})
			}
		}
	}
	return pd, nil
}

// dropSampleType removes the named sample type from p, leaving at least one.
func dropSampleType(p *profile.Profile, name string) {
	if len(p.SampleType) < 2 {
		return
	}
	drop := -1
	for i, st := range p.SampleType {
		if st.Type == name {
			drop = i
			break
		}
	}
	if drop < 0 {
		return
	}
	p.SampleType = append(p.SampleType[:drop:drop], p.SampleType[drop+1:]...)
	for _, s := range p.Sample {
		s.Value = append(s.Value[:drop:drop], s.Value[drop+1:]...)
	}
	if p.DefaultSampleType == name {
		p.DefaultSampleType = ""
	}
}

// pruneUnreferenced removes the locations no sample uses, then the functions
// and mappings no remaining location uses. (profile.Compact would too, but on a
// runtime heap profile its output fails the OTLP translator with "numeric units
// with multiple values".)
func pruneUnreferenced(p *profile.Profile) {
	locs := map[*profile.Location]bool{}
	for _, s := range p.Sample {
		for _, l := range s.Location {
			locs[l] = true
		}
	}
	p.Location = slices.DeleteFunc(p.Location, func(l *profile.Location) bool { return !locs[l] })
	fns, maps := map[*profile.Function]bool{}, map[*profile.Mapping]bool{}
	for _, l := range p.Location {
		maps[l.Mapping] = true
		for _, ln := range l.Line {
			fns[ln.Function] = true
		}
	}
	p.Function = slices.DeleteFunc(p.Function, func(f *profile.Function) bool { return !fns[f] })
	p.Mapping = slices.DeleteFunc(p.Mapping, func(m *profile.Mapping) bool { return !maps[m] })
}

type linkRef struct {
	trace pcommon.TraceID
	span  pcommon.SpanID
}

// extractLinks lifts the trace_id / span_id labels off every sample and returns,
// per sample index, the link they name (the zero linkRef when a sample has none
// or a malformed one). It also flattens any other multi-valued label to its
// first value, because the translator rejects those outright.
func extractLinks(p *profile.Profile) []linkRef {
	links := make([]linkRef, len(p.Sample))
	for i, s := range p.Sample {
		tid, sid := first(s.Label[profileLabelTraceID]), first(s.Label[profileLabelSpanID])
		delete(s.Label, profileLabelTraceID)
		delete(s.Label, profileLabelSpanID)
		if len(s.Label) == 0 {
			s.Label = nil
		}
		for k, v := range s.Label {
			if len(v) > 1 {
				s.Label[k] = v[:1]
			}
		}
		for k, v := range s.NumLabel {
			if len(v) > 1 {
				s.NumLabel[k] = v[:1]
			}
			if u := s.NumUnit[k]; len(u) > 1 {
				s.NumUnit[k] = u[:1]
			}
		}
		links[i] = parseLink(tid, sid)
	}
	return links
}

func first(v []string) string {
	if len(v) == 0 {
		return ""
	}
	return v[0]
}

func parseLink(traceHex, spanHex string) linkRef {
	var ref linkRef
	if len(traceHex) != 32 || len(spanHex) != 16 {
		return linkRef{}
	}
	if _, err := hex.Decode(ref.trace[:], []byte(traceHex)); err != nil {
		return linkRef{}
	}
	if _, err := hex.Decode(ref.span[:], []byte(spanHex)); err != nil {
		return linkRef{}
	}
	if ref.trace.IsEmpty() || ref.span.IsEmpty() {
		return linkRef{}
	}
	return ref
}

func newProfileID() pprofile.ProfileID {
	var id pprofile.ProfileID
	_, _ = rand.Read(id[:]) // never fails since Go 1.24
	return id
}

func putResourceAttributes(dst pcommon.Map, res *resource.Resource) {
	for _, kv := range res.Attributes() {
		k := string(kv.Key)
		switch kv.Value.Type() {
		case attribute.BOOL:
			dst.PutBool(k, kv.Value.AsBool())
		case attribute.INT64:
			dst.PutInt(k, kv.Value.AsInt64())
		case attribute.FLOAT64:
			dst.PutDouble(k, kv.Value.AsFloat64())
		default:
			dst.PutStr(k, kv.Value.Emit())
		}
	}
}
