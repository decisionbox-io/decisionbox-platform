# Domain Packs

> **Version**: 0.1.0

Domain packs are DecisionBox's extensibility model. They define **what** the AI looks for and **how** it reasons about data for a specific industry. Without a domain pack, DecisionBox wouldn't know whether to look for churn patterns, cart abandonment rates, or supply chain bottlenecks.

## What's in a Domain Pack

A domain pack provides four things:

| Component | What it does | File type |
|-----------|-------------|-----------|
| **Categories** | Sub-types within a domain | Go code |
| **Analysis Areas** | What patterns to find | JSON + Markdown |
| **Prompts** | How the AI reasons | Markdown files |
| **Profile Schema** | What context users provide | JSON Schema |

## Three-Level Hierarchy

```
Domain: Gaming
├── Category: Match-3 (shipped)
│   ├── Area: Churn Risks          (base — shared)
│   ├── Area: Engagement Patterns  (base — shared)
│   ├── Area: Monetization         (base — shared)
│   ├── Area: Level Difficulty     (match-3 specific)
│   └── Area: Booster Usage        (match-3 specific)
│
├── Category: FPS (future)
│   ├── Area: Churn Risks          (base — shared)
│   ├── Area: Engagement Patterns  (base — shared)
│   ├── Area: Monetization         (base — shared)
│   ├── Area: Weapon Balance       (FPS specific)
│   └── Area: Map Performance      (FPS specific)
│
└── Category: Strategy (future)
    ├── Area: Churn Risks          (base — shared)
    ├── Area: Engagement Patterns  (base — shared)
    ├── Area: Monetization         (base — shared)
    ├── Area: Resource Economy     (strategy specific)
    └── Area: Unit Balance         (strategy specific)
```

**Base areas** are shared across all categories in a domain. **Category-specific areas** add specialized analysis. When you select "Gaming / Match-3", you get all base gaming areas PLUS match-3 specific areas.

## File Structure

```
domain-packs/gaming/
├── go/
│   ├── pack.go                    # Registers the pack, implements interfaces
│   ├── discovery.go               # Categories, analysis areas, prompts loading
│   └── *_test.go                  # Tests
│
├── prompts/
│   ├── base/                      # Shared across ALL gaming categories
│   │   ├── areas.json             # Base analysis area definitions
│   │   ├── base_context.md        # Shared context (profile, previous results)
│   │   ├── exploration.md         # Main exploration system prompt
│   │   ├── analysis_churn.md      # Churn pattern analysis prompt
│   │   ├── analysis_engagement.md # Engagement pattern analysis prompt
│   │   ├── analysis_monetization.md # Monetization analysis prompt
│   │   └── recommendations.md     # Recommendation generation prompt
│   │
│   └── categories/
│       └── match3/                # Match-3 specific
│           ├── areas.json         # Additional areas (levels, boosters)
│           ├── exploration_context.md  # Appended to base exploration prompt
│           ├── analysis_levels.md     # Level difficulty analysis
│           └── analysis_boosters.md   # Booster usage analysis
│
└── profiles/
    ├── schema.json                # Base gaming profile (JSON Schema)
    └── categories/
        └── match3.json            # Match-3 extensions (boosters, IAP packages, etc.)
```

## Areas Definition (areas.json)

Each `areas.json` defines which analysis areas are available and maps them to prompt files.

**Base areas** (`prompts/base/areas.json`):
```json
[
  {
    "id": "churn",
    "name": "Churn Risks",
    "description": "Players at risk of leaving the game",
    "keywords": ["churn", "retention", "cohort", "day_", "d1_", "d7_", "d30_", "inactive", "lapsed"],
    "priority": 1,
    "prompt_file": "analysis_churn.md"
  },
  {
    "id": "engagement",
    "name": "Engagement Patterns",
    "description": "Player behavior and session trends",
    "keywords": ["session", "engagement", "duration", "frequency", "active", "dau", "mau", "playtime"],
    "priority": 2,
    "prompt_file": "analysis_engagement.md"
  },
  {
    "id": "monetization",
    "name": "Monetization Opportunities",
    "description": "Revenue optimization and conversion opportunities",
    "keywords": ["purchase", "iap", "revenue", "payer", "currency", "spend", "arpu", "ltv", "conversion"],
    "priority": 3,
    "prompt_file": "analysis_monetization.md"
  }
]
```

