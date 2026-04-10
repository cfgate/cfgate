package status

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestTruncateConditionMessage(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want string
	}{
		{
			name: "empty",
			msg:  "",
			want: "",
		},
		{
			name: "under limit",
			msg:  "hello",
			want: "hello",
		},
		{
			name: "exactly at limit",
			msg:  strings.Repeat("a", MaxConditionMessageLength),
			want: strings.Repeat("a", MaxConditionMessageLength),
		},
		{
			name: "one over limit",
			msg:  strings.Repeat("a", MaxConditionMessageLength+1),
			want: strings.Repeat("a", MaxConditionMessageLength-3) + "...",
		},
		{
			name: "much over limit",
			msg:  strings.Repeat("a", 100000),
			want: strings.Repeat("a", MaxConditionMessageLength-3) + "...",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateConditionMessage(tt.msg)
			if got != tt.want {
				t.Errorf("truncateConditionMessage() length = %d, want %d", len(got), len(tt.want))
			}
		})
	}
}

func TestNewCondition(t *testing.T) {
	tests := []struct {
		name       string
		condType   string
		status     metav1.ConditionStatus
		reason     string
		message    string
		generation int64
	}{
		{
			name:       "normal condition",
			condType:   ConditionTypeReady,
			status:     metav1.ConditionTrue,
			reason:     ReasonReady,
			message:    "All good.",
			generation: 1,
		},
		{
			name:       "empty message",
			condType:   ConditionTypeReady,
			status:     metav1.ConditionTrue,
			reason:     ReasonReady,
			message:    "",
			generation: 1,
		},
		{
			name:       "long message truncated",
			condType:   ConditionTypeReady,
			status:     metav1.ConditionTrue,
			reason:     ReasonReady,
			message:    strings.Repeat("x", 33000),
			generation: 1,
		},
		{
			name:       "zero generation",
			condType:   ConditionTypeReady,
			status:     metav1.ConditionTrue,
			reason:     ReasonReady,
			message:    "ok",
			generation: 0,
		},
		{
			name:       "unknown status",
			condType:   ConditionTypeReady,
			status:     metav1.ConditionUnknown,
			reason:     ReasonReconciling,
			message:    "Reconciling.",
			generation: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := time.Now()
			got := NewCondition(tt.condType, tt.status, tt.reason, tt.message, tt.generation)
			after := time.Now()

			if got.Type != tt.condType {
				t.Errorf("Type = %q, want %q", got.Type, tt.condType)
			}
			if got.Status != tt.status {
				t.Errorf("Status = %q, want %q", got.Status, tt.status)
			}
			if got.Reason != tt.reason {
				t.Errorf("Reason = %q, want %q", got.Reason, tt.reason)
			}
			if got.ObservedGeneration != tt.generation {
				t.Errorf("ObservedGeneration = %d, want %d", got.ObservedGeneration, tt.generation)
			}
			if len(got.Message) > MaxConditionMessageLength {
				t.Errorf("Message length %d exceeds max %d", len(got.Message), MaxConditionMessageLength)
			}
			if got.LastTransitionTime.Time.Before(before) || got.LastTransitionTime.After(after) {
				t.Errorf("LastTransitionTime %v not between %v and %v", got.LastTransitionTime.Time, before, after)
			}
		})
	}
}

func TestError2ConditionMsg(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "nil error",
			err:  nil,
			want: "",
		},
		{
			name: "simple error",
			err:  errors.New("connection failed"),
			want: "Connection failed.",
		},
		{
			name: "already capitalized",
			err:  errors.New("Connection failed"),
			want: "Connection failed.",
		},
		{
			name: "already has period",
			err:  errors.New("failed."),
			want: "Failed.",
		},
		{
			name: "both capitalized and period",
			err:  errors.New("Done."),
			want: "Done.",
		},
		{
			name: "empty error message",
			err:  errors.New(""),
			want: "",
		},
		{
			name: "single char",
			err:  errors.New("x"),
			want: "X.",
		},
		{
			name: "multiline",
			err:  errors.New("line1\nline2"),
			want: "Line1\nline2.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Error2ConditionMsg(tt.err)
			if got != tt.want {
				t.Errorf("Error2ConditionMsg() = %q, want %q", got, tt.want)
			}
		})
	}
}

