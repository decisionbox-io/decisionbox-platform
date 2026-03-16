# Project Profiles

> **Version**: 0.1.0

Project profiles provide domain-specific context to the AI. A well-filled profile dramatically improves insight quality — the AI understands your specific product instead of making generic assumptions.

## What Profiles Do

The profile is serialized to JSON and injected into every prompt via `{{PROFILE}}` in `base_context.md`. The AI uses this to:

- Understand your product's mechanics, monetization model, and goals
- Reference specific features by name (e.g., "Magnet booster" instead of "some power-up")
- Compare metrics against your KPI targets
- Focus on areas relevant to your business model

## How Profiles Work

1. The **domain pack** provides a JSON Schema defining profile fields
2. The **dashboard** renders a dynamic form from the schema
3. The **user** fills in their product details
4. The **agent** injects the profile into prompts as `{{PROFILE}}`

## Gaming Profile Fields

### Base Fields (All Gaming Categories)

**Basic Info:**
- Genre (puzzle, fps, strategy, rpg, casual, etc.)
- Sub-genre (e.g., "Match-3", "Battle Royale")
- Platforms (iOS, Android, Web, PC, Console)
- Target audience (e.g., "Adult Female 30-65+")
- Game description

**Gameplay:**
- Core mechanic (match3, hidden_object, shooter, etc.)
- Game type (level_based, open_world, session_based, endless)
- Session type (short under 5min, medium 5-15min, long 15min+)
- Average session duration (minutes)
- Complexity (low, medium, high)
- Difficulty curve (gradual, steep, flat, wave)

**Monetization:**
- Model (freemium, premium, ad_supported, subscription)
- Has ads, has IAP, has subscription (boolean toggles)

**Target KPIs:**
- D1 retention target (%)
- D7 retention target (%)
- D30 retention target (%)
- ARPU target ($)
- DAU target
- Session length target (minutes)

### Match-3 Specific Fields

**Progression:**
- Progression type (linear, branching, open)
- Total levels
- Levels per chapter
- Star rating system (boolean + max stars)

**Boosters** (repeatable items):
Each booster has: name, description, usage type (consumable/permanent/timed), starting amount, purchasable, earnable.

**IAP Packages** (repeatable items):
Each package has: name, SKU, type (consumable/non-consumable/subscription), price (USD), contents (array of {item, count}).

**Lootboxes** (repeatable items):
Each has: name, rarity (common/rare/epic/legendary), possible rewards (list of strings).

**Retention Features:**
- Daily rewards (boolean)
- Streak bonus (boolean + max streak days)

## Filling in Profiles

### Via Dashboard

1. Go to **Settings → Profile** tab
2. Fill in each section
3. For repeatable items (boosters, IAP packages), click **+** to add items
4. Click **Save Settings**

### Via API

Profiles are part of the project update:

```bash
curl -X PUT http://localhost:8080/api/v1/projects/{id} \
  -H "Content-Type: application/json" \
  -d '{
    "profile": {
      "basic_info": {
        "genre": "puzzle",
        "sub_genre": "match-3",
        "platforms": ["iOS", "Android"],
        "target_audience": "Adult Female 30-65+",
        "description": "A casual match-3 puzzle game with home decoration"
      },
      "gameplay": {
        "core_mechanic": "match3",
        "game_type": "level_based",
        "session_type": "short",
        "avg_session_duration_minutes": 6,
        "complexity": "low",
        "difficulty_curve": "gradual"
      },
      "monetization": {
        "model": "freemium",
        "has_ads": true,
        "has_iap": true,
        "has_subscription": false
      },
      "kpis": {
        "retention_d1_target": 40,
        "retention_d7_target": 20,
        "retention_d30_target": 8,
        "arpu_target": 0.50,
        "dau_target": 5000
      },
      "boosters": [
        {"name": "Magnet", "description": "Collects same-color tiles", "usage": "consumable", "starting_amount": 3, "can_purchase": true, "can_earn": true},
        {"name": "Hammer", "description": "Removes one tile", "usage": "consumable", "starting_amount": 2, "can_purchase": true, "can_earn": false}
      ],
      "iap_packages": [
        {"name": "Starter Pack", "sku": "starter_v1", "type": "consumable", "price_usd": 0.99, "contents": [{"item": "Magnet", "count": 5}, {"item": "Hammer", "count": 3}]}
      ]
    }
  }'
```

## Impact on Insights

Without profile:
> "There appears to be a retention issue at some early levels."

With profile:
> "Level 11 Difficulty Cliff: Success Rate Drops from 63% to 34.2% — this exceeds your gradual difficulty curve design. At 22.2% quit rate, you're losing players before they reach your first IAP offer at Level 15. Your Magnet booster (starting_amount: 3) is depleted by Level 8, leaving players without tools for the difficulty spike."

The AI references specific game mechanics, booster names, monetization context, and KPI targets from the profile.

## Profile Schema (For Domain Pack Authors)

Profiles use [JSON Schema](https://json-schema.org/) (draft 2020-12). The dashboard renders forms dynamically from the schema.

Supported field types:
- `string` → TextInput
- `string` with `enum` → Select dropdown
- `integer` / `number` → NumberInput
- `boolean` → Checkbox
- `array` of strings → Comma-separated TextInput
- `array` of strings with `enum` → MultiSelect
- `array` of objects → Repeatable card editor (ArrayOfObjectsEditor)
- Nested `array` of objects → Inline row editor (InlineArrayEditor)

See [Creating Domain Packs](creating-domain-packs.md) for how to write profile schemas.

## Next Steps

- [Customizing Prompts](customizing-prompts.md) — Edit how the AI uses profile data
- [Creating Domain Packs](creating-domain-packs.md) — Define profiles for your domain
- [Your First Discovery](../getting-started/first-discovery.md) — End-to-end walkthrough
