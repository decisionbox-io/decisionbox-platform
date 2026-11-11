package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGetEnv(t *testing.T) {
	os.Setenv("TEST_GET_ENV", "hello")
	defer os.Unsetenv("TEST_GET_ENV")

	if got := GetEnv("TEST_GET_ENV"); got != "hello" {
		t.Errorf("GetEnv() = %q, want %q", got, "hello")
	}

	if got := GetEnv("TEST_GET_ENV_MISSING"); got != "" {
		t.Errorf("GetEnv() for missing key = %q, want empty", got)
	}
}

func TestGetEnvFilePrefix(t *testing.T) {
	dir := t.TempDir()
	secretFile := filepath.Join(dir, "secret.txt")
	os.WriteFile(secretFile, []byte("  my-secret-value  \n"), 0644)

	os.Setenv("TEST_FILE_ENV", "file://"+secretFile)
	defer os.Unsetenv("TEST_FILE_ENV")

	if got := GetEnv("TEST_FILE_ENV"); got != "my-secret-value" {
		t.Errorf("GetEnv() with file:// = %q, want %q", got, "my-secret-value")
	}
}

func TestGetEnvFilePrefixMissing(t *testing.T) {
	os.Setenv("TEST_FILE_MISSING", "file:///nonexistent/path")
	defer os.Unsetenv("TEST_FILE_MISSING")

	if got := GetEnv("TEST_FILE_MISSING"); got != "" {
		t.Errorf("GetEnv() with missing file = %q, want empty", got)
	}
}

func TestGetEnvOrDefault(t *testing.T) {
	os.Setenv("TEST_DEFAULT_SET", "value")
	defer os.Unsetenv("TEST_DEFAULT_SET")

	if got := GetEnvOrDefault("TEST_DEFAULT_SET", "fallback"); got != "value" {
		t.Errorf("got %q, want %q", got, "value")
	}

	if got := GetEnvOrDefault("TEST_DEFAULT_MISSING", "fallback"); got != "fallback" {
		t.Errorf("got %q, want %q", got, "fallback")
	}
}

func TestGetEnvAsInt(t *testing.T) {
	os.Setenv("TEST_INT", "42")
	defer os.Unsetenv("TEST_INT")

	if got := GetEnvAsInt("TEST_INT", 0); got != 42 {
		t.Errorf("got %d, want 42", got)
	}

	if got := GetEnvAsInt("TEST_INT_MISSING", 99); got != 99 {
		t.Errorf("got %d, want 99", got)
	}

	os.Setenv("TEST_INT_BAD", "notanumber")
	defer os.Unsetenv("TEST_INT_BAD")

	if got := GetEnvAsInt("TEST_INT_BAD", 99); got != 99 {
		t.Errorf("got %d, want 99 for bad input", got)
	}
}

func TestGetEnvAsBool(t *testing.T) {
	tests := []struct {
		value    string
		expected bool
	}{
		{"true", true},
		{"false", false},
		{"1", true},
		{"0", false},
	}

	for _, tt := range tests {
		os.Setenv("TEST_BOOL", tt.value)
		if got := GetEnvAsBool("TEST_BOOL", false); got != tt.expected {
			t.Errorf("GetEnvAsBool(%q) = %v, want %v", tt.value, got, tt.expected)
		}
	}
	os.Unsetenv("TEST_BOOL")

	if got := GetEnvAsBool("TEST_BOOL_MISSING", true); got != true {
		t.Errorf("default not returned for missing key")
	}
}

func TestGetEnvAsDuration(t *testing.T) {
	os.Setenv("TEST_DUR", "5s")
	defer os.Unsetenv("TEST_DUR")

	if got := GetEnvAsDuration("TEST_DUR", time.Second); got != 5*time.Second {
		t.Errorf("got %v, want 5s", got)
	}

	if got := GetEnvAsDuration("TEST_DUR_MISSING", 10*time.Second); got != 10*time.Second {
		t.Errorf("got %v, want 10s", got)
	}

	os.Setenv("TEST_DUR_BAD", "invalid")
	defer os.Unsetenv("TEST_DUR_BAD")

	if got := GetEnvAsDuration("TEST_DUR_BAD", 10*time.Second); got != 10*time.Second {
		t.Errorf("got %v, want 10s for bad input", got)
	}
}

func TestGetEnvAsSlice(t *testing.T) {
	os.Setenv("TEST_SLICE", "a,b,c")
	defer os.Unsetenv("TEST_SLICE")

	got := GetEnvAsSlice("TEST_SLICE", ",")
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Errorf("got %v, want [a b c]", got)
	}

	if got := GetEnvAsSlice("TEST_SLICE_MISSING", ","); got != nil {
		t.Errorf("got %v, want nil", got)
	}

	os.Setenv("TEST_SLICE_SPACES", " a , b , c ")
	defer os.Unsetenv("TEST_SLICE_SPACES")

	got = GetEnvAsSlice("TEST_SLICE_SPACES", ",")
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Errorf("got %v, want trimmed [a b c]", got)
	}
}

func TestMustGetEnv(t *testing.T) {
	os.Setenv("TEST_MUST", "exists")
	defer os.Unsetenv("TEST_MUST")

	if got := MustGetEnv("TEST_MUST"); got != "exists" {
		t.Errorf("got %q, want %q", got, "exists")
	}

	defer func() {
		if r := recover(); r == nil {
			t.Error("MustGetEnv should panic for missing key")
		}
	}()
	MustGetEnv("TEST_MUST_MISSING")
}

func TestGetEnvAsFloat(t *testing.T) {
	cases := []struct {
		name     string
		value    string
		set      bool
		def      float64
		expected float64
	}{
		{name: "plain fraction", value: "0.25", set: true, def: 0.5, expected: 0.25},
		{name: "integer form", value: "2", set: true, def: 0.5, expected: 2},
		{name: "negative", value: "-1.5", set: true, def: 0.5, expected: -1.5},
		{name: "surrounded by whitespace", value: " 0.4 ", set: true, def: 0.5, expected: 0.4},
		{name: "unparseable falls back", value: "notanumber", set: true, def: 0.5, expected: 0.5},
		{name: "empty falls back", value: "", set: true, def: 0.5, expected: 0.5},
		{name: "unset falls back", set: false, def: 0.5, expected: 0.5},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			const key = "TEST_FLOAT"
			os.Unsetenv(key)
			if tt.set {
				os.Setenv(key, tt.value)
				defer os.Unsetenv(key)
			}
			if got := GetEnvAsFloat(key, tt.def); got != tt.expected {
				t.Errorf("GetEnvAsFloat(%q) = %v, want %v", tt.value, got, tt.expected)
			}
		})
	}
}
