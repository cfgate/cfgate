package controller

import (
	"context"
	"fmt"
	"reflect"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gateway "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1b1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/cloudflare"
	"cfgate.io/cfgate/internal/controller/annotations"
	ctxwrappers "cfgate.io/cfgate/internal/controller/context"
	"cfgate.io/cfgate/internal/controller/features"
	"cfgate.io/cfgate/internal/controller/status"
)

const (
	// accessPolicyFinalizer is the finalizer for CloudflareAccessPolicy resources.
	accessPolicyFinalizer = "cfgate.io/access-policy-cleanup"

	// accessPolicyRequeueAfterError is the requeue delay after an error.
	accessPolicyRequeueAfterError = 30 * time.Second

	// accessPolicyRequeueAfterSuccess is the requeue delay for periodic sync.
	accessPolicyRequeueAfterSuccess = 5 * time.Minute

	// AccessPolicyControllerName is the controller name for policy status.
	AccessPolicyControllerName = "cfgate.io/cloudflare-tunnel-controller"

	// accessPolicyTargetIndex is the field index key for target lookups.
	accessPolicyTargetIndex = ".spec.targetRefs"

	// accessDeletionRetryBudget is the maximum time to retry Access resource
	// cleanup before emitting an escalated warning. After this budget, the
	// controller keeps blocking (does not remove the finalizer). The only
	// escape is the cfgate.io/deletion-policy=orphan annotation.
	accessDeletionRetryBudget = 1 * time.Minute

	// accessDeletionRequeueInterval is the requeue delay between deletion retries.
	accessDeletionRequeueInterval = 15 * time.Second
)

// CloudflareAccessPolicyReconciler reconciles CloudflareAccessPolicy resources.
//
// It manages the complete Access policy lifecycle including:
//   - Target resolution (Gateway, HTTPRoute)
//   - Cross-namespace reference validation via ReferenceGrant
//   - Cloudflare Access Application creation and updates
//   - Access Policy synchronization
//   - Service token provisioning (optional)
//
// Credentials can be specified explicitly via cloudflareRef or inherited from
// a CloudflareTunnel referenced by target Gateways.
type CloudflareAccessPolicyReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder events.EventRecorder

	// CFClient is the Cloudflare API client. Injected for testing.
	CFClient cloudflare.Client

	// CredentialCache caches validated Cloudflare clients to avoid repeated validations.
	CredentialCache *cloudflare.CredentialCache

	// FeatureGates tracks which optional Gateway API CRDs are available.
	FeatureGates *features.FeatureGates
}

// +kubebuilder:rbac:groups=cfgate.io,resources=cloudflareaccesspolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cfgate.io,resources=cloudflareaccesspolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cfgate.io,resources=cloudflareaccesspolicies/finalizers,verbs=update
// +kubebuilder:rbac:groups=cfgate.io,resources=cloudflaretunnels,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways;httproutes,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=referencegrants,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconcile handles the reconciliation loop for CloudflareAccessPolicy resources.
// It ensures Access Applications and Policies exist in Cloudflare and are synced.
//
// The reconciliation proceeds through these phases:
//  1. Fetch the CloudflareAccessPolicy resource
//  2. Handle deletion via finalizers (cleanup Access Application)
//  3. Resolve Cloudflare credentials (explicit or inherited from tunnel)
//  4. Resolve and validate target references
//  5. Check ReferenceGrants for cross-namespace targets
//  6. Ensure Access Application exists in Cloudflare
//  7. Sync Access Policies to the application
//  8. Ensure service tokens (if configured)
//  9. Update status conditions and ancestor statuses
//
// On error, the controller requeues after 30 seconds. On success, it requeues
// after 5 minutes for periodic policy sync.
func (r *CloudflareAccessPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx).WithName("controller").WithName("accesspolicy").
		WithValues("namespace", req.Namespace, "name", req.Name)
	log.Info("starting reconciliation")

	// Phase 1: Fetch CloudflareAccessPolicy resource
	var policy cfgatev1alpha1.CloudflareAccessPolicy
	if err := r.Get(ctx, req.NamespacedName, &policy); err != nil {
		if apierrors.IsNotFound(err) {
			log.V(1).Info("policy not found, likely deleted")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to get CloudflareAccessPolicy: %w", err)
	}

	// Handle deletion (finalizers)
	if !policy.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &policy)
	}

	// Add finalizer if not present (using patch to reduce lock contention)
	if !controllerutil.ContainsFinalizer(&policy, accessPolicyFinalizer) {
		patch := client.MergeFrom(policy.DeepCopy())
		controllerutil.AddFinalizer(&policy, accessPolicyFinalizer)
		if err := r.Patch(ctx, &policy, patch); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to add finalizer: %w", err)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Execute reconciliation phases
	return r.reconcilePhases(ctx, &policy)
}

