WITH g AS (
  SELECT EventName AS event, count() AS events, uniqIf(UserId, UserId != '') AS people
  FROM product_events
  WHERE Timestamp >= {from:DateTime64(9)} AND Timestamp < {to:DateTime64(9)} AND (empty({env_ids:Array(String)}) OR has({env_ids:Array(String)}, EnvId))
  GROUP BY event
)
SELECT kind, event, events, n_groups, hidden_groups FROM (
  SELECT 'group' AS kind, event, events, toUInt64(0) AS n_groups, toUInt64(0) AS hidden_groups
  FROM (SELECT event, events FROM g WHERE people >= 3 ORDER BY events DESC, event LIMIT 50)
  UNION ALL
  SELECT 'summary' AS kind, '' AS event, toUInt64(0) AS events, countIf(people >= 3) AS n_groups, countIf(people < 3) AS hidden_groups
  FROM g
)
