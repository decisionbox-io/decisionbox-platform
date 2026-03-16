# Configuring Data Warehouses

> **Version**: 0.1.0

DecisionBox connects to your existing data warehouse in read-only mode. This guide covers setup for each supported warehouse.

## Google BigQuery

### Prerequisites

- A GCP project with BigQuery datasets
- One of:
  - **On GCP**: Application Default Credentials (Workload Identity, gcloud auth)
  - **Outside GCP**: A service account JSON key

### Dashboard Setup

1. Select **Google BigQuery** as warehouse provider
2. Fill in:
   - **Project ID**: Your GCP project ID (e.g., `my-gcp-project`)
   - **Location**: Dataset location (e.g., `US`, `us-central1`, `us-east5`)
3. Enter **Datasets**: Comma-separated (e.g., `analytics, features_prod`)
4. Optionally set **Filter**: field + value for multi-tenant data

### Authentication

**On GCP (GKE, Cloud Run, Compute Engine):**

No additional setup. BigQuery uses Application Default Credentials automatically.

**Outside GCP (local dev, AWS, Azure):**

1. Create a service account in GCP Console with `BigQuery Data Viewer` and `BigQuery Job User` roles
2. Download the JSON key
3. In DecisionBox: **Settings → Secrets → Warehouse Credentials (SA Key JSON)**
4. Paste the entire JSON key content

The agent reads this credential from the secret provider and uses it to authenticate with BigQuery.

### Multi-Dataset Support

BigQuery projects can have multiple datasets. List all datasets you want the agent to explore:

```
Datasets: events_prod, features_prod, analytics
```

The agent discovers table schemas from all listed datasets and can query across them.

### Filtering

For shared datasets with data from multiple apps/tenants:

```
Filter Field: app_id
Filter Value: 68a42f378e3b227c8e41b0e5
```

The agent adds `WHERE app_id = '68a42f378e3b227c8e41b0e5'` to all queries.

### Cost

BigQuery charges per TB scanned (default: $7.50/TB for on-demand pricing). The cost estimation feature uses BigQuery's dry-run API to preview costs before running.

## Amazon Redshift

### Prerequisites

- A Redshift cluster (provisioned) or Redshift Serverless workgroup
- AWS credentials with Redshift Data API access

### Dashboard Setup — Serverless

1. Select **Amazon Redshift** as warehouse provider
2. Fill in:
   - **Workgroup Name**: Your Serverless workgroup (e.g., `default-workgroup`)
   - **Database**: Database name (e.g., `dev`)
   - **Region**: AWS region (e.g., `us-east-1`)
3. Enter **Datasets**: Schema names (e.g., `public`)

### Dashboard Setup — Provisioned

1. Select **Amazon Redshift** as warehouse provider
2. Fill in:
   - **Cluster Identifier**: Your cluster ID (e.g., `my-redshift-cluster`)
   - **Database**: Database name
   - **Region**: AWS region
3. Enter **Datasets**: Schema names

### Authentication

**On AWS (EKS, EC2, Lambda):**

No additional setup. Uses IAM role / instance profile automatically.

**Outside AWS (local dev, GCP, Azure):**

1. Configure AWS credentials:
   ```bash
   aws configure
   # Or set environment variables:
   export AWS_ACCESS_KEY_ID=AKIA...
   export AWS_SECRET_ACCESS_KEY=...
   export AWS_REGION=us-east-1
   ```

2. Ensure the IAM user/role has these permissions:
   - `redshift-data:ExecuteStatement`
   - `redshift-data:DescribeStatement`
   - `redshift-data:GetStatementResult`
   - `redshift-serverless:GetCredentials` (for Serverless)
   - Or: `redshift:GetClusterCredentials` (for Provisioned)

### How Redshift Queries Work

DecisionBox uses the **Redshift Data API** (not JDBC), which works asynchronously:

1. `ExecuteStatement` — Submit SQL
2. `DescribeStatement` — Poll until complete
3. `GetStatementResult` — Fetch results

This means no JDBC driver is needed, and it works with both Serverless and Provisioned clusters.

### Data Type Handling

Redshift types are automatically normalized:
- `INTEGER`, `BIGINT`, `SMALLINT` → `INT64`
- `VARCHAR`, `TEXT`, `CHAR` → `STRING`
- `DECIMAL`, `NUMERIC` → `FLOAT64` (parsed from column metadata, not string guessing)
- `BOOLEAN` → `BOOL`
- `TIMESTAMP`, `TIMESTAMPTZ` → `TIMESTAMP`

### System Table Filtering

The agent automatically excludes Redshift system tables from discovery:
- `pg_*` tables
- `stl_*` tables (system log)
- `svv_*` tables (system views)

## Cross-Cloud Authentication

DecisionBox supports accessing warehouses from a different cloud:

| Scenario | How |
|----------|-----|
| BigQuery from AWS | Store GCP SA key JSON in secret provider |
| Redshift from GCP | Configure AWS credentials on the machine |
| Any from local dev | Configure cloud CLI (`gcloud auth`, `aws configure`) |

The key concept: warehouse credentials can be stored in the **secret provider** (`Settings → Secrets → Warehouse Credentials`). The agent reads credentials from the secret provider before initializing the warehouse provider.

## Next Steps

- [Configuration Reference](../reference/configuration.md) — All environment variables
- [Configuring Secrets](configuring-secrets.md) — Secret provider setup
- [Adding Warehouse Providers](adding-warehouse-providers.md) — Support a new warehouse
