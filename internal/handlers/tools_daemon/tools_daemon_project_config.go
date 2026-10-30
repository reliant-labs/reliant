package tools_daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	cfgpkg "github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/ospath"
	"github.com/reliant-labs/reliant/internal/projectfacts"
)

func (s *Service) handleProjectDiscovery(ctx context.Context, conn *daemonConnection, discovery *reliantv1.ProjectDiscovery) error {
	if discovery == nil {
		return nil
	}

	paths := make([]string, 0, len(discovery.Projects))
	projectsByPath := make(map[string]*reliantv1.DiscoveredProject, len(discovery.Projects))
	seen := make(map[string]struct{})
	for _, p := range discovery.Projects {
		if p == nil {
			continue
		}
		path := normalizeProjectPath(p.Path)
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
		projectsByPath[path] = p
	}
	sort.Strings(paths)

	daemon, err := s.deps.Repo.GetDaemon(ctx, conn.daemonID)
	if err != nil {
		logging.DebugContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" Project discovery ignored for unknown daemon", "daemonID", conn.daemonID)
		return nil
	}

	projectPathsJSON, err := jsonStringSlicePtr(paths)
	if err != nil {
		return fmt.Errorf("failed to encode discovered project paths: %w", err)
	}
	daemon.ProjectPaths = projectPathsJSON
	if err := s.deps.Repo.UpsertDaemon(ctx, daemon); err != nil {
		return err
	}

	for _, path := range paths {
		discovered := projectsByPath[path]
		// One unresolvable path must not abandon the rest of the daemon's
		// projects, so this logs and moves on rather than returning.
		if _, err := s.ensureOwnedProjectForPath(ctx, conn, path, discovered); err != nil {
			logging.ErrorContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" Skipped discovered project that could not be resolved",
				"error", err, "projectPath", path, "daemonID", conn.daemonID, "userID", conn.userID)
			continue
		}

		if sendErr := s.sendLoadAndWatchProjectConfig(ctx, conn, path, true); sendErr != nil {
			logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" Failed requesting config load/watch for discovered project",
				"error", sendErr,
				"projectPath", path,
				"daemonID", conn.daemonID,
			)
		}
	}

	return nil
}

func (s *Service) handleLoadProjectConfigsResponse(ctx context.Context, conn *daemonConnection, resp *reliantv1.LoadProjectConfigsResponse) error {
	if resp == nil {
		return nil
	}
	if strings.TrimSpace(resp.Error) != "" {
		logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" Daemon returned load_project_configs error",
			"requestID", resp.RequestId,
			"error", resp.Error,
			"daemonID", conn.daemonID,
		)
		return nil
	}
	if resp.Snapshot == nil {
		return nil
	}

	return s.persistProjectConfigSnapshot(ctx, conn, resp.Snapshot, true)
}

func (s *Service) handleFileSystemChanged(ctx context.Context, conn *daemonConnection, msg *reliantv1.FileSystemChanged) error {
	if msg == nil {
		return nil
	}

	projectPath := normalizeProjectPath(msg.ProjectPath)
	if projectPath == "" {
		return nil
	}

	// Scoped by user: `projects` is UNIQUE on (user_id, path), so a path-only
	// lookup can return another user's row for the same directory and this
	// would emit a refetch naming a project the connected user cannot see.
	project, err := s.deps.Repo.GetProjectByPathAndUser(ctx, projectPath, conn.userID)
	if err != nil {
		return nil // project might not exist yet, that's fine
	}
	if project == nil {
		return nil
	}

	// A clone dispatched by CreateProjectFromRepo left a project_daemons row
	// in "installing". This announcement is the daemon saying the directory
	// now exists, so the row is settled here.
	//
	// Matched by (project, daemon) rather than by request id: unlike the
	// failure path, FileSystemChanged carries no request id — only the path
	// — and the path is exactly what the queued row recorded.
	if err := s.deps.Repo.MarkProjectDaemonInstalled(ctx, project.ID, conn.daemonID); err != nil {
		logging.WarnContext(ctx, "could not settle queued clone after filesystem change",
			"error", err, "project_id", project.ID, "daemon_id", conn.daemonID)
	} else {
		// The clone just SUCCEEDED on this daemon, which means forge.yaml is
		// on its disk right now. Read the project's forge name here rather
		// than waiting for someone to open the Forge tab with a daemon
		// online — see forge_name_learn.go for why that wait was the bug.
		s.learnForgeNameForProject(ctx, conn, project)
	}

	return s.deps.Repo.EmitUserRefetch(ctx, conn.userID, db.RefetchFileTree, db.RefetchOpts{
		ProjectID: &project.ID,
	})
}

