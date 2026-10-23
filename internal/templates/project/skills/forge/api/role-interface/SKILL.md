---
name: role-interface
description: Extend a service's Repository without breaking sibling fakes — the opt-in role-interface pattern for parallel-migration rounds, and when to promote it back onto Repository.
---

# Extending Repository without breaking fakes (role-interface pattern)

`Repository` is the canonical name for a service's storage interface — one per service, extended as needed. But in a **parallel-migration round**, adding a single method to `Repository` atomically breaks every fake Repository in sibling files (test fakes, in-memory `e2e/` fakes, the generated `mocks/<svc>_mock_gen.go`). The fix is the **opt-in role interface**: declare a narrow interface in the consuming file and type-assert `s.deps.Repo` to it at call time:

```go
type ModelPerformanceLister interface {
    GetModelPerformance(ctx context.Context, opts ModelPerformanceOpts) ([]*db.ModelPerformance, error)
}

func (s *Service) GetModelPerformance(ctx context.Context, req ...) (..., error) {
    lister, ok := s.deps.Repo.(ModelPerformanceLister)
    if !ok {
        return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("Repo lacks ModelPerformanceLister"))
    }
    // ... use lister ...
}
```

Production `*ormRepo` implements both, so the assertion is a runtime no-op; sibling fakes that haven't grown the method still satisfy `Repository` and return `CodeUnimplemented` — same outcome as a CRUD shape-mismatch stub.

**Use it** when adding a method to a Repository with 2+ fakes in sibling lanes, or whose impl is in-flight in a parallel agent's package; **don't** in greenfield work where you own the Repository and every fake.

Once every consumer implements it (or the round ends), promote it back onto `Repository` — consuming code unchanged, assertion becomes always-true; `forge lint --conventions` flags one ready to consolidate.
