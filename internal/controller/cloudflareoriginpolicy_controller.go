package controller

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gateway "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/controller/status"
)

// CloudflareOriginPolicyReconciler reconciles CloudflareOriginPolicy status.
type CloudflareOriginPolicyReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=cfgate.io,resources=cloudflareoriginpolicies,verbs=get;list;watch
// +kubebuilder:rbac:groups=cfgate.io,resources=cloudflareoriginpolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes;referencegrants,verbs=get;list;watch

func (r *CloudflareOriginPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx).WithName("controller").WithName("originpolicy")
	var policy cfgatev1alpha1.CloudflareOriginPolicy
	if err := r.Get(ctx, req.NamespacedName, &policy); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get CloudflareOriginPolicy: %w", err)
	}

	ancestors, attached, targetsOK, grantsOK, err := r.evaluateOriginPolicy(ctx, &policy)
	if err != nil {
		log.Error(err, "failed to evaluate CloudflareOriginPolicy")
		return ctrl.Result{RequeueAfter: requeueAfterError}, nil
	}
	conditions := []metav1.Condition{
		status.NewCondition(status.ConditionTypeTargetsResolved, conditionStatus(targetsOK), targetReason(targetsOK), targetMessage(targetsOK), policy.Generation),
		status.NewCondition(status.ConditionTypeReferenceGrantValid, conditionStatus(grantsOK), referenceGrantReason(grantsOK), referenceGrantMessage(grantsOK), policy.Generation),
	}
	ready := targetsOK && grantsOK && attached > 0
	conditions = append(conditions, status.NewCondition(status.ConditionTypeReady, conditionStatus(ready), readyReason(ready), readyMessage(ready), policy.Generation))

	policy.Status.AttachedTargets = int32(attached)
	policy.Status.ObservedGeneration = policy.Generation
	policy.Status.Ancestors = ancestors
	policy.Status.Conditions = status.MergeConditions(policy.Status.Conditions, conditions...)
	if err := r.Status().Update(ctx, &policy); err != nil {
		log.Error(err, "failed to update CloudflareOriginPolicy status")
		return ctrl.Result{RequeueAfter: requeueAfterError}, nil
	}
	return ctrl.Result{}, nil
}

func (r *CloudflareOriginPolicyReconciler) evaluateOriginPolicy(ctx context.Context, policy *cfgatev1alpha1.CloudflareOriginPolicy) ([]cfgatev1alpha1.PolicyAncestorStatus, int, bool, bool, error) {
	var all cfgatev1alpha1.CloudflareOriginPolicyList
	if err := r.List(ctx, &all); err != nil {
		return nil, 0, false, false, fmt.Errorf("list CloudflareOriginPolicies: %w", err)
	}
	attached := 0
	targetsOK := true
	grantsOK := true
	ancestors := make([]cfgatev1alpha1.PolicyAncestorStatus, 0, len(policy.Spec.TargetRefs))
	for _, ref := range policy.Spec.TargetRefs {
		ancestorRef := originTargetToPolicyTarget(policy.Namespace, ref)
		accepted := true
		reason := status.PolicyReasonAccepted
		message := "Origin policy target accepted."

		targetNS := ref.Namespace
		if targetNS == "" {
			targetNS = policy.Namespace
		}
		var route gateway.HTTPRoute
		routeKey := types.NamespacedName{Namespace: targetNS, Name: ref.Name}
		if err := r.Get(ctx, routeKey, &route); err != nil {
			if apierrors.IsNotFound(err) {
				accepted = false
				targetsOK = false
				reason = status.PolicyReasonTargetNotFound
				message = fmt.Sprintf("HTTPRoute %s/%s was not found.", targetNS, ref.Name)
			} else {
				return nil, 0, false, false, fmt.Errorf("get CloudflareOriginPolicy target HTTPRoute %s: %w", routeKey.String(), err)
			}
		} else if targetNS != policy.Namespace {
			ok, err := (&CloudflareTunnelReconciler{Client: r.Client}).referenceGrantPermits(ctx, policy.Namespace, targetNS, cfgatev1alpha1.GroupVersion.Group, "CloudflareOriginPolicy", gateway.GroupName, "HTTPRoute", ref.Name)
			if err != nil {
				return nil, 0, false, false, fmt.Errorf("checking CloudflareOriginPolicy ReferenceGrant for HTTPRoute %s/%s: %w", targetNS, ref.Name, err)
			}
			if !ok {
				accepted = false
				grantsOK = false
				reason = status.ReasonReferenceGrantRequired
				message = "ReferenceGrant is required for this cross-namespace HTTPRoute target."
			}
		}
		if accepted && ref.SectionName != "" && !httpRouteHasRule(&route, ref.SectionName) {
			accepted = false
			targetsOK = false
			reason = status.PolicyReasonTargetNotFound
			message = fmt.Sprintf("HTTPRoute %s/%s has no rule section %q.", targetNS, ref.Name, ref.SectionName)
		}
		if accepted && originPolicyRefConflicted(policy, ref, all.Items) {
			accepted = false
			reason = status.PolicyReasonConflicted
			message = "An older CloudflareOriginPolicy targets the same HTTPRoute section."
		}
		if accepted {
			attached++
		}
		ancestors = append(ancestors, cfgatev1alpha1.PolicyAncestorStatus{
			AncestorRef:    ancestorRef,
			ControllerName: GatewayControllerName,
			Conditions: []metav1.Condition{
				status.NewPolicyAcceptedCondition(accepted, reason, message, policy.Generation),
			},
		})
	}
	return ancestors, attached, targetsOK, grantsOK, nil
}

