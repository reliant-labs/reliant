package testreport

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// CovdataBuild is one build's coverage from a GOCOVERDIR, converted to a Go
// text profile.
type CovdataBuild struct {
	// MetaHash is the hash in the build's covmeta.<hash> file name. It
	// groups one binary's counter files together; it is not a per-package
	// fingerprint (two binaries from one tree differ), so it is not passed
	// as CoverageOptions.BuildFingerprint.
	MetaHash string
	// Profile is `go tool covdata textfmt` output for the build: a Go text
	// profile ([FormatGoCover]).
	Profile []byte
}

// ConvertCovdata converts the covdata in dir (what binaries built with -cover
// write to GOCOVERDIR) to Go text profiles, one per binary build. Builds are
// told apart by their covmeta hash and converted separately, because `go
// tool covdata` merges two builds of one package silently into wrong
// numbers. [Merge] then joins the profiles, and refuses them when they come
// from different source (tree hash, per-file block sets).
//
// It is the one function in this package that runs a program: goBin (""
// means "go" on PATH) with `tool covdata textfmt`. Everything else is pure,
// so a caller that already has textfmt output never needs it.
func ConvertCovdata(ctx context.Context, goBin, dir string) ([]CovdataBuild, error) {
	if goBin == "" {
		goBin = "go"
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	metas := map[string][]string{} // hash -> files of the build
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		switch {
		case strings.HasPrefix(name, "covmeta."):
			h := strings.TrimPrefix(name, "covmeta.")
			metas[h] = append(metas[h], name)
		case strings.HasPrefix(name, "covcounters."):
			h, _, _ := strings.Cut(strings.TrimPrefix(name, "covcounters."), ".")
			metas[h] = append(metas[h], name)
		}
	}
	var hashes []string
	for h, files := range metas {
		for _, f := range files {
			if strings.HasPrefix(f, "covmeta.") {
				hashes = append(hashes, h)
				break
			}
		}
	}
	sort.Strings(hashes)
	out := make([]CovdataBuild, 0, len(hashes))
	for _, h := range hashes {
		prof, err := textfmt(ctx, goBin, dir, metas[h], len(hashes) == 1)
		if err != nil {
			return nil, fmt.Errorf("testreport: covdata build %s: %w", h, err)
		}
		out = append(out, CovdataBuild{MetaHash: h, Profile: prof})
	}
	return out, nil
}

// textfmt runs `go tool covdata textfmt` over one build's files. When the
// directory holds only that build it reads it in place; otherwise it copies
// the build's files into a temporary directory first.
func textfmt(ctx context.Context, goBin, dir string, files []string, alone bool) ([]byte, error) {
	tmp, err := os.MkdirTemp("", "forge-covdata-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	in := dir
	if !alone {
		in = filepath.Join(tmp, "in")
		if err := os.Mkdir(in, 0o755); err != nil {
			return nil, err
		}
		for _, f := range files {
			if err := copyFile(filepath.Join(dir, f), filepath.Join(in, f)); err != nil {
				return nil, err
			}
		}
	}
	outFile := filepath.Join(tmp, "profile.out")
	cmd := exec.CommandContext(ctx, goBin, "tool", "covdata", "textfmt", "-i="+in, "-o="+outFile)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("go tool covdata textfmt: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return os.ReadFile(outFile)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
