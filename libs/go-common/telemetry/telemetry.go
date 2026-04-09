// Package telemetry provides anonymous, privacy-respecting usage telemetry
// for DecisionBox. Telemetry is enabled by default but can be disabled by
// setting TELEMETRY_ENABLED=false or DO_NOT_TRACK=1.
//
// What is collected: anonymous install ID, version, OS/arch, provider types,
// event counts (projects created, discoveries run). No PII, no query content,
// no credentials, no warehouse/table names.
//
// See TELEMETRY.md at the repository root for full details.
package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/decisionbox-io/decisionbox/libs/go-common/config"
)

const (
	defaultEndpoint  = "https://telemetry.decisionbox.io/v1/events"
	flushInterval    = 1 * time.Hour
	maxBatchSize     = 50
	sendTimeout      = 10 * time.Second

	// publicAPIKey is a non-secret key that identifies requests as coming from
	// a DecisionBox instance. It filters out non-DecisionBox traffic and casual
	// abuse. This is intentionally public — it's in the source code.
	publicAPIKey = "dbox_tel_pub_v1_a8f3e2d1c4b5" //nolint:gosec // G101: not a credential — intentionally public API key
)

// Event represents a single telemetry event.
type Event struct {
	Name       string            `json:"name"`
	Properties map[string]any    `json:"properties,omitempty"`
	Timestamp  time.Time         `json:"timestamp"`
}

// Batch is the payload sent to the telemetry endpoint.
type Batch struct {
	InstallID  string  `json:"install_id"`
	Version    string  `json:"version"`
	GoVersion  string  `json:"go_version"`
	OS         string  `json:"os"`
	Arch       string  `json:"arch"`
	Service    string  `json:"service"`
	Events     []Event `json:"events"`
}

// Client manages telemetry event collection and transmission.
type Client struct {
	mu            sync.Mutex
	enabled       bool
	installID     string
	version       string
	service       string
	endpoint      string
	flushInterval time.Duration
	events        []Event
	done          chan struct{}
	httpClient    *http.Client
}

var (
	globalClient *Client
	once         sync.Once
)

// Init initializes the global telemetry client. Call once at startup.
// installID should be a persistent random UUID stored in MongoDB.
// version is the application version string.
// service is "api" or "agent".
func Init(installID, version, service string) {
	once.Do(func() {
		enabled := isEnabled()
		endpoint := config.GetEnvOrDefault("TELEMETRY_ENDPOINT", defaultEndpoint)

		interval := config.GetEnvAsDuration("TELEMETRY_FLUSH_INTERVAL", flushInterval)

		globalClient = &Client{
			enabled:       enabled,
			installID:     installID,
			version:       version,
			service:       service,
			endpoint:      endpoint,
			flushInterval: interval,
			events:        make([]Event, 0, maxBatchSize),
			done:          make(chan struct{}),
			httpClient:    &http.Client{Timeout: sendTimeout},
		}

		if enabled {
			go globalClient.flushLoop()
		}
	})
}

// Track records a telemetry event. No-op if telemetry is disabled.
func Track(name string, properties map[string]any) {
	if globalClient == nil || !globalClient.enabled {
		return
	}
	globalClient.track(name, properties)
}

// Shutdown flushes pending events and stops the background sender.
// Call during graceful shutdown.
func Shutdown() {
	if globalClient == nil || !globalClient.enabled {
		return
	}
	globalClient.shutdown()
}

// IsEnabled returns whether telemetry is currently enabled.
func IsEnabled() bool {
	if globalClient == nil {
		return false
	}
	return globalClient.enabled
}

func (c *Client) track(name string, properties map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.events = append(c.events, Event{
		Name:       name,
		Properties: properties,
		Timestamp:  time.Now().UTC(),
	})

	if len(c.events) >= maxBatchSize {
		c.flushLocked()
	}
}

func (c *Client) flushLoop() {
	ticker := time.NewTicker(c.flushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.flush()
		case <-c.done:
			return
		}
	}
}

func (c *Client) flush() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.flushLocked()
}

func (c *Client) flushLocked() {
	if len(c.events) == 0 {
		return
	}

	batch := Batch{
		InstallID: c.installID,
		Version:   c.version,
		GoVersion: runtime.Version(),
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		Service:   c.service,
		Events:    c.events,
	}

	c.events = make([]Event, 0, maxBatchSize)

	go c.send(batch)
}

func (c *Client) send(batch Batch) {
	body, err := json.Marshal(batch)
	if err != nil {
		log.Printf("[telemetry] marshal error: %v", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		log.Printf("[telemetry] request error: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", publicAPIKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// Silently ignore network errors — telemetry must never affect the application
		return
	}
	defer resp.Body.Close()
}

func (c *Client) shutdown() {
	c.flush()
	close(c.done)
}

// isEnabled checks environment variables to determine if telemetry is enabled.
// Respects DO_NOT_TRACK (https://consoledonottrack.com/).
func isEnabled() bool {
	// DO_NOT_TRACK standard (any truthy value disables)
	dnt := config.GetEnv("DO_NOT_TRACK")
	if dnt == "1" || dnt == "true" || dnt == "yes" {
		return false
	}

	// TELEMETRY_ENABLED (default: true)
	return config.GetEnvAsBool("TELEMETRY_ENABLED", true)
}
