package runner

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	apilog "github.com/decisionbox-io/decisionbox/services/api/internal/log"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// KubernetesRunner spawns agent containers as K8s Jobs.
// Production mode — each discovery run is an isolated container.
type KubernetesRunner struct {
	client    kubernetes.Interface
	config    Config
}

func NewKubernetesRunner(cfg Config) (*KubernetesRunner, error) {
	// Use in-cluster config (assumes API runs inside K8s)
	k8sCfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("kubernetes runner: failed to get in-cluster config (is the API running in K8s?): %w", err)
	}

	clientset, err := kubernetes.NewForConfig(k8sCfg)
	if err != nil {
		return nil, fmt.Errorf("kubernetes runner: failed to create client: %w", err)
	}

	apilog.WithFields(apilog.Fields{
		"namespace": cfg.Namespace,
		"image":     cfg.AgentImage,
	}).Info("Runner mode: kubernetes")

	return &KubernetesRunner{
		client: clientset,
		config: cfg,
	}, nil
}

// jobSpec holds the variable parts for building a K8s Job.
type jobSpec struct {
	name      string
	labels    map[string]string
	podLabels map[string]string
	args      []string
	ttl       int32
	deadline  *int64 // nil = no deadline
	cpuReq    string
	cpuLim    string
	memReq    string
	memLim    string
}

// buildJob creates a K8s Job from the spec, using shared config (image, SA, env vars).
func (r *KubernetesRunner) buildJob(spec jobSpec) *batchv1.Job {
	envVars := []corev1.EnvVar{
		{Name: "MONGODB_URI", Value: getEnv("MONGODB_URI", "mongodb://localhost:27017")},
		{Name: "MONGODB_DB", Value: getEnv("MONGODB_DB", "decisionbox")},
	}
	for _, kv := range []struct{ key, envKey string }{
		{"SECRET_PROVIDER", "SECRET_PROVIDER"},
		{"SECRET_NAMESPACE", "SECRET_NAMESPACE"},
		{"SECRET_ENCRYPTION_KEY", "SECRET_ENCRYPTION_KEY"},
		{"SECRET_GCP_PROJECT_ID", "SECRET_GCP_PROJECT_ID"},
		{"QDRANT_URL", "QDRANT_URL"},
		{"QDRANT_API_KEY", "QDRANT_API_KEY"},
	} {
		if v := getEnv(kv.envKey, ""); v != "" {
			envVars = append(envVars, corev1.EnvVar{Name: kv.key, Value: v})
		}
	}

	backoffLimit := int32(0)

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      spec.name,
			Namespace: r.config.Namespace,
			Labels:    spec.labels,
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoffLimit,
			TTLSecondsAfterFinished: &spec.ttl,
			ActiveDeadlineSeconds:   spec.deadline,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: spec.podLabels},
				Spec: corev1.PodSpec{
					ServiceAccountName: r.config.ServiceAccountName,
					RestartPolicy:      corev1.RestartPolicyNever,
					Containers: []corev1.Container{
						{
							Name:  "agent",
							Image: r.config.AgentImage,
							Args:  spec.args,
							Env:   envVars,
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse(spec.cpuReq),
									corev1.ResourceMemory: resource.MustParse(spec.memReq),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse(spec.cpuLim),
									corev1.ResourceMemory: resource.MustParse(spec.memLim),
								},
							},
						},
					},
				},
			},
		},
	}
	return job
}

