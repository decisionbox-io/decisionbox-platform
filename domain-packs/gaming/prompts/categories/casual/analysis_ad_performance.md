# Ad Performance Analysis

You are a gaming analytics expert analyzing ad monetization patterns in a casual or hyper-casual game. Your goal is to identify the optimal balance between ad revenue and user retention — the point where ad frequency maximizes total lifetime ad revenue without driving players away.

## Context

**Dataset**: {{DATASET}}
**Exploration Queries**: {{TOTAL_QUERIES}}

## Your Task

Analyze the query results and identify **ad performance patterns** — revenue opportunities, tolerance thresholds, format effectiveness, and retention impact.

## What to Look For

- **Ad tolerance threshold**: At what ad frequency does retention start dropping significantly? This is the most important metric for ad-supported games.
- **Format effectiveness**: Rewarded ads vs interstitial vs banner — which generate the most revenue per impression? Which have the lowest churn impact?
- **Rewarded ad opt-in rates**: What percentage of players voluntarily watch rewarded ads? What rewards drive the highest opt-in?
- **eCPM trends**: Are eCPMs stable, rising, or declining? By platform (iOS vs Android), by country, by ad network?
- **Ad fatigue**: Do players who see many ads gradually reduce engagement? At what point does fatigue set in?
- **Session-level impact**: How do ads placed at different session points (beginning, mid-session, between core loops, end) affect session duration and return rate?
- **Revenue per user segmentation**: How does ARPDAU (average revenue per daily active user) vary by player segment, platform, and geography?
- **Non-ad-watchers**: What percentage of players never watch a rewarded ad? Are they being served too many interstitials to compensate?

## Required Output Format

Respond with ONLY valid JSON:

```json
{
  "insights": [
    {
      "name": "Ad Tolerance Cliff at 6+ Interstitials Per Session",
      "description": "Players shown 6+ interstitial ads per session have 42% lower D1 retention (18%) compared to players shown 3-5 ads (31%). Players shown 1-2 ads retain at 38%. The revenue gain from high-frequency ads (+$0.03 ARPDAU) is offset by a 45% drop in D7 retention, resulting in 28% lower lifetime ad revenue per user. 3,200 players are currently in the high-frequency group.",
      "severity": "critical",
      "affected_count": 3200,
      "risk_score": 0.42,
      "confidence": 0.9,
      "metrics": {
        "pattern_type": "tolerance_threshold",
        "threshold_ads_per_session": 6,
        "retention_below_threshold": 0.31,
        "retention_above_threshold": 0.18,
        "retention_drop_percent": -42.0,
        "arpdau_below_threshold": 0.08,
        "arpdau_above_threshold": 0.11,
        "estimated_ltv_impact": -0.28
      },
      "indicators": [
        "D1 retention drops from 31% (3-5 ads) to 18% (6+ ads) — a 42% decline",
        "D7 retention drops from 14% to 7.7% — a 45% decline",
        "ARPDAU increase from high-frequency ads: only +$0.03",
        "Estimated lifetime revenue loss: -28% per user due to faster churn",
        "3,200 players currently in high-frequency ad group"
      ],
      "target_segment": "Players shown 6 or more interstitial ads per session",
      "source_steps": [2, 5, 8]
    }
  ]
}
```

- **source_steps**: List the step numbers from the query results that this insight is based on. Each query result has a "step" field — cite the exact steps.

## Pattern Types

- **tolerance_threshold**: The ad frequency point where retention drops significantly (the most critical finding)
- **format_mismatch**: Using the wrong ad format for the context (e.g., interstitials between core loops instead of rewarded)
- **rewarded_opportunity**: High-value rewarded ad placements with low current opt-in rates
- **ecpm_decline**: eCPM trends declining over time or for specific segments
- **revenue_concentration**: Revenue overly dependent on a specific geo, platform, or ad network
- **fatigue_pattern**: Gradual decline in ad engagement over player lifetime
- **segment_gap**: Significant ARPDAU differences between player segments (platform, geo, etc.)

## Severity Calibration

- **critical**: Ad frequency directly causing measurable churn (>20% retention drop), or eCPM declining >15%
- **high**: Significant revenue optimization opportunity (>10% LTV improvement possible), or retention risk from ad format issues
- **medium**: Moderate optimization in ad placement, timing, or format selection
- **low**: Minor eCPM or opt-in rate optimization

## Important Rules

1. **Use ONLY data from the queries** — don't make up numbers
2. **If no ad patterns found**, return `{"insights": []}`
3. **CRITICAL**: affected_count = COUNT(DISTINCT user_id)
4. **Always consider BOTH sides**: Ad frequency increases short-term ARPDAU but may decrease LTV. Report the net impact.
5. **Platform differences matter**: iOS and Android often have very different eCPMs and ad tolerance levels. Report separately if data shows >15% difference.
6. **Think in terms of LTV**: Short-term revenue per session matters less than lifetime revenue. A player who returns for 30 days with 3 ads/session generates more than a player who churns after 3 days with 8 ads/session.

## Query Results

{{QUERY_RESULTS}}

Now analyze and respond with valid JSON.
