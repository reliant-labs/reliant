package kclplugin

import (
	"fmt"
)

// boolCallArg reads an optional boolean argument by keyword or position.
// Absent is false. A non-bool is an error rather than a guess: `shared="no"`
// must not quietly mean true.
func boolCallArg(args *methodArgs, index int, key string) (bool, error) {
	v := args.callArg(index, key)
	if v == nil {
		return false, nil
	}
	b, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("%s must be a bool, got %T (%v)", key, v, v)
	}
	return b, nil
}

// Register installs the kcl_plugin.forge namespace and wires forge's
// purego plugin bridge into the native KCL client. Idempotent, cheap after
// the first call, and safe to call before every render.
//
// ORDERING IS LOAD-BEARING: the native client is a process-wide singleton
// whose plugin agent is fixed by whoever initializes it first, and kcl.Run /
// kcl.ListOptions / kpm all initialize it implicitly with no agent (a
// plugin-less runtime where `import kcl_plugin.forge` fails). So Register
// must run before ANY of those. Rather than an init() — which would load
// libkcl in every forge command, including ones that never render — every
// forge entry into KCL calls Register itself: Serialized (the choke point
// for every native evaluation) and the kpm-client call sites in kclrender
// and kcloptions, which can initialize the client outside Serialized.
func Register() {
	install()
}

func init() {
	methods = map[string]methodSpec{
		// resolve_port(name, preferred) -> int. One stable host port
		// per name, preferring `preferred` when free.
		"resolve_port": {
			Body: func(args *methodArgs) (any, error) {
				name := args.strArg(0)
				preferred := int(args.intArg(1))
				p, err := currentResolver().Resolve(name, preferred)
				if err != nil {
					return nil, err
				}
				return p, nil
			},
		},
		// allocate_port(base, key) -> int. Deterministic, memoized
		// keyed port for parallel dev stacks: base + block(key)*100,
		// where forge assigns + persists a stable block per key (the
		// index is internal, never surfaced here). key "" -> base.
		"allocate_port": {
			Body: func(args *methodArgs) (any, error) {
				base := int(args.intArg(0))
				key := args.strArg(1)
				p, err := allocatePort(base, key)
				if err != nil {
					return nil, err
				}
				return p, nil
			},
		},
		// REMOVED: host_path(rel) -> str, an absolute path under the
		// developer's HOME.
		//
		// It existed to serve exactly one case — telling a HOST
		// process where a cluster's kubeconfig lives — and it served
		// it by GUESSING: `host_path(".kube/config")` is wrong on any
		// machine that sets $KUBECONFIG, and a guess that is usually
		// right fails on one teammate's machine, reported by client-go
		// as "context does not exist" rather than as the wrong file.
		//
		// The path is now DERIVED from the cluster that owns it —
		// `<cluster>.kubeconfig`, backed by the reserved `-D
		// kubeconfig=` binding forge resolves per-render (see
		// internal/kubeconfig and kclrender.withKubeconfigDArg). It is
		// not replaced by a general home-anchoring escape hatch,
		// because a general version of "resolve a path against this
		// developer's home" invites exactly the class of declaration
		// that motivated removing it: a machine-shaped literal, in
		// version control, that renders differently per machine with
		// nothing saying so. A future host fact should likewise be
		// derived from the forge concept that owns it.
		//
		// dev_stacks() -> [str]. The registered DEV-STACK keys (git
		// worktrees), for a module that generates one config block
		// per running stack. Excludes plain port-block keys, which
		// are not stacks, and the implicit default stack "" (a
		// generator always emits that one itself). Empty on a
		// read-only render (forge generate / forge ci), keeping
		// generate a pure function of committed inputs.
		"dev_stacks": {
			Body: func(args *methodArgs) (any, error) {
				keys, err := devStacks()
				if err != nil {
					return nil, err
				}
				return keys, nil
			},
		},
		// write_file(path, content) -> str. Materializes a
		// project-relative file — but ONLY on a render whose job is
		// to materialize this env (env up; env deploy of a local
		// env). Every other render (generate, lint, ci, doctor, env
		// config / render) writes nothing and returns the path
		// unchanged. Use this, not KCL's file.write, for any file a
		// render generates: file.write fires on every evaluation,
		// read-only ones included. See materialize.go.
		//
		// shared=True (keyword, or a third positional) writes into
		// the repo's PRIMARY checkout instead of this one — for a
		// file a machine-shared resource mounts, so every worktree's
		// render updates the one copy that resource reads.
		"write_file": {
			Body: func(args *methodArgs) (any, error) {
				shared, err := boolCallArg(args, 2, "shared")
				if err != nil {
					return nil, fmt.Errorf("write_file: %w", err)
				}
				p, err := writeFile(args.strArg(0), args.strArg(1), shared)
				if err != nil {
					return nil, err
				}
				return p, nil
			},
		},
		// lower_container(fw.Container) -> k8s container dict. Lowers a
		// forge container for a hand-written pod (a raw Deployment) with
		// the SAME Go lowering forge's own sidecars get, so probe timings
		// and native-sidecar restartPolicy are defined once. See
		// deploy.LowerContainer for what it leaves to the pod.
		"lower_container": {
			Body: func(args *methodArgs) (any, error) {
				return lowerContainer(args.args[0])
			},
		},
		// probe(token) -> token. Side-effect-free echo, used ONLY by
		// Probe (probe.go) to prove a KCL program can call back into
		// this process. Not part of the namespace a project should use.
		"probe": {
			Body: func(args *methodArgs) (any, error) {
				return args.strArg(0), nil
			},
		},
		// derive_jwk(private_key_pem, kid, alg) -> JWK dict. Derives
		// the PUBLIC JWK from an ES256 (EC) or RS256 (RSA) private-key
		// PEM at render time so a forge.TestJWKS publishes the public
		// half of the EXACT key its signer uses — signer + JWKS can't
		// drift.
		"derive_jwk": {
			Body: func(args *methodArgs) (any, error) {
				pemStr := args.strArg(0)
				kid := args.strArg(1)
				alg := args.strArg(2)
				jwk, err := DeriveJWK(pemStr, kid, alg)
				if err != nil {
					return nil, err
				}
				return jwk, nil
			},
		},
	}
}
