package qdrant

import "testing"

// Every DecisionBox Qdrant connection is built from ClientConfig, so the
// connection settings carry through and the dial options that keep a new
// channel from stalling on DNS are always present. Measured on a Docker
// Compose network: creating a client took 8s (gRPC's DNS resolver waits on a
// service-config TXT lookup) and 2ms with the lookup disabled — longer than
// the 3s budget of the prompt-context providers that open one.
func TestClientConfig(t *testing.T) {
	cfg := ClientConfig("qdrant", 6334, "key", true)
	if cfg.Host != "qdrant" || cfg.Port != 6334 || cfg.APIKey != "key" || !cfg.UseTLS {
		t.Errorf("connection settings not carried: %+v", cfg)
	}
	if len(cfg.GrpcOptions) == 0 {
		t.Error("no dial options: a new channel would wait on the gRPC service-config DNS lookup")
	}
}
