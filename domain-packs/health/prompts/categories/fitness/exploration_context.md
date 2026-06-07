# Fitness Tracking & Wellness Platform Context

This is a **fitness and wellness platform** where users follow workout programs, track physical activity, set health goals, and monitor progress over time. Key aspects to explore:

- **Workout behavior patterns**: Users engage through structured workouts, exercise sessions, activity tracking, and training programs. Analyze workout frequency, workout type preferences, intensity distribution, session duration, and consistency over time.
- **Exercise preferences**: Different users prefer different workout types (cardio, strength training, walking, running, cycling, flexibility, recovery). Identify which activities drive the highest engagement, adherence, and long-term participation.
- **Workout intensity trends**: Exercise intensity often changes as users progress through fitness programs. Analyze whether users increase intensity over time, maintain stable activity levels, or experience activity decline and burnout.
- **Rest and recovery behavior**: Rest days are essential for sustainable health improvement. Analyze workout-to-rest ratios, recovery periods, inactivity gaps, and whether excessive activity or insufficient recovery correlates with drop-off.
- **Goal-setting behavior**: Users may create goals around weight loss, step counts, workout frequency, sleep improvement, heart-rate targets, or program completion. Analyze which goal types are most commonly adopted and which are most successfully achieved.
- **Goal completion journey**: Goal achievement depends on consistency and milestone progression. Identify where users abandon goals, which milestones create momentum, and how achievement rates differ across user segments.
- **Milestone progression**: Many fitness programs are structured around daily, weekly, or monthly milestones. Analyze milestone completion rates, progression speed, and common points where users stop advancing.
- **Program effectiveness**: Structured fitness plans should improve adherence and outcomes. Compare users participating in guided programs versus self-directed activity tracking to determine which approaches generate better engagement and success rates.
- **Behavior consistency**: Long-term health improvement depends on habit formation. Analyze streak behavior, adherence trends, recurring workout schedules, and factors associated with sustained participation.

### Fitness Platform Example Queries

> **Important**: Adapt all column names, table names, and SQL functions to match the actual schema. Use `lookup_schema` on the candidate tables before running these queries — column names below are illustrative, not guaranteed.

**Workout Type Preferences**

```sql
SELECT
  workout_type,
  COUNT(DISTINCT user_id) AS users,
  COUNT(*) AS sessions,
  AVG(duration_minutes) AS avg_duration
FROM {{REF:workout_sessions}}
{{FILTER}}
GROUP BY workout_type
ORDER BY sessions DESC
```

**Workout Frequency Distribution**

```sql
SELECT
  workouts_per_week,
  COUNT(DISTINCT user_id) AS users
FROM (
  SELECT
    user_id,
    COUNT(*) AS workouts_per_week
  FROM {{REF:workout_sessions}}
  {{FILTER}}
  GROUP BY user_id
) t
GROUP BY workouts_per_week
ORDER BY workouts_per_week
```

**Workout Intensity Trends**

```sql
SELECT
  program_week,
  AVG(intensity_score) AS avg_intensity,
  COUNT(DISTINCT user_id) AS users
FROM {{REF:workout_sessions}}
{{FILTER}}
GROUP BY program_week
ORDER BY program_week
```

**Goal Completion Rates**

```sql
SELECT
  goal_type,
  COUNT(DISTINCT user_id) AS users,
  AVG(CASE WHEN goal_completed = TRUE THEN 1 ELSE 0 END) AS completion_rate
FROM {{REF:user_goals}}
{{FILTER}}
GROUP BY goal_type
ORDER BY completion_rate DESC
```

**Milestone Progression**

```sql
SELECT
  milestone_level,
  COUNT(DISTINCT user_id) AS users_reached,
  AVG(days_to_reach) AS avg_days
FROM {{REF:user_milestones}}
{{FILTER}}
GROUP BY milestone_level
ORDER BY milestone_level
```

**Rest Day Patterns**

```sql
SELECT
  rest_days_per_week,
  COUNT(DISTINCT user_id) AS users,
  AVG(workout_completion_rate) AS completion_rate
FROM {{REF:user_activity_summary}}
{{FILTER}}
GROUP BY rest_days_per_week
ORDER BY rest_days_per_week
```
