// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// TestDaemonQueries ports the daemons hand SQL of the Repository and the
// daemon registry, interleaved with the Repository on the same database.
func TestDaemonQueries(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)

	// Registration through the store, read by the Repository.
	d1 := &core.Daemon{ID: "d1", UserID: "u1", Hostname: strp("host-1"), Platform: strp("linux"), DaemonType: strp("self_hosted")}
	if wrote, err := store.UpsertDaemonRegistration(ctx, env.ORM, d1, base, base); err != nil || !wrote {
		t.Fatalf("UpsertDaemonRegistration = %v, %v", wrote, err)
	}
	twin, err := env.Repo.GetDaemon(ctx, "d1")
	if err != nil || *twin.Hostname != "host-1" || *twin.DaemonType != "self_hosted" || !twin.CreatedAt.Equal(base) {
		t.Fatalf("Repo.GetDaemon = %+v, %v", twin, err)
	}
	// A re-registration keeps what it leaves nil; another owner writes nothing.
	if wrote, err := store.UpsertDaemonRegistration(ctx, env.ORM, &core.Daemon{ID: "d1", UserID: "u1", Platform: strp("darwin")}, base, base.Add(time.Minute)); err != nil || !wrote {
		t.Fatalf("UpsertDaemonRegistration(again) = %v, %v", wrote, err)
	}
	if wrote, err := store.UpsertDaemonRegistration(ctx, env.ORM, &core.Daemon{ID: "d1", UserID: "u2", Hostname: strp("evil")}, base, base); err != nil || wrote {
		t.Fatalf("UpsertDaemonRegistration(other owner) = %v, %v; want false", wrote, err)
	}
	got, err := store.GetDaemon(ctx, env.ORM, "d1")
	if err != nil || *got.Hostname != "host-1" || *got.Platform != "darwin" || got.UserID != "u1" || !got.UpdatedAt.Equal(base.Add(time.Minute)) {
		t.Fatalf("GetDaemon = %+v, %v", got, err)
	}
	if _, err := store.GetDaemon(ctx, env.ORM, "none"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetDaemon(missing) err = %v, want sql.ErrNoRows", err)
	}

	// Written by the Repository, listed by the store, newest update first.
	if err := env.Repo.UpsertDaemon(ctx, &core.Daemon{ID: "d2", UserID: "u1", Hostname: strp("host-2")}); err != nil {
		t.Fatalf("Repo.UpsertDaemon: %v", err)
	}
	list, err := store.ListDaemonsByUserID(ctx, env.ORM, "u1")
	if err != nil || len(list) != 2 || list[0].ID != "d2" || list[1].ID != "d1" {
		t.Fatalf("ListDaemonsByUserID = %+v, %v", list, err)
	}
	if twins, _ := env.Repo.ListDaemonsByUserID(ctx, "u1"); len(twins) != 2 || twins[0].ID != list[0].ID {
		t.Fatalf("Repo.ListDaemonsByUserID = %+v", twins)
	}

	// Lifecycle: newest wins; an omitted size keeps the stored one.
	lc := core.DaemonLifecycleUpdate{DaemonID: "d1", Phase: "ready", Size: "xl", StatusMessage: "up", ChangedAt: base, OOMKillCount: 2}
	if applied, err := store.ApplyDaemonLifecycle(ctx, env.ORM, lc); err != nil || !applied {
		t.Fatalf("ApplyDaemonLifecycle = %v, %v", applied, err)
	}
	older := core.DaemonLifecycleUpdate{DaemonID: "d1", Phase: "provisioning", ChangedAt: base.Add(-time.Minute)}
	if applied, err := store.ApplyDaemonLifecycle(ctx, env.ORM, older); err != nil || applied {
		t.Fatalf("ApplyDaemonLifecycle(older) = %v, %v; want dropped", applied, err)
	}
	oom := base.Add(time.Second)
	newer := core.DaemonLifecycleUpdate{DaemonID: "d1", Phase: "suspended", ChangedAt: base.Add(time.Minute), LastOOMKilledAt: &oom, OOMKillCount: 1}
	if applied, err := env.Repo.ApplyDaemonLifecycle(ctx, newer); err != nil || !applied {
		t.Fatalf("Repo.ApplyDaemonLifecycle = %v, %v", applied, err)
	}
	got, _ = store.GetDaemon(ctx, env.ORM, "d1")
	if *got.LifecyclePhase != "suspended" || *got.Size != "xl" || got.OOMKillCount != 2 || got.LastOOMKilledAt == nil || got.LastStatusMessage != "" {
		t.Fatalf("after lifecycle = %+v", got)
	}

	// Local models and storage state.
	if err := store.SetDaemonLocalModels(ctx, env.ORM, "d1", `{"models":[]}`); err != nil {
		t.Fatalf("SetDaemonLocalModels: %v", err)
	}
	if m, err := env.Repo.ListDaemonLocalModels(ctx, "u1"); err != nil || len(m) != 1 || m["d1"] != `{"models":[]}` {
		t.Fatalf("Repo.ListDaemonLocalModels = %v, %v", m, err)
	}
	if m, err := store.ListDaemonLocalModels(ctx, env.ORM, "u1"); err != nil || len(m) != 1 {
		t.Fatalf("ListDaemonLocalModels = %v, %v", m, err)
	}
	if err := env.Repo.SetDaemonStorageState(ctx, "d2", `{"free":1}`); err != nil {
		t.Fatalf("Repo.SetDaemonStorageState: %v", err)
	}
	if s, err := store.GetDaemonStorageState(ctx, env.ORM, "d2"); err != nil || s != `{"free":1}` {
		t.Fatalf("GetDaemonStorageState = %q, %v", s, err)
	}
	if s, err := store.GetDaemonStorageState(ctx, env.ORM, "none"); err != nil || s != "" {
		t.Fatalf("GetDaemonStorageState(missing) = %q, %v", s, err)
	}
	if err := store.SetDaemonStorageState(ctx, env.ORM, "d1", `{"free":2}`); err != nil {
		t.Fatalf("SetDaemonStorageState: %v", err)
	}
	if m, err := store.ListDaemonStorageState(ctx, env.ORM, "u1"); err != nil || len(m) != 2 || m["d1"] != `{"free":2}` {
		t.Fatalf("ListDaemonStorageState = %v, %v", m, err)
	}

	// Identity upsert: create, no-op when unchanged, write on a new name.
	ident := core.DaemonIdentity{DaemonID: "d3", UserID: "u1", Name: "box", DaemonType: "managed"}
	if wrote, err := store.UpsertDaemonIdentity(ctx, env.ORM, ident, base, base); err != nil || !wrote {
		t.Fatalf("UpsertDaemonIdentity = %v, %v", wrote, err)
	}
	if wrote, err := store.UpsertDaemonIdentity(ctx, env.ORM, ident, base, base.Add(time.Minute)); err != nil || wrote {
		t.Fatalf("UpsertDaemonIdentity(unchanged) = %v, %v; want no-op", wrote, err)
	}
	ident.Name = "box-2"
	if wrote, err := env.Repo.UpsertDaemonIdentity(ctx, ident); err != nil || !wrote {
		t.Fatalf("Repo.UpsertDaemonIdentity(renamed) = %v, %v", wrote, err)
	}
	if d, _ := store.GetDaemon(ctx, env.ORM, "d3"); d.Name != "box-2" || *d.DaemonType != "managed" {
		t.Fatalf("after identity = %+v", d)
	}
	if wrote, err := store.UpsertDaemonIdentity(ctx, env.ORM, core.DaemonIdentity{DaemonID: "d4", UserID: "u1"}, base, base); err != nil || !wrote {
		t.Fatalf("UpsertDaemonIdentity(no type) = %v, %v", wrote, err)
	}
	if d, _ := env.Repo.GetDaemon(ctx, "d4"); d.DaemonType != nil {
		t.Fatalf("empty daemon type stored as %q, want NULL", *d.DaemonType)
	}

	// Last seen: the lease while it exists, then the released lease's mark.
	if seen, err := store.DaemonLastSeenAt(ctx, env.ORM, "u1", "d1"); err != nil || !seen.IsZero() {
		t.Fatalf("DaemonLastSeenAt(never) = %v, %v", seen, err)
	}
	att := &core.DaemonAttachment{DaemonID: "d1", UserID: "u1", Source: core.DaemonAttachmentSourceInbound, AttachedAt: base, LastStreamActivity: base.Add(time.Minute)}
	if wrote, err := store.UpsertDaemonAttachment(ctx, env.ORM, att); err != nil || !wrote {
		t.Fatalf("UpsertDaemonAttachment = %v, %v", wrote, err)
	}
	if seen, err := store.DaemonLastSeenAt(ctx, env.ORM, "u1", "d1"); err != nil || !seen.Equal(base.Add(time.Minute)) {
		t.Fatalf("DaemonLastSeenAt(leased) = %v, %v", seen, err)
	}
	if err := store.ReleaseDaemonAttachment(ctx, env.ORM, "d1", base); err != nil {
		t.Fatalf("ReleaseDaemonAttachment: %v", err)
	}
	if seen, err := env.Repo.DaemonLastSeenAt(ctx, "u1", "d1"); err != nil || !seen.Equal(base.Add(time.Minute)) {
		t.Fatalf("Repo.DaemonLastSeenAt(released) = %v, %v", seen, err)
	}
	if seen, err := store.DaemonLastSeenAt(ctx, env.ORM, "u2", "d1"); err != nil || !seen.IsZero() {
		t.Fatalf("DaemonLastSeenAt(other user) = %v, %v", seen, err)
	}

	// Snapshot pieces, in one transaction as the registry runs them.
	wt := newWorktree("wt-d", "on-d2", "b", base)
	wt.DaemonID = strp("d2")
	if err := store.InsertWorktree(ctx, env.ORM, wt); err != nil {
		t.Fatalf("InsertWorktree: %v", err)
	}
	if err := store.RecordProjectDaemonInstalled(ctx, env.ORM, storetest.SeedProjectID, "d2", "/srv/d2", nil); err != nil {
		t.Fatalf("RecordProjectDaemonInstalled: %v", err)
	}
	if _, err := store.UpsertDaemonAttachment(ctx, env.ORM, &core.DaemonAttachment{DaemonID: "d4", UserID: "u1", Source: core.DaemonAttachmentSourceInbound, AttachedAt: time.Now(), LastStreamActivity: time.Now()}); err != nil {
		t.Fatalf("UpsertDaemonAttachment(d4): %v", err)
	}
	var absent []string
	err = env.ORM.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
		var err error
		absent, err = store.LockDaemonsAbsentFromSnapshot(ctx, env.ORM, "u1", []string{"d3"}, time.Now().Add(time.Hour), time.Now().Add(-time.Minute))
		return err
	})
	if err != nil || len(absent) != 2 {
		t.Fatalf("LockDaemonsAbsentFromSnapshot = %v, %v; want d1 and d2 (d3 kept, d4 attached)", absent, err)
	}
	if ids, err := store.LockDaemonsAbsentFromSnapshot(ctx, env.ORM, "u1", nil, time.Now().Add(time.Hour), time.Now().Add(time.Hour)); err != nil || len(ids) != 4 {
		t.Fatalf("LockDaemonsAbsentFromSnapshot(keep nothing) = %v, %v", ids, err)
	}
	if err := store.ReleaseDaemonFromWorkspaces(ctx, env.ORM, "d2"); err != nil {
		t.Fatalf("ReleaseDaemonFromWorkspaces: %v", err)
	}
	if w, _ := env.Repo.GetWorktree(ctx, "wt-d"); w.DaemonID != nil {
		t.Fatalf("worktree still bound to removed daemon: %v", *w.DaemonID)
	}
	if pds, _ := env.Repo.ListProjectDaemonsForDaemon(ctx, "d2"); len(pds) != 0 {
		t.Fatalf("installs on removed daemon survived: %+v", pds)
	}
	if owner, removed, err := store.DeleteDaemonRow(ctx, env.ORM, "d4"); err != nil || !removed || owner != "u1" {
		t.Fatalf("DeleteDaemonRow = %q, %v, %v", owner, removed, err)
	}
	if _, removed, err := store.DeleteDaemonRow(ctx, env.ORM, "d4"); err != nil || removed {
		t.Fatalf("DeleteDaemonRow(again) = %v, %v; want not removed", removed, err)
	}
	if all, _ := env.Repo.ListAllDaemonAttachments(ctx); len(all) != 0 {
		t.Fatalf("lease of removed daemon survived: %+v", all)
	}
}

