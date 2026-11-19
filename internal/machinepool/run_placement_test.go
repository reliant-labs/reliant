// Copyright (c) 2025 Reliant Labs
package machinepool

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/orm"
	"go.temporal.io/sdk/temporal"

	"github.com/reliant-labs/reliant/internal/db/core"
)

var runCatalog = `[{"slug":"pr-review","content_hash":"A"}]`

func memberRow(name, phase string, live int) core.MachinePoolMemberRow {
	return core.MachinePoolMemberRow{DaemonID: "id-" + name, Name: name, Phase: phase, LiveRuns: live,
		Installed: true, Verified: phase == "connected", WorkflowsJSON: &runCatalog, CatalogPushedAt: &t0}
}

// fakeRunStore is a RunStore over maps: one pool ("cicd"), its members, the
// owner's labelled machines, and the recorded placements.
type fakeRunStore struct {
	pools    map[string]*core.MachinePool
	members  []core.MachinePoolMemberRow
	labelled []core.MachinePoolMemberRow
	placed   map[string]*core.RunMachinePlacement
	// slotCount is what the slot guard counts for a member, by attempt; nil
	// counts the member's row.
	slotCount func(daemonID string) int
	locks     int
	txs       int
}

func newFakeRunStore(members ...core.MachinePoolMemberRow) *fakeRunStore {
	return &fakeRunStore{
		pools:   map[string]*core.MachinePool{"cicd": {ID: "pool-1", UserID: "u", Name: "cicd", Selector: DefaultSelector("cicd"), MaxRunsPerMember: 2}},
		members: members,
		placed:  map[string]*core.RunMachinePlacement{},
	}
}

func (f *fakeRunStore) GetMachinePoolForUser(_ context.Context, userID, id string) (*core.MachinePool, error) {
	for _, p := range f.pools {
		if p.ID == id && p.UserID == userID {
			return p, nil
		}
	}
	return nil, core.ErrMachinePoolNotFound
}

func (f *fakeRunStore) GetMachinePoolByName(_ context.Context, userID, name string) (*core.MachinePool, error) {
	if p, ok := f.pools[name]; ok && p.UserID == userID {
		return p, nil
	}
	return nil, core.ErrMachinePoolNotFound
}

func (f *fakeRunStore) ListMachinePoolMembers(context.Context, *core.MachinePool, string) ([]core.MachinePoolMemberRow, error) {
	return f.members, nil
}

func (f *fakeRunStore) ListLabelledMachines(_ context.Context, _ string, labels map[string]string, _ string) ([]core.MachinePoolMemberRow, error) {
	if len(labels) == 0 {
		return nil, nil
	}
	return f.labelled, nil
}

func (f *fakeRunStore) LockMachinePool(context.Context, string) error {
	f.locks++
	return nil
}

func (f *fakeRunStore) CountMachinePoolMemberLiveRuns(_ context.Context, _, daemonID string) (int, error) {
	if f.slotCount != nil {
		return f.slotCount(daemonID), nil
	}
	for _, m := range f.members {
		if m.DaemonID == daemonID {
			return m.LiveRuns, nil
		}
	}
	return 0, nil
}

func (f *fakeRunStore) CountTriggerCapacityHeldByMember(context.Context, string, string, string, string) (map[string]int, error) {
	return nil, nil
}

func (f *fakeRunStore) GetRunMachinePlacement(_ context.Context, workflowID, scope string) (*core.RunMachinePlacement, error) {
	if p, ok := f.placed[workflowID+"/"+scope]; ok {
		return p, nil
	}
	return nil, core.ErrRunMachinePlacementNotFound
}

func (f *fakeRunStore) RecordRunMachinePlacement(_ context.Context, p *core.RunMachinePlacement) (*core.RunMachinePlacement, error) {
	key := p.WorkflowID + "/" + p.Scope
	if existing, ok := f.placed[key]; ok {
		return existing, nil
	}
	stored := *p
	f.placed[key] = &stored
	return &stored, nil
}

func (f *fakeRunStore) RunTx(ctx context.Context, _ orm.TxPolicy, fn func(context.Context) error) error {
	f.txs++
	return fn(ctx)
}

type recordingWaker struct{ woke []string }

func (w *recordingWaker) WakeMember(_ context.Context, _, daemonID string) error {
	w.woke = append(w.woke, daemonID)
	return nil
}

