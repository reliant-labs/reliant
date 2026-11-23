package storage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

var rotatedLog = regexp.MustCompile(`^(.+)\.[0-9]{4}-[0-9]{2}-[0-9]{2}T[^/]+\.log$`)

// Logs only expires rotated Forge logs. Current streams, releases, promotion
// history and other files in .forge are never candidates.
//
// Each registered project is maintained independently: one that cannot be
// read (a project under ~/Documents the scheduled job has no TCC access to, a
// permission error, a log removed underneath the pass) is reported in the
// combined error and the remaining projects are still maintained.
func (r Runner) Logs(apply bool) error {
	var failures []error
	for _, project := range r.Policy.Projects {
		if err := r.hostCtx().Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		if err := r.projectLogs(project, apply); err != nil {
			if fsDistress(err) {
				err = r.deletes().observe(project, err)
			}
			if stopsPass(err) {
				return errors.Join(append(failures, err)...)
			}
			failures = append(failures, fmt.Errorf("project %s: %w", project, err))
		}
	}
	return errors.Join(failures...)
}

// projectLogs expires one project's rotated logs, continuing past an
// unreadable env directory or file so one bad entry costs only itself.
func (r Runner) projectLogs(project string, apply bool) error {
	dirs, err := os.ReadDir(filepath.Join(project, ".forge", "logs"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var failures []error
	del := r.deletes()
	for _, dir := range dirs {
		if !dir.IsDir() {
			continue
		}
		root := filepath.Join(project, ".forge", "logs", dir.Name())
		entries, err := os.ReadDir(root)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		type logFile struct {
			name, stream string
			size         int64
			at           time.Time
		}
		var files []logFile
		var total int64
		for _, entry := range entries {
			match := rotatedLog.FindStringSubmatch(entry.Name())
			if len(match) != 2 || !entry.Type().IsRegular() {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				// Gone or unreadable since the listing: it is neither
				// counted nor a candidate, and the rest still proceed.
				if !os.IsNotExist(err) {
					failures = append(failures, err)
				}
				continue
			}
			files = append(files, logFile{entry.Name(), match[1], info.Size(), info.ModTime()})
			total += info.Size()
		}
		sort.Slice(files, func(i, j int) bool { return files[i].at.After(files[j].at) })
		count := map[string]int{}
		var candidates []logFile
		for _, f := range files {
			count[f.stream]++
			if count[f.stream] > 5 {
				candidates = append(candidates, f)
			}
		}
		for i := len(candidates) - 1; i >= 0; i-- {
			f := candidates[i]
			if time.Since(f.at) < 7*24*time.Hour && total <= int64(r.Policy.LogBudgetGiB*GiB) {
				continue
			}
			path := filepath.Join(root, f.name)
			r.print("expire rotated log %s (%d bytes)\n", path, f.size)
			if err := r.hostCtx().Err(); err != nil {
				return errors.Join(append(failures, err)...)
			}
			if apply {
				if err := del.unlink(r.hostCtx(), path); err != nil {
					if stopsPass(err) {
						return errors.Join(append(failures, err)...)
					}
					failures = append(failures, fmt.Errorf("remove rotated log: %w", err))
					continue
				}
			}
			total -= f.size
		}
	}
	return errors.Join(failures...)
}

// RegisterProject adds a project whose rotated logs Forge can maintain.
func RegisterProject(path, project string) error {
	absolute, err := filepath.Abs(project)
	if err != nil {
		return err
	}
	return WithLock(path, func() error {
		if underTempDir(absolute) {
			return fmt.Errorf("refusing to register %s: it is under the temp directory, so it is a fixture or scratch project, not one whose logs forge should maintain", absolute)
		}
		p, err := Load(path)
		if err != nil {
			return err
		}
		if !contains(p.Projects, absolute) {
			p.Projects = append(p.Projects, absolute)
		}
		return Save(path, p)
	})
}
