`legacy/` is a project forge scaffolded before the composition graph (forge
357579470b0d, `project new app --service tasks`, a `Task` entity and a `ticker`
worker), trimmed to the files `forge fix composition-graph` reads. Its go.mod is
`go.mod.txt` so the forge module never sees a nested module; the tests copy the
tree into a temp dir and restore the name.
