# Domain Packs

> **Version**: 0.4.0

Domain packs are DecisionBox's extensibility model. They define **what** the AI looks for and **how** it reasons about data for a specific industry. Without a domain pack, DecisionBox wouldn't know whether to look for churn patterns, cart abandonment rates, or supply chain bottlenecks.

## Available Domain Packs

| Domain | Categories | Base Areas | Description |
|--------|-----------|------------|-------------|
| **Gaming** | Match-3, Idle/Incremental, Casual/Hyper-Casual | Churn, Engagement, Monetization | Player behavior, retention, and revenue analytics for games |
| **Social Network** | Content Sharing | Growth, Engagement, Retention | User growth, engagement, content creation, and monetization analytics for social platforms |
| **Ecommerce** | Multi-Category | Conversion, Revenue, Retention | Purchase funnel, revenue & pricing, customer retention, product performance, and session behavior analytics for online stores |
| **System Test** | Quick, Standard, Thorough | Connectivity, Schema Discovery | Diagnostic pack for warehouse validation and data profiling (not an industry pack) |

> **Note:** The System Test domain pack is intended for testing and onboarding only.
> It is hidden by default and requires setting `DECISIONBOX_ENABLE_SYSTEM_TEST=true` to enable.

## What's in a Domain Pack

A domain pack provides four things:

| Component | What it does | Format |
|-----------|-------------|--------|
| **Categories** | Sub-types within a domain | JSON (stored in MongoDB) |
| **Analysis Areas** | What patterns to find | JSON (stored in MongoDB) |
| **Prompts** | How the AI reasons | Markdown content (stored in MongoDB) |
| **Profile Schema** | What context users provide | JSON Schema (stored in MongoDB) |

## Three-Level Hierarchy

```
Domain: Gaming
├── Category: Match-3
│   ├── Area: Churn Risks          (base — shared)
│   ├── Area: Engagement Patterns  (base — shared)
│   ├── Area: Monetization         (base — shared)
│   ├── Area: Level Difficulty     (match-3 specific)
│   └── Area: Booster Usage        (match-3 specific)
│
├── Category: Idle / Incremental
│   ├── Area: Churn Risks          (base — shared)
│   ├── Area: Engagement Patterns  (base — shared)
│   ├── Area: Monetization         (base — shared)
│   ├── Area: Progression & Prestige  (idle specific)
│   └── Area: Economy Balance         (idle specific)
│
└── Category: Casual / Hyper-Casual
    ├── Area: Churn Risks          (base — shared)
    ├── Area: Engagement Patterns  (base — shared)
    ├── Area: Monetization         (base — shared)
    ├── Area: Ad Performance       (casual specific)
    └── Area: Session Flow         (casual specific)

Domain: Social Network
└── Category: Content Sharing
    ├── Area: Growth & Activation     (base — shared)
    ├── Area: Engagement Patterns     (base — shared)
    ├── Area: Retention & Churn       (base — shared)
    ├── Area: Content Creation Health  (content sharing specific)
    └── Area: Monetization & Premium   (content sharing specific)

Domain: Ecommerce
└── Category: Multi-Category
    ├── Area: Conversion Funnel           (base — shared)
    ├── Area: Revenue & Pricing           (base — shared)
    ├── Area: Customer Retention          (base — shared)
    ├── Area: Product & Category Performance  (multi-category specific)
    └── Area: Session & Browsing Behavior     (multi-category specific)
```

**Base areas** are shared across all categories in a domain. **Category-specific areas** add specialized analysis. When you select "Gaming / Match-3", you get all base gaming areas PLUS match-3 specific areas.

## Storage

Domain packs are stored as JSON documents in the MongoDB `domain_packs` collection.
Each document contains all categories, analysis areas, prompt content, and profile schemas for the domain.
There is no Go code per pack and no filesystem dependency.

The `domain-packs/` directory in the repository still contains the raw prompt and profile data files used to build the embedded seed JSON, but these files are not read at runtime.

## Areas Definition (areas.json)

Each `areas.json` defines which analysis areas are available and maps them to prompt files.

**Example** — Gaming base areas (`prompts/base/areas.json`):
```json
[
  {
    "id": "churn",
    "name": "Churn Risks",
    "description": "Players at risk of leaving the game — identify churn patterns across the player lifecycle",
    "keywords": ["churn", "retention", "cohort", "day_", "d1_", "d7_", "d30_", "inactive", "lapsed"],
    "priority": 1,
    "prompt_file": "analysis_churn.md"
  }
]
```

