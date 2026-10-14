// Copyright (c) 2025 Reliant Labs
package gitpublish

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeGitHub struct {
	def       string
	protected map[string]bool
	prs       map[int64]PullRequestHead
	err       error
	asked     []string
}

func (f *fakeGitHub) DefaultBranch(context.Context, Repo) (string, error) {
	f.asked = append(f.asked, "default")
	if f.err != nil {
		return "", f.err
	}
	return f.def, nil
}

func (f *fakeGitHub) Protected(_ context.Context, _ Repo, b string) (bool, error) {
	f.asked = append(f.asked, "protected:"+b)
	return f.protected[b], f.err
}

func (f *fakeGitHub) PullRequest(_ context.Context, _ Repo, n int64) (PullRequestHead, error) {
	pr, ok := f.prs[n]
	if !ok {
		return PullRequestHead{}, ErrPullRequestNotFound
	}
	return pr, nil
}

var acme = Repo{Owner: "acme", Name: "app"}

func newGH() *fakeGitHub {
	return &fakeGitHub{def: "trunk", protected: map[string]bool{"stable": true}, prs: map[int64]PullRequestHead{
		7:  {State: "open", HeadRepo: "acme/app", HeadRef: "fix/save", BaseRepo: "acme/app"},
		8:  {State: "open", HeadRepo: "mallory/app", HeadRef: "fix/save", BaseRepo: "acme/app"},
		9:  {State: "closed", HeadRepo: "acme/app", HeadRef: "old", BaseRepo: "acme/app"},
		10: {State: "open", HeadRepo: "acme/app", HeadRef: "trunk", BaseRepo: "acme/app"},
		11: {State: "open", HeadRepo: "", HeadRef: "gone", BaseRepo: "acme/app"},
	}}
}

func TestCheck_AllowsAFeatureBranch(t *testing.T) {
	d, err := Check(context.Background(), newGH(), Request{Repo: acme, Branch: "autopilot/issue-12", Allow: []string{"autopilot/*"}})
	require.NoError(t, err)
	assert.True(t, d.Allowed, d.Reason)
	assert.Equal(t, "autopilot/issue-12", d.Branch)
	assert.Equal(t, "trunk", d.Default)
}

func TestCheck_Refusals(t *testing.T) {
	for name, tc := range map[string]struct {
		req    Request
		reason string
	}{
		"the default branch, whatever it is called": {Request{Repo: acme, Branch: "trunk"}, "default branch"},
		"the default branch, other case":            {Request{Repo: acme, Branch: "Trunk"}, "default branch"},
		"main":                                      {Request{Repo: acme, Branch: "main"}, "protected name"},
		"master":                                    {Request{Repo: acme, Branch: "master"}, "protected name"},
		"a release branch":                          {Request{Repo: acme, Branch: "release/v1"}, "protected name"},
		"a nested release branch":                   {Request{Repo: acme, Branch: "release/v1/hotfix"}, "protected name"},
		"a protected branch":                        {Request{Repo: acme, Branch: "stable"}, "protected branch"},
		"a refs/ name":                              {Request{Repo: acme, Branch: "refs/heads/feat"}, "plain branch"},
		"a tag ref":                                 {Request{Repo: acme, Branch: "refs/tags/v1"}, "plain branch"},
		"a dotted escape":                           {Request{Repo: acme, Branch: "a/../main"}, "plain branch"},
		"a leading dash":                            {Request{Repo: acme, Branch: "--force"}, "plain branch"},
		"a space":                                   {Request{Repo: acme, Branch: "my branch"}, "plain branch"},
		"a lock name":                               {Request{Repo: acme, Branch: "feat.lock"}, "plain branch"},
		"a colon refspec":                           {Request{Repo: acme, Branch: "feat:main"}, "plain branch"},
		"a plus force refspec":                      {Request{Repo: acme, Branch: "+feat"}, "plain branch"},
		"outside the allow list":                    {Request{Repo: acme, Branch: "feature/x", Allow: []string{"autopilot/*"}}, "none of the allowed"},
		"no branch at all":                          {Request{Repo: acme}, "no branch"},
		"a branch and a pull request":               {Request{Repo: acme, Branch: "x", PRHeadOf: 7}, "not both"},
		"a pull request from a fork":                {Request{Repo: acme, PRHeadOf: 8}, "fork"},
		"a pull request with a deleted fork":        {Request{Repo: acme, PRHeadOf: 11}, "fork"},
		"a closed pull request":                     {Request{Repo: acme, PRHeadOf: 9}, "closed"},
		"a pull request whose head is the default":  {Request{Repo: acme, PRHeadOf: 10}, "default branch"},
		"a pull request that does not exist":        {Request{Repo: acme, PRHeadOf: 99}, "does not exist"},
		"an empty repository":                       {Request{Branch: "feat"}, "owner/name"},
	} {
		t.Run(name, func(t *testing.T) {
			d, err := Check(context.Background(), newGH(), tc.req)
			require.NoError(t, err)
			assert.False(t, d.Allowed, "must refuse")
			assert.Contains(t, d.Reason, tc.reason)
			assert.Empty(t, d.Branch, "a refusal names no branch to push to")
		})
	}
}

// A pull request's own head is allowed when it is in this repository, and it is
// not subject to the allow list, which names branches an automation may CREATE.
func TestCheck_PullRequestHead(t *testing.T) {
	d, err := Check(context.Background(), newGH(), Request{Repo: acme, PRHeadOf: 7, Allow: []string{"autopilot/*"}})
	require.NoError(t, err)
	assert.True(t, d.Allowed, d.Reason)
	assert.Equal(t, "fix/save", d.Branch)
}

// A pull request's head is still checked against the always-on rules.
func TestCheck_PullRequestHeadIsStillProtected(t *testing.T) {
	gh := newGH()
	gh.protected["fix/save"] = true
	d, err := Check(context.Background(), gh, Request{Repo: acme, PRHeadOf: 7})
	require.NoError(t, err)
	assert.False(t, d.Allowed)
	assert.Contains(t, d.Reason, "protected")
}

// Not being able to ask GitHub is not a pass: nothing may be pushed.
func TestCheck_FailsClosedWhenGitHubCannotBeAsked(t *testing.T) {
	gh := newGH()
	gh.err = errors.New("github unreachable")
	d, err := Check(context.Background(), gh, Request{Repo: acme, Branch: "autopilot/x"})
	require.Error(t, err)
	assert.False(t, d.Allowed)
}

func TestMatchGlob(t *testing.T) {
	for _, tc := range []struct {
		pattern, name string
		want          bool
	}{
		{"autopilot/*", "autopilot/issue-1", true},
		{"autopilot/*", "autopilot/a/b", true},
		{"autopilot/*", "autopilot", false},
		{"autopilot/*", "other/autopilot/x", false},
		{"release/*", "release/v1/hotfix", true},
		{"*", "anything", true},
		{"a*c", "abbbc", true},
		{"a*c", "ab", false},
		{"a*c", "c", false},
		{"fix/*-wip", "fix/save-wip", true},
		{"fix/*-wip", "fix/save", false},
		{"main", "main", true},
		{"main", "mainline", false},
	} {
		assert.Equal(t, tc.want, matchGlob(tc.pattern, tc.name), "%s ~ %s", tc.pattern, tc.name)
	}
}