// reconcilePhases executes the main reconciliation phases for CloudflareAccessPolicy.
// It proceeds through credential resolution, target resolution, ReferenceGrant checks,
// Access Application management, policy sync, and service tokens.
func (r *CloudflareAccessPolicyReconciler) reconcilePhases(ctx context.Context, policy *cfgatev1alpha1.CloudflareAccessPolicy) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	generation := policy.Generation

	// Phase 2: Resolve credentials
	accessService, accountID, err := r.resolveCredentials(ctx, policy)
	if err != nil {
		log.Error(err, "failed to resolve credentials")
		policy.Status.Conditions = status.MergeConditions(policy.Status.Conditions,
			status.NewCondition(status.ConditionTypeCredentialsValid, metav1.ConditionFalse,
				status.ReasonCredentialsInvalid, status.Error2ConditionMsg(err), generation),
		)
		if statusErr := r.updateStatus(ctx, policy); statusErr != nil {
			log.Error(statusErr, "failed to update status")
		}
		r.Recorder.Eventf(policy, nil, corev1.EventTypeWarning, "CredentialsInvalid", "Validate", "%s", err.Error())
		return ctrl.Result{RequeueAfter: accessPolicyRequeueAfterError}, nil
	}
	policy.Status.Conditions = status.MergeConditions(policy.Status.Conditions,
		status.NewCondition(status.ConditionTypeCredentialsValid, metav1.ConditionTrue,
			status.ReasonCredentialsValid, "Credentials validated successfully.", generation),
	)

	// Phase 3: Resolve targets
	policyCtx := ctxwrappers.NewAccessPolicyContext(ctx, policy, r.Client)

	if err := r.validateTargetKinds(ctx, policy); err != nil {
		log.Info("unsupported target kind", "error", err.Error())
		policy.Status.Conditions = status.MergeConditions(policy.Status.Conditions,
			status.NewTargetsResolvedCondition(false, status.ReasonTargetNotFound,
				status.Error2ConditionMsg(err), generation),
		)
		if statusErr := r.updateStatus(ctx, policy); statusErr != nil {
			log.Error(statusErr, "failed to update status")
		}
		return ctrl.Result{RequeueAfter: accessPolicyRequeueAfterError}, nil
	}

	if policyCtx.HasFailedTargets() {
		failedTargets := policyCtx.FailedTargets()
		msg := fmt.Sprintf("Failed to resolve %d target(s): %s", len(failedTargets), failedTargets[0].Error.Error())
		policy.Status.Conditions = status.MergeConditions(policy.Status.Conditions,
			status.NewTargetsResolvedCondition(false, status.ReasonTargetNotFound, msg, generation),
		)
		// Continue with partial resolution if some targets succeeded
		if len(policyCtx.SuccessfullyResolvedTargets()) == 0 {
			if statusErr := r.updateStatus(ctx, policy); statusErr != nil {
				log.Error(statusErr, "failed to update status")
			}
			r.Recorder.Eventf(policy, nil, corev1.EventTypeWarning, "TargetNotFound", "Resolve", "%s", msg)
			return ctrl.Result{RequeueAfter: accessPolicyRequeueAfterError}, nil
		}
	} else {
		policy.Status.Conditions = status.MergeConditions(policy.Status.Conditions,
			status.NewTargetsResolvedCondition(true, status.ReasonTargetsResolved,
				fmt.Sprintf("Resolved %d target(s).", len(policyCtx.SuccessfullyResolvedTargets())), generation),
		)
	}

	// Phase 4: Check ReferenceGrants (cross-namespace)
	if policyCtx.HasCrossNamespaceTargets() {
		if err := r.checkReferenceGrants(ctx, policy, policyCtx); err != nil {
			log.Info("ReferenceGrant check failed", "error", err.Error())
			policy.Status.Conditions = status.MergeConditions(policy.Status.Conditions,
				status.NewCondition(status.ConditionTypeReferenceGrantValid, metav1.ConditionFalse,
					status.ReasonReferenceGrantRequired, status.Error2ConditionMsg(err), generation),
			)
			if statusErr := r.updateStatus(ctx, policy); statusErr != nil {
				log.Error(statusErr, "failed to update status")
			}
			return ctrl.Result{RequeueAfter: accessPolicyRequeueAfterError}, nil
		}
	}

	// Phase 5: Ensure Access Application
	hostnames, err := policyCtx.ExtractHostnames(ctx, r.Client)
	if err != nil {
		log.Error(err, "failed to extract hostnames")
		if len(hostnames) == 0 && policy.Spec.Application.Domain == "" {
			policy.Status.Conditions = status.MergeConditions(policy.Status.Conditions,
				status.NewApplicationCreatedCondition(false, status.ReasonApplicationError,
					status.Error2ConditionMsg(fmt.Errorf("failed to extract hostnames from targets: %w", err)), generation),
			)
			if statusErr := r.updateStatus(ctx, policy); statusErr != nil {
				log.Error(statusErr, "failed to update status")
			}
			return ctrl.Result{RequeueAfter: accessPolicyRequeueAfterError}, nil
		}
	}
	app, err := r.ensureApplication(ctx, accessService, accountID, policy, hostnames)
	if err != nil {
		log.Error(err, "failed to ensure Access Application")
		policy.Status.Conditions = status.MergeConditions(policy.Status.Conditions,
			status.NewApplicationCreatedCondition(false, status.ReasonApplicationError,
				status.Error2ConditionMsg(err), generation),
		)
		if statusErr := r.updateStatus(ctx, policy); statusErr != nil {
			log.Error(statusErr, "failed to update status")
		}
		r.Recorder.Eventf(policy, nil, corev1.EventTypeWarning, "ApplicationError", "Create", "%s", err.Error())
		return ctrl.Result{RequeueAfter: accessPolicyRequeueAfterError}, nil
	}
	policy.Status.ApplicationID = app.ID
	policy.Status.ApplicationAUD = app.AUD
	policy.Status.Conditions = status.MergeConditions(policy.Status.Conditions,
		status.NewApplicationCreatedCondition(true, status.ReasonApplicationCreated,
			fmt.Sprintf("Access Application %s created.", app.ID), generation),
	)

	// Phase 6: Sync Access Policies
	policyIDs, err := r.syncPolicies(ctx, accessService, accountID, app.ID, policy)
	if err != nil {
		log.Error(err, "failed to sync Access Policies")
		policy.Status.Conditions = status.MergeConditions(policy.Status.Conditions,
			status.NewPoliciesAttachedCondition(false, status.ReasonPolicyError,
				status.Error2ConditionMsg(err), generation),
		)
		if statusErr := r.updateStatus(ctx, policy); statusErr != nil {
			log.Error(statusErr, "failed to update status")
		}
		r.Recorder.Eventf(policy, nil, corev1.EventTypeWarning, "PolicySyncError", "Sync", "%s", err.Error())
		return ctrl.Result{RequeueAfter: accessPolicyRequeueAfterError}, nil
	}
	log.V(1).Info("synced access policies", "policyCount", len(policyIDs))
	policy.Status.Conditions = status.MergeConditions(policy.Status.Conditions,
		status.NewPoliciesAttachedCondition(true, status.ReasonPoliciesAttached,
			fmt.Sprintf("Synced %d Access Policy(ies).", len(policyIDs)), generation),
	)

	// Phase 7: Service Tokens (non-fatal)
	if policyCtx.RequiresServiceTokens() {
		if err := r.ensureServiceTokens(ctx, accessService, accountID, policy); err != nil {
			log.Error(err, "failed to ensure service tokens (continuing)")
			policy.Status.Conditions = status.MergeConditions(policy.Status.Conditions,
				status.NewCondition(status.ConditionTypeServiceTokensReady, metav1.ConditionFalse,
					status.ReasonServiceTokenError, status.Error2ConditionMsg(err), generation),
			)
		} else {
			policy.Status.Conditions = status.MergeConditions(policy.Status.Conditions,
				status.NewCondition(status.ConditionTypeServiceTokensReady, metav1.ConditionTrue,
					status.ReasonServiceTokensReady, "Service tokens ready.", generation),
			)
		}
	}

	// Phase 8: Update status
	policy.Status.AttachedTargets = int32(len(policyCtx.SuccessfullyResolvedTargets()))
	policy.Status.ObservedGeneration = generation
	r.updateAncestorStatuses(policy, policyCtx)

	hasServiceTokens := len(policy.Spec.ServiceTokens) > 0
	readyCondition := status.NewAccessPolicyReadyCondition(policy.Status.Conditions, hasServiceTokens, generation)
	policy.Status.Conditions = status.MergeConditions(policy.Status.Conditions, readyCondition)

	if err := r.updateStatus(ctx, policy); err != nil {
		return ctrl.Result{}, err
	}

	r.Recorder.Eventf(policy, nil, corev1.EventTypeNormal, "Reconciled", "Reconcile",
		"Access policy reconciled successfully")
	return ctrl.Result{RequeueAfter: accessPolicyRequeueAfterSuccess}, nil
}

