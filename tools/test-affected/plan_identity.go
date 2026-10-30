package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const planSchema = 1

// checkoutDigest identifies the inputs a plan was made from: HEAD names every
// committed byte, so only what differs from it is read — the bytes of each
// modified and untracked (non-ignored) file, and each deletion. Hashing every
// tracked file instead read the whole checkout (~30 MB) three times per gate.
// node_modules and output/log directories ignored by git are not inputs.
func checkoutDigest(ctx context.Context, root string) (string, error) {
	head, err := git(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	names, err := git(ctx, root, "ls-files", "--modified", "--deleted", "--others", "--exclude-standard", "-z")
	if err != nil {
		return "", err
	}
	paths := strings.Split(strings.TrimSuffix(names, "\x00"), "\x00")
	slices.Sort(paths)
	h := sha256.New()
	fmt.Fprintf(h, "HEAD=%s\x00", strings.TrimSpace(head))
	for _, name := range slices.Compact(paths) {
		if name == "" {
			continue
		}
		p := filepath.Join(root, name)
		st, err := os.Lstat(p)
		if os.IsNotExist(err) {
			fmt.Fprintf(h, "%s\x00deleted\x00", name)
			continue
		}
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\x00%v\x00", name, st.Mode())
		if st.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(p)
			if err != nil {
				return "", err
			}
			fmt.Fprint(h, target)
		} else if st.Mode().IsRegular() {
			f, err := os.Open(p)
			if err != nil {
				return "", err
			}
			_, copyErr := io.Copy(h, f)
			closeErr := f.Close()
			if copyErr != nil {
				return "", copyErr
			}
			if closeErr != nil {
				return "", closeErr
			}
		} else {
			return "", fmt.Errorf("cannot fingerprint plan input %s (%v)", name, st.Mode())
		}
		fmt.Fprint(h, "\x00")
	}
	for _, key := range []string{"GOOS", "GOARCH", "GOFLAGS", "GOWORK", "GOTOOLCHAIN", "CGO_ENABLED"} {
		fmt.Fprintf(h, "%s=%s\x00", key, os.Getenv(key))
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

func validatePlanIdentity(ctx context.Context, root, base string, pl *Plan) error {
	if pl.Schema != planSchema || pl.Base == "" || pl.Head == "" || len(pl.InputDigest) != 64 {
		return fmt.Errorf("missing/unsupported plan identity; replan this checkout")
	}
	_, currentBase, head, err := diff(ctx, root, base)
	if err != nil {
		return err
	}
	if pl.Base != currentBase || pl.Head != head {
		return fmt.Errorf("stale plan base/head; replan this checkout")
	}
	digest, err := checkoutDigest(ctx, root)
	if err != nil {
		return err
	}
	if pl.InputDigest != digest {
		return fmt.Errorf("stale plan input digest; replan this checkout")
	}
	return nil
}
