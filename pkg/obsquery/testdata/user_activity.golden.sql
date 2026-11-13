WITH
act AS (
  SELECT EnvId, UserId, Timestamp, Client, RetentionDays
  FROM t_org.product_events
  WHERE UserId != '' AND EventName NOT IN ('auth_signed_out', 'auth_accounts') AND Props['forge.kind'] != 'state' AND Timestamp >= fromUnixTimestamp64Nano({p6:Int64}) AND Timestamp < fromUnixTimestamp64Nano({p3:Int64}) AND EnvId = {p1:String} AND ServiceName IN ({p2:String})),
nu_cur AS (
SELECT EnvId, UserId, if(reports = 1, signed_up_at, first_at) AS NewAt, reports AS ReportsSignups
FROM (
  SELECT EnvId, UserId, first_at, signed_up_at, has_signup, max(has_signup) OVER (PARTITION BY EnvId) AS reports,
         min(first_at) OVER (PARTITION BY EnvId) AS data_from
  FROM (
    SELECT EnvId, UserId, min(Timestamp) AS first_at, minIf(Timestamp, EventName = 'auth_signed_up') AS signed_up_at,
           toUInt8(countIf(EventName = 'auth_signed_up') > 0) AS has_signup
    FROM t_org.product_events
    WHERE UserId != '' AND EventName NOT IN ('auth_signed_out', 'auth_accounts') AND Props['forge.kind'] != 'state' AND Timestamp >= fromUnixTimestamp64Nano({p3:Int64}) - INTERVAL 90 DAY AND Timestamp < fromUnixTimestamp64Nano({p3:Int64}) AND EnvId = {p1:String} AND ServiceName IN ({p2:String})
    GROUP BY EnvId, UserId))
WHERE has_signup = 1 OR (reports = 0 AND first_at >= data_from + INTERVAL 7 DAY)),
nu_prev AS (
SELECT EnvId, UserId, if(reports = 1, signed_up_at, first_at) AS NewAt, reports AS ReportsSignups
FROM (
  SELECT EnvId, UserId, first_at, signed_up_at, has_signup, max(has_signup) OVER (PARTITION BY EnvId) AS reports,
         min(first_at) OVER (PARTITION BY EnvId) AS data_from
  FROM (
    SELECT EnvId, UserId, min(Timestamp) AS first_at, minIf(Timestamp, EventName = 'auth_signed_up') AS signed_up_at,
           toUInt8(countIf(EventName = 'auth_signed_up') > 0) AS has_signup
    FROM t_org.product_events
    WHERE UserId != '' AND EventName NOT IN ('auth_signed_out', 'auth_accounts') AND Props['forge.kind'] != 'state' AND Timestamp >= fromUnixTimestamp64Nano({p4:Int64}) - INTERVAL 90 DAY AND Timestamp < fromUnixTimestamp64Nano({p4:Int64}) AND EnvId = {p1:String} AND ServiceName IN ({p2:String})
    GROUP BY EnvId, UserId))
WHERE has_signup = 1 OR (reports = 0 AND first_at >= data_from + INTERVAL 7 DAY)),
nd_cur AS (
SELECT EnvId, toUInt8(max(has_signup)) AS ReportsSignups, min(first_at) AS DataFrom,
       if(max(has_signup) = 1, toDateTime64(0, 9), min(first_at) + INTERVAL 7 DAY) AS DecidedFrom
FROM (
    SELECT EnvId, UserId, min(Timestamp) AS first_at, minIf(Timestamp, EventName = 'auth_signed_up') AS signed_up_at,
           toUInt8(countIf(EventName = 'auth_signed_up') > 0) AS has_signup
    FROM t_org.product_events
    WHERE UserId != '' AND EventName NOT IN ('auth_signed_out', 'auth_accounts') AND Props['forge.kind'] != 'state' AND Timestamp >= fromUnixTimestamp64Nano({p3:Int64}) - INTERVAL 90 DAY AND Timestamp < fromUnixTimestamp64Nano({p3:Int64}) AND EnvId = {p1:String} AND ServiceName IN ({p2:String})
    GROUP BY EnvId, UserId)
GROUP BY EnvId),
nd_prev AS (
SELECT EnvId, toUInt8(max(has_signup)) AS ReportsSignups, min(first_at) AS DataFrom,
       if(max(has_signup) = 1, toDateTime64(0, 9), min(first_at) + INTERVAL 7 DAY) AS DecidedFrom
FROM (
    SELECT EnvId, UserId, min(Timestamp) AS first_at, minIf(Timestamp, EventName = 'auth_signed_up') AS signed_up_at,
           toUInt8(countIf(EventName = 'auth_signed_up') > 0) AS has_signup
    FROM t_org.product_events
    WHERE UserId != '' AND EventName NOT IN ('auth_signed_out', 'auth_accounts') AND Props['forge.kind'] != 'state' AND Timestamp >= fromUnixTimestamp64Nano({p4:Int64}) - INTERVAL 90 DAY AND Timestamp < fromUnixTimestamp64Nano({p4:Int64}) AND EnvId = {p1:String} AND ServiceName IN ({p2:String})
    GROUP BY EnvId, UserId)
GROUP BY EnvId)
SELECT 'cur' AS kind, CAST(EnvId AS String) AS env_id, '' AS day,
       countIf(a = 1) AS active_users, countIf(n = 1) AS new_users, countIf(a = 1 AND n = 0) AS returning_users,
       CAST([] AS Array(String)), toDateTime64(0, 9), toUInt32(0), toUInt8(0)
