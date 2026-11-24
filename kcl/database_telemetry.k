"""
Forge KCL — the telemetry a forge.ManagedDatabase is watched with by default.

forge.Telemetry ships no per-system preset (kcl/telemetry.k): every vendor
fact is the project's own. A ManagedDatabase is the one exception, because
there forge IS the vendor: it renders the database itself, as a CloudNativePG
Cluster (pkg/deploy RenderManagedDatabase; on forge.OnHosted the control
plane renders the same Cluster). The exporter port, the pod labels and the
log shape are facts of what forge renders, not knowledge about a system
someone else built. So a database is watched with no extra line, and the
default is nothing but an ordinary forge.Telemetry: the same targets,
collectors, system dashboard, default alerts and declared dashboards any
system gets.

    managed_database_telemetry(name)    the default Telemetry of one database:
                                        its instances' :9187 exporter (a
                                        bounded allow list), its Postgres JSON
                                        logs, a tcp check on 5432 and a
                                        database dashboard
    cnpg_operator_telemetry(cluster)    the default Telemetry of the
                                        CloudNativePG operator a cluster's
                                        databases run under: its :8080
                                        metrics, its logs and a dashboard

Per database (kcl/schema.k ManagedDatabase):

    telemetry = forge.Telemetry {...}                 replaces the default
    telemetry = forge.managed_database_telemetry("orders") | {alerts = [_lag]}
                                                      extends it
    default_telemetry = False                         turns the default off,
                                                      and the database's share
                                                      of the operator's

The operator default is one target per cluster that holds a defaulted
database (forge.OnCluster; on hosting the operator is the platform's). It is
found by the label the CloudNativePG chart and manifest put on it,
`app.kubernetes.io/name: cloudnative-pg`, in every namespace. Declaring your
own Telemetry with its service name (`cloudnative-pg`, or
`cloudnative-pg-<cluster>` when the env's databases span clusters) in
Observability.telemetry replaces it:

    observability = forge.Observability {
        telemetry = [forge.cnpg_operator_telemetry("prod-hub") | {namespaces = ["cnpg-system"]}]
    }

Nothing here admits the collector with a NetworkPolicy (network_policy is
unset, "auto", which renders none for a database): a NetworkPolicy that
selects the instances would deny every other caller of Postgres. When the
database's namespace already denies ingress, admit the collector on 9187 and
5432 yourself (forge.NetworkPolicy's namespace rules), or set
`network_policy = True` in an override.
"""

import .dashspec

# The instance exporter CloudNativePG runs in every Postgres pod, and the port
# Postgres listens on: the names a database Telemetry's ports may use.
MANAGED_DATABASE_PORTS: {str:int} = {"metrics": 9187, "postgres": 5432}

# The series a database's default scrape keeps (RE2, anchored): the
# instance's own collector (up, WAL, archive status, backups, fencing,
# switchover), the default monitoring queries' backends, database size and
# xid age, replication, the WAL archiver, per-database transactions,
# deadlocks and cache hits, and the Barman Cloud plugin's backup timestamps
# (CloudNativePG >= 1.27 reports backups there when the plugin archives,
# which is how forge renders a database with a backup store). Extend it with
# your own monitoring queries: `allow = forge.MANAGED_DATABASE_METRICS_ALLOW + "|cnpg_my_query_.*"`.
MANAGED_DATABASE_METRICS_ALLOW: str = "cnpg_collector_(up|postgres_version|last_collection_error|pg_wal|pg_wal_archive_status|wal_bytes|wal_records|wal_buffers_full|fencing_on|manual_switchover_required|replica_mode|nodes_used|sync_replicas|last_available_backup_timestamp|last_failed_backup_timestamp|first_recoverability_point)" \
    + "|cnpg_backends_(total|max_tx_duration_seconds|waiting_total)" \
    + "|cnpg_pg_database_(size_bytes|xid_age|mxid_age)" \
    + "|cnpg_pg_postmaster_start_time" \
    + "|cnpg_pg_replication_(lag|in_recovery|is_wal_receiver_up|streaming_replicas)" \
    + "|cnpg_pg_stat_archiver_(archived_count|failed_count|seconds_since_last_archival|seconds_since_last_failure)" \
    + "|cnpg_pg_stat_database_(xact_commit|xact_rollback|deadlocks|blks_hit|blks_read|temp_bytes|conflicts)" \
    + "|barman_cloud_cloudnative_pg_io_(last_available_backup_timestamp|last_failed_backup_timestamp|first_recoverability_point)"