// resolveCredentials resolves Cloudflare credentials for the policy.
// It first checks for explicit cloudflareRef in the spec, then falls back to
// inheriting credentials from a CloudflareTunnel referenced by target Gateways.
// Returns an AccessService client, the account ID, or an error if no credentials found.
func (r *CloudflareAccessPolicyReconciler) resolveCredentials(
	ctx context.Context,
	policy *cfgatev1alpha1.CloudflareAccessPolicy,
) (*cloudflare.AccessService, string, error) {
	log := log.FromContext(ctx)

	var secretRef *cfgatev1alpha1.CloudflareSecretRef
	var accountID string

	// Option 1: Explicit cloudflareRef
	if policy.Spec.CloudflareRef != nil {
		secretRef = policy.Spec.CloudflareRef
		accountID = policy.Spec.CloudflareRef.AccountID
		log.V(1).Info("using explicit cloudflareRef",
			"secretName", secretRef.Name,
			"accountId", accountID,
		)
	} else {
		// Option 2: Inherit from tunnelRef (look for cfgate.io/tunnel-ref annotation on targets)
		tunnelCreds, tunnelAccountID, err := r.inheritCredentialsFromTunnel(ctx, policy)
		if err != nil {
			return nil, "", fmt.Errorf("failed to inherit credentials: %w", err)
		}
		if tunnelCreds == nil {
			return nil, "", fmt.Errorf("no credentials configured: set cloudflareRef or ensure targets reference a tunnel")
		}
		secretRef = tunnelCreds
		accountID = tunnelAccountID
		log.V(1).Info("inherited credentials from tunnel",
			"secretName", secretRef.Name,
			"accountId", accountID,
		)
	}

	if accountID == "" {
		return nil, "", fmt.Errorf("account ID not specified and could not be resolved")
	}

	// Create Cloudflare client
	cfClient, err := r.getCloudflareClient(ctx, policy.Namespace, secretRef)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create Cloudflare client: %w", err)
	}

	accessService := cloudflare.NewAccessService(cfClient, log)
	return accessService, accountID, nil
}

// inheritCredentialsFromTunnel finds credentials from Gateway tunnel references.
// It iterates through Gateway targetRefs, looks for cfgate.io/tunnel-ref annotations,
// and returns the first tunnel's credentials if found.
func (r *CloudflareAccessPolicyReconciler) inheritCredentialsFromTunnel(
	ctx context.Context,
	policy *cfgatev1alpha1.CloudflareAccessPolicy,
) (*cfgatev1alpha1.CloudflareSecretRef, string, error) {
	log := log.FromContext(ctx)

	// Gather all target refs
	refs := policy.Spec.TargetRefs
	if policy.Spec.TargetRef != nil {
		refs = append([]cfgatev1alpha1.PolicyTargetReference{*policy.Spec.TargetRef}, refs...)
	}

	// Find tunnels referenced by target Gateways or HTTPRoutes
	for _, ref := range refs {
		if ref.Kind == "Gateway" {
			namespace := policy.Namespace
			if ref.Namespace != nil {
				namespace = *ref.Namespace
			}

			var gw gateway.Gateway
			if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: ref.Name}, &gw); err != nil {
				continue
			}

			tunnelRefValue, ok := gw.Annotations[annotations.AnnotationTunnelRef]
			if !ok {
				continue
			}

			tunnelNS, tunnelName, err := annotations.ParseNamespacedName(tunnelRefValue, gw.Namespace)
			if err != nil {
				log.V(1).Info("invalid tunnel-ref annotation on Gateway",
					"gateway", namespace+"/"+ref.Name,
					"tunnelRef", tunnelRefValue,
					"error", err.Error(),
				)
				continue
			}

			var tunnel cfgatev1alpha1.CloudflareTunnel
			if err := r.Get(ctx, types.NamespacedName{Namespace: tunnelNS, Name: tunnelName}, &tunnel); err != nil {
				log.V(1).Info("failed to get tunnel for credential inheritance",
					"tunnel", tunnelNS+"/"+tunnelName,
					"error", err.Error(),
				)
				continue
			}

			// Found tunnel - inherit credentials
			return &cfgatev1alpha1.CloudflareSecretRef{
				Name:      tunnel.Spec.Cloudflare.SecretRef.Name,
				Namespace: ptr.To(tunnel.Namespace),
				AccountID: tunnel.Spec.Cloudflare.AccountID,
			}, tunnel.Spec.Cloudflare.AccountID, nil

		} else if ref.Kind == "HTTPRoute" {
			// Walk HTTPRoute → parentRef → Gateway → tunnel
			namespace := policy.Namespace
			if ref.Namespace != nil {
				namespace = *ref.Namespace
			}

			var route gateway.HTTPRoute
			if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: ref.Name}, &route); err != nil {
				continue
			}

			// Walk parent refs to find a cfgate Gateway
			for _, parentRef := range route.Spec.ParentRefs {
				// Default kind is Gateway per Gateway API spec
				if parentRef.Kind != nil && *parentRef.Kind != "Gateway" {
					continue
				}

				gwNamespace := route.Namespace
				if parentRef.Namespace != nil {
					gwNamespace = string(*parentRef.Namespace)
				}

				var gw gateway.Gateway
				if err := r.Get(ctx, types.NamespacedName{Namespace: gwNamespace, Name: string(parentRef.Name)}, &gw); err != nil {
					continue
				}

				// Check if this is a cfgate-managed Gateway
				var gc gateway.GatewayClass
				if err := r.Get(ctx, types.NamespacedName{Name: string(gw.Spec.GatewayClassName)}, &gc); err != nil {
					continue
				}
				if string(gc.Spec.ControllerName) != GatewayControllerName {
					continue
				}

				// Found cfgate Gateway — resolve tunnel credentials
				tunnelRefValue, ok := gw.Annotations[annotations.AnnotationTunnelRef]
				if !ok {
					continue
				}

				tunnelNS, tunnelName, err := annotations.ParseNamespacedName(tunnelRefValue, gw.Namespace)
				if err != nil {
					log.V(1).Info("invalid tunnel-ref annotation on Gateway (via HTTPRoute)",
						"httproute", namespace+"/"+ref.Name,
						"gateway", gwNamespace+"/"+string(parentRef.Name),
						"tunnelRef", tunnelRefValue,
						"error", err.Error(),
					)
					continue
				}

				var tunnel cfgatev1alpha1.CloudflareTunnel
				if err := r.Get(ctx, types.NamespacedName{Namespace: tunnelNS, Name: tunnelName}, &tunnel); err != nil {
					log.V(1).Info("failed to get tunnel for credential inheritance via HTTPRoute",
						"httproute", namespace+"/"+ref.Name,
						"gateway", gwNamespace+"/"+string(parentRef.Name),
						"tunnel", tunnelNS+"/"+tunnelName,
						"error", err.Error(),
					)
					continue
				}

				return &cfgatev1alpha1.CloudflareSecretRef{
					Name:      tunnel.Spec.Cloudflare.SecretRef.Name,
					Namespace: ptr.To(tunnel.Namespace),
					AccountID: tunnel.Spec.Cloudflare.AccountID,
				}, tunnel.Spec.Cloudflare.AccountID, nil
			}
		}
	}

	return nil, "", nil
}

