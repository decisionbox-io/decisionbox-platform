# Your First Discovery

> **Time:** 15 minutes
> **Prerequisites:** DecisionBox running ([Quick Start](quickstart.md)), a data warehouse with data, an LLM API key

This guide walks through creating a project, configuring it, running a discovery, and understanding the results.

## Step 1: Create a Project

Open **http://localhost:3000** and click **New Project**.

### Basics

- **Name**: A descriptive name (e.g., "Puzzle Quest Analytics")
- **Domain**: Select your industry. Available: **Gaming**, **Social Network**
- **Category**: Select your sub-type. For gaming: **Match-3**, **Idle/Incremental**, **Casual/Hyper-Casual**. For social: **Content Sharing**.

The domain and category determine which analysis areas and prompts are used. For example, a Match-3 game gets churn, engagement, monetization analysis (shared across all games) plus level difficulty and booster usage analysis (specific to match-3). A social network gets growth, engagement, retention analysis (shared) plus content creation and monetization analysis (specific to content sharing).

### Data Warehouse

Select your warehouse provider and enter connection details:

**BigQuery:**
- Project ID: Your GCP project ID (e.g., `my-gcp-project`)
- Location: Dataset location (e.g., `us-central1`, `US`, `us-east5`)
- Datasets: Comma-separated dataset names (e.g., `analytics, features`)

**Amazon Redshift (Serverless):**
- Workgroup Name: Your Redshift Serverless workgroup (e.g., `default-workgroup`)
- Database: Database name (e.g., `dev`)
- Region: AWS region (e.g., `us-east-1`)

**Filter (optional):** If your warehouse has data from multiple apps/tenants, set a filter:
- Filter Field: Column name (e.g., `app_id`)
- Filter Value: Your app's ID (e.g., `my-app-123`)

The agent will add `WHERE app_id = 'my-app-123'` to all queries.

### AI Provider

Select your LLM provider:

| Provider | Model Example | Auth |
|----------|--------------|------|
| Claude (Anthropic) | `claude-sonnet-4-20250514` | API key |
| OpenAI | `gpt-4o` | API key |
| Ollama | `llama3.1:70b` | None (local) |
| Vertex AI | `claude-sonnet-4-20250514` | GCP ADC |
| AWS Bedrock | `us.anthropic.claude-sonnet-4-20250514-v1:0` | AWS credentials |

Type the model name as free text — model lists change frequently, so we don't restrict to a dropdown.

For provider-specific configuration (Vertex AI project ID, Bedrock region), additional fields appear when you select the provider.

### Schedule

Leave **disabled** for now. You'll trigger runs manually. Scheduling can be enabled later in project settings.

Click **Create Project**.

## Step 2: Add Your API Key

After creating the project, go to **Settings** → **Secrets** tab.

1. Select **LLM API Key** from the dropdown
2. Enter your API key
3. Click **Save Secret**

If your warehouse requires credentials (e.g., BigQuery from outside GCP):
1. Select **Warehouse Credentials (SA Key JSON)**
2. Paste your service account JSON key
3. Click **Save Secret**

## Step 3: Fill in Your Profile (Optional but Recommended)

Go to **Settings** → **Profile** tab.

The profile tells the AI about your product — its genre, mechanics, target audience, monetization model. This context dramatically improves insight quality.

For a match-3 game, you'd fill in:
- **Basic Info**: Genre, platforms, target audience
- **Gameplay**: Core mechanic (match3), session type, difficulty curve
- **Monetization**: Model (freemium/ad-supported), has IAP, has ads
- **Boosters**: Name, description, consumable/permanent, purchasable
- **IAP Packages**: Name, SKU, price, contents
- **KPIs**: Target retention rates, ARPU, DAU

The form is generated from the domain pack's JSON Schema — different domains have different fields.

## Step 4: Run Discovery

Click **Run discovery** in the top bar.

The dropdown lets you configure:
- **Exploration steps**: More steps = more comprehensive (default: 100). Start with 50-100 for your first run.
- **Estimate cost**: Check this to see estimated LLM and warehouse costs before running.
- **Run All Areas**: Runs all analysis areas (churn, engagement, monetization, etc.)
- **Select areas**: Run only specific analysis areas.

Click **Run All Areas** to start.

### What Happens During a Run

The live progress panel shows each step:

1. **Schema Discovery** — The agent lists your warehouse tables and reads their schemas
2. **Exploration** — The AI writes SQL queries, executes them, analyzes results, and writes more queries based on what it finds. Each step shows:
   - What the AI is thinking
   - The SQL query it wrote
   - Row count and execution time
   - Whether the query was auto-fixed (SQL errors are retried with corrections)
3. **Analysis** — For each analysis area (churn, engagement, etc.), the AI reviews all exploration results and generates insights
4. **Validation** — Each insight's affected count is verified against the warehouse
5. **Recommendations** — Based on all validated insights, the AI generates specific action steps

## Step 5: Review Results

Click the completed discovery card to see the full results.

### Insights

Insights are displayed in a table with:

- **Name**: Specific finding (e.g., "Day 0-to-Day 1 Drop: 67% Never Return")
- **Severity**: Critical, High, Medium, or Low
- **Area**: Which analysis area found this (churn, engagement, etc.)
- **Players Affected**: How many users are impacted
- **Confidence**: How confident the AI is (based on data quality and validation)

Click an insight name to see:
- Full description with exact numbers
- Key indicators (specific metrics)
- Risk score and confidence
- Validation results (claimed vs. verified count)
- The actual SQL queries that discovered this insight

### Recommendations

Each recommendation includes:
- **Title**: Specific action (e.g., "Send Extra Lives After 3 Failures on Level 42")
- **Impact estimate**: Expected improvement (e.g., "+15-20% retention")
- **Effort**: Low, Medium, or High
- **Target segment**: Exact criteria for who to target
- **Action steps**: Numbered implementation steps
- **Related insights**: Which insights this recommendation addresses

### Feedback

Use the thumbs up/down buttons on insights and recommendations:
- **Like**: Tells the agent this finding is valuable — it will monitor for changes
- **Dislike**: Tells the agent to avoid similar conclusions — add a comment explaining why

Feedback is used in subsequent runs. The agent won't repeat disliked insights and will track liked ones for changes.

## Step 6: Run Again

After reviewing results and providing feedback, run another discovery. The agent will:
- **Not repeat** previously found insights (unless data changed)
- **Avoid** patterns you disliked
- **Monitor** insights you liked for changes
- **Focus** on new patterns and unexplored areas

Each run builds on previous context, making discoveries more targeted over time.

## Next Steps

- [Configuring LLM Providers](../guides/configuring-llm.md) — Detailed setup for each provider
- [Customizing Prompts](../guides/customizing-prompts.md) — Edit how the AI reasons about your data
- [Project Profiles](../guides/project-profiles.md) — Improve insight quality with context
- [Architecture](../concepts/architecture.md) — Understand how the system works
