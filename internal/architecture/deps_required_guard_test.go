package architecture

import (
	"archive/tar"
	"bytes"
	"cmp"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// ─── every construction of a service sets every required Deps field ───────
//
// A pull request adds a field to a service's Deps struct and wires every
// construction the merge gate runs: the affected packages' tests under
// -short. The constructions it does not run are left without the field: a
// slow test (testkit.Slow, or a testing.Short skip), a test behind a build tag
// (e2e, replayfixtures, integration) and the harnesses those tests share. The
// merge gate compiles them (scripts/test-build.sh) and they compile, because a
// missing field is a zero value, not an error. Main then goes red at the
// release gate with a nil dereference deep in the service:
//
//   - #3368 made chat.Deps.Chats and threads.Deps.Chats required; the e2e
//     stories harness, TestInterruptThread_ReadsOnlyLiveCalls (slow) and
//     replaytest's generate_interrupt (replayfixtures) did not set them
//     (fixed by #3589).
//   - #3599 added launch.Deps.Chats; TestReconcilerRecoversAFireStuckPending
//     (DB-backed, skipped under -short) did not set it (fixed by #3605).
//
// This test reads the source, so it sees every construction whatever its
// build tags and however slow its test is, and runs on every Go change
// (test-affected selects packages that walk every Go file). It reports only
// missing fields added relative to the queue base, so unrelated queued changes
// cannot fail this PR. A field is required unless marked `// forge:optional-dep`
// (forge's convention: an optional dep's consumer handles nil).
// Every composite literal of a Deps type
// outside its own package, and every one passed to a call inside it (directly
// or through local bindings and the returns of bare, package-qualified and method helpers), must name each required field. A literal inside the
// package that builds a Service by hand (`&Service{deps: Deps{Repo: r}}`) is
// a unit test of one method and is not judged.
//
// So a new Deps field is required everywhere at once: wire it at every
// construction this test names, or mark it `// forge:optional-dep` (its
// consumer must then handle nil), in the pull request that adds it.

// Historical baseline, retained for reference only; the guard now compares
// against the queue base instead of applying these broad directory exemptions.
// depsFieldsNotEverywhere names the Deps fields ("<package dir>.<Field>")
// that constructions in the listed package directories left unset when this
// test was added (2026-11-26), on purpose (a unit test that calls one method,
// a service built only to create threads) or by history. An entry exempts
// those directories only: a new harness or test elsewhere that leaves the
// field out is still a finding. It shrinks: the test fails on a directory
// that sets the field everywhere now, so delete it then. Never add a field a
// pull request introduces: wire it everywhere, or mark it
// `// forge:optional-dep`.
var _ = map[string][]string{
	// Existing local-variable and wrapper constructions, newly covered by B1.
	"cmd/reliant/cmd.Cmd":                          {"cmd/reliant/cmd"},
	"internal/handlers/account.Purger":             {"internal/handlers/account"},
	"internal/handlers/background.Logger":          {"internal/handlers/background"},
	"internal/handlers/background.Config":          {"internal/handlers/background"},
	"internal/handlers/chat.Chats":                 {"internal/handlers/chat"},
	"internal/handlers/connector.Logger":           {"internal/handlers/connector"},
	"internal/handlers/connector.Config":           {"internal/handlers/connector"},
	"internal/handlers/connector.Tokens":           {"internal/handlers/connector"},
	"internal/handlers/connector.Integrations":     {"internal/handlers/connector"},
	"internal/handlers/daemon.Logger":              {"internal/handlers/daemon"},
	"internal/handlers/daemon.Config":              {"internal/handlers/daemon"},
	"internal/handlers/mcp.Repo":                   {"internal/handlers/mcp"},
	"internal/handlers/mcp.Projects":               {"internal/handlers/mcp"},
	"internal/handlers/mcp.ProjectConfigs":         {"internal/handlers/mcp"},
	"internal/handlers/package_commands.Repo":      {"internal/handlers/package_commands"},
	"internal/handlers/package_commands.Projects":  {"internal/handlers/package_commands"},
	"internal/handlers/preset.Logger":              {"internal/handlers/preset"},
	"internal/handlers/preset.Config":              {"internal/handlers/preset"},
	"internal/handlers/project.Repo":               {"internal/handlers/project"},
	"internal/handlers/project.Projects":           {"internal/handlers/project"},
	"internal/handlers/question.Mailbox":           {"internal/handlers/question"},
	"internal/handlers/settings.Repo":              {"internal/handlers/settings"},
	"internal/handlers/settings.Refetch":           {"internal/handlers/settings"},
	"internal/handlers/settings.Privacy":           {"internal/handlers/settings"},
	"internal/handlers/settings.PollCopilotDevice": {"internal/handlers/settings"},
	"internal/handlers/streaming.ChatRows":         {"internal/handlers/streaming"},
	"internal/handlers/terminal.Logger":            {"internal/handlers/terminal"},
	"internal/handlers/terminal.Config":            {"internal/handlers/terminal"},
	"internal/handlers/token.Logger":               {"internal/handlers/token"},
	"internal/handlers/token.Config":               {"internal/handlers/token"},
	"internal/handlers/token.ControlPlane":         {"internal/handlers/token"},
	"internal/handlers/tools_daemon.Refetch":       {"internal/handlers/tools_daemon"},
	"internal/handlers/tools_daemon.Projects":      {"internal/handlers/tools_daemon"},
	"internal/handlers/tools_daemon.Installs":      {"internal/handlers/tools_daemon"},
	"internal/handlers/tools_daemon.Checkouts":     {"internal/handlers/tools_daemon"},
	"internal/handlers/tools_daemon.Pools":         {"internal/handlers/tools_daemon"},
	"internal/handlers/trigger.Repo":               {"internal/handlers/trigger"},
	"internal/handlers/trigger.Projects":           {"internal/handlers/trigger"},
	"internal/handlers/worktree_reclaim.Logger":    {"internal/handlers/worktree_reclaim"},
	"internal/handlers/worktree_reclaim.Config":    {"internal/handlers/worktree_reclaim"},
	// Required fields main added after this test was written (merged in
	// 2026-11-25); their own packages' unit tests leave them out.
	"internal/handlers/chat.QuestionResolver":            {"internal/handlers/chat"},
	"internal/handlers/chat.TriggerEvents":               {"internal/handlers/chat"},
	"internal/handlers/inbox.Chats":                      {"internal/handlers/inbox"},
	"internal/handlers/question.Resolver":                {"internal/handlers/question"},
	"internal/handlers/run.RunLists":                     {"internal/handlers/run"},
	"internal/launch.Runs":                               {"internal/launch"},
	"internal/launch.Selections":                         {"internal/launch"},
	"internal/launch.TaskQueue":                          {"internal/launch"},
	"internal/launch.Temporal":                           {"internal/launch"},
	"internal/launch.Threads":                            {"internal/launch"},
	"internal/launch.TriggerEvents":                      {"internal/launch"},
	"internal/workflowcheck.Store":                       {"internal/workflowcheck"},
	"internal/accesstokenclient.HTTPClient":              {"internal/accesstokenclient", "internal/handlers/approval", "internal/llm/drivers", "internal/orgcredentials", "internal/tokenauthority"},
	"internal/changetools.Session":                       {"internal/app"},
	"internal/chatprlinks.Chats":                         {"internal/chatprlinks"},
	"internal/chatprlinks.Store":                         {"internal/chatprlinks"},
	"internal/cliauth.HTTPClient":                        {"internal/cliauth"},
	"internal/cliauth.Now":                               {"internal/cliauth"},
	"internal/cliauth.OpenURL":                           {"internal/cliauth"},
	"internal/controlplane.ControlPlane":                 {"internal/controlplane"},
	"internal/controlplane.HTTPClient":                   {"internal/app", "internal/controlplane", "internal/mcpserver", "internal/workersetup", "internal/workflow/runtime/activities/handlers"},
	"internal/controlplane.Sign":                         {"internal/app", "internal/controlplane", "internal/mcpserver", "internal/workflow/runtime/activities/handlers"},
	"internal/credentials/automationtokens.Sealer":       {"internal/credentials/automationtokens"},
	"internal/credentials/connectionvault.DB":            {"internal/credentials/connectionvault"},
	"internal/credentials/orgaccounts.DB":                {"internal/credentials/orgaccounts"},
	"internal/credentials/orgaccounts.Sealer":            {"internal/credentials/orgaccounts"},
	"internal/credentials/provideraccounts.DB":           {"internal/credentials/provideraccounts"},
	"internal/credentials/provideraccounts.Sealer":       {"internal/credentials/provideraccounts"},
	"internal/daemonmirror.Config":                       {"internal/daemonmirror", "internal/daemonmirror/daemonmirrortest"},
	"internal/daemonmirror.Daemons":                      {"internal/daemonmirror"},
	"internal/daemonmirror.Lifecycle":                    {"internal/daemonmirror"},
	"internal/daemonmirror.Logger":                       {"internal/daemonmirror", "internal/daemonmirror/daemonmirrortest"},
	"internal/daemonmirror.Tx":                           {"internal/daemonmirror"},
	"internal/daemonmirror.Updates":                      {"internal/daemonmirror"},
	"internal/dblock.Config":                             {"internal/app", "internal/db", "internal/dblock"},
	"internal/dblock.Logger":                             {"internal/app", "internal/db", "internal/dblock"},
	"internal/dblock.Sessions":                           {"internal/dblock"},
	"internal/egresslimit.Clock":                         {"internal/app", "internal/egresslimit", "internal/toolexec", "internal/toolexec/daemonruntime"},
	"internal/egresslimit.Limits":                        {"internal/egresslimit"},
	"internal/egresslimit.Logger":                        {"internal/egresslimit", "internal/toolexec", "internal/toolexec/daemonruntime"},
	"internal/forgecred.Now":                             {"cmd/reliant/cmd"},
	"internal/forgecred.Org":                             {"internal/forgecred"},
	"internal/forgepin.CacheDir":                         {"internal/skills/catalog", "internal/toolexec/daemonruntime"},
	"internal/forgepin.Run":                              {"internal/skills/catalog", "internal/toolexec/daemonruntime"},
	"internal/gitcredentialclient.ControlPlane":          {"internal/gitcredentialclient"},
	"internal/gitcredentialclient.HTTPClient":            {"internal/app", "internal/gitcredentialclient", "internal/integrations/credwire"},
	"internal/handlers/account.Config":                   {"internal/handlers/account"},
	"internal/handlers/account.Logger":                   {"internal/handlers/account"},
	"internal/handlers/approval.Config":                  {"internal/handlers/approval"},
	"internal/handlers/approval.Connections":             {"e2e/stories", "internal/handlers/approval"},
	"internal/handlers/approval.Logger":                  {"internal/handlers/approval"},
	"internal/handlers/approval.Repo":                    {"internal/handlers/approval"},
	"internal/handlers/approval.Signals":                 {"internal/handlers/approval"},
	"internal/handlers/attachment.API":                   {"internal/handlers/attachment"},
	"internal/handlers/attachment.Attachments":           {"internal/handlers/attachment"},
	"internal/handlers/attachment.Config":                {"internal/handlers/attachment"},
	"internal/handlers/attachment.Logger":                {"internal/handlers/attachment"},
	"internal/handlers/automation_takeover.Config":       {"internal/handlers/automation_takeover"},
	"internal/handlers/automation_takeover.Logger":       {"internal/handlers/automation_takeover"},
	"internal/handlers/automation_takeover.Offers":       {"internal/handlers/automation_takeover"},
	"internal/handlers/automation_takeover.Repo":         {"internal/handlers/automation_takeover"},
	"internal/handlers/automation_takeover.Triggers":     {"internal/handlers/automation_takeover"},
	"internal/handlers/catalog.Config":                   {"internal/handlers/catalog"},
	"internal/handlers/catalog.Logger":                   {"internal/handlers/catalog"},
	"internal/handlers/chat.AgentMessages":               {"internal/handlers/chat"},
	"internal/handlers/chat.Backoff":                     {"internal/handlers/chat"},
	"internal/handlers/chat.ChatRows":                    {"internal/handlers/chat"},
	"internal/handlers/chat.ChatViews":                   {"internal/handlers/chat"},
	"internal/handlers/chat.Config":                      {"internal/handlers/chat"},
	"internal/handlers/chat.Logger":                      {"internal/handlers/chat"},
	"internal/handlers/chat.Mailbox":                     {"internal/handlers/chat"},
	"internal/handlers/chat.Messages":                    {"internal/handlers/chat"},
	"internal/handlers/chat.Pause":                       {"internal/handlers/chat"},
	"internal/handlers/chat.ProjectRows":                 {"internal/handlers/chat"},
	"internal/handlers/chat.Questions":                   {"internal/handlers/chat"},
	"internal/handlers/chat.Selections":                  {"internal/handlers/chat"},
	"internal/handlers/chat.Senders":                     {"internal/handlers/chat"},
	"internal/handlers/chat.Spawns":                      {"internal/handlers/chat"},
	"internal/handlers/chat.Temporal":                    {"internal/handlers/chat"},
	"internal/handlers/chat.ToolBlocks":                  {"internal/handlers/chat"},
	"internal/handlers/chat.Workflows":                   {"internal/handlers/chat"},
	"internal/handlers/chat.Worktrees":                   {"internal/handlers/chat"},
	"internal/handlers/connection.Config":                {"internal/handlers/connection"},
	"internal/handlers/connection.Logger":                {"internal/handlers/connection"},
	"internal/handlers/daemon_registry.Config":           {"internal/handlers/daemon_registry"},
	"internal/handlers/daemon_registry.Logger":           {"internal/handlers/daemon_registry"},
	"internal/handlers/daemon_registry.Mirror":           {"internal/handlers/daemon_registry"},
	"internal/handlers/daemon_registry.Pools":            {"internal/handlers/daemon_registry"},
	"internal/handlers/daemon_registry.Repo":             {"internal/handlers/daemon_registry"},
	"internal/handlers/file_system.Config":               {"internal/handlers/file_system"},
	"internal/handlers/file_system.Logger":               {"internal/handlers/file_system"},
	"internal/handlers/file_system.Placement":            {"internal/handlers/file_system"},
	"internal/handlers/file_system.Repo":                 {"internal/handlers/file_system"},
	"internal/handlers/file_system.Router":               {"internal/handlers/file_system"},
	"internal/handlers/forge.Config":                     {"internal/handlers/forge"},
	"internal/handlers/forge.Logger":                     {"internal/handlers/forge"},
	"internal/handlers/forge.Router":                     {"internal/handlers/forge"},
	"internal/handlers/inbox.Config":                     {"internal/handlers/inbox"},
	"internal/handlers/inbox.Firings":                    {"internal/handlers/inbox"},
	"internal/handlers/inbox.Items":                      {"internal/handlers/inbox"},
	"internal/handlers/inbox.Logger":                     {"internal/handlers/inbox"},
	"internal/handlers/inbox.Machines":                   {"internal/handlers/inbox"},
	"internal/handlers/inbox.Marks":                      {"internal/handlers/inbox"},
	"internal/handlers/inbox.Notices":                    {"internal/handlers/inbox"},
	"internal/handlers/inbox.OrgKeys":                    {"internal/handlers/inbox"},
	"internal/handlers/inbox.ProjectRows":                {"internal/handlers/inbox"},
	"internal/handlers/inbox.Recoveries":                 {"internal/handlers/inbox"},
	"internal/handlers/inbox.Repo":                       {"internal/handlers/inbox"},
	"internal/handlers/inbox.Storage":                    {"internal/handlers/inbox"},
	"internal/handlers/mcp.Config":                       {"internal/handlers/mcp"},
	"internal/handlers/mcp.Logger":                       {"internal/handlers/mcp"},
	"internal/handlers/mcp.ProjectRows":                  {"internal/handlers/mcp"},
	"internal/handlers/mcp.Refetch":                      {"internal/handlers/mcp"},
	"internal/handlers/message.Chats":                    {"internal/handlers/message"},
	"internal/handlers/message.Config":                   {"internal/handlers/message"},
	"internal/handlers/message.Logger":                   {"internal/handlers/message"},
	"internal/handlers/model_endpoint.Config":            {"internal/handlers/model_endpoint"},
	"internal/handlers/model_endpoint.Logger":            {"internal/handlers/model_endpoint"},
	"internal/handlers/model_endpoint.Machines":          {"internal/handlers/model_endpoint"},
	"internal/handlers/model_endpoint.Network":           {"internal/handlers/model_endpoint"},
	"internal/handlers/org_policy_impact.Config":         {"internal/handlers/org_policy_impact"},
	"internal/handlers/org_policy_impact.Logger":         {"internal/handlers/org_policy_impact"},
	"internal/handlers/org_provider_account.Config":      {"internal/handlers/org_provider_account"},
	"internal/handlers/org_provider_account.Logger":      {"internal/handlers/org_provider_account"},
	"internal/handlers/org_usage.Config":                 {"internal/handlers/org_usage"},
	"internal/handlers/org_usage.Logger":                 {"internal/handlers/org_usage"},
	"internal/handlers/package_commands.Config":          {"internal/handlers/package_commands"},
	"internal/handlers/package_commands.Favorites":       {"internal/handlers/package_commands"},
	"internal/handlers/package_commands.Logger":          {"internal/handlers/package_commands"},
	"internal/handlers/package_commands.Placement":       {"internal/handlers/package_commands"},
	"internal/handlers/package_commands.Router":          {"internal/handlers/package_commands"},
	"internal/handlers/plan.Config":                      {"internal/handlers/plan"},
	"internal/handlers/plan.Logger":                      {"internal/handlers/plan"},
	"internal/handlers/project.Config":                   {"internal/handlers/project"},
	"internal/handlers/project.Detachments":              {"internal/handlers/project"},
	"internal/handlers/project.Installs":                 {"internal/handlers/project"},
	"internal/handlers/project.Ledger":                   {"internal/handlers/project"},
	"internal/handlers/project.Logger":                   {"internal/handlers/project"},
	"internal/handlers/project.Lookups":                  {"internal/handlers/project"},
	"internal/handlers/project.Machines":                 {"internal/handlers/project"},
	"internal/handlers/project.ProjectConfigs":           {"internal/handlers/project"},
	"internal/handlers/project.ProjectRows":              {"internal/handlers/project"},
	"internal/handlers/project.Purges":                   {"internal/handlers/project"},
	"internal/handlers/project.Repos":                    {"internal/handlers/project"},
	"internal/handlers/project.Shared":                   {"internal/handlers/project"},
	"internal/handlers/provider_account.Accounts":        {"internal/handlers/provider_account"},
	"internal/handlers/provider_account.Chats":           {"internal/handlers/provider_account"},
	"internal/handlers/provider_account.Config":          {"internal/handlers/provider_account"},
	"internal/handlers/provider_account.Funding":         {"internal/handlers/provider_account"},
	"internal/handlers/provider_account.Logger":          {"internal/handlers/provider_account"},
	"internal/handlers/provider_account.Refetch":         {"internal/handlers/provider_account"},
	"internal/handlers/provider_account.Store":           {"internal/handlers/provider_account"},
	"internal/handlers/provider_account.ValidateKey":     {"internal/handlers/provider_account"},
	"internal/handlers/question.ChatRows":                {"internal/handlers/question"},
	"internal/handlers/question.ChatViews":               {"internal/handlers/question"},
	"internal/handlers/question.Config":                  {"internal/handlers/question"},
	"internal/handlers/question.Logger":                  {"internal/handlers/question"},
	"internal/handlers/question.Messages":                {"internal/handlers/question"},
	"internal/handlers/question.Questions":               {"internal/handlers/question"},
	"internal/handlers/question.Refetch":                 {"internal/handlers/question", "internal/workflow/runtime/replaytest"},
	"internal/handlers/question.Repo":                    {"internal/handlers/question"},
	"internal/handlers/question.Signals":                 {"internal/handlers/question"},
	"internal/handlers/repo.Config":                      {"internal/handlers/repo"},
	"internal/handlers/repo.Logger":                      {"internal/handlers/repo"},
	"internal/handlers/repo.Repos":                       {"internal/handlers/repo"},
	"internal/handlers/run.ChatRows":                     {"internal/handlers/run"},
	"internal/handlers/run.ChatViews":                    {"internal/handlers/run"},
	"internal/handlers/run.Chats":                        {"internal/handlers/run"},
	"internal/handlers/run.Config":                       {"internal/handlers/run"},
	"internal/handlers/run.Logger":                       {"internal/handlers/run"},
	"internal/handlers/run.Repo":                         {"internal/handlers/run"},
	"internal/handlers/run.Workflows":                    {"internal/handlers/run"},
	"internal/handlers/scenario.Config":                  {"internal/handlers/scenario"},
	"internal/handlers/scenario.Drafts":                  {"internal/handlers/scenario"},
	"internal/handlers/scenario.Logger":                  {"internal/handlers/scenario"},
	"internal/handlers/scenario.ProjectConfigs":          {"internal/handlers/scenario"},
	"internal/handlers/scenario.ProjectRows":             {"internal/handlers/scenario"},
	"internal/handlers/scenario.Projects":                {"internal/handlers/scenario"},
	"internal/handlers/settings.Accounts":                {"internal/handlers/settings"},
	"internal/handlers/settings.Config":                  {"internal/handlers/settings"},
	"internal/handlers/settings.ControlPlane":            {"internal/handlers/settings"},
	"internal/handlers/settings.ExchangeAntigravityCode": {"internal/handlers/settings"},
	"internal/handlers/settings.ExchangeClaudeCode":      {"internal/handlers/settings"},
	"internal/handlers/settings.ExchangeCodexCode":       {"internal/handlers/settings"},
	"internal/handlers/settings.Logger":                  {"internal/handlers/settings"},
	"internal/handlers/settings.ValidateKey":             {"internal/handlers/settings"},
	"internal/handlers/streaming.Config":                 {"internal/handlers/streaming"},
	"internal/handlers/streaming.Heads":                  {"internal/handlers/streaming"},
	"internal/handlers/streaming.Logger":                 {"internal/handlers/streaming"},
	"internal/handlers/streaming.OrgFeed":                {"internal/handlers/streaming"},
	"internal/handlers/streaming.Projects":               {"internal/grpc", "internal/handlers/chat", "internal/handlers/streaming"},
	"internal/handlers/streaming.Senders":                {"internal/handlers/streaming"},
	"internal/handlers/streaming.Siblings":               {"internal/handlers/streaming"},
	"internal/handlers/streaming.SpawnCalls":             {"internal/handlers/streaming"},
	"internal/handlers/streaming.UserLog":                {"internal/handlers/streaming"},
	"internal/handlers/system.Config":                    {"internal/handlers/system"},
	"internal/handlers/system.Logger":                    {"internal/handlers/system"},
	"internal/handlers/task.Config":                      {"internal/handlers/task"},
	"internal/handlers/task.Logger":                      {"internal/handlers/task"},
	"internal/handlers/tool_call.Chats":                  {"internal/handlers/tool_call"},
	"internal/handlers/tool_call.Config":                 {"internal/handlers/tool_call"},
	"internal/handlers/tool_call.Logger":                 {"internal/handlers/tool_call"},
	"internal/handlers/tool_call.Workflows":              {"internal/handlers/tool_call"},
	"internal/handlers/tools_daemon.MachineStats":        {"internal/grpc", "internal/handlers/project", "internal/handlers/tools_daemon", "internal/servergateway", "internal/workersetup"},
	"internal/handlers/trigger.Chats":                    {"internal/handlers/trigger"},
	"internal/handlers/trigger.Config":                   {"internal/handlers/trigger"},
	"internal/handlers/trigger.Firings":                  {"internal/handlers/trigger"},
	"internal/handlers/trigger.Logger":                   {"internal/handlers/trigger"},
	"internal/handlers/trigger.OrgMoves":                 {"internal/handlers/trigger"},
	"internal/handlers/trigger.Owners":                   {"internal/handlers/trigger"},
	"internal/handlers/trigger.Pools":                    {"internal/handlers/trigger"},
	"internal/handlers/trigger.TriggerWrites":            {"internal/handlers/trigger"},
	"internal/handlers/trigger.WebhookSecrets":           {"internal/handlers/trigger"},
	"internal/handlers/usage.Config":                     {"internal/handlers/usage"},
	"internal/handlers/usage.Logger":                     {"internal/handlers/usage"},
	"internal/handlers/workflow.Config":                  {"internal/handlers/workflow"},
	"internal/handlers/workflow.Logger":                  {"internal/handlers/workflow"},
	"internal/handlers/workflow.Pools":                   {"internal/handlers/workflow"},
	"internal/handlers/workflow.ProjectRows":             {"internal/handlers/workflow"},
	"internal/handlers/worktree.Chats":                   {"internal/handlers/worktree"},
	"internal/handlers/worktree.Config":                  {"internal/handlers/worktree"},
	"internal/handlers/worktree.Daemons":                 {"internal/handlers/worktree"},
	"internal/handlers/worktree.GitCredentials":          {"internal/handlers/worktree"},
	"internal/handlers/worktree.Logger":                  {"internal/handlers/worktree"},
	"internal/handlers/worktree.ProjectRows":             {"internal/handlers/worktree"},
	"internal/handlers/worktree.Projects":                {"internal/handlers/worktree"},
	"internal/handlers/worktree.Recoveries":              {"internal/handlers/worktree"},
	"internal/handlers/worktree.Refetch":                 {"internal/handlers/worktree"},
	"internal/handlers/worktree.Repos":                   {"internal/handlers/worktree"},
	"internal/handlers/worktree.Rows":                    {"internal/handlers/worktree"},
	"internal/hosteduse.HTTPClient":                      {"internal/app", "internal/hosteduse"},
	"internal/integrations/connauth.Broker":              {"internal/handlers/trigger", "internal/integrations/connauth", "internal/integrations/ghdelegated", "internal/triggers", "internal/workflow/runtime/activities/handlers"},
	"internal/integrations/connauth.Resolver":            {"internal/integrations/connauth"},
	"internal/integrations/ghusers.APIBaseURL":           {"internal/app", "internal/integrations/ghusers"},
	"internal/integrations/ghusers.HTTPClient":           {"internal/app", "internal/integrations/ghusers"},
	"internal/integrations/inputoptions.Credentials":     {"internal/integrations/inputoptions"},
	"internal/integrations/inputoptions.Logger":          {"internal/app", "internal/integrations/inputoptions", "internal/integrations/inputoptions/inputoptionstest"},
	"internal/integrations/inputoptions.Now":             {"internal/app", "internal/integrations/inputoptions", "internal/integrations/inputoptions/inputoptionstest"},
	"internal/integrations/inputoptions.Runner":          {"internal/integrations/inputoptions"},
	"internal/integrations/inputoptions.TTL":             {"internal/app", "internal/integrations/inputoptions", "internal/integrations/inputoptions/inputoptionstest"},
	"internal/integrations/inputoptions.Types":           {"internal/integrations/inputoptions"},
	"internal/integrations/writeledger.Now":              {"internal/integrations/writeledger", "internal/triggers", "internal/workflow/runtime/activities/handlers"},
	"internal/integrations/writeledger.PRLinks":          {"internal/integrations/writeledger", "internal/triggers", "internal/workflow/runtime/activities/handlers"},
	"internal/integrations/writeledger.Runs":             {"internal/integrations/writeledger", "internal/workflow/runtime/activities/handlers"},
	"internal/integrations/writeledger.Store":            {"internal/integrations/writeledger"},
	"internal/integrations/writeledger.Workflows":        {"internal/integrations/writeledger", "internal/triggers", "internal/workflow/runtime/activities/handlers"},
	"internal/launch.Chats":                              {"internal/launch"},
	"internal/launch.Pinner":                             {"e2e/stories", "internal/agentruns", "internal/handlers/trigger", "internal/handlers/workflow", "internal/launch", "internal/triggers", "internal/triggers/workflowevent", "internal/workflow/reconciliation"},
	"internal/launch.ProjectRows":                        {"internal/launch"},
	"internal/launch.Projects":                           {"internal/launch"},
	"internal/launch.Workflows":                          {"internal/launch"},
	"internal/launch.Worktrees":                          {"internal/launch"},
	"internal/llm/accountrouter.Now":                     {"internal/workersetup", "internal/workflow/runtime/activities/handlers"},
	"internal/llm/accountrouter.Rand":                    {"internal/workersetup"},
	"internal/llm/accountrouter.Store":                   {"internal/llm/accountrouter"},
	"internal/llmusage.Config":                           {"internal/llmusage", "internal/llmusage/llmusagetest", "internal/workersetup"},
	"internal/llmusage.Ledger":                           {"internal/llmusage"},
	"internal/llmusage.Logger":                           {"internal/llmusage", "internal/llmusage/llmusagetest", "internal/workersetup"},
	"internal/llmusage.Tools":                            {"internal/llmusage"},
	"internal/llmusage.Tx":                               {"internal/llmusage"},
	"internal/localprobe.HTTPClient":                     {"internal/toolexec/daemonruntime"},
	"internal/machinetools.Actions":                      {"internal/machinetools"},
	"internal/machinetools.Pools":                        {"internal/machinetools"},
	"internal/machinetools.Waker":                        {"internal/machinetools"},
	"internal/machinewake.Owners":                        {"internal/machinewake"},
	"internal/orgcredentials.Now":                        {"internal/app", "internal/orgcredentials"},
	"internal/orgmove.Config":                            {"internal/app", "internal/orgmove"},
	"internal/orgmove.Logger":                            {"internal/orgmove"},
	"internal/orgmove.Members":                           {"internal/orgmove"},
	"internal/planning.Config":                           {"internal/db", "internal/planning", "internal/planningtest"},
	"internal/planning.Dependencies":                     {"internal/planning"},
	"internal/planning.Logger":                           {"internal/db", "internal/planning", "internal/planningtest"},
	"internal/planning.Plans":                            {"internal/planning"},
	"internal/planning.Tasks":                            {"internal/planning"},
	"internal/privacy.Config":                            {"internal/privacy"},
	"internal/privacy.Logger":                            {"internal/privacy"},
	"internal/privacy.Settings":                          {"internal/privacy"},
	"internal/projectaccess.Store":                       {"internal/projectaccess"},
	"internal/providerbackoff.Activity":                  {"internal/providerbackoff"},
	"internal/providerbackoff.Config":                    {"internal/db", "internal/providerbackoff", "internal/providerbackoff/providerbackofftest", "internal/workersetup"},
	"internal/providerbackoff.Logger":                    {"internal/db", "internal/providerbackoff", "internal/providerbackoff/providerbackofftest", "internal/workersetup"},
	"internal/providerbackoff.Markers":                   {"internal/providerbackoff"},
	"internal/providerbackoff.Tx":                        {"internal/providerbackoff"},
	"internal/readguard.Limits":                          {"internal/app", "internal/readguard"},
	"internal/readguard.Procedures":                      {"internal/readguard"},
	"internal/runs.Waits":                                {"e2e/stories", "internal/handlers/chat", "internal/handlers/streaming", "internal/handlers/trigger"},
	"internal/seatmatch.Config":                          {"internal/handlers/settings", "internal/seatmatch"},
	"internal/seatmatch.Logger":                          {"internal/handlers/settings", "internal/seatmatch"},
	"internal/seatmatch.Store":                           {"internal/seatmatch"},
	"internal/threads.Chats":                             {"internal/agentruns", "internal/app", "internal/handlers/chat", "internal/handlers/streaming", "internal/handlers/trigger", "internal/handlers/workflow", "internal/launch", "internal/llm/tools", "internal/threads", "internal/triggers", "internal/triggers/workflowevent", "internal/workersetup", "internal/workflow/reconciliation", "internal/workflow/runtime/activities", "internal/workflow/runtime/activities/handlers"},
	"internal/threads.Messages":                          {"internal/app", "internal/handlers/chat", "internal/handlers/streaming", "internal/handlers/trigger", "internal/llm/tools", "internal/threads", "internal/workflow/runtime/activities/handlers"},
	"internal/threads.Senders":                           {"internal/app", "internal/handlers/chat", "internal/handlers/streaming", "internal/handlers/trigger", "internal/llm/tools", "internal/threads", "internal/workflow/runtime/activities/handlers"},
	"internal/threads.Signaler":                          {"e2e/stories", "internal/agentruns", "internal/app", "internal/handlers/chat", "internal/handlers/streaming", "internal/handlers/trigger", "internal/handlers/workflow", "internal/launch", "internal/llm/tools", "internal/threads", "internal/triggers", "internal/triggers/workflowevent", "internal/workersetup", "internal/workflow/reconciliation", "internal/workflow/runtime/activities", "internal/workflow/runtime/activities/handlers"},
	"internal/threads.ToolCanceler":                      {"e2e/stories", "internal/agentruns", "internal/app", "internal/handlers/chat", "internal/handlers/streaming", "internal/handlers/trigger", "internal/handlers/workflow", "internal/launch", "internal/llm/tools", "internal/threads", "internal/triggers", "internal/triggers/workflowevent", "internal/workersetup", "internal/workflow/reconciliation", "internal/workflow/runtime/activities", "internal/workflow/runtime/activities/handlers", "internal/workflow/runtime/replaytest"},
	"internal/tokenauthority.ControlPlane":               {"internal/grpc", "internal/tokenauthority"},
	"internal/tokenauthority.DB":                         {"internal/grpc", "internal/tokenauthority"},
	"internal/triggerowner.Adoptions":                    {"internal/triggerowner"},
	"internal/triggerowner.Config":                       {"e2e/stories", "internal/app", "internal/handlers/trigger", "internal/store/storetest", "internal/triggerowner", "internal/triggertools"},
	"internal/triggerowner.Locks":                        {"internal/triggerowner"},
	"internal/triggerowner.Logger":                       {"e2e/stories", "internal/app", "internal/handlers/trigger", "internal/store/storetest", "internal/triggerowner", "internal/triggertools"},
	"internal/triggerowner.Offers":                       {"internal/triggerowner"},
	"internal/triggerowner.OrgReads":                     {"internal/triggerowner"},
	"internal/triggerowner.Triggers":                     {"internal/triggerowner"},
	"internal/triggerowner.Tx":                           {"internal/triggerowner"},
	"internal/vault.Now":                                 {"cmd/reliant/cmd", "internal/app", "internal/connections", "internal/db", "internal/handlers/connection", "internal/handlers/trigger", "internal/integrations/connauth", "internal/integrations/ghdelegated", "internal/store/storetest", "internal/triggers", "internal/vault", "internal/workflow/runtime/activities/handlers", "tools/reliant-dev"},
	"internal/workflow/reconciliation.Chats":             {"internal/workflow/reconciliation"},
	"internal/workflow/reconciliation.Daemons":           {"internal/workflow/reconciliation"},
	"internal/workflow/reconciliation.Mailbox":           {"internal/workflow/reconciliation"},
	"internal/workflow/reconciliation.Orphans":           {"internal/workflow/reconciliation"},
	"internal/workflow/reconciliation.Workflows":         {"internal/workflow/reconciliation"},
	"internal/workflowcheck.GitHub":                      {"internal/app", "internal/workersetup", "internal/workflowcheck"},
	"internal/workflowcheck.RunURL":                      {"internal/app", "internal/triggers", "internal/workersetup", "internal/workflowcheck"},
	"internal/workflowdrafts.Config":                     {"internal/db", "internal/workflowdrafts", "internal/workflowdraftstest"},
	"internal/workflowdrafts.Drafts":                     {"internal/workflowdrafts"},
	"internal/workflowdrafts.Locks":                      {"internal/workflowdrafts"},
	"internal/workflowdrafts.Logger":                     {"internal/db", "internal/workflowdrafts", "internal/workflowdraftstest"},
	"internal/workflowdrafts.Tx":                         {"internal/workflowdrafts"},
	"internal/workflowdrafts.Updates":                    {"internal/workflowdrafts"},
}

func TestEveryConstructionSetsEveryRequiredDepsField(t *testing.T) {
	t.Parallel()

	repo := root(t)
	module := modulePath(t, repo)
	files := map[string][]byte{}
	err := filepath.WalkDir(repo, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p == repo {
				return nil
			}
			switch d.Name() {
			case ".git", "node_modules", "testdata", "vendor", "dist", ".reliant", ".forge":
				return filepath.SkipDir
			}
			if _, err := os.Stat(filepath.Join(p, "go.mod")); err == nil {
				return filepath.SkipDir // another module
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") {
			return nil
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if !strings.Contains(string(src), "Deps") {
			return nil
		}
		rel, err := filepath.Rel(repo, p)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = src
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", repo, err)
	}

	findings, err := missingDepsFields(module, files)
	if err != nil {
		t.Fatal(err)
	}
	// Read the baseline through git, not the live main checkout.
	base, unavailable, err := resolveDepsGuardBase(repo, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	if unavailable != "" {
		t.Skipf("skip: %s, so the guard cannot read the baseline it compares against. Set DEPS_GUARD_BASE=<rev>, or run `task test` in a full clone with origin/main fetched.", unavailable)
	}
	baseline := map[string][]byte{}
	archive := tar.NewReader(bytes.NewReader(depsGit(t, repo, "archive", base)))
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		// Use exactly the live walk's input set (including build-tagged files).
		if _, ok := files[header.Name]; !ok {
			continue
		}
		src, err := io.ReadAll(archive)
		if err != nil {
			t.Fatal(err)
		}
		baseline[header.Name] = src
	}
	previous, err := missingDepsFields(module, baseline)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range newDepsFindings(findings, previous) {
		t.Errorf("%s builds %s.Deps without %s (new relative to %s). Set every required dep, including in slow and build-tagged tests, or mark the field // forge:optional-dep if its consumer handles nil.",
			f.pos, path.Base(f.pkgDir), strings.Join(f.missing, ", "), base)
	}
}

func depsGit(t *testing.T, repo string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return out
}

// Compare counts per file, type and field rather than line numbers, which move
// on unrelated edits. A new required field is absent from the baseline and
// therefore still checks every existing construction, not just edited files.
func newDepsFindings(current, baseline []depsFinding) []depsFinding {
	key := func(f depsFinding, field string) string { return f.file + " " + f.pkgDir + "." + field }
	remaining := map[string]int{}
	for _, f := range baseline {
		for _, field := range f.missing {
			remaining[key(f, field)]++
		}
	}
	var out []depsFinding
	for _, f := range current {
		var added []string
		for _, field := range f.missing {
			k := key(f, field)
			if remaining[k] > 0 {
				remaining[k]--
			} else {
				added = append(added, field)
			}
		}
		if len(added) > 0 {
			f.missing = added
			out = append(out, f)
		}
	}
	return out
}

// depsGuardBase is the revision the guard's baseline is read at, from the
// environment: DEPS_GUARD_BASE when set; in a GitHub merge group HEAD^1, the
// queue base; in a Reliant merge queue gate, whose checkout has no
// origin/main, the batch's base, GATE_BASE (or CI_BASE). "" means the
// branch's merge base with origin/main.
func depsGuardBase(getenv func(string) string) string {
	if base := getenv("DEPS_GUARD_BASE"); base != "" {
		return base
	}
	if getenv("GITHUB_EVENT_NAME") == "merge_group" {
		return "HEAD^1"
	}
	return cmp.Or(getenv("GATE_BASE"), getenv("CI_BASE"))
}

// resolveDepsGuardBase is the commit the baseline is read at: depsGuardBase's
// explicit revision when the environment names one, else the merge base of
// origin/main and HEAD. When the host cannot provide that history (not a git
// checkout, no origin/main ref, or a shallow clone missing the commit) it
// returns why in unavailable, and the guard skips: a missing resource, not a
// finding. Anything else that fails to resolve, in a full clone, is an error.
func resolveDepsGuardBase(repo string, getenv func(string) string) (base, unavailable string, err error) {
	out, err := depsGitOutput(repo, "rev-parse", "--is-shallow-repository")
	if err != nil {
		return "", fmt.Sprintf("git history is unavailable (%s is not a git checkout: %v)", repo, err), nil
	}
	shallow := strings.TrimSpace(out) == "true"

	if base = depsGuardBase(getenv); base != "" {
		if _, err := depsGitOutput(repo, "rev-parse", "--verify", "--quiet", base+"^{commit}"); err != nil {
			if shallow {
				return "", fmt.Sprintf("git history is unavailable (shallow clone has no commit %s)", base), nil
			}
			return "", "", fmt.Errorf("deps guard baseline %q is not a commit: %w", base, err)
		}
		return base, "", nil
	}

	if _, err := depsGitOutput(repo, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/main^{commit}"); err != nil {
		return "", "the origin/main ref is unavailable", nil
	}
	out, err = depsGitOutput(repo, "merge-base", "origin/main", "HEAD")
	if err != nil {
		if shallow {
			return "", "git history is unavailable (shallow clone: no merge base of origin/main and HEAD)", nil
		}
		return "", "", fmt.Errorf("git merge-base origin/main HEAD: %w", err)
	}
	return strings.TrimSpace(out), "", nil
}

// depsGitOutput runs git in repo and returns its stdout; a failure carries
// git's stderr. GIT_DIR and its kin are dropped so a git hook's environment
// cannot point the command at another repository than repo.
func depsGitOutput(repo string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	for _, kv := range os.Environ() {
		switch k, _, _ := strings.Cut(kv, "="); k {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY":
		default:
			cmd.Env = append(cmd.Env, kv)
		}
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

// The guard skips only when the host lacks the history; a base it can resolve
// is used, and an explicit base that is not a commit in a full clone fails.
func TestResolveDepsGuardBaseSkipsOnlyWithoutHistory(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("skip: git is not installed")
	}
	dir := t.TempDir()
	run := func(d string, args ...string) string {
		t.Helper()
		out, err := depsGitOutput(d, append([]string{
			"-c", "user.name=t", "-c", "user.email=t@example.com",
			"-c", "init.defaultBranch=main", "-c", "commit.gpgsign=false",
		}, args...)...)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(out)
	}
	// upstream: c1 ← c2 on main, and c1 ← f1 on feature.
	upstream := filepath.Join(dir, "upstream")
	if err := os.Mkdir(upstream, 0o755); err != nil {
		t.Fatal(err)
	}
	run(upstream, "init", "-q")
	run(upstream, "commit", "-q", "--allow-empty", "-m", "c1")
	c1 := run(upstream, "rev-parse", "HEAD")
	run(upstream, "commit", "-q", "--allow-empty", "-m", "c2")
	run(upstream, "checkout", "-q", "-b", "feature", c1)
	run(upstream, "commit", "-q", "--allow-empty", "-m", "f1")
	url := "file://" + filepath.ToSlash(upstream)

	full := filepath.Join(dir, "full")
	run(dir, "clone", "-q", "--branch", "feature", url, full)
	// The fixer's checkout: the PR branch and origin/main, one commit each.
	shallow := filepath.Join(dir, "shallow")
	run(dir, "clone", "-q", "--depth", "1", "--branch", "feature", url, shallow)
	run(shallow, "fetch", "-q", "--depth", "1", "origin", "main:refs/remotes/origin/main")
	noOrigin := filepath.Join(dir, "no-origin")
	run(dir, "clone", "-q", "--branch", "feature", url, noOrigin)
	run(noOrigin, "update-ref", "-d", "refs/remotes/origin/main")
	notGit := filepath.Join(dir, "not-git")
	if err := os.Mkdir(notGit, 0o755); err != nil {
		t.Fatal(err)
	}

	env := func(kv ...string) func(string) string {
		return func(k string) string {
			for i := 0; i+1 < len(kv); i += 2 {
				if kv[i] == k {
					return kv[i+1]
				}
			}
			return ""
		}
	}
	for _, tc := range []struct {
		name, repo  string
		getenv      func(string) string
		wantBase    string
		unavailable string // substring of the skip reason; "" means resolved
		wantErr     bool
	}{
		{name: "full clone merge base", repo: full, getenv: env(), wantBase: c1},
		{name: "full clone explicit base", repo: full, getenv: env("DEPS_GUARD_BASE", "HEAD^1"), wantBase: "HEAD^1"},
		{name: "full clone bad explicit base fails", repo: full, getenv: env("DEPS_GUARD_BASE", "no-such-rev"), wantErr: true},
		{name: "shallow clone without merge base", repo: shallow, getenv: env(), unavailable: "shallow clone: no merge base"},
		{name: "shallow clone without explicit base", repo: shallow, getenv: env("DEPS_GUARD_BASE", "HEAD^1"), unavailable: "shallow clone has no commit HEAD^1"},
		{name: "shallow clone with explicit base", repo: shallow, getenv: env("DEPS_GUARD_BASE", "HEAD"), wantBase: "HEAD"},
		{name: "no origin/main ref", repo: noOrigin, getenv: env(), unavailable: "origin/main ref is unavailable"},
		{name: "not a git checkout", repo: notGit, getenv: env(), unavailable: "not a git checkout"},
	} {
		base, unavailable, err := resolveDepsGuardBase(tc.repo, tc.getenv)
		switch {
		case tc.wantErr:
			if err == nil {
				t.Errorf("%s: got base %q, skip %q, want an error", tc.name, base, unavailable)
			}
		case err != nil:
			t.Errorf("%s: %v", tc.name, err)
		case tc.unavailable != "":
			if !strings.Contains(unavailable, tc.unavailable) || base != "" {
				t.Errorf("%s: got base %q, skip %q, want a skip naming %q", tc.name, base, unavailable, tc.unavailable)
			}
		case unavailable != "" || base != tc.wantBase:
			t.Errorf("%s: got base %q, skip %q, want base %q", tc.name, base, unavailable, tc.wantBase)
		}
	}
}

func TestDepsGuardBaseHonoursTheQueueBase(t *testing.T) {
	for _, tc := range []struct {
		env  map[string]string
		want string
	}{
		{nil, ""},
		{map[string]string{"GATE_BASE": "g"}, "g"},
		{map[string]string{"CI_BASE": "c"}, "c"},
		{map[string]string{"GATE_BASE": "g", "CI_BASE": "c"}, "g"},
		{map[string]string{"GITHUB_EVENT_NAME": "merge_group", "GATE_BASE": "g"}, "HEAD^1"},
		{map[string]string{"DEPS_GUARD_BASE": "d", "GITHUB_EVENT_NAME": "merge_group", "GATE_BASE": "g"}, "d"},
	} {
		if got := depsGuardBase(func(k string) string { return tc.env[k] }); got != tc.want {
			t.Errorf("depsGuardBase(%v) = %q, want %q", tc.env, got, tc.want)
		}
	}
}

func TestDepsGuardIgnoresQueueBaselineButFindsRegressions(t *testing.T) {
	files := map[string][]byte{
		"svc/service.go":     []byte("package svc; type Deps struct { Repo any }; func New(Deps) {}"),
		"svc/queued_test.go": []byte("package svc; func queued() { New(Deps{}) }"),
	}
	baseline, err := missingDepsFields("example", files)
	if err != nil {
		t.Fatal(err)
	}
	if got := newDepsFindings(baseline, baseline); len(got) != 0 {
		t.Fatalf("unrelated queued construction failed: %v", got)
	}
	files["svc/new_test.go"] = []byte("//go:build integration\npackage svc; func added() { New(Deps{}) }")
	current, err := missingDepsFields("example", files)
	if err != nil {
		t.Fatal(err)
	}
	if got := newDepsFindings(current, baseline); len(got) != 1 || got[0].file != "svc/new_test.go" {
		t.Fatalf("new tagged construction not detected: %v", got)
	}
	delete(files, "svc/new_test.go")
	files["svc/service.go"] = []byte("package svc; type Deps struct { Repo any; Chats any }; func New(Deps) {}")
	current, err = missingDepsFields("example", files)
	if err != nil {
		t.Fatal(err)
	}
	if got := newDepsFindings(current, baseline); len(got) != 1 || strings.Join(got[0].missing, ",") != "Chats" {
		t.Fatalf("new field must check unchanged constructions: %v", got)
	}
}

// depsFinding is one Deps literal that leaves required fields unset.
type depsFinding struct {
	file    string   // module-relative file of the literal
	pos     string   // file:line of the literal
	pkgDir  string   // module-relative directory of the package declaring Deps
	missing []string // required fields it does not name, in declaration order
}

// depsStruct is a package's Deps struct: its fields in order, and which are
// marked `// forge:optional-dep`.
type depsStruct struct {
	fields   []string
	optional map[string]bool
}

// missingDepsFields finds, in files (module-relative path → source), every
// Deps literal that leaves a required field unset. Pure, so the rule itself
// is tested on fixtures (TestMissingDepsFieldsFindsTheShapesThatReddenedMain).
func missingDepsFields(module string, files map[string][]byte) ([]depsFinding, error) {
	fset := token.NewFileSet()
	parsed := map[string]*ast.File{}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		f, err := parser.ParseFile(fset, name, files[name], parser.ParseComments)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		parsed[name] = f
	}

	// The Deps structs, keyed by the import path of their package.
	structs := map[string]*depsStruct{}
	for _, name := range names {
		f := parsed[name]
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts := spec.(*ast.TypeSpec)
				st, ok := ts.Type.(*ast.StructType)
				if !ok || ts.Name.Name != "Deps" {
					continue
				}
				ds := &depsStruct{optional: map[string]bool{}}
				for _, field := range st.Fields.List {
					optional := field.Doc != nil && strings.Contains(field.Doc.Text(), "forge:optional-dep")
					for _, n := range fieldNames(field) {
						ds.fields = append(ds.fields, n)
						if optional {
							ds.optional[n] = true
						}
					}
				}
				structs[importPath(module, name)] = ds
			}
		}
	}

	toCall := depsLiteralsReachingCalls(module, parsed)
	var out []depsFinding
	for _, name := range names {
		f := parsed[name]
		self := importPath(module, name)
		imports := fileImports(f)
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			var decl string
			switch typ := lit.Type.(type) {
			case *ast.Ident:
				if typ.Name != "Deps" || !toCall[lit] {
					return true
				}
				decl = self
			case *ast.SelectorExpr:
				x, ok := typ.X.(*ast.Ident)
				if !ok || typ.Sel.Name != "Deps" {
					return true
				}
				decl = imports[x.Name]
			default:
				return true
			}
			ds := structs[decl]
			if ds == nil || decl == "" {
				return true
			}
			set := map[string]bool{}
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					return true // positional: every field is named by construction
				}
				if id, ok := kv.Key.(*ast.Ident); ok {
					set[id.Name] = true
				}
			}
			var missing []string
			for _, field := range ds.fields {
				if ds.optional[field] || set[field] {
					continue
				}
				if decl != self && !ast.IsExported(field) {
					continue // another package cannot set it
				}
				missing = append(missing, field)
			}
			if len(missing) > 0 {
				p := fset.Position(lit.Pos())
				out = append(out, depsFinding{
					file:    name,
					pos:     fmt.Sprintf("%s:%d", p.Filename, p.Line),
					pkgDir:  strings.TrimPrefix(strings.TrimPrefix(decl, module), "/"),
					missing: missing,
				})
			}
			return true
		})
	}
	return out, nil
}

