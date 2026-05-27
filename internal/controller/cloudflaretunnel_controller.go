package controller

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gateway "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/cloudflare"
	"cfgate.io/cfgate/internal/cloudflared"
	"cfgate.io/cfgate/internal/controller/annotations"
	"cfgate.io/cfgate/internal/controller/status"
)

const (
	// tunnelFinalizer is the finalizer for CloudflareTunnel resources.
	tunnelFinalizer = "cfgate.io/tunnel-cleanup"

	// requeueAfterError is the requeue delay after an error.
	requeueAfterError = 30 * time.Second

	// requeueAfterSuccess is the requeue delay for periodic sync.
	requeueAfterSuccess = 5 * time.Minute

	// deletionRetryBudget is the maximum time to retry CF tunnel deletion
	// before emitting an escalated warning. After this budget, the controller
	// keeps blocking (does not remove the finalizer). The only escape is the
	// cfgate.io/deletion-policy=orphan annotation.
	// 2 minutes handles cloudflared connection drain (~30s) with generous margin.
	deletionRetryBudget = 2 * time.Minute

	// deletionRequeueInterval is the requeue delay between deletion retries.
	deletionRequeueInterval = 10 * time.Second

	// configHashAnnotation stores a SHA-256 hash of the last-synced tunnel
	// configuration, enabling the config diff gate to skip redundant API updates.
	configHashAnnotation = "cfgate.io/config-hash"
)

// CloudflareTunnelReconciler reconciles a CloudflareTunnel object.
// It manages the complete tunnel lifecycle: credential validation, tunnel
// creation/adoption, cloudflared deployment, and configuration sync.
type CloudflareTunnelReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder events.EventRecorder

	// APIReader provides uncached reads for watch mappers to avoid informer lag.
	APIReader client.Reader

	// CFClient is the Cloudflare API client. Injected for testing.
	CFClient cloudflare.Client

	// Builder creates Kubernetes resources for cloudflared.
	Builder cloudflared.Builder

	// CredentialCache caches validated Cloudflare clients to avoid repeated validations.
	CredentialCache *cloudflare.CredentialCache
}

// +kubebuilder:rbac:groups=cfgate.io,resources=cloudflaretunnels,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cfgate.io,resources=cloudflaretunnels/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cfgate.io,resources=cloudflaretunnels/finalizers,verbs=update
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways;httproutes,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete

// Reconcile handles the reconciliation loop for CloudflareTunnel resources.
// It ensures the Cloudflare tunnel exists, deploys cloudflared, and syncs configuration.
//
// The reconciliation proceeds through these phases:
//  1. Fetch the CloudflareTunnel resource
//  2. Handle deletion via finalizers (cleanup tunnel from Cloudflare)
//  3. Validate Cloudflare API credentials
//  4. Ensure tunnel exists in Cloudflare (create or adopt)
//  5. Deploy cloudflared connector (Deployment + Secret)
//  6. Sync ingress configuration from Gateway/HTTPRoute resources
//  7. Update status conditions
//
// On error, the controller requeues after 30 seconds. On success, it requeues
// after 5 minutes for periodic configuration sync.
func (r *CloudflareTunnelReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx).WithName("controller").WithName("tunnel")
	log.Info("starting reconciliation", "namespace", req.Namespace, "name", req.Name)

	// 1. Fetch CloudflareTunnel resource
	var tunnel cfgatev1alpha1.CloudflareTunnel
	if err := r.Get(ctx, req.NamespacedName, &tunnel); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("CloudflareTunnel not found, ignoring")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to get CloudflareTunnel: %w", err)
	}

	// 2. Handle deletion (finalizers)
	if !tunnel.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &tunnel)
	}

	// Add finalizer if not present (using patch to reduce lock contention)
	if !controllerutil.ContainsFinalizer(&tunnel, tunnelFinalizer) {
		patch := client.MergeFrom(tunnel.DeepCopy())
		controllerutil.AddFinalizer(&tunnel, tunnelFinalizer)
		if err := r.Patch(ctx, &tunnel, patch); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to add finalizer: %w", err)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	tunnelLifecycleUnchanged := tunnel.Generation == tunnel.Status.ObservedGeneration &&
		isTunnelHealthy(&tunnel) &&
		tunnel.Status.LastSyncTime != nil &&
		time.Since(tunnel.Status.LastSyncTime.Time) < 30*time.Minute

	if tunnelLifecycleUnchanged {
		log.V(1).Info("tunnel lifecycle unchanged, skipping credential/tunnel/deployment checks",
			"generation", tunnel.Generation,
			"lastSync", tunnel.Status.LastSyncTime.Time)

		deploymentName := cloudflared.DeploymentName(tunnel.Name)
		var deployment appsv1.Deployment
		if err := r.Get(ctx, types.NamespacedName{Name: deploymentName, Namespace: tunnel.Namespace}, &deployment); err == nil {
			if deployment.Status.ReadyReplicas != tunnel.Status.ReadyReplicas ||
				deployment.Status.Replicas != tunnel.Status.Replicas {
				tunnel.Status.ReadyReplicas = deployment.Status.ReadyReplicas
				tunnel.Status.Replicas = deployment.Status.Replicas
			}
		}

		originState, err := r.ensureCloudflaredOriginCAPoolDeployment(ctx, &tunnel)
		if err != nil {
			log.Error(err, "failed to reconcile cloudflared origin CA mounts in guard path")
			r.setCondition(&tunnel, status.ConditionTypeCloudflaredDeployed, metav1.ConditionFalse, status.ReasonDeploymentError, err.Error())
			r.setCondition(&tunnel, status.ConditionTypeReady, metav1.ConditionFalse, status.ReasonDeploymentError, "Failed to deploy cloudflared")
			_ = r.updateStatus(ctx, &tunnel)
			r.Recorder.Eventf(&tunnel, nil, corev1.EventTypeWarning, "DeploymentError", "Deploy", "%s", err.Error())
			return ctrl.Result{RequeueAfter: requeueAfterError}, nil
		}

		syncErr := r.syncConfigurationWithRuntime(ctx, &tunnel, originState.runtime)
		if syncErr != nil {
			log.Error(syncErr, "failed to sync configuration in guard path")
			r.setCondition(&tunnel, status.ConditionTypeConfigurationSynced, metav1.ConditionFalse, status.ReasonConfigSyncError, syncErr.Error())
		} else {
			r.setCondition(&tunnel, status.ConditionTypeConfigurationSynced, metav1.ConditionTrue, status.ReasonConfigurationSynced,
				fmt.Sprintf("Configuration synced with %d ingress rules", tunnel.Status.ConnectedRouteCount))
		}

		now := metav1.Now()
		tunnel.Status.LastSyncTime = &now
		if err := r.updateStatus(ctx, &tunnel); err != nil {
			log.Error(err, "failed to update status in guard path")
			return ctrl.Result{RequeueAfter: requeueAfterError}, nil
		}
		if syncErr != nil {
			return ctrl.Result{RequeueAfter: requeueAfterError}, nil
		}
		return ctrl.Result{RequeueAfter: requeueAfterSuccess}, nil
	}

	// 3. Validate credentials
	if err := r.validateCredentials(ctx, &tunnel); err != nil {
		log.Error(err, "credentials validation failed")
		r.setCondition(&tunnel, status.ConditionTypeCredentialsValid, metav1.ConditionFalse, status.ReasonCredentialsInvalid, err.Error())
		r.setCondition(&tunnel, status.ConditionTypeReady, metav1.ConditionFalse, status.ReasonCredentialsInvalid, "API credentials are invalid")
		if err := r.updateStatus(ctx, &tunnel); err != nil {
			log.Error(err, "failed to update status")
		}
		r.Recorder.Eventf(&tunnel, nil, corev1.EventTypeWarning, "CredentialsInvalid", "Validate", "%s", err.Error())
		return ctrl.Result{RequeueAfter: requeueAfterError}, nil
	}
	r.setCondition(&tunnel, status.ConditionTypeCredentialsValid, metav1.ConditionTrue, status.ReasonCredentialsValid, "API token validated successfully")

	// 4. Resolve/create tunnel
	if err := r.ensureTunnel(ctx, &tunnel); err != nil {
		log.Error(err, "failed to ensure tunnel")
		r.setCondition(&tunnel, status.ConditionTypeTunnelReady, metav1.ConditionFalse, status.ReasonTunnelError, err.Error())
		r.setCondition(&tunnel, status.ConditionTypeReady, metav1.ConditionFalse, status.ReasonTunnelError, "Failed to ensure tunnel")
		if err := r.updateStatus(ctx, &tunnel); err != nil {
			log.Error(err, "failed to update status")
		}
		r.Recorder.Eventf(&tunnel, nil, corev1.EventTypeWarning, "TunnelError", "Reconcile", "%s", err.Error())
		return ctrl.Result{RequeueAfter: requeueAfterError}, nil
	}
	r.setCondition(&tunnel, status.ConditionTypeTunnelReady, metav1.ConditionTrue, status.ReasonTunnelReady, fmt.Sprintf("Tunnel %s ready", tunnel.Status.TunnelID))

	// 5. Deploy cloudflared
	originState, err := r.deployCloudflared(ctx, &tunnel)
	if err != nil {
		log.Error(err, "failed to deploy cloudflared")
		r.setCondition(&tunnel, status.ConditionTypeCloudflaredDeployed, metav1.ConditionFalse, status.ReasonDeploymentError, err.Error())
		r.setCondition(&tunnel, status.ConditionTypeReady, metav1.ConditionFalse, status.ReasonDeploymentError, "Failed to deploy cloudflared")
		if err := r.updateStatus(ctx, &tunnel); err != nil {
			log.Error(err, "failed to update status")
		}
		r.Recorder.Eventf(&tunnel, nil, corev1.EventTypeWarning, "DeploymentError", "Deploy", "%s", err.Error())
		return ctrl.Result{RequeueAfter: requeueAfterError}, nil
	}
	r.setCondition(&tunnel, status.ConditionTypeCloudflaredDeployed, metav1.ConditionTrue, status.ReasonDeploymentReady, "cloudflared deployment ready")

	// 6. Sync configuration
	if err := r.syncConfigurationWithRuntime(ctx, &tunnel, originState.runtime); err != nil {
		log.Error(err, "failed to sync configuration")
		r.setCondition(&tunnel, status.ConditionTypeConfigurationSynced, metav1.ConditionFalse, status.ReasonConfigSyncError, err.Error())
		r.setCondition(&tunnel, status.ConditionTypeReady, metav1.ConditionFalse, status.ReasonConfigSyncError, "Failed to sync configuration")
		if err := r.updateStatus(ctx, &tunnel); err != nil {
			log.Error(err, "failed to update status")
		}
		r.Recorder.Eventf(&tunnel, nil, corev1.EventTypeWarning, "ConfigSyncError", "Sync", "%s", err.Error())
		return ctrl.Result{RequeueAfter: requeueAfterError}, nil
	}
	r.setCondition(&tunnel, status.ConditionTypeConfigurationSynced, metav1.ConditionTrue, status.ReasonConfigurationSynced, fmt.Sprintf("Configuration synced with %d ingress rules", tunnel.Status.ConnectedRouteCount))

	// Note: DNS management is handled by CloudflareDNS CRD

	// 7. Update status
	r.setCondition(&tunnel, status.ConditionTypeReady, metav1.ConditionTrue, status.ReasonTunnelOperational, "Tunnel is fully operational")
	tunnel.Status.ObservedGeneration = tunnel.Generation
	now := metav1.Now()
	tunnel.Status.LastSyncTime = &now

	if err := r.updateStatus(ctx, &tunnel); err != nil {
		log.Error(err, "failed to update status")
		return ctrl.Result{RequeueAfter: requeueAfterError}, nil
	}

	r.Recorder.Eventf(&tunnel, nil, corev1.EventTypeNormal, "Reconciled", "Reconcile", "Tunnel reconciled successfully")
	return ctrl.Result{RequeueAfter: requeueAfterSuccess}, nil
}

