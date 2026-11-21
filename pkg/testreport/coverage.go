package testreport

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"strings"
)

// Coverage is the forge.test.coverage/v1 document: files, their statement
// blocks, and each block's hit count per lane.
//
// One parsed input is a Coverage with one lane and one entry in Inputs;
// [Merge] unions any number of them (and a previous merge) into one. A
// block's Hits are aligned with Lanes: Hits[i] is the count under Lanes[i].
//
// No source is stored, ever. Each file carries its repo-relative path and git
// blob id, which pin the exact bytes the counts describe; a viewer fetches the
// source by blob id, and draws nothing over source that does not match it.
type Coverage struct {
	Head
	// Commit, Tree and Dirty describe the source the counts were taken on.
	// Tree is the tree hash inputs must agree on to merge ([Merge]).
	Commit string `json:"commit,omitempty"`
	Tree   string `json:"tree,omitempty"`
	Dirty  bool   `json:"dirty,omitempty"`
	// Lanes are the lanes the document has counts for, sorted. Every
	// block's Hits has one entry per lane, in this order.
	Lanes []LaneInfo `json:"lanes"`
	// Inputs are the raw inputs merged in, by digest. Merging an input
	// whose digest is already here changes nothing.
	Inputs []InputRef `json:"inputs"`
	// Builds maps a Go package's import path to a fingerprint of the source
	// it was instrumented from, for callers that have a true per-package
	// fingerprint (opt-in). Two different fingerprints for one package never
	// merge ([BuildMismatchError]). A binary's covmeta hash is NOT such a
	// fingerprint: two binaries built from one tree have different ones.
	Builds map[string]string `json:"builds,omitempty"`
	// Files are the covered files, sorted by language then path.
	Files []CoverageFile `json:"files"`
	// Changed are the executable-line candidates of a diff: the line ranges
	// changed between the base and this commit, which [Coverage.Diff] reads.
	// The producer computes them (the server has no checkout).
	Changed []ChangedFile `json:"changed,omitempty"`
}

// LaneInfo is one lane of a coverage document.
type LaneInfo struct {
	Name Lane `json:"name"`
	// Mode is "count" (hit counts, summed across inputs) or "set" (0 or 1).
	// A lane any `mode: set` profile joined is a set lane: its hits are
	// clamped to 0 or 1, because a count cannot be summed with a boolean.
	Mode Mode `json:"mode"`
}

// Mode is how a lane's hits read.
type Mode string

// The two lane modes. Go's `count` and `atomic` profiles both merge as
// ModeCount.
const (
	ModeCount Mode = "count"
	ModeSet   Mode = "set"
)

// InputRef records one raw input merged into a document.
type InputRef struct {
	// Digest is "sha256:<hex>" of the raw input bytes.
	Digest string `json:"digest"`
	Lane   Lane   `json:"lane"`
	Format Format `json:"format"`
	// Mode is the input's own mode: Go's "set", "count" or "atomic", or
	// "count" for formats that carry hit counts.
	Mode string `json:"mode,omitempty"`
}

// Granularity is what a block stands for.
type Granularity string

// The two granularities. Go profiles and istanbul are statement-granular.
// LCOV and Cobertura record lines only, so each line is one block of one
// statement and the file says so.
const (
	GranularityStatement Granularity = "statement"
	GranularityLine      Granularity = "line"
)

// CoverageFile is one covered source file.
type CoverageFile struct {
	Lang Lang `json:"lang"`
	// ImportPath is how a Go profile names the file: the package import
	// path and the file name, "github.com/acme/shop/internal/billing/refund.go".
	// Empty for other languages.
	ImportPath string `json:"import_path,omitempty"`
	// Path is the file's repo-relative path, "internal/billing/refund.go".
	// For Go it is derived when the module path is known ([MergeOptions]);
	// otherwise the producer fills it.
	Path string `json:"path,omitempty"`
	// BlobID is the git blob id of the file the counts were taken on
	// ([GitBlobID]). The producer fills it; it is what a viewer fetches the
	// source by.
	BlobID string `json:"blob_id,omitempty"`
	// Package is the Go package import path, or the file's directory for
	// other languages: what per-package rollups group by.
	Package string `json:"package"`
	// Granularity is what the file's blocks stand for.
	Granularity Granularity `json:"granularity"`
	// Excluded says why the file does not count toward percentages:
	// "generated", or "filtered" by an include/exclude glob. Excluded files
	// are kept, and reported on their own line.
	Excluded string `json:"excluded,omitempty"`
	// Blocks are the file's blocks, sorted by position.
	Blocks []Block `json:"blocks"`
}

// Why a file is excluded.
const (
	ExcludedGenerated = "generated"
	ExcludedFiltered  = "filtered"
)

