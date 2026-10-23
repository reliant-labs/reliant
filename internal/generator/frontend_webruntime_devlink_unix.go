//go:build !windows

package generator

import "os"

func createDirLink(rel, _, link string) error { return os.Symlink(rel, link) }

func linkMatches(existing, rel, _ string) bool { return existing == rel }