// SetupWithManager sets up the controller with the Manager.
// It configures watches for CloudflareTunnel and owned resources.
//
// Watched resources:
//   - CloudflareTunnel (primary resource)
//   - Deployment (owned, for cloudflared)
//   - Secret (owned, for tunnel token)
//   - Gateway (via annotation cfgate.io/tunnel-ref)
//   - HTTPRoute (via parent Gateway reference)
//   - Secret (origin default and named origin CA Secret data changes)
//   - ConfigMap (BackendTLSPolicy CA bundle ConfigMap data changes)
//
// Gateway and HTTPRoute watches include cfgate.io/* annotation changes because
// tunnel references and origin settings may change without generation bumps.
// Secret and ConfigMap watches use ResourceVersion because data updates do not
// increment metadata.generation. Their mappers enqueue only tunnels with origin
// CA inputs affected by the changed object.
func (r *CloudflareTunnelReconciler) SetupWithManager(mgr ctrl.Manager) error {
	log := mgr.GetLogger().WithName("controller").WithName("tunnel")
	log.Info("registering controller with manager")
	r.APIReader = mgr.GetAPIReader()

	return ctrl.NewControllerManagedBy(mgr).
		For(&cfgatev1alpha1.CloudflareTunnel{},
			builder.WithPredicates(GenerationOrDeletionPredicate),
		).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Secret{}).
		// Watch Gateway resources that reference our tunnels
		Watches(
			&gateway.Gateway{},
			handler.EnqueueRequestsFromMapFunc(r.findTunnelsForGateway),
			builder.WithPredicates(CfgateAnnotationOrGenerationPredicate, GatewayCreateAnnotationFilter),
		).
		// Watch HTTPRoute resources that may affect tunnel configuration
		Watches(
			&gateway.HTTPRoute{},
			handler.EnqueueRequestsFromMapFunc(r.findTunnelsForHTTPRoute),
			builder.WithPredicates(CfgateAnnotationOrGenerationPredicate),
		).
		Watches(
			&cfgatev1alpha1.CloudflareOriginPolicy{},
			handler.EnqueueRequestsFromMapFunc(r.findAllTunnels),
			builder.WithPredicates(predicate.GenerationChangedPredicate{}),
		).
		Watches(
			&gateway.BackendTLSPolicy{},
			handler.EnqueueRequestsFromMapFunc(r.findAllTunnels),
			builder.WithPredicates(predicate.GenerationChangedPredicate{}),
		).
		Watches(
			&gatewayv1beta1.ReferenceGrant{},
			handler.EnqueueRequestsFromMapFunc(r.findAllTunnels),
		).
		Watches(
			&corev1.ConfigMap{},
			handler.EnqueueRequestsFromMapFunc(r.findTunnelsForConfigMap),
			builder.WithPredicates(DataResourceChangedPredicate),
		).
		Watches(
			&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(r.findTunnelsForSecret),
			builder.WithPredicates(DataResourceChangedPredicate),
		).
		Complete(r)
}

func (r *CloudflareTunnelReconciler) watchReader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

func (r *CloudflareTunnelReconciler) findAllTunnels(ctx context.Context, obj client.Object) []reconcile.Request {
	var tunnels cfgatev1alpha1.CloudflareTunnelList
	reader := r.watchReader()
	if reader == nil {
		return nil
	}
	if err := reader.List(ctx, &tunnels); err != nil {
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(tunnels.Items))
	for _, tunnel := range tunnels.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: tunnel.Namespace, Name: tunnel.Name}})
	}
	return reqs
}

func (r *CloudflareTunnelReconciler) findTunnelsForSecret(ctx context.Context, obj client.Object) []reconcile.Request {
	secret, ok := obj.(*corev1.Secret)
	if !ok {
		return nil
	}
	reader := r.watchReader()
	if reader == nil {
		return nil
	}
	var tunnels cfgatev1alpha1.CloudflareTunnelList
	if err := reader.List(ctx, &tunnels); err != nil {
		return nil
	}

	var reqs []reconcile.Request
	seen := map[types.NamespacedName]struct{}{}
	for _, tunnel := range tunnels.Items {
		if ref := tunnel.Spec.OriginDefaults.CAPoolSecretRef; ref != nil &&
			tunnel.Namespace == secret.Namespace &&
			ref.Name == secret.Name {
			appendTunnelRequest(&reqs, seen, tunnel.Namespace, tunnel.Name)
		}
		for _, pool := range tunnel.Spec.OriginCAPools {
			refNS := tunnel.Namespace
			if pool.SecretRef.Namespace != nil && *pool.SecretRef.Namespace != "" {
				refNS = *pool.SecretRef.Namespace
			}
			if refNS == secret.Namespace && pool.SecretRef.Name == secret.Name {
				appendTunnelRequest(&reqs, seen, tunnel.Namespace, tunnel.Name)
			}
		}
	}
	sortReconcileRequests(reqs)
	return reqs
}

func (r *CloudflareTunnelReconciler) findTunnelsForConfigMap(ctx context.Context, obj client.Object) []reconcile.Request {
	configMap, ok := obj.(*corev1.ConfigMap)
	if !ok {
		return nil
	}
	reader := r.watchReader()
	if reader == nil {
		return nil
	}

	var policies gateway.BackendTLSPolicyList
	if err := reader.List(ctx, &policies, client.InNamespace(configMap.Namespace)); err != nil {
		return nil
	}
	targetServices := map[types.NamespacedName]struct{}{}
	for _, policy := range policies.Items {
		configMapName, ok := backendTLSPolicyCAConfigMap(policy)
		if !ok || configMapName != configMap.Name {
			continue
		}
		service, ok := backendTLSPolicyTargetService(policy)
		if ok {
			targetServices[service] = struct{}{}
		}
	}
	if len(targetServices) == 0 {
		return nil
	}

	var routes gateway.HTTPRouteList
	if err := reader.List(ctx, &routes); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	seen := map[types.NamespacedName]struct{}{}
	gatewayCache := map[types.NamespacedName]*gateway.Gateway{}
	for _, route := range routes.Items {
		routeTargetsService := false
		for _, rule := range route.Spec.Rules {
			service, ok := routeRuleBackendService(route.Namespace, rule)
			if !ok {
				continue
			}
			if _, ok := targetServices[service]; ok {
				routeTargetsService = true
				break
			}
		}
		if !routeTargetsService {
			continue
		}

		for _, parentRef := range route.Spec.ParentRefs {
			if !isGatewayParentRef(parentRef) {
				continue
			}
			gwNamespace := route.Namespace
			if parentRef.Namespace != nil {
				gwNamespace = string(*parentRef.Namespace)
			}
			gwName := types.NamespacedName{Namespace: gwNamespace, Name: string(parentRef.Name)}
			gw := gatewayCache[gwName]
			if gw == nil {
				var fetched gateway.Gateway
				if err := reader.Get(ctx, gwName, &fetched); err != nil {
					continue
				}
				gw = &fetched
				gatewayCache[gwName] = gw
			}
			if len(routeHostnamesForGateway(&route, gw, parentRef)) == 0 {
				continue
			}
			ref := annotations.GetAnnotation(gw, annotations.AnnotationTunnelRef)
			if ref == "" {
				continue
			}
			ns, name, err := annotations.ParseNamespacedName(ref, gw.Namespace)
			if err != nil {
				continue
			}
			appendTunnelRequest(&reqs, seen, ns, name)
		}
	}
	sortReconcileRequests(reqs)
	return reqs
}

func appendTunnelRequest(reqs *[]reconcile.Request, seen map[types.NamespacedName]struct{}, namespace, name string) {
	key := types.NamespacedName{Namespace: namespace, Name: name}
	if key.Namespace == "" || key.Name == "" {
		return
	}
	if _, ok := seen[key]; ok {
		return
	}
	seen[key] = struct{}{}
	*reqs = append(*reqs, reconcile.Request{NamespacedName: key})
}

func sortReconcileRequests(reqs []reconcile.Request) {
	sort.SliceStable(reqs, func(i, j int) bool {
		left, right := reqs[i].NamespacedName, reqs[j].NamespacedName
		if left.Namespace != right.Namespace {
			return left.Namespace < right.Namespace
		}
		return left.Name < right.Name
	})
}

