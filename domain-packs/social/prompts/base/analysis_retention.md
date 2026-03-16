# Retention & Churn Analysis

You are a social network analytics expert analyzing user retention and churn patterns. Your goal is to identify where and why users leave the platform, which users are at risk, and what behaviors predict long-term retention vs abandonment.

## Context

**Dataset**: {{DATASET}}
**Exploration Queries**: {{TOTAL_QUERIES}}

## Your Task

Analyze the query results below and identify **specific retention and churn patterns** with exact numbers and percentages. Look across the full user lifecycle — from first-day retention through long-term loyalty and potential reactivation.

## Retention Dimensions to Analyze

- **Cohort retention curves**: D1, D7, D14, D30, D60, D90 retention by signup cohort. Are newer cohorts retaining better or worse than older ones?
- **Lifecycle-stage churn**: Where in the user lifecycle does churn concentrate?
  - **Immediate abandonment** (Day 0): Users who sign up but never return
  - **Early churn** (Day 1-7): Users who tried the platform but didn't form a habit
  - **Mid-term churn** (Day 7-30): Users who engaged initially but lost interest
  - **Late churn** (Day 30+): Established users leaving — the most damaging type
- **Churn predictors**: What behaviors in the first 24/48/72 hours predict 30-day retention? (e.g., following 5+ users, posting once, joining a group)
- **Segment-level retention**: Creators vs consumers, premium vs free users, mobile vs web, geographic differences
- **Premium user retention**: Paying users (VIP, subscribers) who churn represent direct revenue loss. Analyze subscription churn separately.
- **Reactivation patterns**: Do churned users ever come back? What triggers reactivation? (Push notifications, email campaigns, content from connections)
- **Social graph impact on retention**: Do users with more connections retain better? Is there a "magic number" of connections that predicts retention?
- **Content correlation**: Does seeing high-quality content in the first session predict retention? Does content freshness affect retention?

## Required Output Format

Respond with ONLY valid JSON (no markdown, no explanations):

```json
{
  "insights": [
    {
      "name": "Day 0 Abandonment — 52% of New Users Never Return After Signup",
      "description": "52% of users who complete registration never log in a second time. These users complete signup (avg 2.1 minutes) but the median first session is only 45 seconds — they sign up, look around briefly, and leave permanently. Users who follow at least 3 accounts in their first session have 3.1x higher D1 retention (68%) vs those who follow 0 (22%). This represents 7,800 lost users in the last 30 days.",
      "severity": "critical",
      "affected_count": 7800,
      "risk_score": 0.52,
      "confidence": 0.9,
      "metrics": {
        "churn_rate": 0.52,
        "lifecycle_stage": "immediate",
        "median_first_session_seconds": 45,
        "retention_with_follows": 0.68,
        "retention_without_follows": 0.22,
        "follows_threshold": 3,
        "reactivation_potential": "low"
      },
      "indicators": [
        "52% of signups never return after first session",
        "Median first session: 45 seconds (insufficient to discover value)",
        "Users who follow 3+ accounts: 68% D1 retention",
        "Users who follow 0 accounts: 22% D1 retention (3.1x gap)",
        "7,800 users lost to immediate abandonment in 30 days"
      ],
      "target_segment": "Users who completed registration but have 0 second-day logins",
      "source_steps": [1, 4, 8]
    }
  ]
}
```

- **source_steps**: List the step numbers from the query results that this insight is based on. Each query result has a "step" field — cite the exact steps.

## Lifecycle Stages

- **immediate** (Day 0): Signed up but never returned. Low reactivation potential.
- **early** (Day 1-7): Tried the platform briefly. Moderate reactivation potential if triggered quickly.
- **mid** (Day 7-30): Showed initial engagement but lost interest. Good reactivation potential with the right hook.
- **late** (Day 30+): Established users leaving. High concern — they represent invested users. Best reactivation potential.
- **premium_churn**: Paying users who cancel or stop paying. Direct revenue impact.

## Severity Calibration

When the project profile includes KPI targets, calibrate severity against them:
- **critical**: Retention below target by >20%, or a lifecycle stage losing >40% of users, or premium user churn increasing, or D1 retention <30%
- **high**: Retention significantly below target, or creator churn > consumer churn (platform health risk)
- **medium**: Retention moderately below target, affects 5-10% of active users
- **low**: Slightly elevated churn in a non-critical segment, or a positive reactivation trend

## Quality Standards

- **Name**: Be VERY specific — include lifecycle stage, cohort, metric, and magnitude
- **Description**: Must include exact percentages, user counts, behavioral predictors, and business impact
- **affected_count**: Actual count from data (COUNT(DISTINCT user_id)), not estimates
- **reactivation_potential**: Estimate based on user investment level (connections, content, premium status)
- **Minimum affected**: Only include patterns affecting 50+ users
- **CRITICAL — Validate user counts**: affected_count must be COUNT(DISTINCT user_id)
- **Premium users always flagged**: Any premium/VIP user churn should be reported regardless of magnitude

## Important Rules

1. **Use ONLY data from the queries below** — don't make up numbers
2. **Be extremely specific** — exact percentages, counts, time periods
3. **If no retention patterns found**, return `{"insights": []}`
4. **Creator churn > consumer churn in severity**: Losing a creator has cascading effects on their followers
5. **Premium churn = revenue loss**: Always calculate the revenue impact of premium user churn
6. **Don't duplicate**: Each insight should describe a unique pattern
7. **Cohort comparison is essential**: "D7 retention is 25%" is not useful. "D7 retention dropped from 32% to 25% over the last 4 cohorts" IS useful.

## Query Results

{{QUERY_RESULTS}}

Now analyze the data above and respond with valid JSON.
