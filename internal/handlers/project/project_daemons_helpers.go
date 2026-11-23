package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/daemonevents"
	"github.com/reliant-labs/reliant/internal/daemonplacement"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/ospath"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// projectDirHealConsumerName is the durable JetStream consumer name used by
// StartDaemonEventConsumer. Stable across api-server restarts so progress is
// preserved; safe across multiple api-server replicas because JetStream
// serializes delivery to a single instance per durable.
const projectDirHealConsumerName = "api-server-project-dir-heal"

// StartDaemonEventConsumer subscribes to daemon.v1.events.connected on the
// DAEMON_EVENTS JetStream stream and heals user project directories on each
// connect. Blocks until ctx is cancelled.
//
// Why this exists: the api-server's local ToolsDaemonService never sees
// daemon connections — daemons connect to the daemon-gateway process, not
// here. Previously a DaemonConnectionListener on the local service was used
// for the same heal job, but it never fired in cloud OR OSS mode. The
// daemon-gateway publishes connect events to JetStream
// (internal/daemonevents/publisher.go); we consume those instead.
//
// The synchronous fs.mkdir at the top of CreateProject and the
// EnqueueDaemonCommand fallback both require the daemon to be known to the
// api-server — but during onboarding the api-server's daemons table is
// empty until the cloud pod actually opens its bidi stream, which races
// CreateProject by several seconds. Reacting to the connect event lets us
// mkdir each of the user's project paths the moment the daemon comes
// online, healing those rows without depending on the api-server having
// the daemon ID at create time.
//
// mkdir is idempotent, so JetStream redelivery on a missed Ack is harmless.
func (s *Service) StartDaemonEventConsumer(ctx context.Context, nc *nats.Conn) error {
	if s == nil || s.deps.Router == nil {
		logging.WarnContext(ctx, "daemon-events consumer not started: ProjectService has no daemon router")
		return nil
	}
	if nc == nil {
		logging.WarnContext(ctx, "daemon-events consumer not started: nil NATS connection")
		return nil
	}

	js, err := jetstream.New(nc)
	if err != nil {
		return fmt.Errorf("create jetstream context: %w", err)
	}

	// Look up the stream — don't create it. The daemon-gateway owns stream
	// config (file storage, 7-day retention). If it hasn't started yet,
	// exit gracefully; the heal job will be re-attempted on the next
	// api-server restart.
	stream, err := js.Stream(ctx, daemonevents.StreamDaemonEvents)
	if err != nil {
		if errors.Is(err, jetstream.ErrStreamNotFound) {
			logging.WarnContext(ctx, "daemon-events consumer not started: DAEMON_EVENTS stream does not exist yet (daemon-gateway will create it)")
			return nil
		}
		return fmt.Errorf("look up %s stream: %w", daemonevents.StreamDaemonEvents, err)
	}

	consumer, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		Durable:       projectDirHealConsumerName,
		FilterSubject: daemonevents.SubjectConnected,
		// Don't replay historical connects — every daemon that's already
		// connected when this consumer starts is irrelevant for heal; we
		// only care about future connect events. The reconciler equivalent
		// for the existing-state case is the synchronous mkdir during
		// CreateProject + the EnqueueDaemonCommand fallback.
		DeliverPolicy: jetstream.DeliverNewPolicy,
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	if err != nil {
		return fmt.Errorf("create/update %s consumer: %w", projectDirHealConsumerName, err)
	}

	cc, err := consumer.Consume(func(msg jetstream.Msg) {
		var evt daemonevents.Event
		if err := json.Unmarshal(msg.Data(), &evt); err != nil {
			logging.WarnContext(ctx, "daemon-events consumer: failed to unmarshal event, acking to avoid poison loop", "error", err)
			_ = msg.Ack()
			return
		}
		if evt.UserID == "" {
			_ = msg.Ack()
			return
		}
		if err := s.ensureProjectDirsOnDaemon(evt.UserID, evt.DaemonID); err != nil {
			// The machine stopped answering mid-heal. The connect event
			// is published only once its link answered a probe, so this
			// is a link that dropped again: try the whole heal later
			// (mkdir is idempotent), a bounded number of times.
			if md, mdErr := msg.Metadata(); mdErr == nil && md.NumDelivered < projectDirHealMaxDeliveries {
				_ = msg.NakWithDelay(projectDirHealRetryDelay)
				return
			}
		}
		_ = msg.Ack()
		// A machine that is back may hold checkouts of workspace creates
		// whose answer was lost; they are removed now rather than when a
		// retry trips over one (reliant #2413).
		s.startFailedCreateCleanup(evt.UserID, evt.DaemonID)
	})
	if err != nil {
		return fmt.Errorf("start consuming %s: %w", daemonevents.SubjectConnected, err)
	}
	defer cc.Stop()

	logging.InfoContext(ctx, "daemon-events consumer started",
		"stream", daemonevents.StreamDaemonEvents,
		"subject", daemonevents.SubjectConnected,
		"durable", projectDirHealConsumerName,
	)

	<-ctx.Done()
	logging.InfoContext(ctx, "daemon-events consumer stopped")
	return nil
}