# The operator's controller-runtime series: reconciles and their errors and
# latency, and the work queues.
CNPG_OPERATOR_METRICS_ALLOW: str = "controller_runtime_reconcile_(total|errors_total)" \
    + "|controller_runtime_reconcile_time_seconds_(bucket|sum|count)" \
    + "|controller_runtime_(active_workers|max_concurrent_reconciles)" \
    + "|workqueue_(depth|longest_running_processor_seconds|retries_total|unfinished_work_seconds)"

# The operator's service name when the env's databases are on one cluster.
CNPG_OPERATOR_SERVICE: str = "cloudnative-pg"

# A database's series stay well inside this: the per-database and
# per-backend-state families multiply by the databases and the clients'
# application names, never by rows.
_DB_SAMPLE_LIMIT = 2000

# Postgres's error_severity, as CloudNativePG writes it into record.
_PG_SEVERITY: {str:[str]} = {
    debug = ["DEBUG1", "DEBUG2", "DEBUG3", "DEBUG4", "DEBUG5"]
    info = ["LOG", "INFO", "NOTICE"]
    warn = ["WARNING"]
    error = ["ERROR"]
    fatal = ["FATAL", "PANIC"]
}

# The level CloudNativePG's own processes (operator, instance manager) write.
_CNPG_LEVEL: {str:[str]} = {
    debug = ["debug", "trace"]
    info = ["info"]
    warn = ["warning"]
    error = ["error"]
}

# The service.name a database's signals carry by default.
managed_database_service_name = lambda name: str -> str {
    "${name}-postgres"
}

_db_env_filter = lambda -> [dashspec.Filter] {
    [dashspec.Filter {id = "env", label = "deployment.environment.name", field = "deployment.environment.name"}]
}

# A metrics panel over one service.
_db_panel = lambda svc: str, id: str, title: str, description: str, series: [dashspec.Series], unit: str, group_by: [str] = [], width: int = 12 -> dashspec.Panel {
    dashspec.Panel {
        id = id
        title = title
        description = description
        kind = "line"
        signal = "metrics"
        where = [dashspec.Predicate {field = "service.name", op = "eq", value = svc}]
        series = series
        groupBy = group_by if group_by else Undefined
        unit = unit if unit else Undefined
        width = width
        height = 7
    }
}

_by_instance = ["resource.attr.service.instance.id"]
_by_database = ["metric.attr.datname"]