// TestDaemonAttachmentQueries ports the daemon_attachment hand SQL.
func TestDaemonAttachmentQueries(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	for _, d := range []*core.Daemon{{ID: "a1", UserID: "u1"}, {ID: "a2", UserID: "u1"}, {ID: "b1", UserID: "u2"}} {
		if err := env.Repo.UpsertDaemon(ctx, d); err != nil {
			t.Fatalf("Repo.UpsertDaemon(%s): %v", d.ID, err)
		}
	}

	// Upsert through the store, read by the Repository.
	port := 9000
	a1 := &core.DaemonAttachment{DaemonID: "a1", UserID: "u1", Source: core.DaemonAttachmentSourceOutbound, PodIP: strp("10.0.0.1"), PodPort: &port, AttachedAt: now, LastStreamActivity: now}
	if wrote, err := store.UpsertDaemonAttachment(ctx, env.ORM, a1); err != nil || !wrote {
		t.Fatalf("UpsertDaemonAttachment = %v, %v", wrote, err)
	}
	// Another user's attachment for a1 is refused.
	if wrote, err := store.UpsertDaemonAttachment(ctx, env.ORM, &core.DaemonAttachment{DaemonID: "a1", UserID: "u2", Source: core.DaemonAttachmentSourceInbound, AttachedAt: now, LastStreamActivity: now}); err != nil || wrote {
		t.Fatalf("UpsertDaemonAttachment(other user) = %v, %v; want refused", wrote, err)
	}
	out, err := env.Repo.ListOutboundAttachments(ctx)
	if err != nil || len(out) != 1 || *out[0].PodIP != "10.0.0.1" || *out[0].PodPort != 9000 || out[0].Source != core.DaemonAttachmentSourceOutbound {
		t.Fatalf("Repo.ListOutboundAttachments = %+v, %v", out, err)
	}

	// Telemetry and ports through the store; a missing lease is a no-op.
	if err := store.SetDaemonAttachmentMemory(ctx, env.ORM, "a1", 100, 200, true); err != nil {
		t.Fatalf("SetDaemonAttachmentMemory: %v", err)
	}
	if err := store.SetDaemonAttachmentPorts(ctx, env.ORM, "a1", []uint32{3000, 8080}); err != nil {
		t.Fatalf("SetDaemonAttachmentPorts: %v", err)
	}
	if err := store.SetDaemonAttachmentMemory(ctx, env.ORM, "none", 1, 2, false); err != nil {
		t.Fatalf("SetDaemonAttachmentMemory(missing): %v", err)
	}
	if err := store.SetDaemonAttachmentPorts(ctx, env.ORM, "none", nil); err != nil {
		t.Fatalf("SetDaemonAttachmentPorts(missing): %v", err)
	}
	fresh, err := env.Repo.ListFreshDaemonAttachmentsForUser(ctx, "u1", time.Minute)
	if err != nil || len(fresh) != 1 || fresh[0].MemoryUsedBytes != 100 || !fresh[0].MemoryPressure || len(fresh[0].DetectedPorts) != 2 || !fresh[0].LastStreamActivity.Equal(now) {
		t.Fatalf("Repo.ListFreshDaemonAttachmentsForUser = %+v, %v", fresh, err)
	}

	// Renew: forward only, re-creates a lost lease, refuses another user.
	if created, err := store.RenewDaemonAttachment(ctx, env.ORM, &core.DaemonAttachment{DaemonID: "a1", UserID: "u1", Source: core.DaemonAttachmentSourceOutbound, AttachedAt: now, LastStreamActivity: now.Add(time.Second)}); err != nil || created {
		t.Fatalf("RenewDaemonAttachment = %v, %v; want renewed, not created", created, err)
	}
	if created, err := store.RenewDaemonAttachment(ctx, env.ORM, &core.DaemonAttachment{DaemonID: "a1", UserID: "u1", Source: core.DaemonAttachmentSourceOutbound, AttachedAt: now, LastStreamActivity: now.Add(-time.Hour)}); err != nil || created {
		t.Fatalf("RenewDaemonAttachment(older) = %v, %v", created, err)
	}
	if created, err := store.RenewDaemonAttachment(ctx, env.ORM, &core.DaemonAttachment{DaemonID: "a2", UserID: "u1", Source: core.DaemonAttachmentSourceInbound, AttachedAt: now, LastStreamActivity: now}); err != nil || !created {
		t.Fatalf("RenewDaemonAttachment(lost lease) = %v, %v; want created", created, err)
	}
	if created, err := store.RenewDaemonAttachment(ctx, env.ORM, &core.DaemonAttachment{DaemonID: "b1", UserID: "u1", Source: core.DaemonAttachmentSourceInbound, AttachedAt: now, LastStreamActivity: now}); err != nil || created {
		t.Fatalf("RenewDaemonAttachment(not the user's daemon) = %v, %v", created, err)
	}
	if created, err := env.Repo.RenewDaemonAttachment(ctx, &core.DaemonAttachment{DaemonID: "a2", UserID: "u1", Source: core.DaemonAttachmentSourceInbound, AttachedAt: now, LastStreamActivity: now.Add(time.Second)}); err != nil || created {
		t.Fatalf("Repo.RenewDaemonAttachment = %v, %v", created, err)
	}
	all, err := store.ListAllDaemonAttachments(ctx, env.ORM)
	if err != nil || len(all) != 2 {
		t.Fatalf("ListAllDaemonAttachments = %+v, %v", all, err)
	}
	for _, a := range all {
		if !a.LastStreamActivity.Equal(now.Add(time.Second)) {
			t.Fatalf("lease %s last_stream_activity = %v, want the newest renewal", a.DaemonID, a.LastStreamActivity)
		}
		if a.DaemonID == "a1" && (a.MemoryLimitBytes != 200 || len(a.DetectedPorts) != 2) {
			t.Fatalf("renewal touched telemetry: %+v", a)
		}
	}

	// Liveness reads against their twins.
	cutoff := now.Add(-time.Minute)
	if ok, err := store.IsUserDaemonAttached(ctx, env.ORM, "u1", cutoff); err != nil || !ok {
		t.Fatalf("IsUserDaemonAttached = %v, %v", ok, err)
	}
	if ok, err := store.IsUserDaemonAttached(ctx, env.ORM, "u2", cutoff); err != nil || ok {
		t.Fatalf("IsUserDaemonAttached(u2) = %v, %v", ok, err)
	}
	if ok, err := store.IsDaemonIDAttached(ctx, env.ORM, "a2", cutoff); err != nil || !ok {
		t.Fatalf("IsDaemonIDAttached = %v, %v", ok, err)
	}
	if ok, err := store.IsDaemonIDAttached(ctx, env.ORM, "a2", now.Add(time.Minute)); err != nil || ok {
		t.Fatalf("IsDaemonIDAttached(stale) = %v, %v", ok, err)
	}
	if ok, err := env.Repo.IsDaemonIDAttached(ctx, "a2", time.Hour); err != nil || !ok {
		t.Fatalf("Repo.IsDaemonIDAttached = %v, %v", ok, err)
	}
	ids, err := store.ListAttachedDaemonIDsForUser(ctx, env.ORM, "u1", cutoff)
	if err != nil || len(ids) != 2 {
		t.Fatalf("ListAttachedDaemonIDsForUser = %v, %v", ids, err)
	}
	if fresh, err := store.ListFreshDaemonAttachmentsForUser(ctx, env.ORM, "u1", cutoff); err != nil || len(fresh) != 2 {
		t.Fatalf("ListFreshDaemonAttachmentsForUser = %+v, %v", fresh, err)
	}
	if outbound, err := store.ListOutboundAttachments(ctx, env.ORM); err != nil || len(outbound) != 1 || outbound[0].DaemonID != "a1" || *outbound[0].PodPort != 9000 {
		t.Fatalf("ListOutboundAttachments = %+v, %v", outbound, err)
	}

	// Release only deletes a lease at or before the session's attached_at.
	if err := store.ReleaseDaemonAttachment(ctx, env.ORM, "a2", now.Add(-time.Second)); err != nil {
		t.Fatalf("ReleaseDaemonAttachment(older session): %v", err)
	}
	if ok, _ := env.Repo.IsDaemonIDAttached(ctx, "a2", time.Hour); !ok {
		t.Fatal("an older session's release deleted a newer lease")
	}
	// The reaper deletes leases not renewed since its cutoff.
	if n, err := store.DeleteStaleDaemonAttachments(ctx, env.ORM, now.Add(time.Hour)); err != nil || n != 2 {
		t.Fatalf("DeleteStaleDaemonAttachments = %d, %v", n, err)
	}
	if n, err := env.Repo.DeleteStaleDaemonAttachments(ctx, time.Minute); err != nil || n != 0 {
		t.Fatalf("Repo.DeleteStaleDaemonAttachments = %d, %v", n, err)
	}
}

