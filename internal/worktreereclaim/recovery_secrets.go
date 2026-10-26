// Copyright (c) 2026 Reliant Labs
package worktreereclaim

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// RecoverySecretPolicy identifies a deliberately conservative, pinned local
// publication policy, NOT a guarantee that arbitrary credentials are detectable.
// Binary, encrypted/encoded, high-entropy and unknown content classes hold.
const RecoverySecretPolicy = "reliant-text-only-v2" //nolint:gosec // public detector policy version, not a credential

var recoverySecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`),
	regexp.MustCompile(`(?:AKIA|ASIA)[A-Z0-9]{16}`),
	regexp.MustCompile(`(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|sk-[A-Za-z0-9_-]{20,}|xox[baprs]-[A-Za-z0-9-]{10,})`),
	regexp.MustCompile(`(?i)(?:password|passwd|secret|api[_-]?key|access[_-]?token|auth[_-]?token|authorization|client[_-]?secret|private[_-]?key)\s*["']?\s*[:=]\s*["']?[^\s"']{4,}`),
	regexp.MustCompile(`(?i)(?:https?|postgres(?:ql)?|mysql|redis|mongodb)://[^\s/:]+:[^\s/@]+@`),
	regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`),
}

func recoveryScanPath(path []byte) error {
	// Paths are uploaded as raw tree names and base64 manifest entries. Assess
	// the decoded bytes with the same pinned credential detectors as file text;
	// base64 encoding is representation, not a secret-detection exemption.
	for _, detector := range recoverySecretPatterns {
		if detector.Match(path) {
			return fmt.Errorf("recovery: sensitive filename content detected")
		}
	}
	// Git supports non-UTF8 filenames. Preserve their bytes, while separating
	// invalid UTF8 when applying the text entropy policy; token detectors above
	// always see the original bytes, including tokens adjacent to invalid bytes.
	if err := recoveryScanContent(bytes.ToValidUTF8(path, []byte("-"))); err != nil {
		return err
	}
	// Name checks intentionally over-hold: a generated marker, ignore rule or
	// equality to another checkout is not evidence that data is safe to publish.
	p := strings.ToLower(string(path))
	for _, part := range strings.Split(p, "/") {
		if part == ".env" || strings.HasPrefix(part, ".env.") || strings.Contains(part, "credential") || strings.Contains(part, "secret") || strings.Contains(part, "password") || part == ".ssh" || part == ".aws" || part == ".npmrc" || part == ".netrc" || part == ".pypirc" || part == "id_rsa" || part == "id_ed25519" || part == ".git-credentials" || part == "data" || part == "backups" || part == "database" {
			return fmt.Errorf("recovery: sensitive path detected")
		}
		for _, suffix := range []string{".pem", ".key", ".p12", ".pfx", ".keystore", ".kdbx", ".age", ".gpg", ".enc", ".db", ".sqlite", ".sqlite3", ".dump", ".zip", ".gz", ".7z", ".tar", ".parquet", ".pdf", ".png", ".jpg", ".jpeg", ".wasm"} {
			if strings.HasSuffix(part, suffix) {
				return fmt.Errorf("recovery: unassessable data path")
			}
		}
	}
	return nil
}

func recoveryScanContent(b []byte) error {
	if len(b) > recoveryMaxObject {
		return fmt.Errorf("recovery: secret scan object budget exceeded")
	}
	if !utf8.Valid(b) {
		return fmt.Errorf("recovery: unsupported binary/encoding")
	}
	for _, r := range string(b) {
		if r == utf8.RuneError || unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return fmt.Errorf("recovery: unsupported binary/encoding")
		}
	}
	for _, p := range recoverySecretPatterns {
		if p.Match(b) {
			return fmt.Errorf("recovery: sensitive content detected")
		}
	}
	if bytes.Contains(b, []byte("version https://git-lfs.github.com/spec/v1")) || bytes.Contains(b, []byte("filter=")) || bytes.Contains(b, []byte("working-tree-encoding=")) {
		return fmt.Errorf("recovery: LFS/filter-dependent content unsupported")
	}
	// Opaque long strings cannot be assessed safely. Hex hashes/encoded data are
	// not granted a blanket exception; this intentionally holds some safe text.
	for _, token := range strings.FieldsFunc(string(b), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("+/=_-", r)
	}) {
		if len(token) < 24 {
			continue
		}
		counts := map[rune]int{}
		for _, r := range token {
			counts[r]++
		}
		entropy := 0.0
		for _, n := range counts {
			p := float64(n) / float64(len(token))
			entropy -= p * math.Log2(p)
		}
		if entropy >= 3.6 || len(token) >= 128 {
			return fmt.Errorf("recovery: opaque/high-entropy content is unassessable")
		}
	}
	return nil
}

// recoveryScanClosure enumerates every actual reachable object. It scans blobs,
// tree names and complete commit text, including staged-only and deleted-history
// content. Missing objects, unsupported modes/encodings and budget limits hold.
// Git is run with lazy fetch disabled and protocols denied: scanning never dials.
func recoveryScanClosure(ctx context.Context, path string, tips []string, manifestBlob string) error {
	if len(tips) == 0 || len(tips) > recoveryMaxTips+1 {
		return fmt.Errorf("recovery: invalid closure tips")
	}
	args := []string{"rev-list", "--objects", "--no-object-names"}
	for _, tip := range tips {
		if !recoveryOID(tip) {
			return fmt.Errorf("recovery: invalid closure identity")
		}
		args = append(args, tip)
	}
	args = append(args, "--")
	raw, err := recoveryCaptureGit(ctx, path, nil, nil, args...)
	if err != nil {
		return fmt.Errorf("recovery: incomplete history closure")
	}
	oids := strings.Fields(string(raw))
	if len(oids) > recoveryMaxFiles*4 {
		return fmt.Errorf("recovery: object count budget exceeded")
	}
	var input strings.Builder
	for _, oid := range oids {
		if !recoveryOID(oid) {
			return fmt.Errorf("recovery: malformed closure enumeration")
		}
		input.WriteString(oid)
		input.WriteByte('\n')
	}
	data, err := recoveryCaptureGit(ctx, path, nil, []byte(input.String()), "cat-file", "--batch")
	if err != nil {
		return fmt.Errorf("recovery: object enumeration failed")
	}
	total := 0
	for _, want := range oids {
		hdr, rest, ok := bytes.Cut(data, []byte{'\n'})
		if !ok {
			return fmt.Errorf("recovery: truncated object batch")
		}
		f := strings.Fields(string(hdr))
		if len(f) != 3 || f[0] != want {
			return fmt.Errorf("recovery: missing closure object")
		}
		size, e := strconv.Atoi(f[2])
		if e != nil || size < 0 || size > recoveryMaxObject || len(rest) < size+1 {
			return fmt.Errorf("recovery: unassessable object size")
		}
		total += size
		if total > recoveryMaxBytes {
			return fmt.Errorf("recovery: closure byte budget exceeded")
		}
		body := rest[:size]
		if rest[size] != '\n' {
			return fmt.Errorf("recovery: malformed object batch")
		}
		data = rest[size+1:]
		switch f[1] {
		case "blob":
			if want != manifestBlob {
				if e = recoveryScanContent(body); e != nil {
					return e
				}
			} // canonical manifest fields and paths are assessed structurally, not as opaque encoded payloads
		case "commit":
			// Commit object headers contain object hashes, not opaque user strings.
			// Scan metadata/message separately so hashes do not trigger entropy policy.
			for _, line := range bytes.Split(body, []byte{'\n'}) {
				if bytes.HasPrefix(line, []byte("tree ")) || bytes.HasPrefix(line, []byte("parent ")) {
					continue
				}
				if e = recoveryScanContent(line); e != nil {
					return e
				}
			}
		case "tree":
			hashBytes := len(want) / 2
			for len(body) > 0 {
				meta, tail, ok := bytes.Cut(body, []byte{0})
				if !ok || len(tail) < hashBytes {
					return fmt.Errorf("recovery: invalid tree")
				}
				mode, name, ok := bytes.Cut(meta, []byte{' '})
				if !ok {
					return fmt.Errorf("recovery: invalid tree name")
				}
				if string(mode) != "40000" && string(mode) != "100644" && string(mode) != "100755" && string(mode) != "120000" {
					return fmt.Errorf("recovery: history has unsupported Git mode")
				}
				if err = recoveryPath(string(name)); err != nil {
					return err
				}
				if err = recoveryScanPath(name); err != nil {
					return err
				}
				body = tail[hashBytes:]
			}
		default:
			return fmt.Errorf("recovery: unsupported closure object class")
		}
	}
	if len(data) != 0 {
		return fmt.Errorf("recovery: extra object batch data")
	}
	return nil
}
