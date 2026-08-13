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

// The guidance quotes a row limit the model should re-run within. Three caps
// bound a chartable result and the smallest wins, so quoting PreviewRows alone
// would walk a tuned deployment into another rejected chart.
func TestChartableRowCap(t *testing.T) {
	tests := []struct {
		name                              string
		preview, fetch, maxPoints, expect int
	}{
		{"preview is the only bound", 50, 1000, 50, 50},
		{"chart point cap is lower", 50, 1000, 20, 20},
		{"fetch cap is lower", 50, 30, 50, 30},
		{"smallest of the three wins", 50, 30, 10, 10},
		{"unset caps do not clamp to zero", 50, 0, 0, 50},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Config{PreviewRows: tt.preview, MaxFetchRows: tt.fetch}
			c.ChartCaps.MaxPoints = tt.maxPoints
			if got := c.ChartableRowCap(); got != tt.expect {
				t.Errorf("ChartableRowCap() = %d, want %d", got, tt.expect)
			}
		})
	}
}
