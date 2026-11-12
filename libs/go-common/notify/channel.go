package notify

import (
	"context"
	"time"
)

// Channel is a notification delivery backend (e.g., Slack, Teams, webhook).
// Enterprise plugins implement this interface and register via Register().
//
// Each Channel is responsible for checking its own configuration:
// when Notify is called, the implementation queries its store to determine
// whether notifications are configured and enabled for the given project.
// If not configured, Notify returns nil (no-op).
type Channel interface {
	// Type returns the channel identifier (e.g., "slack", "teams", "webhook").
	Type() string

	// Notify delivers a notification event.
	// The implementation checks its own store to determine whether the channel
	// is configured for the event's project. Returns nil if not configured.
	Notify(ctx context.Context, event Event) error

	// ValidateConfig verifies that the channel's global configuration is valid
	// (e.g., bot token exists and is accepted by the remote API).
	ValidateConfig(ctx context.Context) error
}

// ChannelMeta describes a notification channel for UI rendering.
// Returned by the GET /api/v1/integrations/channels endpoint so the dashboard
// can discover available channels and render configuration forms.
type ChannelMeta struct {
	Type        string        `json:"type"`
	Name        string        `json:"name"`
	Description string        `json:"description"`
	IconURL     string        `json:"icon_url,omitempty"`
	Fields      []ConfigField `json:"fields"`
}

// ConfigField describes a configuration field for UI rendering.
type ConfigField struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
	Type        string `json:"type"` // "text", "credential", "select", "boolean"
	Default     string `json:"default,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	Scope       string `json:"scope"` // "global" (instance-level) or "project" (per-project)
}

// Event represents a notification-worthy event in the platform.
type Event struct {
	Type        EventType     `json:"type"`
	ProjectID   string        `json:"project_id"`
	ProjectName string        `json:"project_name"`
	Domain      string        `json:"domain,omitempty"`
	Category    string        `json:"category,omitempty"`
	RunID       string        `json:"run_id"`
	DiscoveryID string        `json:"discovery_id,omitempty"`
	Duration    time.Duration `json:"duration"`
	Timestamp   time.Time     `json:"timestamp"`

	// Populated on discovery_completed.
	//
	// The three counters below count PRIORITY TIERS, not severity words. They
	// keep their wire names because a channel's own templates are built
	// against them, and what changed is what they mean: `critical` is the top
	// tier (high severity, trusted evidence), `high` is the tier below it
	// (medium severity, trusted), and `medium` carries `investigate` -- a
	// severe finding nothing could check, which the old severity vocabulary
	// had no way to express at all.
	//
	// Counting by tier rather than by the word is not cosmetic. No insight
	// writes `critical` as a severity any more, so a counter reading the word
	// would have reported zero of them on every run.
	InsightsTotal    int            `json:"insights_total,omitempty"`
	InsightsCritical int            `json:"insights_critical,omitempty"`
	InsightsHigh     int            `json:"insights_high,omitempty"`
	InsightsMedium   int            `json:"insights_medium,omitempty"`
	Recommendations  int            `json:"recommendations,omitempty"`
	QueriesExecuted  int            `json:"queries_executed,omitempty"`
	TopInsights      []InsightBrief `json:"top_insights,omitempty"`
	TopRecommendations []RecommendationBrief `json:"top_recommendations,omitempty"`

	// Populated on discovery_failed
	Error string `json:"error,omitempty"`
}

// InsightBrief is a summary of a single insight for notifications.
type InsightBrief struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Severity is the derived band -- high, medium or low -- or the model's
	// own word on an insight written before it was derived, `critical`
	// included.
	Severity string `json:"severity"`
	// PriorityTier is what the briefs are ordered by, and what a channel
	// should lead with: it folds the severity band together with whether the
	// claim held up against warehouse rows. Empty on a legacy insight, which
	// is ordered by its severity word instead.
	PriorityTier  string `json:"priority_tier,omitempty"`
	AnalysisArea  string `json:"analysis_area"`
	AffectedCount int    `json:"affected_count"`
}

// RecommendationBrief is a summary of a single recommendation for notifications.
type RecommendationBrief struct {
	ID                   string `json:"id"`
	Title                string `json:"title"`
	Metric               string `json:"metric"`
	EstimatedImprovement string `json:"estimated_improvement"`
}

// EventType identifies the kind of notification event.
type EventType string

const (
	EventDiscoveryCompleted EventType = "discovery_completed"
	EventDiscoveryFailed    EventType = "discovery_failed"
)