func poolRequest(scope string) RunRequest {
	return RunRequest{PlaceRunInput: PlaceRunInput{ChatID: "chat-1", WorkflowID: "wf-1", Scope: scope, Pool: "cicd"}, UserID: "u", ProjectID: "proj"}
}

func labelsRequest() RunRequest {
	return RunRequest{PlaceRunInput: PlaceRunInput{ChatID: "chat-1", WorkflowID: "wf-1", Labels: map[string]string{"role": "gate"}}, UserID: "u", ProjectID: "proj"}
}

func TestPlaceRun(t *testing.T) {
	tests := []struct {
		name  string
		store func() *fakeRunStore
		req   RunRequest
		// want is the answer; wantReason a substring of a Waiting reason.
		want       PlaceRunOutput
		wantReason string
		wantErr    error
		wantWoke   []string
		// wantRecord is the placement recorded for the request ("" none).
		wantRecord     string
		wantRecordPool string
	}{
		{
			name: "pool places on the least-loaded connected member",
			store: func() *fakeRunStore {
				return newFakeRunStore(memberRow("a", "connected", 1), memberRow("b", "connected", 0), memberRow("c", "suspended", 0))
			},
			req:        poolRequest(""),
			want:       PlaceRunOutput{DaemonID: "id-b", DaemonName: "b", PoolID: "pool-1", PoolName: "cicd"},
			wantRecord: "id-b", wantRecordPool: "pool-1",
		},
		{
			name: "labels place on the least-loaded labelled machine, naming no pool",
			store: func() *fakeRunStore {
				s := newFakeRunStore()
				s.labelled = []core.MachinePoolMemberRow{memberRow("x", "connected", 3), memberRow("y", "connected", 1)}
				return s
			},
			req:        labelsRequest(),
			want:       PlaceRunOutput{DaemonID: "id-y", DaemonName: "y"},
			wantRecord: "id-y",
		},
		{
			name: "labels have no per-member bound",
			store: func() *fakeRunStore {
				s := newFakeRunStore()
				s.labelled = []core.MachinePoolMemberRow{memberRow("x", "connected", 50)}
				return s
			},
			req:        labelsRequest(),
			want:       PlaceRunOutput{DaemonID: "id-x", DaemonName: "x"},
			wantRecord: "id-x",
		},
		{
			name: "a recorded placement is the answer, whatever the members are now",
			store: func() *fakeRunStore {
				s := newFakeRunStore(memberRow("a", "connected", 0), memberRow("b", "disconnected", 0))
				s.placed["wf-1/"] = &core.RunMachinePlacement{WorkflowID: "wf-1", ChatID: "chat-1", MachinePoolID: "pool-1", DaemonID: "id-b"}
				return s
			},
			req:        poolRequest(""),
			want:       PlaceRunOutput{DaemonID: "id-b", DaemonName: "b", PoolID: "pool-1", PoolName: "cicd"},
			wantRecord: "id-b", wantRecordPool: "pool-1",
		},
		{
			name: "a full pool waits, saying why",
			store: func() *fakeRunStore {
				return newFakeRunStore(memberRow("a", "connected", 2), memberRow("b", "connected", 2))
			},
			req:        poolRequest(""),
			want:       PlaceRunOutput{Waiting: true, PoolID: "pool-1", PoolName: "cicd"},
			wantReason: WaitingPrefix + "cicd (a: full (2/2 runs); b: full (2/2 runs))",
		},
		{
			name: "a suspended member is woken and the run waits for it",
			store: func() *fakeRunStore {
				return newFakeRunStore(memberRow("a", "suspended", 0), memberRow("b", "disconnected", 0))
			},
			req:        poolRequest(""),
			want:       PlaceRunOutput{Waiting: true, PoolID: "pool-1", PoolName: "cicd"},
			wantReason: "waking a",
			wantWoke:   []string{"id-a"},
		},
		{
			name: "no labelled machine waits, naming the labels",
			store: func() *fakeRunStore {
				return newFakeRunStore()
			},
			req:        labelsRequest(),
			want:       PlaceRunOutput{Waiting: true},
			wantReason: WaitingPrefix + "labelled role=gate: it has no members",
		},
		{
			name: "a trigger-placed chat keeps its member for the workflow-level placement",
			store: func() *fakeRunStore {
				return newFakeRunStore(memberRow("a", "connected", 0), memberRow("b", "connected", 2))
			},
			req: func() RunRequest {
				r := poolRequest("")
				r.ChatPoolID, r.ChatDaemonID = "pool-1", "id-b"
				return r
			}(),
			want:       PlaceRunOutput{DaemonID: "id-b", DaemonName: "b", PoolID: "pool-1", PoolName: "cicd"},
			wantRecord: "id-b", wantRecordPool: "pool-1",
		},
		{
			name: "a node of a trigger-placed chat is placed like any other",
			store: func() *fakeRunStore {
				return newFakeRunStore(memberRow("a", "connected", 0), memberRow("b", "connected", 1))
			},
			req: func() RunRequest {
				r := poolRequest("review")
				r.ChatPoolID, r.ChatDaemonID = "pool-1", "id-b"
				return r
			}(),
			want:       PlaceRunOutput{DaemonID: "id-a", DaemonName: "a", PoolID: "pool-1", PoolName: "cicd"},
			wantRecord: "id-a", wantRecordPool: "pool-1",
		},
		{
			name: "a chat placed on another pool is placed afresh",
			store: func() *fakeRunStore {
				return newFakeRunStore(memberRow("a", "connected", 0), memberRow("b", "connected", 1))
			},
			req: func() RunRequest {
				r := poolRequest("")
				r.ChatPoolID, r.ChatDaemonID = "pool-other", "id-b"
				return r
			}(),
			want:       PlaceRunOutput{DaemonID: "id-a", DaemonName: "a", PoolID: "pool-1", PoolName: "cicd"},
			wantRecord: "id-a", wantRecordPool: "pool-1",
		},
		{
			name: "a slot taken by a concurrent placement re-places",
			store: func() *fakeRunStore {
				s := newFakeRunStore(memberRow("a", "connected", 0), memberRow("b", "connected", 1))
				// a's last slot went between the choice and the guard; the
				// next read sees it.
				s.slotCount = func(daemonID string) int {
					if daemonID == "id-a" {
						s.members[0].LiveRuns = 2
						return 2
					}
					return 1
				}
				return s
			},
			req:        poolRequest(""),
			want:       PlaceRunOutput{DaemonID: "id-b", DaemonName: "b", PoolID: "pool-1", PoolName: "cicd"},
			wantRecord: "id-b", wantRecordPool: "pool-1",
		},
		{
			name: "every attempt losing its slot waits",
			store: func() *fakeRunStore {
				s := newFakeRunStore(memberRow("a", "connected", 0))
				s.slotCount = func(string) int { return 2 }
				return s
			},
			req:        poolRequest(""),
			want:       PlaceRunOutput{Waiting: true, PoolID: "pool-1", PoolName: "cicd"},
			wantReason: "taken by a concurrent placement",
		},
		{
			name:    "an unknown pool is not found",
			store:   func() *fakeRunStore { return newFakeRunStore() },
			req:     func() RunRequest { r := poolRequest(""); r.Pool = "nope"; return r }(),
			wantErr: core.ErrMachinePoolNotFound,
		},
		{
			name:  "a pool and labels together are refused",
			store: func() *fakeRunStore { return newFakeRunStore(memberRow("a", "connected", 0)) },
			req: func() RunRequest {
				r := poolRequest("")
				r.Labels = map[string]string{"role": "gate"}
				return r
			}(),
			wantErr: ErrInvalidRunRequest,
		},
		{
			name:    "neither a pool nor labels is refused",
			store:   func() *fakeRunStore { return newFakeRunStore() },
			req:     func() RunRequest { r := poolRequest(""); r.Pool = ""; return r }(),
			wantErr: ErrInvalidRunRequest,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := tt.store()
			waker := &recordingWaker{}
			got, err := NewRunPlacer(store).WithWaker(waker).PlaceRun(context.Background(), tt.req)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				if len(store.placed) != 0 {
					t.Fatalf("recorded %v on an error", store.placed)
				}
				return
			}
			if err != nil {
				t.Fatalf("PlaceRun: %v", err)
			}
			if tt.wantReason != "" && !strings.Contains(got.Reason, tt.wantReason) {
				t.Fatalf("reason = %q, want it to contain %q", got.Reason, tt.wantReason)
			}
			got.Reason = ""
			if got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
			if strings.Join(waker.woke, ",") != strings.Join(tt.wantWoke, ",") {
				t.Fatalf("woke %v, want %v", waker.woke, tt.wantWoke)
			}
			rec := store.placed[tt.req.WorkflowID+"/"+tt.req.Scope]
			switch {
			case tt.wantRecord == "" && rec != nil:
				t.Fatalf("recorded %+v, want nothing (the run waits)", rec)
			case tt.wantRecord != "" && (rec == nil || rec.DaemonID != tt.wantRecord || rec.MachinePoolID != tt.wantRecordPool):
				t.Fatalf("recorded %+v, want %s on pool %q", rec, tt.wantRecord, tt.wantRecordPool)
			}
		})
	}
}

