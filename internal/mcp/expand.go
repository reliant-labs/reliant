// Copyright (c) 2025 Reliant Labs
package mcp

import (
	"os"
	"strings"

	"github.com/reliant-labs/reliant/internal/config"
)

// ${VAR} references in an MCP server config.
//
// A config is stored, shipped and shown exactly as the user wrote it:
// `"Authorization": "Bearer ${GITHUB_TOKEN}"` stays that text in the database,
// in ListServers and in mcp.json. The references are resolved here, once, at
// launch, by the daemon — the only process that runs in the user's
// environment. The API server and the worker run in Reliant's, so a reference
// resolved there would substitute a Reliant credential into the user's config.
//
// Syntax: ${VAR}, ${VAR:-default} (default when VAR is unset or empty) and
// $VAR. A reference is looked up in the server's own env block first, so
// `args: ["--header", "key:${AUTH_TOKEN}"]` with `env: {AUTH_TOKEN: ...}`
// works, then in the daemon's environment. Env-block values themselves
// resolve against the daemon's environment only. An unknown reference with no
// default becomes the empty string.

// resolveLaunchConfig returns cfg with every reference in its command, args,
// url, headers, dir and env values resolved through lookupEnv (the daemon's
// environment). The project-path placeholders in args (projectPathPlaceholders)
// are substituted first, so they never reach the environment lookup. cfg is not
// modified.
func resolveLaunchConfig(cfg config.MCPServer, lookupEnv func(string) (string, bool)) config.MCPServer {
	out := cfg
	fromDaemon := referenceExpander(lookupEnv)

	serverEnv := make(map[string]string, len(cfg.Env))
	out.Env = nil
	for _, entry := range cfg.Env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			out.Env = append(out.Env, entry)
			continue
		}
		value = fromDaemon(value)
		serverEnv[key] = value
		out.Env = append(out.Env, key+"="+value)
	}

	expand := referenceExpander(func(name string) (string, bool) {
		if value, ok := serverEnv[name]; ok {
			return value, true
		}
		return lookupEnv(name)
	})

	out.Command = expand(cfg.Command)
	out.URL = expand(cfg.URL)
	out.Dir = expand(cfg.Dir)
	if cfg.Args != nil {
		out.Args = expandArgs(cfg.Args, normalizeProjectPath(out.Dir))
		for i, arg := range out.Args {
			out.Args[i] = expand(arg)
		}
	}
	if cfg.Headers != nil {
		out.Headers = make(map[string]string, len(cfg.Headers))
		for key, value := range cfg.Headers {
			out.Headers[key] = expand(value)
		}
	}
	return out
}

// referenceExpander returns a function resolving the ${VAR}, ${VAR:-default}
// and $VAR references in a string through lookup.
func referenceExpander(lookup func(string) (string, bool)) func(string) string {
	return func(s string) string {
		if !strings.Contains(s, "$") {
			return s
		}
		return os.Expand(s, func(ref string) string {
			name, fallback, hasDefault := strings.Cut(ref, ":-")
			value, ok := lookup(name)
			if hasDefault && (!ok || value == "") {
				return fallback
			}
			return value
		})
	}
}