_managed_database_dashboard = lambda svc: str, name: str -> dashspec.Dashboard {
    dashspec.Dashboard {
        id = svc
        title = "Postgres: ${name}"
        description = "The CloudNativePG instances of the managed database ${name}: availability, connections, size, throughput, replication, WAL archiving and backups. Its logs, scrape health and resources are on its system dashboard."
        group = "platform"
        tags = ["postgres", "database"]
        filters = _db_env_filter()
        sections = [
            dashspec.Section {id = "health", title = "Health"}
            dashspec.Section {id = "load", title = "Load"}
            dashspec.Section {id = "durability", title = "Replication, WAL and backups"}
        ]
        panels = [
            _db_panel(svc, "up", "Postgres up", "1 while the instance accepts queries, 0 when it does not. One line per instance.", \
                [dashspec.gauge("up", "cnpg_collector_up", "min")], "", _by_instance) | {section = "health"}
            _db_panel(svc, "connections", "Connections by state", "Backends by state (active, idle, idle in transaction). Idle in transaction holds locks and blocks vacuum.", \
                [dashspec.gauge("backends", "cnpg_backends_total", "sum")], "count", ["metric.attr.state"]) | {section = "health"}
            _db_panel(svc, "waiting", "Backends waiting on locks", "Queries blocked behind another backend's lock.", \
                [dashspec.gauge("waiting", "cnpg_backends_waiting_total", "max")], "count") | {section = "health"}
            _db_panel(svc, "longest-tx", "Longest open transaction", "The oldest transaction still open. Long ones hold back vacuum and bloat the tables.", \
                [dashspec.gauge("seconds", "cnpg_backends_max_tx_duration_seconds", "max")], "s") | {section = "health"}
            _db_panel(svc, "size", "Database size", "Disk used by each database.", \
                [dashspec.gauge("size", "cnpg_pg_database_size_bytes", "max")], "bytes", _by_database) | {section = "load"}
            _db_panel(svc, "transactions", "Transactions / s", "Commits and rollbacks per second, all databases.", \
                [dashspec.rate("commits", "cnpg_pg_stat_database_xact_commit"), dashspec.rate("rollbacks", "cnpg_pg_stat_database_xact_rollback")], "count") | {section = "load"}
            _db_panel(svc, "deadlocks", "Deadlocks / s", "Deadlocks Postgres detected and broke by cancelling a transaction.", \
                [dashspec.rate("deadlocks", "cnpg_pg_stat_database_deadlocks")], "count", _by_database) | {section = "load"}
            _db_panel(svc, "xid-age", "Transaction ID age", "Transactions since each database's oldest unfrozen XID. Postgres stops accepting writes near 2 billion; autovacuum should keep it far below.", \
                [dashspec.gauge("xid age", "cnpg_pg_database_xid_age", "max")], "count", _by_database) | {section = "load"}
            _db_panel(svc, "replication-lag", "Replication lag", "How far each replica's replay is behind the primary. 0 on the primary.", \
                [dashspec.gauge("lag", "cnpg_pg_replication_lag", "max")], "s", _by_instance) | {section = "durability"}
            _db_panel(svc, "replicas", "Streaming replicas", "Replicas streaming from the primary.", \
                [dashspec.gauge("replicas", "cnpg_pg_replication_streaming_replicas", "max")], "count") | {section = "durability"}
            _db_panel(svc, "wal-size", "WAL on disk", "The size of pg_wal. It grows when archiving or a replica falls behind.", \
                [dashspec.gauge("wal", "cnpg_collector_pg_wal", "max", [dashspec.Predicate {field = "metric.attr.value", op = "eq", value = "size"}])], "bytes", _by_instance) | {section = "durability"}
            _db_panel(svc, "wal-ready", "WAL waiting to be archived", "Segments marked ready but not yet archived. A growing count means archiving is failing or slow.", \
                [dashspec.gauge("ready", "cnpg_collector_pg_wal_archive_status", "max", [dashspec.Predicate {field = "metric.attr.value", op = "eq", value = "ready"}])], "count", _by_instance) | {section = "durability"}
            _db_panel(svc, "archive-failures", "WAL archive failures / s", "Failed attempts to archive a WAL segment to the backup store.", \
                [dashspec.rate("failed", "cnpg_pg_stat_archiver_failed_count")], "count") | {section = "durability"}
            _db_panel(svc, "archive-age", "Time since last archived WAL", "Seconds since a WAL segment was last archived. -1 before the first.", \
                [dashspec.gauge("seconds", "cnpg_pg_stat_archiver_seconds_since_last_archival", "max")], "s") | {section = "durability"}
            _db_panel(svc, "last-backup", "Last backup", "Unix time of the newest base backup in the store, as the Barman Cloud plugin reports it (the instance's own series on older operators). It steps up at every scheduled backup; a flat line is a backup that stopped.", \
                [dashspec.gauge("plugin", "barman_cloud_cloudnative_pg_io_last_available_backup_timestamp", "max"), dashspec.gauge("instance", "cnpg_collector_last_available_backup_timestamp", "max")], "s", [], 24) | {section = "durability"}
        ]
    }
}