func cond(condType string, status metav1.ConditionStatus) metav1.Condition {
	return metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             "TestReason",
		Message:            "test message",
		LastTransitionTime: metav1.NewTime(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)),
		ObservedGeneration: 1,
	}
}

func condWithGen(condType string, status metav1.ConditionStatus, gen int64) metav1.Condition {
	c := cond(condType, status)
	c.ObservedGeneration = gen
	return c
}

func condWithReasonMsg(condType string, status metav1.ConditionStatus, reason, message string) metav1.Condition {
	c := cond(condType, status)
	c.Reason = reason
	c.Message = message
	return c
}

func TestFindCondition(t *testing.T) {
	tests := []struct {
		name       string
		conditions []metav1.Condition
		condType   string
		wantNil    bool
		wantStatus metav1.ConditionStatus
	}{
		{
			name:       "nil slice",
			conditions: nil,
			condType:   ConditionTypeReady,
			wantNil:    true,
		},
		{
			name:       "empty slice",
			conditions: []metav1.Condition{},
			condType:   ConditionTypeReady,
			wantNil:    true,
		},
		{
			name:       "not found",
			conditions: []metav1.Condition{cond(ConditionTypeAccepted, metav1.ConditionTrue)},
			condType:   ConditionTypeReady,
			wantNil:    true,
		},
		{
			name:       "found",
			conditions: []metav1.Condition{cond(ConditionTypeReady, metav1.ConditionTrue)},
			condType:   ConditionTypeReady,
			wantNil:    false,
			wantStatus: metav1.ConditionTrue,
		},
		{
			name: "multiple conditions",
			conditions: []metav1.Condition{
				cond(ConditionTypeAccepted, metav1.ConditionTrue),
				cond(ConditionTypeReady, metav1.ConditionFalse),
				cond(ConditionTypeProgrammed, metav1.ConditionTrue),
			},
			condType:   ConditionTypeReady,
			wantNil:    false,
			wantStatus: metav1.ConditionFalse,
		},
		{
			name: "duplicate types returns first",
			conditions: []metav1.Condition{
				cond(ConditionTypeReady, metav1.ConditionTrue),
				cond(ConditionTypeReady, metav1.ConditionFalse),
			},
			condType:   ConditionTypeReady,
			wantNil:    false,
			wantStatus: metav1.ConditionTrue,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FindCondition(tt.conditions, tt.condType)
			if tt.wantNil {
				if got != nil {
					t.Errorf("FindCondition() = %v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatal("FindCondition() = nil, want non-nil")
				return
			}
			if got.Status != tt.wantStatus {
				t.Errorf("FindCondition().Status = %q, want %q", got.Status, tt.wantStatus)
			}
		})
	}
}

func TestRemoveCondition(t *testing.T) {
	tests := []struct {
		name       string
		conditions []metav1.Condition
		condType   string
		wantLen    int
		wantTypes  []string
	}{
		{
			name:       "nil slice",
			conditions: nil,
			condType:   ConditionTypeReady,
			wantLen:    0,
		},
		{
			name:       "not present",
			conditions: []metav1.Condition{cond(ConditionTypeAccepted, metav1.ConditionTrue)},
			condType:   ConditionTypeReady,
			wantLen:    1,
			wantTypes:  []string{ConditionTypeAccepted},
		},
		{
			name: "present among multiple",
			conditions: []metav1.Condition{
				cond(ConditionTypeReady, metav1.ConditionTrue),
				cond(ConditionTypeAccepted, metav1.ConditionTrue),
			},
			condType:  ConditionTypeReady,
			wantLen:   1,
			wantTypes: []string{ConditionTypeAccepted},
		},
		{
			name:       "only element",
			conditions: []metav1.Condition{cond(ConditionTypeReady, metav1.ConditionTrue)},
			condType:   ConditionTypeReady,
			wantLen:    0,
		},
		{
			name: "duplicate types both removed",
			conditions: []metav1.Condition{
				cond(ConditionTypeReady, metav1.ConditionTrue),
				cond(ConditionTypeReady, metav1.ConditionFalse),
			},
			condType: ConditionTypeReady,
			wantLen:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RemoveCondition(tt.conditions, tt.condType)
			if len(got) != tt.wantLen {
				t.Errorf("RemoveCondition() length = %d, want %d", len(got), tt.wantLen)
			}
			for i, wantType := range tt.wantTypes {
				if got[i].Type != wantType {
					t.Errorf("RemoveCondition()[%d].Type = %q, want %q", i, got[i].Type, wantType)
				}
			}
		})
	}

	t.Run("does not modify input", func(t *testing.T) {
		input := []metav1.Condition{
			cond(ConditionTypeReady, metav1.ConditionTrue),
			cond(ConditionTypeAccepted, metav1.ConditionTrue),
		}
		RemoveCondition(input, ConditionTypeReady)
		if len(input) != 2 {
			t.Errorf("input was modified: length = %d, want 2", len(input))
		}
	})
}

