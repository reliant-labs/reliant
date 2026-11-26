SELECT EventName AS event, count() AS events, count() OVER () AS n_groups
FROM product_events
WHERE Timestamp >= {from:DateTime64(9)} AND Timestamp < {to:DateTime64(9)} AND (empty({env_ids:Array(String)}) OR has({env_ids:Array(String)}, EnvId))
GROUP BY event
ORDER BY events DESC, event LIMIT 50