// learnForgeNameForProject reads forge.yaml's `name` off the daemon that just
// finished a clone and persists it on the project row.
//
// Dispatched onto its own goroutine, NOT awaited: this runs on the
// connection's inbound state worker, and waiting on a daemon round trip there
// would stall every other state message from this daemon until the command
// returned or timed out (see forge_name_learn.go). The settle it follows is
// already committed, so nothing depends on the answer.
//
// conn.done bounds the goroutine: a daemon that disconnects mid-read releases
// it rather than leaving it parked on a response that can no longer arrive.
func (s *Service) learnForgeNameForProject(ctx context.Context, conn *daemonConnection, project *db.Project) {
	if conn == nil || project == nil {
		return
	}
	go func() {
		// Deliberately detached from the message's ctx, which is scoped to
		// handling one DaemonMessage and is cancelled as soon as this
		// handler returns. Bounded by the command timeout and by conn.done.
		readCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		defer cancel()
		go func() {
			select {
			case <-conn.done:
				cancel()
			case <-readCtx.Done():
			}
		}()
		projectfacts.LearnForgeName(readCtx, connCommandSender{svc: s, conn: conn},
			s.deps.Repo, conn.userID, conn.daemonID, project.ID, project.Path)
	}()
}

// connCommandSender adapts one daemon CONNECTION to the ForgeNameReader that
// projectfacts.LearnForgeName declares.
//
// It targets the connection directly rather than resolving the user's default
// daemon: the checkout exists on the machine that just cloned it, and a user
// with two daemons would otherwise have forge.yaml read off the wrong disk.
type connCommandSender struct {
	svc  *Service
	conn *daemonConnection
}

func (c connCommandSender) SendDaemonCommandToDaemon(
	ctx context.Context, _, _, commandType string, payload []byte, timeoutMs int32,
) ([]byte, error) {
	resp, err := c.svc.sendCommandToConn(ctx, c.conn, &reliantv1.DaemonCommandRequest{
		RequestId:   uuid.NewString(),
		CommandType: commandType,
		Payload:     payload,
		TimeoutMs:   timeoutMs,
	})
	if err != nil {
		return nil, err
	}
	if !resp.GetSuccess() {
		msg := strings.TrimSpace(resp.GetErrorMessage())
		if msg == "" {
			msg = "the daemon reported no reason"
		}
		return nil, fmt.Errorf("daemon command %s: %s", commandType, msg)
	}
	return resp.GetPayload(), nil
}

// cloneFailureReason keeps the stored reason bounded and non-empty. The
// daemon's message is user-safe by contract, but a clone that failed with no
// message at all must still read as failed rather than as a blank.
func cloneFailureReason(message string) string {
	trimmed := strings.TrimSpace(message)
	if trimmed == "" {
		return "the clone failed, but the machine reported no reason"
	}
	const maxReason = 500
	if len(trimmed) > maxReason {
		return trimmed[:maxReason]
	}
	return trimmed
}

