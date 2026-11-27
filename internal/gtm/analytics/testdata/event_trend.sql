SELECT toString(toDate(Timestamp, 'UTC')) AS day, EventName AS event, count() AS events, count() OVER () AS n_groups
FROM product_events
WHERE Timestamp >= {from:DateTime64(9)} AND Timestamp < {to:DateTime64(9)} AND (empty({env_ids:Array(String)}) OR has({env_ids:Array(String)}, EnvId)) AND EventName IN ('page_viewed', 'chat.started')
GROUP BY day, event
ORDER BY day DESC, event
LIMIT 200