func TestConditionTrue(t *testing.T) {
	tests := []struct {
		name       string
		conditions []metav1.Condition
		condType   string
		want       bool
	}{
		{"is true", []metav1.Condition{cond(ConditionTypeReady, metav1.ConditionTrue)}, ConditionTypeReady, true},
		{"is false", []metav1.Condition{cond(ConditionTypeReady, metav1.ConditionFalse)}, ConditionTypeReady, false},
		{"is unknown", []metav1.Condition{cond(ConditionTypeReady, metav1.ConditionUnknown)}, ConditionTypeReady, false},
		{"not found", nil, ConditionTypeReady, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ConditionTrue(tt.conditions, tt.condType); got != tt.want {
				t.Errorf("ConditionTrue() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConditionFalse(t *testing.T) {
	tests := []struct {
		name       string
		conditions []metav1.Condition
		condType   string
		want       bool
	}{
		{"is false", []metav1.Condition{cond(ConditionTypeReady, metav1.ConditionFalse)}, ConditionTypeReady, true},
		{"is true", []metav1.Condition{cond(ConditionTypeReady, metav1.ConditionTrue)}, ConditionTypeReady, false},
		{"is unknown", []metav1.Condition{cond(ConditionTypeReady, metav1.ConditionUnknown)}, ConditionTypeReady, false},
		{"not found", nil, ConditionTypeReady, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ConditionFalse(tt.conditions, tt.condType); got != tt.want {
				t.Errorf("ConditionFalse() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConditionUnknown(t *testing.T) {
	tests := []struct {
		name       string
		conditions []metav1.Condition
		condType   string
		want       bool
	}{
		{"is unknown", []metav1.Condition{cond(ConditionTypeReady, metav1.ConditionUnknown)}, ConditionTypeReady, true},
		{"not found", nil, ConditionTypeReady, true},
		{"is true", []metav1.Condition{cond(ConditionTypeReady, metav1.ConditionTrue)}, ConditionTypeReady, false},
		{"is false", []metav1.Condition{cond(ConditionTypeReady, metav1.ConditionFalse)}, ConditionTypeReady, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ConditionUnknown(tt.conditions, tt.condType); got != tt.want {
				t.Errorf("ConditionUnknown() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSetCondition(t *testing.T) {
	t.Run("add to empty", func(t *testing.T) {
		got := SetCondition(nil, cond(ConditionTypeReady, metav1.ConditionTrue))
		if len(got) != 1 || got[0].Type != ConditionTypeReady {
			t.Errorf("SetCondition() = %v, want single Ready condition", got)
		}
	})

	t.Run("update existing", func(t *testing.T) {
		existing := []metav1.Condition{cond(ConditionTypeReady, metav1.ConditionFalse)}
		got := SetCondition(existing, cond(ConditionTypeReady, metav1.ConditionTrue))
		if len(got) != 1 || got[0].Status != metav1.ConditionTrue {
			t.Errorf("SetCondition() status = %v, want True", got)
		}
	})
}

func TestMergeConditions(t *testing.T) {
	fixedTime := metav1.NewTime(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))

	t.Run("both empty", func(t *testing.T) {
		got := MergeConditions(nil)
		if got != nil {
			t.Errorf("MergeConditions(nil) = %v, want nil", got)
		}
	})

	t.Run("empty conditions with update", func(t *testing.T) {
		update := cond(ConditionTypeReady, metav1.ConditionTrue)
		got := MergeConditions(nil, update)
		if len(got) != 1 {
			t.Fatalf("len = %d, want 1", len(got))
		}
		if got[0].Type != ConditionTypeReady || got[0].Status != metav1.ConditionTrue {
			t.Errorf("got %v, want Ready=True", got[0])
		}
	})

	t.Run("conditions with no updates", func(t *testing.T) {
		existing := []metav1.Condition{cond(ConditionTypeReady, metav1.ConditionTrue)}
		got := MergeConditions(existing)
		if len(got) != 1 || got[0].Type != ConditionTypeReady {
			t.Errorf("got %v, want passthrough", got)
		}
	})

	t.Run("update same status preserves LastTransitionTime", func(t *testing.T) {
		existing := []metav1.Condition{{
			Type:               ConditionTypeReady,
			Status:             metav1.ConditionTrue,
			Reason:             "OldReason",
			Message:            "old",
			LastTransitionTime: fixedTime,
			ObservedGeneration: 1,
		}}
		update := metav1.Condition{
			Type:               ConditionTypeReady,
			Status:             metav1.ConditionTrue,
			Reason:             "NewReason",
			Message:            "new",
			ObservedGeneration: 2,
		}
		got := MergeConditions(existing, update)
		if len(got) != 1 {
			t.Fatalf("len = %d, want 1", len(got))
		}
		if got[0].LastTransitionTime != fixedTime {
			t.Errorf("LastTransitionTime = %v, want preserved %v", got[0].LastTransitionTime, fixedTime)
		}
		if got[0].Reason != "NewReason" {
			t.Errorf("Reason = %q, want NewReason", got[0].Reason)
		}
	})

	t.Run("update different status changes LastTransitionTime", func(t *testing.T) {
		existing := []metav1.Condition{{
			Type:               ConditionTypeReady,
			Status:             metav1.ConditionTrue,
			Reason:             "OldReason",
			LastTransitionTime: fixedTime,
		}}
		update := metav1.Condition{
			Type:   ConditionTypeReady,
			Status: metav1.ConditionFalse,
			Reason: "NewReason",
		}
		before := time.Now()
		got := MergeConditions(existing, update)
		if got[0].LastTransitionTime.Time.Before(before) {
			t.Errorf("LastTransitionTime not updated on status change")
		}
	})

	t.Run("add new type", func(t *testing.T) {
		existing := []metav1.Condition{cond(ConditionTypeReady, metav1.ConditionTrue)}
		update := cond(ConditionTypeAccepted, metav1.ConditionTrue)
		got := MergeConditions(existing, update)
		if len(got) != 2 {
			t.Fatalf("len = %d, want 2", len(got))
		}
	})

	t.Run("duplicate updates last wins", func(t *testing.T) {
		u1 := cond(ConditionTypeReady, metav1.ConditionTrue)
		u2 := cond(ConditionTypeReady, metav1.ConditionFalse)
		got := MergeConditions(nil, u1, u2)
		if len(got) != 1 {
			t.Fatalf("len = %d, want 1", len(got))
		}
		if got[0].Status != metav1.ConditionFalse {
			t.Errorf("Status = %q, want False (last wins)", got[0].Status)
		}
	})

	t.Run("preserve unupdated conditions", func(t *testing.T) {
		existing := []metav1.Condition{
			cond("A", metav1.ConditionTrue),
			cond("B", metav1.ConditionFalse),
		}
		update := cond("A", metav1.ConditionFalse)
		got := MergeConditions(existing, update)
		if len(got) != 2 {
			t.Fatalf("len = %d, want 2", len(got))
		}
		bFound := false
		for _, c := range got {
			if c.Type == "B" {
				bFound = true
				if c.Status != metav1.ConditionFalse {
					t.Errorf("B.Status = %q, want False", c.Status)
				}
			}
		}
		if !bFound {
			t.Error("condition B not preserved")
		}
	})

	t.Run("message truncation", func(t *testing.T) {
		update := metav1.Condition{
			Type:    ConditionTypeReady,
			Status:  metav1.ConditionTrue,
			Reason:  "Test",
			Message: strings.Repeat("a", MaxConditionMessageLength+1),
		}
		got := MergeConditions(nil, update)
		if len(got[0].Message) != MaxConditionMessageLength {
			t.Errorf("message length = %d, want %d", len(got[0].Message), MaxConditionMessageLength)
		}
	})

	t.Run("ObservedGeneration zero inherits previous", func(t *testing.T) {
		existing := []metav1.Condition{condWithGen(ConditionTypeReady, metav1.ConditionTrue, 5)}
		update := condWithGen(ConditionTypeReady, metav1.ConditionTrue, 0)
		got := MergeConditions(existing, update)
		if got[0].ObservedGeneration != 5 {
			t.Errorf("ObservedGeneration = %d, want 5 (inherited)", got[0].ObservedGeneration)
		}
	})

	t.Run("ObservedGeneration nonzero overrides", func(t *testing.T) {
		existing := []metav1.Condition{condWithGen(ConditionTypeReady, metav1.ConditionTrue, 5)}
		update := condWithGen(ConditionTypeReady, metav1.ConditionTrue, 7)
		got := MergeConditions(existing, update)
		if got[0].ObservedGeneration != 7 {
			t.Errorf("ObservedGeneration = %d, want 7", got[0].ObservedGeneration)
		}
	})

	t.Run("result ordering: updates first then retained", func(t *testing.T) {
		existing := []metav1.Condition{
			cond("A", metav1.ConditionTrue),
			cond("B", metav1.ConditionTrue),
			cond("C", metav1.ConditionTrue),
		}
		got := MergeConditions(existing, cond("A", metav1.ConditionFalse), cond("D", metav1.ConditionTrue))
		if len(got) != 4 {
			t.Fatalf("len = %d, want 4", len(got))
		}
		if got[0].Type != "A" || got[1].Type != "D" {
			t.Errorf("updates should come first: got [%s, %s, ...], want [A, D, ...]", got[0].Type, got[1].Type)
		}
		if got[2].Type != "B" || got[3].Type != "C" {
			t.Errorf("retained should come after: got [..., %s, %s], want [..., B, C]", got[2].Type, got[3].Type)
		}
	})
}

func TestSimpleConditionConstructors(t *testing.T) {
	type constructorFunc func(bool, string, string, int64) metav1.Condition

	tests := []struct {
		name     string
		fn       constructorFunc
		wantType string
	}{
		{"NewCredentialsValidCondition", NewCredentialsValidCondition, ConditionTypeCredentialsValid},
		{"NewTunnelCreatedCondition", NewTunnelCreatedCondition, ConditionTypeTunnelCreated},
		{"NewTunnelConfiguredCondition", NewTunnelConfiguredCondition, ConditionTypeTunnelConfigured},
		{"NewDeploymentReadyCondition", NewDeploymentReadyCondition, ConditionTypeDeploymentReady},
		{"NewZonesResolvedCondition", NewZonesResolvedCondition, ConditionTypeZonesResolved},
		{"NewRecordsSyncedCondition", NewRecordsSyncedCondition, ConditionTypeRecordsSynced},
		{"NewOwnershipVerifiedCondition", NewOwnershipVerifiedCondition, ConditionTypeOwnershipVerified},
		{"NewTargetsResolvedCondition", NewTargetsResolvedCondition, ConditionTypeTargetsResolved},
		{"NewApplicationCreatedCondition", NewApplicationCreatedCondition, ConditionTypeApplicationCreated},
		{"NewPoliciesAttachedCondition", NewPoliciesAttachedCondition, ConditionTypePoliciesAttached},
		{"NewServiceTokensReadyCondition", NewServiceTokensReadyCondition, ConditionTypeServiceTokensReady},
		{"NewPolicyAcceptedCondition", NewPolicyAcceptedCondition, PolicyConditionAccepted},
	}

	for _, tt := range tests {
		t.Run(tt.name+"/true", func(t *testing.T) {
			got := tt.fn(true, "TestReason", "test msg", 1)
			if got.Type != tt.wantType {
				t.Errorf("Type = %q, want %q", got.Type, tt.wantType)
			}
			if got.Status != metav1.ConditionTrue {
				t.Errorf("Status = %q, want True", got.Status)
			}
		})

		t.Run(tt.name+"/false", func(t *testing.T) {
			got := tt.fn(false, "TestReason", "test msg", 1)
			if got.Type != tt.wantType {
				t.Errorf("Type = %q, want %q", got.Type, tt.wantType)
			}
			if got.Status != metav1.ConditionFalse {
				t.Errorf("Status = %q, want False", got.Status)
			}
		})
	}
}

func allTrueConditions(types ...string) []metav1.Condition {
	conditions := make([]metav1.Condition, len(types))
	for i, t := range types {
		conditions[i] = condWithReasonMsg(t, metav1.ConditionTrue, "Ready", "All good.")
	}
	return conditions
}

func setConditionStatus(conditions []metav1.Condition, condType string, status metav1.ConditionStatus, reason, message string) []metav1.Condition {
	result := make([]metav1.Condition, len(conditions))
	copy(result, conditions)
	for i := range result {
		if result[i].Type == condType {
			result[i].Status = status
			result[i].Reason = reason
			result[i].Message = message
		}
	}
	return result
}

func TestNewTunnelReadyCondition(t *testing.T) {
	tunnelSubConditions := []string{
		ConditionTypeCredentialsValid,
		ConditionTypeTunnelCreated,
		ConditionTypeTunnelConfigured,
		ConditionTypeDeploymentReady,
	}

	tests := []struct {
		name       string
		conditions []metav1.Condition
		wantStatus metav1.ConditionStatus
		wantReason string
	}{
		{
			name:       "all true",
			conditions: allTrueConditions(tunnelSubConditions...),
			wantStatus: metav1.ConditionTrue,
			wantReason: ReasonReconcileSuccess,
		},
		{
			name:       "credentials false",
			conditions: setConditionStatus(allTrueConditions(tunnelSubConditions...), ConditionTypeCredentialsValid, metav1.ConditionFalse, "CredsFailed", "creds bad"),
			wantStatus: metav1.ConditionFalse,
			wantReason: "CredsFailed",
		},
		{
			name:       "tunnel not created",
			conditions: setConditionStatus(allTrueConditions(tunnelSubConditions...), ConditionTypeTunnelCreated, metav1.ConditionFalse, "TunnelFailed", "tunnel bad"),
			wantStatus: metav1.ConditionFalse,
			wantReason: "TunnelFailed",
		},
		{
			name:       "tunnel not configured",
			conditions: setConditionStatus(allTrueConditions(tunnelSubConditions...), ConditionTypeTunnelConfigured, metav1.ConditionFalse, "ConfigFailed", "config bad"),
			wantStatus: metav1.ConditionFalse,
			wantReason: "ConfigFailed",
		},
		{
			name:       "deployment not ready",
			conditions: setConditionStatus(allTrueConditions(tunnelSubConditions...), ConditionTypeDeploymentReady, metav1.ConditionFalse, "DeployFailed", "deploy bad"),
			wantStatus: metav1.ConditionFalse,
			wantReason: "DeployFailed",
		},
		{
			name: "all false reports first failure",
			conditions: func() []metav1.Condition {
				c := allTrueConditions(tunnelSubConditions...)
				for i := range c {
					c[i].Status = metav1.ConditionFalse
					c[i].Reason = c[i].Type + "Failed"
				}
				return c
			}(),
			wantStatus: metav1.ConditionFalse,
			wantReason: ConditionTypeCredentialsValid + "Failed",
		},
		{
			name:       "credentials unknown treated as not true",
			conditions: setConditionStatus(allTrueConditions(tunnelSubConditions...), ConditionTypeCredentialsValid, metav1.ConditionUnknown, "CredsUnknown", "checking"),
			wantStatus: metav1.ConditionFalse,
			wantReason: "CredsUnknown",
		},
		{
			name:       "empty conditions",
			conditions: nil,
			wantStatus: metav1.ConditionUnknown,
			wantReason: ReasonReconciling,
		},
		{
			name:       "partial: only credentials",
			conditions: allTrueConditions(ConditionTypeCredentialsValid),
			wantStatus: metav1.ConditionUnknown,
			wantReason: ReasonReconciling,
		},
		{
			name:       "partial: two of four",
			conditions: allTrueConditions(ConditionTypeCredentialsValid, ConditionTypeTunnelCreated),
			wantStatus: metav1.ConditionUnknown,
			wantReason: ReasonReconciling,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NewTunnelReadyCondition(tt.conditions, 1)
			if got.Status != tt.wantStatus {
				t.Errorf("Status = %q, want %q", got.Status, tt.wantStatus)
			}
			if got.Reason != tt.wantReason {
				t.Errorf("Reason = %q, want %q", got.Reason, tt.wantReason)
			}
			if got.Type != ConditionTypeReady {
				t.Errorf("Type = %q, want %q", got.Type, ConditionTypeReady)
			}
		})
	}
}

func TestNewDNSReadyCondition(t *testing.T) {
	dnsSubConditions := []string{
		ConditionTypeCredentialsValid,
		ConditionTypeZonesResolved,
		ConditionTypeRecordsSynced,
	}

	tests := []struct {
		name       string
		conditions []metav1.Condition
		wantStatus metav1.ConditionStatus
		wantReason string
	}{
		{
			name:       "all true",
			conditions: allTrueConditions(dnsSubConditions...),
			wantStatus: metav1.ConditionTrue,
			wantReason: ReasonReconcileSuccess,
		},
		{
			name:       "credentials false",
			conditions: setConditionStatus(allTrueConditions(dnsSubConditions...), ConditionTypeCredentialsValid, metav1.ConditionFalse, "CredsFailed", "bad"),
			wantStatus: metav1.ConditionFalse,
			wantReason: "CredsFailed",
		},
		{
			name:       "zones not resolved",
			conditions: setConditionStatus(allTrueConditions(dnsSubConditions...), ConditionTypeZonesResolved, metav1.ConditionFalse, "ZoneFailed", "bad"),
			wantStatus: metav1.ConditionFalse,
			wantReason: "ZoneFailed",
		},
		{
			name:       "records not synced",
			conditions: setConditionStatus(allTrueConditions(dnsSubConditions...), ConditionTypeRecordsSynced, metav1.ConditionFalse, "SyncFailed", "bad"),
			wantStatus: metav1.ConditionFalse,
			wantReason: "SyncFailed",
		},
		{
			name:       "empty conditions",
			conditions: nil,
			wantStatus: metav1.ConditionUnknown,
			wantReason: ReasonReconciling,
		},
		{
			name: "all false reports first",
			conditions: func() []metav1.Condition {
				c := allTrueConditions(dnsSubConditions...)
				for i := range c {
					c[i].Status = metav1.ConditionFalse
					c[i].Reason = c[i].Type + "Failed"
				}
				return c
			}(),
			wantStatus: metav1.ConditionFalse,
			wantReason: ConditionTypeCredentialsValid + "Failed",
		},
		{
			name:       "partial: only credentials",
			conditions: allTrueConditions(ConditionTypeCredentialsValid),
			wantStatus: metav1.ConditionUnknown,
			wantReason: ReasonReconciling,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NewDNSReadyCondition(tt.conditions, 1)
			if got.Status != tt.wantStatus {
				t.Errorf("Status = %q, want %q", got.Status, tt.wantStatus)
			}
			if got.Reason != tt.wantReason {
				t.Errorf("Reason = %q, want %q", got.Reason, tt.wantReason)
			}
			if got.Type != ConditionTypeReady {
				t.Errorf("Type = %q, want %q", got.Type, ConditionTypeReady)
			}
		})
	}
}

func TestNewAccessPolicyReadyCondition(t *testing.T) {
	accessSubConditions := []string{
		ConditionTypeCredentialsValid,
		ConditionTypeTargetsResolved,
		ConditionTypeApplicationCreated,
		ConditionTypePoliciesAttached,
	}

	accessWithST := append(append([]string{}, accessSubConditions...), ConditionTypeServiceTokensReady)

	tests := []struct {
		name             string
		conditions       []metav1.Condition
		hasServiceTokens bool
		wantStatus       metav1.ConditionStatus
		wantReason       string
	}{
		{
			name:             "all true without service tokens",
			conditions:       allTrueConditions(accessSubConditions...),
			hasServiceTokens: false,
			wantStatus:       metav1.ConditionTrue,
			wantReason:       ReasonReconcileSuccess,
		},
		{
			name:             "all true with service tokens",
			conditions:       allTrueConditions(accessWithST...),
			hasServiceTokens: true,
			wantStatus:       metav1.ConditionTrue,
			wantReason:       ReasonReconcileSuccess,
		},
		{
			name:             "service tokens false when required",
			conditions:       setConditionStatus(allTrueConditions(accessWithST...), ConditionTypeServiceTokensReady, metav1.ConditionFalse, "STFailed", "bad"),
			hasServiceTokens: true,
			wantStatus:       metav1.ConditionFalse,
			wantReason:       "STFailed",
		},
		{
			name: "service tokens false but not required",
			conditions: append(
				allTrueConditions(accessSubConditions...),
				condWithReasonMsg(ConditionTypeServiceTokensReady, metav1.ConditionFalse, "STFailed", "bad"),
			),
			hasServiceTokens: false,
			wantStatus:       metav1.ConditionTrue,
			wantReason:       ReasonReconcileSuccess,
		},
		{
			// When ServiceTokensReady is missing (not set yet), the ready check
			// fails but the for-loop can't find a failing condition to report,
			// so it falls through to Unknown/Reconciling.
			name:             "service tokens missing when required",
			conditions:       allTrueConditions(accessSubConditions...),
			hasServiceTokens: true,
			wantStatus:       metav1.ConditionUnknown,
			wantReason:       ReasonReconciling,
		},
		{
			name:             "credentials false",
			conditions:       setConditionStatus(allTrueConditions(accessSubConditions...), ConditionTypeCredentialsValid, metav1.ConditionFalse, "CredsFailed", "bad"),
			hasServiceTokens: false,
			wantStatus:       metav1.ConditionFalse,
			wantReason:       "CredsFailed",
		},
		{
			name:             "targets not resolved",
			conditions:       setConditionStatus(allTrueConditions(accessSubConditions...), ConditionTypeTargetsResolved, metav1.ConditionFalse, "TRFailed", "bad"),
			hasServiceTokens: false,
			wantStatus:       metav1.ConditionFalse,
			wantReason:       "TRFailed",
		},
		{
			name:             "app not created",
			conditions:       setConditionStatus(allTrueConditions(accessSubConditions...), ConditionTypeApplicationCreated, metav1.ConditionFalse, "AppFailed", "bad"),
			hasServiceTokens: false,
			wantStatus:       metav1.ConditionFalse,
			wantReason:       "AppFailed",
		},
		{
			name:             "policies not attached",
			conditions:       setConditionStatus(allTrueConditions(accessSubConditions...), ConditionTypePoliciesAttached, metav1.ConditionFalse, "PAFailed", "bad"),
			hasServiceTokens: false,
			wantStatus:       metav1.ConditionFalse,
			wantReason:       "PAFailed",
		},
		{
			name:             "empty conditions without service tokens",
			conditions:       nil,
			hasServiceTokens: false,
			wantStatus:       metav1.ConditionUnknown,
			wantReason:       ReasonReconciling,
		},
		{
			name:             "empty conditions with service tokens",
			conditions:       nil,
			hasServiceTokens: true,
			wantStatus:       metav1.ConditionUnknown,
			wantReason:       ReasonReconciling,
		},
		{
			name: "all false with service tokens reports first",
			conditions: func() []metav1.Condition {
				c := allTrueConditions(accessWithST...)
				for i := range c {
					c[i].Status = metav1.ConditionFalse
					c[i].Reason = c[i].Type + "Failed"
				}
				return c
			}(),
			hasServiceTokens: true,
			wantStatus:       metav1.ConditionFalse,
			wantReason:       ConditionTypeCredentialsValid + "Failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NewAccessPolicyReadyCondition(tt.conditions, tt.hasServiceTokens, 1)
			if got.Status != tt.wantStatus {
				t.Errorf("Status = %q, want %q", got.Status, tt.wantStatus)
			}
			if tt.wantReason != "" && got.Reason != tt.wantReason {
				t.Errorf("Reason = %q, want %q", got.Reason, tt.wantReason)
			}
			if got.Type != ConditionTypeReady {
				t.Errorf("Type = %q, want %q", got.Type, ConditionTypeReady)
			}
		})
	}
}

func TestLogConditionChange(t *testing.T) {
	// logr.Discard() ensures no panics; we verify the branch logic by calling both paths.
	t.Run("different status does not panic", func(t *testing.T) {
		LogConditionChange(discardLogger(), "tunnel", ConditionTypeReady, metav1.ConditionTrue, metav1.ConditionFalse, "Error")
	})

	t.Run("same status does not panic", func(t *testing.T) {
		LogConditionChange(discardLogger(), "tunnel", ConditionTypeReady, metav1.ConditionTrue, metav1.ConditionTrue, "Ok")
	})
}

func TestLogStatusUpdate(t *testing.T) {
	t.Run("does not panic", func(t *testing.T) {
		LogStatusUpdate(discardLogger(), "tunnel", []metav1.Condition{
			cond(ConditionTypeReady, metav1.ConditionTrue),
		})
	})
}

func discardLogger() logr.Logger {
	return logr.Discard()
}