// findTunnelsForGateway returns reconcile requests for tunnels referenced by a Gateway.
func (r *CloudflareTunnelReconciler) findTunnelsForGateway(ctx context.Context, obj client.Object) []reconcile.Request {
	gw, ok := obj.(*gateway.Gateway)
	if !ok {
		return nil
	}

	// Get tunnel reference from annotation
	ref := annotations.GetAnnotation(gw, annotations.AnnotationTunnelRef)
	if ref == "" {
		return nil
	}

	ns, name, err := annotations.ParseNamespacedName(ref, gw.Namespace)
	if err != nil {
		return nil
	}

	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{
			Namespace: ns,
			Name:      name,
		},
	}}
}

// findTunnelsForHTTPRoute returns reconcile requests for tunnels affected by HTTPRoute changes.
func (r *CloudflareTunnelReconciler) findTunnelsForHTTPRoute(ctx context.Context, obj client.Object) []reconcile.Request {
	route, ok := obj.(*gateway.HTTPRoute)
	if !ok {
		return nil
	}

	// Find parent Gateways, then their tunnels
	var requests []reconcile.Request
	for _, parentRef := range route.Spec.ParentRefs {
		if !isGatewayParentRef(parentRef) {
			continue
		}

		gwNamespace := route.Namespace
		if parentRef.Namespace != nil {
			gwNamespace = string(*parentRef.Namespace)
		}

		gw := &gateway.Gateway{}
		if err := r.APIReader.Get(ctx, types.NamespacedName{
			Namespace: gwNamespace,
			Name:      string(parentRef.Name),
		}, gw); err != nil {
			continue
		}

		// Get tunnel from Gateway annotation
		ref := annotations.GetAnnotation(gw, annotations.AnnotationTunnelRef)
		if ref == "" {
			continue
		}

		ns, name, err := annotations.ParseNamespacedName(ref, gw.Namespace)
		if err != nil {
			continue
		}

		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Namespace: ns,
				Name:      name,
			},
		})
	}

	return requests
}

// validateCredentials validates the Cloudflare API credentials.
// Returns an error if credentials are invalid or missing required permissions.
func (r *CloudflareTunnelReconciler) validateCredentials(ctx context.Context, tunnel *cfgatev1alpha1.CloudflareTunnel) error {
	log := log.FromContext(ctx)

	// Get the Cloudflare client
	cfClient, err := r.getCloudflareClient(ctx, tunnel)
	if err != nil {
		return fmt.Errorf("failed to create Cloudflare client: %w", err)
	}

	// Validate token using operational validation (works for both User and Account tokens)
	accountID, err := r.resolveAccountID(ctx, cfClient, tunnel)
	if err != nil {
		return fmt.Errorf("failed to resolve account: %w", err)
	}
	if err := cfClient.ValidateToken(ctx, accountID); err != nil {
		return fmt.Errorf("token validation failed: %w", err)
	}

	log.Info("Cloudflare credentials validated successfully")
	return nil
}

// ensureTunnel ensures the tunnel exists in Cloudflare.
// Creates the tunnel if it doesn't exist, adopts it if it does.
func (r *CloudflareTunnelReconciler) ensureTunnel(ctx context.Context, tunnel *cfgatev1alpha1.CloudflareTunnel) error {
	log := log.FromContext(ctx)

	cfClient, err := r.getCloudflareClient(ctx, tunnel)
	if err != nil {
		return fmt.Errorf("failed to create Cloudflare client: %w", err)
	}

	tunnelService := cloudflare.NewTunnelService(cfClient, log)
	accountID, err := r.resolveAccountID(ctx, cfClient, tunnel)
	if err != nil {
		return fmt.Errorf("failed to resolve account: %w", err)
	}

	cfTunnel, created, err := tunnelService.EnsureTunnel(ctx, accountID, tunnel.Spec.Tunnel.Name)
	if err != nil {
		return fmt.Errorf("failed to ensure tunnel: %w", err)
	}

	// Update status with tunnel info
	tunnel.Status.TunnelID = cfTunnel.ID
	tunnel.Status.TunnelName = cfTunnel.Name
	tunnel.Status.TunnelDomain = cloudflare.TunnelDomain(cfTunnel.ID)
	tunnel.Status.AccountID = accountID

	if created {
		log.Info("Created new tunnel", "tunnelID", cfTunnel.ID, "tunnelName", cfTunnel.Name)
		r.Recorder.Eventf(tunnel, nil, corev1.EventTypeNormal, "TunnelCreated", "Create", "Created tunnel %s (ID: %s)", cfTunnel.Name, cfTunnel.ID)
	} else {
		log.Info("Adopted existing tunnel", "tunnelID", cfTunnel.ID, "tunnelName", cfTunnel.Name)
		r.Recorder.Eventf(tunnel, nil, corev1.EventTypeNormal, "TunnelAdopted", "Adopt", "Adopted existing tunnel %s (ID: %s)", cfTunnel.Name, cfTunnel.ID)
	}

	return nil
}

// deployCloudflared ensures the cloudflared Deployment is running.
// Creates or updates the Deployment, ConfigMap, and token Secret.
func (r *CloudflareTunnelReconciler) deployCloudflared(ctx context.Context, tunnel *cfgatev1alpha1.CloudflareTunnel) (*originRuntimeState, error) {
	if tunnel.Status.TunnelID == "" {
		return nil, fmt.Errorf("tunnel ID not set in status")
	}
	if err := r.validateOriginCAPoolSecretRef(ctx, tunnel); err != nil {
		return nil, err
	}
	originState, err := r.resolveOriginRuntimeState(ctx, tunnel)
	if err != nil {
		return nil, err
	}
	if err := r.syncGeneratedOriginCASecrets(ctx, tunnel, originState.backendTLSPolicies); err != nil {
		return nil, err
	}
	if err := r.deployCloudflaredWithOriginRuntimeState(ctx, tunnel, originState); err != nil {
		return nil, err
	}
	return originState, nil
}

func (r *CloudflareTunnelReconciler) deployCloudflaredWithOriginRuntimeState(ctx context.Context, tunnel *cfgatev1alpha1.CloudflareTunnel, originState *originRuntimeState) error {
	log := log.FromContext(ctx)

	if originState == nil {
		return fmt.Errorf("origin runtime state not set")
	}

	// Get tunnel token
	cfClient, err := r.getCloudflareClient(ctx, tunnel)
	if err != nil {
		return fmt.Errorf("failed to create Cloudflare client: %w", err)
	}

	tunnelService := cloudflare.NewTunnelService(cfClient, log)
	accountID, err := r.resolveAccountID(ctx, cfClient, tunnel)
	if err != nil {
		return fmt.Errorf("failed to resolve account: %w", err)
	}

	token, err := tunnelService.GetToken(ctx, accountID, tunnel.Status.TunnelID)
	if err != nil {
		return fmt.Errorf("failed to get tunnel token: %w", err)
	}

	// Get or create builder
	builder := r.Builder
	if builder == nil {
		builder = cloudflared.NewBuilder()
	}

	// Create or update token Secret
	secret := builder.BuildTokenSecret(tunnel, token)
	if err := controllerutil.SetControllerReference(tunnel, secret, r.Scheme); err != nil {
		return fmt.Errorf("failed to set secret owner reference: %w", err)
	}

	existingSecret := &corev1.Secret{}
	err = r.Get(ctx, types.NamespacedName{Name: secret.Name, Namespace: secret.Namespace}, existingSecret)
	if err != nil {
		if apierrors.IsNotFound(err) {
			if err := r.Create(ctx, secret); err != nil {
				if !apierrors.IsAlreadyExists(err) {
					return fmt.Errorf("failed to create token secret: %w", err)
				}
				// Already exists from concurrent reconcile, continue
			} else {
				log.Info("Created token secret", "name", secret.Name)
			}
		} else {
			return fmt.Errorf("failed to get token secret: %w", err)
		}
	} else {
		existingSecret.Data = secret.Data
		existingSecret.StringData = secret.StringData
		if err := r.Update(ctx, existingSecret); err != nil {
			return fmt.Errorf("failed to update token secret: %w", err)
		}
	}

	// Create or update Deployment
	deployment := r.buildCloudflaredDeploymentWithOriginCAPoolMounts(tunnel, token, originState.originCAPoolMounts)
	if err := controllerutil.SetControllerReference(tunnel, deployment, r.Scheme); err != nil {
		return fmt.Errorf("failed to set deployment owner reference: %w", err)
	}

	existingDeployment := &appsv1.Deployment{}
	err = r.Get(ctx, types.NamespacedName{Name: deployment.Name, Namespace: deployment.Namespace}, existingDeployment)
	if err != nil {
		if apierrors.IsNotFound(err) {
			if err := r.Create(ctx, deployment); err != nil {
				if !apierrors.IsAlreadyExists(err) {
					return fmt.Errorf("failed to create deployment: %w", err)
				}
				// Already exists from concurrent reconcile, continue
			} else {
				log.Info("Created cloudflared deployment", "name", deployment.Name)
				r.Recorder.Eventf(tunnel, nil, corev1.EventTypeNormal, "DeploymentCreated", "Create", "Created cloudflared deployment %s", deployment.Name)
			}
		} else {
			return fmt.Errorf("failed to get deployment: %w", err)
		}
	} else {
		// Update deployment spec
		existingDeployment.Spec = deployment.Spec
		if err := r.Update(ctx, existingDeployment); err != nil {
			return fmt.Errorf("failed to update deployment: %w", err)
		}
		log.Info("Updated cloudflared deployment", "name", deployment.Name)
	}

	// Re-fetch deployment to get current status (after create/update)
	if err := r.Get(ctx, types.NamespacedName{Name: deployment.Name, Namespace: deployment.Namespace}, existingDeployment); err != nil {
		log.Error(err, "failed to get deployment status")
	} else {
		tunnel.Status.Replicas = existingDeployment.Status.Replicas
		tunnel.Status.ReadyReplicas = existingDeployment.Status.ReadyReplicas
	}

	return nil
}

