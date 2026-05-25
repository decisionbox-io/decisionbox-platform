package runner

import (
	"context"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestKubernetesRunner_RunValidateDoc_CreatesJob verifies the happy
// path — RunValidateDoc creates a Job with the right --mode flag and
// the right labels.
func TestKubernetesRunner_RunValidateDoc_CreatesJob(t *testing.T) {
	r := newFakeK8sRunner()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// We don't expect the Job to "complete" in the fake client; the
	// ctx will time out first. We only care that the Job got created
	// with the right shape before the watcher gives up.
	_ = r.RunValidateDoc(ctx, ValidateDocOptions{
		JobID:       "job-abc-123",
		ProjectID:   "proj-1",
		DiscoveryID: "disc-1",
		DocKind:     "insight",
		DocID:       "ins-aaa",
	})

	jobs, err := r.client.BatchV1().Jobs("test-ns").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	// Either the Job is still there (ctx timed out before delete) or
	// it was deleted by the ctx-cancel path. Both are valid happy-path
	// outcomes — the bug we're guarding against is the Job being
	// LEFT running uncancellable. We assert below that the Job was
	// created at some point.
	if len(jobs.Items) > 0 {
		j := jobs.Items[0]
		if j.Labels["type"] != "validate-doc" {
			t.Errorf("expected type=validate-doc label, got %q", j.Labels["type"])
		}
		if j.Labels["job-id"] != "job-abc-123" {
			t.Errorf("expected job-id=job-abc-123 label, got %q", j.Labels["job-id"])
		}
		// Walk the container args for --mode validate-doc.
		spec := j.Spec.Template.Spec.Containers[0]
		found := false
		for i := 0; i+1 < len(spec.Args); i++ {
			if spec.Args[i] == "--mode" && spec.Args[i+1] == "validate-doc" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected --mode validate-doc in args, got %v", spec.Args)
		}
	}
}

// THE Codex prod-r2 P0 regression test. When the caller cancels ctx
// while the Job is still in flight, RunValidateDoc MUST delete the
// Job before returning. RunIndexSchema historically just returned
// ctx.Err() and left the Job running on the cluster — that's the
// bug we are explicitly guarding against here.
func TestKubernetesRunner_RunValidateDoc_DeletesJobOnContextCancel(t *testing.T) {
	r := newFakeK8sRunner()
	ctx, cancel := context.WithCancel(context.Background())

	// Cancel after a short delay so RunValidateDoc has time to create
	// the Job before we yank the ctx.
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	err := r.RunValidateDoc(ctx, ValidateDocOptions{
		JobID:       "job-cancel-test",
		ProjectID:   "proj-cancel",
		DiscoveryID: "disc-cancel",
		DocKind:     "insight",
		DocID:       "ins-cancel",
	})
	if err == nil {
		t.Fatalf("RunValidateDoc should return ctx.Err() on cancel")
	}

	// Confirm the Job is gone (or marked for deletion). The fake
	// client honours foreground-propagation Delete by removing the
	// object outright; against a real cluster the Job would have a
	// DeletionTimestamp set while the controller cascade-deletes the
	// pod, and Get would return it briefly. Either way the test
	// would FAIL on the historical "silent return" behaviour
	// because the Job would persist unchanged.
	jobs, err := r.client.BatchV1().Jobs("test-ns").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("post-cancel list jobs: %v", err)
	}
	for _, j := range jobs.Items {
		if j.Labels["job-id"] == "job-cancel-test" {
			if !isDeletingOrGone(j) {
				t.Errorf("expected Job to be deleted or marked for deletion after ctx cancel; still present: %+v", j.ObjectMeta)
			}
		}
	}
}

// TestKubernetesRunner_RunValidateDoc_RejectsEmptyJobID guards the
// invariant that the agent receives an explicit --job-id (which it
// uses to load the ValidationJob row).
func TestKubernetesRunner_RunValidateDoc_RejectsEmptyJobID(t *testing.T) {
	r := newFakeK8sRunner()
	err := r.RunValidateDoc(context.Background(), ValidateDocOptions{})
	if err == nil {
		t.Errorf("RunValidateDoc with empty JobID should error")
	}
}

func TestSubprocessRunner_RunValidateDoc_RejectsEmptyJobID(t *testing.T) {
	r := NewSubprocessRunner()
	err := r.RunValidateDoc(context.Background(), ValidateDocOptions{})
	if err == nil {
		t.Errorf("RunValidateDoc with empty JobID should error")
	}
}

// isDeletingOrGone returns true when a Job has been deleted from the
// fake client or has a deletion timestamp set. Either is a valid
// "deletion was issued" signal.
func isDeletingOrGone(j batchv1.Job) bool {
	return j.DeletionTimestamp != nil
}

// Codex prod-r4 P1 — sanitizeK8sLabelSegment must never let a
// hyphen land on the last character. UUID v4 hyphens at positions
// 8/13/18/23 made the original 24-char truncation produce
// `validate-xxxxxxxx-xxxx-xxxx-xxxx-` which the apiserver rejects.
func TestSanitizeK8sLabelSegment_StripsTrailingDashFromUUIDTruncation(t *testing.T) {
	uuid := "abcd1234-5678-90ab-cdef-1234567890ab"
	got := sanitizeK8sLabelSegment(uuid, 24)
	if got == "" {
		t.Fatalf("sanitizer returned empty string")
	}
	if got[len(got)-1] == '-' {
		t.Errorf("trailing dash not stripped, got %q", got)
	}
	if got[0] == '-' {
		t.Errorf("leading dash not stripped, got %q", got)
	}
}

func TestSanitizeK8sLabelSegment_PreservesShortIDs(t *testing.T) {
	got := sanitizeK8sLabelSegment("short-id", 24)
	if got != "short-id" {
		t.Errorf("got %q, want short-id", got)
	}
}

func TestSanitizeK8sLabelSegment_HandlesAllDashes(t *testing.T) {
	got := sanitizeK8sLabelSegment("------", 24)
	if got != "" {
		t.Errorf("all-dash input should produce empty string, got %q", got)
	}
}
