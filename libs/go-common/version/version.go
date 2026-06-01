// Package version holds the DecisionBox build metadata.
//
// All three variables are overridden at build time via -ldflags so a
// shipped binary reports the image it was built as, not the source-tree
// default. The API and Agent Dockerfiles inject them; see
// docs/reference/configuration.md ("Build metadata") for the build args.
//
//	go build -ldflags "\
//	  -X github.com/decisionbox-io/decisionbox/libs/go-common/version.Version=0.4.0 \
//	  -X github.com/decisionbox-io/decisionbox/libs/go-common/version.Commit=abc1234 \
//	  -X github.com/decisionbox-io/decisionbox/libs/go-common/version.BuildDate=2026-06-01T12:00:00Z"
package version

var (
	// Version is the DecisionBox release version (e.g. "0.4.0"). The
	// source-tree default marks an un-stamped local build.
	Version = "0.4.0-dev"

	// Commit is the git commit the binary was built from. "unknown"
	// when the build did not inject it.
	Commit = "unknown"

	// BuildDate is the RFC3339 UTC timestamp of the build. "unknown"
	// when the build did not inject it.
	BuildDate = "unknown"
)
