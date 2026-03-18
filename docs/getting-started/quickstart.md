# Quick Start

> **Time:** 5 minutes
> **Prerequisites:** Docker and Docker Compose installed

This guide gets DecisionBox running locally with Docker Compose. By the end, you'll have the dashboard open and ready to create your first project.

## 1. Clone and Start

```bash
git clone https://github.com/decisionbox-io/decisionbox-platform.git
cd decisionbox-platform

# Start all services
docker compose up -d
```

This starts three containers:

| Service | Port | Description |
|---------|------|-------------|
| MongoDB | 27017 | Database (projects, discoveries, secrets) |
| API | 8080 | REST API (not exposed publicly — dashboard proxies to it) |
| Dashboard | 3000 | Web UI |

Wait about 10 seconds for all services to start, then open **http://localhost:3000**.

## 2. Create a Project

Click **New Project** and fill in:

1. **Basics** — Project name, domain (e.g., Gaming or Social Network), category (e.g., Match-3, Idle, Casual, Content Sharing)
2. **Data Warehouse** — Select your warehouse provider and enter connection details
3. **AI Provider** — Select your LLM provider and enter model name
4. **Schedule** — Leave disabled for now (you'll trigger runs manually)

Click **Create Project**.

## 3. Add Your API Key

Go to **Settings** (left sidebar) → **Secrets** tab.

1. Select **LLM API Key** from the dropdown
2. Paste your API key (e.g., `sk-ant-...` for Claude, `sk-...` for OpenAI)
3. Click **Save Secret**

The key is encrypted and stored per-project. It's never exposed in full via the API.

## 4. Run Your First Discovery

Click the **Run discovery** button in the top bar. The AI agent will:

1. Discover your warehouse table schemas
2. Autonomously write and execute SQL queries
3. Analyze results to find patterns
4. Validate findings against your data
5. Generate actionable recommendations

You can watch the progress live — each step shows what the AI is thinking, what SQL it wrote, and what it found.

## 5. Review Results

When the run completes, click the discovery card to see:

- **Insights** — Severity-ranked findings with confidence scores
- **Recommendations** — Specific action steps with impact estimates
- **Transparency** — Every SQL query the AI ran, its reasoning, and validation results

## Next Steps

- [Installation Guide](installation.md) — Other ways to install (binary, from source)
- [Your First Discovery](first-discovery.md) — Detailed walkthrough with explanations
- [Configuring LLM Providers](../guides/configuring-llm.md) — Set up Claude, OpenAI, Vertex AI, Bedrock, or Ollama
- [Configuring Warehouses](../guides/configuring-warehouse.md) — BigQuery or Redshift setup details

## Stopping

```bash
# Stop all services (data preserved)
docker compose down

# Stop and remove all data
docker compose down -v
```
