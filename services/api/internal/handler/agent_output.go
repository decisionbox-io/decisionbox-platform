package handler

import (
	"encoding/json"
	"strings"
)

// extractJSONObject finds the agent result JSON in mixed output.
// K8s pod logs mix stdout (result) with stderr (structured log lines).
// We scan from the end and return the first valid JSON object that is
// not a log line (log lines always contain a "severity" key).
func extractJSONObject(data []byte) []byte {
	lines := strings.Split(string(data), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var obj map[string]interface{}
		if json.Unmarshal([]byte(line), &obj) == nil {
			if _, ok := obj["severity"]; !ok {
				return []byte(line)
			}
		}
	}
	return nil
}
