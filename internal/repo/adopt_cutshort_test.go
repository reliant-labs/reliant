// Copyright (c) 2025 Reliant Labs
package repo

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
)

type answeringMachine string

func (a answeringMachine) SendDaemonCommand(context.Context, string, string, []byte, int32) ([]byte, error) {
	return []byte(a), nil
}

// A scan the machine cut short before finding a repository is not "this
// project has no repository": adoption says the search stopped early, and why,
// rather than the NoReposMessage refusal.
func TestAdoptFromDaemonSaysACutShortSearchIsNotAnEmptyOne(t *testing.T) {
	t.Parallel()
	project := &db.Project{ID: "p-1", UserID: "u", Path: "/Users/me"}
	_, err := AdoptFromDaemon(context.Background(), nil,
		answeringMachine(`{"discovered":[],"truncated":true,"truncated_reason":"it ran out of time"}`), project)
	var cutShort *SearchCutShortError
	require.True(t, errors.As(err, &cutShort), "got %v", err)
	require.Contains(t, err.Error(), "/Users/me stopped early, because it ran out of time")

	// An empty, complete scan is still the plain "none found".
	repos, err := AdoptFromDaemon(context.Background(), nil, answeringMachine(`{"discovered":[]}`), project)
	require.NoError(t, err)
	require.Empty(t, repos)
}