func (r *CloudflareTunnelReconciler) ensureCloudflaredOriginCAPoolDeployment(ctx context.Context, tunnel *cfgatev1alpha1.CloudflareTunnel) (*originRuntimeState, error) {
	if tunnel.Status.TunnelID == "" {
		return nil, fmt.Errorf("tunnel ID not set in status")
	}
	if err := r.validateOriginCAPoolSecretRef(ctx, tunnel); err != nil {
		return nil, err
	}
	originState, err := r.resolveOriginRuntimeState(ctx, tunnel)
	if err != nil {
		return nil, err
	}
	if err := r.syncGeneratedOriginCASecrets(ctx, tunnel, originState.backendTLSPolicies); err != nil {
		return nil, err
	}

	desired := r.buildCloudflaredDeploymentWithOriginCAPoolMounts(tunnel, "", originState.originCAPoolMounts)
	existing := &appsv1.Deployment{}
	err = r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, existing)
	if err != nil {
		if apierrors.IsNotFound(err) {
			if err := r.deployCloudflaredWithOriginRuntimeState(ctx, tunnel, originState); err != nil {
				return nil, err
			}
			return originState, nil
		}
		return nil, fmt.Errorf("failed to get deployment: %w", err)
	}

	changed, ok := replaceManagedOriginCAPoolDeploymentMounts(existing, desired)
	if !ok {
		if err := r.deployCloudflaredWithOriginRuntimeState(ctx, tunnel, originState); err != nil {
			return nil, err
		}
		return originState, nil
	}
	if !changed {
		tunnel.Status.Replicas = existing.Status.Replicas
		tunnel.Status.ReadyReplicas = existing.Status.ReadyReplicas
		return originState, nil
	}
	if err := r.Update(ctx, existing); err != nil {
		return nil, fmt.Errorf("failed to update deployment origin CA mounts: %w", err)
	}
	tunnel.Status.Replicas = existing.Status.Replicas
	tunnel.Status.ReadyReplicas = existing.Status.ReadyReplicas
	return originState, nil
}

func (r *CloudflareTunnelReconciler) buildCloudflaredDeploymentWithOriginCAPoolMounts(tunnel *cfgatev1alpha1.CloudflareTunnel, token string, mounts []cloudflared.OriginCAPoolMount) *appsv1.Deployment {
	builder := r.Builder
	if builder == nil {
		builder = cloudflared.NewBuilder()
	}
	if capoolBuilder, ok := builder.(cloudflared.DeploymentOriginCAPoolBuilder); ok {
		return capoolBuilder.BuildDeploymentWithOriginCAPoolMounts(tunnel, token, mounts)
	}
	return builder.BuildDeployment(tunnel, token)
}

func replaceManagedOriginCAPoolDeploymentMounts(existing, desired *appsv1.Deployment) (changed bool, ok bool) {
	existingContainer := cloudflaredContainer(&existing.Spec.Template.Spec)
	desiredContainer := cloudflaredContainer(&desired.Spec.Template.Spec)
	if existingContainer == nil || desiredContainer == nil {
		return false, false
	}

	existingVolumes := existing.Spec.Template.Spec.Volumes
	existingMounts := existingContainer.VolumeMounts
	nextVolumes := replaceManagedOriginCAPoolVolumes(existingVolumes, desired.Spec.Template.Spec.Volumes)
	nextMounts := replaceManagedOriginCAPoolMounts(existingMounts, desiredContainer.VolumeMounts)

	volumesChanged := !apiequality.Semantic.DeepEqual(existingVolumes, nextVolumes)
	mountsChanged := !apiequality.Semantic.DeepEqual(existingMounts, nextMounts)
	if !volumesChanged && !mountsChanged {
		return false, true
	}

	existing.Spec.Template.Spec.Volumes = nextVolumes
	existingContainer.VolumeMounts = nextMounts
	return true, true
}

func cloudflaredContainer(podSpec *corev1.PodSpec) *corev1.Container {
	for i := range podSpec.Containers {
		if podSpec.Containers[i].Name == "cloudflared" {
			return &podSpec.Containers[i]
		}
	}
	return nil
}

func replaceManagedOriginCAPoolVolumes(existing, desired []corev1.Volume) []corev1.Volume {
	next := make([]corev1.Volume, 0, len(existing)+len(desired))
	for _, volume := range existing {
		if !isManagedOriginCAPoolVolume(volume) {
			next = append(next, volume)
		}
	}
	managed := managedOriginCAPoolVolumes(desired)
	sortOriginCAPoolVolumes(managed)
	next = append(next, managed...)
	return next
}

func replaceManagedOriginCAPoolMounts(existing, desired []corev1.VolumeMount) []corev1.VolumeMount {
	next := make([]corev1.VolumeMount, 0, len(existing)+len(desired))
	for _, mount := range existing {
		if !isManagedOriginCAPoolMount(mount) {
			next = append(next, mount)
		}
	}
	managed := managedOriginCAPoolMounts(desired)
	sortOriginCAPoolMounts(managed)
	next = append(next, managed...)
	return next
}

func managedOriginCAPoolVolumes(volumes []corev1.Volume) []corev1.Volume {
	managed := make([]corev1.Volume, 0, len(volumes))
	for _, volume := range volumes {
		if isManagedOriginCAPoolVolume(volume) {
			managed = append(managed, volume)
		}
	}
	return managed
}

func managedOriginCAPoolMounts(mounts []corev1.VolumeMount) []corev1.VolumeMount {
	managed := make([]corev1.VolumeMount, 0, len(mounts))
	for _, mount := range mounts {
		if isManagedOriginCAPoolMount(mount) {
			managed = append(managed, mount)
		}
	}
	return managed
}

func isManagedOriginCAPoolVolume(volume corev1.Volume) bool {
	return isManagedOriginCAPoolVolumeName(volume.Name)
}

func isManagedOriginCAPoolMount(mount corev1.VolumeMount) bool {
	return isManagedOriginCAPoolVolumeName(mount.Name) ||
		mount.MountPath == cloudflared.OriginCAPoolMountPath ||
		strings.HasPrefix(mount.MountPath, cloudflared.NamedOriginCAPoolMountBase+"/") ||
		strings.HasPrefix(mount.MountPath, cloudflared.BackendTLSCAPoolMountBase+"/")
}

func isManagedOriginCAPoolVolumeName(name string) bool {
	return name == cloudflared.OriginCAPoolVolumeName ||
		strings.HasPrefix(name, "origin-ca-pool-") ||
		strings.HasPrefix(name, "origin-ca-backendtls-")
}

func sortOriginCAPoolVolumes(volumes []corev1.Volume) {
	sort.Slice(volumes, func(i, j int) bool { return volumes[i].Name < volumes[j].Name })
}

func sortOriginCAPoolMounts(mounts []corev1.VolumeMount) {
	sort.Slice(mounts, func(i, j int) bool {
		if mounts[i].Name != mounts[j].Name {
			return mounts[i].Name < mounts[j].Name
		}
		return mounts[i].MountPath < mounts[j].MountPath
	})
}

func (r *CloudflareTunnelReconciler) validateOriginCAPoolSecretRef(ctx context.Context, tunnel *cfgatev1alpha1.CloudflareTunnel) error {
	ref := tunnel.Spec.OriginDefaults.CAPoolSecretRef
	if ref == nil {
		return nil
	}

	var secret corev1.Secret
	key := ref.Key
	if key == "" {
		key = cloudflared.DefaultOriginCAPoolSecretKey
	}
	namespacedName := types.NamespacedName{Name: ref.Name, Namespace: tunnel.Namespace}
	if err := r.Get(ctx, namespacedName, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("origin CA pool Secret %s/%s not found", tunnel.Namespace, ref.Name)
		}
		return fmt.Errorf("failed to get origin CA pool Secret %s/%s: %w", tunnel.Namespace, ref.Name, err)
	}
	if _, ok := secret.Data[key]; !ok {
		return fmt.Errorf("origin CA pool Secret %s/%s missing key %q", tunnel.Namespace, ref.Name, key)
	}
	return nil
}

// syncConfiguration syncs the tunnel configuration to Cloudflare.
// Collects routes from Gateway/HTTPRoute resources and pushes to Cloudflare API.
func (r *CloudflareTunnelReconciler) syncConfiguration(ctx context.Context, tunnel *cfgatev1alpha1.CloudflareTunnel) error {
	originState, err := r.resolveOriginRuntimeState(ctx, tunnel)
	if err != nil {
		return err
	}
	if err := r.syncGeneratedOriginCASecrets(ctx, tunnel, originState.backendTLSPolicies); err != nil {
		return err
	}
	return r.syncConfigurationWithRuntime(ctx, tunnel, originState.runtime)
}