// validateTargetKinds validates that all target kinds are supported.
func (r *CloudflareAccessPolicyReconciler) validateTargetKinds(
	ctx context.Context,
	policy *cfgatev1alpha1.CloudflareAccessPolicy,
) error {
	refs := policy.Spec.TargetRefs
	if policy.Spec.TargetRef != nil {
		refs = append([]cfgatev1alpha1.PolicyTargetReference{*policy.Spec.TargetRef}, refs...)
	}

	for _, ref := range refs {
		switch ref.Kind {
		case "Gateway", "HTTPRoute":
			continue
		default:
			return fmt.Errorf("unsupported target kind: %s", ref.Kind)
		}
	}
	return nil
}

// checkReferenceGrants verifies cross-namespace references are permitted.
// For each target in a different namespace, it checks for a ReferenceGrant that
// allows CloudflareAccessPolicy from the policy's namespace to reference the target.
func (r *CloudflareAccessPolicyReconciler) checkReferenceGrants(
	ctx context.Context,
	policy *cfgatev1alpha1.CloudflareAccessPolicy,
	policyCtx *ctxwrappers.AccessPolicyContext,
) error {
	log := log.FromContext(ctx)

	if r.FeatureGates != nil && !r.FeatureGates.HasReferenceGrantSupport() {
		log.Info("ReferenceGrant CRD not available, cross-namespace references may fail")
		return nil
	}

	for _, target := range policyCtx.ResolvedTargets() {
		if target.Namespace == policy.Namespace {
			continue // Same namespace, no grant needed
		}

		log.V(1).Info("checking ReferenceGrant",
			"fromNamespace", policy.Namespace,
			"toNamespace", target.Namespace,
			"targetKind", target.Kind,
		)

		// List ReferenceGrants in target namespace
		var grants gatewayv1b1.ReferenceGrantList
		if err := r.List(ctx, &grants, client.InNamespace(target.Namespace)); err != nil {
			return fmt.Errorf("listing ReferenceGrants: %w", err)
		}

		permitted := false
		for _, grant := range grants.Items {
			if r.grantPermitsAccess(grant, policy.Namespace, target.Kind) {
				permitted = true
				break
			}
		}

		if !permitted {
			return fmt.Errorf("cross-namespace reference to %s/%s not permitted by ReferenceGrant",
				target.Namespace, target.Name)
		}
	}

	return nil
}

// grantPermitsAccess checks if a ReferenceGrant permits access from the policy namespace.
// Returns true if the grant allows CloudflareAccessPolicy from fromNamespace to
// reference resources of targetKind in the grant's namespace.
func (r *CloudflareAccessPolicyReconciler) grantPermitsAccess(
	grant gatewayv1b1.ReferenceGrant,
	fromNamespace, targetKind string,
) bool {
	for _, from := range grant.Spec.From {
		if string(from.Group) != "cfgate.io" {
			continue
		}
		if string(from.Kind) != "CloudflareAccessPolicy" {
			continue
		}
		if string(from.Namespace) != fromNamespace {
			continue
		}

		for _, to := range grant.Spec.To {
			if string(to.Group) == "gateway.networking.k8s.io" &&
				string(to.Kind) == targetKind {
				return true
			}
		}
	}
	return false
}

// ensureApplication ensures the Access Application exists in Cloudflare.
// It determines the domain from spec or extracted hostnames, builds the application
// parameters, and calls EnsureApplication to create or update the application.
func (r *CloudflareAccessPolicyReconciler) ensureApplication(
	ctx context.Context,
	accessService *cloudflare.AccessService,
	accountID string,
	policy *cfgatev1alpha1.CloudflareAccessPolicy,
	hostnames []string,
) (*cloudflare.AccessApplication, error) {
	log := log.FromContext(ctx)

	// Determine domain for application
	domain := policy.Spec.Application.Domain
	if domain == "" && len(hostnames) > 0 {
		domain = hostnames[0] // Use first hostname from targets
		log.V(1).Info("using hostname from target as application domain",
			"domain", domain,
		)
	}
	if domain == "" {
		return nil, fmt.Errorf("no domain configured: set application.domain or target an HTTPRoute with hostnames")
	}

	// Build application params
	params := cloudflare.ApplicationParams{
		Name:                    policy.Spec.Application.Name,
		Domain:                  domain,
		Type:                    policy.Spec.Application.Type,
		SessionDuration:         policy.Spec.Application.SessionDuration,
		SkipInterstitial:        policy.Spec.Application.SkipInterstitial,
		EnableBindingCookie:     policy.Spec.Application.EnableBindingCookie,
		SameSiteCookieAttribute: policy.Spec.Application.SameSiteCookieAttribute,
		LogoURL:                 policy.Spec.Application.LogoURL,
		CustomDenyMessage:       policy.Spec.Application.CustomDenyMessage,
		CustomDenyURL:           policy.Spec.Application.CustomDenyURL,
	}

	// Set HttpOnlyCookieAttribute (defaults to true in CRD, need to pass pointer)
	httpOnly := policy.Spec.Application.HttpOnlyCookieAttribute
	params.HttpOnlyCookieAttribute = &httpOnly

	// P0 fields (already in internal SDK, CRD-only wiring)
	params.AllowedIdps = policy.Spec.Application.AllowedIdps
	params.AutoRedirectToIdentity = policy.Spec.Application.AutoRedirectToIdentity
	if policy.Spec.Application.AppLauncherVisible != nil {
		params.AppLauncherVisible = *policy.Spec.Application.AppLauncherVisible
	}

	// P1 fields
	params.OptionsPreflightBypass = policy.Spec.Application.OptionsPreflightBypass
	params.PathCookieAttribute = policy.Spec.Application.PathCookieAttribute
	params.ServiceAuth401Redirect = policy.Spec.Application.ServiceAuth401Redirect
	params.CustomNonIdentityDenyURL = policy.Spec.Application.CustomNonIdentityDenyURL
	params.ReadServiceTokensFromHeader = policy.Spec.Application.ReadServiceTokensFromHeader

	// CORSHeaders (nested, conditional)
	if policy.Spec.Application.CORSHeaders != nil {
		cors := policy.Spec.Application.CORSHeaders
		params.CORSHeaders = &cloudflare.CORSHeadersParam{
			AllowAllHeaders:  cors.AllowAllHeaders,
			AllowAllMethods:  cors.AllowAllMethods,
			AllowAllOrigins:  cors.AllowAllOrigins,
			AllowCredentials: cors.AllowCredentials,
		}
		params.CORSHeaders.AllowedHeaders = append(params.CORSHeaders.AllowedHeaders, cors.AllowedHeaders...)
		for _, m := range cors.AllowedMethods {
			params.CORSHeaders.AllowedMethods = append(params.CORSHeaders.AllowedMethods, string(m))
		}
		params.CORSHeaders.AllowedOrigins = append(params.CORSHeaders.AllowedOrigins, cors.AllowedOrigins...)
		if cors.MaxAge != nil {
			params.CORSHeaders.MaxAge = *cors.MaxAge
		}
	}

	if params.Name == "" {
		params.Name = policy.Name
	}

	log.Info("ensuring Access Application exists",
		"accountId", accountID,
		"domain", domain,
		"applicationName", params.Name,
	)

	app, _, err := accessService.EnsureApplication(ctx, accountID, params)
	return app, err
}