managed_database_telemetry = lambda name: str, metrics: bool = True, logs: bool = True -> Telemetry {
    """The Telemetry a ManagedDatabase named `name` gets when it declares
    none: its CloudNativePG instances' exporter (`metrics`, 9187) with a
    bounded allow list, their Postgres logs (record.error_severity is the
    severity, record.message the body), a tcp check on Postgres (5432), and
    a database dashboard. Extend it with `|`; replace it with your own.
    metrics = False leaves out the scrape, the check and the dashboard, and
    logs = False the log parse: forge passes the env's observability
    signals, so a default never declares a signal the env does not ship."""
    _svc = managed_database_service_name(name)
    Telemetry {
        service_name = _svc
        # The instance pod's Postgres container (its init container is the
        # bootstrap copy of the manager binary).
        container = "postgres"
        metrics = [MetricsScrape {port = "metrics", allow = MANAGED_DATABASE_METRICS_ALLOW, sample_limit = _DB_SAMPLE_LIMIT}] if metrics else []
        logs = LogParse {
            format = "json"
            timestamp = LogTimestamp {field = "ts", layout = "rfc3339"}
            # A Postgres line (logger "postgres") carries its real severity
            # here; CloudNativePG's own `level` is always info for it.
            severity = LogSeverity {field = "record.error_severity", mapping = _PG_SEVERITY}
            body = "record.message"
            # The instance manager's own lines have no record: their `level`
            # is the severity. Never info, which every Postgres line also says.
            rules = [
                LogSeverityRule {field = "level", value_match = "^error$", severity = "error"}
                LogSeverityRule {field = "level", value_match = "^warning$", severity = "warn"}
                LogSeverityRule {field = "level", value_match = "^(debug|trace)$", severity = "debug"}
            ]
        } if logs else Undefined
        checks = [HealthCheck {port = "postgres"}] if metrics else []
        dashboards = [_managed_database_dashboard(_svc, name)] if metrics else []
    }
}

_cnpg_operator_dashboard = lambda svc: str -> dashspec.Dashboard {
    dashspec.Dashboard {
        id = svc
        title = "CloudNativePG operator"
        description = "The operator that reconciles every managed database's Cluster: reconcile rate, errors, latency and queue depth. Its logs, scrape health and resources are on its system dashboard."
        group = "platform"
        tags = ["postgres", "operator"]
        filters = _db_env_filter()
        panels = [
            _db_panel(svc, "reconciles", "Reconciles / s", "Reconciles per controller.", \
                [dashspec.rate("reconciles", "controller_runtime_reconcile_total")], "count", ["metric.attr.controller"])
            _db_panel(svc, "errors", "Reconcile errors / s", "Reconciles that returned an error, per controller. A steady rate is a Cluster the operator cannot converge.", \
                [dashspec.rate("errors", "controller_runtime_reconcile_errors_total")], "count", ["metric.attr.controller"])
            _db_panel(svc, "latency", "Reconcile p95", "How long a reconcile takes, per controller.", \
                [dashspec.quantile("p95", "controller_runtime_reconcile_time_seconds", 0.95)], "s", ["metric.attr.controller"])
            _db_panel(svc, "queue", "Work queue depth", "Objects waiting to be reconciled.", \
                [dashspec.gauge("depth", "workqueue_depth", "max")], "count", ["metric.attr.name"])
        ]
    }
}

cnpg_operator_telemetry = lambda cluster: str, service_name: str = CNPG_OPERATOR_SERVICE, metrics: bool = True, logs: bool = True -> Telemetry {
    """The Telemetry of the CloudNativePG operator on `cluster`: its pods
    (`app.kubernetes.io/name: cloudnative-pg`, in every namespace), their
    :8080 controller metrics with a bounded allow list, their JSON logs and
    a dashboard. forge emits it by default for each cluster that holds a
    defaulted ManagedDatabase; it is also a valid Observability.telemetry
    entry, to declare (or narrow) it yourself."""
    Telemetry {
        service_name = service_name
        cluster = cluster
        selector = {"app.kubernetes.io/name" = "cloudnative-pg"}
        metrics = [MetricsScrape {port = 8080, allow = CNPG_OPERATOR_METRICS_ALLOW, sample_limit = _DB_SAMPLE_LIMIT}] if metrics else []
        logs = LogParse {
            format = "json"
            timestamp = LogTimestamp {field = "ts", layout = "rfc3339"}
            severity = LogSeverity {field = "level", mapping = _CNPG_LEVEL}
            body = "msg"
        } if logs else Undefined
        dashboards = [_cnpg_operator_dashboard(service_name)] if metrics else []
    }
}
