"""
Positive test — a forge.ManagedDatabase is watched by default
(kcl/database_telemetry.k), through the ordinary Telemetry contract.

Pins:
  1. A cluster database with no telemetry renders one `manageddatabase`
     target: its CloudNativePG instances (`cnpg.io/cluster`) in its
     namespace on its cluster, the bounded :9187 scrape, the Postgres log
     parse, a tcp check on 5432 and a dashboard.
  2. The cluster's CloudNativePG operator gets one default target.
  3. A hosted database's target carries no cluster or namespace (the
     platform places them) and the same signals.
  4. `telemetry` replaces the default, and may extend it with `|`.
  5. `default_telemetry = False` renders nothing for the database, and no
     operator target when no database on the cluster keeps the default.
  6. A Telemetry named like the operator's default replaces it.
  7. The default leaves out a signal the env's observability does not ship
     (signals.metrics / signals.logs = False).
"""

import forge

_ct = forge.ClusterTarget {cluster = "k3d-shop", namespace = "shop-prod"}

_t = lambda dbs: [forge.ManagedDatabase], obs: any -> {str:} {
    _out = forge.render(forge.Bundle {
        project = "shop"
        env = "prod"
        cluster_target = _ct
        control_plane = forge.ControlPlane {}
        databases = dbs
        observability = obs
    })
    {t.service_name: t for t in _out.telemetry} if _out.telemetry else {}
}

# 1 + 2: the default on a cluster.
_c = _t([forge.ManagedDatabase {name = "orders"}], Undefined)
_db = _c["orders-postgres"]
assert_db_placement = _db.attachment == "manageddatabase" and _db.source == "manageddatabase:orders" \
    and _db.runtime == "cluster" and _db.cluster == "k3d-shop" and _db.namespaces == ["shop-prod"] \
    and _db.selector == {"cnpg.io/cluster": "orders"} and _db.container == "postgres"
assert_db_ports = _db.ports == {"metrics": 9187, "postgres": 5432}
assert_db_scrape = _db.metrics[0].port == 0 and _db.metrics[0].port_name == "metrics" \
    and _db.metrics[0].allow == forge.MANAGED_DATABASE_METRICS_ALLOW and _db.metrics[0].sample_limit == 2000
assert_db_logs = _db.logs.severity.field == "record.error_severity" and _db.logs.body == "record.message" \
    and _db.logs.timestamp.field == "ts" and _db.logs.severity.mapping.error == ["ERROR"]
assert_db_check = _db.checks[0].port_name == "postgres" and _db.checks[0].kind == "tcp"
assert_db_dashboard = _db.dashboards[0].id == "orders-postgres" and len(_db.dashboards[0].panels) > 0
assert_db_no_policy = "network_policy" not in _db
assert_db_default_alerts = _db.default_alerts
_op = _c["cloudnative-pg"]
assert_operator = _op.attachment == "manageddatabase" and _op.source == "manageddatabase-operator:k3d-shop" \
    and _op.cluster == "k3d-shop" and _op.namespaces == [] \
    and _op.selector == {"app.kubernetes.io/name": "cloudnative-pg"} and _op.metrics[0].port == 8080 \
    and _op.metrics[0].allow == forge.CNPG_OPERATOR_METRICS_ALLOW
assert_two_targets = len(_c) == 2

# 3: hosted.
_h = _t([forge.ManagedDatabase {name = "orders", runtime = forge.OnHosted {}}], Undefined)
_hdb = _h["orders-postgres"]
assert_hosted = len(_h) == 1 and _hdb.runtime == "hosted" and _hdb.cluster == "" \
    and _hdb.namespaces == [] and _hdb.selector == {"cnpg.io/cluster": "orders"} \
    and _hdb.metrics[0].allow and _hdb.metrics[0].sample_limit <= 5000

# 4: replace, and extend.
_mine = forge.Telemetry {checks = [forge.HealthCheck {port = "postgres"}]}
_r = _t([forge.ManagedDatabase {name = "orders", telemetry = _mine}], Undefined)
assert_replaced = _r["orders-postgres"].metrics == [] and _r["orders-postgres"].dashboards == [] \
    and len(_r["orders-postgres"].checks) == 1 and "cloudnative-pg" in _r
_x = _t([forge.ManagedDatabase {
    name = "orders"
    telemetry = forge.managed_database_telemetry("orders") | {service_name = "orders-db", default_alerts = False}
}], Undefined)
assert_extended = "orders-db" in _x and len(_x["orders-db"].metrics) == 1 and not _x["orders-db"].default_alerts \
    and _x["orders-db"].source == "manageddatabase:orders"

# 5: off.
_o = _t([forge.ManagedDatabase {name = "orders", default_telemetry = False}], Undefined)
assert_off = _o == {}
_mixed = _t([
    forge.ManagedDatabase {name = "orders", default_telemetry = False}
    forge.ManagedDatabase {name = "ledger"}
], Undefined)
assert_operator_kept_by_one = sorted([k for k in _mixed]) == ["cloudnative-pg", "ledger-postgres"]

# 6: an operator Telemetry of the same service name replaces the default.
_own = _t([forge.ManagedDatabase {name = "orders"}], forge.Observability {
    local_port = 4318
    telemetry = [forge.cnpg_operator_telemetry("k3d-shop") | {namespaces = ["cnpg-system"]}]
})
assert_operator_replaced = _own["cloudnative-pg"].source == "observability" and _own["cloudnative-pg"].namespaces == ["cnpg-system"] \
    and len(_own) == 2

# Databases on two clusters: one operator target each, named by cluster.
_two = _t([
    forge.ManagedDatabase {name = "orders"}
    forge.ManagedDatabase {name = "ledger", runtime = forge.OnCluster {target = forge.ClusterTarget {cluster = "gke_acme_us-central1_Daemon", namespace = "ledger"}}}
], Undefined)
assert_operator_per_cluster = "cloudnative-pg-k3d-shop" in _two and "cloudnative-pg-gke_acme_us-central1_daemon" in _two \
    and _two["ledger-postgres"].namespaces == ["ledger"]

# 7: the default follows the env's signals. The node agent refuses a declared
# signal the env does not ship, so an env with metrics or logs off keeps
# rendering once the default exists.
_nometrics = _t([forge.ManagedDatabase {name = "orders"}], forge.Observability {
    local_port = 4318
    signals = forge.ObservabilitySignals {metrics = False}
})
assert_no_metrics_default = _nometrics["orders-postgres"].metrics == [] and _nometrics["orders-postgres"].checks == [] \
    and _nometrics["orders-postgres"].dashboards == [] and _nometrics["orders-postgres"].logs.body == "record.message" \
    and _nometrics["cloudnative-pg"].metrics == [] and _nometrics["cloudnative-pg"].dashboards == []
_nologs = _t([forge.ManagedDatabase {name = "orders"}], forge.Observability {
    local_port = 4318
    signals = forge.ObservabilitySignals {logs = False}
})
assert_no_logs_default = not _nologs["orders-postgres"].logs and len(_nologs["orders-postgres"].metrics) == 1 \
    and not _nologs["cloudnative-pg"].logs