// syncPolicies synchronizes Access Policies for the application.
// It converts the CRD policy rules to API parameters and calls SyncPolicies
// to ensure the Cloudflare application has the correct policies attached.
func (r *CloudflareAccessPolicyReconciler) syncPolicies(
	ctx context.Context,
	accessService *cloudflare.AccessService,
	accountID, appID string,
	policy *cfgatev1alpha1.CloudflareAccessPolicy,
) ([]string, error) {
	log := log.FromContext(ctx)

	// Convert CRD policies to API params
	var params []cloudflare.PolicyParams
	for i, rule := range policy.Spec.Policies {
		precedence := i + 1
		if rule.Precedence != nil {
			precedence = *rule.Precedence
		}

		// Warn about unsupported ApprovalGroup fields
		for _, ag := range rule.ApprovalGroups {
			if ag.EmailDomain != "" {
				log.Info("approvalGroup.emailDomain is not yet supported and will be ignored",
					"policyName", rule.Name,
					"emailDomain", ag.EmailDomain,
				)
			}
		}

		includeRules, err := convertAccessRules(rule.Include)
		if err != nil {
			return nil, fmt.Errorf("policy %q include rules: %w", rule.Name, err)
		}
		excludeRules, err := convertAccessRules(rule.Exclude)
		if err != nil {
			return nil, fmt.Errorf("policy %q exclude rules: %w", rule.Name, err)
		}
		requireRules, err := convertAccessRules(rule.Require)
		if err != nil {
			return nil, fmt.Errorf("policy %q require rules: %w", rule.Name, err)
		}

		params = append(params, cloudflare.PolicyParams{
			Name:                         rule.Name,
			Decision:                     rule.Decision,
			Precedence:                   precedence,
			Include:                      includeRules,
			Exclude:                      excludeRules,
			Require:                      requireRules,
			SessionDuration:              rule.SessionDuration,
			PurposeJustificationRequired: rule.PurposeJustificationRequired,
			PurposeJustificationPrompt:   rule.PurposeJustificationPrompt,
			ApprovalRequired:             rule.ApprovalRequired,
			ApprovalGroups:               convertApprovalGroups(rule.ApprovalGroups),
		})
	}

	log.Info("syncing access policies",
		"applicationId", appID,
		"policyCount", len(params),
	)

	return accessService.SyncPolicies(ctx, accountID, appID, params)
}

// convertAccessRules converts CRD AccessRule slice to API AccessRuleParam slice.
// Returns an error if a rule references a list by name without providing its ID.
// Implements P0/P1/P2 rule types for alpha.3 with SDK-aligned naming:
//   - P0: No IdP (IP, IPList, Country, Everyone, ServiceToken, AnyValidServiceToken)
//   - P1: Basic IdP (Email, EmailList, EmailDomain, OIDCClaim)
//   - P2: Google Workspace (GSuiteGroup)
//   - P3: Not in current product scope (Certificate, Group, GitHub, Azure, Okta, SAML, etc.)
func convertAccessRules(crdRules []cfgatev1alpha1.AccessRule) ([]cloudflare.AccessRuleParam, error) {
	var rules []cloudflare.AccessRuleParam
	for _, r := range crdRules {
		// ============================================================
		// P0: No IdP required
		// ============================================================

		// IP ranges -> multiple rules (SDK: IPRule)
		if r.IP != nil {
			for _, cidr := range r.IP.Ranges {
				cidrCopy := cidr
				rules = append(rules, cloudflare.AccessRuleParam{
					IPRange: &cidrCopy,
				})
			}
			continue
		}

		// IPList -> by ID (SDK: IPListRule)
		if r.IPList != nil {
			if r.IPList.ID != "" {
				id := r.IPList.ID
				rules = append(rules, cloudflare.AccessRuleParam{
					IPListID: &id,
				})
			} else if r.IPList.Name != "" {
				return nil, fmt.Errorf("ipList rule specifies name %q without listID; listID is required for IP list rules", r.IPList.Name)
			} else {
				return nil, fmt.Errorf("ipList rule must specify listID")
			}
			continue
		}

		// Country codes -> multiple rules (SDK: CountryRule)
		if r.Country != nil {
			for _, code := range r.Country.Codes {
				codeCopy := code
				rules = append(rules, cloudflare.AccessRuleParam{
					Country: &codeCopy,
				})
			}
			continue
		}

		// Everyone (SDK: EveryoneRule)
		if r.Everyone != nil && *r.Everyone {
			everyone := true
			rules = append(rules, cloudflare.AccessRuleParam{
				Everyone: &everyone,
			})
			continue
		}

		// ServiceToken by ID (SDK: ServiceTokenRule)
		if r.ServiceToken != nil && r.ServiceToken.TokenID != "" {
			tokenID := r.ServiceToken.TokenID
			rules = append(rules, cloudflare.AccessRuleParam{
				ServiceTokenID: &tokenID,
			})
			continue
		}

		// AnyValidServiceToken (SDK: AnyValidServiceTokenRule)
		if r.AnyValidServiceToken != nil && *r.AnyValidServiceToken {
			anyValid := true
			rules = append(rules, cloudflare.AccessRuleParam{
				AnyValidServiceToken: &anyValid,
			})
			continue
		}

		// ============================================================
		// P1: Basic IdP required (Google Workspace)
		// ============================================================

		// Email addresses -> multiple rules (SDK: EmailRule)
		if r.Email != nil {
			for _, addr := range r.Email.Addresses {
				addrCopy := addr
				rules = append(rules, cloudflare.AccessRuleParam{
					Email: &addrCopy,
				})
			}
			continue
		}

		// EmailList -> by ID (SDK: EmailListRule)
		if r.EmailList != nil {
			if r.EmailList.ID != "" {
				id := r.EmailList.ID
				rules = append(rules, cloudflare.AccessRuleParam{
					EmailListID: &id,
				})
			} else if r.EmailList.Name != "" {
				return nil, fmt.Errorf("emailList rule specifies name %q without listID; listID is required for email list rules", r.EmailList.Name)
			} else {
				return nil, fmt.Errorf("emailList rule must specify listID")
			}
			continue
		}

		// EmailDomain (SDK: DomainRule)
		if r.EmailDomain != nil {
			domain := r.EmailDomain.Domain
			rules = append(rules, cloudflare.AccessRuleParam{
				EmailDomain: &domain,
			})
			continue
		}

		// OIDCClaim (SDK: AccessOIDCClaimRule)
		if r.OIDCClaim != nil {
			rules = append(rules, cloudflare.AccessRuleParam{
				OIDCClaim: &cloudflare.OIDCClaimParam{
					IdentityProviderID: r.OIDCClaim.IdentityProviderID,
					ClaimName:          r.OIDCClaim.ClaimName,
					ClaimValue:         r.OIDCClaim.ClaimValue,
				},
			})
			continue
		}

		// ============================================================
		// P2: Google Workspace Groups
		// ============================================================

		// GSuiteGroup (SDK: GSuiteGroupRule)
		if r.GSuiteGroup != nil {
			rules = append(rules, cloudflare.AccessRuleParam{
				GSuiteGroup: &cloudflare.GSuiteGroupParam{
					IdentityProviderID: r.GSuiteGroup.IdentityProviderID,
					Email:              r.GSuiteGroup.Email,
				},
			})
			continue
		}

		// ============================================================
		// P3: not in current product scope
		// ============================================================
		// Certificate, CommonName, Group, GitHub, Azure, Okta, SAML,
		// AuthenticationMethod, DevicePosture, ExternalEvaluation, LoginMethod
	}
	return rules, nil
}

