package policy

import "context"

// NoopChecker is the default Checker for self-hosted deployments. Every
// Check* method returns nil (allow); FeatureEnabled returns true
// (self-hosted users always have every feature); ObserveLLMTokens drops
// its argument on the floor.
//
// Behavior is intentionally identical to the pre-policy codebase so the
// OSS path is unchanged when no plugin registers.
type NoopChecker struct{}

// NewNoopChecker returns a NoopChecker. Exposed so callers can compare
// identity in tests if they wish.
func NewNoopChecker() *NoopChecker { return &NoopChecker{} }

func (NoopChecker) CheckCreateProject(_ context.Context, deploymentID string, intent ProjectIntent) (*Reservation, error) {
	return &Reservation{DeploymentID: deploymentID, Kind: KindProjectCreate, Subject: intent.ProjectID}, nil
}

func (NoopChecker) CheckStartDiscoveryRun(_ context.Context, deploymentID, projectID, runID string) (*Reservation, error) {
	return &Reservation{DeploymentID: deploymentID, Kind: KindDiscoveryRunStart, Subject: runID}, nil
}

func (NoopChecker) ConfirmDiscoveryRunEnded(_ context.Context, _ string, _ RunOutcome) error {
	return nil
}

func (NoopChecker) CheckAddDataSource(_ context.Context, deploymentID string) (*Reservation, error) {
	return &Reservation{DeploymentID: deploymentID, Kind: KindDataSourceAdd}, nil
}

func (NoopChecker) CheckLLMProviderAllowed(_ context.Context, _, _ string) error {
	return nil
}

func (NoopChecker) CheckRegisterUser(_ context.Context, _ string, _ UserIdentity) error {
	return nil
}

func (NoopChecker) FeatureEnabled(_ context.Context, _, _ string) (bool, error) {
	return true, nil
}

func (NoopChecker) Release(_ context.Context, _ string) error {
	return nil
}

func (NoopChecker) ObserveLLMTokens(_ context.Context, _ string, _ LLMUsageEvent) {
	// drop
}

func (NoopChecker) SyncCounters(_ context.Context, _ string, _ CounterSnapshot) {
	// drop
}
