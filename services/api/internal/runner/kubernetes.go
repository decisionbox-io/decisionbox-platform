package runner

import (
	"context"
	"fmt"
	"strconv"
	"strings"

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

	// Build env vars from current API environment (MongoDB, LLM key, domain pack path)
	envVars := []corev1.EnvVar{
		{Name: "MONGODB_URI", Value: getEnv("MONGODB_URI", "mongodb://localhost:27017")},
		{Name: "MONGODB_DB", Value: getEnv("MONGODB_DB", "decisionbox")},
		{Name: "DOMAIN_PACK_PATH", Value: "/app/domain-packs"},
	}
	// Pass LLM_API_KEY if set
	if apiKey := getEnv("LLM_API_KEY", ""); apiKey != "" {
		envVars = append(envVars, corev1.EnvVar{Name: "LLM_API_KEY", Value: apiKey})
	}

	backoffLimit := int32(0) // no retries — agent handles its own
	ttl := int32(3600)       // clean up completed jobs after 1 hour

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: r.config.Namespace,
			Labels: map[string]string{
				"app":        "decisionbox-agent",
				"project-id": opts.ProjectID,
				"run-id":     opts.RunID,
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoffLimit,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app":    "decisionbox-agent",
						"run-id": opts.RunID,
					},
				},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{
						{
							Name:  "agent",
							Image: r.config.AgentImage,
							Args:  args,
							Env:   envVars,
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse(r.config.CPURequest),
									corev1.ResourceMemory: resource.MustParse(r.config.MemoryRequest),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse(r.config.CPULimit),
									corev1.ResourceMemory: resource.MustParse(r.config.MemoryLimit),
								},
							},
						},
					},
				},
			},
		},
	}

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

	return nil
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
