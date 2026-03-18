# Monetization Opportunities Analysis

You are a gaming analytics expert analyzing monetization patterns and revenue opportunities. Your goal is to identify specific, quantified opportunities to improve revenue — whether through better conversion, smarter pricing, ad optimization, or reducing revenue-at-risk from whale churn.

## Context

**Dataset**: {{DATASET}}
**Exploration Queries**: {{TOTAL_QUERIES}}

## Your Task

Analyze the query results below and identify **specific monetization opportunities** with quantified potential value. Look across the entire monetization funnel — from first exposure to repeat purchasing, ad engagement, and LTV optimization.

## Monetization Dimensions to Analyze

- **Conversion funnel**: Where do potential payers drop off? What triggers the first purchase?
- **Payer segmentation**: Minnows (<$5), Dolphins ($5-50), Whales ($50+) — behavior differences
- **First purchase triggers**: What in-game events or situations lead to the first IAP?
- **Revenue concentration risk**: How concentrated is revenue in top spenders? (whale dependency)
- **Ad monetization**: Rewarded video engagement, ad frequency tolerance, eCPM trends
- **Purchase timing**: When in the player lifecycle do purchases happen? What events precede them?
- **Bundle and offer effectiveness**: Which offers convert best? Price point analysis
- **LTV by cohort**: How does lifetime value evolve across acquisition cohorts?

## Required Output Format

Respond with ONLY valid JSON (no markdown, no explanations):

```json
{
  "insights": [
    {
      "name": "High-Intent Non-Payers: 1,250 Players Ready to Convert",
      "description": "1,250 players have 10+ sessions, reach level 15+, but never purchased. They show 85% engagement with optional content and 3.2 sessions/week. Based on similar segments, estimated 8-12% would convert with a targeted first-purchase offer at $1.99-$2.99.",
      "severity": "high",
      "affected_count": 1250,
      "risk_score": 0.0,
      "confidence": 0.8,
      "metrics": {
        "opportunity_type": "conversion",
        "segment_size": 1250,
        "estimated_value_per_user": 5.50,
        "total_potential": 6875.00,
        "conversion_estimate": 0.10,
        "current_conversion_rate": 0.0,
        "benchmark_comparison": "Similar engaged segments convert at 8-12%"
      },
      "indicators": [
        "10+ sessions average with 3.2 sessions/week",
        "85% engagement with optional content",
        "Zero purchases to date",
        "Average session duration 14.5 minutes (above median)"
      ],
      "target_segment": "Engaged non-payers with 10+ sessions and level 15+",
      "source_steps": [3, 6]
    }
  ]
}
```

- **source_steps**: List the step numbers from the query results that this insight is based on. Each query result has a "step" field — cite the exact steps.

## Opportunity Types

- **conversion**: Non-payers who show high engagement and could be converted with the right offer
- **upsell**: Existing payers who could increase spend (e.g., upgrade from small to medium packs)
- **retention_revenue**: Revenue at risk from churning payers (whale dependency, declining spender activity)
- **pricing**: Price points that are suboptimal (too high for conversion, too low for revenue capture)
- **ad_optimization**: Rewarded ad improvements (frequency, placement, reward value)
- **first_purchase**: Opportunities to trigger first-ever purchases (starter packs, limited offers)
- **bundle_optimization**: Offer/bundle composition improvements based on purchase patterns
- **whale_risk**: Revenue concentration risk from top spender dependency

## Severity Calibration

- **critical**: Revenue declining or whale churn detected (revenue at risk), OR >$10,000 monthly potential
- **high**: Clear conversion opportunity with strong intent signals, OR $1,000-$10,000 monthly potential
- **medium**: Moderate opportunity requiring more validation, OR $100-$1,000 monthly potential
- **low**: Small optimization or long-term opportunity, OR <$100 monthly potential

## Quality Standards

- **Realistic value estimates**: Base on actual player behavior and comparable segments, not optimistic projections
- **Strong intent signals required**: Don't flag every non-payer — require evidence of engagement, progression, or ad interaction
- **Minimum segment size**: 50+ players for any opportunity
- **CRITICAL — Validate user counts**: segment sizes must be COUNT(DISTINCT user_id)
- **Show your math**: When estimating potential value, explain the calculation (segment_size * conversion_estimate * avg_value)

## Important Rules

1. **Use ONLY data from the queries below** — don't make up numbers
2. **If no clear opportunities found**, return `{"insights": []}`
3. **Revenue at risk is as important as new revenue**: Declining whale activity or payer churn should be flagged
4. **Consider the game's monetization model**: A freemium game has different opportunities than a premium or ad-supported game. Reference the project profile.
5. **Don't report obvious facts as insights**: "Most players don't pay" is not an insight. "Players who watch 3+ rewarded videos have 4x higher conversion rate" IS an insight.

## Query Results

{{QUERY_RESULTS}}

Now analyze the data above and respond with valid JSON.
