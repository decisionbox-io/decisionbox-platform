## Casual / Hyper-Casual Game Context

This is a **casual or hyper-casual game** characterized by simple mechanics, short sessions, and broad audience appeal. Key aspects to explore:

- **Session brevity**: Sessions are typically very short (30 seconds to 5 minutes for hyper-casual, 3-10 minutes for casual). Analyze session duration distributions, sessions per day, and how session patterns predict retention.
- **Onboarding & first session**: The first 30-60 seconds determine whether a player returns. Analyze first-session completion rates, tutorial skip rates, and first-session-to-second-session conversion.
- **Core loop engagement**: Casual games have a tight core loop (play → result → reward → play again). Measure loops per session, loop completion rates, and at what point players break the loop to leave.
- **Ad monetization**: Casual/hyper-casual games are heavily ad-dependent. Analyze ad frequency, format mix (interstitial vs rewarded vs banner), eCPM trends, and the critical balance between ad revenue and user retention.
- **Ad tolerance**: Too many ads drive players away. Analyze the relationship between ad frequency and churn. Find the optimal ad frequency that maximizes revenue without destroying retention.
- **Feature discovery**: Many casual players never discover secondary features (daily challenges, achievements, social features). Track what percentage of players encounter each feature and whether it correlates with retention.
- **Viral & social mechanics**: If the game has sharing, challenges, or referrals, analyze their effectiveness as acquisition and retention drivers.
- **Content freshness**: Casual players get bored quickly. If the game has levels/stages/content, analyze how quickly players exhaust content and whether new content drops correlate with re-engagement.

### Casual Game Example Queries

**First Session Funnel**:
```sql
SELECT
  onboarding_step,
  COUNT(DISTINCT user_id) as players_reached,
  COUNT(DISTINCT CASE WHEN completed = true THEN user_id END) as players_completed
FROM `{{DATASET}}.onboarding_events`
{{FILTER}}
  AND event_date >= DATE_SUB(CURRENT_DATE(), INTERVAL 30 DAY)
GROUP BY onboarding_step
ORDER BY onboarding_step
```

**Session Duration Distribution**:
```sql
SELECT
  CASE
    WHEN session_duration_seconds < 30 THEN 'under_30s'
    WHEN session_duration_seconds < 60 THEN '30s_to_1m'
    WHEN session_duration_seconds < 180 THEN '1m_to_3m'
    WHEN session_duration_seconds < 300 THEN '3m_to_5m'
    ELSE 'over_5m'
  END as duration_bucket,
  COUNT(DISTINCT user_id) as unique_players,
  COUNT(*) as total_sessions,
  AVG(core_loops_completed) as avg_loops
FROM `{{DATASET}}.sessions`
{{FILTER}}
  AND event_date >= DATE_SUB(CURRENT_DATE(), INTERVAL 7 DAY)
GROUP BY duration_bucket
ORDER BY duration_bucket
```

**Ad Frequency vs Retention**:
```sql
SELECT
  CASE
    WHEN ads_shown_per_session < 2 THEN 'low_ads'
    WHEN ads_shown_per_session < 5 THEN 'medium_ads'
    ELSE 'high_ads'
  END as ad_frequency,
  COUNT(DISTINCT user_id) as players,
  AVG(day_1_returned) as d1_retention,
  AVG(day_7_returned) as d7_retention
FROM `{{DATASET}}.user_ad_exposure`
{{FILTER}}
  AND event_date >= DATE_SUB(CURRENT_DATE(), INTERVAL 30 DAY)
GROUP BY ad_frequency
```

**Core Loop Completion**:
```sql
SELECT
  session_number,
  AVG(core_loops_completed) as avg_loops,
  AVG(session_duration_seconds) as avg_duration,
  COUNT(DISTINCT user_id) as players
FROM `{{DATASET}}.sessions`
{{FILTER}}
  AND event_date >= DATE_SUB(CURRENT_DATE(), INTERVAL 30 DAY)
  AND session_number <= 10
GROUP BY session_number
ORDER BY session_number
```

**Feature Discovery Rate**:
```sql
SELECT
  feature_name,
  COUNT(DISTINCT user_id) as users_discovered,
  AVG(session_number_discovered) as avg_session_discovered,
  AVG(CASE WHEN day_7_retained = true THEN 1.0 ELSE 0.0 END) as d7_retention_if_discovered
FROM `{{DATASET}}.feature_discovery`
{{FILTER}}
  AND event_date >= DATE_SUB(CURRENT_DATE(), INTERVAL 30 DAY)
GROUP BY feature_name
ORDER BY users_discovered DESC
```
