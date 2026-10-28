package ai

import (
	"fmt"
	"os"
	"strings"
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

// Per-layer models give every layer its OWN Client. A capture sequence kept
// per client restarts at 1 in each of them, and the only other thing in the
// filename is a seconds-resolution timestamp — so two layers calling in the
// same second wrote the same file and one capture was lost.
func TestSavePrompt_LayersDoNotOverwriteEachOthersCaptures(t *testing.T) {
	t.Chdir(t.TempDir())

	const perLayer = 3
	layers := []string{"exploration", "analysis", "table_descriptions"}
	for _, phase := range layers {
		client, _ := New(testutil.NewMockLLMProvider(), "test-model")
		client.SetTestMode(true)
		client.SetPhase(phase)
		for i := 0; i < perLayer; i++ {
			client.savePrompt([]gollm.Message{{Role: "user", Content: phase}}, "system")
		}
	}

	files, err := os.ReadDir("test-prompts")
	if err != nil {
		t.Fatalf("read capture dir: %v", err)
	}
	if want := len(layers) * perLayer; len(files) != want {
		names := make([]string, 0, len(files))
		for _, f := range files {
			names = append(names, f.Name())
		}
		t.Fatalf("captured %d files for %d requests across %d layers: %v", len(files), want, len(layers), names)
	}
	// Each capture says which layer wrote it, so a directory of them is
	// readable without opening every file.
	for _, phase := range layers {
		found := false
		for _, f := range files {
			if strings.Contains(f.Name(), strings.ReplaceAll(phase, "_", "-")) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no capture names the %q layer", phase)
		}
	}
}
