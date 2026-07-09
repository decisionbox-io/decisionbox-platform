// Package connectors is the OSS extension point for external-storage
// connectors — integrations that let a project attach files/folders from an
// external account (via the provider's native file picker) and ingest them.
//
// It is provider-agnostic: no vendor-specific code lives here. Implementations
// live out-of-tree and self-register via init() + blank import, exactly like
// the embedding providers (libs/go-common/embedding). A build with no connector
// registered links fine — NewConnector simply returns a clear error.
package connectors

import (
	"context"
	"io"
	"time"
)

// Token is an OAuth credential set for a connection. RefreshToken is durable —
// the caller persists it as a secret — and carries the connection's grant;
// access tokens are short-lived and minted per use.
type Token struct {
	AccessToken  string
	RefreshToken string
	Expiry       time.Time
	Scope        string
}

// BrowserToken is a narrow, browser-safe access token for a provider's native
// picker. It is deliberately distinct from Token so a broad server-side grant is
// never handed to the browser: an implementation may down-scope the connection's
// grant to the minimum the picker needs (limiting the blast radius of an XSS
// token theft).
type BrowserToken struct {
	AccessToken string
	Expiry      time.Time
	Scope       string
}

// PickedItem is one selection returned by a provider's native picker — a single
// file or a folder.
type PickedItem struct {
	ExternalID  string `json:"external_id"`
	IsFolder    bool   `json:"is_folder"`
	Name        string `json:"name"`
	MimeType    string `json:"mime_type"`
	DriveID     string `json:"drive_id,omitempty"`
	ResourceKey string `json:"resource_key,omitempty"`
}

// Item is a concrete, ingestible file resolved by Enumerate (folders expanded,
// native-document export targets mapped, inaccessible items omitted).
type Item struct {
	ExternalID string `json:"external_id"` // stable identity across rename/move
	Name       string `json:"name"`
	MimeType   string `json:"mime_type"` // original type in the provider
	// ExportSourceType is the parser key the downloaded bytes will carry
	// (docx|xlsx|csv|md|txt) — the same as MimeType's mapping for real
	// binaries, or the export target for native documents.
	ExportSourceType string `json:"export_source_type"`
	ExternalVersion  string `json:"external_version"` // change-detection key (etag / modified time)
	SizeBytes        int64  `json:"size_bytes"`
	ParentID         string `json:"parent_id,omitempty"`
	DriveID          string `json:"drive_id,omitempty"`
	ResourceKey      string `json:"resource_key,omitempty"`
	WebViewURL       string `json:"web_view_url,omitempty"` // provenance link back to the original file
}

// Connector is the provider-agnostic interface an external-storage integration
// implements. Selection is by kind (Connector.Kind), constructed through the
// registry (NewConnector).
type Connector interface {
	// Kind returns the connector's stable identifier (e.g. "gdrive").
	Kind() string

	// Exchange completes the OAuth Authorization Code (offline) flow, returning
	// a Token whose RefreshToken is durable.
	Exchange(ctx context.Context, code, redirectURI string) (Token, error)

	// Refresh mints a fresh backend access token from the connection's durable
	// grant. Used server-side for Enumerate/Download.
	Refresh(ctx context.Context, tok Token) (Token, error)

	// PickerToken mints a least-privilege, browser-safe token for the provider's
	// native picker, so the browser never holds the broader backend grant.
	PickerToken(ctx context.Context, tok Token) (BrowserToken, error)

	// Enumerate expands the picked selections into concrete ingestible files —
	// folders expanded (recursively where the provider allows), native documents
	// mapped to an export target, inaccessible items omitted.
	Enumerate(ctx context.Context, tok Token, selections []PickedItem) ([]Item, error)

	// Download returns the file's content stream and the MIME type of the bytes
	// returned (which may differ from Item.MimeType when a native document is
	// exported). The caller closes the stream.
	Download(ctx context.Context, tok Token, item Item) (io.ReadCloser, string, error)
}