// TestBackgroundProcessQueries ports the background_processes and
// background_process_output hand SQL.
func TestBackgroundProcessQueries(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	running := reliantv1.BackgroundProcessStatus_BACKGROUND_PROCESS_STATUS_RUNNING
	completed := reliantv1.BackgroundProcessStatus_BACKGROUND_PROCESS_STATUS_COMPLETED

	seedChatThread(t, env, "chat-1", "thread-1")
	if err := store.InsertWorktree(ctx, env.ORM, newWorktree("wt-1", "bg", "bg", base)); err != nil {
		t.Fatalf("InsertWorktree: %v", err)
	}
	pid, pkg := 42, "2"
	p1 := &core.BackgroundProcess{
		ID: "p1", PID: &pid, Command: "npm run dev", WorkingDir: "/srv", WorktreeID: strp("wt-1"), ProjectID: strp(storetest.SeedProjectID),
		UserID: "u1", Status: running, StartedAt: base, SourceType: core.BgProcessSourcePackageCommand, PackageType: &pkg,
		CommandName: strp("dev"), CreatedAt: base, UpdatedAt: base,
	}
	if err := store.InsertBackgroundProcess(ctx, env.ORM, p1); err != nil {
		t.Fatalf("InsertBackgroundProcess: %v", err)
	}
	twin, err := env.Repo.GetBackgroundProcess(ctx, "p1")
	if err != nil || *twin.PID != 42 || *twin.PackageType != "2" || twin.SourceType != core.BgProcessSourcePackageCommand || twin.Status != running || !twin.UpdatedAt.Equal(base) {
		t.Fatalf("Repo.GetBackgroundProcess = %+v, %v", twin, err)
	}
	p2 := &core.BackgroundProcess{ID: "p2", Command: "make", WorkingDir: "/srv", ChatID: strp("chat-1"), UserID: "u1", Status: running,
		StartedAt: base.Add(time.Minute), SourceType: core.BgProcessSourceLLM, CreatedAt: base, UpdatedAt: base}
	if err := env.Repo.CreateBackgroundProcess(ctx, p2); err != nil {
		t.Fatalf("Repo.CreateBackgroundProcess: %v", err)
	}
	got, err := store.GetBackgroundProcess(ctx, env.ORM, "p2")
	if err != nil || got.PID != nil || got.PackageType != nil || *got.ChatID != "chat-1" || got.SourceType != core.BgProcessSourceLLM {
		t.Fatalf("GetBackgroundProcess = %+v, %v", got, err)
	}
	if _, err := store.GetBackgroundProcess(ctx, env.ORM, "none"); err == nil || err.Error() != "process not found" {
		t.Fatalf("GetBackgroundProcess(missing) err = %v", err)
	}
	// An unset source type is the column DEFAULT.
	if err := store.InsertBackgroundProcess(ctx, env.ORM, &core.BackgroundProcess{ID: "p3", Command: "x", WorkingDir: "/", UserID: "u2", Status: completed, StartedAt: base.Add(-time.Minute)}); err != nil {
		t.Fatalf("InsertBackgroundProcess(defaults): %v", err)
	}
	if p, _ := env.Repo.GetBackgroundProcess(ctx, "p3"); p.SourceType != core.BgProcessSourceLLM {
		t.Fatalf("defaulted source type = %q, want llm", p.SourceType)
	}

	// Filtered lists against their twins.
	src := core.BgProcessSourcePackageCommand
	for name, f := range map[string]core.BackgroundProcessFilters{
		"user":    {UserID: "u1"},
		"status":  {Status: &running},
		"source":  {SourceType: &src},
		"chat":    {ChatID: strp("chat-1")},
		"project": {ProjectID: strp(storetest.SeedProjectID), WorktreeID: strp("wt-1")},
		"page":    {Limit: 1, Offset: 1},
	} {
		got, err := store.ListBackgroundProcesses(ctx, env.ORM, f)
		if err != nil {
			t.Fatalf("ListBackgroundProcesses(%s): %v", name, err)
		}
		want, err := env.Repo.ListBackgroundProcesses(ctx, f)
		if err != nil {
			t.Fatalf("Repo.ListBackgroundProcesses(%s): %v", name, err)
		}
		if len(got) != len(want) || len(got) == 0 {
			t.Fatalf("ListBackgroundProcesses(%s) = %d rows, twin %d", name, len(got), len(want))
		}
		for i := range got {
			if got[i].ID != want[i].ID {
				t.Fatalf("ListBackgroundProcesses(%s)[%d] = %s, twin %s", name, i, got[i].ID, want[i].ID)
			}
		}
	}
	if all, _ := store.ListBackgroundProcesses(ctx, env.ORM, core.BackgroundProcessFilters{UserID: "u1"}); all[0].ID != "p2" {
		t.Fatalf("ListBackgroundProcesses order = %s first, want p2 (newest start)", all[0].ID)
	}

	// Status, pid, running, stale, cleanup.
	exit, ended := 0, base.Add(2*time.Minute)
	if err := store.SetBackgroundProcessStatus(ctx, env.ORM, "p1", completed, &exit, &ended); err != nil {
		t.Fatalf("SetBackgroundProcessStatus: %v", err)
	}
	if p, _ := env.Repo.GetBackgroundProcess(ctx, "p1"); p.Status != completed || *p.ExitCode != 0 || !p.EndedAt.Equal(ended) || !p.UpdatedAt.After(base) {
		t.Fatalf("after status = %+v", p)
	}
	runningRows, err := store.ListRunningBackgroundProcesses(ctx, env.ORM)
	if err != nil || len(runningRows) != 1 || runningRows[0].ID != "p2" {
		t.Fatalf("ListRunningBackgroundProcesses = %+v, %v", runningRows, err)
	}
	// No twin: the Repository's hand SQL sent an unbound `?` and failed on
	// Postgres every time (42601), so startup recovery never saw a process.
	if err := store.MarkStaleBackgroundProcesses(ctx, env.ORM, []string{"p2", "none"}); err != nil {
		t.Fatalf("MarkStaleBackgroundProcesses: %v", err)
	}
	if err := store.MarkStaleBackgroundProcesses(ctx, env.ORM, nil); err != nil {
		t.Fatalf("MarkStaleBackgroundProcesses(none): %v", err)
	}
	if p, _ := env.Repo.GetBackgroundProcess(ctx, "p2"); p.Status != reliantv1.BackgroundProcessStatus_BACKGROUND_PROCESS_STATUS_STALE || p.EndedAt == nil {
		t.Fatalf("after stale = %+v", p)
	}
	// p3 has no ended_at, so only p1 (ended in the past) and p2 (ended now) qualify.
	if n, err := store.DeleteEndedBackgroundProcesses(ctx, env.ORM, base.Add(3*time.Minute)); err != nil || n != 1 {
		t.Fatalf("DeleteEndedBackgroundProcesses = %d, %v; want p1", n, err)
	}
	if n, err := env.Repo.CleanupOldBackgroundProcesses(ctx, time.Now().Add(time.Hour)); err != nil || n != 1 {
		t.Fatalf("Repo.CleanupOldBackgroundProcesses = %d, %v; want p2", n, err)
	}

	// Output: written in one bound batch, read back in order after a seq.
	lines := []core.BackgroundProcessOutputLine{
		{ProcessID: "p3", Seq: 1, Stream: "stdout", Line: "one"},
		{ProcessID: "p3", Seq: 2, Stream: "stderr", Line: "two ? 'quoted'"},
		{ProcessID: "p3", Seq: 3, Stream: "stdout", Line: ""},
	}
	if err := store.InsertBackgroundProcessOutput(ctx, env.ORM, lines); err != nil {
		t.Fatalf("InsertBackgroundProcessOutput: %v", err)
	}
	if err := store.InsertBackgroundProcessOutput(ctx, env.ORM, nil); err != nil {
		t.Fatalf("InsertBackgroundProcessOutput(empty): %v", err)
	}
	if err := env.Repo.CreateBackgroundProcessOutputBatch(ctx, []core.BackgroundProcessOutputLine{{ProcessID: "p3", Seq: 4, Stream: "stdout", Line: "four"}}); err != nil {
		t.Fatalf("Repo.CreateBackgroundProcessOutputBatch: %v", err)
	}
	read, err := store.ReadBackgroundProcessOutput(ctx, env.ORM, "p3", 1, 2)
	if err != nil || len(read) != 2 || read[0].Seq != 2 || read[0].Line != "two ? 'quoted'" || read[0].Stream != "stderr" || read[1].Line != "" || read[0].CreatedAt.IsZero() {
		t.Fatalf("ReadBackgroundProcessOutput = %+v, %v", read, err)
	}
	twinRead, err := env.Repo.GetBackgroundProcessOutput(ctx, "p3", 0, 0)
	if err != nil || len(twinRead) != 4 || twinRead[3].Line != "four" {
		t.Fatalf("Repo.GetBackgroundProcessOutput = %+v, %v", twinRead, err)
	}
}
