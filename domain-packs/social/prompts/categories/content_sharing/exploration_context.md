## Content Sharing Platform Context

This is a **content sharing social network** where users create and consume content — photos, videos, stories, or short-form posts. Key aspects to explore:

- **Creator-consumer ecosystem**: The platform depends on a healthy creator base producing content for consumers. Analyze the creator-to-consumer ratio, creator retention vs consumer retention, and whether the content supply is growing or shrinking. A 90/9/1 pattern is common (90% lurkers, 9% occasional contributors, 1% power creators).
- **Content lifecycle**: How content is created, discovered (feed algorithm, explore, search, hashtags), consumed (views, watch time, completion rate), and interacted with (likes, comments, shares, saves). Identify which content types perform best and where content discovery fails.
- **Feed and discovery**: How effectively does the feed surface relevant content? Are users scrolling past content without engaging? Is there content that gets zero impressions despite being high quality?
- **Social graph**: Following/follower dynamics, network density, reciprocal vs one-directional relationships. Identify isolated users (few connections) vs well-connected users and their retention differences.
- **Premium features and IAP**: Many social platforms monetize through premium subscriptions (VIP badges, enhanced profiles, priority matching), paid interactions (super likes, paid messaging, content boosts), virtual currencies (coins, tokens for tipping creators), and gated content (pay-to-view posts, exclusive content). Analyze conversion funnels, purchase triggers, and premium user retention.
- **Creator monetization**: If the platform shares revenue with creators (creator fund, ad revenue share, tips, paid subscriptions to individual creators), analyze creator earnings, earnings distribution, and how earnings affect creator retention.
- **Messaging and direct interactions**: DMs, comments, and replies drive deep engagement. Analyze messaging adoption, conversation depth, and the impact on retention. Note paid messaging features (pay-to-DM, priority inbox).
- **Content moderation and safety**: Report rates, content removal, user blocking patterns. High moderation activity may indicate community health issues.
- **Cross-platform sharing**: Content shared to external platforms (WhatsApp, Twitter, etc.) drives organic growth. Track external share rates and resulting signups.

### Content Sharing Example Queries

**Creator-Consumer Ratio Trend**:
```sql
SELECT
  DATE_TRUNC(event_date, WEEK) as week,
  COUNT(DISTINCT CASE WHEN posts_this_week > 0 THEN user_id END) as creators,
  COUNT(DISTINCT CASE WHEN posts_this_week = 0 AND sessions_this_week > 0 THEN user_id END) as consumers,
  SAFE_DIVIDE(
    COUNT(DISTINCT CASE WHEN posts_this_week > 0 THEN user_id END),
    COUNT(DISTINCT user_id)
  ) as creator_ratio
FROM `{{DATASET}}.weekly_user_activity`
{{FILTER}}
  AND event_date >= DATE_SUB(CURRENT_DATE(), INTERVAL 12 WEEK)
GROUP BY week
ORDER BY week DESC
```

**Content Performance by Type**:
```sql
SELECT
  content_type,
  COUNT(*) as total_posts,
  AVG(views_count) as avg_views,
  AVG(likes_count) as avg_likes,
  AVG(comments_count) as avg_comments,
  AVG(shares_count) as avg_shares,
  AVG(SAFE_DIVIDE(likes_count + comments_count + shares_count, views_count)) as avg_engagement_rate
FROM `{{DATASET}}.content_posts`
{{FILTER}}
  AND created_date >= DATE_SUB(CURRENT_DATE(), INTERVAL 30 DAY)
GROUP BY content_type
ORDER BY total_posts DESC
```

**Premium Feature Adoption**:
```sql
SELECT
  feature_name,
  COUNT(DISTINCT user_id) as unique_purchasers,
  SUM(revenue_usd) as total_revenue,
  AVG(revenue_usd) as avg_transaction,
  COUNT(*) as total_transactions
FROM `{{DATASET}}.iap_transactions`
{{FILTER}}
  AND purchase_date >= DATE_SUB(CURRENT_DATE(), INTERVAL 30 DAY)
GROUP BY feature_name
ORDER BY total_revenue DESC
```

**Social Graph Health**:
```sql
SELECT
  CASE
    WHEN following_count = 0 THEN 'isolated'
    WHEN following_count < 5 THEN 'low_connections'
    WHEN following_count < 20 THEN 'moderate_connections'
    ELSE 'well_connected'
  END as connection_segment,
  COUNT(DISTINCT user_id) as users,
  AVG(days_active_last_30d) as avg_days_active,
  AVG(sessions_last_30d) as avg_sessions
FROM `{{DATASET}}.user_social_graph`
{{FILTER}}
GROUP BY connection_segment
ORDER BY users DESC
```

**Virtual Currency Economy**:
```sql
SELECT
  transaction_type,
  COUNT(DISTINCT user_id) as unique_users,
  SUM(coin_amount) as total_coins,
  AVG(coin_amount) as avg_per_transaction
FROM `{{DATASET}}.virtual_currency_transactions`
{{FILTER}}
  AND event_date >= DATE_SUB(CURRENT_DATE(), INTERVAL 30 DAY)
GROUP BY transaction_type
ORDER BY total_coins DESC
```

**Creator Earnings Distribution**:
```sql
SELECT
  CASE
    WHEN total_earnings_usd = 0 THEN 'zero_earnings'
    WHEN total_earnings_usd < 10 THEN 'micro_earner'
    WHEN total_earnings_usd < 100 THEN 'small_earner'
    WHEN total_earnings_usd < 1000 THEN 'medium_earner'
    ELSE 'top_earner'
  END as earnings_tier,
  COUNT(DISTINCT user_id) as creators,
  SUM(total_earnings_usd) as total_earnings,
  AVG(posts_last_30d) as avg_posts
FROM `{{DATASET}}.creator_earnings`
{{FILTER}}
  AND period_date >= DATE_SUB(CURRENT_DATE(), INTERVAL 30 DAY)
GROUP BY earnings_tier
ORDER BY total_earnings DESC
```
