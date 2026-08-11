# Multi-warehouse live E2E (#161, Phase 2)

Exercises the multi-datasource ask-serve `ProjectRuntime` against **three live,
read-only SQL datasources** to validate the Phase 2 claims unit tests can only
fake — one warehouse per statement, agent-orchestrated multi-hop with a bounded
crossed value set, per-datasource dialect execution, and read-only enforcement
end-to-end.

| datasource   | infra                                                        | role in the test |
|--------------|--------------------------------------------------------------|------------------|
| `redshift`   | Redshift Serverless `dbx-discovery` (TICKIT sample), via its PostgreSQL wire endpoint as the read-only `dbx_ro` user | ticket sales — the 172k-row `sales` table |
| `rnacentral` | public RNAcentral genomics Postgres (`hh-pgsql-public.ebi.ac.uk`, `reader`) | a third heterogeneous read-only datasource |
| `crm`        | local CRM Postgres (testcontainer), seeded from `crm_seed.sql` | shares `userid` with TICKIT + a `flagged` column; reuses `public.users` so its qualified name collides with TICKIT's |

The test (`internal/askserve/e2e_multiwarehouse_test.go`, build tag
`e2e_multiwarehouse`) drives the real runtime with the deterministic scripted
LLM and asserts: `query_data(datasource_id)` runs each statement against the
named datasource; the multi-hop turn chains a bounded set (top-10 buyer ids)
from Redshift into a CRM filter and returns exactly the expected flags; the
turn records `routed_datasource_ids = [redshift, crm]`; and a write against the
read-only CRM user is rejected with "permission denied".

> Note on the Redshift driver: DecisionBox's `redshift` driver uses the
> redshift-data API + IAM (mapping to the caller's DB user), which cannot use a
> password-based read-only user. The E2E therefore connects to Redshift through
> its PostgreSQL wire protocol via the `postgres` driver as `dbx_ro` — a real
> Redshift connection with real read-only creds. The redshift-data driver path
> is covered by its own unit tests.

## One-time provisioning

```bash
EP=dbx-discovery.435998721348.us-east-1.redshift-serverless.amazonaws.com
# superuser temp creds (IAM identity must be a Redshift superuser)
CREDS=$(aws redshift-serverless get-credentials --workgroup-name dbx-discovery --db-name dev --duration-seconds 3600 --output json)
DBUSER=$(echo "$CREDS" | python3 -c 'import sys,json;print(json.load(sys.stdin)["dbUser"])')
DBPASS=$(echo "$CREDS" | python3 -c 'import sys,json;print(json.load(sys.stdin)["dbPassword"])')
RO_PW='<choose a password>'

# load TICKIT + create the read-only dbx_ro user
docker run --rm -i -e PGPASSWORD="$DBPASS" postgres:16-alpine \
  psql -h "$EP" -p 5439 -U "$DBUSER" -d dev -v ON_ERROR_STOP=1 -v ro_password="$RO_PW" < redshift_seed.sql

# save the RO connection details where the test reads them (keep OUT of git)
cat > /home/dev/.e2e_multiwarehouse.env <<EOF
export E2E_REDSHIFT_HOST="$EP"
export E2E_REDSHIFT_PORT="5439"
export E2E_REDSHIFT_DB="dev"
export E2E_REDSHIFT_USER="dbx_ro"
export E2E_REDSHIFT_PASSWORD="$RO_PW"
EOF
```

The COPY in `redshift_seed.sql` needs the namespace's copy role to be able to
read `s3://redshift-downloads/*` (a one-time inline policy on `dbx-redshift-copy`).

## Run

```bash
source /home/dev/.e2e_multiwarehouse.env
export PATH=$PATH:$(go env GOPATH)/bin
cd services/agent
go test -tags e2e_multiwarehouse -run TestE2E_MultiWarehouse -v ./internal/askserve/
```

Skips cleanly when `E2E_REDSHIFT_*` is unset. Requires Docker (CRM container +
ryuk) and outbound egress to Redshift + RNAcentral. CRM is created and torn down
per run; TICKIT + `dbx_ro` persist on the workgroup (re-running `redshift_seed.sql`
is idempotent). Pause/stop the serverless workgroup when done to save cost.
