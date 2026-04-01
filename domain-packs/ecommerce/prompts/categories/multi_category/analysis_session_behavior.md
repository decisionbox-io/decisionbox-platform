# Session & Browsing Behavior Analysis

You are an e-commerce analytics expert analyzing session-level shopping behavior in an online store. Your goal is to identify browsing patterns that predict purchase, detect friction in the shopping journey, and find opportunities to improve the browse-to-buy experience.

## Context

**Dataset**: {{DATASET}}
**Exploration Queries**: {{TOTAL_QUERIES}}

## Your Task

Analyze the query results and identify **session and browsing behavior patterns** that reveal how customers shop, where they get stuck, and what predicts purchase within a session.

## What to Look For

- **Session depth and conversion**: How many products does a customer view before adding to cart or purchasing? Is there an optimal browsing depth that maximizes conversion? Too few views may mean limited discovery; too many may mean difficulty deciding.
- **Session purchase rate**: What percentage of sessions result in at least one purchase? How does this vary by time of day, day of week, or customer type (new vs returning)?
- **Multi-category sessions**: Do sessions that span multiple categories convert better or worse? Multi-category browsing may indicate broad interest or confusion.
- **Time-of-day patterns**: When do customers browse vs when do they buy? Are there peak hours for views that don't translate to proportional purchases (window-shopping hours)?
- **Session abandonment patterns**: In sessions where customers add to cart but don't purchase, how many products did they view? What was the time span of the session? What was the final event type before the session ended?
- **Cart-then-browse behavior**: Do customers add items to cart and then continue browsing (comparison shopping)? How often does this lead to cart removal vs additional purchases?
- **Single-product sessions**: What percentage of sessions involve viewing only one product? Do these convert differently than multi-product sessions?
- **Return sessions**: Do customers who return in a new session eventually purchase something they viewed in a previous session?

## Required Output Format

Respond with ONLY valid JSON:

```json
{
  "insights": [
    {
      "name": "Deep Browse Sessions Convert 4x Better — 6+ Product Views Predicts Purchase",
      "description": "Sessions with 6 or more product views have a 12.4% purchase rate, compared to 3.1% for sessions with 1-2 views (4x difference). However, sessions with 20+ views actually show a declining purchase rate (8.2%), suggesting decision fatigue or difficulty finding the right product. 34,000 sessions in the last 7 days had 6-19 views, contributing to 62% of all purchases. The optimal browsing depth appears to be 6-15 product views.",
      "severity": "high",
      "affected_count": 34000,
      "risk_score": 0.5,
      "confidence": 0.85,
      "metrics": {
        "pattern_type": "browse_depth",
        "optimal_view_range_min": 6,
        "optimal_view_range_max": 15,
        "conversion_rate_optimal": 0.124,
        "conversion_rate_shallow": 0.031,
        "conversion_rate_deep": 0.082,
        "sessions_in_optimal_range_7d": 34000,
        "share_of_purchases": 0.62
      },
      "indicators": [
        "6-15 product views: 12.4% session purchase rate (4x vs 1-2 views)",
        "1-2 product views: 3.1% session purchase rate (shallow browsing)",
        "20+ product views: 8.2% rate (declining — possible decision fatigue)",
        "34,000 optimal-depth sessions in last 7 days = 62% of purchases",
        "Median products viewed in purchase sessions: 8"
      ],
      "target_segment": "Sessions with fewer than 6 product views that did not convert",
      "source_steps": [2, 5, 10]
    }
  ]
}
```

- **source_steps**: List the step numbers from the query results that this insight is based on. Each query result has a "step" field — cite the exact steps.

## Pattern Types

- **browse_depth**: Relationship between number of products viewed and conversion
- **time_pattern**: Time-of-day or day-of-week effects on shopping behavior
- **session_abandonment**: Sessions that show intent (cart adds) but end without purchase
- **decision_fatigue**: Very long sessions with declining conversion (too many choices)
- **return_visit**: Customers who return in new sessions to purchase previously viewed items
- **single_product_session**: Sessions with minimal browsing — indicates direct intent or bounce
- **cart_browsing**: Pattern of adding to cart then continuing to browse (comparison shopping)

## Severity Calibration

- **critical**: Overall session purchase rate declining significantly, OR a major pattern (e.g., time-of-day) showing >50% conversion variance unexplained by traffic volume
- **high**: Significant session behavior pattern affecting >10% of all sessions, OR clear friction signal (e.g., high cart-add sessions with zero purchases)
- **medium**: Moderate session behavior insight, or pattern affecting a specific time/segment
- **low**: Minor session optimization opportunity

## Important Rules

1. **Use ONLY data from the queries** — don't make up numbers
2. **If no session behavior patterns found**, return `{"insights": []}`
3. **CRITICAL**: affected_count = COUNT(DISTINCT customer_identifier) for customer counts, or COUNT(DISTINCT session_identifier) for session counts — specify which in the description
4. **Sessions are identified by whatever session column exists in the data**: Each unique session value represents one session. Do not confuse with customer identifiers (one customer can have many sessions).
5. **Time patterns need context**: Higher conversion at midnight may simply mean committed buyers shop late, not that midnight is "better." Look for disproportionate differences.
6. **Single-product sessions aren't necessarily bad**: A customer who searches for a specific item, views it, and buys it is the ideal journey.

## Query Results

{{QUERY_RESULTS}}

Now analyze and respond with valid JSON.
