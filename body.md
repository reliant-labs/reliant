## review:changes

Lane: **MEDIUM** (one pass on gpt-6-luna@codex).

MEDIUM: api (proto-vendor/controlplane/controlplane/v1/deploy.proto, proto-vendor/controlplane/services/claimable/v1/claimable.proto, …); code (proto-vendor/controlplane/SOURCE); deploy (proto-vendor/controlplane/services/deploy/v1/deploy.proto)

The changed API contract redefines the compatibility behavior of an existing request field. Other changed contract additions are additive; the listed managed-database, apps, and retention RPCs are vendored but not called by this PR.

- **blocker** [B1] `proto-vendor/controlplane/services/deploy/v1/deploy.proto:2415` — The updated contract changes the documented behavior of existing field `withhold_costs_without_billing_read`: previously unset (as sent by clients predating the field) preserves the old cost response, while the new contract says callers without billing:read are refused. Re-vendoring this contract accepts a breaking behavior change on an existing v1 request field; older Reliant clients can now have usage reads fail instead of receiving the prior response. Fix: Keep the existing field's behavior compatible for requests from older clients, or add a new opt-in path while retaining the old behavior; do not redefine this v1 field's meaning.

**Generated, reviewed through their inputs (not line by line):** `frontends/web/src/gen/controlplane/controlplane/v1/deploy_pb.ts`, `frontends/web/src/gen/controlplane/services/claimable/v1/claimable_pb.ts`, `frontends/web/src/gen/controlplane/services/deploy/v1/deploy_pb.ts`, `frontends/web/src/gen/controlplane/services/domain/v1/domain_pb.ts`, `frontends/web/src/gen/controlplane/services/issues/v1/issues_pb.ts`, `frontends/web/src/gen/controlplane/services/observability/v1/observability_pb.ts`, `frontends/web/src/gen/controlplane/services/organization/v1/organization_pb.ts`, `gen/controlplane/controlplane/v1/deploy.pb.go`, `gen/controlplane/services/claimable/v1/claimable.pb.go`, `gen/controlplane/services/deploy/v1/controlplanev1connect/deploy.connect.go`, `gen/controlplane/services/deploy/v1/deploy.pb.go`, `gen/controlplane/services/domain/v1/domain.pb.go`, `gen/controlplane/services/issues/v1/issues.pb.go`, `gen/controlplane/services/observability/v1/observability.pb.go`, `gen/controlplane/services/organization/v1/organization.pb.go`

Fix the blockers and push: the push is reviewed again. (pr-fix takes over if nobody does.)

Reviewed `932f5c74320e8450f0843ef177d9c30f6ad68890`.
<!-- review-changes sha=932f5c74320e8450f0843ef177d9c30f6ad68890 -->
<!-- review-round sha=932f5c74320e8450f0843ef177d9c30f6ad68890 kind=code -->
<!-- review-blockers sha=932f5c74320e8450f0843ef177d9c30f6ad68890 [{"gap":"defect","kind":"code","line":2415,"path":"proto-vendor/controlplane/services/deploy/v1/deploy.proto","problem":"The updated contract changes the documented behavior of existing field `withhold_costs_without_billing_read`: previously unset (as sent by clients predating the field) preserves the old cost response, while the new contract says callers without billing:read are refused. Re-vendoring this contract accepts a breaking behavior change on an existing v1 request field; older Reliant clients can now have usage reads fail instead of receiving the prior response."}] -->