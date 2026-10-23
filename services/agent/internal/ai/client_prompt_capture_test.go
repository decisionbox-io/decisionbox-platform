package ai

import (
	"fmt"
	"os"
	"sync"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/testutil"
)

// Validation can run documents in parallel on one Client. In test mode every
// request is captured to its own numbered file; concurrent captures must not
// share a number (and overwrite each other) or race on the counter.
func TestSavePrompt_ConcurrentCapturesGetUniqueFiles(t *testing.T) {
	t.Chdir(t.TempDir())
	client, _ := New(testutil.NewMockLLMProvider(), "test-model")
	client.SetTestMode(true)

	const n = 64
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			client.savePrompt([]gollm.Message{{Role: "user", Content: fmt.Sprintf("request %d", i)}}, "system")
		}(i)
	}
	wg.Wait()

	files, err := os.ReadDir("test-prompts")
	if err != nil {
		t.Fatalf("read capture dir: %v", err)
	}
	if len(files) != n {
		t.Errorf("captured %d prompt files for %d requests", len(files), n)
	}
}