// depsLiteralsReachingCalls follows local bindings and helper return values.
// Index package functions across files too: parser object links are file-local.
// A helper may be called bare (`New(makeDeps())`), qualified by its package
// (`New(launch.MakeDeps())`, resolved through the calling file's imports) or as a
// method (`New(f.deps())`). Without type information a method's receiver is
// unknown, so a method call follows every method of that name whose results
// include the package's own Deps (`Deps` or `*Deps`). Visited expressions bound
// recursive helpers and cyclic aliases; nested function bodies are inspected only
// when called, not as returns of their enclosing helper.
func depsLiteralsReachingCalls(module string, files map[string]*ast.File) map[*ast.CompositeLit]bool {
	// helper is a function or method declaration with the package and the
	// imports of the file declaring it, which its return values resolve in.
	type helper struct {
		decl    *ast.FuncDecl
		pkg     string
		imports map[string]string
	}
	functions := map[string]helper{} // "<import path>.<name>"
	methods := map[string][]helper{} // "<name>", Deps-returning methods only
	for name, f := range files {
		pkg, imports := importPath(module, name), fileImports(f)
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			h := helper{decl: fn, pkg: pkg, imports: imports}
			if fn.Recv == nil {
				functions[pkg+"."+fn.Name.Name] = h
			} else if returnsDeps(fn) {
				methods[fn.Name.Name] = append(methods[fn.Name.Name], h)
			}
		}
	}
	toCall := map[*ast.CompositeLit]bool{}
	seen := map[ast.Expr]bool{}
	var follow func(ast.Expr, string, map[string]string)
	returns := func(body *ast.BlockStmt, pkg string, imports map[string]string) {
		if body == nil {
			return
		}
		ast.Inspect(body, func(n ast.Node) bool {
			if _, nested := n.(*ast.FuncLit); nested {
				return false
			}
			if ret, ok := n.(*ast.ReturnStmt); ok {
				for _, result := range ret.Results {
					follow(result, pkg, imports)
				}
			}
			return true
		})
	}
	follow = func(expr ast.Expr, pkg string, imports map[string]string) {
		if expr == nil || seen[expr] {
			return
		}
		seen[expr] = true
		switch e := expr.(type) {
		case *ast.CompositeLit:
			toCall[e] = true
		case *ast.UnaryExpr:
			follow(e.X, pkg, imports)
		case *ast.ParenExpr:
			follow(e.X, pkg, imports)
		case *ast.CallExpr:
			follow(e.Fun, pkg, imports)
		case *ast.FuncLit:
			returns(e.Body, pkg, imports)
		case *ast.SelectorExpr:
			// A package-qualified helper: the qualifier is an import of this file
			// (and not a local name that shadows it).
			if x, ok := e.X.(*ast.Ident); ok && x.Obj == nil {
				if p, ok := imports[x.Name]; ok {
					if h, ok := functions[p+"."+e.Sel.Name]; ok {
						returns(h.decl.Body, h.pkg, h.imports)
					}
					return
				}
			}
			for _, h := range methods[e.Sel.Name] {
				returns(h.decl.Body, h.pkg, h.imports)
			}
		case *ast.Ident:
			if e.Obj == nil {
				if h, ok := functions[pkg+"."+e.Name]; ok {
					returns(h.decl.Body, h.pkg, h.imports)
				}
				return
			}
			switch d := e.Obj.Decl.(type) {
			case *ast.FuncDecl:
				returns(d.Body, pkg, imports)
			case *ast.AssignStmt:
				for _, rhs := range d.Rhs {
					follow(rhs, pkg, imports)
				}
			case *ast.ValueSpec:
				for _, value := range d.Values {
					follow(value, pkg, imports)
				}
			}
		}
	}
	for name, f := range files {
		pkg, imports := importPath(module, name), fileImports(f)
		ast.Inspect(f, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				for _, arg := range call.Args {
					follow(arg, pkg, imports)
				}
			}
			return true
		})
	}
	return toCall
}

