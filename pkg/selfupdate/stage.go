package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Prepare downloads rel's archive for this platform, verifies it, extracts
// the binary into StateDir/staged/<version>/ and probes it. Nothing running
// is touched. It refuses an install something else owns ([ErrNotSelfOwned]).
func (u *Updater) Prepare(ctx context.Context, rel Release) (Staged, error) {
	if _, err := u.requireSelfOwned(); err != nil {
		return Staged{}, err
	}
	version := rel.label()
	if !safeName(version) {
		return Staged{}, fmt.Errorf("selfupdate: release version %q is not a safe directory name", version)
	}
	archiveName := u.cfg.ArchiveName(runtime.GOOS, runtime.GOARCH)
	archive, ok := rel.Asset(archiveName)
	if !ok {
		return Staged{}, fmt.Errorf("selfupdate: release %s has no %s for %s/%s", version, archiveName, runtime.GOOS, runtime.GOARCH)
	}
	sums, ok := rel.Asset(u.cfg.ChecksumsName)
	if !ok {
		return Staged{}, fmt.Errorf("selfupdate: release %s has no %s", version, u.cfg.ChecksumsName)
	}
	downloads := []Asset{archive, sums}
	if rel.Bundle.URL != "" {
		downloads = append(downloads, rel.Bundle)
	}
	for _, a := range downloads {
		if !safeName(a.Name) {
			return Staged{}, fmt.Errorf("selfupdate: release asset name %q is not a plain file name", a.Name)
		}
	}

	var result Staged
	err := withLock(ctx, u.cfg.StateDir, func() error {
		stagedRoot := filepath.Join(u.cfg.StateDir, "staged")
		dir := filepath.Join(stagedRoot, version)
		dl := filepath.Join(dir, "download")
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
		if err := os.MkdirAll(dl, 0o700); err != nil {
			return err
		}
		staged := false
		defer func() {
			if !staged {
				_ = os.RemoveAll(dir) // a refused release leaves nothing behind
			}
		}()
		files := make(map[string]string, len(downloads))
		for _, a := range downloads {
			dst := filepath.Join(dl, a.Name)
			if err := u.download(ctx, a, dst); err != nil {
				return err
			}
			files[a.Name] = dst
		}

		if u.cfg.Verifier != nil {
			if err := u.cfg.Verifier.Verify(ctx, rel, files); err != nil {
				return fmt.Errorf("selfupdate: release %s failed signature verification: %w", version, err)
			}
		} else {
			u.log.WarnContext(ctx, "selfupdate: INSECURE: staging an unverified release (Config.InsecureAllowUnsigned)", "version", version)
		}
		// The sha256 comes from the checksums.txt the verifier just checked,
		// never from one fetched unsigned beside the archive.
		want, err := checksumFor(files[sums.Name], archive.Name)
		if err != nil {
			return err
		}
		got, err := sha256File(files[archive.Name])
		if err != nil {
			return err
		}
		if !strings.EqualFold(want, got) {
			return fmt.Errorf("selfupdate: sha256 mismatch for %s: %s says %s, the download is %s", archive.Name, sums.Name, want, got)
		}

		bin := filepath.Join(dir, u.cfg.BinaryName)
		if err := extractBinary(files[archive.Name], archive.Name, u.cfg.BinaryName, bin, u.cfg.MaxDownloadBytes); err != nil {
			return fmt.Errorf("selfupdate: extract %s from %s: %w", u.cfg.BinaryName, archive.Name, err)
		}
		if err := syncDir(dir); err != nil {
			return err
		}
		if err := u.probe(ctx, bin); err != nil {
			return err
		}
		sum, err := sha256File(bin)
		if err != nil {
			return err
		}
		_ = os.RemoveAll(dl)
		pruneStaged(stagedRoot, version)
		result = Staged{Release: rel, Path: bin, SHA256: sum}
		staged = true
		return nil
	})
	if err != nil {
		return Staged{}, err
	}
	u.log.InfoContext(ctx, "selfupdate: staged", "binary", u.cfg.Binary, "version", version, "path", result.Path)
	return result, nil
}

// safeName is a single path element that cannot escape its directory.
func safeName(s string) bool {
	if s == "" || s == "." || s == ".." || strings.ContainsAny(s, `/\:`) {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// pruneStaged removes every staged release but keep.
func pruneStaged(root, keep string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.Name() != keep {
			_ = os.RemoveAll(filepath.Join(root, e.Name()))
		}
	}
}

// httpsOnly wraps c so a redirect can never downgrade a download to http.
func httpsOnly(c *http.Client) *http.Client {
	cp := *c
	prev := c.CheckRedirect
	cp.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" {
			return fmt.Errorf("selfupdate: refusing a redirect to non-https %s", req.URL.Redacted())
		}
		if prev != nil {
			return prev(req, via)
		}
		if len(via) >= 10 {
			return errors.New("selfupdate: stopped after 10 redirects")
		}
		return nil
	}
	return &cp
}