// Asking twice records once and answers the same member, even when the
// members' load changed in between.
func TestPlaceRun_IsIdempotent(t *testing.T) {
	store := newFakeRunStore(memberRow("a", "connected", 0), memberRow("b", "connected", 1))
	placer := NewRunPlacer(store)
	first, err := placer.PlaceRun(context.Background(), poolRequest("review"))
	if err != nil || first.DaemonID != "id-a" {
		t.Fatalf("first = %+v, %v; want a", first, err)
	}
	store.members[0].LiveRuns = 2
	again, err := placer.PlaceRun(context.Background(), poolRequest("review"))
	if err != nil || again != first {
		t.Fatalf("again = %+v, %v; want %+v", again, err, first)
	}
	if len(store.placed) != 1 || store.txs != 1 || store.locks != 1 {
		t.Fatalf("placed %d, %d transactions, %d locks; want one of each", len(store.placed), store.txs, store.locks)
	}
}

// The labels selector is recorded on a labels placement, with no pool.
func TestPlaceRun_LabelsRecordTheirSelector(t *testing.T) {
	store := newFakeRunStore()
	store.labelled = []core.MachinePoolMemberRow{memberRow("x", "connected", 0)}
	if _, err := NewRunPlacer(store).PlaceRun(context.Background(), labelsRequest()); err != nil {
		t.Fatal(err)
	}
	rec := store.placed["wf-1/"]
	if rec == nil || rec.MachinePoolID != "" || rec.Selector["role"] != "gate" || rec.ChatID != "chat-1" || rec.UserID != "u" {
		t.Fatalf("recorded %+v", rec)
	}
	if store.locks != 0 {
		t.Fatalf("a labels placement took %d pool locks; it has no pool", store.locks)
	}
}