// returnsDeps reports whether a function's results include its package's own
// Deps, by value or by pointer.
func returnsDeps(fn *ast.FuncDecl) bool {
	if fn.Type.Results == nil {
		return false
	}
	for _, field := range fn.Type.Results.List {
		typ := field.Type
		if star, ok := typ.(*ast.StarExpr); ok {
			typ = star.X
		}
		if id, ok := typ.(*ast.Ident); ok && id.Name == "Deps" {
			return true
		}
	}
	return false
}

// fileImports maps each import's local name in f to its import path.
func fileImports(f *ast.File) map[string]string {
	imports := map[string]string{}
	for _, im := range f.Imports {
		p, err := strconv.Unquote(im.Path.Value)
		if err != nil {
			continue
		}
		local := path.Base(p)
		if im.Name != nil {
			local = im.Name.Name
		}
		imports[local] = p
	}
	return imports
}

// fieldNames is a struct field's names; an embedded field's is its type's.
func fieldNames(field *ast.Field) []string {
	if len(field.Names) > 0 {
		out := make([]string, 0, len(field.Names))
		for _, n := range field.Names {
			out = append(out, n.Name)
		}
		return out
	}
	typ := field.Type
	if star, ok := typ.(*ast.StarExpr); ok {
		typ = star.X
	}
	switch t := typ.(type) {
	case *ast.Ident:
		return []string{t.Name}
	case *ast.SelectorExpr:
		return []string{t.Sel.Name}
	}
	return nil
}

