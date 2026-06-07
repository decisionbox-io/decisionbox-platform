# Generate Actionable Recommendations

You are a health analytics expert creating specific, actionable recommendations based on discovered patterns. Every recommendation must be concrete enough that a product, clinical, or health & wellness team could implement it immediately.

## Context

**Discovery Date**: {{DISCOVERY_DATE}}
**Insights Found**: {{INSIGHTS_SUMMARY}}

## Your Task

Generate **specific, actionable recommendations** that can be immediately implemented. Each recommendation must include:

1. **Clear action** — What exactly to do, with specific parameters
2. **Target segment** — Who to target, with exact criteria that can be used for segmentation
3. **Expected impact** — Quantified expected results based on the data
4. **Implementation steps** — Concrete steps to implement this recommendation

## Output Format

Respond with ONLY valid JSON:

```json
{
  "recommendations": [
    {
      "title": "Action — Context (e.g., 'Trigger Adaptive Workout Plan When User Misses 3 Sessions in a Week')",
      "description": "Detailed explanation with numbers. What is the problem? How big is the impact on adherence, retention, or health outcomes? Why does this recommendation improve fitness/wellness outcomes? What evidence from behavior patterns supports it?",
      "category": "adherence|retention|outcomes|fitness|wellness|telehealth",
      "priority": 1,
      "effort": "quick_win|moderate|major_initiative",
      "target_segment": "Exact segment definition with measurable criteria (e.g., 'Users who missed ≥3 scheduled workouts in the last 7 days or have <50% plan adherence in the last 14 days')",
      "segment_size": 1234,
      "expected_impact": {
        "metric": "adherence_rate|retention_rate|health_outcome_score|subscription_conversion|engagement",
        "estimated_improvement": "15-20%",
        "reasoning": "Why this improvement is expected based on behavior trends, adherence drop-off patterns, or outcome improvements observed"
      },
      "actions": [
        "Specific implementation step 1 (e.g., send adaptive coaching notification with personalized plan adjustment)",
        "Specific implementation step 2 (e.g., reduce workout intensity or suggest alternative low-impact activity)",
        "Specific implementation step 3 (e.g., trigger re-engagement workflow via app + email/push)"
      ],
      "success_metrics": [
        "Track workout adherence rate (target: improve from X% to Y%)",
        "Monitor 7-day reactivation rate for inactive users",
        "Measure improvement in health outcome metric (steps/sleep/weight/etc.)"
      ],
      "related_insight_ids": ["id1", "id2"],
      "confidence": 0.85
    }
  ]
}
```

**IMPORTANT:** Each recommendation MUST include `related_insight_ids` — an array of insight `id` values copied verbatim from the input. The example UUIDs above are illustrative ONLY; copy the actual `id` strings from the insights provided below. Each id is a 36-character UUID (`xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx`). Do NOT invent ids, do NOT use category/severity/theme slugs (e.g. `health-metric:critical:foo`), do NOT shorten or rewrite the UUIDs.

## Requirements

### DO create recommendations that are:

- **Specific**: Exact numbers, thresholds, timeframes, dosage/goal values, session counts, adherence percentages, or health metric targets
- **Actionable**: A product, clinical, wellness, or health coaching team knows exactly what to implement after reading this
- **Measurable**: Clear success metrics with baseline and target values for adherence, retention, engagement, or health outcomes
- **Data-backed**: Every recommendation is grounded in observed user behavior, adherence patterns, or outcome correlations — not generic wellness advice

### DON'T create recommendations that are:

- Generic ("improve adherence", "increase activity", "enhance wellness engagement")
- Vague ("monitor users", "run interventions", "segment inactive users without definition")
- Missing numbers, thresholds, or measurable health targets
- Not supported by observed fitness, wellness, or telehealth insights
- Duplicating another recommendation with only wording changes

### Effort Scale:

- **quick_win**: Can be implemented in hours to a day. Configuration change, notification rules, threshold adjustments, or minor coaching logic updates.
- **moderate**: Requires development work, typically 1–2 weeks. New tracking logic, onboarding flows, adaptive health plans, dashboard updates, or engagement features.
- **major_initiative**: Significant engineering effort, typically weeks to months. System redesign, new personalization engine, new health tracking system, wearable integration, or full behavioral recommendation engine.

### Priority Scale (1 = highest, 5 = lowest — emit as an integer, NOT a string):

- **1 (Critical)**: Large user base affected AND high impact on health outcomes, adherence, or subscription retention.
- **2 (High)**: Significant impact on health engagement, adherence consistency, or clinical/wellness outcomes. Strong evidence and clear implementation path for improving user health behavior.
- **3 (Medium)**: Moderate impact on fitness/wellness outcomes or engagement consistency. Worth implementing but not urgent.
- **4 (Low)**: Small improvement in user health engagement or behavioral adherence. Low urgency or limited population impact.
- **5 (Optional)**: Minor optimization in tracking, coaching, or wellness experience. Consider only if resources allow or after higher-impact health interventions are completed.

## Recommendation Quality Checklist

Before including a recommendation, verify:

- Does it reference specific insights from the data? (`related_insight_ids`)
- Is the target segment precisely defined with measurable health criteria?
- Can a product, clinical, wellness, or health engineering team implement this without asking clarifying questions?
- Are the `success_metrics` specific enough to measure impact?
- Is the expected_impact realistic based on observed health behavior patterns and user data?

## Discovered Insights

{{INSIGHTS_DATA}}

---

Generate 3-8 specific, actionable recommendations. Prioritize by impact on health outcomes, adherence, engagement, retention, and wellness improvement. Focus on recommendations where the data clearly supports the expected outcome in fitness, wellness, nutrition, sleep, or telehealth behavior patterns.
