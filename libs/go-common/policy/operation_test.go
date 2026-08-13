package policy

import (
	"context"
	"testing"
)

func TestStepsForEffort(t *testing.T) {
	cases := map[string]int{
		EffortLower:  20,
		EffortLow:    40,
		EffortMedium: 60,
		EffortHigh:   80,
		EffortHigher: 100,
	}
	for effort, want := range cases {
		got, ok := StepsForEffort(effort)
		if !ok || got != want {
			t.Errorf("StepsForEffort(%q) = %d, %v; want %d, true", effort, got, ok, want)
		}
		if !ValidEffort(effort) {
			t.Errorf("ValidEffort(%q) = false, want true", effort)
		}
	}
	if _, ok := StepsForEffort("extreme"); ok {
		t.Errorf("StepsForEffort(extreme) should be unrecognised")
	}
	if ValidEffort("") {
		t.Errorf("empty effort should be invalid")
	}
}

// chargingChecker is a NoopChecker that also implements OperationCharger, to
// verify ChargeIfMetered/RefundIfMetered route to a metering checker.
type chargingChecker struct {
	NoopChecker
	charged  []Operation
	refunded []string
}

func (c *chargingChecker) ChargeOperation(_ context.Context, _ string, op Operation) (*OperationCharge, error) {
	c.charged = append(c.charged, op)
	return &OperationCharge{Charged: 42, Balance: 100}, nil
}
func (c *chargingChecker) RefundOperation(_ context.Context, _, reference string) error {
	c.refunded = append(c.refunded, reference)
	return nil
}

func TestChargeIfMetered_RoutesToChargerOrNoops(t *testing.T) {
	// Default (Noop) checker doesn't implement OperationCharger → free no-op.
	RegisterChecker(NewNoopChecker())
	t.Cleanup(func() { RegisterChecker(NewNoopChecker()) })
	ch, err := ChargeIfMetered(context.Background(), "dep", Operation{Name: OpAskMessage, Reference: "r1"})
	if err != nil || ch == nil || ch.Charged != 0 {
		t.Fatalf("noop charge = %+v, err=%v; want zero charge", ch, err)
	}

	// A metering checker receives the operation.
	cc := &chargingChecker{}
	RegisterChecker(cc)
	ch, err = ChargeIfMetered(context.Background(), "dep", Operation{Name: OpDiscoveryRun, Effort: EffortMedium, Reference: "run-1"})
	if err != nil || ch.Charged != 42 {
		t.Fatalf("metered charge = %+v, err=%v; want 42", ch, err)
	}
	if len(cc.charged) != 1 || cc.charged[0].Name != OpDiscoveryRun || cc.charged[0].Effort != EffortMedium {
		t.Errorf("charged = %+v", cc.charged)
	}
	RefundIfMetered(context.Background(), "dep", "run-1")
	if len(cc.refunded) != 1 || cc.refunded[0] != "run-1" {
		t.Errorf("refunded = %+v", cc.refunded)
	}
}