// importPath is the import path of the package holding the module-relative
// file name.
func importPath(module, name string) string {
	dir := path.Dir(name)
	if dir == "." {
		return module
	}
	return module + "/" + dir
}

// modulePath reads the module path from repo's go.mod.
func modulePath(t *testing.T, repo string) string {
	t.Helper()
	for _, line := range strings.Split(readFile(t, filepath.Join(repo, "go.mod")), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.TrimSpace(rest)
		}
	}
	t.Fatalf("no module line in %s/go.mod", repo)
	return ""
}

// The shapes behind #3589 and #3605, on fixtures: a slow or build-tagged
// test that builds a service without a Deps field its pull request added.
func TestMissingDepsFieldsFindsTheShapesThatReddenedMain(t *testing.T) {
	t.Parallel()
	const module = "example.com/m"
	launchPkg := `package launch

type Deps struct {
	Store Store
	// Chats reads the chat a launch starts.
	Chats ChatReader
	// Pinner pins a chat. Nil pins nothing.
	// forge:optional-dep
	Pinner ChatPinner
	internal int
}

func New(d Deps) *Launcher { return nil }
`
	files := map[string][]byte{
		"internal/launch/launch.go": []byte(launchPkg),
		// #3605: a DB-backed test skipped under -short builds launch without
		// the Chats #3599 added.
		"internal/reconciliation/stranded_test.go": []byte(`package reconciliation

import "example.com/m/internal/launch"

func TestStuck(t *testing.T) {
	if testing.Short() { t.Skip("needs a database") }
	_ = launch.New(launch.Deps{Store: repo})
}
`),
		// #3589: an e2e-tagged harness, under an import alias.
		"e2e/stories/harness_test.go": []byte(`//go:build e2e

package stories

import l "example.com/m/internal/launch"

var deps = &l.Deps{Store: repo}
`),
		// Wired: Chats set, the optional Pinner and the unexported field left out.
		"internal/app/compose.go": []byte(`package app

import "example.com/m/internal/launch"

var _ = launch.New(launch.Deps{Store: repo, Chats: chats})
`),
		// Inside the package: New's literal is judged, a hand-built Service's is not.
		"internal/launch/launch_test.go": []byte(`package launch

var a = New(Deps{Store: s, Chats: c, internal: 1})
var b = New(Deps{Chats: c})
var c = &Launcher{deps: Deps{Store: s}}
func variable() {
	d := Deps{Store: s}
	New(d)
}
func wrapper(d Deps) { New(d) }
var wrapped = wrapper(Deps{Store: s})
func wiredVariable() {
	d := Deps{Store: s, Chats: c, internal: 1}
	New(d)
}
var fromHelper = New(makeDeps())
func makeDeps() Deps { return Deps{Store: s} }
var fromOtherFile = New(otherFileDeps())
var fromClosure = New(func() Deps { return Deps{Store: s} }())
var fromAlias = New(aliasDeps())
func aliasDeps() Deps { d := Deps{Store: s}; return d }
var recursive = New(recursiveDeps())
func recursiveDeps() Deps { if condition { return recursiveDeps() }; return Deps{Store: s} }
func unusedHelper() Deps { return Deps{Store: s} }
func wiredHelper() Deps { return Deps{Store: s, Chats: c, internal: 1} }
var wiredReturn = New(wiredHelper())
type fixture struct{}
func (fixture) deps() Deps { return Deps{Store: s} }
var fromMethod = New(fixture{}.deps())
func (*fixture) wiredDeps() *Deps { return &Deps{Store: s, Chats: c, internal: 1} }
var wiredMethod = New(*(&fixture{}).wiredDeps())
func (fixture) unusedDeps() Deps { return Deps{Store: s} }
`),
		"internal/launch/helpers_test.go": []byte(`package launch
func otherFileDeps() Deps { return Deps{Store: s} }
`),
		// Helpers of the launch package called package-qualified from another
		// package: MakeDeps reaches New, WiredDeps sets every field, and
		// UnusedDeps is never passed to a call.
		"internal/launch/factory.go": []byte(`package launch

func MakeDeps() Deps { return Deps{Store: s} }
func WiredDeps() Deps { return Deps{Store: s, Chats: c, internal: 1} }
func UnusedDeps() Deps { return Deps{Store: s} }
`),
		"internal/other/qualified_test.go": []byte(`package other

import ln "example.com/m/internal/launch"

var _ = ln.New(ln.MakeDeps())
var _ = ln.New(ln.WiredDeps())
`),
	}
	findings, err := missingDepsFields(module, files)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range findings {
		got[f.pos] = f.pkgDir + ": " + strings.Join(f.missing, ",")
	}
	want := map[string]string{
		"internal/reconciliation/stranded_test.go:7": "internal/launch: Chats",
		"e2e/stories/harness_test.go:7":              "internal/launch: Chats",
		"internal/launch/launch_test.go:4":           "internal/launch: Store,internal",
		"internal/launch/launch_test.go:7":           "internal/launch: Chats,internal",
		"internal/launch/launch_test.go:11":          "internal/launch: Chats,internal",
		"internal/launch/launch_test.go:17":          "internal/launch: Chats,internal",
		"internal/launch/launch_test.go:19":          "internal/launch: Chats,internal",
		"internal/launch/launch_test.go:21":          "internal/launch: Chats,internal",
		"internal/launch/launch_test.go:23":          "internal/launch: Chats,internal",
		"internal/launch/launch_test.go:28":          "internal/launch: Chats,internal",
		"internal/launch/helpers_test.go:2":          "internal/launch: Chats,internal",
		"internal/launch/factory.go:3":               "internal/launch: Chats,internal",
	}
	if len(got) != len(want) {
		t.Fatalf("findings = %v, want %v", got, want)
	}
	for pos, w := range want {
		if got[pos] != w {
			t.Errorf("finding at %s = %q, want %q (all: %v)", pos, got[pos], w, got)
		}
	}
}
