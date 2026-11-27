WITH
asg AS (
  SELECT EnvId, UserId AS unit, min(Timestamp) AS first_at,
         uniqExact(Props['variant']) AS variants, if(variants = 1, any(Props['variant']), '') AS variant
  FROM t_org.product_events
  WHERE EventName = 'flag_evaluated' AND Props['provider'] = 'forge' AND Props['reason'] = 'experiment' AND Props['flag'] = {p5:String} AND Props['run'] = {p6:String} AND Props['unit'] = {p7:String}
    AND UserId != '' AND Timestamp >= fromUnixTimestamp64Nano({p2:Int64}) AND Timestamp < fromUnixTimestamp64Nano({p3:Int64}) AND EnvId = {p1:String}
  GROUP BY EnvId, unit),
outcome AS (
  SELECT EnvId, UserId AS unit, Timestamp
  FROM t_org.product_events
  WHERE EventName = {p8:String} AND Props[{p9:String}] = {p10:String}
    AND UserId != '' AND Timestamp >= fromUnixTimestamp64Nano({p2:Int64}) AND Timestamp <= fromUnixTimestamp64Nano({p3:Int64}) AND EnvId = {p1:String}),
units AS (
  SELECT any(a.variant) AS arm, any(a.variants) AS n_variants, any(a.first_at) AS assigned_at,
         toUInt8(countIf(o.Timestamp >= a.first_at AND o.Timestamp <= a.first_at + toIntervalHour({p4:UInt32})) > 0) AS conv,
         toUInt8(any(a.first_at) + toIntervalHour({p4:UInt32}) <= fromUnixTimestamp64Nano({p3:Int64})) AS done
  FROM asg AS a LEFT JOIN outcome AS o ON o.EnvId = a.EnvId AND o.unit = a.unit
  GROUP BY a.EnvId, a.unit)
SELECT 'arm' AS kind, arm AS variant, count() AS assigned, countIf(conv = 1) AS converted,
       countIf(done = 1) AS window_complete, countIf(done = 1 AND conv = 1) AS window_complete_converted,
       min(assigned_at) AS first_at, max(assigned_at) AS last_at
FROM units WHERE n_variants = 1 GROUP BY arm
UNION ALL
SELECT 'conflicted' AS kind, '' AS variant, countIf(n_variants > 1) AS assigned, toUInt64(0) AS converted,
       toUInt64(0) AS window_complete, toUInt64(0) AS window_complete_converted,
       min(assigned_at) AS first_at, max(assigned_at) AS last_at
FROM units
