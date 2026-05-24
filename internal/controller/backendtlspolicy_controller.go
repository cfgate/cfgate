package controller

import (
	"context"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gateway "sigs.k8s.io/gateway-api/apis/v1"

	"cfgate.io/cfgate/internal/cloudflared"
	"cfgate.io/cfgate/internal/controller/status"
)

// BackendTLSPolicyReconciler validates Gateway API BackendTLSPolicy objects for cfgate.
type BackendTLSPolicyReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=backendtlspolicies,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=backendtlspolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=services;configmaps,verbs=get;list;watch

func (r *BackendTLSPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var policy gateway.BackendTLSPolicy
	if err := r.Get(ctx, req.NamespacedName, &policy); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get BackendTLSPolicy: %w", err)
	}
	accepted, acceptedReason, acceptedMessage, resolved, resolvedReason, resolvedMessage := r.evaluateBackendTLSPolicy(ctx, &policy)
	policy.Status.Ancestors = []gateway.PolicyAncestorStatus{{
		AncestorRef:    backendTLSPolicyAncestorRef(&policy),
		ControllerName: gateway.GatewayController(GatewayControllerName),
		Conditions: []metav1.Condition{
			status.NewCondition(string(gateway.PolicyConditionAccepted), conditionStatus(accepted), string(acceptedReason), acceptedMessage, policy.Generation),
			status.NewCondition(string(gateway.BackendTLSPolicyConditionResolvedRefs), conditionStatus(resolved), string(resolvedReason), resolvedMessage, policy.Generation),
		},
	}}
	if err := r.Status().Update(ctx, &policy); err != nil {
		return ctrl.Result{RequeueAfter: requeueAfterError}, nil
	}
	return ctrl.Result{}, nil
}

func (r *BackendTLSPolicyReconciler) evaluateBackendTLSPolicy(ctx context.Context, policy *gateway.BackendTLSPolicy) (bool, gateway.PolicyConditionReason, string, bool, gateway.PolicyConditionReason, string) {
	if len(policy.Spec.TargetRefs) != 1 {
		return false, gateway.PolicyReasonInvalid, "cfgate supports exactly one BackendTLSPolicy targetRef.", false, gateway.BackendTLSPolicyReasonInvalidKind, "cfgate supports exactly one BackendTLSPolicy targetRef."
	}
	ref := policy.Spec.TargetRefs[0]
	if string(ref.Group) != "" && string(ref.Group) != "core" {
		return false, gateway.PolicyReasonInvalid, "cfgate supports only core Service BackendTLSPolicy targets.", false, gateway.BackendTLSPolicyReasonInvalidKind, "targetRef group must be core."
	}
	if string(ref.Kind) != "Service" {
		return false, gateway.PolicyReasonInvalid, "cfgate supports only Service BackendTLSPolicy targets.", false, gateway.BackendTLSPolicyReasonInvalidKind, "targetRef kind must be Service."
	}
	if ref.SectionName != nil && *ref.SectionName != "" {
		return false, gateway.PolicyReasonInvalid, "cfgate does not support BackendTLSPolicy sectionName in v0.3.0-alpha.1.", true, gateway.BackendTLSPolicyReasonResolvedRefs, "References resolved."
	}
	var svc corev1.Service
	if err := r.Get(ctx, types.NamespacedName{Namespace: policy.Namespace, Name: string(ref.Name)}, &svc); err != nil {
		return false, gateway.PolicyReasonTargetNotFound, "target Service was not found.", false, gateway.BackendTLSPolicyReasonInvalidKind, "target Service was not found."
	}
	if r.backendTLSPolicyConflicted(ctx, policy) {
		return false, gateway.PolicyReasonConflicted, "An older BackendTLSPolicy targets the same Service.", true, gateway.BackendTLSPolicyReasonResolvedRefs, "References resolved."
	}
	if len(policy.Spec.Options) > 0 {
		return false, gateway.PolicyReasonInvalid, "cfgate does not support BackendTLSPolicy options in v0.3.0-alpha.1.", true, gateway.BackendTLSPolicyReasonResolvedRefs, "References resolved."
	}
	if len(policy.Spec.Validation.SubjectAltNames) > 0 {
		return false, gateway.PolicyReasonInvalid, "cfgate does not support BackendTLSPolicy subjectAltNames in v0.3.0-alpha.1.", true, gateway.BackendTLSPolicyReasonResolvedRefs, "References resolved."
	}
	if policy.Spec.Validation.WellKnownCACertificates != nil {
		if *policy.Spec.Validation.WellKnownCACertificates != gateway.WellKnownCACertificatesSystem {
			return false, gateway.PolicyReasonInvalid, "cfgate supports only wellKnownCACertificates: System.", true, gateway.BackendTLSPolicyReasonResolvedRefs, "References resolved."
		}
		return true, gateway.PolicyReasonAccepted, "BackendTLSPolicy accepted.", true, gateway.BackendTLSPolicyReasonResolvedRefs, "References resolved."
	}
	if len(policy.Spec.Validation.CACertificateRefs) != 1 {
		return false, gateway.BackendTLSPolicyReasonNoValidCACertificate, "cfgate supports exactly one CA ConfigMap ref.", false, gateway.BackendTLSPolicyReasonInvalidCACertificateRef, "exactly one CA ConfigMap ref is required."
	}
	caRef := policy.Spec.Validation.CACertificateRefs[0]
	if string(caRef.Group) != "" || string(caRef.Kind) != "ConfigMap" {
		return false, gateway.BackendTLSPolicyReasonNoValidCACertificate, "CA ref must be a core ConfigMap.", false, gateway.BackendTLSPolicyReasonInvalidKind, "CA ref must be a core ConfigMap."
	}
	var cm corev1.ConfigMap
	if err := r.Get(ctx, types.NamespacedName{Namespace: policy.Namespace, Name: string(caRef.Name)}, &cm); err != nil {
		return false, gateway.BackendTLSPolicyReasonNoValidCACertificate, "CA ConfigMap was not found.", false, gateway.BackendTLSPolicyReasonInvalidCACertificateRef, "CA ConfigMap was not found."
	}
	if _, ok := cm.Data[cloudflared.DefaultOriginCAPoolSecretKey]; !ok {
		return false, gateway.BackendTLSPolicyReasonNoValidCACertificate, "CA ConfigMap missing ca.crt.", false, gateway.BackendTLSPolicyReasonInvalidCACertificateRef, "CA ConfigMap missing ca.crt."
	}
	return true, gateway.PolicyReasonAccepted, "BackendTLSPolicy accepted.", true, gateway.BackendTLSPolicyReasonResolvedRefs, "References resolved."
}

