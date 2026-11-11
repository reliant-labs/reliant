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
	// readUnused: nothing outside internal/db calls it yet. Classify it
	// again before the first caller.
	readUnused = "no caller outside internal/db"
)

// userScopedReads classifies every exported *Repo read in internal/db that
// takes a user id or a *Filters struct. A new one fails
// TestEveryUserScopedReadIsClassified until it is added here.
var userScopedReads = map[string]string{
	"GetChatRowWithUserCheck":  readRepoChecks,
	"GetChatWithUserCheck":     readRepoChecks,
	"GetProjectWithUserCheck":  readRepoChecks,
	"GetUsableWorkflowBySlug":  readRepoChecks,
	"GetWorkflowDraftByName":   readRepoChecks,
	"GetWorkflowDraftBySlug":   readRepoChecks,
	"ListWorkflowDraftsByUser": readRepoChecks,

	"ListAnswerablePendingQuestionsByUser": readCallersFilter,
	"ListArchivedChats":                    readCallersFilter,
	"ListChats":                            readCallersFilter,
	"ListInboxPending":                     readCallersFilter,
	"ListMainWorktreesForUser":             readCallersFilter,
	"ListProjects":                         readCallersFilter,
	"ListRuns":                             readCallersFilter,
	"ListTriggers":                         readCallersFilter,
	"ListUnlaunchedTriggerEvents":          readCallersFilter,
	"ListWorktreeRecoveries":               readCallersFilter,
	"SearchChats":                          readCallersFilter,
	"GetUserUpdatesSince":                  readCallersFilter,
	"GetProjectByPathAndUser":              readCallersFilter,
	"GetProjectByRemoteURLAndUser":         readCallersFilter,

	"FiringsSinceLastSuccess":   readParentAuthorized,
	"GetPresetBySlugAndProject": readParentAuthorized,
	"GetWorktreeRecovery":       readParentAuthorized,
	"ListPresetsByTag":          readParentAuthorized,
	"ListTriggerChanges":        readParentAuthorized,
	"ListTriggerRegistrations":  readParentAuthorized,
	"ListUserPresetsByProject":  readParentAuthorized,
	"ListWorktrees":             readParentAuthorized,
	"RecentTriggerFirings":      readParentAuthorized,
	"RecentTriggerOutcomes":     readParentAuthorized,
	"WorkflowSlugExists":        readParentAuthorized,

	"GetAnalyticsEnabled":             readPersonal,
	"GetAutomationToken":              readPersonal,
	"GetIntegrationAccessRefresh":     readPersonal,
	"GetIntegrationAccessSubject":     readPersonal,
	"GetLatestUserUpdateSequence":     readPersonal,
	"GetMachinePoolByName":            readPersonal,
	"GetMachinePoolForUser":           readPersonal,
	"GetModelEndpoint":                readPersonal,
	"GetPresetBySlug":                 readPersonal,
	"GetPrivacySettings":              readPersonal,
	"GetProviderAccountWithSecrets":   readPersonal,
	"GetSetting":                      readPersonal,
	"GetSettingValue":                 readPersonal,
	"GetVisibilityOverride":           readPersonal,
	"ListAccountLimits":               readPersonal,
	"ListCommandFavorites":            readPersonal,
	"ListDismissedInboxItemIDs":       readPersonal,
	"ListLLMUsageDaily":               readPersonal,
	"ListMachinePoolsForUser":         readPersonal,
	"ListModelEndpoints":              readPersonal,
	"ListProviderAccounts":            readPersonal,
	"ListProviderAccountsWithSecrets": readPersonal,
	"ListSettings":                    readPersonal,
	"ListSettingsByKey":               readPersonal,
	"ListUserPresets":                 readUnused,
	"ListUserPresetsGlobal":           readUnused,
	"ListVisibilityOverrides":         readPersonal,

	"IsDaemonAttached":                  readOrgless,
	"ListAttachedDaemonIDsForUser":      readOrgless,
	"ListBackgroundProcesses":           readUnused,
	"ListDaemonLocalModels":             readOrgless,
	"ListDaemonStorageState":            readOrgless,
	"ListDaemonsByUserID":               readOrgless,
	"ListFreshDaemonAttachmentsForUser": readOrgless,
	"ListHeldWorktreesForUser":          readOrgless,
	"ListMarkedWorktreesForUser":        readOrgless,
	"ListTriggerEvents":                 readOrgless,

	"CountLiveLaunchedRuns":            readBackground,
	"CountTriggerCapacityHeldByMember": readBackground,
	"HasEnabledTriggerOfKind":          readBackground,
	"ListCapacityWakeTargets":          readBackground,
	"ListChatsPausedForMachine":        readBackground,
	"ListChatsQueuedForMachine":        readBackground,
	"ListChatsWaitingForMachine":       readBackground,
	"ListLiveWorktreePathsForUser":     readBackground,
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
