package daemonruntime

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type ownedRunExecution struct {
	chat           string
	rootFirstRunID string
	cancel         context.CancelFunc
	done           chan struct{}
	uncertainty    error
}

func (d *daemonClient) trackRunExecution(id, chat, rootFirstRunID string, cancel context.CancelFunc) (func(), error) {
	d.cancelMu.Lock()
	defer d.cancelMu.Unlock()
	if rootFirstRunID == "" && chat != "" {
		// A drained-upgrade deployment cannot admit legacy requests for a chat
		// with a cancellation journal: identity-less work cannot bypass fencing.
		if entries, err := os.ReadDir(filepath.Join(d.bootCfg.DataDir, "cancelled-runs")); err == nil && len(entries) > 0 {
			return nil, fmt.Errorf("root execution identity required after cancellation; update worker")
		}
	}
	if rootFirstRunID != "" {
		path, err := d.runTombstone(chat, rootFirstRunID)
		if err != nil {
			return nil, err
		}
		if _, err := os.Stat(path); err == nil {
			return nil, fmt.Errorf("root execution cancelled")
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	marker := ""
	if rootFirstRunID != "" {
		base, _ := d.runTombstone(chat, rootFirstRunID)
		sum := sha256.Sum256([]byte(id))
		marker = base + fmt.Sprintf(".active-%x", sum)
		if err := persistRunTombstone(marker); err != nil {
			return nil, err
		}
	}
	if d.runExecutions == nil {
		d.runExecutions = map[string]*ownedRunExecution{}
	}
	e := &ownedRunExecution{chat: chat, rootFirstRunID: rootFirstRunID, cancel: cancel, done: make(chan struct{})}
	d.runExecutions[id] = e
	return func() {
		d.cancelMu.Lock()
		defer d.cancelMu.Unlock()
		if marker != "" && e.uncertainty == nil {
			if err := os.Remove(marker); err != nil {
				e.uncertainty = err
			} else {
				dir, err := os.Open(filepath.Dir(marker))
				if err == nil {
					err = dir.Sync()
					dir.Close()
				}
				e.uncertainty = err
			}
		}
		delete(d.runExecutions, id)
		close(e.done)
	}, nil
}

// The reply acknowledges a durable execution fence and handler completion,
// not merely delivery of a signal.
// A timeout/error MUST NOT be translated into a successful terminal status.
func (d *daemonClient) cancelOwnedRun(ctx context.Context, payload []byte) ([]byte, error) {
	var req struct {
		ChatID         string `json:"chat_id"`
		RootFirstRunID string `json:"root_first_run_id"`
	}
	if err := json.Unmarshal(payload, &req); err != nil || req.ChatID == "" {
		return nil, fmt.Errorf("run.cancel requires chat_id")
	}
	d.cancelMu.Lock()
	if req.RootFirstRunID == "" {
		d.cancelMu.Unlock()
		return nil, fmt.Errorf("cancellation unconfirmed: root execution identity required")
	}
	path, err := d.runTombstone(req.ChatID, req.RootFirstRunID)
	if err == nil {
		err = persistRunTombstone(path)
	}
	if err != nil {
		d.cancelMu.Unlock()
		return nil, fmt.Errorf("persist cancellation fence: %w", err)
	}
	var owned []*ownedRunExecution
	for _, e := range d.runExecutions {
		if e.chat == req.ChatID && (e.rootFirstRunID == req.RootFirstRunID || e.rootFirstRunID == "") {
			if e.rootFirstRunID == "" {
				e.uncertainty = errors.Join(e.uncertainty, fmt.Errorf("legacy request has no root execution identity; late dispatch cannot be fenced"))
			}
			owned = append(owned, e)
		}
	}
	d.cancelMu.Unlock()
	for _, e := range owned {
		e.cancel()
	}
	for _, e := range owned {
		select {
		case <-e.done:
		case <-ctx.Done():
			return nil, fmt.Errorf("run cancellation unconfirmed: command still executing: %w", ctx.Err())
		}
	}
	// Tombstone committed under the same lock as dispatch admission before
	// snapshotting. Late old activity requests cannot enter after this drain.
	d.cancelMu.Lock()
	defer d.cancelMu.Unlock()
	var errs []error
	for _, e := range owned {
		errs = append(errs, e.uncertainty)
	}
	markers, scanErr := filepath.Glob(path + ".active-*")
	if scanErr != nil {
		errs = append(errs, scanErr)
	}
	if len(markers) > 0 {
		errs = append(errs, fmt.Errorf("cancellation unconfirmed: %d execution markers remain after drain or daemon restart", len(markers)))
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return []byte(`{"stopped":true}`), nil
}

func (d *daemonClient) runTombstone(chat, root string) (string, error) {
	if d.bootCfg.DataDir == "" {
		return "", fmt.Errorf("daemon data directory unavailable")
	}
	sum := sha256.Sum256([]byte(chat + "\x00" + root))
	return filepath.Join(d.bootCfg.DataDir, "cancelled-runs", fmt.Sprintf("%x", sum)), nil
}
func persistRunTombstone(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	parent, err := os.Open(filepath.Dir(filepath.Dir(path)))
	if err != nil {
		return err
	}
	if err = parent.Sync(); err != nil {
		parent.Close()
		return err
	}
	parent.Close()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
