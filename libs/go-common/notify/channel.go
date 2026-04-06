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
	RunID       string        `json:"run_id"`
	Duration    time.Duration `json:"duration"`
	Timestamp   time.Time     `json:"timestamp"`

	// Populated on discovery_completed
	InsightsTotal    int `json:"insights_total,omitempty"`
	InsightsCritical int `json:"insights_critical,omitempty"`
	InsightsHigh     int `json:"insights_high,omitempty"`
	Recommendations  int `json:"recommendations,omitempty"`

	// Populated on discovery_failed
	Error string `json:"error,omitempty"`
}

// EventType identifies the kind of notification event.
type EventType string

const (
	EventDiscoveryCompleted EventType = "discovery_completed"
	EventDiscoveryFailed    EventType = "discovery_failed"
)
