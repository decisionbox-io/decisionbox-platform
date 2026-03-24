// Package all imports all warehouse providers so they register via init().
// Services import this package instead of individual providers:
//
//	import _ "github.com/decisionbox-io/decisionbox/providers/warehouse/all"
//
// When adding a new warehouse provider, add its blank import here.
package all

import (
	_ "github.com/decisionbox-io/decisionbox/providers/warehouse/bigquery"  // registers "bigquery"
	_ "github.com/decisionbox-io/decisionbox/providers/warehouse/redshift"  // registers "redshift"
)