func (r *CloudflareTunnelReconciler) syncConfigurationWithRuntime(ctx context.Context, tunnel *cfgatev1alpha1.CloudflareTunnel, runtime *originRuntime) error {
	log := log.FromContext(ctx)

	if tunnel.Status.TunnelID == "" {
		return fmt.Errorf("tunnel ID not set in status")
	}

	// Collect ingress rules from HTTPRoutes
	rules, routeCount, err := r.collectIngressRulesWithRuntime(ctx, tunnel, runtime)
	if err != nil {
		return fmt.Errorf("failed to collect ingress rules: %w", err)
	}

	// Build configuration with defaults
	var defaults *cloudflare.OriginRequestConfig
	if tunnel.Spec.OriginDefaults.ConnectTimeout != "" ||
		tunnel.Spec.OriginDefaults.NoTLSVerify ||
		tunnel.Spec.OriginDefaults.HTTP2Origin ||
		tunnel.Spec.OriginDefaults.H2cOrigin ||
		tunnel.Spec.OriginDefaults.CAPoolSecretRef != nil {
		defaults = &cloudflare.OriginRequestConfig{
			ConnectTimeout: tunnel.Spec.OriginDefaults.ConnectTimeout,
			NoTLSVerify:    tunnel.Spec.OriginDefaults.NoTLSVerify,
			HTTP2Origin:    tunnel.Spec.OriginDefaults.HTTP2Origin,
			H2cOrigin:      tunnel.Spec.OriginDefaults.H2cOrigin,
		}
		if tunnel.Spec.OriginDefaults.CAPoolSecretRef != nil {
			defaults.CAPool = cloudflared.OriginCAPoolPath()
		}
	}

	config := cloudflare.BuildConfiguration(rules, defaults)

	// Update fallback target
	if len(config.Ingress) > 0 {
		lastIdx := len(config.Ingress) - 1
		if config.Ingress[lastIdx].Hostname == "" && config.Ingress[lastIdx].Path == "" {
			fallback := tunnel.Spec.FallbackTarget
			if fallback == "" {
				fallback = "http_status:404"
			}
			config.Ingress[lastIdx].Service = fallback
		}
	}
	if err := validateTunnelFallbackOriginRequest(tunnel, config); err != nil {
		return err
	}

	// Sync to Cloudflare
	cfClient, err := r.getCloudflareClient(ctx, tunnel)
	if err != nil {
		return fmt.Errorf("failed to create Cloudflare client: %w", err)
	}

	tunnelService := cloudflare.NewTunnelService(cfClient, log)
	accountID, err := r.resolveAccountID(ctx, cfClient, tunnel)
	if err != nil {
		return fmt.Errorf("failed to resolve account: %w", err)
	}

	desiredHash := tunnelConfigHash(config)
	currentHash := tunnel.Annotations[configHashAnnotation]
	if desiredHash == currentHash {
		log.V(1).Info("tunnel configuration unchanged, skipping update",
			"tunnelID", tunnel.Status.TunnelID)
		tunnel.Status.ConnectedRouteCount = int32(routeCount)
		return nil
	}

	if err := tunnelService.UpdateConfiguration(ctx, accountID, tunnel.Status.TunnelID, config); err != nil {
		errStr := err.Error()
		if strings.Contains(errStr, "404") || strings.Contains(errStr, "not found") || strings.Contains(errStr, "Tunnel not found") {
			log.Info("Tunnel not found on Cloudflare, clearing tunnelID to force re-adoption", "tunnelID", tunnel.Status.TunnelID)
			tunnel.Status.TunnelID = ""
			tunnel.Status.TunnelName = ""
			tunnel.Status.TunnelDomain = ""
			if statusErr := r.Status().Update(ctx, tunnel); statusErr != nil {
				log.Error(statusErr, "failed to clear stale tunnelID from status")
			}
		}
		return fmt.Errorf("failed to update tunnel configuration: %w", err)
	}

	annotationTunnel := tunnel.DeepCopy()
	patch := client.MergeFrom(annotationTunnel.DeepCopy())
	if annotationTunnel.Annotations == nil {
		annotationTunnel.Annotations = make(map[string]string)
	}
	annotationTunnel.Annotations[configHashAnnotation] = desiredHash
	if err := r.Patch(ctx, annotationTunnel, patch); err != nil {
		log.Error(err, "failed to store config hash annotation")
	} else {
		tunnel.Annotations = annotationTunnel.Annotations
		tunnel.ResourceVersion = annotationTunnel.ResourceVersion
	}

	tunnel.Status.ConnectedRouteCount = int32(routeCount)
	log.Info("Synced tunnel configuration", "rules", len(config.Ingress), "routes", routeCount)

	return nil
}

// collectIngressRules collects ingress rules from HTTPRoutes that reference this tunnel.
//
//nolint:unused // retained as read-only wrapper for callers that do not need generated Secret sync.
func (r *CloudflareTunnelReconciler) collectIngressRules(ctx context.Context, tunnel *cfgatev1alpha1.CloudflareTunnel) ([]cloudflare.IngressRule, int, error) {
	originRuntime, err := r.buildOriginRuntime(ctx, tunnel)
	if err != nil {
		return nil, 0, err
	}
	return r.collectIngressRulesWithRuntime(ctx, tunnel, originRuntime)
}

func (r *CloudflareTunnelReconciler) collectIngressRulesWithRuntime(ctx context.Context, tunnel *cfgatev1alpha1.CloudflareTunnel, runtime *originRuntime) ([]cloudflare.IngressRule, int, error) {
	var rules []cloudflare.IngressRule
	routeCount := 0
	if runtime == nil {
		runtime = &originRuntime{
			namedCAPoolPaths:      map[string]string{},
			backendTLSCAPoolPaths: map[types.NamespacedName]string{},
		}
	}
	runtime.originDefaults = &tunnel.Spec.OriginDefaults

	// Find Gateways that reference this tunnel
	var gateways gateway.GatewayList
	if err := r.List(ctx, &gateways); err != nil {
		return nil, 0, fmt.Errorf("failed to list gateways: %w", err)
	}

	relevantGateways := map[types.NamespacedName]gateway.Gateway{}

	for _, gw := range gateways.Items {
		managed, err := gatewayClassManagedByCfgate(ctx, r.Client, &gw)
		if err != nil || !managed {
			continue
		}
		ref := annotations.GetAnnotation(&gw, annotations.AnnotationTunnelRef)
		if ref == "" {
			continue
		}
		ns, name, err := annotations.ParseNamespacedName(ref, gw.Namespace)
		if err != nil {
			continue
		}
		if name == tunnel.Name && ns == tunnel.Namespace {
			relevantGateways[types.NamespacedName{Namespace: gw.Namespace, Name: gw.Name}] = gw
		}
	}

	// Fetch HTTPRoutes once, then match against each relevant gateway.
	var routes gateway.HTTPRouteList
	if err := r.List(ctx, &routes); err != nil {
		return nil, 0, fmt.Errorf("failed to list httproutes: %w", err)
	}
	candidateRoutes := candidateHTTPRoutesForGateways(routes.Items, relevantGateways)
	lookups, err := r.loadOriginRulePolicyLookups(ctx, candidateRoutes)
	if err != nil {
		return nil, 0, err
	}

	for _, route := range candidateRoutes {
		for _, parentRef := range route.Spec.ParentRefs {
			parentNS := route.Namespace
			if parentRef.Namespace != nil {
				parentNS = string(*parentRef.Namespace)
			}
			_, ok := relevantGateways[types.NamespacedName{Namespace: parentNS, Name: string(parentRef.Name)}]
			if !ok {
				continue
			}

			eval, err := evaluateHTTPRouteParentRef(ctx, r.Client, &route, parentRef, httpRouteParentEvaluationOptions{})
			if err != nil {
				return nil, 0, err
			}
			if !eval.Accepted {
				r.Recorder.Eventf(tunnel, nil, corev1.EventTypeWarning, "HTTPRouteError", "CollectRules",
					"skipping HTTPRoute %s/%s: %s", route.Namespace, route.Name, eval.Message)
				continue
			}
			hostnames := acceptedHTTPRouteHostnames(&route, eval.AcceptedListeners)
			if len(hostnames) == 0 {
				r.Recorder.Eventf(tunnel, nil, corev1.EventTypeWarning, "HTTPRouteError", "CollectRules",
					"skipping HTTPRoute %s/%s: no route hostnames and no matching listener hostname", route.Namespace, route.Name)
				continue
			}

			backendCondition := validateHTTPRouteBackendRefs(ctx, r.Client, &route)
			if backendCondition.Status == metav1.ConditionFalse {
				r.Recorder.Eventf(tunnel, nil, corev1.EventTypeWarning, "HTTPRouteError", "CollectRules",
					"skipping HTTPRoute %s/%s: %s", route.Namespace, route.Name, backendCondition.Message)
				continue
			}

			routeRules, err := r.buildRulesFromHTTPRouteForHostnamesWithRuntimeAndLookups(ctx, &route, hostnames, tunnel.Spec.OriginDefaults.CAPoolSecretRef != nil, runtime, lookups)
			if err != nil {
				r.Recorder.Eventf(tunnel, nil, corev1.EventTypeWarning, "HTTPRouteError", "CollectRules",
					"skipping HTTPRoute %s/%s: %s", route.Namespace, route.Name, err.Error())
				continue
			}
			rules = append(rules, routeRules...)
			routeCount += len(routeRules)
		}
	}

	// Sort rules by specificity for Cloudflare's first-match-wins evaluation.
	// Within each hostname: path-bearing rules before path-less, longer paths first.
	sort.SliceStable(rules, func(i, j int) bool {
		if rules[i].Hostname != rules[j].Hostname {
			return false
		}
		iHasPath := rules[i].Path != ""
		jHasPath := rules[j].Path != ""
		if iHasPath != jHasPath {
			return iHasPath
		}
		return len(rules[i].Path) > len(rules[j].Path)
	})

	return rules, routeCount, nil
}

func candidateHTTPRoutesForGateways(routes []gateway.HTTPRoute, gateways map[types.NamespacedName]gateway.Gateway) []gateway.HTTPRoute {
	candidates := make([]gateway.HTTPRoute, 0, len(routes))
	for _, route := range routes {
		for _, parentRef := range route.Spec.ParentRefs {
			parentNS := route.Namespace
			if parentRef.Namespace != nil {
				parentNS = string(*parentRef.Namespace)
			}
			if _, ok := gateways[types.NamespacedName{Namespace: parentNS, Name: string(parentRef.Name)}]; ok {
				candidates = append(candidates, route)
				break
			}
		}
	}
	return candidates
}

// buildRulesFromHTTPRoute builds ingress rules from an HTTPRoute by iterating
// all hostnames and all matches per rule. Returns an error if a rule has
// multiple backendRefs (Cloudflare tunnel ingress is 1:1) or an unsupported
// backend group/kind. Rules with zero backendRefs are skipped.
func (r *CloudflareTunnelReconciler) buildRulesFromHTTPRoute(route *gateway.HTTPRoute) ([]cloudflare.IngressRule, error) {
	return r.buildRulesFromHTTPRouteForHostnames(route, effectiveHTTPRouteHostnames(route), false)
}

func (r *CloudflareTunnelReconciler) buildRulesFromHTTPRouteForHostnames(route *gateway.HTTPRoute, hostnames []gateway.Hostname, originCAPoolMounted bool) ([]cloudflare.IngressRule, error) {
	return r.buildRulesFromHTTPRouteForHostnamesWithRuntime(context.Background(), route, hostnames, originCAPoolMounted, &originRuntime{
		namedCAPoolPaths:      map[string]string{},
		backendTLSCAPoolPaths: map[types.NamespacedName]string{},
	})
}

