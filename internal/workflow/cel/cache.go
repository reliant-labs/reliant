// Copyright (c) 2025 Reliant Labs
package wfcel

import (
	"container/list"
	"strings"
	"sync"

	"github.com/google/cel-go/cel"
)

// Building a cel.Env is the expensive part of evaluating a workflow
// expression: it re-registers the stdlib, every extension, the native types
// and the custom functions. Before these caches every evaluation built one —
// in the 2026-10-23 replay storm cel.NewEnv was 28% of worker CPU, and most of
// the ~35% spent in GC was its garbage.
//
// Both caches are safe to share. A cel.Env is immutable once built (Extend
// returns a new one), and a cel.Program carries no evaluation state: each Eval
// makes its own activation and cost tracker. Both are deterministic functions
// of their key, so a cache hit is indistinguishable from a rebuild — which is
// what keeps this safe inside workflow code.
const (
	// envCacheSize bounds the environments kept. One exists per distinct
	// CELEnvConfig, a handful in practice.
	envCacheSize = 64
	// programCacheSize bounds the compiled expressions kept. Expressions come
	// from workflow definitions, which users write, so this one must be
	// bounded; a few thousand covers every built-in and a long tail of drafts.
	programCacheSize = 4096
)

var (
	envCache     = newLRU[*cel.Env](envCacheSize)
	programCache = newLRU[*CompiledExpr](programCacheSize)
)

// CompiledExpr is one expression checked and planned against one environment.
type CompiledExpr struct {
	Env     *cel.Env
	AST     *cel.Ast
	Program cel.Program
}

// cacheKey identifies the environment config builds. NewEnv appends one
// declaration per namespace in order, so the order is part of the key.
func (c CELEnvConfig) cacheKey() string {
	var b strings.Builder
	if c.IncludeStdLib {
		b.WriteString("std|")
	}
	if c.IncludeCustomFunctions {
		b.WriteString("fn|")
	}
	for _, ns := range c.Namespaces {
		b.WriteString(string(ns))
		b.WriteByte(',')
	}
	return b.String()
}

// Compile returns expr checked against config's environment and planned with
// ProgramOptions, from the cache when it has been compiled before. A compile
// failure is returned as issues (and not cached: an expression that does not
// compile fails its step, so it is never on a hot path); a planning failure as
// err.
func Compile(config CELEnvConfig, expr string) (*CompiledExpr, *cel.Issues, error) {
	envKey := config.cacheKey()
	key := envKey + "\x00" + expr
	if compiled, ok := programCache.get(key); ok {
		return compiled, nil, nil
	}
	env, err := NewEnv(config)
	if err != nil {
		return nil, nil, err
	}
	ast, issues := env.Compile(expr)
	if issues != nil && issues.Err() != nil {
		return nil, issues, nil
	}
	prg, err := env.Program(ast, ProgramOptions()...)
	if err != nil {
		return nil, nil, err
	}
	compiled := &CompiledExpr{Env: env, AST: ast, Program: prg}
	programCache.add(key, compiled)
	return compiled, nil, nil
}

// lru is a mutex-guarded least-recently-used map of bounded size.
type lru[V any] struct {
	mu    sync.Mutex
	limit int
	order *list.List // front = most recently used; values are *lruEntry[V]
	items map[string]*list.Element
}

type lruEntry[V any] struct {
	key   string
	value V
}

func newLRU[V any](limit int) *lru[V] {
	return &lru[V]{limit: limit, order: list.New(), items: make(map[string]*list.Element, limit)}
}

func (c *lru[V]) get(key string) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		c.order.MoveToFront(el)
		return el.Value.(*lruEntry[V]).value, true
	}
	var zero V
	return zero, false
}

// add stores value under key, evicting the least recently used entry past the
// limit. Two callers that both missed may both add; the later one wins, and
// since a value is a pure function of its key either is correct.
func (c *lru[V]) add(key string, value V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		el.Value.(*lruEntry[V]).value = value
		c.order.MoveToFront(el)
		return
	}
	c.items[key] = c.order.PushFront(&lruEntry[V]{key: key, value: value})
	for c.order.Len() > c.limit {
		oldest := c.order.Back()
		c.order.Remove(oldest)
		delete(c.items, oldest.Value.(*lruEntry[V]).key)
	}
}

func (c *lru[V]) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}
