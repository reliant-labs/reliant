# HyperDX dashboards

Every `*.json` file in this directory is loaded into the local HyperDX as a
provisioned dashboard, within about 15 seconds of the file appearing or
changing, with no restart. The files are HyperDX v2 dashboard JSON
(`name`, `tiles`, `tags`, `filters`), pinned to ClickStack 2.40.0.

A provisioned dashboard is read-only in the UI and is replaced by name on the
next load, so edit the file here, not the copy in the browser. A dashboard you
build by hand in the UI is separate and is never overwritten.

## Generated dashboards

`forge generate` writes `<project>-<id>_gen.json` here from your services:
an overview, one dashboard per service (RED per rpc, database, Go runtime,
logs) and, when you have a frontend, a browser dashboard. Do not edit a
`_gen.json`: forge rewrites it, and a hand-edit is refused as drift.

To change one, copy it to the same name without `_gen`
(`shop-service-orders_gen.json` -> `shop-service-orders.json`), give it a NEW
`name`, and edit that. Its presence tells forge to stop writing the generated
twin. Any other `*.json` you add is yours too. forge never writes or deletes a
file here that does not end in `_gen.json`.

## Shipping with a release

`forge env build <env> --release <v>` also carries the renderer-neutral form of
every dashboard in the release bundle (`observability/dashboards/`), for the
Reliant console. A file here with a `panels` array (rather than HyperDX's
`tiles`) is shipped as it is, and one named like a generated dashboard replaces
it. `forge obs dashboards check` validates them and lists what would ship.

Tiles name their source (`Logs`, `Traces`, `Metrics`); the local stack binds
the names to this database's source ids for you.

Each `*_gen.ndjson` is one service's generated saved searches. The
renderer-neutral model they and the dashboards come from is `../spec/`: one
file for the project and one per service, so two branches that each add a
service never change the same file. Alerts are not provisioned here (HyperDX
2.40 never evaluates a file-declared alert); they are `forge.AlertRule`s in
your env's KCL, with generated defaults in `../alert-defaults_gen.json`.
