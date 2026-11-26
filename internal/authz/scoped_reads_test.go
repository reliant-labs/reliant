// Copyright (c) 2025 Reliant Labs

package authz

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// How a user-scoped repository read reaches the org boundary. The store
// filters these reads on their owner only, so owning a row is not enough to
// see it: every one is classified here.
const (
	// readRepoChecks: the internal/db facade decides each row through authz.
	readRepoChecks = "the repository decides each row through authz"
	// readCallersFilter: every caller decides the rows (authz.Visible, a
	// Can* per row, or the stream's org gate) before showing or acting on one.
	readCallersFilter = "every caller decides the rows through authz"
	// readParentAuthorized: keyed by a parent (project, trigger) the caller
	// authorized first, or a uniqueness check that must see every row.
	readParentAuthorized = "keyed by a parent the caller authorized first, or a uniqueness check"
	// readPersonal: the user's own settings, credentials, usage and
	// preferences; they belong to no org.
	readPersonal = "personal: belongs to no org"
	// readOrgless: machines and their state, and trigger firings, which
	// belong to no org (authz decides them owner-only).
	readOrgless = "orgless: machines, their disks and firings"
	// readBackground: the owner's own background work (delivery, sweeps,
	// capacity accounting), shown to no caller; a launch it leads to is
	// decided by authz in the launcher.
	readBackground = "background owner work, shown to no caller"
)

// userScopedReads classifies every exported *Repo read in internal/db that
// takes a user id or a *Filters struct. A new one fails
// TestEveryUserScopedReadIsClassified until it is added here.
var userScopedReads = map[string]string{
	"GetProjectWithUserCheck":  readRepoChecks,
	"GetUsableWorkflowBySlug":  readRepoChecks,
	"GetWorkflowDraftByName":   readRepoChecks,
	"GetWorkflowDraftBySlug":   readRepoChecks,
	"GetWorkflowDraftsBySlugs": readRepoChecks,
	// Deprecated forwarders still decide visibility through workflowdrafts.
	"ListWorkflowDraftsByUser": readRepoChecks,

	"ListArchivedChats":        readCallersFilter,
	"ListChats":                readCallersFilter,
	"ListMainWorktreesForUser": readCallersFilter,
	"ListProjects":             readCallersFilter,
	"ListRuns":                 readCallersFilter,
	// The takeover decides each offer: its offeree and offerer must be live
	// members of its org, or an admin of it asked (internal/orgtakeover).
	// The inbox's P11 notices (inbox_automation_notices.go): each row is the
	// caller's own, decided by CanReadTrigger, or one of an org they
	// administer (control-plane's live answer).
	// The account loader counts a grant only for a live member of the org,
	// and only when it postdates their current membership (internal/llm/
	// drivers, org-provider-accounts §3.3).
	// A project's moves span two orgs: ProjectService decides each with
	// canReadMove (its owner or actor through authz.CanReadOwn on either org,
	// or authz.RequireOrgAdmin of either), the list and a retried request alike.
	// "Recently deleted": ProjectService decides each row again with
	// authz.ProjectDelete (its owner, a live member; or an admin of its org,
	// asked fresh, once the owner left).
	"ListUnlaunchedTriggerEvents": readCallersFilter,
	// The inbox passes each incident through authz.Visible (itemReads.incident:
	// its chat and its project) before showing it.
	"ListAttentionIncidents":       readCallersFilter,
	"SearchChats":                  readCallersFilter,
	"GetUserUpdatesSince":          readCallersFilter,
	"GetProjectByPathAndUser":      readCallersFilter,
	"GetProjectByRemoteURLAndUser": readCallersFilter,

	"GetPresetBySlugAndProject": readParentAuthorized,
	"ListPresetsByTag":          readParentAuthorized,
	"ListUserPresetsByProject":  readParentAuthorized,
	"ListWorktrees":             readParentAuthorized,
	// The caller's own connection, metadata only (the ciphertext is internal/credentials/connectionvault's
	// alone): the trigger paths resolve an activation's owner and its connection through it.
	"GetConnection":               readPersonal,
	"GetIntegrationAccessSubject": readPersonal,
	// Whether the caller's own grants reach a repository: PullRequestRecordService's gate.
	"GetLatestUserUpdateSequence": readPersonal,
	"GetPresetBySlug":             readPersonal,
	"GetSetting":                  readPersonal,
	"GetSettingValue":             readPersonal,
	"ListSettings":                readPersonal,
	"ListSettingsByKey":           readPersonal,
	"ListVisibilityOverrides":     readPersonal,

	"IsDaemonAttached":                  readOrgless,
	"ListAttachedDaemonIDsForUser":      readOrgless,
	"ListDaemonLocalModels":             readOrgless,
	"ListDaemonStorageState":            readOrgless,
	"ListDaemonsByUserID":               readOrgless,
	"ListFreshDaemonAttachmentsForUser": readOrgless,

	"ListCapacityWakeTargets": readBackground,
}

var (
	scopedReadName  = regexp.MustCompile(`^(Get|List|Search|Recent|Count|Has|Firings|Find|Is|Lookup|Workflow\w*Exists)`)
	scopedUserParam = regexp.MustCompile(`(?i)^(user|owner|owneruser|acting)(id)?$|userid$`)
)

// TestEveryUserScopedReadIsClassified fails on an exported *Repo read in
// internal/db that takes a user id or a *Filters struct and is not in
// userScopedReads, and on an entry that names no such read any more. The
// store scopes these reads to an owner, not to an org: decide where the org
// boundary is enforced for a new one before adding it.
//
// It reads every file itself (os.ReadFile), so Go's test cache sees each one.
func TestEveryUserScopedReadIsClassified(t *testing.T) {
	dir := filepath.Join("..", "db")
	found := map[string]bool{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != dir {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, src, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || !fn.Name.IsExported() || !scopedReadName.MatchString(fn.Name.Name) || !isRepoReceiver(fn) {
				continue
			}
			if takesUserScope(fn) {
				found[fn.Name.Name] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var missing, stale []string
	for name := range found {
		if _, ok := userScopedReads[name]; !ok {
			missing = append(missing, name)
		}
	}
	for name := range userScopedReads {
		if !found[name] {
			stale = append(stale, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	for _, name := range missing {
		t.Errorf("(*Repo).%s reads a user's rows but is not classified: decide where it meets the org boundary and add it to userScopedReads", name)
	}
	for _, name := range stale {
		t.Errorf("userScopedReads[%q] names no user-scoped read any more: remove it", name)
	}
}

func isRepoReceiver(fn *ast.FuncDecl) bool {
	star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	id, ok := star.X.(*ast.Ident)
	return ok && id.Name == "Repo"
}

// takesUserScope reports whether fn takes a string user id or a *Filters
// struct (which carries one).
func takesUserScope(fn *ast.FuncDecl) bool {
	for _, field := range fn.Type.Params.List {
		typeName := ""
		switch ty := field.Type.(type) {
		case *ast.Ident:
			typeName = ty.Name
		case *ast.SelectorExpr:
			typeName = ty.Sel.Name
		}
		if strings.HasSuffix(typeName, "Filters") {
			return true
		}
		for _, name := range field.Names {
			if typeName == "string" && scopedUserParam.MatchString(name.Name) {
				return true
			}
		}
	}
	return false
}