func (r *KubernetesRunner) Run(ctx context.Context, opts RunOptions) error {
	jobName := fmt.Sprintf("discovery-%s", opts.RunID[:min(len(opts.RunID), 20)])

	args := []string{
		"--project-id", opts.ProjectID,
		"--run-id", opts.RunID,
	}
	if len(opts.Areas) > 0 {
		args = append(args, "--areas", strings.Join(opts.Areas, ","))
	}
	if opts.MaxSteps > 0 {
		args = append(args, "--max-steps", strconv.Itoa(opts.MaxSteps))
	}

	job := r.buildJob(jobSpec{
		name: jobName,
		labels: map[string]string{
			"app": "decisionbox-agent", "project-id": opts.ProjectID, "run-id": opts.RunID,
		},
		podLabels: map[string]string{
			"app": "decisionbox-agent", "run-id": opts.RunID,
		},
		args:   args,
		ttl:    3600,
		cpuReq: r.config.CPURequest, cpuLim: r.config.CPULimit,
		memReq: r.config.MemoryRequest, memLim: r.config.MemoryLimit,
	})

	created, err := r.client.BatchV1().Jobs(r.config.Namespace).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		apilog.WithFields(apilog.Fields{
			"job": jobName, "namespace": r.config.Namespace, "error": err.Error(),
		}).Error("Failed to create K8s Job")
		return fmt.Errorf("failed to create K8s Job: %w", err)
	}

	apilog.WithFields(apilog.Fields{
		"job":        created.Name,
		"namespace":  r.config.Namespace,
		"image":      r.config.AgentImage,
		"run_id":     opts.RunID,
		"project_id": opts.ProjectID,
		"areas":      opts.Areas,
		"max_steps":  opts.MaxSteps,
	}).Info("K8s Job created for discovery run")

	// Watch Job completion in background to detect failures
	if opts.OnFailure != nil {
		go r.watchJob(created.Name, opts.RunID, opts.OnFailure) //nolint:gosec // intentional: long-running watcher outlives request context
	}

	return nil
}

func (r *KubernetesRunner) RunSync(ctx context.Context, opts RunSyncOptions) (*RunSyncResult, error) {
	jobName := fmt.Sprintf("test-%s-%d", opts.ProjectID[:min(len(opts.ProjectID), 12)], time.Now().UnixMilli()%100000)
	args := append([]string{"--project-id", opts.ProjectID}, opts.Args...)
	deadline := int64(60)

	job := r.buildJob(jobSpec{
		name: jobName,
		labels: map[string]string{
			"app": "decisionbox-agent", "type": "test-connection", "project-id": opts.ProjectID,
		},
		podLabels: map[string]string{
			"app": "decisionbox-agent", "type": "test-connection",
		},
		args: args, ttl: 60, deadline: &deadline,
		cpuReq: "100m", cpuLim: "500m", memReq: "128Mi", memLim: "256Mi",
	})

	if _, err := r.client.BatchV1().Jobs(r.config.Namespace).Create(ctx, job, metav1.CreateOptions{}); err != nil {
		return nil, fmt.Errorf("failed to create test Job: %w", err)
	}

	apilog.WithFields(apilog.Fields{
		"job": jobName, "project_id": opts.ProjectID,
	}).Info("Test connection K8s Job created")

	// Poll until Job completes or fails
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			j, err := r.client.BatchV1().Jobs(r.config.Namespace).Get(ctx, jobName, metav1.GetOptions{})
			if err != nil {
				continue
			}
			if j.Status.Succeeded > 0 {
				logs := r.readPodLogs(ctx, jobName)
				return &RunSyncResult{Output: logs}, nil
			}
			if j.Status.Failed > 0 {
				logs := r.readPodLogs(ctx, jobName)
				errMsg := r.getPodErrorMessage(ctx, jobName)
				if errMsg == "" {
					errMsg = "test connection job failed"
				}
				return &RunSyncResult{Output: logs, Error: errMsg}, fmt.Errorf("%s", errMsg)
			}
		}
	}
}

// readPodLogs reads stdout from a pod created by a Job.
func (r *KubernetesRunner) readPodLogs(ctx context.Context, jobName string) []byte {
	pods, err := r.client.CoreV1().Pods(r.config.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("job-name=%s", jobName),
	})
	if err != nil || len(pods.Items) == 0 {
		return nil
	}

	req := r.client.CoreV1().Pods(r.config.Namespace).GetLogs(pods.Items[0].Name, &corev1.PodLogOptions{})
	stream, err := req.Stream(ctx)
	if err != nil {
		return nil
	}
	defer stream.Close()

	var buf bytes.Buffer
	if _, err = buf.ReadFrom(stream); err != nil {
		return nil
	}
	return buf.Bytes()
}

