package telemetry

// Event names — every event sent by DecisionBox is defined here.
const (
	EventServerStarted       = "server_started"
	EventServerStopped       = "server_stopped"
	EventProjectCreated      = "project_created"
	EventDiscoveryCompleted  = "discovery_completed"
	EventDiscoveryFailed     = "discovery_failed"
)

// DurationBucket returns a human-readable duration bucket for telemetry.
// We never send exact durations — only coarse buckets.
func DurationBucket(seconds float64) string {
	switch {
	case seconds < 60:
		return "<1m"
	case seconds < 300:
		return "1-5m"
	case seconds < 900:
		return "5-15m"
	default:
		return ">15m"
	}
}

// CountBucket returns a coarse count bucket.
func CountBucket(count int) string {
	switch {
	case count == 0:
		return "0"
	case count <= 5:
		return "1-5"
	case count <= 20:
		return "6-20"
	case count <= 50:
		return "21-50"
	default:
		return "50+"
	}
}

// TrackServerStarted records that the API server started.
func TrackServerStarted(props map[string]any) {
	Track(EventServerStarted, props)
}

// TrackServerStopped records that the API server stopped.
func TrackServerStopped() {
	Track(EventServerStopped, nil)
}

// TrackProjectCreated records that a new project was created.
func TrackProjectCreated(warehouseProvider, llmProvider, domain string) {
	Track(EventProjectCreated, map[string]any{
		"warehouse_provider": warehouseProvider,
		"llm_provider":       llmProvider,
		"domain":             domain,
	})
}

// TrackDiscoveryCompleted records that a discovery run completed.
func TrackDiscoveryCompleted(warehouseProvider, llmProvider, domain, domainPack string, durationSec float64, insightsCount, recsCount, queriesCount int) {
	Track(EventDiscoveryCompleted, map[string]any{
		"warehouse_provider": warehouseProvider,
		"llm_provider":       llmProvider,
		"domain":             domain,
		"domain_pack":        domainPack,
		"duration_bucket":    DurationBucket(durationSec),
		"insights_bucket":    CountBucket(insightsCount),
		"recs_bucket":        CountBucket(recsCount),
		"queries_bucket":     CountBucket(queriesCount),
	})
}

// TrackDiscoveryFailed records that a discovery run failed.
func TrackDiscoveryFailed(warehouseProvider, llmProvider, domain string, errorClass string) {
	Track(EventDiscoveryFailed, map[string]any{
		"warehouse_provider": warehouseProvider,
		"llm_provider":       llmProvider,
		"domain":             domain,
		"error_class":        errorClass,
	})
}
