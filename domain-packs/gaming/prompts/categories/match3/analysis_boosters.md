# Booster Usage Analysis

You are a gaming analytics expert analyzing booster/power-up usage patterns in a match-3 puzzle game. Your goal is to identify patterns in how players earn, use, and purchase boosters — revealing both problems (depletion risks, economy imbalance) and opportunities (conversion triggers, underutilized items).

## Context

**Dataset**: {{DATASET}}
**Exploration Queries**: {{TOTAL_QUERIES}}

## Your Task

Analyze the query results and identify **booster usage patterns** that reveal opportunities or problems.

## What to Look For

- **Depletion patterns**: When do players run out of each booster type? What level/day does depletion typically happen?
- **Usage vs level difficulty**: Do booster usage spikes correlate with hard levels? Are boosters being used where they're most needed?
- **Economy balance**: Is the earn rate sustainable? Are players earning enough boosters through gameplay, or forced to purchase?
- **Purchase triggers**: What events lead to booster purchases? (Running out? Facing a hard level? Seeing an ad offer?)
- **Free vs paid usage**: Ratio of earned boosters vs purchased. Is the balance healthy?
- **Churn correlation**: Does booster depletion predict churn? What's the churn multiplier for depleted vs stocked players?
- **Ad-earned boosters**: How many boosters come from rewarded ads? What's the ad-to-booster conversion rate?
- **Underutilized boosters**: Are some boosters available but rarely used? (Awareness issue or design problem)
- **Cross-booster patterns**: Do players who use one booster type tend to use others? Or do they specialize?

## Required Output Format

Respond with ONLY valid JSON:

```json
{
  "insights": [
    {
      "name": "Hint Booster Depletion Before Level 15 — 2.3x Churn Risk",
      "description": "78% of players exhaust their free Hint boosters before reaching level 15, receiving an average of 5 free hints from onboarding but using them all by level 12. Players who run out of hints have a 2.3x higher churn rate than those with remaining stock. Only 12% purchase hint packs after depletion — the rest either quit or struggle without. 1,500 players affected in last 30 days.",
      "severity": "high",
      "affected_count": 1500,
      "risk_score": 0.65,
      "confidence": 0.8,
      "metrics": {
        "booster_name": "Hint",
        "pattern_type": "depletion_risk",
        "depletion_level": 15,
        "depletion_rate": 0.78,
        "churn_multiplier": 2.3,
        "avg_starting_amount": 5,
        "avg_earned_per_day": 0.3,
        "avg_used_per_day": 0.8,
        "purchase_rate_after_depletion": 0.12
      },
      "indicators": [
        "78% deplete hints before level 15",
        "2.3x higher churn after depletion",
        "Only 12% purchase hint packs after running out",
        "Earn rate (0.3/day) far below usage rate (0.8/day)",
        "5 free hints from onboarding exhausted by level 12"
      ],
      "target_segment": "Players who depleted hints before level 15",
      "source_steps": [4, 8]
    }
  ]
}
```

- **source_steps**: List the step numbers from the query results that this insight is based on. Each query result has a "step" field — cite the exact steps.

## Pattern Types

- **depletion_risk**: Players running out of boosters at critical moments, correlating with churn
- **underutilized**: Boosters available but not used — awareness, UX, or design issue
- **purchase_opportunity**: High usage of free boosters with low purchase rate — conversion opportunity
- **effectiveness**: Booster usage strongly correlates with level completion or retention
- **economy_imbalance**: Earn rate significantly mismatched with usage rate (too generous or too stingy)
- **ad_conversion**: Rewarded ad viewing patterns — how many players earn boosters through ads

## Severity Calibration

- **critical**: Booster depletion directly causing measurable churn (churn_multiplier > 2x), affecting 100+ players
- **high**: Strong depletion-churn correlation or significant conversion opportunity, affecting 50+ players
- **medium**: Moderate economy imbalance or underutilization pattern
- **low**: Minor optimization opportunity or small segment affected

## Important Rules

1. **Use ONLY data from the queries** — don't make up numbers
2. **If no booster patterns found**, return `{"insights": []}`
3. **Minimum 50 players** for a pattern to be significant
4. **CRITICAL**: affected_count = COUNT(DISTINCT user_id)
5. **Compare earn vs spend rates**: If players earn 0.3 boosters/day but use 0.8/day, that's a sustainability problem
6. **Look at the full chain**: Depletion → behavior change → churn (or purchase). Don't stop at depletion alone.

## Query Results

{{QUERY_RESULTS}}

Now analyze and respond with valid JSON.
