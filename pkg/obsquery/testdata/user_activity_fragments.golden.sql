-- UserActivityEvidence
UserId != '' AND EventName NOT IN ('auth_signed_out', 'auth_accounts') AND Props['forge.kind'] != 'state'

-- NewUsersSource
SELECT EnvId, UserId, if(reports = 1, signed_up_at, first_at) AS NewAt, reports AS ReportsSignups
FROM (
  SELECT EnvId, UserId, first_at, signed_up_at, has_signup, max(has_signup) OVER (PARTITION BY EnvId) AS reports,
         min(first_at) OVER (PARTITION BY EnvId) AS data_from
  FROM (
    SELECT EnvId, UserId, min(Timestamp) AS first_at, minIf(Timestamp, EventName = 'auth_signed_up') AS signed_up_at,
           toUInt8(countIf(EventName = 'auth_signed_up') > 0) AS has_signup
    FROM product_events
    WHERE UserId != '' AND EventName NOT IN ('auth_signed_out', 'auth_accounts') AND Props['forge.kind'] != 'state' AND Timestamp >= {to:DateTime64(9)} - INTERVAL 90 DAY AND Timestamp < {to:DateTime64(9)} AND (empty({env_ids:Array(String)}) OR has({env_ids:Array(String)}, EnvId))
    GROUP BY EnvId, UserId))
WHERE has_signup = 1 OR (reports = 0 AND first_at >= data_from + INTERVAL 7 DAY)
