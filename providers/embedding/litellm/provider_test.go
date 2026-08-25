package litellm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	goembedding "github.com/decisionbox-io/decisionbox/libs/go-common/embedding"
)

func TestRegistered(t *testing.T) {
	meta, ok := goembedding.GetProviderMeta("litellm")
	if !ok {
		t.Fatal("litellm embedding provider not registered")
	}
	if meta.Name != "LiteLLM" {
		t.Errorf("Name = %q", meta.Name)
	}
	var hasCA, hasSkip, hasNone bool
	for _, f := range meta.ConfigFields {
		switch f.Key {
		case goembedding.TLSCACertKey:
			hasCA = true
		case goembedding.TLSSkipVerifyKey:
			hasSkip = true
		}
	}
	for _, m := range meta.AuthMethods {
		if m.ID == "none" && len(m.Fields) == 0 {
			hasNone = true
		}
	}
	if !hasCA || !hasSkip || !hasNone {
		t.Errorf("meta incomplete: ca=%v skip=%v none=%v", hasCA, hasSkip, hasNone)
	}
}

func TestFactory_RequiresBaseURLAndModel(t *testing.T) {
	if _, err := goembedding.NewProvider("litellm", goembedding.ProviderConfig{"model": "m"}); err == nil {
		t.Error("expected base_url required error")
	}
	if _, err := goembedding.NewProvider("litellm", goembedding.ProviderConfig{"base_url": "https://x:4000"}); err == nil {
		t.Error("expected model required error")
	}
}

func TestFactory_MalformedCA(t *testing.T) {
	_, err := goembedding.NewProvider("litellm", goembedding.ProviderConfig{
		"base_url":               "https://x:4000",
		"model":                  "m",
		goembedding.TLSCACertKey: "not a cert",
	})
	if err == nil {
		t.Error("expected malformed CA error")
	}
}

func TestEmbed_SendsModelAndBearer(t *testing.T) {
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[0.1,0.2,0.3]}]}`))
	}))
	defer srv.Close()

	prov, err := goembedding.NewProvider("litellm", goembedding.ProviderConfig{
		"base_url":         srv.URL,
		"model":            "text-embedding-3-small",
		"auth_method":      "api_key",
		"credentials_json": "sk-key",
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	vecs, err := prov.Embed(context.Background(), []string{"hello"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 1 || len(vecs[0]) != 3 {
		t.Errorf("unexpected vecs: %+v", vecs)
	}
	if gotAuth != "Bearer sk-key" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotPath != "/v1/embeddings" {
		t.Errorf("path = %q, want /v1/embeddings", gotPath)
	}
}

func TestEmbed_NoneAuthDropsKey(t *testing.T) {
	var hadAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hadAuth = r.Header["Authorization"]
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[0.1]}]}`))
	}))
	defer srv.Close()

	prov, err := goembedding.NewProvider("litellm", goembedding.ProviderConfig{
		"base_url":         srv.URL,
		"model":            "m",
		"auth_method":      "none",
		"credentials_json": "sk-should-drop",
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	if _, err := prov.Embed(context.Background(), []string{"x"}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if hadAuth {
		t.Error("auth_method=none must not send Authorization")
	}
}
