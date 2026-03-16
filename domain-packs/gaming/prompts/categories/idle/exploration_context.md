## Idle / Incremental Game Context

This is an **idle/incremental game** where players progress by accumulating resources over time, both actively and passively. Key aspects to explore:

- **Offline earnings**: Players earn resources while not playing. Analyze offline earning rates, return patterns, and whether offline rewards feel satisfying enough to drive re-engagement. If offline earnings are too low, players stop returning. If too high, active play feels unrewarding.
- **Prestige/rebirth cycles**: Most idle games have a reset mechanic where players restart with permanent bonuses. Analyze prestige timing, how many cycles players complete before churning, and whether the prestige rewards feel worth the reset.
- **Upgrade progression**: Players purchase upgrades (generators, multipliers, automation) that increase resource production. Look for pricing bottlenecks where progression stalls, and identify upgrade tiers that players skip or avoid.
- **Session patterns**: Idle game sessions tend to be very short (check-ins of 1-3 minutes) but frequent (3-10+ times per day). Analyze session frequency, duration, and how they evolve as players progress through the game.
- **Currency economy**: Most idle games have soft currency (earned through gameplay) and hard currency (earned through ads/IAP). Track inflation — is soft currency becoming worthless? Is the earn-to-spend ratio sustainable?
- **Monetization**: Typically rewarded video ads (2x offline earnings, instant boosts) and IAP (premium currency, time skips, automation unlocks). Analyze ad watch frequency, tolerance, and conversion to IAP.
- **Milestone pacing**: Key unlocks (new worlds, new generators, automation features) drive continued play. Are milestones spaced well? Do players quit between milestones?

### Idle Game Example Queries

**Prestige Cycle Analysis**:
```sql
SELECT
  prestige_count,
  COUNT(DISTINCT user_id) as players,
  AVG(time_to_prestige_hours) as avg_hours_to_prestige,
  AVG(prestige_multiplier_gained) as avg_multiplier
FROM `{{DATASET}}.prestige_events`
{{FILTER}}
  AND event_date >= DATE_SUB(CURRENT_DATE(), INTERVAL 30 DAY)
GROUP BY prestige_count
ORDER BY prestige_count
```

**Offline Earnings Effectiveness**:
```sql
SELECT
  CASE
    WHEN offline_duration_hours < 1 THEN 'under_1h'
    WHEN offline_duration_hours < 4 THEN '1_to_4h'
    WHEN offline_duration_hours < 12 THEN '4_to_12h'
    ELSE 'over_12h'
  END as offline_bucket,
  COUNT(DISTINCT user_id) as players,
  AVG(offline_earnings_collected) as avg_earnings,
  AVG(session_duration_seconds) as avg_session_after_return
FROM `{{DATASET}}.sessions`
{{FILTER}}
  AND event_date >= DATE_SUB(CURRENT_DATE(), INTERVAL 30 DAY)
  AND offline_duration_hours > 0
GROUP BY offline_bucket
ORDER BY offline_bucket
```

**Upgrade Purchase Velocity**:
```sql
SELECT
  upgrade_name,
  upgrade_tier,
  COUNT(DISTINCT user_id) as purchasers,
  AVG(player_day) as avg_day_purchased,
  AVG(cost) as avg_cost
FROM `{{DATASET}}.upgrade_purchases`
{{FILTER}}
  AND event_date >= DATE_SUB(CURRENT_DATE(), INTERVAL 30 DAY)
GROUP BY upgrade_name, upgrade_tier
ORDER BY upgrade_name, upgrade_tier
```

**Session Check-in Frequency**:
```sql
SELECT
  user_id,
  COUNT(*) as sessions_per_day,
  AVG(session_duration_seconds) as avg_session_seconds,
  MAX(highest_stage_reached) as max_stage
FROM `{{DATASET}}.sessions`
{{FILTER}}
  AND event_date = DATE_SUB(CURRENT_DATE(), INTERVAL 1 DAY)
GROUP BY user_id
HAVING sessions_per_day >= 2
ORDER BY sessions_per_day DESC
LIMIT 100
```
