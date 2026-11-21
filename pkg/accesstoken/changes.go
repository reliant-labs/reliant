package accesstoken

// ── Changes ───────────────────────────────────────────────────────────
//
// changes:write and changes:launch control Changes: one record per change per
// environment that says who gets a declared behavior (everyone, a stable
// percentage, or a measured A/B split). This package knows no rpc; the table
// below is the contract a control plane's ChangesService enforces.
//
//	Draft, edit a draft, set the preview list        changes:write
//	Stop (mode off; the kill switch)                 changes:write
//	Launch a run, ramp, roll out, end a run          changes:launch
//
// WHICH SCOPE SATISFIES WHICH ([Set.Permits]):
//
//   - changes:launch implies changes:write at the point of use: a holder that
//     can start delivery can always stop it. The stored grant is not widened,
//     and Covers stays exact, so a launch holder cannot MINT changes:write
//     without holding it.
//   - changes:write implies nothing: drafting and stopping never put a
//     variant in front of users.
//   - Nothing else implies either. deploy:write ships a release; deciding who
//     sees what the release declares is a separate authority.
//
// Neither acts as a person: an org automation token (CI, an agent) may carry
// them. Both may be bound to one environment (ResourceEnvironment). A
// protected environment's policy may still require a PERSON to approve a
// launch or a ramp up; that is the control plane's check, made with the
// credential's identity, and the scope alone never satisfies it.

// ChangesScopes are the two scopes above, narrowest first.
var ChangesScopes = []Scope{
	ScopeChangesWrite,
	ScopeChangesLaunch,
}
