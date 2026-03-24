// Package all imports all domain packs so they register via init().
// Services import this package instead of individual domain packs:
//
//	import _ "github.com/decisionbox-io/decisionbox/domain-packs/all"
//
// When adding a new domain pack, add its blank import here.
package all

import (
	_ "github.com/decisionbox-io/decisionbox/domain-packs/gaming/go" // registers "gaming"
	_ "github.com/decisionbox-io/decisionbox/domain-packs/social/go" // registers "social"
)