// handleDaemonCommandFailed persists a DaemonCommandFailed announcement as a
// USER_UPDATE_TYPE_NOTIFICATION row. This is the only outcome signal for a
// command dispatched with no live RPC waiter (control-plane's fire-and-forget
// CloneRepo enqueues onto DAEMON_PENDING_COMMANDS and returns immediately —
// see gitcredential.svc.Clone) — the DaemonCommandResponse the daemon also
// sends has nowhere to go, since no pendingCommands channel was ever
// registered for a request id the API server never issued synchronously.
// Writing it to user_updates makes it replay to the client on reconnect,
// same as the success path in handleFileSystemChanged.
func (s *Service) handleDaemonCommandFailed(ctx context.Context, conn *daemonConnection, msg *reliantv1.DaemonCommandFailed) error {
	if msg == nil {
		return nil
	}

	// A clone dispatched by CreateProjectFromRepo has a project_daemons row
	// sitting in "installing". Resolve it here, or the project stays in that
	// state forever and the user is left watching a spinner for a clone that
	// already failed. Best-effort: the notification below is what the client
	// actually reacts to, and must be written either way.
	if msg.CommandType == "git.clone" && msg.RequestId != "" {
		if err := s.deps.Repo.ResolveQueuedProjectDaemon(ctx, msg.RequestId, cloneFailureReason(msg.ErrorMessage)); err != nil {
			logging.WarnContext(ctx, "could not mark queued clone failed",
				"error", err, "request_id", msg.RequestId)
		}
	}

	data, err := json.Marshal(map[string]string{
		"reason":       "daemon_command_failed",
		"command_type": msg.CommandType,
		"request_id":   msg.RequestId,
		"error":        msg.ErrorMessage,
	})
	if err != nil {
		return fmt.Errorf("marshaling daemon command failure notification: %w", err)
	}

	return s.deps.Repo.CreateUserUpdate(ctx, &db.UserUpdate{
		UserID:     conn.userID,
		UpdateType: db.UserUpdateNotification,
		EntityType: db.EntityTypeSystem,
		EntityID:   msg.CommandType,
		Data:       data,
	})
}

func (s *Service) handleProjectConfigDelta(ctx context.Context, conn *daemonConnection, delta *reliantv1.ProjectConfigDelta) error {
	if delta == nil {
		return nil
	}

	projectPath := normalizeProjectPath(delta.ProjectPath)
	if projectPath == "" {
		return nil
	}

	project, err := s.ensureOwnedProjectForPath(ctx, conn, projectPath, nil)
	if err != nil {
		return err
	}

	applyUpdate, err := s.shouldApplyProjectConfigUpdate(ctx, project.ID, conn.daemonID, delta.DaemonTimestampUnixMs)
	if err != nil {
		return err
	}
	if !applyUpdate {
		return nil
	}

	if snapshot := delta.SnapshotIfCompacted; snapshot != nil {
		if snapshot.ProjectPath == "" {
			snapshot.ProjectPath = projectPath
		}
		return s.persistProjectConfigSnapshot(ctx, conn, snapshot, false)
	}

	return s.SendLoadProjectConfigs(ctx, conn.userID, conn.daemonID, projectPath, uuid.New().String())
}

// daemonRuntimeTypeFromLabels extracts the daemon's runtime/sandbox type
// ("kata", "gvisor", ...) from its registration labels. Returns nil when the
// label is absent or empty (local/unknown daemons), so it persists as NULL.
func daemonRuntimeTypeFromLabels(labels map[string]string) *string {
	rt := strings.TrimSpace(labels[cfgpkg.DaemonRuntimeTypeLabelKey])
	if rt == "" {
		return nil
	}
	return &rt
}

