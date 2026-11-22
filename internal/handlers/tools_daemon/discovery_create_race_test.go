package tools_daemon

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/store"
)

// createRaceRepo loses this machine's first project create to another
// machine's discovery of the same folder: just before the insert, winner
// commits that discovery on its own connection (not the caller's
// transaction), so the insert then meets the project's unique key. The
// resolution runs one attempt: the repository retries a unique violation,
// and a retry finds the winner by its path, so the recovery after a lost
// create (lostProjectCreateError) is reached only once the retries ran out,
// which one attempt stands for.
type createRaceRepo struct {
	*db.Repo
	once   sync.Once
	winner func()
}

func (r *createRaceRepo) CreateProject(ctx context.Context, p *db.Project) error {
	r.once.Do(r.winner)
	return r.Repo.CreateProject(ctx, p)
}

// raceCheckouts is the machine's checkouts with the locked transaction run
// once, as createRaceRepo's comment says.
type raceCheckouts struct {
	MachineCheckouts
	repo *db.Repo
}

func (r raceCheckouts) WithMachineCheckoutsLocked(ctx context.Context, _ string, fn func(ctx context.Context) error) error {
	return r.repo.RunTx(ctx, orm.ReadCommitted.WithMaxAttempts(1), fn)
}

// Review round 9 (B1): two machines discover the same new folder at once. The
// one whose create loses the race recovers the winner's project, and records
// its own checkout of it as well, so its config snapshot is stored at once,
// not dropped until it reconnects.
func TestDiscoveryThatLosesTheCreateRace_RecordsItsCheckoutAndStoresTheSnapshot(t *testing.T) {
	repo := db.NewTestRepo(t)
	ctx := context.Background()
	userID := "race-user-" + uuid.NewString()
	loser, winner := "machine-loser-"+uuid.NewString(), "machine-winner-"+uuid.NewString()
	for _, id := range []string{loser, winner} {
		require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: id, UserID: userID}))
	}
	path := "/home/workspace/projects/race-" + uuid.NewString()

	var winnerID string
	racing := &createRaceRepo{Repo: repo, winner: func() {
		// The winning machine's discovery, whole: its project, and its own
		// checkout of it.
		winnerID = uuid.NewString()
		require.NoError(t, repo.CreateProject(context.Background(), &db.Project{ID: winnerID, UserID: userID, Name: "race", Path: path,
			CreatedAt: fixedNow, UpdatedAt: fixedNow, LastActive: fixedNow}))
		recorded, err := repo.RecordObservedProjectDaemonInScope(context.Background(), winnerID, winner, path, nil, nil)
		require.NoError(t, err)
		require.True(t, recorded)
	}}
	svc := newTestService(racing, raceCheckouts{MachineCheckouts: store.NewProjectDaemonDetachmentExtStore(repo.ORM()), repo: repo})
	defer svc.Close()
	conn := newTestConn(userID, loser, nil)

	require.NoError(t, svc.persistProjectConfigSnapshot(ctx, conn, &reliantv1.ProjectConfigSnapshot{
		ProjectPath: path, DaemonTimestampUnixMs: fixedNow.UnixMilli(), UserConfigYaml: []byte("from the loser"),
	}, false))

	require.NotEmpty(t, winnerID, "the create race never ran")
	rows, err := repo.ListProjectDaemonsForProject(ctx, winnerID)
	require.NoError(t, err)
	var machines []string
	for _, r := range rows {
		machines = append(machines, r.DaemonID)
	}
	assert.ElementsMatch(t, []string{winner, loser}, machines, "the machine that lost the create holds the project too")
	record, synced, err := repo.GetProjectMachineConfigRecord(ctx, winnerID, loser)
	require.NoError(t, err)
	require.True(t, synced, "the losing machine's snapshot was dropped until a reconnect")
	assert.Equal(t, "from the loser", *record.UserConfigYAML)
}
