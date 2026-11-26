package accesstoken

// ── Changes ───────────────────────────────────────────────────────────
//
// changes:read, changes:write and changes:launch control Changes: one record
// per change per environment that says who gets a declared behavior
// (everyone, a stable percentage, or a measured A/B split). This package
// knows no rpc; the table below is the contract a control plane's
// ChangesService enforces.
//
//	Read one environment's Changes snapshot,         changes:read
//	  GetChangesSnapshot; nothing else
//	Draft, edit a draft, set the preview list        changes:write
//	Stop (mode off; the kill switch)                 changes:write
//	Launch a run, ramp, roll out, end a run          changes:launch
//
// changes:read is the credential of an app NOT on Reliant hosting: its
// backend fetches the snapshot it evaluates (pkg/changes OpenControlPlane)
// where a hosted workload reads a mounted file. It reads the delivered
// snapshot only, never a change's draft, history or readout.
//
// WHICH SCOPE SATISFIES WHICH ([Set.Permits]):
//
//   - changes:launch implies changes:write, and both imply changes:read, at
//     the point of use: a holder that can start delivery can always stop it,
//     and whoever may change a delivery may read what is delivered. The
//     stored grant is not widened, and Covers stays exact, so a launch holder
//     cannot MINT changes:write (nor a write holder changes:read) without
//     holding it.
//   - changes:read and changes:write imply nothing further: reading,
//     drafting and stopping never put a variant in front of users.
//   - Nothing else implies any of them. deploy:write ships a release;
//     deciding who sees what the release declares is a separate authority.
//
// None acts as a person: an org automation token (CI, an agent, a deployed
// app) may carry them. Each may be bound to one environment
// (ResourceEnvironment). A protected environment's policy may still require a
// PERSON to approve a launch or a ramp up; that is the control plane's check,
// made with the credential's identity, and the scope alone never satisfies
// it.

// ChangesScopes are the three scopes above, narrowest first.
var ChangesScopes = []Scope{
	ScopeChangesRead,
	ScopeChangesWrite,
	ScopeChangesLaunch,
}
