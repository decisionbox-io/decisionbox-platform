package askserve

import (
	"testing"
	"time"
)

func TestLoadConfig_Defaults(t *testing.T) {
	cfg := LoadConfig()
	if cfg.Port != defaultPort {
		t.Errorf("Port = %d, want %d", cfg.Port, defaultPort)
	}
	if cfg.WallClock != defaultWallClock {
		t.Errorf("WallClock = %s, want %s", cfg.WallClock, defaultWallClock)
	}
	if cfg.MaxFetchRows != defaultMaxFetchRows || cfg.PreviewRows != defaultPreviewRows {
		t.Errorf("fetch/preview = %d/%d", cfg.MaxFetchRows, cfg.PreviewRows)
	}
	if cfg.MaxConcurrentTurns != defaultMaxConcurrentTurns || cfg.MaxConcurrentPerProject != defaultMaxConcurrentPerPrj {
		t.Errorf("concurrency = %d/%d", cfg.MaxConcurrentTurns, cfg.MaxConcurrentPerProject)
	}
}

func TestLoadConfig_EnvOverrides(t *testing.T) {
	t.Setenv(EnvPort, "9999")
	t.Setenv(EnvWallClockSeconds, "60")
	t.Setenv(EnvMaxRounds, "3")
	t.Setenv(EnvPoolIdleTTL, "30s")
	cfg := LoadConfig()
	if cfg.Port != 9999 {
		t.Errorf("Port = %d, want 9999", cfg.Port)
	}
	if cfg.WallClock != 60*time.Second {
		t.Errorf("WallClock = %s, want 1m", cfg.WallClock)
	}
	if cfg.MaxRounds != 3 {
		t.Errorf("MaxRounds = %d, want 3", cfg.MaxRounds)
	}
	if cfg.PoolIdleTTL != 30*time.Second {
		t.Errorf("PoolIdleTTL = %s, want 30s", cfg.PoolIdleTTL)
	}
}

func TestLoadConfig_InvalidFallsBackToDefault(t *testing.T) {
	t.Setenv(EnvMaxFetchRows, "-5")    // non-positive → default
	t.Setenv(EnvWallClockSeconds, "0") // non-positive seconds → default
	cfg := LoadConfig()
	if cfg.MaxFetchRows != defaultMaxFetchRows {
		t.Errorf("MaxFetchRows = %d, want default %d", cfg.MaxFetchRows, defaultMaxFetchRows)
	}
	if cfg.WallClock != defaultWallClock {
		t.Errorf("WallClock = %s, want default", cfg.WallClock)
	}
}