// watchJob polls the Job status until it completes or fails.
func (r *KubernetesRunner) watchJob(jobName, runID string, onFailure func(string, string)) {
	ctx := context.Background()
	// Poll every 30s. Total ticks = timeout_hours * 120 (3600s / 30s per tick)
	maxTicks := r.config.JobTimeoutHours * 120
	ticker := newTicker(30, maxTicks)

	for range ticker {
		job, err := r.client.BatchV1().Jobs(r.config.Namespace).Get(ctx, jobName, metav1.GetOptions{})
		if err != nil {
			apilog.WithFields(apilog.Fields{
				"job": jobName, "error": err.Error(),
			}).Warn("Failed to get Job status")
			continue
		}

		// Check for failure conditions
		for _, cond := range job.Status.Conditions {
			if cond.Type == batchv1.JobFailed && cond.Status == corev1.ConditionTrue {
				errMsg := fmt.Sprintf("K8s Job failed: %s", cond.Message)
				if cond.Reason != "" {
					errMsg = fmt.Sprintf("K8s Job failed (%s): %s", cond.Reason, cond.Message)
				}

				// Try to get pod logs for more detail
				if podErr := r.getPodErrorMessage(ctx, runID); podErr != "" {
					errMsg = podErr
				}

				apilog.WithFields(apilog.Fields{
					"job": jobName, "run_id": runID, "error": errMsg,
				}).Error("Agent K8s Job failed — updating run status")
				onFailure(runID, errMsg)
				return
			}
			if cond.Type == batchv1.JobComplete && cond.Status == corev1.ConditionTrue {
				return // completed successfully
			}
		}

		// Also check if the Job has been running too long (safety net)
		if job.Status.Failed > 0 {
			errMsg := "K8s Job failed (container exited with error)"
			if podErr := r.getPodErrorMessage(ctx, runID); podErr != "" {
				errMsg = podErr
			}
			onFailure(runID, errMsg)
			return
		}
		if job.Status.Succeeded > 0 {
			return
		}
	}
}

// getPodErrorMessage tries to extract error message from the failed pod's termination message.
func (r *KubernetesRunner) getPodErrorMessage(ctx context.Context, runID string) string {
	pods, err := r.client.CoreV1().Pods(r.config.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("run-id=%s", runID),
	})
	if err != nil || len(pods.Items) == 0 {
		return ""
	}

	pod := pods.Items[0]
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Terminated != nil && cs.State.Terminated.ExitCode != 0 {
			if cs.State.Terminated.Message != "" {
				return cs.State.Terminated.Message
			}
			return fmt.Sprintf("Container exited with code %d: %s",
				cs.State.Terminated.ExitCode, cs.State.Terminated.Reason)
		}
	}
	return ""
}

// newTicker creates a channel that ticks every n seconds, up to maxTicks times.
func newTicker(intervalSec, maxTicks int) <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		defer close(ch)
		for i := 0; i < maxTicks; i++ {
			time.Sleep(time.Duration(intervalSec) * time.Second)
			ch <- struct{}{}
		}
	}()
	return ch
}

func (r *KubernetesRunner) Cancel(ctx context.Context, runID string) error {
	jobName := fmt.Sprintf("discovery-%s", runID[:min(len(runID), 20)])

	propagation := metav1.DeletePropagationForeground
	err := r.client.BatchV1().Jobs(r.config.Namespace).Delete(ctx, jobName, metav1.DeleteOptions{
		PropagationPolicy: &propagation,
	})
	if err != nil {
		apilog.WithFields(apilog.Fields{
			"job": jobName, "error": err.Error(),
		}).Warn("Failed to delete K8s Job")
		return fmt.Errorf("failed to delete K8s Job: %w", err)
	}

	apilog.WithFields(apilog.Fields{
		"job": jobName, "run_id": runID,
	}).Info("K8s Job deleted (discovery cancelled)")
	return nil
}
