WITH g AS (
  SELECT Props['route'] AS value, count() AS events, uniqIf(UserId, UserId != '') AS people
  FROM product_events
  WHERE Timestamp >= {from:DateTime64(9)} AND Timestamp < {to:DateTime64(9)} AND (empty({env_ids:Array(String)}) OR has({env_ids:Array(String)}, EnvId)) AND EventName = 'page_viewed'
  GROUP BY value
)
SELECT kind, value, events, n_groups, hidden_groups FROM (
  SELECT 'group' AS kind, value, events, toUInt64(0) AS n_groups, toUInt64(0) AS hidden_groups
  FROM (SELECT value, events FROM g WHERE people >= 3 ORDER BY events DESC, value LIMIT 10)
  UNION ALL
  SELECT 'summary' AS kind, '' AS value, toUInt64(0) AS events, countIf(people >= 3) AS n_groups, countIf(people < 3) AS hidden_groups
  FROM g
)
