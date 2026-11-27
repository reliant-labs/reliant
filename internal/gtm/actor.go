// Copyright (c) 2026 Reliant Labs

package gtm

import "errors"

// ActorKind is whether a person or an agent acts.
type ActorKind int

// ActorKind values. The zero value is no kind, and an actor without one is
// refused.
const (
	// ActorPerson is a signed-in person (the Connect handler).
	ActorPerson ActorKind = iota + 1
	// ActorAgent is an agent acting for a person in a run (the GTM tools).
	ActorAgent
)

// String names the kind.
func (k ActorKind) String() string {
	switch k {
	case ActorPerson:
		return "person"
	case ActorAgent:
		return "agent"
	}
	return "unknown"
}

// Actor is who a call acts for. The handler builds a Person from the request's
// user; a tool builds an Agent from the run's user, org and chat.
type Actor struct {
	// UserID is the person, or the person an agent acts for.
	UserID string
	// OrgID is the org acted in; every row read or written is this org's.
	OrgID string
	Kind  ActorKind
	// ChatID is the agent's chat, recorded as an agent idea's source chat.
	ChatID string
	// ViaToken marks a Person acting through an access token rather than a
	// person-class credential. The handler sets it only for
	// ImportStrategyLibrary, the one call a token may make as a person, and
	// there a token only upserts: its prune is staged for a person to
	// confirm. Everywhere else a ViaToken actor is not a Person.
	ViaToken bool
}

// Person reports whether a person acts through a person-class credential:
// every person-only rule asks this, so a ViaToken actor never passes one.
func (a Actor) Person() bool { return a.Kind == ActorPerson && !a.ViaToken }

// credentialKind is the class of credential a person-kind actor came
// through, for the library audit.
func (a Actor) credentialKind() CredentialKind {
	if a.ViaToken {
		return CredentialToken
	}
	return CredentialPerson
}

func (a Actor) validate() error {
	switch {
	case a.UserID == "":
		return errors.New("gtm: actor has no user")
	case a.OrgID == "":
		return errors.New("gtm: actor has no org")
	case a.Kind != ActorPerson && a.Kind != ActorAgent:
		return errors.New("gtm: actor has no kind")
	case a.ViaToken && a.Kind != ActorPerson:
		return errors.New("gtm: only a person acts through a token")
	}
	return nil
}
