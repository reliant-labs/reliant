// Copyright (c) 2025 Reliant Labs

package viewertest

// Viewer is what an rpc answers a viewer of the fixture's shared rows.
type Viewer int

const (
	// AsOutsider: exactly what an outsider gets, error and response alike.
	// Every write (a refusal), every read that does not serve viewers (a
	// refusal), and every list of the caller's own rows (an empty list).
	AsOutsider Viewer = iota
	// Answered: the read-only answer, shaped for a viewer (authz.IsViewer):
	// the owner and the viewer are answered, the outsider is refused.
	Answered
	// Stream: a server stream; StreamingService's own tests decide it.
	Stream
	// Dedicated: SharingService; its own tests hold every rule of it.
	Dedicated
)

func (v Viewer) String() string {
	switch v {
	case Answered:
		return "answered"
	case Stream:
		return "stream"
	case Dedicated:
		return "dedicated"
	default:
		return "as-outsider"
	}
}

// Matrix is what every rpc that takes a chat or a project (a chat_id,
// project_id, plan_id or message_id; TestMatrixCoversEveryChatOrProjectRPC)
// answers a viewer: the §4 table of control-plane docs/design/org-sharing.md
// as code. Answered is exactly the read sites that serve viewers
// (authz.WithViewerReads, internal/authz shareGuard.viewerReadSites); every
// other rpc gives a viewer exactly what it gives an outsider.
var Matrix = map[string]Viewer{
	// The transcript, its run and its plan: the read-only chat.
	"reliant.v1.ChatService/GetChat":               Answered,
	"reliant.v1.ChatService/ListMessages":          Answered,
	"reliant.v1.ChatService/ListChatPlans":         Answered,
	"reliant.v1.ChatService/ListStepExecutions":    Answered,
	"reliant.v1.ChatService/GetWorkflowExecutions": Answered,
	"reliant.v1.MessageService/GetMessage":         Answered,
	"reliant.v1.PlanService/GetPlanByChatId":       Answered,
	"reliant.v1.TaskService/ListTasks":             Answered,
	// Questions as read-only cards; answering stays the owner's.
	"reliant.v1.QuestionService/ListPendingQuestions": Answered,
	"reliant.v1.QuestionService/ListAsyncQuestions":   Answered,
	// The project card, without its path.
	"reliant.v1.ProjectService/GetProject": Answered,

	// The bodies a STUB read left out, for a viewer only those their FULL
	// read shows: a tool call's input, never a tool result.
	"reliant.v1.ChatService/GetContentBlockBodies": Answered,

	"reliant.v1.StreamingService/StreamChatUpdates": Stream,
	"reliant.v1.StreamingService/StreamUserUpdates": Stream,

	"reliant.v1.SharingService/SetChatVisibility":    Dedicated,
	"reliant.v1.SharingService/SetProjectVisibility": Dedicated,
	"reliant.v1.SharingService/ListSharedChats":      Dedicated,

	// Everything else: a viewer is an outsider. Tool results
	// (GetToolResult), the run's live inputs
	// (GetThreadWorkflowInputs), its updates feed (GetChatUpdates), its
	// approvals, branches and machine, and every write.
	"reliant.v1.ApprovalService/ListApprovalsByChat":           AsOutsider,
	"reliant.v1.BackgroundService/ListProcesses":               AsOutsider,
	"reliant.v1.ChatService/AdoptChat":                         AsOutsider,
	"reliant.v1.ChatService/BranchChat":                        AsOutsider,
	"reliant.v1.ChatService/CancelQueuedAgentMessage":          AsOutsider,
	"reliant.v1.ChatService/CompactChat":                       AsOutsider,
	"reliant.v1.ChatService/DeleteChat":                        AsOutsider,
	"reliant.v1.ChatService/DismissChat":                       AsOutsider,
	"reliant.v1.ChatService/GetChatUpdates":                    AsOutsider,
	"reliant.v1.ChatService/GetThreadWorkflowInputs":           AsOutsider,
	"reliant.v1.ChatService/GetToolResult":                     AsOutsider,
	"reliant.v1.ChatService/GetWorkflowNodeExecutions":         AsOutsider,
	"reliant.v1.ChatService/InterruptThread":                   AsOutsider,
	"reliant.v1.ChatService/ListBranches":                      AsOutsider,
	"reliant.v1.ChatService/ListChats":                         AsOutsider,
	"reliant.v1.ChatService/ListQueuedAgentMessages":           AsOutsider,
	"reliant.v1.ChatService/MarkUnreadChat":                    AsOutsider,
	"reliant.v1.ChatService/MoveChatToProject":                 AsOutsider,
	"reliant.v1.ChatService/PauseChat":                         AsOutsider,
	"reliant.v1.ChatService/ResumeChat":                        AsOutsider,
	"reliant.v1.ChatService/RetryUsageLimitWait":               AsOutsider,
	"reliant.v1.ChatService/RunNow":                            AsOutsider,
	"reliant.v1.ChatService/SearchChats":                       AsOutsider,
	"reliant.v1.ChatService/SendAgentMessage":                  AsOutsider,
	"reliant.v1.ChatService/SendMessage":                       AsOutsider,
	"reliant.v1.ChatService/SetChatDaemon":                     AsOutsider,
	"reliant.v1.ChatService/StartChat":                         AsOutsider,
	"reliant.v1.ChatService/TerminateChat":                     AsOutsider,
	"reliant.v1.ChatService/UnadoptChat":                       AsOutsider,
	"reliant.v1.ChatService/UpdateChat":                        AsOutsider,
	"reliant.v1.ChatService/UpdateChatProviderPolicy":          AsOutsider,
	"reliant.v1.ChatService/UpdateChatState":                   AsOutsider,
	"reliant.v1.ChatService/UpdateWorkflowParams":              AsOutsider,
	"reliant.v1.FileSystemService/CopyFile":                    AsOutsider,
	"reliant.v1.FileSystemService/CreateFileOrFolder":          AsOutsider,
	"reliant.v1.FileSystemService/DeleteFileOrFolder":          AsOutsider,
	"reliant.v1.FileSystemService/GetFileContent":              AsOutsider,
	"reliant.v1.FileSystemService/GetFileMetadata":             AsOutsider,
	"reliant.v1.FileSystemService/GetFilePreview":              AsOutsider,
	"reliant.v1.FileSystemService/GetFilePreviewInfo":          AsOutsider,
	"reliant.v1.FileSystemService/GetFileTree":                 AsOutsider,
	"reliant.v1.FileSystemService/ReplaceInFiles":              AsOutsider,
	"reliant.v1.FileSystemService/SaveFileContent":             AsOutsider,
	"reliant.v1.FileSystemService/SearchFiles":                 AsOutsider,
	"reliant.v1.ForgeService/ApplyPromote":                     AsOutsider,
	"reliant.v1.ForgeService/GetDeployStatus":                  AsOutsider,
	"reliant.v1.ForgeService/GetEnvShape":                      AsOutsider,
	"reliant.v1.ForgeService/GetEnvStatus":                     AsOutsider,
	"reliant.v1.ForgeService/GetTopology":                      AsOutsider,
	"reliant.v1.ForgeService/ListSecrets":                      AsOutsider,
	"reliant.v1.ForgeService/PlanDeploy":                       AsOutsider,
	"reliant.v1.ForgeService/PlanPromote":                      AsOutsider,
	"reliant.v1.ForgeService/StartDeploy":                      AsOutsider,
	"reliant.v1.ForgeService/StartDeployPlan":                  AsOutsider,
	"reliant.v1.ForgeService/VerifyEnv":                        AsOutsider,
	"reliant.v1.InboxService/ListInbox":                        AsOutsider,
	"reliant.v1.MCPService/GetServer":                          AsOutsider,
	"reliant.v1.MCPService/GetServerTools":                     AsOutsider,
	"reliant.v1.MCPService/InstallServer":                      AsOutsider,
	"reliant.v1.MCPService/ListRecommended":                    AsOutsider,
	"reliant.v1.MCPService/ListServers":                        AsOutsider,
	"reliant.v1.MCPService/MoveServerScope":                    AsOutsider,
	"reliant.v1.MCPService/RestartServer":                      AsOutsider,
	"reliant.v1.MCPService/SetServerEnabled":                   AsOutsider,
	"reliant.v1.MCPService/UninstallServer":                    AsOutsider,
	"reliant.v1.MCPService/UpdateServerConfig":                 AsOutsider,
	"reliant.v1.PackageCommandsService/GetCommandFavorites":    AsOutsider,
	"reliant.v1.PackageCommandsService/ListCommands":           AsOutsider,
	"reliant.v1.PackageCommandsService/ListProcesses":          AsOutsider,
	"reliant.v1.PackageCommandsService/RunCommand":             AsOutsider,
	"reliant.v1.PackageCommandsService/SetCommandFavorite":     AsOutsider,
	"reliant.v1.PlanService/CreatePlan":                        AsOutsider,
	"reliant.v1.PlanService/DeletePlan":                        AsOutsider,
	"reliant.v1.PlanService/GetPlan":                           AsOutsider,
	"reliant.v1.PlanService/ListPlanTasks":                     AsOutsider,
	"reliant.v1.PlanService/ListPlansByProject":                AsOutsider,
	"reliant.v1.PlanService/UpdatePlan":                        AsOutsider,
	"reliant.v1.PresetService/CreatePreset":                    AsOutsider,
	"reliant.v1.PresetService/DeletePreset":                    AsOutsider,
	"reliant.v1.PresetService/GetDefaultPreset":                AsOutsider,
	"reliant.v1.PresetService/GetDefaultPresetsBatch":          AsOutsider,
	"reliant.v1.PresetService/GetPreset":                       AsOutsider,
	"reliant.v1.PresetService/ListPresets":                     AsOutsider,
	"reliant.v1.PresetService/ListPresetsForWorkflow":          AsOutsider,
	"reliant.v1.PresetService/SetDefaultPreset":                AsOutsider,
	"reliant.v1.PresetService/UpdatePreset":                    AsOutsider,
	"reliant.v1.ProjectService/AttachProjectToDaemon":          AsOutsider,
	"reliant.v1.ProjectService/DeleteProject":                  AsOutsider,
	"reliant.v1.ProjectService/DetachProjectFromDaemon":        AsOutsider,
	"reliant.v1.ProjectService/GetProjectChanges":              AsOutsider,
	"reliant.v1.ProjectService/GetProjectGitBranches":          AsOutsider,
	"reliant.v1.ProjectService/GetProjectGitInfo":              AsOutsider,
	"reliant.v1.ProjectService/GetProjectInitStatus":           AsOutsider,
	"reliant.v1.ProjectService/GetProjectMetadata":             AsOutsider,
	"reliant.v1.ProjectService/GetProjectPrompts":              AsOutsider,
	"reliant.v1.ProjectService/InitializeGitRepo":              AsOutsider,
	"reliant.v1.ProjectService/InitializeProject":              AsOutsider,
	"reliant.v1.ProjectService/MarkProjectInstalled":           AsOutsider,
	"reliant.v1.ProjectService/PreviewProjectOrgMove":          AsOutsider,
	"reliant.v1.ProjectService/PullProjectOnDaemon":            AsOutsider,
	"reliant.v1.ProjectService/RecloneProjectOnDaemon":         AsOutsider,
	"reliant.v1.ProjectService/RemoveProjectFromDaemon":        AsOutsider,
	"reliant.v1.ProjectService/SaveProjectPrompts":             AsOutsider,
	"reliant.v1.ProjectService/TouchProject":                   AsOutsider,
	"reliant.v1.ProjectService/UpdateProject":                  AsOutsider,
	"reliant.v1.ProjectService/UpdateProjectMetadata":          AsOutsider,
	"reliant.v1.ProviderAccountService/GetChatProviderAccount": AsOutsider,
	"reliant.v1.RepoService/AddRepo":                           AsOutsider,
	"reliant.v1.RepoService/DiscoverRepos":                     AsOutsider,
	"reliant.v1.RepoService/ListRepos":                         AsOutsider,
	"reliant.v1.RunService/LastRunPerWorkflow":                 AsOutsider,
	"reliant.v1.RunService/ListRuns":                           AsOutsider,
	"reliant.v1.ScenarioService/CreateScenario":                AsOutsider,
	"reliant.v1.ScenarioService/DeleteScenario":                AsOutsider,
	"reliant.v1.ScenarioService/ExportScenario":                AsOutsider,
	"reliant.v1.ScenarioService/ListScenarios":                 AsOutsider,
	"reliant.v1.ScenarioService/RunScenario":                   AsOutsider,
	"reliant.v1.ScenarioService/UploadScenario":                AsOutsider,
	"reliant.v1.SettingsService/BatchUpsertSettings":           AsOutsider,
	"reliant.v1.SettingsService/CreateSetting":                 AsOutsider,
	"reliant.v1.SettingsService/DeleteGlobalSkill":             AsOutsider,
	"reliant.v1.SettingsService/DeleteSetting":                 AsOutsider,
	"reliant.v1.SettingsService/GetConfigHealth":               AsOutsider,
	"reliant.v1.SettingsService/GetInstalledSkillDefinition":   AsOutsider,
	"reliant.v1.SettingsService/GetSetting":                    AsOutsider,
	"reliant.v1.SettingsService/InstallSkill":                  AsOutsider,
	"reliant.v1.SettingsService/ListInstalledSkills":           AsOutsider,
	"reliant.v1.SettingsService/ListRecommendedSkills":         AsOutsider,
	"reliant.v1.SettingsService/ListSettings":                  AsOutsider,
	"reliant.v1.SettingsService/SetSkillEnabled":               AsOutsider,
	"reliant.v1.SettingsService/UpdateSetting":                 AsOutsider,
	"reliant.v1.TaskService/CreateTask":                        AsOutsider,
	"reliant.v1.TaskService/ListReadyTasks":                    AsOutsider,
	"reliant.v1.TriggerService/DisableWorkflow":                AsOutsider,
	"reliant.v1.TriggerService/DraftAutomation":                AsOutsider,
	"reliant.v1.TriggerService/EnableWorkflow":                 AsOutsider,
	"reliant.v1.TriggerService/GetLaunchEvent":                 AsOutsider,
	"reliant.v1.TriggerService/ListReplayEvents":               AsOutsider,
	"reliant.v1.TriggerService/ListTriggers":                   AsOutsider,
	"reliant.v1.TriggerService/PreviewReplay":                  AsOutsider,
	"reliant.v1.WorkflowService/CopyWorkflow":                  AsOutsider,
	"reliant.v1.WorkflowService/CreateWorkflowDraft":           AsOutsider,
	"reliant.v1.WorkflowService/DeleteWorkflow":                AsOutsider,
	"reliant.v1.WorkflowService/ExportWorkflow":                AsOutsider,
	// Org admins only (a fresh role), and only for a saved workflow whose
	// creator LEFT the org: a viewer of a shared project forks nothing.
	"reliant.v1.WorkflowService/ForkOrphanedWorkflow":   AsOutsider,
	"reliant.v1.WorkflowService/GetWorkflow":            AsOutsider,
	"reliant.v1.WorkflowService/ImportWorkflow":         AsOutsider,
	"reliant.v1.WorkflowService/ListWorkflows":          AsOutsider,
	"reliant.v1.WorkflowService/SaveWorkflow":           AsOutsider,
	"reliant.v1.WorkflowService/SetWorkflowStatus":      AsOutsider,
	"reliant.v1.WorkflowService/SetWorkflowVisibility":  AsOutsider,
	"reliant.v1.WorkflowService/ValidateWorkflow":       AsOutsider,
	"reliant.v1.WorktreeService/CreateWorktree":         AsOutsider,
	"reliant.v1.WorktreeService/DiscoverWorktrees":      AsOutsider,
	"reliant.v1.WorktreeService/ImportWorktree":         AsOutsider,
	"reliant.v1.WorktreeService/ListArchivedWorktrees":  AsOutsider,
	"reliant.v1.WorktreeService/ListWorktreeRecoveries": AsOutsider,
	"reliant.v1.WorktreeService/ListWorktrees":          AsOutsider,
	"reliant.v1.WorktreeService/PruneWorktrees":         AsOutsider,
	"reliant.v1.TaskService/DeleteTask":                 AsOutsider,
	"reliant.v1.TaskService/GetTask":                    AsOutsider,
	"reliant.v1.TaskService/UpdateTask":                 AsOutsider,
}

// matrixKeys are the request fields that make an rpc "take a chat or a
// project" for TestMatrixCoversEveryChatOrProjectRPC.
var matrixKeys = []string{"chat_id", "project_id", "plan_id", "message_id", "task_id"}