// Key is the file's identity across inputs: language plus its repo-relative
// path when known, else its import path. [Merge] derives a Go file's path
// from its import path first (MergeOptions.ModulePath), so a Go profile
// (named by import path) and an LCOV or Cobertura report of the same file
// (named by path) land on one file.
func (f CoverageFile) Key() string {
	if f.Path != "" {
		return string(f.Lang) + ":" + f.Path
	}
	return string(f.Lang) + ":" + f.ImportPath
}

// DisplayPath is Path when known, else ImportPath.
func (f CoverageFile) DisplayPath() string {
	if f.Path != "" {
		return f.Path
	}
	return f.ImportPath
}

// Block is one statement block (or, at line granularity, one line). Its key
// is its position: the same block has the same position in a unit profile,
// an integration profile and covdata converted by textfmt, so the union of
// inputs is exact.
type Block struct {
	StartLine int `json:"start_line"`
	StartCol  int `json:"start_col"`
	EndLine   int `json:"end_line"`
	EndCol    int `json:"end_col"`
	// Statements is the number of statements in the block (1 for a line).
	Statements int `json:"statements"`
	// Hits is the block's count per lane, aligned with [Coverage.Lanes].
	Hits []int64 `json:"hits"`
}

// Covered reports whether any lane hit the block.
func (b Block) Covered() bool {
	for _, h := range b.Hits {
		if h > 0 {
			return true
		}
	}
	return false
}

type blockKey struct{ sl, sc, el, ec int }

func (b Block) key() blockKey { return blockKey{b.StartLine, b.StartCol, b.EndLine, b.EndCol} }

// ChangedFile is one file's changed line ranges in a diff.
type ChangedFile struct {
	// Path is repo-relative.
	Path   string      `json:"path"`
	Ranges []LineRange `json:"ranges"`
}

// LineRange is an inclusive range of 1-based lines.
type LineRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// CoverageOptions configures [ParseCoverage].
type CoverageOptions struct {
	// Format is the input's format; empty detects it ([DetectFormat]).
	Format Format
	// Lane is the lane the counts belong to. Required.
	Lane Lane
	// Root is the directory absolute file paths are made relative to (the
	// repository root). LCOV, Cobertura and istanbul usually name files by
	// absolute path. Empty keeps them as written.
	Root string
	// Lang overrides the per-file language, which is otherwise
	// [LangFromPath] (Go profiles are always Go).
	Lang Lang
	// Tree, Commit and Dirty are stamped on the document.
	Tree   string
	Commit string
	Dirty  bool
	// BuildFingerprint, when set, is recorded in Builds for every Go package
	// in the input: an opt-in per-package source fingerprint. Do not pass a
	// covmeta hash ([CovdataBuild.MetaHash]); it fingerprints a binary, and
	// the tree and block-set checks already refuse different source.
	BuildFingerprint string
	// Limits bounds the parse.
	Limits Limits
}

// ParseCoverage reads one coverage input into a one-lane document. A Go
// profile's blocks that repeat (a `-coverpkg` profile repeats each block once
// per test binary, and textfmt over several builds' covdata once per meta
// file) are deduplicated as they are read, their hits summed. A repeated
// block whose statement count disagrees is a [*SourceMismatchError]: the
// inputs describe different source.
func ParseCoverage(r io.Reader, opts CoverageOptions) (*Coverage, error) {
	if opts.Lane == "" {
		return nil, fmt.Errorf("testreport: coverage input needs a lane")
	}
	lim := opts.Limits.resolved()
	h := sha256.New()
	br := bufio.NewReaderSize(io.TeeReader(newLimitedReader(r, lim.MaxInputBytes), h), 64<<10)
	format := opts.Format
	if format == "" {
		head, err := br.Peek(detectPeekBytes)
		if len(head) == 0 {
			if err != nil && err != io.EOF {
				return nil, err
			}
			return nil, ErrNoInput
		}
		format = DetectFormat(head)
		if !format.IsCoverage() {
			return nil, fmt.Errorf("testreport: input is not a coverage format (detected %s)", format)
		}
	}
	b := newCovBuilder(opts, lim)
	var (
		mode string
		err  error
	)
	switch format {
	case FormatGoCover:
		mode, err = parseGoCover(br, b, lim)
	case FormatLCOV:
		err = parseLCOV(br, b, lim)
	case FormatCobertura:
		err = parseCobertura(br, b)
	case FormatIstanbul:
		err = parseIstanbul(br, b)
	default:
		return nil, fmt.Errorf("testreport: %q is not a coverage format", format)
	}
	if err != nil {
		return nil, err
	}
	// Drain what a parser left unread, so the digest is of the whole input.
	if _, err := io.Copy(io.Discard, br); err != nil {
		return nil, err
	}
	if mode == "" {
		mode = string(ModeCount)
	}
	laneMode := ModeCount
	if mode == "set" {
		laneMode = ModeSet
	}
	c := b.build(laneMode)
	c.Inputs = []InputRef{{Digest: "sha256:" + hex.EncodeToString(h.Sum(nil)), Lane: opts.Lane, Format: format, Mode: mode}}
	c.Schema = SchemaCoverage
	c.OK = true
	return c, nil
}

