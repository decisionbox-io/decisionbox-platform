## Match-3 Game Context

This is a **match-3 puzzle game**. Key aspects to explore:

- **Level progression**: Players progress through sequential levels organized into chapters/episodes. Look for difficulty spikes, quit rates per level, progression bottlenecks, and chapter transition drop-offs. Identify levels where the largest cohort of players gets permanently stuck.
- **Boosters/power-ups**: Items like Hint, Magnet, Extra Life, Hammer that help players pass levels. Analyze usage patterns, purchase vs earned ratios, depletion timing, and correlation with level completion and retention.
- **Lives system**: Players have limited lives. Running out of lives creates churn risk. Analyze life depletion patterns, recovery times, and whether players wait, watch ads, or pay to refill.
- **Star rating system**: Many match-3 games rate level completion (1-3 stars). Analyze star distribution, replay motivation, and perfectionist behavior.
- **Session patterns**: Match-3 players tend to have short-to-medium sessions (3-15 minutes). Look for session duration trends, session frequency by day of week, and how sessions evolve as players progress.
- **Monetization**: Typically freemium with IAP (booster packs, extra moves, no-ads) and rewarded video ads. Analyze conversion funnels, first-purchase triggers, and what in-game moments drive purchases.
- **Economy balance**: Track the flow of in-game currencies — how fast do players earn vs spend coins/boosters? Is there inflation? Are players running out too early or hoarding?
- **Social features**: If the game has teams, leaderboards, or gifting, analyze social engagement and its correlation with retention.

### Match-3 Example Queries

**Level Difficulty Analysis**:
```sql
SELECT level_number, quit_rate, success_rate, avg_attempts_per_player,
       COUNT(DISTINCT user_id) as unique_players
FROM `{{DATASET}}.level_performance_weekly_trends`
{{FILTER}}
  AND week_start_date >= DATE_SUB(CURRENT_DATE(), INTERVAL 30 DAY)
GROUP BY level_number, quit_rate, success_rate, avg_attempts_per_player
HAVING quit_rate > 0.3 OR success_rate < 0.4
ORDER BY quit_rate DESC
LIMIT 20
```

**Booster Usage Patterns**:
```sql
SELECT booster_name,
       COUNT(DISTINCT user_id) as unique_users,
       COUNT(*) as total_uses,
       AVG(level_number) as avg_level_used
FROM `{{DATASET}}.booster_usage`
{{FILTER}}
  AND event_date >= DATE_SUB(CURRENT_DATE(), INTERVAL 30 DAY)
GROUP BY booster_name
ORDER BY total_uses DESC
```

**Chapter Transition Drop-off**:
```sql
SELECT
  CAST(FLOOR((level_number - 1) / 20) + 1 AS INT64) as chapter,
  COUNT(DISTINCT user_id) as players_reached,
  MIN(level_number) as chapter_start_level
FROM `{{DATASET}}.user_level_progress`
{{FILTER}}
GROUP BY chapter
ORDER BY chapter
```

**Economy Balance — Booster Earn vs Spend**:
```sql
SELECT
  booster_name,
  SUM(CASE WHEN transaction_type = 'earn' THEN quantity ELSE 0 END) as total_earned,
  SUM(CASE WHEN transaction_type = 'spend' THEN quantity ELSE 0 END) as total_spent,
  COUNT(DISTINCT user_id) as unique_users
FROM `{{DATASET}}.economy_transactions`
{{FILTER}}
  AND event_date >= DATE_SUB(CURRENT_DATE(), INTERVAL 30 DAY)
GROUP BY booster_name
ORDER BY total_spent DESC
```

**Star Rating Distribution**:
```sql
SELECT level_number,
       stars_earned,
       COUNT(DISTINCT user_id) as player_count
FROM `{{DATASET}}.level_completions`
{{FILTER}}
  AND event_date >= DATE_SUB(CURRENT_DATE(), INTERVAL 30 DAY)
GROUP BY level_number, stars_earned
ORDER BY level_number, stars_earned
```