// convertApprovalGroups converts CRD ApprovalGroup slice to API ApprovalGroupParam slice.
// Each approval group specifies email addresses and the number of approvals needed.
func convertApprovalGroups(groups []cfgatev1alpha1.ApprovalGroup) []cloudflare.ApprovalGroupParam {
	var result []cloudflare.ApprovalGroupParam
	for _, g := range groups {
		result = append(result, cloudflare.ApprovalGroupParam{
			EmailAddresses:  g.Emails,
			ApprovalsNeeded: g.ApprovalsNeeded,
		})
	}
	return result
}

// ensureServiceTokens ensures service tokens exist and stores credentials in Kubernetes secrets.
// For each configured service token, it calls EnsureServiceToken and writes the
// client ID and secret to the referenced Secret with owner references for garbage collection.
func (r *CloudflareAccessPolicyReconciler) ensureServiceTokens(
	ctx context.Context,
	accessService *cloudflare.AccessService,
	accountID string,
	policy *cfgatev1alpha1.CloudflareAccessPolicy,
) error {
	log := log.FromContext(ctx)

	if policy.Status.ServiceTokenIDs == nil {
		policy.Status.ServiceTokenIDs = make(map[string]string)
	}

	for _, tokenConfig := range policy.Spec.ServiceTokens {
		log.Info("ensuring service token",
			"tokenName", tokenConfig.Name,
			"secretRef", tokenConfig.SecretRef.Name,
		)

		params := cloudflare.ServiceTokenParams{
			Name:     tokenConfig.Name,
			Duration: tokenConfig.Duration,
		}

		secretWriter := &k8sSecretWriter{
			client:    r.Client,
			namespace: policy.Namespace,
			secretRef: tokenConfig.SecretRef,
			owner:     policy,
			scheme:    r.Scheme,
		}

		token, err := accessService.EnsureServiceToken(ctx, accountID, params, secretWriter)
		if err != nil {
			return fmt.Errorf("failed to ensure service token %s: %w", tokenConfig.Name, err)
		}

		policy.Status.ServiceTokenIDs[tokenConfig.Name] = token.ID
		log.V(1).Info("service token ready",
			"tokenId", token.ID,
			"tokenName", tokenConfig.Name,
		)
	}

	return nil
}

// k8sSecretWriter implements cloudflare.SecretWriter for Kubernetes secrets.
// It creates or updates secrets with owner references for garbage collection.
type k8sSecretWriter struct {
	client    client.Client
	namespace string
	secretRef cfgatev1alpha1.ServiceTokenSecretRef
	owner     *cfgatev1alpha1.CloudflareAccessPolicy
	scheme    *runtime.Scheme
}

// WriteSecret creates or updates a Kubernetes Secret with the given data.
// It sets the owner reference to the CloudflareAccessPolicy for automatic cleanup.
func (w *k8sSecretWriter) WriteSecret(ctx context.Context, name string, data map[string][]byte) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      w.secretRef.Name,
			Namespace: w.namespace,
		},
		Type: corev1.SecretTypeOpaque,
		Data: data,
	}

	// Set owner reference for garbage collection
	if err := controllerutil.SetControllerReference(w.owner, secret, w.scheme); err != nil {
		return fmt.Errorf("setting owner reference: %w", err)
	}

	// Create or update
	existing := &corev1.Secret{}
	err := w.client.Get(ctx, client.ObjectKeyFromObject(secret), existing)
	if apierrors.IsNotFound(err) {
		return w.client.Create(ctx, secret)
	}
	if err != nil {
		return err
	}

	existing.Data = data
	return w.client.Update(ctx, existing)
}

// reconcileDelete handles policy deletion cleanup.
// It deletes the Access Application (which cascades to policies), revokes service
// tokens, and removes the finalizer. Cleanup failure
// blocks finalizer removal and requeues. Set cfgate.io/deletion-policy=orphan
// to skip cleanup and allow deletion to proceed.
func (r *CloudflareAccessPolicyReconciler) reconcileDelete(
	ctx context.Context,
	policy *cfgatev1alpha1.CloudflareAccessPolicy,
) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	log.Info("reconciling deletion of CloudflareAccessPolicy")

	if !controllerutil.ContainsFinalizer(policy, accessPolicyFinalizer) {
		return ctrl.Result{}, nil
	}

	// Check deletion policy annotation
	if policy.Annotations["cfgate.io/deletion-policy"] == "orphan" {
		log.Info("deletion policy is orphan, skipping Cloudflare cleanup")
		return r.removeFinalizer(ctx, policy)
	}

	// Get credentials for deletion
	accessService, accountID, err := r.resolveCredentials(ctx, policy)
	if err != nil {
		log.Error(err, "failed to resolve credentials for deletion")
		return r.blockAccessDeletion(ctx, policy,
			fmt.Sprintf("Failed to resolve credentials: %s", err.Error()))
	}

	// Delete Access Application (cascades to policies)
	if policy.Status.ApplicationID != "" {
		log.Info("deleting Access Application",
			"applicationId", policy.Status.ApplicationID,
		)
		if err := accessService.DeleteApplication(ctx, accountID, policy.Status.ApplicationID); err != nil {
			log.Error(err, "failed to delete Access Application")
			return r.blockAccessDeletion(ctx, policy,
				fmt.Sprintf("Failed to delete Access Application %s: %s", policy.Status.ApplicationID, err.Error()))
		}
		r.Recorder.Eventf(policy, nil, corev1.EventTypeNormal, "ApplicationDeleted", "Delete",
			"Access Application %s deleted", policy.Status.ApplicationID)
	}

	// Revoke service tokens
	for name, tokenID := range policy.Status.ServiceTokenIDs {
		log.V(1).Info("revoking service token",
			"tokenName", name,
			"tokenId", tokenID,
		)
		if err := r.revokeServiceToken(ctx, accessService, accountID, tokenID); err != nil {
			log.Error(err, "failed to revoke service token", "tokenName", name)
			return r.blockAccessDeletion(ctx, policy,
				fmt.Sprintf("Failed to revoke service token %s (%s): %s", name, tokenID, err.Error()))
		}
	}

	return r.removeFinalizer(ctx, policy)
}

