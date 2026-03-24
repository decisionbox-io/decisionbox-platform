// Package all imports all LLM providers so they register via init().
// Services import this package instead of individual providers:
//
//	import _ "github.com/decisionbox-io/decisionbox/providers/llm/all"
//
// When adding a new LLM provider, add its blank import here.
package all

import (
	_ "github.com/decisionbox-io/decisionbox/providers/llm/bedrock"    // registers "bedrock"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/claude"     // registers "claude"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/ollama"     // registers "ollama"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/openai"     // registers "openai"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/vertex-ai"  // registers "vertex-ai"
)
