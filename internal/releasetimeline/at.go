package releasetimeline

import "time"

// AtResult answers "which release was live at T", with the promotions on
// either side.
type AtResult struct {
	Env string    `json:"env"`
	At  time.Time `json:"at"`
	// Live is the promotion in force at At: the newest one made at or
	// before it. Nil when At precedes the first promotion on record.
	Live *Entry `json:"live,omitempty"`
	// Previous is the promotion before Live; Next the one after it. Next is
	// the release that replaced Live, and is nil when Live is still current.
	Previous *Entry `json:"previous,omitempty"`
	Next     *Entry `json:"next,omitempty"`
	// BeforeHistory: At precedes every promotion on record, so nothing was
	// bound yet (Next is the first promotion).
	BeforeHistory bool `json:"before_history,omitempty"`
	// HistoryIncomplete: the source did not reach the start of the ledger,
	// so an At older than the oldest promotion fetched cannot be answered.
	HistoryIncomplete bool `json:"history_incomplete,omitempty"`
	// Unsettled: the rollout of Live had not been reported finished at At
	// (still rolling, never reported, or failed). A promotion is intent; the
	// environment may still have been serving Previous.
	Unsettled bool `json:"unsettled,omitempty"`
}

// At returns the release live at the given time. A promotion made exactly at
// that time is live at it: the ledger records the moment the environment was
// bound.
//
// It reads every promotion fetched, unfiltered by since/limit, in ledger order.
func (t Timeline) At(at time.Time) AtResult {
	res := AtResult{Env: t.Env, At: at.UTC()}
	live := -1
	for i, p := range t.promos {
		if p.At.After(at) {
			break
		}
		live = i
	}
	if live < 0 {
		if t.truncated {
			res.HistoryIncomplete = true
		} else {
			res.BeforeHistory = true
		}
		if len(t.promos) > 0 {
			next := t.promos[0]
			res.Next = &next
		}
		return res
	}
	l := t.promos[live]
	res.Live = &l
	if live > 0 {
		p := t.promos[live-1]
		res.Previous = &p
	}
	if live+1 < len(t.promos) {
		n := t.promos[live+1]
		res.Next = &n
	}
	res.Unsettled = l.Rollout == nil || l.Rollout.Status != StatusSucceeded || rolloutFinishedAfter(l.Rollout, at)
	return res
}

func rolloutFinishedAfter(r *Rollout, at time.Time) bool {
	if r.FinishedAt == "" {
		return false
	}
	f, err := time.Parse(time.RFC3339, r.FinishedAt)
	return err == nil && f.After(at)
}
