package agentplugin

import (
	"context"
	"fmt"
	"sync"
)

// A cross-datasource answer is assembled in hops: query one datasource, then
// filter another by values read out of that result. The hop is only sound if
// the field those values came from identifies the same things in both
// datasources — an order id that is an order id on both sides, not an order id
// on one and a session id on the other. Nothing in the query text says which of
// those it is, and the agent cannot tell them apart by looking.
//
// The knowledge of which fields bind two datasources is produced when a
// datasource is connected, by a component that reads both sides' fields. That
// component is not part of this repository, so this registry is the seam it
// plugs into: with a validator wired, a declared join key is checked; with none
// wired, the answer is "cannot tell", never "fine".

// JoinKeyRequest asks whether Field, as observed on the source datasource,
// binds that datasource to the target one.
type JoinKeyRequest struct {
	ProjectID string
	// SourceDatasourceID is the datasource the values were read FROM.
	SourceDatasourceID string
	// TargetDatasourceID is the datasource now being filtered by those values.
	TargetDatasourceID string
	// Field is the field name as it appeared in the source result.
	Field string
}

// JoinKeyVerdict is a validator's answer.
//
// Verified is deliberately one-sided: true means a report positively names this
// field as a key binding the two datasources. False means only that nothing
// established it — an absent report, an unindexed datasource and a report that
// lists other keys all land here, and none of them proves the join is wrong.
// Detail says which, in words a model can act on, and is what the turn shows
// instead of a verdict it has not earned.
type JoinKeyVerdict struct {
	Verified bool
	Detail   string
}

// JoinKeyValidatorFunc answers a JoinKeyRequest. An error means the validator
// could not reach its evidence; callers must treat that as "cannot tell" and
// carry on, never as a refusal — a report being unreachable is not a reason to
// fail a query the user asked for.
type JoinKeyValidatorFunc func(ctx context.Context, req JoinKeyRequest) (JoinKeyVerdict, error)

var (
	joinKeyValidatorMu   sync.RWMutex
	joinKeyValidator     JoinKeyValidatorFunc
	joinKeyValidatorName string
)

// RegisterJoinKeyValidator registers fn as THE join-key validator. Plugins call
// this from init() with a blank import. Empty name or nil fn panics, as does a
// second registration: unlike the filter registries, which compose in order,
// there is no meaningful way to combine two answers to "is this a join key" —
// picking one silently would make the verdict depend on link order.
func RegisterJoinKeyValidator(name string, fn JoinKeyValidatorFunc) {
	if name == "" {
		panic("agentplugin: RegisterJoinKeyValidator called with empty name")
	}
	if fn == nil {
		panic(fmt.Sprintf("agentplugin: RegisterJoinKeyValidator %q called with nil fn", name))
	}
	joinKeyValidatorMu.Lock()
	defer joinKeyValidatorMu.Unlock()
	if joinKeyValidator != nil {
		panic(fmt.Sprintf("agentplugin: JoinKeyValidator %q already registered (%q)", name, joinKeyValidatorName))
	}
	joinKeyValidator = fn
	joinKeyValidatorName = name
}

// ValidateJoinKey asks the registered validator about req.
//
// With no validator wired it returns an unverified verdict and no error, so
// callers need no separate "is one configured" branch: the honest answer to a
// question nothing can answer is the same as the honest answer to a question
// answered "not established".
func ValidateJoinKey(ctx context.Context, req JoinKeyRequest) (JoinKeyVerdict, error) {
	joinKeyValidatorMu.RLock()
	fn, name := joinKeyValidator, joinKeyValidatorName
	joinKeyValidatorMu.RUnlock()

	if fn == nil {
		return JoinKeyVerdict{Detail: "this deployment has no join-key report to check it against"}, nil
	}
	v, err := callValidator(ctx, fn, req)
	if err != nil {
		return JoinKeyVerdict{}, fmt.Errorf("join-key validator %q: %w", name, err)
	}
	// A validator cannot verify a join and stay silent about which one: the
	// turn quotes Detail back to the model, so an empty one would render as a
	// bare assertion. Supply the fallback rather than dropping the verdict.
	if v.Detail == "" {
		if v.Verified {
			v.Detail = "the join-key report lists it as a key between these datasources"
		} else {
			v.Detail = "the join-key report does not list it as a key between these datasources"
		}
	}
	return v, nil
}

// callValidator invokes fn, converting a panic into an error.
//
// A validator is another module's code, reached through a blank import: a nil
// map or a bad index inside it would otherwise escape into whatever goroutine
// asked. An ask turn runs in one with no recover of its own, so a single
// declared join could take the agent process down — and with it every other
// turn running on it — over a question whose honest answer is "cannot tell".
//
// This seam promises exactly three outcomes: verified, not verified, or could
// not tell. A panic must land on the third rather than outside all of them.
func callValidator(ctx context.Context, fn JoinKeyValidatorFunc, req JoinKeyRequest) (v JoinKeyVerdict, err error) {
	defer func() {
		if r := recover(); r != nil {
			v = JoinKeyVerdict{}
			err = fmt.Errorf("panicked: %v", r)
		}
	}()
	return fn(ctx, req)
}

// ResetJoinKeyValidatorForTest drops the registered validator. Test-only.
func ResetJoinKeyValidatorForTest() {
	joinKeyValidatorMu.Lock()
	defer joinKeyValidatorMu.Unlock()
	joinKeyValidator = nil
	joinKeyValidatorName = ""
}
