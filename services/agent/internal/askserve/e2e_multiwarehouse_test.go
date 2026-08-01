//go:build e2e_multiwarehouse

// Package askserve — live multi-warehouse E2E (#161 Phase 2).
//
// Exercises the real multi-datasource ask-serve ProjectRuntime against THREE
// live, read-only SQL datasources on real infrastructure:
//
//   - redshift   — Redshift Serverless (TICKIT sample) via its PostgreSQL wire
//     endpoint, connected as the read-only dbx_ro user. Real cloud
//     warehouse, real 172k-row sales table.
//   - rnacentral — the public RNAcentral genomics Postgres (read-only reader).
//   - crm        — a local CRM Postgres (testcontainer) sharing `userid` with
//     TICKIT and carrying a `flagged` column. Deliberately reuses
//     `public.users` so its qualified name COLLIDES with TICKIT's,
//     proving per-warehouse isolation at execution time.
//
// It validates the Phase 2 claims that unit tests can only fake: query_data
// routes each statement to the named datasource and executes it there; a turn
// chains a bounded value set across two datasources (top-N TICKIT buyers →
// their CRM flags); read-only credentials actually block writes end-to-end; and
// the runtime records which datasources answered.
//
// Run (Redshift creds come from the gitignored env file the seed wrote):
//
//	source /home/dev/.e2e_multiwarehouse.env
//	go test -tags e2e_multiwarehouse -run TestE2E_MultiWarehouse -v ./internal/askserve/
//
// Skips cleanly when E2E_REDSHIFT_* is unset. Requires Docker (for CRM) +
// outbound egress to Redshift + RNAcentral.
package askserve

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"

	_ "github.com/decisionbox-io/decisionbox/providers/warehouse/postgres" // registers the "postgres" driver
)

// crmROPassword is a throwaway password for the ephemeral CRM testcontainer's
// read-only role — not a secret (the container is torn down after the test).
const crmROPassword = "crm_ro_pw"

// The top-10 TICKIT buyers by spend are stable (fixed sample data). The CRM seed
// flags userid%7==0 plus {240,3286,16391,5636}; against these ten that flags
// exactly {240,1239,3286,16391,5636}.
var topTICKITBuyers = []int{4303, 240, 1239, 3286, 16391, 9355, 13673, 16449, 5636, 5149}
var expectedFlagged = map[int]bool{240: true, 1239: true, 3286: true, 16391: true, 5636: true}

func pgConfig(host, port, database, user, password, schema, sslmode string) gowarehouse.ProviderConfig {
	return gowarehouse.ProviderConfig{
		"host": host, "port": port, "database": database, "user": user,
		"credentials_json": password, "dataset": schema, "sslmode": sslmode,
	}
}