func (r *CloudflareTunnelReconciler) buildRulesFromHTTPRouteForHostnamesWithRuntime(ctx context.Context, route *gateway.HTTPRoute, hostnames []gateway.Hostname, originCAPoolMounted bool, runtime *originRuntime) ([]cloudflare.IngressRule, error) {
	lookups, err := r.loadOriginRulePolicyLookups(ctx, []gateway.HTTPRoute{*route})
	if err != nil {
		return nil, err
	}
	return r.buildRulesFromHTTPRouteForHostnamesWithRuntimeAndLookups(ctx, route, hostnames, originCAPoolMounted, runtime, lookups)
}

func (r *CloudflareTunnelReconciler) buildRulesFromHTTPRouteForHostnamesWithRuntimeAndLookups(ctx context.Context, route *gateway.HTTPRoute, hostnames []gateway.Hostname, originCAPoolMounted bool, runtime *originRuntime, lookups *originRulePolicyLookups) ([]cloudflare.IngressRule, error) {
	var rules []cloudflare.IngressRule
	if runtime == nil {
		runtime = &originRuntime{
			namedCAPoolPaths:      map[string]string{},
			backendTLSCAPoolPaths: map[types.NamespacedName]string{},
		}
	}
	if lookups == nil {
		lookups = emptyOriginRulePolicyLookups()
	}
	if err := validateRouteOriginCAPool(route, originCAPoolMounted, runtime.namedCAPoolPaths); err != nil {
		return nil, err
	}

	for _, hostname := range hostnames {
		for _, rule := range route.Spec.Rules {
			if len(rule.BackendRefs) == 0 {
				continue
			}
			if len(rule.BackendRefs) > 1 {
				return nil, fmt.Errorf("route %s/%s: multiple backendRefs not supported for tunnel ingress rules",
					route.Namespace, route.Name)
			}

			backend := rule.BackendRefs[0]

			if backend.Group != nil && *backend.Group != "" && *backend.Group != "core" {
				return nil, fmt.Errorf("route %s/%s: unsupported backend group %q",
					route.Namespace, route.Name, *backend.Group)
			}
			if backend.Kind != nil && *backend.Kind != "" && *backend.Kind != "Service" {
				return nil, fmt.Errorf("route %s/%s: unsupported backend kind %q",
					route.Namespace, route.Name, *backend.Kind)
			}

			servicePort := int32(80)
			if backend.Port != nil {
				servicePort = int32(*backend.Port)
			}

			backendNS := route.Namespace
			if backend.Namespace != nil && *backend.Namespace != "" {
				backendNS = string(*backend.Namespace)
			}

			protocol := "http"
			originRequest := &cloudflare.OriginRequestConfig{}
			effectiveOriginRequest := cloudflareOriginRequestFromTunnelDefaults(runtime.originDefaults)
			if effectiveOriginRequest == nil {
				effectiveOriginRequest = &cloudflare.OriginRequestConfig{}
			}
			originPolicy, err := r.originPolicyForRuleFromLookups(ctx, lookups, route, rule)
			if err != nil {
				return nil, err
			}
			if policyProtocol, err := applyOriginPolicy(originRequest, originPolicy, runtime.namedCAPoolPaths); err != nil {
				return nil, err
			} else if policyProtocol != "" {
				protocol = policyProtocol
			}
			if _, err := applyOriginPolicy(effectiveOriginRequest, originPolicy, runtime.namedCAPoolPaths); err != nil {
				return nil, err
			}

			backendTLSPolicy := backendTLSPolicyForBackendFromLookups(lookups, backendNS, string(backend.Name))
			backendTLSForcesHTTPS := false
			if usesHTTPS, err := applyBackendTLSPolicy(originRequest, backendTLSPolicy, runtime.backendTLSCAPoolPaths); err != nil {
				return nil, err
			} else if usesHTTPS {
				backendTLSForcesHTTPS = true
				protocol = "https"
			}
			if usesHTTPS, err := applyBackendTLSPolicy(effectiveOriginRequest, backendTLSPolicy, runtime.backendTLSCAPoolPaths); err != nil {
				return nil, err
			} else if usesHTTPS {
				backendTLSForcesHTTPS = true
			}

			annotationProtocol := annotations.GetAnnotation(route, annotations.AnnotationOriginProtocol)
			if backendTLSForcesHTTPS && annotationProtocol == "http" && backendTLSPolicy != nil {
				return nil, fmt.Errorf("route %s/%s: %s=%s cannot override BackendTLSPolicy %s/%s; BackendTLSPolicy requires HTTPS origin service",
					route.Namespace, route.Name, annotations.AnnotationOriginProtocol, annotationProtocol, backendTLSPolicy.Namespace, backendTLSPolicy.Name)
			}
			if annotationProtocol == "https" || annotationProtocol == "http" {
				protocol = annotationProtocol
			}

			service := fmt.Sprintf("%s://%s.%s.svc.cluster.local:%d",
				protocol, backend.Name, backendNS, servicePort)

			annotationOriginRequest := cloudflareOriginRequestFromRouteAnnotations(route)
			if capoolRef := annotations.GetAnnotation(route, annotations.AnnotationOriginCAPoolRef); capoolRef != "" {
				path, ok := runtime.namedCAPoolPaths[capoolRef]
				if !ok {
					return nil, fmt.Errorf("route %s/%s: %s references unknown origin CA pool %q",
						route.Namespace, route.Name, annotations.AnnotationOriginCAPoolRef, capoolRef)
				}
				if annotationOriginRequest == nil {
					annotationOriginRequest = &cloudflare.OriginRequestConfig{}
				}
				annotationOriginRequest.CAPool = path
			}
			originRequest = mergeOriginRequest(originRequest, annotationOriginRequest)
			effectiveOriginRequest = mergeOriginRequest(effectiveOriginRequest, annotationOriginRequest)
			if err := validateRouteOriginRequest(route, effectiveOriginRequest, service, backendTLSForcesHTTPS); err != nil {
				return nil, err
			}

			if len(rule.Matches) == 0 {
				rules = append(rules, cloudflare.IngressRule{
					Hostname:      string(hostname),
					Service:       service,
					OriginRequest: originRequest,
				})
				continue
			}

			for _, match := range rule.Matches {
				path, err := cloudflaredPathRegex(match)
				if err != nil {
					return nil, fmt.Errorf("route %s/%s: %w", route.Namespace, route.Name, err)
				}
				rules = append(rules, cloudflare.IngressRule{
					Hostname:      string(hostname),
					Path:          path,
					Service:       service,
					OriginRequest: originRequest,
				})
			}
		}
	}

	return rules, nil
}

func validateRouteOriginCAPool(route *gateway.HTTPRoute, originCAPoolMounted bool, namedCAPoolPaths map[string]string) error {
	caPoolRef := annotations.GetAnnotation(route, annotations.AnnotationOriginCAPoolRef)
	caPool := annotations.GetAnnotation(route, annotations.AnnotationOriginCAPool)
	if caPoolRef != "" && caPool != "" {
		return fmt.Errorf("route %s/%s: %s and %s are mutually exclusive",
			route.Namespace, route.Name, annotations.AnnotationOriginCAPool, annotations.AnnotationOriginCAPoolRef)
	}
	if caPoolRef != "" {
		if _, ok := namedCAPoolPaths[caPoolRef]; !ok {
			return fmt.Errorf("route %s/%s: %s references unknown origin CA pool %q",
				route.Namespace, route.Name, annotations.AnnotationOriginCAPoolRef, caPoolRef)
		}
	}
	if caPool == "" {
		return nil
	}
	mode := annotations.GetAnnotation(route, annotations.AnnotationOriginCAPoolMode)
	if mode == "" {
		mode = "managed"
	}
	if mode == "unmanaged" {
		if !strings.HasPrefix(caPool, "/") {
			return fmt.Errorf("route %s/%s: unmanaged %s must be an absolute path",
				route.Namespace, route.Name, annotations.AnnotationOriginCAPool)
		}
		return nil
	}
	if caPool == cloudflared.OriginCAPoolPath() {
		if !originCAPoolMounted {
			return fmt.Errorf("route %s/%s: %s requires CloudflareTunnel spec.originDefaults.caPoolSecretRef",
				route.Namespace, route.Name, annotations.AnnotationOriginCAPool)
		}
		return nil
	}
	for _, namedPath := range namedCAPoolPaths {
		if caPool == namedPath {
			return nil
		}
	}
	if len(namedCAPoolPaths) > 0 {
		valid := []string{cloudflared.OriginCAPoolPath()}
		named := make([]string, 0, len(namedCAPoolPaths))
		for _, path := range namedCAPoolPaths {
			named = append(named, path)
		}
		sort.Strings(named)
		valid = append(valid, named...)
		return fmt.Errorf("route %s/%s: %s must be one of %v for managed origin CA pools",
			route.Namespace, route.Name, annotations.AnnotationOriginCAPool, valid)
	}
	if originCAPoolMounted {
		return fmt.Errorf("route %s/%s: %s must be %q for managed origin CA pools",
			route.Namespace, route.Name, annotations.AnnotationOriginCAPool, cloudflared.OriginCAPoolPath())
	}
	return fmt.Errorf("route %s/%s: %s must be a managed origin CA pool path",
		route.Namespace, route.Name, annotations.AnnotationOriginCAPool)
}

func cloudflareOriginRequestFromTunnelDefaults(defaults *cfgatev1alpha1.OriginDefaults) *cloudflare.OriginRequestConfig {
	if defaults == nil {
		return nil
	}
	if defaults.ConnectTimeout == "" &&
		!defaults.NoTLSVerify &&
		!defaults.HTTP2Origin &&
		!defaults.H2cOrigin &&
		defaults.CAPoolSecretRef == nil {
		return nil
	}
	config := &cloudflare.OriginRequestConfig{
		ConnectTimeout: defaults.ConnectTimeout,
		NoTLSVerify:    defaults.NoTLSVerify,
		HTTP2Origin:    defaults.HTTP2Origin,
		H2cOrigin:      defaults.H2cOrigin,
	}
	if defaults.CAPoolSecretRef != nil {
		config.CAPool = cloudflared.OriginCAPoolPath()
	}
	return config
}

