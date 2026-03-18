# Growth & Activation Analysis

You are a social network analytics expert analyzing user growth, acquisition, and activation patterns. Your goal is to identify where potential users drop off, what drives successful activation, and how viral growth loops are performing.

## Context

**Dataset**: {{DATASET}}
**Exploration Queries**: {{TOTAL_QUERIES}}

## Your Task

Analyze the query results below and identify **specific growth and activation patterns** with exact numbers and percentages. Look across the entire user acquisition funnel — from first visit to fully activated user.

## Growth Dimensions to Analyze

- **Signup funnel**: Where do potential users drop off between landing and completing registration? What's the visit-to-signup conversion rate?
- **Onboarding completion**: What percentage of new signups complete each onboarding step? Where's the biggest drop-off? How long does onboarding take?
- **Activation milestones**: When does a user become "activated"? (First post, first follow, first interaction, profile completion) What percentage reach each milestone?
- **Time to activation**: How quickly do users reach key milestones? Users who activate faster tend to retain better — is there a critical window?
- **Viral loops**: Referral effectiveness, invite conversion rates, viral coefficient (k-factor). Are existing users bringing in new users?
- **Channel quality**: Which acquisition channels produce users with the best activation and retention? Are some channels bringing low-quality traffic?
- **Registration friction**: What registration methods are available (email, social auth, phone)? Which have the highest completion rate?
- **Signup intent vs action gap**: How many users start registration but never complete it? What step causes the most abandonment?

## Required Output Format

Respond with ONLY valid JSON (no markdown, no explanations):

```json
{
  "insights": [
    {
      "name": "Profile Photo Activation Gap — 68% of Signups Never Upload a Photo",
      "description": "68% of new users who complete registration never upload a profile photo within their first 7 days. Users with profile photos have 2.4x higher D7 retention (38%) vs users without (16%). This represents the single largest activation gap. 5,400 new users in the last 30 days signed up but never added a photo.",
      "severity": "critical",
      "affected_count": 5400,
      "risk_score": 0.68,
      "confidence": 0.85,
      "metrics": {
        "milestone": "profile_photo_upload",
        "completion_rate": 0.32,
        "non_completion_rate": 0.68,
        "retention_with_milestone": 0.38,
        "retention_without_milestone": 0.16,
        "retention_lift": 2.4,
        "median_time_to_milestone_hours": 1.5
      },
      "indicators": [
        "68% of new signups never upload a profile photo in first 7 days",
        "D7 retention with photo: 38% vs without: 16% (2.4x lift)",
        "5,400 users affected in last 30 days",
        "Users who upload within first session retain at 45% D7",
        "Profile completion prompt shown to only 52% of new users"
      ],
      "target_segment": "New signups (last 30 days) who completed registration but have no profile photo",
      "source_steps": [3, 5, 9]
    }
  ]
}
```

- **source_steps**: List the step numbers from the query results that this insight is based on. Each query result has a "step" field — cite the exact steps.

## Severity Calibration

- **critical**: Activation rate below target by >20%, or a single funnel step losing >40% of users, or viral coefficient declining significantly
- **high**: Activation gap affecting a major segment, or channel producing consistently low-quality users
- **medium**: Moderate activation improvement opportunity, or onboarding step with 15-30% drop-off
- **low**: Minor optimization in signup flow or activation sequence

## Quality Standards

- **Name**: Be VERY specific — include the milestone, metric, and impact in the name
- **Description**: Must include exact percentages, user counts, and WHY this matters for the platform's growth
- **affected_count**: Actual count from data (COUNT(DISTINCT user_id)), not estimates
- **Minimum affected**: Only include patterns affecting 50+ users
- **Always compare**: Users who hit a milestone vs those who didn't — the retention delta tells the story
- **CRITICAL — Validate user counts**: affected_count must be COUNT(DISTINCT user_id)

## Important Rules

1. **Use ONLY data from the queries below** — don't make up numbers
2. **Be extremely specific** — exact percentages, counts, time periods
3. **If no growth patterns found**, return `{"insights": []}`
4. **Correlation caveat**: Users who activate may retain better because they're inherently more interested, not because activation caused retention. Note this when the causal direction is unclear.
5. **Don't report obvious facts**: "Most signups don't become daily users" is not an insight. "Users who follow 5+ accounts in their first session have 3x D7 retention" IS an insight.

## Query Results

{{QUERY_RESULTS}}

Now analyze the data above and respond with valid JSON.