**Category-specific areas** (`prompts/categories/match3/areas.json`):
```json
[
  {
    "id": "levels",
    "name": "Level Difficulty",
    "description": "Difficulty spikes and frustration points in level progression",
    "keywords": ["level", "quit", "success", "difficulty", "fail", "attempt", "stage", "star"],
    "priority": 4,
    "prompt_file": "analysis_levels.md"
  },
  {
    "id": "boosters",
    "name": "Booster Usage",
    "description": "Power-up usage patterns, depletion risks, and purchase opportunities",
    "keywords": ["booster", "hint", "magnet", "power", "extra_life", "hammer", "consumable"],
    "priority": 5,
    "prompt_file": "analysis_boosters.md"
  }
]
```

### Field Reference

| Field | Required | Description |
|-------|----------|-------------|
| `id` | Yes | Unique identifier (lowercase, no spaces). Used in API, prompts, insights. |
| `name` | Yes | Human-readable display name. |
| `description` | Yes | What this analysis area looks for. Shown in the dashboard. |
| `keywords` | Yes | Keywords to match exploration results with this area. The agent filters queries by these keywords when feeding data to the analysis prompt. |
| `priority` | Yes | Execution order (1 = first). Also controls display order in the UI. |
| `prompt_file` | Yes | Filename of the analysis prompt (relative to the areas.json directory). |

## How Prompts Are Merged

When the agent loads prompts for a project with domain=gaming, category=match3:

```
1. Load base exploration prompt:
   prompts/base/exploration.md

2. Append category context (if exists):
   + prompts/categories/match3/exploration_context.md

3. Load base context:
   prompts/base/base_context.md

4. Load analysis prompts:
   Base areas:
     churn     → prompts/base/analysis_churn.md
     engagement → prompts/base/analysis_engagement.md
     monetization → prompts/base/analysis_monetization.md
   Category areas:
     levels    → prompts/categories/match3/analysis_levels.md
     boosters  → prompts/categories/match3/analysis_boosters.md

5. Load recommendations prompt:
   prompts/base/recommendations.md
```

**Project-level overrides:** Users can edit any prompt per-project via the dashboard's Prompts page. Overrides are stored in MongoDB and take priority over domain pack files.

## Profile Schema

The profile schema defines what context users provide about their product. It's a [JSON Schema](https://json-schema.org/) that the dashboard renders as a dynamic form.

**Base schema** (`profiles/schema.json`) — Fields shared across all gaming categories:
- Basic info (genre, platforms, target audience)
- Gameplay (core mechanic, session type, difficulty curve)
- Monetization (model, has ads, has IAP)
- KPIs (retention targets, ARPU target, DAU target)

**Category extensions** (`profiles/categories/match3.json`) — Additional fields for match-3 games:
- Progression (total levels, star system)
- Boosters (name, type, starting amount, purchasable)
- IAP packages (name, SKU, price, contents)
- Lootboxes (name, rarity, possible rewards)
- Retention features (daily rewards, streak bonus)

The schemas are merged at runtime (base + category). The resulting form lets users describe their specific product, which the AI uses as context for better analysis.

## How Domain Packs Are Loaded

```
DOMAIN_PACK_PATH environment variable (default: /app/domain-packs)
  ↓
domain-packs/
  gaming/                    ← domain pack directory
    go/pack.go               ← registers via domainpack.Register("gaming", ...)
    prompts/                 ← read at runtime via DOMAIN_PACK_PATH
    profiles/                ← read at runtime via DOMAIN_PACK_PATH
```

The Go code reads prompt and profile files from the filesystem at runtime (not embedded at compile time). This means:
- You can edit prompts without recompiling
- Docker images bake prompts into `/app/domain-packs/`
- In development, `DOMAIN_PACK_PATH` points to the repo's `domain-packs/` directory

## Creating Your Own

See the [Creating Domain Packs](../guides/creating-domain-packs.md) guide for a step-by-step tutorial on building a domain pack for your industry.

## Next Steps

- [Providers](providers.md) — Plugin architecture for LLM, warehouse, and secrets
- [Prompts](prompts.md) — Template variables and prompt customization
- [Creating Domain Packs](../guides/creating-domain-packs.md) — Build your own