func cloudflareOriginRequestFromRouteAnnotations(route *gateway.HTTPRoute) *cloudflare.OriginRequestConfig {
	config := &cloudflare.OriginRequestConfig{}
	if value := annotations.GetAnnotation(route, annotations.AnnotationOriginConnectTimeout); value != "" {
		config.ConnectTimeout = value
	}
	if value := annotations.GetAnnotation(route, annotations.AnnotationOriginHTTPHostHeader); value != "" {
		config.HTTPHostHeader = value
	}
	if value := annotations.GetAnnotation(route, annotations.AnnotationOriginServerName); value != "" {
		config.OriginServerName = value
	}
	if value := annotations.GetAnnotation(route, annotations.AnnotationOriginCAPool); value != "" {
		config.CAPool = value
	}
	if value, present := annotations.GetAnnotationBoolValue(route, annotations.AnnotationOriginSSLVerify); present {
		config.NoTLSVerify = !value
		config.NoTLSVerifySet = true
	}
	if value, present := annotations.GetAnnotationBoolValue(route, annotations.AnnotationOriginHTTP2); present {
		config.HTTP2Origin = value
		config.HTTP2OriginSet = true
	}
	if value, present := annotations.GetAnnotationBoolValue(route, annotations.AnnotationOriginH2c); present {
		config.H2cOrigin = value
		config.H2cOriginSet = true
	}
	if originRequestEmpty(config) {
		return nil
	}
	return config
}

func validateRouteOriginRequest(route *gateway.HTTPRoute, config *cloudflare.OriginRequestConfig, service string, backendTLSForcesHTTPS bool) error {
	if config != nil && config.HTTP2Origin && config.H2cOrigin {
		return fmt.Errorf("route %s/%s: http2Origin and h2cOrigin are mutually exclusive", route.Namespace, route.Name)
	}
	if config != nil && config.H2cOrigin {
		switch {
		case config.HTTP2Origin:
			return fmt.Errorf("route %s/%s: h2cOrigin requires cleartext HTTP and cannot be combined with http2Origin", route.Namespace, route.Name)
		case backendTLSForcesHTTPS:
			return fmt.Errorf("route %s/%s: h2cOrigin requires cleartext HTTP origin service and is incompatible with BackendTLSPolicy", route.Namespace, route.Name)
		case strings.HasPrefix(service, "https://") || strings.HasPrefix(service, "wss://"):
			return fmt.Errorf("route %s/%s: h2cOrigin requires cleartext HTTP origin service", route.Namespace, route.Name)
		}
	}
	return nil
}

func validateTunnelFallbackOriginRequest(tunnel *cfgatev1alpha1.CloudflareTunnel, config cloudflare.TunnelConfiguration) error {
	if tunnel == nil || !tunnel.Spec.OriginDefaults.H2cOrigin {
		return nil
	}
	if len(config.Ingress) == 0 {
		return nil
	}
	fallback := config.Ingress[len(config.Ingress)-1]
	if fallback.Hostname != "" || fallback.Path != "" {
		return nil
	}
	if strings.HasPrefix(fallback.Service, "https://") || strings.HasPrefix(fallback.Service, "wss://") {
		return fmt.Errorf("CloudflareTunnel %s/%s: spec.originDefaults.h2cOrigin requires cleartext HTTP fallbackTarget, got %q", tunnel.Namespace, tunnel.Name, fallback.Service)
	}
	return nil
}

func effectiveHTTPRouteHostnames(route *gateway.HTTPRoute) []gateway.Hostname {
	if host := annotations.GetAnnotation(route, annotations.AnnotationHostname); host != "" {
		return []gateway.Hostname{gateway.Hostname(host)}
	}
	return append([]gateway.Hostname(nil), route.Spec.Hostnames...)
}

func routeHostnamesForGateway(route *gateway.HTTPRoute, gw *gateway.Gateway, parentRef gateway.ParentReference) []gateway.Hostname {
	hostnames := effectiveHTTPRouteHostnames(route)
	if len(hostnames) > 0 {
		return hostnames
	}

	seen := map[string]struct{}{}
	for _, listener := range gw.Spec.Listeners {
		if parentRef.SectionName != nil && listener.Name != *parentRef.SectionName {
			continue
		}
		if listener.Protocol != gateway.HTTPProtocolType && listener.Protocol != gateway.HTTPSProtocolType {
			continue
		}
		if !listenerAllowsHTTPRouteKind(listener) {
			continue
		}
		if listener.Hostname == nil || *listener.Hostname == "" {
			continue
		}
		hostname := string(*listener.Hostname)
		if _, ok := seen[hostname]; ok {
			continue
		}
		seen[hostname] = struct{}{}
		hostnames = append(hostnames, *listener.Hostname)
	}
	return hostnames
}

func cloudflaredPathRegex(match gateway.HTTPRouteMatch) (string, error) {
	if match.Path == nil || match.Path.Value == nil || *match.Path.Value == "" {
		return "", nil
	}

	path := *match.Path.Value
	matchType := gateway.PathMatchPathPrefix
	if match.Path.Type != nil {
		matchType = *match.Path.Type
	}

	switch matchType {
	case gateway.PathMatchPathPrefix:
		if path == "/" {
			return "^/.*$", nil
		}
		quoted := regexp.QuoteMeta(path)
		if strings.HasSuffix(path, "/") {
			return fmt.Sprintf("^%s.*$", quoted), nil
		}
		return fmt.Sprintf("^%s(?:/.*)?$", quoted), nil
	case gateway.PathMatchExact:
		return fmt.Sprintf("^%s$", regexp.QuoteMeta(path)), nil
	case gateway.PathMatchRegularExpression:
		if _, err := regexp.Compile(path); err != nil {
			return "", fmt.Errorf("unsupported path regular expression %q: %w", path, err)
		}
		return path, nil
	default:
		return "", fmt.Errorf("unsupported path match type %q", matchType)
	}
}

// reconcileDelete handles CloudflareTunnel deletion by deleting tunnel connections
// and the tunnel itself before removing the finalizer. Cleanup failure blocks
// finalizer removal and requeues. Set cfgate.io/deletion-policy=orphan to skip
// cleanup and allow deletion to proceed.
func (r *CloudflareTunnelReconciler) reconcileDelete(ctx context.Context, tunnel *cfgatev1alpha1.CloudflareTunnel) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	log.Info("handling tunnel deletion", "name", tunnel.Name)

	if !controllerutil.ContainsFinalizer(tunnel, tunnelFinalizer) {
		return ctrl.Result{}, nil
	}

	// Note: DNS cleanup is handled by CloudflareDNS CRD reconciler

	// Check deletion policy
	if tunnel.Annotations["cfgate.io/deletion-policy"] == "orphan" {
		log.Info("Orphaning tunnel due to deletion policy", "tunnelID", tunnel.Status.TunnelID)
		r.Recorder.Eventf(tunnel, nil, corev1.EventTypeNormal, "TunnelOrphaned", "Delete", "Tunnel %s orphaned due to deletion policy", tunnel.Status.TunnelID)
		return r.removeTunnelFinalizer(ctx, tunnel)
	}

	if tunnel.Status.TunnelID != "" {
		cfClient, err := r.getCloudflareClientForDeletion(ctx, tunnel)
		if err != nil {
			log.Error(err, "failed to create Cloudflare client for deletion")
			retryElapsed := time.Since(tunnel.DeletionTimestamp.Time)
			if retryElapsed < deletionRetryBudget {
				r.Recorder.Eventf(tunnel, nil, corev1.EventTypeWarning, "CleanupFailed", "Delete",
					"Failed to resolve credentials for tunnel %s: %v. Set annotation cfgate.io/deletion-policy=orphan to skip cleanup and remove finalizer.",
					tunnel.Status.TunnelID, err)
			} else {
				r.Recorder.Eventf(tunnel, nil, corev1.EventTypeWarning, "CleanupBlocked", "Delete",
					"Tunnel %s credential resolution blocked after %s of retries: %v. Set annotation cfgate.io/deletion-policy=orphan to skip cleanup and remove finalizer.",
					tunnel.Status.TunnelID, retryElapsed.Round(time.Second), err)
			}
			return ctrl.Result{RequeueAfter: deletionRequeueInterval}, nil
		}

		tunnelService := cloudflare.NewTunnelService(cfClient, log)
		accountID, err := r.resolveAccountID(ctx, cfClient, tunnel)
		if err != nil {
			log.Error(err, "failed to resolve account for deletion, using cached accountID")
			accountID = tunnel.Status.AccountID
		}

		if accountID == "" {
			log.Info("no account ID available for deletion")
			retryElapsed := time.Since(tunnel.DeletionTimestamp.Time)
			if retryElapsed < deletionRetryBudget {
				r.Recorder.Eventf(tunnel, nil, corev1.EventTypeWarning, "CleanupFailed", "Delete",
					"No account ID available for tunnel %s. Set annotation cfgate.io/deletion-policy=orphan to skip cleanup and remove finalizer.",
					tunnel.Status.TunnelID)
			} else {
				r.Recorder.Eventf(tunnel, nil, corev1.EventTypeWarning, "CleanupBlocked", "Delete",
					"Tunnel %s account resolution blocked after %s of retries. Set annotation cfgate.io/deletion-policy=orphan to skip cleanup and remove finalizer.",
					tunnel.Status.TunnelID, retryElapsed.Round(time.Second))
			}
			return ctrl.Result{RequeueAfter: deletionRequeueInterval}, nil
		}

		if err := tunnelService.Delete(ctx, accountID, tunnel.Status.TunnelID); err != nil {
			retryElapsed := time.Since(tunnel.DeletionTimestamp.Time)
			if retryElapsed < deletionRetryBudget {
				log.Error(err, "failed to delete tunnel from Cloudflare, will retry",
					"retryElapsed", retryElapsed.Round(time.Second),
					"retryBudget", deletionRetryBudget)
				r.Recorder.Eventf(tunnel, nil, corev1.EventTypeWarning, "CleanupFailed", "Delete",
					"Failed to delete tunnel %s: %v. Set annotation cfgate.io/deletion-policy=orphan to skip cleanup and remove finalizer.",
					tunnel.Status.TunnelID, err)
				return ctrl.Result{RequeueAfter: deletionRequeueInterval}, nil
			}
			log.Error(err, "retry budget exhausted, cleanup still blocked",
				"retryElapsed", retryElapsed.Round(time.Second),
				"tunnelID", tunnel.Status.TunnelID)
			r.Recorder.Eventf(tunnel, nil, corev1.EventTypeWarning, "CleanupBlocked", "Delete",
				"Tunnel %s deletion blocked after %s of retries: %v. Set annotation cfgate.io/deletion-policy=orphan to skip cleanup and remove finalizer.",
				tunnel.Status.TunnelID, retryElapsed.Round(time.Second), err)
			return ctrl.Result{RequeueAfter: deletionRequeueInterval}, nil
		}

		log.Info("Deleted tunnel from Cloudflare", "tunnelID", tunnel.Status.TunnelID)
		r.Recorder.Eventf(tunnel, nil, corev1.EventTypeNormal, "TunnelDeleted", "Delete", "Deleted tunnel %s from Cloudflare", tunnel.Status.TunnelID)
	}

	return r.removeTunnelFinalizer(ctx, tunnel)
}

