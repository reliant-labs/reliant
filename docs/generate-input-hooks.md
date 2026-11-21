# Descriptor inputs for generation

Generate hooks default to `phase: post`: they run against Forge's completed output before validation. Existing hooks retain that ordering.

Use `phase: input` when a hook produces vendored TypeScript descriptors consumed by frontend generation:

```yaml
generate:
  hooks:
    - name: vendor-client
      phase: input
      command: [buf, generate, --template, buf.gen.vendor.yaml]
      writes: [frontends/web/src/gen/vendor]
```

Input hooks run after the core proto/stub steps and before frontend hooks and mock projections. Hooks run once, in declaration order **within each phase**, not in global list order. Input hooks must not depend on a completed generated application; keep commands such as application `go generate` in the default post phase.

The normal rollback journal captures declared writes for both phases. Failures stop generation. Stale generated mocks are then removed by the existing certified-file sweep in the same run: edited and disowned files retain their ownership protections.

Without the input phase, a post hook that excludes a vendored service refreshes its descriptor after mock generation, leaving mocks based on the previous descriptor until another run. The phase expresses that dependency without moving existing post hooks earlier.
