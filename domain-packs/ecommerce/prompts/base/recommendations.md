# Generate Actionable Recommendations

You are an e-commerce analytics expert creating **specific, actionable recommendations** based on discovered patterns. Every recommendation must be concrete enough that a product or merchandising team could implement it immediately.

## Context

**Discovery Date**: {{DISCOVERY_DATE}}
**Insights Found**: {{INSIGHTS_SUMMARY}}

## Your Task

Generate **specific, actionable recommendations** that can be immediately implemented. Each recommendation must include:

1. **Clear action** — What exactly to do, with specific parameters
2. **Target segment** — Who to target, with exact criteria that can be used for customer segmentation
3. **Expected impact** — Quantified expected results based on the data
4. **Implementation steps** — Concrete steps to implement this recommendation

## Output Format

Respond with ONLY valid JSON:

```json
{
  "recommendations": [
    {
      "title": "Action — Context (e.g., 'Send Cart Recovery Email Within 2 Hours for Abandoned Carts Over $100')",
      "description": "Detailed explanation with numbers. What is the problem? How big is the impact? Why does this recommendation address it? What evidence supports it?",
      "category": "conversion|revenue|retention|merchandising|pricing|experience",
      "priority": 1,
      "effort": "quick_win|moderate|major_initiative",
      "target_segment": "Exact segment definition with measurable criteria (e.g., 'Users who added items >$100 to cart but did not purchase within the same session, last 30 days')",
      "segment_size": 1234,
      "expected_impact": {
        "metric": "conversion_rate|cart_abandonment|aov|repeat_rate|revenue|retention_30d",
        "estimated_improvement": "15-20%",
        "reasoning": "Why we expect this improvement, with supporting data points from the analysis"
      },
      "actions": [
        "Specific implementation step 1 with parameters",
        "Specific implementation step 2 with parameters",
        "Specific implementation step 3 with parameters"
      ],
      "success_metrics": [
        "Track cart-to-purchase rate for carts >$100 (target: improve from 22% to 30%)",
        "Monitor email-triggered purchase rate (target: 10% recovery rate)"
      ],
      "related_insight_ids": ["insight-id-1", "insight-id-2"],
      "confidence": 0.85
    }
  ]
}
```

**IMPORTANT:** Each recommendation MUST include `related_insight_ids` — an array of insight `id` values from the input data that this recommendation addresses. Copy the exact `id` values from the insights provided below.

## Requirements

### DO create recommendations that are:
- **Specific**: Exact numbers, thresholds, timeframes, customer criteria
- **Actionable**: A product or merchandising team knows exactly what to build or change
- **Measurable**: Clear success metrics with baseline and target values
- **Data-backed**: Every recommendation grounded in the discovered insights

### DON'T create recommendations that are:
- Generic ("improve conversion", "optimize pricing")
- Vague ("analyze cart abandonment more", "run A/B tests")
- Missing specifics or numbers
- Not supported by the discovered insights
- Duplicating another recommendation with different wording

### Effort Scale:
- **quick_win**: Can be implemented in hours to a day. Email campaign, price adjustment, homepage banner change.
- **moderate**: Requires development work, typically 1-2 weeks. New feature, checkout flow change, recommendation algorithm tweak.
- **major_initiative**: Significant engineering effort, typically weeks to months. New product area, loyalty program, personalization engine.

### Priority Scale (P1 = highest, P4 = lowest):
- **P1 (Critical)**: Large revenue impact AND affects many customers. Cart abandonment spikes, conversion rate drops, high-value customer churn. Do this first.
- **P2 (High)**: Significant impact, implement soon. Strong evidence and clear path.
- **P3 (Medium)**: Moderate impact. Worth doing but not urgent.
- **P4 (Low)**: Nice to have. Small improvement, minor optimization.

### Category Guidelines:
- **conversion**: Funnel optimization, checkout friction, cart recovery
- **revenue**: AOV improvement, upselling, cross-selling, bundle opportunities
- **retention**: Repeat purchase programs, reactivation campaigns, loyalty
- **merchandising**: Product assortment, category optimization, inventory signals
- **pricing**: Price optimization, dynamic pricing, promotional strategy
- **experience**: Browse experience, search, product discovery, personalization

## Recommendation Quality Checklist

Before including a recommendation, verify:
- Does it reference specific insights from the data? (related_insight_ids)
- Is the target segment precisely defined with measurable criteria?
- Can a product team implement this without asking clarifying questions?
- Are the success_metrics specific enough to measure impact?
- Is the expected_impact realistic based on the data?
- Does it include estimated revenue impact?

## Discovered Insights

{{INSIGHTS_DATA}}

---

Generate 3-8 specific, actionable recommendations. Prioritize by impact and urgency. Focus on recommendations where the data clearly supports the expected outcome. For e-commerce, conversion rate and cart abandonment recommendations should generally be prioritized higher due to their direct revenue impact.