type fakeChats map[string]*core.RunPlacementChat

func (f fakeChats) GetRunPlacementChat(_ context.Context, id string) (*core.RunPlacementChat, error) {
	if c, ok := f[id]; ok {
		return c, nil
	}
	return nil, core.ErrChatNotFound
}

func TestPlaceRunActivity(t *testing.T) {
	chats := fakeChats{"chat-1": {ID: "chat-1", UserID: "u", ProjectID: "proj", MachinePoolID: "pool-1", ActiveDaemonID: "id-b"}}
	store := newFakeRunStore(memberRow("a", "connected", 0), memberRow("b", "connected", 1))
	act := PlaceRunActivity{Chats: chats, Placer: NewRunPlacer(store)}

	tests := []struct {
		name     string
		in       PlaceRunInput
		want     string
		wantType string
		wantMsg  string
	}{
		{name: "the chat's owner and trigger placement are used", in: PlaceRunInput{ChatID: "chat-1", WorkflowID: "wf-1", Pool: "cicd"}, want: "id-b"},
		{name: "an unknown pool fails for good, saying what to do", in: PlaceRunInput{ChatID: "chat-1", WorkflowID: "wf-2", Pool: "gpu"},
			wantType: PoolNotFoundErrorType, wantMsg: `no machine pool named "gpu" — create it in Settings → Machines or fix the workflow's daemon: {pool: gpu}`},
		{name: "a missing chat fails for good", in: PlaceRunInput{ChatID: "chat-gone", WorkflowID: "wf-3", Pool: "cicd"}, wantType: invalidPlaceRunErrorType},
		{name: "an invalid request fails for good", in: PlaceRunInput{ChatID: "chat-1", WorkflowID: "wf-4"}, wantType: invalidPlaceRunErrorType},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := act.Execute(context.Background(), tt.in)
			if tt.wantType == "" {
				if err != nil || got.DaemonID != tt.want {
					t.Fatalf("got %+v, %v; want %s", got, err, tt.want)
				}
				return
			}
			var appErr *temporal.ApplicationError
			if !errors.As(err, &appErr) || appErr.Type() != tt.wantType || !appErr.NonRetryable() {
				t.Fatalf("err = %v, want a non-retryable %s", err, tt.wantType)
			}
			if tt.wantMsg != "" && appErr.Message() != tt.wantMsg {
				t.Fatalf("message = %q, want %q", appErr.Message(), tt.wantMsg)
			}
		})
	}
}