// buildConn opens a read-only warehouse connection through the postgres driver
// and wraps it in the self-healing executor, exactly as ask_serve.go's
// warehouseBuild does (minus the SQL fixer — the E2E's SQL is dialect-correct).
func buildConn(t *testing.T, label string, cfg gowarehouse.ProviderConfig) *WarehouseConn {
	t.Helper()
	prov, err := gowarehouse.NewProvider("postgres", cfg)
	if err != nil {
		t.Fatalf("%s: NewProvider: %v", label, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := prov.ValidateReadOnly(ctx); err != nil {
		_ = prov.Close()
		t.Fatalf("%s: ValidateReadOnly (is the datasource reachable + read-only?): %v", label, err)
	}
	return &WarehouseConn{
		Executor: queryexec.NewQueryExecutor(queryexec.QueryExecutorOptions{Warehouse: prov, MaxRetries: 1}),
		Closers:  []func() error{prov.Close},
	}
}

// startCRM spins a Postgres testcontainer, applies crm_seed.sql, and creates a
// read-only dbx_ro role. Returns the datasource config + a cleanup func.
func startCRM(t *testing.T) (gowarehouse.ProviderConfig, func()) {
	t.Helper()
	ctx := context.Background()
	c, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("crm"),
		tcpostgres.WithUsername("crmadmin"),
		tcpostgres.WithPassword("crmadmin"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start CRM postgres container: %v", err)
	}
	cleanup := func() { _ = c.Terminate(ctx) }

	host, _ := c.Host(ctx)
	mapped, _ := c.MappedPort(ctx, "5432")
	admin := fmt.Sprintf("host=%s port=%s user=crmadmin password=crmadmin dbname=crm sslmode=disable", host, mapped.Port())

	db, err := sql.Open("postgres", admin)
	if err != nil {
		cleanup()
		t.Fatalf("open CRM admin: %v", err)
	}
	defer func() { _ = db.Close() }()

	seed, err := os.ReadFile("../../testdata/e2e_multiwarehouse/crm_seed.sql")
	if err != nil {
		cleanup()
		t.Fatalf("read crm_seed.sql: %v", err)
	}
	stmts := append(splitSQL(string(seed)), // schema + 20k rows
		"CREATE ROLE dbx_ro LOGIN PASSWORD '"+crmROPassword+"'",
		"GRANT USAGE ON SCHEMA public TO dbx_ro",
		"GRANT SELECT ON ALL TABLES IN SCHEMA public TO dbx_ro",
		"REVOKE CREATE ON SCHEMA public FROM PUBLIC", // make dbx_ro genuinely read-only
	)
	for _, s := range stmts {
		if _, err := db.ExecContext(ctx, s); err != nil {
			cleanup()
			t.Fatalf("CRM seed stmt failed: %v\n---\n%s", err, s)
		}
	}
	return pgConfig(host, mapped.Port(), "crm", "dbx_ro", crmROPassword, "public", "disable"), cleanup
}

// splitSQL splits a script into individual statements on ';'. Line comments are
// stripped first so a ';' inside a `--` comment doesn't split a statement (the
// E2E seed has no semicolons inside string literals, so this is safe here).
func splitSQL(script string) []string {
	var b strings.Builder
	for _, line := range strings.Split(script, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	var out []string
	for _, raw := range strings.Split(b.String(), ";") {
		if s := strings.TrimSpace(raw); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// e2eRuntime builds a 3-datasource ProjectRuntime driven by the scripted LLM
// (deterministic JSON-text actions), backed by the three real connections.
func e2eRuntime(t *testing.T, responses []string, conns map[string]*WarehouseConn) *ProjectRuntime {
	client, _ := ai.New(&scriptedProvider{responses: responses}, "e2e-model")
	return NewProjectRuntime(ProjectRuntimeOptions{
		AIClient:  client,
		Model:     "e2e-model",
		PrimaryID: "redshift",
		Datasources: []DatasourceInfo{
			{ID: "redshift", Label: "Ticket Sales (Redshift/TICKIT)", Dialect: "redshift", Datasets: []string{"public"}, Description: "event ticket sales — sales(buyerid,pricepaid), users(userid)"},
			{ID: "rnacentral", Label: "Genomics (RNAcentral)", Dialect: "postgres", Datasets: []string{"rnacen"}, Description: "public RNA sequence database"},
			{ID: "crm", Label: "CRM", Dialect: "postgres", Datasets: []string{"public"}, Description: "CRM accounts — users(userid, flagged)"},
		},
		Build: func(_ context.Context, id string) (*WarehouseConn, error) {
			if c, ok := conns[id]; ok {
				return c, nil
			}
			return nil, fmt.Errorf("no connection for datasource %q", id)
		},
	})
}

func TestE2E_MultiWarehouse_RoutingAndMultiHop(t *testing.T) {
	host := os.Getenv("E2E_REDSHIFT_HOST")
	if host == "" {
		t.Skip("E2E_REDSHIFT_* not set — source /home/dev/.e2e_multiwarehouse.env to run")
	}

	// --- three real read-only datasources ---
	crmCfg, crmCleanup := startCRM(t)
	defer crmCleanup()

	conns := map[string]*WarehouseConn{
		"redshift": buildConn(t, "redshift", pgConfig(host, envOr("E2E_REDSHIFT_PORT", "5439"),
			os.Getenv("E2E_REDSHIFT_DB"), os.Getenv("E2E_REDSHIFT_USER"), os.Getenv("E2E_REDSHIFT_PASSWORD"), "public", "require")),
		// EBI's public Postgres has no SSL; it's a public read-only demo DB.
		"rnacentral": buildConn(t, "rnacentral", pgConfig("hh-pgsql-public.ebi.ac.uk", "5432",
			"pfmegrnargs", "reader", "NWDMCE5xdipIjRrp", "rnacen", "disable")),
		"crm": buildConn(t, "crm", crmCfg),
	}
	for _, c := range conns {
		defer closeConn(c)
	}

	// --- 1. single-datasource routing: a genomics query hits RNAcentral ---
	t.Run("routes_to_named_datasource", func(t *testing.T) {
		store := runE2E(t, e2eRuntime(t, []string{
			`{"query":"select count(*) as n from rnacen.rnc_database","datasource_id":"rnacentral"}`,
			`{"answer":"counted the genomics databases"}`,
		}, conns), TurnRequest{TurnID: "e2e-1", SessionID: "s", ProjectID: "p", Question: "how many genomics databases?"})

		requireDone(t, store)
		if len(store.events) != 1 || store.events[0].Error != "" {
			t.Fatalf("expected one successful RNAcentral query, got %+v", store.events)
		}
		if got := store.final.RoutedDatasourceIDs; len(got) != 1 || got[0] != "rnacentral" {
			t.Fatalf("RoutedDatasourceIDs = %v, want [rnacentral]", got)
		}
	})

	// --- 2. multi-hop: top-N TICKIT buyers (Redshift) → their CRM flags ---
	t.Run("multi_hop_across_datasources", func(t *testing.T) {
		inList := joinInts(topTICKITBuyers)
		store := runE2E(t, e2eRuntime(t, []string{
			`{"query":"select buyerid, sum(pricepaid) as spend from sales group by buyerid order by spend desc limit 10","datasource_id":"redshift"}`,
			`{"query":"select userid, flagged from public.users where userid in (` + inList + `) order by userid","datasource_id":"crm"}`,
			`{"answer":"cross-referenced the top buyers against CRM flags"}`,
		}, conns), TurnRequest{TurnID: "e2e-2", SessionID: "s", ProjectID: "p", Question: "which of the top 10 ticket buyers are flagged in CRM?"})

		requireDone(t, store)
		if len(store.events) != 2 || store.events[0].Error != "" || store.events[1].Error != "" {
			t.Fatalf("expected two successful queries (redshift then crm), got %+v", store.events)
		}
		// Datasource attribution on each event + the turn's routing telemetry.
		if ds := store.events[0].Args["datasource_id"]; ds != "redshift" {
			t.Errorf("hop 1 datasource = %v, want redshift", ds)
		}
		if ds := store.events[1].Args["datasource_id"]; ds != "crm" {
			t.Errorf("hop 2 datasource = %v, want crm", ds)
		}
		if got := store.final.RoutedDatasourceIDs; len(got) != 2 || got[0] != "redshift" || got[1] != "crm" {
			t.Fatalf("RoutedDatasourceIDs = %v, want [redshift crm]", got)
		}
		// The CRM hop's real result must carry exactly the expected flags.
		flagged := flaggedFromPreview(t, store.events[1])
		if len(flagged) != len(topTICKITBuyers) {
			t.Fatalf("CRM returned %d of %d buyers", len(flagged), len(topTICKITBuyers))
		}
		for uid, want := range map[int]bool(mergeExpected()) {
			if flagged[uid] != want {
				t.Errorf("userid %d flagged=%v, want %v (real CRM data mismatch)", uid, flagged[uid], want)
			}
		}
	})

	// --- 3. read-only enforcement is real, end-to-end through the executor ---
	t.Run("read_only_blocks_writes", func(t *testing.T) {
		store := runE2E(t, e2eRuntime(t, []string{
			`{"query":"create table e2e_should_fail(i int)","datasource_id":"crm"}`,
			`{"decline":"cannot write"}`,
		}, conns), TurnRequest{TurnID: "e2e-3", SessionID: "s", ProjectID: "p", Question: "make a table"})

		if len(store.events) != 1 || store.events[0].Error == "" {
			t.Fatalf("a write against a read-only datasource must error, got %+v", store.events)
		}
		if !strings.Contains(strings.ToLower(store.events[0].Error), "permission denied") &&
			!strings.Contains(strings.ToLower(store.events[0].Error), "read-only") {
			t.Fatalf("write rejection should cite a permission/read-only error, got %q", store.events[0].Error)
		}
	})
}

// --- helpers ---

func runE2E(t *testing.T, rt *ProjectRuntime, req TurnRequest) *fakeStore {
	t.Helper()
	store := &fakeStore{}
	cfg := Config{MaxRounds: 8, MaxQueriesPerTurn: 6, MaxFetchRows: 1000, PreviewRows: 50, QueryTimeout: 60 * time.Second}
	(&runner{cfg: cfg, store: store}).run(context.Background(), rt, req)
	if store.final == nil {
		t.Fatal("turn did not finalize")
	}
	return store
}

func requireDone(t *testing.T, store *fakeStore) {
	t.Helper()
	if store.final.Status != commonmodels.AskTurnStatusDone {
		t.Fatalf("status = %q (%s), want done", store.final.Status, store.final.Error)
	}
}

// flaggedFromPreview reads userid→flagged out of a query tool event's summary.
func flaggedFromPreview(t *testing.T, ev commonmodels.ToolEvent) map[int]bool {
	t.Helper()
	sum, ok := ev.Output.(QuerySummary)
	if !ok {
		t.Fatalf("event output is not a QuerySummary: %T", ev.Output)
	}
	out := make(map[int]bool, len(sum.Preview))
	for _, row := range sum.Preview {
		uid := toIntVal(row["userid"])
		out[uid] = toBoolVal(row["flagged"])
	}
	return out
}

func mergeExpected() map[int]bool {
	m := make(map[int]bool, len(topTICKITBuyers))
	for _, uid := range topTICKITBuyers {
		m[uid] = expectedFlagged[uid] // false unless in the flagged set
	}
	return m
}

func joinInts(xs []int) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = fmt.Sprintf("%d", x)
	}
	return strings.Join(parts, ",")
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func toIntVal(v any) int {
	switch n := v.(type) {
	case int64:
		return int(n)
	case int:
		return n
	case float64:
		return int(n)
	default:
		return 0
	}
}

func toBoolVal(v any) bool {
	switch b := v.(type) {
	case bool:
		return b
	case string:
		return b == "t" || b == "true"
	default:
		return false
	}
}
