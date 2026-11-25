package templates

// TaskfileTrimpathGOFLAGS is the GOFLAGS entry of a scaffolded Taskfile's
// top-level `env:` map: the comment that says why, then a dynamic variable
// that merges -trimpath into whatever GOFLAGS the machine already uses. It is
// written at two spaces of indentation (the entry) and four (its `sh:`), with
// no trailing newline.
//
// It is the one source of the block. Every Taskfile template renders it
// (`{{taskfileTrimpathGOFLAGS}}`), and `forge fix trimpath-default` inserts
// it into the Taskfile of a project scaffolded before the block existed, so
// the wording a new project gets and the wording an upgraded one gets cannot
// drift apart.
const TaskfileTrimpathGOFLAGS = `  # GOFLAGS gets -trimpath, merged into whatever GOFLAGS you already use (from
  # the environment, ` + "`go env -w`" + ` or the toolchain's go.env). Without it Go
  # hashes each package's ABSOLUTE directory into its build-cache key, so every
  # worktree and scratch copy of this module (forge ci verify-generated
  # regenerates in a fresh one every run) compiles the whole module again and
  # re-runs every test instead of reusing what a sibling already built.
  # Measured: a second worktree of one commit compiled 267 packages and cached
  # 0 of 73 test packages without it; 0 and 58 with it. A GOFLAGS already in
  # your environment wins over this block (Task never overrides one), and
  # -trimpath=false in it, or in ` + "`go env -w GOFLAGS`" + `, opts out.
  GOFLAGS:
    sh: ` + TaskfileTrimpathGOFLAGSScript

// TaskfileTrimpathGOFLAGSScript is the `sh:` value of TaskfileTrimpathGOFLAGS,
// as YAML source (single-quoted). Unquoted, it prints the machine's GOFLAGS
// with -trimpath appended unless it already carries -trimpath (or
// -trimpath=false, which opts out).
const TaskfileTrimpathGOFLAGSScript = `'f="$(go env GOFLAGS 2>/dev/null)"; case " $f " in *" -trimpath"*|*" --trimpath"*) echo "$f" ;; *) echo "${f:+$f }-trimpath" ;; esac'`