// Project-dir heal retry bounds: a heal stopped by a transport failure is
// redelivered after projectDirHealRetryDelay, up to
// projectDirHealMaxDeliveries deliveries in all.
const (
	projectDirHealRetryDelay    = 30 * time.Second
	projectDirHealMaxDeliveries = 5
)

// ensureProjectDirsOnDaemon mkdirs each of the user's project paths on the
// connected daemon. Called by StartDaemonEventConsumer on every daemon
// connect event. Idempotent.
//
// The mkdirs go to daemonID, the machine that connected: sent to the user's
// default machine instead, a cloud machine's connect healed the user's laptop.
// A mkdir whose round trip failed stops the heal and is returned: every later
// one would meet the same link, one 30s timeout at a time. A mkdir the daemon
// itself refused is logged and the heal goes on.
//
// A connect event that names no machine heals nothing: there is no machine to
// pin the mkdirs to, and sending them to the default machine is the
// laptop-healed-for-a-cloud-machine bug above.
func (s *Service) ensureProjectDirsOnDaemon(userID, daemonID string) error {
	if daemonID == "" {
		logging.Warn("project-dir-heal: skipped; the connect event named no machine", "userID", userID)
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	projects, err := s.deps.Repo.ListProjects(ctx, db.ProjectFilters{UserID: userID, Limit: 200})
	if err != nil {
		logging.Warn("project-dir-heal: failed to list user projects", "error", err, "userID", userID, "daemonID", daemonID)
		return nil
	}
	// A project of an org the user has left is not theirs to put on a
	// machine any more.
	projects = authz.Visible(ctx, userID, projects, authz.CanWriteProject)
	// Nor is a project of another org than the machine's: a machine holds
	// only its organization's projects. Unknown, nothing is healed —
	// creating a directory is the irreversible direction.
	machine, err := s.deps.Repo.GetDaemon(ctx, daemonID)
	if err != nil && !svcerr.IsNotFound(err) {
		logging.Warn("project-dir-heal: failed to read the machine", "error", err, "userID", userID, "daemonID", daemonID)
		return nil
	}
	if err != nil {
		// No registry row: a machine of no known org, healed as before
		// machines had orgs (machineScope of nil is every org's).
		machine = nil
	}
	scope, err := s.machineScope(ctx, userID, machine)
	if err != nil {
		logging.Warn("project-dir-heal: could not tell which organization's projects the machine holds", "error", err, "userID", userID, "daemonID", daemonID)
		return nil
	}
	if scope != nil {
		// A machine of no known org heals only its owner's default org's
		// projects (machineScope).
		inScope := projects[:0]
		for _, project := range projects {
			if project != nil && scope.Includes(project.OrgID) {
				inScope = append(inScope, project)
			}
		}
		projects = inScope
	}
	if len(projects) == 0 {
		return nil
	}
	// The counts are logged, not just the total. The total alone read as
	// "ensured 2 project dirs" on 2026-10-07 when the heal had in fact
	// skipped both — they were repositories, whose directories only a clone
	// may create — and the log was the first place anyone looked for why the
	// new machine had no houndersclub directory.
	// A project the user detached from this machine stays off it: its folder
	// is still there or deliberately gone, and either way not the heal's to
	// make. Unreadable, nothing is healed — creating a directory is the
	// irreversible direction.
	detachments, err := s.deps.Detachments.ListProjectDaemonDetachmentsForDaemon(ctx, daemonID)
	if err != nil {
		logging.Warn("project-dir-heal: failed to read the machine's detached projects", "error", err, "userID", userID, "daemonID", daemonID)
		return nil
	}
	detached := make(map[string]bool, len(detachments))
	for _, d := range detachments {
		detached[d.ProjectID] = true
	}
	var healed, skippedRepository, skippedElsewhere, skippedDetached int
	defer func() {
		logging.Info("project-dir-heal: done",
			"userID", userID, "daemonID", daemonID, "projects", len(projects),
			"mkdir", healed, "skipped_repository", skippedRepository, "skipped_elsewhere", skippedElsewhere,
			"skipped_detached", skippedDetached)
	}()
	for _, project := range projects {
		// Judged with ospath because the mkdir runs on the daemon, not here.
		// Under filepath.IsAbs this heal silently skipped every project
		// belonging to a Windows daemon, so their directories were never
		// created and the skip left no trace.
		if project == nil {
			continue
		}
		if detached[project.ID] {
			skippedDetached++
			continue
		}
		if !projectDirIsHealable(project) {
			skippedRepository++
			continue
		}
		if project.Path == "" || !ospath.IsAbs(project.Path) {
			logging.Warn("project-dir-heal: skipping project with a non-absolute path",
				"userID", userID, "daemonID", daemonID, "projectID", project.ID, "path", project.Path)
			continue
		}
		// A plain folder that is already on another machine lives there: an
		// empty twin here would read as a second checkout (the connect
		// reconcile records it installed) and split the project's files.
		if elsewhere, err := s.installedElsewhere(ctx, userID, project.ID, daemonID); err != nil || elsewhere {
			skippedElsewhere++
			continue
		}
		if err := s.healProjectDir(ctx, userID, daemonID, project.Path); err != nil {
			if _, daemonRefused := toolexec.DaemonReportedText(err); daemonRefused {
				logging.Warn("project-dir-heal: failed to mkdir project path", "error", err, "userID", userID, "daemonID", daemonID, "path", project.Path)
				continue
			}
			logging.Info("project-dir-heal: stopped; the machine did not answer, so the heal is retried later",
				"error", err, "userID", userID, "daemonID", daemonID, "path", project.Path)
			return err
		}
		// The folder this made is the project's checkout on this machine, so
		// it is recorded in the same step: a chat's directory comes only from
		// a machine's own project_daemons row. Recorded here rather than left
		// to the connect-time reconcile, which runs concurrently and may have
		// probed before the mkdir landed — the outcome no longer depends on
		// which finishes first. Neither ever removes a row, and both write the
		// same (project, machine, path).
		// Never over a detach that committed while the mkdir ran, nor for a
		// project moved out of the machine's org meanwhile.
		if _, err := s.deps.Repo.RecordObservedProjectDaemonInScope(ctx, project.ID, daemonID, project.Path, project.DefaultBranch, scope); err != nil {
			logging.Warn("project-dir-heal: made the folder but could not record it; the next connect's reconcile records it",
				"error", err, "userID", userID, "daemonID", daemonID, "projectID", project.ID)
		}
		healed++
	}
	return nil
}

// healProjectDir sends one heal mkdir to daemonID, pinned: the heal is about
// the machine that connected, and default resolution may name another.
func (s *Service) healProjectDir(ctx context.Context, userID, daemonID, path string) error {
	return s.sendToDaemon(ctx, userID, daemonID, "fs.mkdir", map[string]string{"path": path}, nil)
}

// installedElsewhere reports whether the project's checkout is recorded on
// another machine that exists and is the user's (daemonplacement.UserCheckouts),
// whether or not it is connected. A row of a removed or replaced machine, or
// of another user's, is not "elsewhere": nothing can run there, and counting
// it left a replaced machine's plain folders (onboarding's my-project among
// them) never healed again. A machine that is kept but never reconnects still
// counts: the refusal names it, and removing it lets the heal run. An error
// reads as "elsewhere": the heal is best effort, and creating a directory is
// the irreversible direction.
func (s *Service) installedElsewhere(ctx context.Context, userID, projectID, daemonID string) (bool, error) {
	rows, err := daemonplacement.UserCheckouts(ctx, s.deps.Repo, userID, projectID)
	if err != nil {
		return true, err
	}
	for _, row := range rows {
		if row.DaemonID != daemonID {
			return true, nil
		}
	}
	return false, nil
}

// projectDirIsHealable reports whether project-dir-heal may create a
// project's directory on a daemon that does not have it.
//
// The heal exists for an EMPTY project created while its daemon was still
// booting, whose mkdir missed the machine. It must never create the directory
// of a project whose checkout comes from a repository. That directory is the
// clone's to create — atomically, and only once the clone succeeded — and an
// empty directory at the project path is read by every other surface as "the
// checkout is here". The heal running on daemon connect is exactly how a
// queued clone that never ran became an empty project the product called
// installed: the connect-time mkdir made the directory, and the connect-time
// reconcile then saw it and marked the install done.
//
// A remote URL is what marks a repository-backed project: CreateProjectFromRepo
// records it, and CreateProject records it when the directory it opened was
// already a checkout. A project with no remote is a plain directory, which an
// empty mkdir faithfully recreates.
func projectDirIsHealable(p *db.Project) bool {
	return p.RemoteURL == nil || strings.TrimSpace(*p.RemoteURL) == ""
}

// projectDaemonToProto converts a db.ProjectDaemon to its proto form.
func projectDaemonToProto(pd *db.ProjectDaemon) *reliantv1.ProjectDaemon {
	out := &reliantv1.ProjectDaemon{
		ProjectId:    pd.ProjectID,
		DaemonId:     pd.DaemonID,
		Path:         pd.Path,
		ClonedAt:     pd.ClonedAt.Format(time.RFC3339),
		InstallState: installStateToProto(pd.InstallState),
		InstallError: pd.InstallError,
	}
	if pd.DefaultBranch != nil {
		out.DefaultBranch = pd.DefaultBranch
	}
	if pd.VerifiedAt != nil {
		verified := pd.VerifiedAt.UTC().Format(time.RFC3339)
		out.VerifiedAt = &verified
	}
	return out
}

// machineNames names each machine once per response: a project list names
// the same few machines over and over.
type machineNames struct {
	s     *Service
	names map[string]string
}

func (s *Service) newMachineNames() *machineNames {
	return &machineNames{s: s, names: map[string]string{}}
}

// proto is row as the wire carries it, its machine named.
func (m *machineNames) proto(ctx context.Context, row *core.ProjectDaemon) *reliantv1.ProjectDaemon {
	out := projectDaemonToProto(row)
	out.DaemonName = m.name(ctx, row.DaemonID)
	return out
}

// name is what to call daemonID, looked up once per response.
func (m *machineNames) name(ctx context.Context, daemonID string) string {
	name, ok := m.names[daemonID]
	if !ok {
		name = strings.Trim(m.s.machineName(ctx, daemonID), `"`)
		m.names[daemonID] = name
	}
	return name
}

// installStateToProto maps the stored state onto the wire enum. An empty
// value means the row predates the column, and those rows are installed by
// definition: the old flow only ever wrote one after a completed clone.
func installStateToProto(state core.ProjectInstallState) reliantv1.ProjectInstallState {
	switch state {
	case core.ProjectInstallInstalling:
		return reliantv1.ProjectInstallState_PROJECT_INSTALL_STATE_INSTALLING
	case core.ProjectInstallFailed:
		return reliantv1.ProjectInstallState_PROJECT_INSTALL_STATE_FAILED
	default:
		return reliantv1.ProjectInstallState_PROJECT_INSTALL_STATE_INSTALLED
	}
}

// resolveProjectDaemonRow loads the project_daemons row for (project_id,
// daemon_id) and the parent project, enforcing user ownership. Returns
// NotFound when there's no clone of that project on that daemon.
func (s *Service) resolveProjectDaemonRow(ctx context.Context, projectID, daemonID, userID string) (*db.Project, *core.ProjectDaemon, error) {
	if projectID == "" || daemonID == "" {
		return nil, nil, svcerr.InvalidArgument("project_id and daemon_id are required")
	}
	// A project owned by someone else is the store's NotFound, the same as a
	// missing one, so the answer never reveals that it exists.
	project, err := s.deps.Projects.GetProjectWithUserCheck(ctx, projectID, userID)
	if err != nil {
		return nil, nil, svcerr.WrapOr(err, svcerr.Internal("failed to get project"))
	}
	rows, err := s.deps.Repo.ListProjectDaemonsForProject(ctx, projectID)
	if err != nil {
		return nil, nil, svcerr.WrapOr(err, svcerr.Internal("failed to look up project_daemons"))
	}
	for _, pd := range rows {
		if pd.DaemonID == daemonID {
			return project, pd, nil
		}
	}
	return nil, nil, svcerr.WithMessage(svcerr.ErrNotFound, "project is not cloned on that daemon")
}

// removeCheckout removes project's checkout pd from its machine and, once
// the machine has, its project_daemons row: RemoveProjectFromDaemon's work,
// which a project delete's machine fan-out repeats per machine
// (project_delete.go). The caller has decided userID may.
func (s *Service) removeCheckout(ctx context.Context, userID string, project *db.Project, pd *core.ProjectDaemon) error {
	machine := s.machineName(ctx, pd.DaemonID)
	// Recorded before it is sent, and holding back workspace creation on the
	// checkout until the machine says how it ended (checkout_teardown.go).
	op, done, err := s.beginTeardown(ctx, userID, project, pd, pd.DaemonID, db.CheckoutTeardownRemove, machine)
	if err != nil {
		return err
	}
	if done {
		// A retry of a remove the machine has since finished.
		return nil
	}
	// The row goes only once the machine has removed the checkout, in the
	// same step that records the op done (db.Repo.SettleCheckoutTeardown). It
	// is the only record of a checkout on that machine: dropped while the
	// clone is still on disk (the machine was offline, the remove failed),
	// the machine is refused the project while its files sit there
	// unrecorded, and the user is told to clone what they already have. The
	// remove is retried from here; a machine that is gone for good releases
	// its rows when it is removed (db.Repo.RemoveDaemon).
	var reply teardownReply
	payload := map[string]string{"path": pd.Path, "op_id": op.ID}
	sendErr := teardownMachine{s: s, userID: userID, daemonID: pd.DaemonID}.Send(ctx, "git.remove", payload, &reply, teardownTimeoutMs)
	return s.settleTeardown(ctx, userID, op, project, machine, reply, sendErr)
}

// removeFromOfflineMachine is RemoveProjectFromDaemon's refusal for a machine
// that is not connected. The checkout is still on that disk and the row is
// its only record, so the row stays; the message names what lets it go.
const removeFromOfflineMachine = "that machine is offline, so the project could not be removed from it: " +
	"bring it online and try again, or remove the machine (that releases every project recorded on it)"
