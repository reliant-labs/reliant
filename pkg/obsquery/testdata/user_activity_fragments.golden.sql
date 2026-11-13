-- ActiveEvidence
UserId != '' AND EventName NOT IN ('auth_signed_out', 'auth_accounts')

-- NewUsersSource
SELECT EnvId, UserId, if(reports = 1, signed_up_at, first_at) AS NewAt, reports AS ReportsSignups
FROM (
  SELECT EnvId, UserId, first_at, signed_up_at, has_signup, max(has_signup) OVER (PARTITION BY EnvId) AS reports
  FROM (
    SELECT EnvId, UserId, min(Timestamp) AS first_at, minIf(Timestamp, EventName = 'auth_signed_up') AS signed_up_at,
           toUInt8(countIf(EventName = 'auth_signed_up') > 0) AS has_signup
    FROM product_events
    WHERE UserId != '' AND EventName NOT IN ('auth_signed_out', 'auth_accounts') AND Timestamp >= {to:DateTime64(9)} - INTERVAL 90 DAY AND Timestamp < {to:DateTime64(9)} AND (empty({env_ids:Array(String)}) OR has({env_ids:Array(String)}, EnvId))
    GROUP BY EnvId, UserId))
WHERE has_signup = 1 OR reports = 0
