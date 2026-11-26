package testretry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Verify checks a retry artifact against independently resolved job metadata,
// trusted Git objects, and the exact initial and retry event streams. It
// reads candidate source as data and never runs Go or any candidate test.
func Verify(ctx context.Context, cfg Config, expectedTree, module string) (Report, error) {
	if !shaPattern.MatchString(expectedTree) || !shaPattern.MatchString(cfg.Base) || !shaPattern.MatchString(cfg.Head) ||
		!packagePattern.MatchString(module) || !repoPattern.MatchString(cfg.Repository) || !runPattern.MatchString(cfg.RunID) || cfg.PR < 0 {
		return Report{}, errors.New("invalid trusted metadata for retry verification")
	}
	file, err := os.Open(cfg.ReportPath)
	if err != nil {
		return Report{}, err
	}
	defer func() { _ = file.Close() }()
	const reportLimit = 48 << 10
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > reportLimit {
		return Report{}, errors.New("invalid or large retry report")
	}
	decoder := json.NewDecoder(io.LimitReader(file, reportLimit+1))
	decoder.DisallowUnknownFields()
	var report Report
	if err := decoder.Decode(&report); err != nil {
		return Report{}, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return Report{}, errors.New("trailing retry report data")
	}
	if report.Schema != Schema || report.Repository != cfg.Repository || report.Base != cfg.Base || report.Head != cfg.Head ||
		report.Tree != expectedTree || report.RunID != cfg.RunID || report.PR != cfg.PR {
		return Report{}, errors.New("retry report is for a different gate")
	}
	gitExe, err := exec.LookPath("git")
	if err != nil {
		return Report{}, err
	}
	tree, err := git(ctx, cfg.Root, gitExe, "rev-parse", cfg.Head+"^{tree}")
	if err != nil || strings.TrimSpace(string(tree)) != expectedTree {
		return Report{}, errors.New("retry report tree is not the job's tree")
	}
	base, err := git(ctx, cfg.Root, gitExe, "merge-base", cfg.Base, cfg.Head)
	if err != nil || strings.TrimSpace(string(base)) != cfg.Base {
		return Report{}, errors.New("retry report base is not an ancestor")
	}
	if hash, err := fileDigest(cfg.JSONPath); err != nil || hash != report.InitialSHA256 {
		return Report{}, errors.New("initial event digest mismatch")
	}
	var packages []string
	if err := scan(cfg.JSONPath, func(ev event) error {
		if ev.Action == "start" {
			packages = append(packages, ev.Package)
		}
		return nil
	}); err != nil {
		return Report{}, err
	}
	selected, err := packageSet(module, packages)
	if err != nil {
		return Report{}, err
	}
	failed, err := judge(cfg.JSONPath, selected, nil)
	if err != nil {
		return Report{}, err
	}
	if len(failed) != len(report.Tests) {
		return Report{}, errors.New("retry report does not cover the exact failures")
	}
	for i, item := range failed {
		src, err := locate(ctx, cfg, gitExe, module, cfg.Head, item.identity)
		if err != nil {
			return Report{}, err
		}
		prior, err := locate(ctx, cfg, gitExe, module, cfg.Base, item.identity)
		if err != nil || src != prior {
			return Report{}, errors.New("retry report names a changed or missing test")
		}
		retry := fmt.Sprintf("%s.retry-%d.ndjson", cfg.ReportPath, i)
		hash, err := fileDigest(retry)
		if err != nil {
			return Report{}, err
		}
		want := Test{Package: item.pkg, Test: item.name, File: src.file, SourceBlob: src.blob, FailureSignature: item.signature, RetrySHA256: hash}
		if report.Tests[i] != want {
			return Report{}, errors.New("retry source or proof disagrees with the checked artifacts")
		}
		if _, err := judge(retry, map[string]bool{item.pkg: true}, &item.identity); err != nil {
			return Report{}, err
		}
	}
	return report, nil
}