func (s *Service) persistProjectConfigSnapshot(ctx context.Context, conn *daemonConnection, snapshot *reliantv1.ProjectConfigSnapshot, force bool) error {
	if snapshot == nil {
		return nil
	}

	projectPath := normalizeProjectPath(snapshot.ProjectPath)
	if projectPath == "" {
		return nil
	}

	project, err := s.ensureOwnedProjectForPath(ctx, conn, projectPath, nil)
	if err != nil {
		// Dropping a snapshot is NOT recoverable by waiting: runProjectWatcher
		// suppresses any delta whose content hash matches the last one it SENT,
		// so the daemon will not re-offer this snapshot until the files on disk
		// change or it reconnects. Everything downstream (skills, MCP, memory)
		// therefore stays empty indefinitely. Log it as the data loss it is —
		// the previous "project not found for path" wording sent debugging
		// after a missing row when the row existed and was merely unresolvable.
		logging.ErrorContext(ctx, "[ToolsDaemon] Dropped project config snapshot — no project resolvable for this daemon's user",
			"error", err,
			"projectPath", projectPath,
			"daemonID", conn.daemonID,
			"userID", conn.userID)
		return err
	}

	record := &db.ProjectConfigRecord{
		ProjectID:            project.ID,
		DaemonID:             conn.daemonID,
		UserConfigYAML:       bytesToStringPtr(snapshot.UserConfigYaml),
		ProjectConfigYAML:    bytesToStringPtr(snapshot.ProjectConfigYaml),
		LocalConfigYAML:      bytesToStringPtr(snapshot.LocalConfigYaml),
		GlobalMemoryMD:       bytesToStringPtr(snapshot.GlobalMemoryMd),
		ProjectMemoryMD:      bytesToStringPtr(snapshot.ProjectMemoryMd),
		MCPConfigs:           flattenMCPConfigs(snapshot.McpConfigs),
		ProjectWorkflowsJSON: flattenIndexedWorkflows(snapshot.Workflows),
		ProjectPresetsJSON:   flattenIndexedPresets(snapshot.Presets),
		ProjectScenariosJSON: flattenIndexedScenarios(snapshot.Scenarios),
		ProjectSkillsJSON:    flattenIndexedSkills(snapshot.Skills),
		RepoMemoriesJSON:     flattenRepoMemories(snapshot.RepoMemoriesMd),
		RuntimeType:          daemonRuntimeTypeFromLabels(conn.labels),
		RepoStatesJSON:       flattenRepoStates(snapshot.RepoStates),
		PushedAt:             daemonTimestampToTime(snapshot.DaemonTimestampUnixMs),
	}

	// The workflows and presets replace this machine's catalog only; the
	// record carries their union over every machine (SyncProjectConfigSnapshot),
	// so a machine without a checkout cannot erase another machine's. Unless
	// forced, a snapshot no newer than this machine's last push is dropped
	// there, under the same lock that writes it.
	_, err = s.deps.Repo.SyncProjectConfigSnapshot(ctx, record, force)
	return err
}

