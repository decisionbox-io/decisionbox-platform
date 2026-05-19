//go:build integration

package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	"github.com/decisionbox-io/decisionbox/libs/go-common/auth"
	"github.com/decisionbox-io/decisionbox/libs/go-common/health"
	gomongo "github.com/decisionbox-io/decisionbox/libs/go-common/mongodb"
	gosecrets "github.com/decisionbox-io/decisionbox/libs/go-common/secrets"
	"github.com/decisionbox-io/decisionbox/services/api/database"
	mongoSecrets "github.com/decisionbox-io/decisionbox/providers/secrets/mongodb"
	tcmongo "github.com/testcontainers/testcontainers-go/modules/mongodb"
)

// End-to-end test that the plugin route-group extension point routes
// requests through the full API server pipeline (auth chain, RBAC,
// logging, CORS) into the plugin-supplied handler. The unit-level mux
// tests pin individual functions; this one pins the wiring — registry
// to server.NewWithRouteGroups to a real HTTP request.

var routeGroupsTestDB *database.DB

func TestMain(m *testing.M) {
	ctx := context.Background()
	container, err := tcmongo.Run(ctx, "mongo:7.0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "MongoDB start failed: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = container.Terminate(ctx) }()

	uri, _ := container.ConnectionString(ctx)
	cfg := gomongo.DefaultConfig()
	cfg.URI = uri
	cfg.Database = "route_groups_integ_test"
	client, err := gomongo.NewClient(ctx, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "MongoDB connect failed: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = client.Disconnect(ctx) }()

	routeGroupsTestDB = database.New(client)
	if err := database.InitDatabase(ctx, routeGroupsTestDB); err != nil {
		fmt.Fprintf(os.Stderr, "InitDatabase failed: %v\n", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func TestInteg_RouteGroups_MountedThroughFullServerPipeline(t *testing.T) {
	var hits int32
	pluginMux := http.NewServeMux()
	pluginMux.HandleFunc("GET /api/plugin-integ/ping", func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("X-Plugin-Hit", "yes")
		_, _ = w.Write([]byte("pong"))
	})

	// Boot a minimal server with the route group attached. NoAuth means
	// every route runs as admin, so we don't need to assemble a token.
	secretProvider, err := mongoSecrets.NewMongoProvider(routeGroupsTestDB.Collection("secrets"), "test", "")
	if err != nil {
		t.Fatalf("secret provider: %v", err)
	}
	defer func() { _ = secretProvider }()
	var _ gosecrets.Provider = secretProvider

	authProvider := auth.NewNoAuthProvider()
	healthHandler := health.NewHandler(database.NewMongoHealthChecker(routeGroupsTestDB))

	handler := NewWithRouteGroups(
		routeGroupsTestDB,
		healthHandler,
		secretProvider,
		authProvider,
		nil, // schemaCollectionDropper — not exercised here
		nil, // indexCanceller — not exercised here
		[]RouteGroup{{Prefix: "/api/plugin-integ", Handler: pluginMux}},
	)

	srv := httptest.NewServer(handler)
	defer srv.Close()

	// The plugin route is reachable.
	resp, err := http.Get(srv.URL + "/api/plugin-integ/ping")
	if err != nil {
		t.Fatalf("GET plugin route: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Plugin-Hit"); got != "yes" {
		t.Errorf("plugin handler did not run — X-Plugin-Hit = %q", got)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("plugin handler hit count = %d, want 1", got)
	}

	// CORS still fires (global middleware wraps the auth+RBAC chain that
	// contains the plugin mux), so a preflight OPTIONS gets the standard
	// CORS response — proving the group is mounted INSIDE the global
	// chain, not outside it.
	req, _ := http.NewRequest("OPTIONS", srv.URL+"/api/plugin-integ/ping", nil)
	preflightResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("OPTIONS plugin route: %v", err)
	}
	defer func() { _ = preflightResp.Body.Close() }()
	if preflightResp.Header.Get("Access-Control-Allow-Origin") == "" {
		t.Error("plugin route did not receive CORS headers — group is mounted outside the global middleware chain")
	}

	// An unrelated built-in route is unaffected — the group does not
	// shadow the rest of the API.
	healthResp, err := http.Get(srv.URL + "/api/v1/health")
	if err != nil {
		t.Fatalf("GET /api/v1/health: %v", err)
	}
	defer func() { _ = healthResp.Body.Close() }()
	if healthResp.StatusCode != http.StatusOK {
		t.Errorf("built-in /api/v1/health status = %d after mounting plugin group", healthResp.StatusCode)
	}
}
