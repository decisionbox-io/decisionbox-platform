package agentplugin

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func okValidator(v JoinKeyVerdict) JoinKeyValidatorFunc {
	return func(context.Context, JoinKeyRequest) (JoinKeyVerdict, error) { return v, nil }
}

func TestValidateJoinKey_NoValidator_IsUnverifiedNotAnError(t *testing.T) {
	defer ResetJoinKeyValidatorForTest()
	ResetJoinKeyValidatorForTest()

	v, err := ValidateJoinKey(context.Background(), JoinKeyRequest{Field: "order_id"})
	if err != nil {
		// An unwired seam must not look like a failure: the caller would then
		// have to decide whether to fail the query, which is exactly the
		// decision this design removes.
		t.Fatalf("unexpected error: %v", err)
	}
	if v.Verified {
		t.Fatal("nothing is wired, so nothing can be verified")
	}
	if v.Detail == "" {
		t.Fatal("an unverified verdict must say why; the turn quotes it to the model")
	}
}

func TestValidateJoinKey_UsesTheRegisteredValidator(t *testing.T) {
	defer ResetJoinKeyValidatorForTest()
	ResetJoinKeyValidatorForTest()

	var got JoinKeyRequest
	RegisterJoinKeyValidator("test-report", func(_ context.Context, req JoinKeyRequest) (JoinKeyVerdict, error) {
		got = req
		return JoinKeyVerdict{Verified: true, Detail: "listed as a shared order key"}, nil
	})

	want := JoinKeyRequest{ProjectID: "p1", SourceDatasourceID: "wh_a", TargetDatasourceID: "wh_b", Field: "order_id"}
	v, err := ValidateJoinKey(context.Background(), want)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != want {
		t.Fatalf("validator saw %+v, want %+v", got, want)
	}
	if !v.Verified || v.Detail != "listed as a shared order key" {
		t.Fatalf("verdict = %+v, want the validator's own answer verbatim", v)
	}
}

func TestValidateJoinKey_FillsInAMissingDetail(t *testing.T) {
	for _, tc := range []struct {
		name     string
		verified bool
		want     string
	}{
		{"verified", true, "lists it"},
		{"unverified", false, "does not list it"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer ResetJoinKeyValidatorForTest()
			ResetJoinKeyValidatorForTest()
			RegisterJoinKeyValidator("silent", okValidator(JoinKeyVerdict{Verified: tc.verified}))

			v, err := ValidateJoinKey(context.Background(), JoinKeyRequest{Field: "order_id"})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if v.Verified != tc.verified {
				t.Fatalf("Verified = %v, want %v", v.Verified, tc.verified)
			}
			if !strings.Contains(v.Detail, tc.want) {
				t.Fatalf("Detail = %q, want it to contain %q", v.Detail, tc.want)
			}
		})
	}
}

func TestValidateJoinKey_ValidatorErrorIsWrappedAndNamed(t *testing.T) {
	defer ResetJoinKeyValidatorForTest()
	ResetJoinKeyValidatorForTest()

	sentinel := errors.New("schema cache unreachable")
	RegisterJoinKeyValidator("the-report", func(context.Context, JoinKeyRequest) (JoinKeyVerdict, error) {
		return JoinKeyVerdict{Verified: true, Detail: "ignore me"}, sentinel
	})

	v, err := ValidateJoinKey(context.Background(), JoinKeyRequest{Field: "order_id"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want it to wrap the validator's own error", err)
	}
	if !strings.Contains(err.Error(), "the-report") {
		t.Fatalf("error = %q, want it to name the validator", err.Error())
	}
	// A verdict returned alongside an error must not be usable: an errored
	// validator did not answer the question.
	if v.Verified {
		t.Fatal("an errored validator must never yield a verified verdict")
	}
}

func TestRegisterJoinKeyValidator_Rejects(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func()
	}{
		{"empty name", func() { RegisterJoinKeyValidator("", okValidator(JoinKeyVerdict{})) }},
		{"nil fn", func() { RegisterJoinKeyValidator("x", nil) }},
		{"double registration", func() {
			RegisterJoinKeyValidator("first", okValidator(JoinKeyVerdict{}))
			RegisterJoinKeyValidator("second", okValidator(JoinKeyVerdict{}))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer ResetJoinKeyValidatorForTest()
			ResetJoinKeyValidatorForTest()
			defer func() {
				if recover() == nil {
					t.Fatalf("%s must panic", tc.name)
				}
			}()
			tc.call()
		})
	}
}

func TestResetJoinKeyValidatorForTest_AllowsReRegistration(t *testing.T) {
	defer ResetJoinKeyValidatorForTest()
	ResetJoinKeyValidatorForTest()

	RegisterJoinKeyValidator("first", okValidator(JoinKeyVerdict{Verified: true, Detail: "a"}))
	ResetJoinKeyValidatorForTest()
	RegisterJoinKeyValidator("second", okValidator(JoinKeyVerdict{Verified: true, Detail: "b"}))

	v, err := ValidateJoinKey(context.Background(), JoinKeyRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.Detail != "b" {
		t.Fatalf("Detail = %q, want the re-registered validator's answer", v.Detail)
	}
}

func TestValidateJoinKey_ValidatorPanicBecomesAnError(t *testing.T) {
	defer ResetJoinKeyValidatorForTest()
	ResetJoinKeyValidatorForTest()

	RegisterJoinKeyValidator("exploding", func(context.Context, JoinKeyRequest) (JoinKeyVerdict, error) {
		var m map[string]string
		m["boom"] = "" // assignment to entry in nil map
		return JoinKeyVerdict{Verified: true}, nil
	})

	// The point of the test is that this call RETURNS at all: an ask turn runs
	// in a goroutine with no recover, so an escaping panic takes the whole
	// agent process down over a question whose honest answer is "cannot tell".
	v, err := ValidateJoinKey(context.Background(), JoinKeyRequest{Field: "order_id"})
	if err == nil {
		t.Fatal("a panicking validator must surface as an error")
	}
	if !strings.Contains(err.Error(), "panicked") || !strings.Contains(err.Error(), "exploding") {
		t.Fatalf("error = %q, want it to name the validator and say it panicked", err.Error())
	}
	if v.Verified {
		t.Fatal("a validator that panicked answered nothing; it must never yield a verified verdict")
	}
}

func TestValidateJoinKey_SurvivesAPanicAndKeepsWorking(t *testing.T) {
	defer ResetJoinKeyValidatorForTest()
	ResetJoinKeyValidatorForTest()

	calls := 0
	RegisterJoinKeyValidator("flaky", func(_ context.Context, req JoinKeyRequest) (JoinKeyVerdict, error) {
		calls++
		if req.Field == "bad" {
			panic("boom")
		}
		return JoinKeyVerdict{Verified: true, Detail: "fine"}, nil
	})

	if _, err := ValidateJoinKey(context.Background(), JoinKeyRequest{Field: "bad"}); err == nil {
		t.Fatal("want an error for the panicking call")
	}
	// The registry must not be left in a broken state by the recover.
	v, err := ValidateJoinKey(context.Background(), JoinKeyRequest{Field: "good"})
	if err != nil || !v.Verified {
		t.Fatalf("v=%+v err=%v; a later call must still be answered", v, err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}