// blockAccessDeletion emits a warning event and returns a requeue result that
// blocks finalizer removal. After the retry budget, the event reason escalates
// to CleanupBlocked.
func (r *CloudflareAccessPolicyReconciler) blockAccessDeletion(
	ctx context.Context,
	policy *cfgatev1alpha1.CloudflareAccessPolicy,
	detail string,
) (ctrl.Result, error) {
	retryElapsed := time.Since(policy.DeletionTimestamp.Time)
	suffix := " Set annotation cfgate.io/deletion-policy=orphan to skip cleanup and remove finalizer."
	if retryElapsed < accessDeletionRetryBudget {
		r.Recorder.Eventf(policy, nil, corev1.EventTypeWarning, "CleanupFailed", "Delete",
			"%s.%s", detail, suffix)
	} else {
		r.Recorder.Eventf(policy, nil, corev1.EventTypeWarning, "CleanupBlocked", "Delete",
			"%s (blocked after %s of retries).%s", detail, retryElapsed.Round(time.Second), suffix)
	}
	return ctrl.Result{RequeueAfter: accessDeletionRequeueInterval}, nil
}

// revokeServiceToken revokes a service token in Cloudflare.
// Called during deletion cleanup to remove orphaned tokens.
func (r *CloudflareAccessPolicyReconciler) revokeServiceToken(
	ctx context.Context,
	accessService *cloudflare.AccessService,
	accountID, tokenID string,
) error {
	return accessService.Client().DeleteServiceToken(ctx, accountID, tokenID)
}

// removeFinalizer removes the access policy finalizer using a patch operation.
// This is the final step in deletion reconciliation.
func (r *CloudflareAccessPolicyReconciler) removeFinalizer(
	ctx context.Context,
	policy *cfgatev1alpha1.CloudflareAccessPolicy,
) (ctrl.Result, error) {
	patch := client.MergeFrom(policy.DeepCopy())
	controllerutil.RemoveFinalizer(policy, accessPolicyFinalizer)
	if err := r.Patch(ctx, policy, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to remove finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}

// updateStatus updates the CloudflareAccessPolicy status subresource.
// It re-fetches the resource to avoid conflicts before applying the status update.
func (r *CloudflareAccessPolicyReconciler) updateStatus(ctx context.Context, policy *cfgatev1alpha1.CloudflareAccessPolicy) error {
	// Re-fetch to avoid conflicts
	var current cfgatev1alpha1.CloudflareAccessPolicy
	if err := r.Get(ctx, types.NamespacedName{Name: policy.Name, Namespace: policy.Namespace}, &current); err != nil {
		return fmt.Errorf("failed to re-fetch policy: %w", err)
	}

	if accessPolicyStatusEqual(&current.Status, &policy.Status) {
		return nil
	}

	// Copy status
	current.Status = policy.Status

	if err := r.Status().Update(ctx, &current); err != nil {
		return fmt.Errorf("failed to update status: %w", err)
	}

	return nil
}

// accessPolicyStatusEqual compares two CloudflareAccessPolicyStatus values for equality,
// ignoring condition LastTransitionTime to avoid spurious status updates.
func accessPolicyStatusEqual(a, b *cfgatev1alpha1.CloudflareAccessPolicyStatus) bool {
	if a.ObservedGeneration != b.ObservedGeneration {
		return false
	}

	if a.ApplicationID != b.ApplicationID {
		return false
	}
	if a.ApplicationAUD != b.ApplicationAUD {
		return false
	}
	if a.AttachedTargets != b.AttachedTargets {
		return false
	}

	if !reflect.DeepEqual(a.ServiceTokenIDs, b.ServiceTokenIDs) {
		return false
	}

	// Compare conditions (ignoring LastTransitionTime)
	if len(a.Conditions) != len(b.Conditions) {
		return false
	}
	for i := range a.Conditions {
		if a.Conditions[i].Type != b.Conditions[i].Type ||
			a.Conditions[i].Status != b.Conditions[i].Status ||
			a.Conditions[i].Reason != b.Conditions[i].Reason ||
			a.Conditions[i].Message != b.Conditions[i].Message {
			return false
		}
	}

	// Compare ancestors
	if len(a.Ancestors) != len(b.Ancestors) {
		return false
	}
	for i := range a.Ancestors {
		if !reflect.DeepEqual(a.Ancestors[i].AncestorRef, b.Ancestors[i].AncestorRef) ||
			a.Ancestors[i].ControllerName != b.Ancestors[i].ControllerName {
			return false
		}
		if len(a.Ancestors[i].Conditions) != len(b.Ancestors[i].Conditions) {
			return false
		}
		for j := range a.Ancestors[i].Conditions {
			if a.Ancestors[i].Conditions[j].Type != b.Ancestors[i].Conditions[j].Type ||
				a.Ancestors[i].Conditions[j].Status != b.Ancestors[i].Conditions[j].Status ||
				a.Ancestors[i].Conditions[j].Reason != b.Ancestors[i].Conditions[j].Reason ||
				a.Ancestors[i].Conditions[j].Message != b.Ancestors[i].Conditions[j].Message {
				return false
			}
		}
	}

	return true
}

// updateAncestorStatuses updates the PolicyAncestorStatus for each resolved target.
// This follows the Gateway API pattern for reporting policy acceptance per-target.
func (r *CloudflareAccessPolicyReconciler) updateAncestorStatuses(
	policy *cfgatev1alpha1.CloudflareAccessPolicy,
	policyCtx *ctxwrappers.AccessPolicyContext,
) {
	generation := policy.Generation
	policy.Status.Ancestors = nil // Reset ancestors

	for _, target := range policyCtx.ResolvedTargets() {
		ancestorRef := cfgatev1alpha1.PolicyTargetReference{
			Group:     "gateway.networking.k8s.io",
			Kind:      target.Kind,
			Name:      target.Name,
			Namespace: ptr.To(target.Namespace),
		}

		var conditions []metav1.Condition
		if target.Resolved && target.Error == nil {
			conditions = []metav1.Condition{
				status.NewPolicyAcceptedCondition(true, status.PolicyReasonAccepted,
					"Policy accepted for target.", generation),
			}
		} else {
			msg := "Target not found."
			if target.Error != nil {
				msg = target.Error.Error()
			}
			conditions = []metav1.Condition{
				status.NewPolicyAcceptedCondition(false, status.PolicyReasonTargetNotFound,
					msg, generation),
			}
		}

		policy.Status.Ancestors = append(policy.Status.Ancestors, cfgatev1alpha1.PolicyAncestorStatus{
			AncestorRef:    ancestorRef,
			ControllerName: AccessPolicyControllerName,
			Conditions:     conditions,
		})
	}
}

// getCloudflareClient creates a Cloudflare client from credentials.
// It uses the injected CFClient for testing, otherwise reads from the secret
// and optionally uses the CredentialCache to avoid repeated client creation.
func (r *CloudflareAccessPolicyReconciler) getCloudflareClient(
	ctx context.Context,
	policyNamespace string,
	secretRef *cfgatev1alpha1.CloudflareSecretRef,
) (cloudflare.Client, error) {
	// If injected client exists, use it (for testing)
	if r.CFClient != nil {
		return r.CFClient, nil
	}

	// Get credentials from secret
	secretNamespace := policyNamespace
	if secretRef.Namespace != nil && *secretRef.Namespace != "" {
		secretNamespace = *secretRef.Namespace
	}

	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      secretRef.Name,
		Namespace: secretNamespace,
	}, secret); err != nil {
		return nil, fmt.Errorf("failed to get credentials secret: %w", err)
	}

	// Use cache if available
	if r.CredentialCache != nil {
		return r.CredentialCache.GetOrCreate(ctx, secret, func() (cloudflare.Client, error) {
			return r.createClientFromSecret(secret)
		})
	}

	return r.createClientFromSecret(secret)
}