// removeTunnelFinalizer removes the tunnel finalizer using a patch to reduce lock contention.
func (r *CloudflareTunnelReconciler) removeTunnelFinalizer(ctx context.Context, tunnel *cfgatev1alpha1.CloudflareTunnel) (ctrl.Result, error) {
	patch := client.MergeFrom(tunnel.DeepCopy())
	controllerutil.RemoveFinalizer(tunnel, tunnelFinalizer)
	if err := r.Patch(ctx, tunnel, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to remove finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}

// updateStatus updates the CloudflareTunnel status, re-fetching the resource
// first to avoid update conflicts from concurrent modifications.
func (r *CloudflareTunnelReconciler) updateStatus(ctx context.Context, tunnel *cfgatev1alpha1.CloudflareTunnel) error {
	// Use APIReader (direct API server read) to avoid stale informer cache.
	// syncConfiguration patches annotations which bumps ResourceVersion; the
	// informer cache may not reflect this yet, causing 409 Conflict on Status().Update.
	var current cfgatev1alpha1.CloudflareTunnel
	if err := r.APIReader.Get(ctx, types.NamespacedName{Name: tunnel.Name, Namespace: tunnel.Namespace}, &current); err != nil {
		return fmt.Errorf("failed to re-fetch tunnel: %w", err)
	}

	if tunnelStatusEqual(&current.Status, &tunnel.Status) {
		return nil
	}

	current.Status = tunnel.Status

	if err := r.Status().Update(ctx, &current); err != nil {
		return fmt.Errorf("failed to update status: %w", err)
	}

	return nil
}

// tunnelStatusEqual compares two CloudflareTunnel statuses for equality, ignoring
// LastSyncTime which changes on every reconciliation to avoid spurious updates.
func tunnelStatusEqual(a, b *cfgatev1alpha1.CloudflareTunnelStatus) bool {
	// Compare generation
	if a.ObservedGeneration != b.ObservedGeneration {
		return false
	}

	// Compare string fields
	if a.TunnelID != b.TunnelID ||
		a.TunnelName != b.TunnelName ||
		a.TunnelDomain != b.TunnelDomain ||
		a.AccountID != b.AccountID {
		return false
	}

	// Compare int32 fields
	if a.Replicas != b.Replicas ||
		a.ReadyReplicas != b.ReadyReplicas ||
		a.ConnectedRouteCount != b.ConnectedRouteCount {
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

	return true
}

// isTunnelHealthy returns true when the tunnel has a non-empty TunnelID and the
// Ready condition is True. This is a conservative check: if anything looks wrong,
// a full reconcile runs.
func isTunnelHealthy(tunnel *cfgatev1alpha1.CloudflareTunnel) bool {
	if tunnel.Status.TunnelID == "" {
		return false
	}
	return meta.IsStatusConditionTrue(tunnel.Status.Conditions, status.ConditionTypeReady)
}

// getCloudflareClient returns a Cloudflare client for the tunnel, creating one
// if needed. Uses credential cache to avoid repeated API validations.
func (r *CloudflareTunnelReconciler) getCloudflareClient(ctx context.Context, tunnel *cfgatev1alpha1.CloudflareTunnel) (cloudflare.Client, error) {
	// If injected client exists, use it (for testing)
	if r.CFClient != nil {
		return r.CFClient, nil
	}

	// Get credentials from secret
	secretNamespace := tunnel.Spec.Cloudflare.SecretRef.Namespace
	if secretNamespace == "" {
		secretNamespace = tunnel.Namespace
	}

	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      tunnel.Spec.Cloudflare.SecretRef.Name,
		Namespace: secretNamespace,
	}, secret); err != nil {
		return nil, fmt.Errorf("failed to get credentials secret: %w", err)
	}

	// Use cache if available
	if r.CredentialCache != nil {
		return r.CredentialCache.GetOrCreate(ctx, secret, func() (cloudflare.Client, error) {
			return r.createClientFromSecret(secret, tunnel.Spec.Cloudflare.SecretKeys.APIToken)
		})
	}

	return r.createClientFromSecret(secret, tunnel.Spec.Cloudflare.SecretKeys.APIToken)
}

// createClientFromSecret creates a Cloudflare client from a secret.
func (r *CloudflareTunnelReconciler) createClientFromSecret(secret *corev1.Secret, tokenKey string) (cloudflare.Client, error) {
	if tokenKey == "" {
		tokenKey = "CLOUDFLARE_API_TOKEN"
	}

	token, ok := secret.Data[tokenKey]
	if !ok {
		return nil, fmt.Errorf("API token key %q not found in secret", tokenKey)
	}

	return cloudflare.NewClient(string(token))
}

// getCloudflareClientForDeletion returns a Cloudflare client for tunnel deletion,
// trying primary credentials first then fallback if the primary secret was deleted.
func (r *CloudflareTunnelReconciler) getCloudflareClientForDeletion(ctx context.Context, tunnel *cfgatev1alpha1.CloudflareTunnel) (cloudflare.Client, error) {
	log := log.FromContext(ctx)

	// Try primary credentials first
	cfClient, err := r.getCloudflareClient(ctx, tunnel)
	if err == nil {
		return cfClient, nil
	}

	// Check if we have fallback credentials
	if tunnel.Spec.FallbackCredentialsRef == nil {
		return nil, fmt.Errorf("primary credentials unavailable and no fallback configured: %w", err)
	}

	log.Info("using fallback credentials for deletion",
		"fallbackSecret", tunnel.Spec.FallbackCredentialsRef.Name,
		"fallbackNamespace", tunnel.Spec.FallbackCredentialsRef.Namespace)

	// Try fallback credentials
	fallbackNamespace := tunnel.Spec.FallbackCredentialsRef.Namespace
	if fallbackNamespace == "" {
		fallbackNamespace = tunnel.Namespace
	}

	fallbackSecret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      tunnel.Spec.FallbackCredentialsRef.Name,
		Namespace: fallbackNamespace,
	}, fallbackSecret); err != nil {
		return nil, fmt.Errorf("failed to get fallback credentials secret: %w", err)
	}

	// Use same token key as primary
	tokenKey := tunnel.Spec.Cloudflare.SecretKeys.APIToken
	if tokenKey == "" {
		tokenKey = "CLOUDFLARE_API_TOKEN"
	}

	token, ok := fallbackSecret.Data[tokenKey]
	if !ok {
		return nil, fmt.Errorf("API token key %q not found in fallback secret", tokenKey)
	}

	return cloudflare.NewClient(string(token))
}

// resolveAccountID returns the Cloudflare account ID with priority:
// spec.cloudflare.accountId > status.accountId (cached) > resolve from accountName via API.
func (r *CloudflareTunnelReconciler) resolveAccountID(ctx context.Context, cfClient cloudflare.Client, tunnel *cfgatev1alpha1.CloudflareTunnel) (string, error) {
	// If accountId is explicitly set in spec, use it
	if tunnel.Spec.Cloudflare.AccountID != "" {
		return tunnel.Spec.Cloudflare.AccountID, nil
	}

	// If we already resolved it, return cached value from status
	if tunnel.Status.AccountID != "" {
		return tunnel.Status.AccountID, nil
	}

	// Resolve accountName to accountId via API
	if tunnel.Spec.Cloudflare.AccountName != "" {
		account, err := cfClient.GetAccountByName(ctx, tunnel.Spec.Cloudflare.AccountName)
		if err != nil {
			return "", fmt.Errorf("failed to resolve account name %q: %w", tunnel.Spec.Cloudflare.AccountName, err)
		}
		if account == nil {
			return "", fmt.Errorf("account %q not found", tunnel.Spec.Cloudflare.AccountName)
		}
		return account.ID, nil
	}

	return "", fmt.Errorf("neither accountId nor accountName specified")
}

// setCondition sets a condition on the tunnel status.
func (r *CloudflareTunnelReconciler) setCondition(tunnel *cfgatev1alpha1.CloudflareTunnel, conditionType string, status metav1.ConditionStatus, reason, message string) {
	condition := metav1.Condition{
		Type:               conditionType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.Now(),
		ObservedGeneration: tunnel.Generation,
	}

	meta.SetStatusCondition(&tunnel.Status.Conditions, condition)
}

// tunnelConfigHash produces a deterministic SHA-256 hex digest of a TunnelConfiguration.
// Ingress rules are sorted by (hostname, path, service) before hashing so that
// rule collection order does not cause spurious config updates.
func tunnelConfigHash(config cloudflare.TunnelConfiguration) string {
	canonical := make([]cloudflare.IngressRule, len(config.Ingress))
	copy(canonical, config.Ingress)
	sort.Slice(canonical, func(i, j int) bool {
		if canonical[i].Hostname != canonical[j].Hostname {
			return canonical[i].Hostname < canonical[j].Hostname
		}
		if canonical[i].Path != canonical[j].Path {
			return canonical[i].Path < canonical[j].Path
		}
		return canonical[i].Service < canonical[j].Service
	})
	normalized := cloudflare.TunnelConfiguration{
		Ingress:       canonical,
		OriginRequest: config.OriginRequest,
		WarpRouting:   config.WarpRouting,
	}
	data, _ := json.Marshal(normalized)
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum)
}