FROM (
  SELECT EnvId, UserId, max(src = 'a') AS a, max(src = 'n') AS n
  FROM (
    SELECT EnvId, UserId, 'a' AS src FROM act WHERE Timestamp >= fromUnixTimestamp64Nano({p4:Int64}) AND Timestamp < fromUnixTimestamp64Nano({p3:Int64})
    UNION ALL
    SELECT EnvId, UserId, 'n' AS src FROM nu_cur WHERE NewAt >= fromUnixTimestamp64Nano({p4:Int64}) AND NewAt < fromUnixTimestamp64Nano({p3:Int64}))
  GROUP BY EnvId, UserId)
GROUP BY EnvId
UNION ALL
SELECT 'prev' AS kind, CAST(EnvId AS String) AS env_id, '' AS day,
       countIf(a = 1) AS active_users, countIf(n = 1) AS new_users, countIf(a = 1 AND n = 0) AS returning_users,
       CAST([] AS Array(String)), toDateTime64(0, 9), toUInt32(0), toUInt8(0)
FROM (
  SELECT EnvId, UserId, max(src = 'a') AS a, max(src = 'n') AS n
  FROM (
    SELECT EnvId, UserId, 'a' AS src FROM act WHERE Timestamp >= fromUnixTimestamp64Nano({p5:Int64}) AND Timestamp < fromUnixTimestamp64Nano({p4:Int64})
    UNION ALL
    SELECT EnvId, UserId, 'n' AS src FROM nu_prev WHERE NewAt >= fromUnixTimestamp64Nano({p5:Int64}) AND NewAt < fromUnixTimestamp64Nano({p4:Int64}))
  GROUP BY EnvId, UserId)
GROUP BY EnvId
UNION ALL
SELECT 'day' AS kind, CAST(EnvId AS String) AS env_id, toString(d) AS day,
       countIf(a = 1) AS active_users, countIf(n = 1) AS new_users, countIf(a = 1 AND n = 0) AS returning_users,
       CAST([] AS Array(String)), toDateTime64(0, 9), toUInt32(0), toUInt8(0)
FROM (
  SELECT EnvId, d, UserId, max(src = 'a') AS a, max(src = 'n') AS n
  FROM (
    SELECT EnvId, toDate(Timestamp, 'UTC') AS d, UserId, 'a' AS src FROM act WHERE Timestamp >= fromUnixTimestamp64Nano({p7:Int64})
    UNION ALL
    SELECT EnvId, toDate(NewAt, 'UTC') AS d, UserId, 'n' AS src FROM nu_cur WHERE NewAt >= fromUnixTimestamp64Nano({p7:Int64}))
  GROUP BY EnvId, d, UserId)
GROUP BY EnvId, d
UNION ALL
SELECT 'env' AS kind, CAST(EnvId AS String) AS env_id, '' AS day, toUInt64(0), toUInt64(0), toUInt64(0),
       arraySort(groupUniqArrayIf(CAST(Client AS String), Timestamp >= fromUnixTimestamp64Nano({p4:Int64}))) AS clients, min(Timestamp) AS first_at,
       toUInt32(max(RetentionDays)) AS retention_days, toUInt8(0) AS flag
FROM act GROUP BY EnvId
UNION ALL
SELECT 'rule' AS kind, CAST(EnvId AS String) AS env_id, '' AS day, toUInt64(0), toUInt64(0), toUInt64(0),
       CAST([] AS Array(String)), DecidedFrom, toUInt32(0), ReportsSignups AS flag
FROM nd_cur
UNION ALL
SELECT 'rule_prev' AS kind, CAST(EnvId AS String) AS env_id, '' AS day, toUInt64(0), toUInt64(0), toUInt64(0),
       CAST([] AS Array(String)), DecidedFrom, toUInt32(0), ReportsSignups AS flag
FROM nd_prev
UNION ALL
SELECT 'seen' AS kind, CAST(EnvId AS String) AS env_id, '' AS day,
       toUInt64(uniqExact(UserId)) AS active_users, toUInt64(0), toUInt64(0),
       CAST([] AS Array(String)), min(Timestamp) AS first_at, toUInt32(max(RetentionDays)) AS retention_days, toUInt8(0)
FROM t_org.product_events
WHERE UserId != '' AND EventName NOT IN ('auth_signed_out', 'auth_accounts') AND Props['forge.kind'] != 'state' AND Timestamp >= fromUnixTimestamp64Nano({p8:Int64}) AND Timestamp < fromUnixTimestamp64Nano({p3:Int64}) AND EnvId = {p1:String} AND ServiceName IN ({p2:String})
GROUP BY EnvId
