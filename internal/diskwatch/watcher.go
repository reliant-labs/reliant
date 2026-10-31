// Copyright (c) 2025 Reliant Labs
package diskwatch

import (
	"context"
	"os"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/logging"
)

// DefaultPollInterval is how often volumes are statfs'd: a handful of syscalls,
// effectively free, and well inside the 15s heartbeat so a flip is current.
const DefaultPollInterval = 10 * time.Second

// Options configures a Watcher. The zero value watches the daemon process's
// own environment.
type Options struct {
	Env      Environment
	Interval time.Duration
	Now      func() time.Time

	// Test seams. Nil means the real thing.
	stat     func(string) (fsStat, string, error)
	docker   dockerAPI
	mounts   func() []overlayMount
	noDocker bool
	reaper   reaperRunner
	// worktreeReap reaps one repository's worktrees (worktreereap.go).
	worktreeReap worktreeReapRunner
}

// Watcher samples the watched volumes, keeps a pressure bit per volume,
// measures what is using a volume while it is under pressure, and reports the
// result shaped for the heartbeat. Safe for concurrent use.
type Watcher struct {
	env      Environment
	interval time.Duration
	now      func() time.Time
	stat     func(string) (fsStat, string, error)
	docker   dockerAPI
	mounts   func() []overlayMount

	targetsOnce sync.Once
	targets     []Target

	mu        sync.Mutex
	valid     bool
	vols      []*volume
	pressure  map[uint64]bool
	scans     map[uint64][]*reliantv1.DiskConsumer
	scanAt    map[uint64]time.Time
	scanning  bool
	noSpaceAt map[uint64]int64
	clearing  map[string]bool

	// reaper is the scheduled storage-reaper routine (reaper.go).
	reaper reaperState
	// worktreeReap is the scheduled worktree reap (worktreereap.go).
	worktreeReap worktreeReapState
	// trim is the native cache trim (cachetrim.go), guarded by mu.
	trim cacheTrimState
	// wtSizes remembers each worktree's size across scans (worktreesizes.go).
	wtSizes worktreeSizes
	// systemEnv: the watcher reads the daemon's own environment, so its
	// caches are this machine's real ones (never trimmed from a test).
	systemEnv bool

	// changed receives one token when something a heartbeat should carry at
	// once changed: a pressure flip, a finished scan, a no-space failure, a
	// clean-up starting or ending. Buffered(1), non-blocking: coalesces.
	changed chan struct{}
}

// New creates a Watcher. It does nothing until Run, but Report-free calls
// (ExplainNoSpace, ClearCaches) work without Run too.
func New(opts Options) *Watcher {
	systemEnv := opts.Env.Getenv == nil
	if systemEnv {
		opts.Env = SystemEnvironment()
	}
	if opts.Interval <= 0 {
		opts.Interval = DefaultPollInterval
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.stat == nil {
		opts.stat = statFS
	}
	if opts.docker == nil && !opts.noDocker {
		opts.docker = systemDocker(opts.Env.Getenv)
	}
	if opts.mounts == nil && opts.Env.GOOS == "linux" {
		opts.mounts = procOverlayMounts
	}
	if opts.reaper == nil {
		opts.reaper = execReaper
	}
	if opts.worktreeReap == nil {
		opts.worktreeReap = execWorktreeReap
	}
	return &Watcher{
		reaper:       reaperState{sem: make(chan struct{}, 1), run: opts.reaper, goCache: execGoCacheReaper},
		worktreeReap: worktreeReapState{run: opts.worktreeReap},
		trim:         cacheTrimState{cfg: DefaultCacheTrimConfig()},
		systemEnv:    systemEnv,
		env:          opts.Env,
		interval:     opts.Interval,
		now:          opts.Now,
		stat:         opts.stat,
		docker:       opts.docker,
		mounts:       opts.mounts,
		pressure:     map[uint64]bool{},
		scans:        map[uint64][]*reliantv1.DiskConsumer{},
		scanAt:       map[uint64]time.Time{},
		noSpaceAt:    map[uint64]int64{},
		clearing:     map[string]bool{},
		changed:      make(chan struct{}, 1),
	}
}

var (
	defaultOnce    sync.Once
	defaultWatcher *Watcher
)

// Default is the process-wide watcher over the daemon's own environment. The
// daemon runtime Runs it and puts its Report on the heartbeat; every exec path
// asks it to ExplainNoSpace, so a "no space left on device" anywhere is
// explained from the same picture and lands on the same report.
func Default() *Watcher {
	defaultOnce.Do(func() { defaultWatcher = New(Options{}) })
	return defaultWatcher
}

func (w *Watcher) targetList() []Target {
	w.targetsOnce.Do(func() { w.targets = DiscoverTargets(w.env) })
	return w.targets
}

// Changed returns a channel that receives a token when the report changed in a
// way worth sending now. Nil-safe: a nil Watcher's channel never fires.
func (w *Watcher) Changed() <-chan struct{} {
	if w == nil {
		return nil
	}
	return w.changed
}

func (w *Watcher) signal() {
	select {
	case w.changed <- struct{}{}:
	default:
	}
}

// Run samples until ctx is done.
func (w *Watcher) Run(ctx context.Context) {
	if w == nil {
		return
	}
	w.poll(ctx)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.poll(ctx)
		}
	}
}