// ensureOwnedProjectForPath resolves the connected daemon's project row for a
// path, creating it when the daemon has discovered a directory this user has no
// project for yet.
//
// The lookup MUST be scoped by user. `projects` is UNIQUE on (user_id, path),
// NOT on path — the same directory legitimately carries one row per user, and a
// path-only `SELECT ... WHERE path = $1` then returns an arbitrary one of them.
// Signing in under a second identity (e.g. dev session -> GitHub OAuth) is
// enough to produce the collision on a single developer's machine.
//
// The failure that motivated this was silent and total: the path-only lookup
// returned the OTHER user's row, the ownership check below rejected it, and the
// caller logged "project not found for path" and DROPPED the snapshot — for a
// project that existed and was created 200ms earlier. The seed row that
// CreateProject writes (all columns NULL) therefore stayed empty forever, so
// every CallLLM against that project saw a 0-entry skill catalog and failed
// preload with "the project config snapshot has not filled yet; retrying" — a
// message that promises a transient condition the pipeline could never clear.
// Both rejections below return an ERROR rather than (nil, nil). A (nil, nil)
// return is what made the incident above silent: it obliged every caller to
// invent its own meaning for a nil project, and two of the three chose to
// return nil and move on, so a rejected path produced a dropped snapshot and
// one Warn line nobody was looking for. An error cannot be ignored by
// accident. Neither branch is reachable in normal operation — all three
// callers pass a path through normalizeProjectPath first — so making them
// loud costs nothing and turns a protocol violation into a visible failure.
func (s *Service) ensureOwnedProjectForPath(ctx context.Context, conn *daemonConnection, path string, discovered *reliantv1.DiscoveredProject) (*db.Project, error) {
	// ospath, not filepath: this path names a directory on the DAEMON, which
	// may be Windows while this server is Linux. Under filepath.IsAbs every
	// `C:\...` path was judged relative, so a Windows daemon hit exactly the
	// silent-drop path described above on every snapshot it ever sent.
	if !ospath.IsAbs(path) {
		return nil, fmt.Errorf("daemon reported a non-absolute project path %q", path)
	}
	if conn == nil || strings.TrimSpace(conn.userID) == "" {
		return nil, fmt.Errorf("cannot resolve project %q: the daemon connection carries no user", path)
	}

	project, err := s.deps.Repo.GetProjectByPathAndUser(ctx, path, conn.userID)
	if err != nil {
		if !isNotFoundErr(err) {
			return nil, err
		}

		// ospath.Base, not filepath.Base: on a Linux server the latter returns
		// `C:\Users\sean\src\proj` whole, so a Windows daemon's discovered
		// project would be named after its entire path.
		name := ospath.Base(path)
		isGitRepo := false
		if discovered != nil {
			if strings.TrimSpace(discovered.Name) != "" {
				name = strings.TrimSpace(discovered.Name)
			}
			isGitRepo = discovered.IsGitRepo
		}

		now := time.Now().UTC()
		createErr := s.deps.Repo.CreateProject(ctx, &db.Project{
			ID:         uuid.New().String(),
			UserID:     conn.userID,
			Name:       name,
			Path:       path,
			IsGitRepo:  isGitRepo,
			CreatedAt:  now,
			UpdatedAt:  now,
			LastActive: now,
		})
		// Read back scoped for the same reason the first lookup is: an
		// unscoped read-back after a successful create can hand us a DIFFERENT
		// user's row for this path. The createErr branch is the lost race with
		// a concurrent create (UNIQUE (user_id, path)) — re-reading our own row
		// is what makes it idempotent rather than fatal.
		project, err = s.deps.Repo.GetProjectByPathAndUser(ctx, path, conn.userID)
		if err != nil {
			if createErr != nil {
				return nil, fmt.Errorf("failed to create discovered project %s: %w", path, createErr)
			}
			return nil, err
		}
	}

	// Reconcile the cached git flag against what daemon discovery actually
	// observed on disk (bidirectional; see projectfacts.ReconcileGitRepo).
	if discovered != nil {
		if updateErr := projectfacts.ReconcileGitRepo(ctx, s.deps.Repo, project, discovered.IsGitRepo); updateErr != nil {
			logging.WarnContext(ctx, "ensureOwnedProjectForPath: failed to update git status", "error", updateErr, "projectID", project.ID)
		}
	}

	return project, nil
}

// shouldApplyProjectConfigUpdate drops a delta older than the last push from
// the SAME machine. It is judged per machine because each machine's watcher
// sends only when its own content changes: another machine's later push (or
// clock) must not drop this one's. It reads the one timestamp, never the
// whole record, which carries every skill body the daemon indexed — 18 MB in
// prod.
func (s *Service) shouldApplyProjectConfigUpdate(ctx context.Context, projectID, daemonID string, daemonTSUnixMs int64) (bool, error) {
	if daemonTSUnixMs <= 0 {
		return true, nil
	}

	pushedAt, err := s.deps.Repo.GetProjectMachineCatalogPushedAt(ctx, projectID, daemonID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return true, nil
		}
		return false, err
	}

	existingTSUnixMs := pushedAt.UTC().UnixMilli()
	if existingTSUnixMs <= 0 {
		return true, nil
	}
	if daemonTSUnixMs <= existingTSUnixMs {
		return false, nil
	}
	return true, nil
}

func daemonTimestampToTime(unixMs int64) time.Time {
	if unixMs <= 0 {
		return time.Now().UTC()
	}
	return time.UnixMilli(unixMs).UTC()
}