func (r *BackendTLSPolicyReconciler) backendTLSPolicyConflicted(ctx context.Context, policy *gateway.BackendTLSPolicy) bool {
	if len(policy.Spec.TargetRefs) != 1 {
		return false
	}
	target := policy.Spec.TargetRefs[0]
	var policies gateway.BackendTLSPolicyList
	if err := r.List(ctx, &policies, client.InNamespace(policy.Namespace)); err != nil {
		return false
	}
	var matches []gateway.BackendTLSPolicy
	for _, candidate := range policies.Items {
		if len(candidate.Spec.TargetRefs) != 1 {
			continue
		}
		candidateTarget := candidate.Spec.TargetRefs[0]
		if candidateTarget.Group == target.Group &&
			candidateTarget.Kind == target.Kind &&
			candidateTarget.Name == target.Name &&
			((candidateTarget.SectionName == nil && target.SectionName == nil) ||
				(candidateTarget.SectionName != nil && target.SectionName != nil && *candidateTarget.SectionName == *target.SectionName)) {
			matches = append(matches, candidate)
		}
	}
	if len(matches) <= 1 {
		return false
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if !matches[i].CreationTimestamp.Equal(&matches[j].CreationTimestamp) {
			return matches[i].CreationTimestamp.Before(&matches[j].CreationTimestamp)
		}
		return matches[i].Namespace+"/"+matches[i].Name < matches[j].Namespace+"/"+matches[j].Name
	})
	return matches[0].Namespace != policy.Namespace || matches[0].Name != policy.Name
}

func backendTLSPolicyAncestorRef(policy *gateway.BackendTLSPolicy) gateway.ParentReference {
	if len(policy.Spec.TargetRefs) == 0 {
		ns := gateway.Namespace(policy.Namespace)
		group := gateway.Group("")
		kind := gateway.Kind("Service")
		return gateway.ParentReference{
			Group:     &group,
			Kind:      &kind,
			Namespace: &ns,
			Name:      gateway.ObjectName(policy.Name),
		}
	}
	ref := policy.Spec.TargetRefs[0]
	ns := gateway.Namespace(policy.Namespace)
	return gateway.ParentReference{
		Group:       &ref.Group,
		Kind:        &ref.Kind,
		Namespace:   &ns,
		Name:        gateway.ObjectName(ref.Name),
		SectionName: ref.SectionName,
	}
}

func (r *BackendTLSPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&gateway.BackendTLSPolicy{}, builder.WithPredicates(GenerationOrDeletionPredicate)).
		Watches(&corev1.Service{}, handler.EnqueueRequestsFromMapFunc(r.findBackendTLSPoliciesForService), builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&corev1.ConfigMap{}, handler.EnqueueRequestsFromMapFunc(r.findBackendTLSPoliciesForConfigMap), builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Complete(r)
}

func (r *BackendTLSPolicyReconciler) findBackendTLSPoliciesForService(ctx context.Context, obj client.Object) []reconcile.Request {
	var policies gateway.BackendTLSPolicyList
	if err := r.List(ctx, &policies, client.InNamespace(obj.GetNamespace())); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for _, policy := range policies.Items {
		if backendTLSPolicyTargetsService(policy, obj.GetName()) {
			reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: policy.Namespace, Name: policy.Name}})
		}
	}
	return reqs
}

func (r *BackendTLSPolicyReconciler) findBackendTLSPoliciesForConfigMap(ctx context.Context, obj client.Object) []reconcile.Request {
	var policies gateway.BackendTLSPolicyList
	if err := r.List(ctx, &policies, client.InNamespace(obj.GetNamespace())); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for _, policy := range policies.Items {
		for _, ref := range policy.Spec.Validation.CACertificateRefs {
			if string(ref.Group) == "" && string(ref.Kind) == "ConfigMap" && string(ref.Name) == obj.GetName() {
				reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: policy.Namespace, Name: policy.Name}})
				break
			}
		}
	}
	return reqs
}