// poll takes one sample, applies each volume's hysteresis transition, and
// starts a consumer scan for any volume under pressure that is due one.
func (w *Watcher) poll(ctx context.Context) {
	vols := groupVolumes(w.targetList(), w.stat)
	now := w.now()

	w.mu.Lock()
	flipped := w.applyLocked(vols)
	var due []*volume
	if !w.scanning {
		for _, v := range vols {
			if w.pressure[v.key] && (w.scanAt[v.key].IsZero() || now.Sub(w.scanAt[v.key]) >= ConsumerScanInterval) {
				due = append(due, v)
			}
		}
		w.scanning = len(due) > 0
	}
	w.mu.Unlock()

	if flipped {
		w.signal()
	}
	if len(due) > 0 {
		go w.scan(ctx, due)
	}
}

// applyLocked replaces the sample and steps every volume's pressure bit,
// logging and reporting whether any bit flipped. A volume that newly asserts
// pressure is due a fresh scan at once.
func (w *Watcher) applyLocked(vols []*volume) bool {
	flipped := false
	next := make(map[uint64]bool, len(vols))
	for _, v := range vols {
		prev := w.pressure[v.key]
		u := v.usage()
		p := NextPressure(prev, u)
		next[v.key] = p
		if p == prev {
			continue
		}
		flipped = true
		if p {
			delete(w.scanAt, v.key)
			logging.Warn("[diskwatch] Disk pressure", "volume", v.label(), "free", FormatBytes(u.FreeBytes), "total", FormatBytes(u.TotalBytes))
		} else {
			logging.Info("[diskwatch] Disk pressure cleared", "volume", v.label(), "free", FormatBytes(u.FreeBytes), "total", FormatBytes(u.TotalBytes))
		}
	}
	w.pressure = next
	w.vols = vols
	w.valid = true
	return flipped
}

func (w *Watcher) scan(ctx context.Context, vols []*volume) {
	defer func() {
		w.mu.Lock()
		w.scanning = false
		w.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(ctx, consumerScanBudget)
	defer cancel()
	for _, v := range vols {
		consumers := w.measureConsumers(ctx, v)
		w.mu.Lock()
		w.scans[v.key] = consumers
		w.scanAt[v.key] = w.now()
		w.mu.Unlock()
	}
	w.signal()
}

// Report is the latest sample, shaped for DaemonHeartbeat.disk. ok is false
// until the first sample. Consumers are included only for a volume under
// pressure (or one a clean-up is running on): a stale breakdown of a healthy
// volume is noise.
func (w *Watcher) Report() (*reliantv1.DiskReport, bool) {
	if w == nil {
		return nil, false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.valid {
		return nil, false
	}
	rep := &reliantv1.DiskReport{}
	for _, v := range w.vols {
		rep.Volumes = append(rep.Volumes, w.volumeProtoLocked(v))
	}
	return rep, true
}

func (w *Watcher) volumeProtoLocked(v *volume) *reliantv1.DiskVolume {
	u := v.usage()
	dv := &reliantv1.DiskVolume{
		Label:           v.label(),
		Paths:           v.paths(),
		TotalBytes:      u.TotalBytes,
		FreeBytes:       u.FreeBytes,
		UsedBytes:       u.UsedBytes,
		Pressure:        w.pressure[v.key],
		FilesTotal:      u.FilesTotal,
		FilesFree:       u.FilesFree,
		LastNoSpaceAtMs: w.noSpaceAt[v.key],
	}
	consumers := w.scans[v.key]
	clearing := false
	for _, c := range consumers {
		clearing = clearing || w.clearing[c.Kind]
	}
	if (dv.Pressure || clearing) && len(consumers) > 0 {
		for _, c := range consumers {
			cc := proto.Clone(c).(*reliantv1.DiskConsumer)
			cc.Clearing = w.clearing[c.Kind]
			dv.Consumers = append(dv.Consumers, cc)
		}
		dv.ConsumersMeasuredAtMs = w.scanAt[v.key].UnixMilli()
	}
	return dv
}

// pathExists is a seam-free existence check used by ClearCaches.
func pathExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