func flattenMCPConfigs(mcpConfigs map[string][]byte) *string {
	if len(mcpConfigs) == 0 {
		return nil
	}
	flat := make(map[string]string, 3)
	for _, scope := range []string{"user", "project", "local"} {
		payload, ok := mcpConfigs[scope]
		if !ok || len(payload) == 0 {
			continue
		}
		flat[scope] = string(payload)
	}
	if len(flat) == 0 {
		return nil
	}
	encoded, err := json.Marshal(flat)
	if err != nil {
		return nil
	}
	value := string(encoded)
	return &value
}

type storedWorkflowJSON struct {
	Slug         string `json:"slug"`
	Name         string `json:"name"`
	RelativePath string `json:"relative_path,omitempty"`
	YAMLContent  string `json:"yaml_content"`
	ContentHash  string `json:"content_hash"`
}

type storedPresetJSON struct {
	Name         string `json:"name"`
	RelativePath string `json:"relative_path,omitempty"`
	YAMLContent  string `json:"yaml_content"`
	ContentHash  string `json:"content_hash"`
}

type storedScenarioJSON struct {
	WorkflowSlug string `json:"workflow_slug"`
	Name         string `json:"name"`
	YAMLContent  string `json:"yaml_content"`
	ContentHash  string `json:"content_hash"`
}

func flattenIndexedWorkflows(workflows []*reliantv1.IndexedWorkflow) *string {
	if len(workflows) == 0 {
		return nil
	}
	items := make([]storedWorkflowJSON, 0, len(workflows))
	for _, w := range workflows {
		items = append(items, storedWorkflowJSON{
			Slug:         w.Slug,
			Name:         w.Name,
			RelativePath: w.RelativePath,
			YAMLContent:  string(w.YamlContent),
			ContentHash:  w.ContentHash,
		})
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return nil
	}
	value := string(encoded)
	return &value
}

func flattenIndexedPresets(presets []*reliantv1.IndexedPreset) *string {
	if len(presets) == 0 {
		return nil
	}
	items := make([]storedPresetJSON, 0, len(presets))
	for _, p := range presets {
		items = append(items, storedPresetJSON{
			Name:         p.Name,
			RelativePath: p.RelativePath,
			YAMLContent:  string(p.YamlContent),
			ContentHash:  p.ContentHash,
		})
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return nil
	}
	value := string(encoded)
	return &value
}

func flattenIndexedScenarios(scenarios []*reliantv1.IndexedScenario) *string {
	if len(scenarios) == 0 {
		return nil
	}
	items := make([]storedScenarioJSON, 0, len(scenarios))
	for _, s := range scenarios {
		items = append(items, storedScenarioJSON{
			WorkflowSlug: s.WorkflowSlug,
			Name:         s.Name,
			YAMLContent:  string(s.YamlContent),
			ContentHash:  s.ContentHash,
		})
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return nil
	}
	value := string(encoded)
	return &value
}

// flattenIndexedSkills converts the proto skills snapshot into the JSON blob
// stored in project_configs.project_skills_json. The JSON shape matches
// config.StoredSkill so StoredConfigProvider can deserialize identically.
func flattenIndexedSkills(skills []*reliantv1.IndexedSkill) *string {
	if len(skills) == 0 {
		return nil
	}
	items := make([]cfgpkg.StoredSkill, 0, len(skills))
	for _, s := range skills {
		if s == nil {
			continue
		}
		var userInvocable *bool
		switch s.UserInvocable {
		case "true":
			v := true
			userInvocable = &v
		case "false":
			v := false
			userInvocable = &v
		}
		items = append(items, cfgpkg.StoredSkill{
			SkillPath:              s.SkillPath,
			Name:                   s.Name,
			Description:            s.Description,
			Scope:                  s.Scope,
			Body:                   s.Body,
			AllowedTools:           s.AllowedTools,
			Metadata:               s.Metadata,
			HasChildren:            s.HasChildren,
			DisableModelInvocation: s.DisableModelInvocation,
			UserInvocable:          userInvocable,
			ArgumentHint:           s.ArgumentHint,
			Paths:                  s.Paths,
			ContentHash:            s.ContentHash,
			Source:                 s.Source,
		})
	}
	if len(items) == 0 {
		return nil
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return nil
	}
	value := string(encoded)
	return &value
}