**Example** — Social base areas (`prompts/base/areas.json`):
```json
[
  {
    "id": "growth",
    "name": "Growth & Activation",
    "description": "User acquisition funnel, signup conversion, onboarding completion, and viral growth loops",
    "keywords": ["signup", "registration", "onboarding", "activation", "invite", "referral", "viral"],
    "priority": 1,
    "prompt_file": "analysis_growth.md"
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

The same merging logic applies to all domain packs and categories.

**Project-level overrides:** Users can edit any prompt per-project via the dashboard's Prompts page. Overrides are stored in MongoDB and take priority over domain pack files.

## Profile Schema

The profile schema defines what context users provide about their product. It's a [JSON Schema](https://json-schema.org/) that the dashboard renders as a dynamic form.

### Gaming Profile

**Base schema** (`profiles/schema.json`) — Fields shared across all gaming categories:
- Basic info (genre, platforms, target audience)
- Gameplay (core mechanic, session type, difficulty curve)
- Monetization (model, has ads, has IAP, primary revenue source)
- Social features (guilds, leaderboards, PvP, chat)
- Live ops (daily rewards, seasonal events, battle pass)
- KPIs (retention targets, ARPU target, DAU target)

**Category extensions** add domain-specific fields:
- **Match-3** — Progression (levels, star system), boosters, IAP packages, lootboxes
- **Idle** — Prestige system, currencies, generators, ad boosts
- **Casual** — Core loop, onboarding, ad configuration, secondary features

### Social Network Profile

**Base schema** — Fields shared across all social categories:
- Platform info (type, content format, target audience, growth stage)
- Engagement model (feed type, stories, messaging, groups, connection model)
- Monetization (premium subscriptions, IAP features, virtual currency, creator monetization, paid messaging, paid content, ads)
- Growth features (referral, contact sync, push notifications)
- KPIs (DAU/MAU ratio, retention targets, creator ratio, premium conversion, ARPU)

**Category extensions:**
- **Content Sharing** — Content types, discovery features, interaction types (with paid flags), creator tools, moderation

### Ecommerce Profile

**Base schema** — Fields shared across all ecommerce categories:
- Business info (industry, business model, target market, platforms, growth stage)
- Product catalog (total products, average price, categories, inventory model)
- Shipping (free shipping threshold, average delivery days, return rate)
- Payment (payment methods, currencies)
- KPIs (conversion rate, AOV, 30-day retention, CAC, LTV)

**Category extensions:**
- **Multi-Category** — Catalog details (total products, total categories, top categories, private label), search & discovery features, cross-sell/upsell strategy

The schemas are merged at runtime (base + category). The resulting form lets users describe their specific product, which the AI uses as context for better analysis.

## How Domain Packs Are Loaded

```
API startup
  ↓
Check MongoDB `domain_packs` collection
  ↓
If empty → seed built-in packs from embedded JSON
  ↓
domain_packs collection
  ├── gaming      (published, built-in)
  ├── social      (published, built-in)
  ├── ecommerce   (published, built-in)
  ├── system-test (unpublished, built-in)
  └── ...         (user-created packs)
```

On first startup, the API seeds the `domain_packs` collection with built-in packs (gaming, ecommerce, social) from embedded JSON.
The system-test pack is also seeded but marked as unpublished by default.

Domain packs are managed entirely through the dashboard or API:
- **Dashboard**: Navigate to `/domain-packs` to create, edit, duplicate, or delete packs
- **API**: CRUD endpoints at `/api/v1/domain-packs`
- **Import/Export**: `POST /api/v1/domain-packs/import` and `GET /api/v1/domain-packs/{slug}/export` use a portable JSON format (`decisionbox-domain-pack`)

The agent does not read domain packs directly.
When a project is created, the selected domain pack's prompts, areas, and profile schema are copied into the project configuration.
The agent reads only from `project.prompts` at runtime.

## Creating Your Own

See the [Creating Domain Packs](../guides/creating-domain-packs.md) guide for a step-by-step tutorial on building a domain pack for your industry.

## Next Steps

- [Providers](providers.md) — Plugin architecture for LLM, warehouse, and secrets
- [Prompts](prompts.md) — Template variables and prompt customization
- [Creating Domain Packs](../guides/creating-domain-packs.md) — Build your own
