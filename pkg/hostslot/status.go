package hostslot

import (
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Report is a class's state: its capacity, who holds a slot, who waits.
type Report struct {
	Capacity Capacity
	// Dir is the class's slot directory.
	Dir     string
	Holders []Holder
	// Waiting is the queue in the order it is served: express waiters, then
	// bulk waiters, each lane oldest first. Holder.Lane names the lane.
	Waiting []Holder
}

// Status reads a class's state. It takes no slot: each slot file is probed
// for an instant, so a concurrent acquirer may skip one slot once.
func Status(class string, opt Options) (Report, error) {
	capy, err := opt.capacity(class)
	if err != nil {
		return Report{}, err
	}
	dir, err := opt.classDir(class)
	if err != nil {
		return Report{}, err
	}
	rep := Report{Capacity: capy, Dir: dir}
	paths, _ := filepath.Glob(filepath.Join(dir, "slot-*.lock"))
	for _, path := range paths {
		i, ok := slotIndex(path)
		if !ok || !locked(path) {
			continue
		}
		rec, _ := readRecord(path)
		h := rec.holder(i)
		h.Slot = i
		rep.Holders = append(rep.Holders, h)
	}
	sort.Slice(rep.Holders, func(a, b int) bool { return rep.Holders[a].Slot < rep.Holders[b].Slot })
	for _, w := range (queue{dir: filepath.Join(dir, expressQueueDir)}).live("") {
		w.Lane = LaneExpress // the queue it sits in decides, whatever its record says
		rep.Waiting = append(rep.Waiting, w)
	}
	for _, w := range (queue{dir: filepath.Join(dir, "queue")}).live("") {
		w.Lane = LaneBulk
		rep.Waiting = append(rep.Waiting, w)
	}
	return rep, nil
}

func slotIndex(path string) (int, bool) {
	name := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "slot-"), ".lock")
	i, err := strconv.Atoi(name)
	return i, err == nil && i >= 0
}
