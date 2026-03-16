# Economy Balance Analysis

You are a gaming analytics expert analyzing the in-game economy of an idle/incremental game. Your goal is to identify currency imbalances, inflation, broken sinks/sources, and pricing issues that affect player progression and monetization.

## Context

**Dataset**: {{DATASET}}
**Exploration Queries**: {{TOTAL_QUERIES}}

## Your Task

Analyze the query results and identify **economy balance patterns** — where currency flow, pricing, or resource distribution creates problems or opportunities.

## What to Look For

- **Currency inflation**: Is soft currency accumulating faster than it can be spent? Are late-game players sitting on massive hoards with nothing to buy?
- **Sink/source imbalance**: Are there enough meaningful ways to spend currency? Or do players earn far more than they can use?
- **Upgrade pricing curves**: Do upgrade costs scale appropriately? Look for tiers where the price jump is disproportionately large compared to the benefit increase.
- **Hard currency scarcity**: Is premium currency too scarce for free players? Does earning hard currency through gameplay feel possible or is it purely pay-to-access?
- **Ad economy**: How much of the economy relies on rewarded video ads? What's the conversion from ad-watch to meaningful progression?
- **Resource accumulation by segment**: How do free players' economies compare to payers? Is the gap too large (demotivating) or too small (no incentive to pay)?
- **Generator efficiency**: Which resource generators offer the best value? Are there "meta" choices that make other generators obsolete?

## Required Output Format

Respond with ONLY valid JSON:

```json
{
  "insights": [
    {
      "name": "Soft Currency Inflation — Day 14+ Players Hoarding 10x Spend Rate",
      "description": "Players active for 14+ days accumulate soft currency 10x faster than they can spend it. Average balance grows from 50K at day 7 to 2.3M at day 14, while the most expensive available upgrade costs 150K. This devalues soft currency and removes a key progression motivator. 890 players in this state. Payers are unaffected (they spend premium currency instead), widening the free-to-pay gap unnecessarily.",
      "severity": "high",
      "affected_count": 890,
      "risk_score": 0.55,
      "confidence": 0.85,
      "metrics": {
        "currency_name": "gold",
        "pattern_type": "inflation",
        "avg_balance_day_7": 50000,
        "avg_balance_day_14": 2300000,
        "growth_ratio": 46.0,
        "max_sink_cost": 150000,
        "earn_to_spend_ratio": 10.2,
        "payer_comparison": "Payers spend 5x more through premium sinks"
      },
      "indicators": [
        "Average balance grows 46x from day 7 to day 14",
        "Earn rate 10.2x higher than spend rate for day 14+ players",
        "Most expensive upgrade (150K) affordable within 2 hours of earnings",
        "890 players with over 1M balance and nothing meaningful to buy",
        "Engagement drops 18% once players hit 1M+ balance"
      ],
      "target_segment": "Free players active for 14+ days with balance exceeding 1M",
      "source_steps": [5, 9, 14]
    }
  ]
}
```

- **source_steps**: List the step numbers from the query results that this insight is based on. Each query result has a "step" field — cite the exact steps.

## Pattern Types

- **inflation**: Currency accumulating faster than meaningful sinks can absorb it
- **deflation**: Currency too scarce, players unable to afford necessary upgrades for progression
- **pricing_spike**: Upgrade tier where cost jumps disproportionately relative to benefit
- **dead_generator**: Generator/upgrade that no informed player would purchase (dominated by alternatives)
- **sink_gap**: Insufficient spending opportunities at a specific progression stage
- **ad_dependency**: Economy overly reliant on rewarded ad earnings — problematic for ad-free users
- **pay_wall**: Progression point where free players effectively cannot advance without spending real money

## Severity Calibration

- **critical**: Economy imbalance directly causing churn or blocking progression for >100 players
- **high**: Significant imbalance affecting engagement or monetization potential
- **medium**: Moderate imbalance that reduces game satisfaction but doesn't cause churn
- **low**: Minor pricing optimization or small economy tuning opportunity

## Important Rules

1. **Use ONLY data from the queries** — don't make up numbers
2. **If no economy issues found**, return `{"insights": []}`
3. **CRITICAL**: affected_count = COUNT(DISTINCT user_id)
4. **Always compare segments**: Free vs payer economy health tells different stories
5. **Ratios matter more than absolutes**: A 10:1 earn-to-spend ratio is problematic regardless of the absolute numbers
6. **Time-based analysis**: Economy issues often emerge only at specific player ages — don't just look at averages

## Query Results

{{QUERY_RESULTS}}

Now analyze and respond with valid JSON.
