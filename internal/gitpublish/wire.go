// Copyright (c) 2025 Reliant Labs
package gitpublish

// The daemon commands a publish is made of. Two, so the credential exists for
// as short a time as it can: preparing a commit needs none, and only the push
// does.
const (
	// CommandPrepare stages and commits the worktree's changes and says what it
	// would push. No credential is involved.
	CommandPrepare = "git.publish_prepare"
	// CommandPush pushes the commit CommandPrepare made, with a credential that
	// is used for that one git process and for nothing else.
	CommandPush = "git.publish"
)

// PrepareRequest is the payload of CommandPrepare.
type PrepareRequest struct {
	// Worktree is the checkout to publish: an absolute path.
	Worktree string `json:"worktree"`
	// Message is the commit message, trailers excluded.
	Message string `json:"message"`
	// Trailers are appended to the message as git trailers, in order.
	Trailers []Trailer `json:"trailers,omitempty"`
	// AuthorName and AuthorEmail identify the commit when the machine has no git
	// identity of its own.
	AuthorName  string `json:"author_name,omitempty"`
	AuthorEmail string `json:"author_email,omitempty"`
}

// Trailer is one `Key: value` trailer line.
type Trailer struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// PrepareResponse is the reply of CommandPrepare.
type PrepareResponse struct {
	// Committed is true when this call made a commit from uncommitted changes.
	Committed bool `json:"committed"`
	// Commits is how many commits a push would publish: the commits on HEAD that
	// no remote-tracking branch has, this call's included. Zero means there is
	// nothing to publish (a clean checkout whose commits are already on a
	// remote): no push, and no error.
	Commits int `json:"commits"`
	// HeadSHA is the commit a push would publish.
	HeadSHA string `json:"head_sha"`
	// BaseSHA is the commit the unpublished commits sit on top of.
	BaseSHA      string `json:"base_sha,omitempty"`
	FilesChanged int    `json:"files_changed"`
	Insertions   int    `json:"insertions"`
	Deletions    int    `json:"deletions"`
}

// PushRequest is the payload of CommandPush.
//
// Token is the one-shot credential. It travels in this payload and nowhere
// else: the daemon hands it to the one git process that pushes, over a pipe the
// process inherits, and never to its environment, its arguments or a file.
// Nothing in this package logs a PushRequest.
type PushRequest struct {
	Worktree string `json:"worktree"`
	// RemoteURL is where to push: https://github.com/<owner>/<repo>.git, built
	// by the server from the node's repo. The checkout's own remotes are not
	// consulted, so a remote an agent repointed cannot redirect the credential.
	RemoteURL string `json:"remote_url"`
	Branch    string `json:"branch"`
	// ExpectSHA is the head CommandPrepare returned. The push refuses if the
	// checkout has moved since, so what is pushed is what was recorded.
	ExpectSHA string `json:"expect_sha"`
	Token     string `json:"token"`
}

// PushResponse is the reply of CommandPush.
type PushResponse struct {
	Pushed bool `json:"pushed"`
	// Refusal is set when the remote or the checkout refused the push: a
	// non-fast-forward, a moved head, a rejected hook on the remote. The reason
	// never contains the token.
	Refusal string `json:"refusal,omitempty"`
	// NonFastForward is true when the remote branch holds commits this checkout
	// does not: the push was refused rather than forced.
	NonFastForward bool `json:"non_fast_forward,omitempty"`
}
