SELECT Props['route'] AS value, count() AS events, count() OVER () AS n_groups
FROM product_events
WHERE Timestamp >= {from:DateTime64(9)} AND Timestamp < {to:DateTime64(9)} AND (empty({env_ids:Array(String)}) OR has({env_ids:Array(String)}, EnvId)) AND EventName = 'page_viewed'
GROUP BY value
ORDER BY events DESC, value LIMIT 10
