// Package all imports all secret providers so they register via init().
// Services import this package instead of individual providers:
//
//	import _ "github.com/decisionbox-io/decisionbox/providers/secrets/all"
//
// Note: the MongoDB secret provider is typically imported by name (not blank)
// because services call mongoSecrets.NewMongoProvider() directly. This
// aggregator covers providers that only need registration (GCP, AWS).
//
// When adding a new secret provider, add its blank import here.
package all

import (
	_ "github.com/decisionbox-io/decisionbox/providers/secrets/aws" // registers "aws"
	_ "github.com/decisionbox-io/decisionbox/providers/secrets/gcp" // registers "gcp"
)
