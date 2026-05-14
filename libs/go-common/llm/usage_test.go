package llm

import "testing"

func TestUsageAccumulator_ZeroValueTotals(t *testing.T) {
	var u UsageAccumulator
	in, out := u.Totals()
	if in != 0 || out != 0 {
		t.Fatalf("zero accumulator: got (%d, %d), want (0, 0)", in, out)
	}
}

func TestUsageAccumulator_SingleAdd(t *testing.T) {
	var u UsageAccumulator
	u.Add(100, 50)
	in, out := u.Totals()
	if in != 100 || out != 50 {
		t.Fatalf("single Add: got (%d, %d), want (100, 50)", in, out)
	}
}

func TestUsageAccumulator_ManyAdds(t *testing.T) {
	var u UsageAccumulator
	u.Add(100, 50)
	u.Add(200, 75)
	u.Add(300, 125)
	in, out := u.Totals()
	if in != 600 || out != 250 {
		t.Fatalf("many Adds: got (%d, %d), want (600, 250)", in, out)
	}
}

func TestUsageAccumulator_AddZero(t *testing.T) {
	var u UsageAccumulator
	u.Add(0, 0)
	u.Add(10, 5)
	u.Add(0, 0)
	in, out := u.Totals()
	if in != 10 || out != 5 {
		t.Fatalf("Add(0,0) should be a no-op: got (%d, %d), want (10, 5)", in, out)
	}
}

func TestUsageAccumulator_AddNegativeClampsToZero(t *testing.T) {
	// Defensive: a misreporting provider must not drive the running
	// totals below the calls that succeeded.
	var u UsageAccumulator
	u.Add(100, 50)
	u.Add(-25, -10)
	in, out := u.Totals()
	if in != 100 || out != 50 {
		t.Fatalf("Add with negatives should clamp: got (%d, %d), want (100, 50)", in, out)
	}
}

func TestUsageAccumulator_MixedSignsClampPerField(t *testing.T) {
	// Input negative is clamped; output positive still counts.
	var u UsageAccumulator
	u.Add(-5, 20)
	u.Add(10, -3)
	in, out := u.Totals()
	if in != 10 || out != 20 {
		t.Fatalf("mixed-sign clamp: got (%d, %d), want (10, 20)", in, out)
	}
}

func TestUsageAccumulator_TotalsIsIdempotent(t *testing.T) {
	// Totals must not mutate the accumulator — repeated reads give the
	// same answer.
	var u UsageAccumulator
	u.Add(50, 25)
	in1, out1 := u.Totals()
	in2, out2 := u.Totals()
	if in1 != in2 || out1 != out2 {
		t.Fatalf("Totals not idempotent: first (%d,%d) second (%d,%d)", in1, out1, in2, out2)
	}
	if in1 != 50 || out1 != 25 {
		t.Fatalf("Totals: got (%d, %d), want (50, 25)", in1, out1)
	}
}
