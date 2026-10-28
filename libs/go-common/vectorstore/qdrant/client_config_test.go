package qdrant

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// Every DecisionBox Qdrant connection is built from ClientConfig, so the
// connection settings carry through.
func TestClientConfig(t *testing.T) {
	cfg := ClientConfig("qdrant", 6334, "key", true)
	if cfg.Host != "qdrant" || cfg.Port != 6334 || cfg.APIKey != "key" || !cfg.UseTLS {
		t.Errorf("connection settings not carried: %+v", cfg)
	}
}

// A Qdrant channel must hand its target straight to the dialer instead of
// resolving it with gRPC's DNS resolver, whose service-config TXT lookup —
// and, once any linked package enables grpclb, its SRV lookup — stalled every
// new connection by 8s on a Docker Compose network.
//
// The target here exists only for the dialer: gRPC's DNS resolver cannot
// resolve it, so the channel becomes ready only if nothing but the dialer
// looks the name up.
func TestClientConfig_DialerResolvesTheTarget(t *testing.T) {
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()

	const target = "qdrant-only-the-dialer-knows.invalid:6334"
	dialed := make(chan string, 4)
	opts := append([]grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
			dialed <- addr
			return lis.DialContext(ctx)
		}),
	}, ClientConfig("qdrant-only-the-dialer-knows.invalid", 6334, "", false).GrpcOptions...)

	conn, err := grpc.NewClient(target, opts...)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer conn.Close()
	conn.Connect()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for s := conn.GetState(); s != connectivity.Ready; s = conn.GetState() {
		if !conn.WaitForStateChange(ctx, s) {
			t.Fatalf("channel never became ready (last state %v): the target went through gRPC's DNS resolver", s)
		}
	}
	if got := <-dialed; got != target {
		t.Errorf("dialer got %q, want the target unresolved (%q)", got, target)
	}
}