// createClientFromSecret creates a Cloudflare client from a Kubernetes Secret.
// It expects the CLOUDFLARE_API_TOKEN key to contain the API token.
func (r *CloudflareAccessPolicyReconciler) createClientFromSecret(secret *corev1.Secret) (cloudflare.Client, error) {
	tokenKey := "CLOUDFLARE_API_TOKEN"

	token, ok := secret.Data[tokenKey]
	if !ok {
		return nil, fmt.Errorf("API token key %q not found in secret", tokenKey)
	}

	return cloudflare.NewClient(string(token))
}

// accessPolicyTargetKey builds an index key for a target reference.
func accessPolicyTargetKey(kind, namespace, name string) string {
	return kind + "/" + namespace + "/" + name
}

// accessPolicyTargetIndexFunc extracts index keys from a CloudflareAccessPolicy.
// Each key encodes Kind/Namespace/Name for all targetRef and targetRefs entries.
func accessPolicyTargetIndexFunc(obj client.Object) []string {
	policy, ok := obj.(*cfgatev1alpha1.CloudflareAccessPolicy)
	if !ok {
		return nil
	}

	var keys []string
	refs := policy.Spec.TargetRefs
	if policy.Spec.TargetRef != nil {
		refs = append([]cfgatev1alpha1.PolicyTargetReference{*policy.Spec.TargetRef}, refs...)
	}

	for _, ref := range refs {
		ns := policy.Namespace
		if ref.Namespace != nil {
			ns = *ref.Namespace
		}
		keys = append(keys, accessPolicyTargetKey(ref.Kind, ns, ref.Name))
	}
	return keys
}

// SetupWithManager sets up the controller with the Manager.
//
// Watched resources:
//   - CloudflareAccessPolicy (primary resource)
//   - Secret (owned, for service token credentials)
//   - HTTPRoute (for policies targeting HTTPRoute)
//   - ReferenceGrant (conditional, for cross-namespace validation)
//
// Route watches use GenerationChangedPredicate to filter out status-only updates.
// Optional CRD watches are registered only when their corresponding FeatureGate is enabled.
func (r *CloudflareAccessPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	log := mgr.GetLogger().WithName("controller").WithName("accesspolicy")
	log.Info("registering controller with manager")

	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(),
		&cfgatev1alpha1.CloudflareAccessPolicy{},
		accessPolicyTargetIndex,
		accessPolicyTargetIndexFunc,
	); err != nil {
		return fmt.Errorf("failed to create target index: %w", err)
	}

	controllerBuilder := ctrl.NewControllerManagedBy(mgr).
		For(&cfgatev1alpha1.CloudflareAccessPolicy{},
			builder.WithPredicates(GenerationOrDeletionPredicate),
		).
		Owns(&corev1.Secret{}). // Service token secrets
		Watches(
			&gateway.HTTPRoute{},
			handler.EnqueueRequestsFromMapFunc(r.findPoliciesForHTTPRoute),
			builder.WithPredicates(predicate.GenerationChangedPredicate{}),
		)

	// Conditionally watch ReferenceGrant
	if r.FeatureGates != nil && r.FeatureGates.HasReferenceGrantSupport() {
		controllerBuilder = controllerBuilder.Watches(
			&gatewayv1b1.ReferenceGrant{},
			handler.EnqueueRequestsFromMapFunc(r.findPoliciesForReferenceGrant),
			builder.WithPredicates(predicate.GenerationChangedPredicate{}),
		)
	}

	return controllerBuilder.Complete(r)
}

// findPoliciesForHTTPRoute returns reconcile requests for policies targeting the given HTTPRoute.
func (r *CloudflareAccessPolicyReconciler) findPoliciesForHTTPRoute(ctx context.Context, obj client.Object) []reconcile.Request {
	return r.findPoliciesForTarget(ctx, "HTTPRoute", obj)
}

// findPoliciesForTarget finds all CloudflareAccessPolicies targeting a specific object.
// Uses the field indexer to efficiently look up policies by target Kind/Namespace/Name.
func (r *CloudflareAccessPolicyReconciler) findPoliciesForTarget(ctx context.Context, kind string, obj client.Object) []reconcile.Request {
	log := log.FromContext(ctx)

	var policies cfgatev1alpha1.CloudflareAccessPolicyList
	key := accessPolicyTargetKey(kind, obj.GetNamespace(), obj.GetName())
	if err := r.List(ctx, &policies, client.MatchingFields{accessPolicyTargetIndex: key}); err != nil {
		log.Error(err, "failed to list CloudflareAccessPolicies by index")
		return nil
	}

	var requests []reconcile.Request
	for _, policy := range policies.Items {
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Namespace: policy.Namespace,
				Name:      policy.Name,
			},
		})
	}

	if len(requests) > 0 {
		log.V(1).Info("found policies for target",
			"kind", kind,
			"target", obj.GetNamespace()+"/"+obj.GetName(),
			"policyCount", len(requests),
		)
	}

	return requests
}

// findPoliciesForReferenceGrant returns reconcile requests for policies affected by ReferenceGrant changes.
// When a ReferenceGrant is created/updated/deleted, policies with cross-namespace references
// to that namespace need to be re-evaluated.
func (r *CloudflareAccessPolicyReconciler) findPoliciesForReferenceGrant(ctx context.Context, obj client.Object) []reconcile.Request {
	log := log.FromContext(ctx)
	grant, ok := obj.(*gatewayv1b1.ReferenceGrant)
	if !ok {
		return nil
	}

	// Find policies in namespaces that reference this grant's namespace
	var policies cfgatev1alpha1.CloudflareAccessPolicyList
	if err := r.List(ctx, &policies); err != nil {
		log.Error(err, "failed to list CloudflareAccessPolicies")
		return nil
	}

	var requests []reconcile.Request
	for _, policy := range policies.Items {
		// Check if policy has cross-namespace refs to grant's namespace
		refs := policy.Spec.TargetRefs
		if policy.Spec.TargetRef != nil {
			refs = append([]cfgatev1alpha1.PolicyTargetReference{*policy.Spec.TargetRef}, refs...)
		}

		for _, ref := range refs {
			if ref.Namespace == nil || *ref.Namespace != grant.Namespace {
				continue
			}
			// This policy references the grant's namespace
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: policy.Namespace,
					Name:      policy.Name,
				},
			})
			break
		}
	}

	return requests
}
