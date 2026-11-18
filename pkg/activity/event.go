package activity

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"time"
)

// ActorKind classifies who performed an action.
type ActorKind string

const (
	// ActorUser is a person, signed in.
	ActorUser ActorKind = "user"
	// ActorMachine is a non-human principal acting on someone's behalf: a
	// CI job, an API key, a service account.
	ActorMachine ActorKind = "machine"
	// ActorSystem is the application itself: a converger, a scheduler, an
	// alert evaluator.
	ActorSystem ActorKind = "system"
)

// Valid reports whether k is one of the three kinds.
func (k ActorKind) Valid() bool {
	switch k {
	case ActorUser, ActorMachine, ActorSystem:
		return true
	}
	return false
}

// Actor is who performed an action.
type Actor struct {
	// ID is the stable identifier filters match on (a user id, "ci",
	// "converger").
	ID string
	// Name is how the actor is shown. Empty falls back to ID.
	Name string
	// Kind classifies the actor.
	Kind ActorKind
}

// User returns a person actor.
func User(id, name string) Actor { return Actor{ID: id, Name: name, Kind: ActorUser} }

// Machine returns a non-human principal actor.
func Machine(id, name string) Actor { return Actor{ID: id, Name: name, Kind: ActorMachine} }

// System returns an actor for the application itself, named name.
func System(name string) Actor { return Actor{ID: name, Name: name, Kind: ActorSystem} }

// Target is the object an action was done to.
type Target struct {
	// Kind is the object's type, a lower_snake token: "release", "secret".
	Kind string
	// ID identifies the object within its kind.
	ID string
	// Label is a human-readable name. Empty falls back to ID.
	Label string
}

// Event is one entry in the activity stream.
type Event struct {
	// ID is assigned by the store on write; set on events List returns.
	ID string
	// OccurredAt is when the action happened. Zero is "now" at write.
	OccurredAt time.Time
	// Scope is what reads filter by: an org, a project. Empty is
	// unscoped.
	Scope string
	// Actor performed the action.
	Actor Actor
	// Verb is the action, a lower_snake token: "promote", "rotate".
	Verb string
	// Target is the object acted on.
	Target Target
	// Env is the environment the action happened in. Empty is a
	// project-level event.
	Env string
	// Via is the channel the action came through: "console", "cli", "ci",
	// "api", or a system's own name.
	Via string
	// Procedure is the RPC that recorded the event, when one did.
	Procedure string
	// Detail carries small, declared extras ({"from_env": "e2e"}).
	Detail map[string]string
	// TraceID ties the event to the request's trace, when there was one.
	TraceID string
}

// Writer persists events. [Store] is the database writer; [LogWriter] is the
// slog-only one the interceptor uses when no store is wired.
type Writer interface {
	Write(ctx context.Context, ev Event) error
}

// WriterFunc adapts a function to [Writer].
type WriterFunc func(ctx context.Context, ev Event) error

// Write calls f.
func (f WriterFunc) Write(ctx context.Context, ev Event) error { return f(ctx, ev) }

// tokenRE is the shape of a verb and a target kind.
var tokenRE = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// normalize fills the defaults a written event never leaves empty.
func (ev *Event) normalize() {
	if ev.OccurredAt.IsZero() {
		ev.OccurredAt = time.Now().UTC()
	}
	if ev.Actor.Name == "" {
		ev.Actor.Name = ev.Actor.ID
	}
	if ev.Target.Label == "" {
		ev.Target.Label = ev.Target.ID
	}
}

// validate reports what makes ev unrecordable. Every problem is reported at
// once, so a caller fixes them in one pass.
func (ev Event) validate() error {
	var errs []error
	if ev.Actor.ID == "" {
		errs = append(errs, errors.New("actor id is empty"))
	}
	if !ev.Actor.Kind.Valid() {
		errs = append(errs, fmt.Errorf("actor kind %q is not user, machine or system", ev.Actor.Kind))
	}
	if !tokenRE.MatchString(ev.Verb) {
		errs = append(errs, fmt.Errorf("verb %q is not a lower_snake token", ev.Verb))
	}
	if !tokenRE.MatchString(ev.Target.Kind) {
		errs = append(errs, fmt.Errorf("target kind %q is not a lower_snake token", ev.Target.Kind))
	}
	if ev.Target.ID == "" {
		errs = append(errs, errors.New("target id is empty"))
	}
	if len(errs) > 0 {
		return fmt.Errorf("activity: invalid event: %w", errors.Join(errs...))
	}
	return nil
}

// LogWriter returns a [Writer] that records each event as one slog line
// (message "activity.event", log_type=activity) and persists nothing. It is
// the interceptor's writer until a [Store] is wired, so a declaration is
// visible in the logs from the first deploy. A nil logger is slog.Default().
func LogWriter(logger *slog.Logger) Writer {
	if logger == nil {
		logger = slog.Default()
	}
	return WriterFunc(func(ctx context.Context, ev Event) error {
		attrs := []slog.Attr{
			slog.String("log_type", "activity"),
			slog.String("actor_id", ev.Actor.ID),
			slog.String("actor_kind", string(ev.Actor.Kind)),
			slog.String("verb", ev.Verb),
			slog.String("target_kind", ev.Target.Kind),
			slog.String("target_id", ev.Target.ID),
			slog.String("target_label", ev.Target.Label),
			slog.String("env", ev.Env),
			slog.String("via", ev.Via),
			slog.String("scope", ev.Scope),
			slog.String("procedure", ev.Procedure),
		}
		if len(ev.Detail) > 0 {
			attrs = append(attrs, slog.Any("detail", ev.Detail))
		}
		logger.LogAttrs(ctx, slog.LevelInfo, "activity.event", attrs...)
		return nil
	})
}