func (u *Updater) download(ctx context.Context, a Asset, dst string) error {
	parsed, err := url.Parse(a.URL)
	if err != nil || parsed.Scheme != "https" {
		return fmt.Errorf("selfupdate: refusing to download %s from non-https URL %q", a.Name, a.URL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return err
	}
	resp, err := httpsOnly(u.cfg.HTTPClient).Do(req)
	if err != nil {
		return fmt.Errorf("selfupdate: download %s: %w", a.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("selfupdate: download %s: %s", a.Name, resp.Status)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(resp.Body, u.cfg.MaxDownloadBytes+1))
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("selfupdate: download %s: %w", a.Name, err)
	}
	if n > u.cfg.MaxDownloadBytes {
		_ = f.Close()
		return fmt.Errorf("selfupdate: download %s: larger than %d bytes", a.Name, u.cfg.MaxDownloadBytes)
	}
	return finishFile(f, 0o600)
}

// checksumFor finds name in a sha256sum-format file ("<hex>  <name>", or
// "<hex> *<name>" for binary mode).
func checksumFor(sumsPath, name string) (string, error) {
	f, err := os.Open(sumsPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) != 2 {
			continue
		}
		if strings.TrimPrefix(fields[1], "*") == name {
			if len(fields[0]) != 64 {
				return "", fmt.Errorf("selfupdate: malformed sha256 for %s", name)
			}
			return strings.ToLower(fields[0]), nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("selfupdate: %s has no entry for %s", filepath.Base(sumsPath), name)
}

// extractBinary writes the one regular file named binary inside the archive
// (at its root or one directory down) to dst. An asset that is not a .tar.gz,
// .tgz or .zip is the binary itself.
func extractBinary(archive, archiveName, binary, dst string, limit int64) error {
	write := func(r io.Reader) error {
		f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			return err
		}
		n, err := io.Copy(f, io.LimitReader(r, limit+1))
		if err != nil {
			_ = f.Close()
			return err
		}
		if n > limit {
			_ = f.Close()
			return fmt.Errorf("binary larger than %d bytes", limit)
		}
		return finishFile(f, 0o755)
	}
	matches := func(name string) bool {
		clean := path.Clean(strings.TrimPrefix(filepath.ToSlash(name), "./"))
		if strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
			return false
		}
		return path.Base(clean) == binary && strings.Count(clean, "/") <= 1
	}
	lower := strings.ToLower(archiveName)
	switch {
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		f, err := os.Open(archive)
		if err != nil {
			return err
		}
		defer f.Close()
		gz, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		tr := tar.NewReader(gz)
		found := false
		for {
			h, err := tr.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return err
			}
			if h.Typeflag != tar.TypeReg || !matches(h.Name) {
				continue
			}
			if found {
				return fmt.Errorf("more than one %s in the archive", binary)
			}
			found = true
			if err := write(tr); err != nil {
				return err
			}
		}
		if !found {
			return fmt.Errorf("no %s in the archive", binary)
		}
		return nil
	case strings.HasSuffix(lower, ".zip"):
		zr, err := zip.OpenReader(archive)
		if err != nil {
			return err
		}
		defer zr.Close()
		var hit *zip.File
		for _, zf := range zr.File {
			if !zf.Mode().IsRegular() || !matches(zf.Name) {
				continue
			}
			if hit != nil {
				return fmt.Errorf("more than one %s in the archive", binary)
			}
			hit = zf
		}
		if hit == nil {
			return fmt.Errorf("no %s in the archive", binary)
		}
		r, err := hit.Open()
		if err != nil {
			return err
		}
		defer r.Close()
		return write(r)
	default:
		f, err := os.Open(archive)
		if err != nil {
			return err
		}
		defer f.Close()
		return write(f)
	}
}

// probe runs the staged binary's self-test, so a wrong-architecture,
// missing-library or truncated binary is caught while the old process still
// serves.
func (u *Updater) probe(ctx context.Context, bin string) error {
	ctx, cancel := context.WithTimeout(ctx, u.cfg.ProbeTimeout)
	defer cancel()
	var out []byte
	var err error
	for attempt := 0; ; attempt++ {
		out, err = exec.CommandContext(ctx, bin, u.cfg.Probe...).CombinedOutput()
		// ETXTBSY: a child forked elsewhere in this process between our
		// write and its own exec still holds the just-written file open
		// (golang/go#22315). It closes within milliseconds.
		if !isTextBusy(err) || attempt == 20 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		text := strings.TrimSpace(string(out))
		if len(text) > 2048 {
			text = text[:2048] + "…"
		}
		return fmt.Errorf("selfupdate: the staged binary failed its probe (%s %s): %w: %s",
			filepath.Base(bin), strings.Join(u.cfg.Probe, " "), err, text)
	}
	return nil
}