func originTargetToPolicyTarget(policyNS string, ref cfgatev1alpha1.OriginPolicyTargetReference) cfgatev1alpha1.PolicyTargetReference {
	ns := ref.Namespace
	if ns == "" {
		ns = policyNS
	}
	group := ref.Group
	if group == "" {
		group = gateway.GroupName
	}
	kind := ref.Kind
	if kind == "" {
		kind = "HTTPRoute"
	}
	section := ref.SectionName
	return cfgatev1alpha1.PolicyTargetReference{
		Group:       group,
		Kind:        kind,
		Name:        ref.Name,
		Namespace:   &ns,
		SectionName: &section,
	}
}

func httpRouteHasRule(route *gateway.HTTPRoute, section string) bool {
	for _, rule := range route.Spec.Rules {
		if rule.Name != nil && string(*rule.Name) == section {
			return true
		}
	}
	return false
}

func originPolicyRefConflicted(policy *cfgatev1alpha1.CloudflareOriginPolicy, ref cfgatev1alpha1.OriginPolicyTargetReference, all []cfgatev1alpha1.CloudflareOriginPolicy) bool {
	current := []cfgatev1alpha1.CloudflareOriginPolicy{*policy}
	for _, candidate := range all {
		if candidate.Namespace == policy.Namespace && candidate.Name == policy.Name {
			continue
		}
		for _, candidateRef := range candidate.Spec.TargetRefs {
			if sameOriginPolicyTarget(policy.Namespace, ref, candidate.Namespace, candidateRef) {
				current = append(current, candidate)
				break
			}
		}
	}
	sortOriginPolicies(current)
	return current[0].Namespace != policy.Namespace || current[0].Name != policy.Name
}

func sameOriginPolicyTarget(leftNS string, left cfgatev1alpha1.OriginPolicyTargetReference, rightNS string, right cfgatev1alpha1.OriginPolicyTargetReference) bool {
	leftTargetNS := left.Namespace
	if leftTargetNS == "" {
		leftTargetNS = leftNS
	}
	rightTargetNS := right.Namespace
	if rightTargetNS == "" {
		rightTargetNS = rightNS
	}
	leftGroup := left.Group
	if leftGroup == "" {
		leftGroup = gateway.GroupName
	}
	rightGroup := right.Group
	if rightGroup == "" {
		rightGroup = gateway.GroupName
	}
	leftKind := left.Kind
	if leftKind == "" {
		leftKind = "HTTPRoute"
	}
	rightKind := right.Kind
	if rightKind == "" {
		rightKind = "HTTPRoute"
	}
	return leftTargetNS == rightTargetNS &&
		leftGroup == rightGroup &&
		leftKind == rightKind &&
		left.Name == right.Name &&
		left.SectionName == right.SectionName
}

func targetReason(ok bool) string {
	if ok {
		return status.ReasonTargetsResolved
	}
	return status.ReasonTargetNotFound
}

func targetMessage(ok bool) string {
	if ok {
		return "All target references resolved."
	}
	return "One or more target references could not be resolved."
}

func readyReason(ok bool) string {
	if ok {
		return status.ReasonReady
	}
	return status.ReasonTargetResolutionFailed
}

func readyMessage(ok bool) string {
	if ok {
		return "Origin policy is ready."
	}
	return "Origin policy is not ready."
}

func (r *CloudflareOriginPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&cfgatev1alpha1.CloudflareOriginPolicy{}, builder.WithPredicates(GenerationOrDeletionPredicate)).
		Watches(&gateway.HTTPRoute{}, handler.EnqueueRequestsFromMapFunc(r.findOriginPoliciesForHTTPRoute), builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&gatewayv1beta1.ReferenceGrant{}, handler.EnqueueRequestsFromMapFunc(r.findAllOriginPolicies)).
		Complete(r)
}

func (r *CloudflareOriginPolicyReconciler) findOriginPoliciesForHTTPRoute(ctx context.Context, obj client.Object) []reconcile.Request {
	route, ok := obj.(*gateway.HTTPRoute)
	if !ok {
		return nil
	}
	var policies cfgatev1alpha1.CloudflareOriginPolicyList
	if err := r.List(ctx, &policies); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for _, policy := range policies.Items {
		for _, ref := range policy.Spec.TargetRefs {
			ns := ref.Namespace
			if ns == "" {
				ns = policy.Namespace
			}
			if ns == route.Namespace && ref.Name == route.Name {
				reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: policy.Namespace, Name: policy.Name}})
				break
			}
		}
	}
	return reqs
}

func (r *CloudflareOriginPolicyReconciler) findAllOriginPolicies(ctx context.Context, obj client.Object) []reconcile.Request {
	var policies cfgatev1alpha1.CloudflareOriginPolicyList
	if err := r.List(ctx, &policies); err != nil {
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(policies.Items))
	for _, policy := range policies.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: policy.Namespace, Name: policy.Name}})
	}
	return reqs
}