// flattenRepoMemories converts the proto repo memories map into a JSON string
// for DB storage.
func flattenRepoMemories(memories map[string][]byte) *string {
	if len(memories) == 0 {
		return nil
	}
	strMap := make(map[string]string, len(memories))
	for k, v := range memories {
		strMap[k] = string(v)
	}
	encoded, err := json.Marshal(strMap)
	if err != nil {
		return nil
	}
	s := string(encoded)
	return &s
}

func (s *Service) sendLoadAndWatchProjectConfig(ctx context.Context, conn *daemonConnection, projectPath string, includeInitial bool) error {
	if conn == nil {
		return nil
	}
	if err := s.SendLoadProjectConfigs(ctx, conn.userID, conn.daemonID, projectPath, uuid.New().String()); err != nil {
		return err
	}
	return s.SendWatchProjectConfigs(ctx, conn.userID, conn.daemonID, projectPath, includeInitial)
}

// SendLoadProjectConfigs requests a full snapshot load for a project path on
// one named daemon.
func (s *Service) SendLoadProjectConfigs(_ context.Context, userID, daemonID string, projectPath string, requestID string) error {
	projectPath = normalizeProjectPath(projectPath)
	if projectPath == "" {
		return nil
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		requestID = uuid.New().String()
	}

	msg := &reliantv1.ServerMessage{
		Message: &reliantv1.ServerMessage_LoadProjectConfigs{
			LoadProjectConfigs: &reliantv1.LoadProjectConfigsRequest{
				ProjectPath: projectPath,
				RequestId:   requestID,
			},
		},
	}
	return s.sendToDaemon(userID, daemonID, msg)
}

// SendWatchProjectConfigs subscribes daemon-side watchers for project config changes.
func (s *Service) SendWatchProjectConfigs(_ context.Context, userID, daemonID string, projectPath string, includeInitial bool) error {
	projectPath = normalizeProjectPath(projectPath)
	if projectPath == "" {
		return nil
	}

	msg := &reliantv1.ServerMessage{
		Message: &reliantv1.ServerMessage_WatchProjectConfigs{
			WatchProjectConfigs: &reliantv1.WatchProjectConfigsRequest{
				ProjectPath:    projectPath,
				IncludeInitial: includeInitial,
			},
		},
	}
	return s.sendToDaemon(userID, daemonID, msg)
}

// SendUnwatchProjectConfigs unsubscribes daemon-side watchers for project config changes.
func (s *Service) SendUnwatchProjectConfigs(userID, daemonID string, projectPath string) error {
	projectPath = normalizeProjectPath(projectPath)
	if projectPath == "" {
		return nil
	}

	msg := &reliantv1.ServerMessage{
		Message: &reliantv1.ServerMessage_UnwatchProjectConfigs{
			UnwatchProjectConfigs: &reliantv1.UnwatchProjectConfigsRequest{ProjectPath: projectPath},
		},
	}
	return s.sendToDaemon(userID, daemonID, msg)
}

func bytesToStringPtr(value []byte) *string {
	if len(value) == 0 {
		return nil
	}
	v := string(value)
	return &v
}

// flattenRepoStates is the snapshot's git state per repo as stored on the
// machine's catalog row (config.SyncedRepoState); nil from a daemon that does
// not report it.
func flattenRepoStates(states []*reliantv1.SyncedRepoState) *string {
	items := make([]cfgpkg.SyncedRepoState, 0, len(states))
	for _, s := range states {
		if s == nil {
			continue
		}
		items = append(items, cfgpkg.SyncedRepoState{
			Repo:          s.GetRepo(),
			Branch:        s.GetBranch(),
			Commit:        s.GetCommitSha(),
			Dirty:         s.GetDirty(),
			DefaultBranch: s.GetDefaultBranch(),
			Error:         s.GetError(),
		})
	}
	encoded, err := cfgpkg.EncodeCatalogJSON(items)
	if err != nil {
		return nil
	}
	return encoded
}