// covBuilder accumulates one input's files and blocks, deduplicating blocks
// by position.
type covBuilder struct {
	opts   CoverageOptions
	lim    Limits
	files  map[string]*builderFile
	order  []string
	blocks int
}

type builderFile struct {
	file  CoverageFile
	index map[blockKey]int
}

func newCovBuilder(opts CoverageOptions, lim Limits) *covBuilder {
	return &covBuilder{opts: opts, lim: lim, files: map[string]*builderFile{}}
}

// fileFor returns the builder entry for a file named as the input names it.
// importPath is set for Go profiles only.
func (b *covBuilder) fileFor(importPath, filePath string, g Granularity) (*builderFile, error) {
	key := importPath
	if key == "" {
		filePath = relativize(filePath, b.opts.Root)
		key = "path:" + filePath
	}
	if f, ok := b.files[key]; ok {
		return f, nil
	}
	if len(b.files) >= b.lim.MaxCases {
		return nil, &LimitError{Limit: "MaxCases", Max: int64(b.lim.MaxCases)}
	}
	cf := CoverageFile{Granularity: g}
	if importPath != "" {
		cf.Lang = LangGo
		cf.ImportPath = importPath
		cf.Package = path.Dir(importPath)
	} else {
		cf.Lang = LangFromPath(filePath)
		cf.Path = filePath
		cf.Package = path.Dir(filePath)
	}
	if b.opts.Lang != "" && importPath == "" {
		cf.Lang = b.opts.Lang
	}
	f := &builderFile{file: cf, index: map[blockKey]int{}}
	b.files[key] = f
	b.order = append(b.order, key)
	return f, nil
}

// add records one block, summing hits into an existing block at the same
// position.
func (b *covBuilder) add(f *builderFile, blk Block, hits int64) error {
	k := blk.key()
	if i, ok := f.index[k]; ok {
		have := &f.file.Blocks[i]
		if have.Statements != blk.Statements {
			return &SourceMismatchError{File: f.file.DisplayPath(), Block: blockPos(blk), Statements: [2]int{have.Statements, blk.Statements}}
		}
		have.Hits[0] = addHits(have.Hits[0], hits)
		return nil
	}
	if b.blocks >= b.lim.MaxBlocks {
		return &LimitError{Limit: "MaxBlocks", Max: int64(b.lim.MaxBlocks)}
	}
	b.blocks++
	blk.Hits = []int64{max(hits, 0)}
	f.index[k] = len(f.file.Blocks)
	f.file.Blocks = append(f.file.Blocks, blk)
	return nil
}

func (b *covBuilder) build(mode Mode) *Coverage {
	c := &Coverage{
		Commit: b.opts.Commit, Tree: b.opts.Tree, Dirty: b.opts.Dirty,
		Lanes: []LaneInfo{{Name: b.opts.Lane, Mode: mode}},
	}
	for _, k := range b.order {
		f := b.files[k].file
		if mode == ModeSet {
			for i := range f.Blocks {
				f.Blocks[i].Hits[0] = min(f.Blocks[i].Hits[0], 1)
			}
		}
		sortBlocks(f.Blocks)
		if b.opts.BuildFingerprint != "" && f.Lang == LangGo {
			if c.Builds == nil {
				c.Builds = map[string]string{}
			}
			c.Builds[f.Package] = b.opts.BuildFingerprint
		}
		c.Files = append(c.Files, f)
	}
	sortFiles(c.Files)
	return c
}

// addHits sums two counts without overflowing.
func addHits(a, b int64) int64 {
	if b > 0 && a > int64(^uint64(0)>>1)-b {
		return int64(^uint64(0) >> 1)
	}
	return a + max(b, 0)
}

func blockPos(b Block) string {
	return fmt.Sprintf("%d.%d,%d.%d", b.StartLine, b.StartCol, b.EndLine, b.EndCol)
}

// relativize makes an absolute path relative to root, with forward slashes.
// A path outside root, or any path when root is empty, is kept (slashed).
func relativize(p, root string) string {
	p = filepath.ToSlash(p)
	if root == "" {
		return strings.TrimPrefix(p, "./")
	}
	r := strings.TrimSuffix(filepath.ToSlash(root), "/")
	if strings.HasPrefix(p, r+"/") {
		return p[len(r)+1:]
	}
	return strings.TrimPrefix(p, "./")
}

// GitBlobID is the git blob id of content: sha1("blob <len>\x00" + content),
// what `git hash-object` and `git ls-files -s` print. It is how a coverage
// file pins the exact source its counts describe.
func GitBlobID(content []byte) string { return gitBlobID(content) }
