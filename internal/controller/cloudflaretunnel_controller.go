package controller

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
	gatewaybeta "sigs.k8s.io/gateway-api/apis/v1beta1"

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

	// deletionWarningAfter is the deletion age at which failed cleanup emits
	// an escalated warning. It does not limit connection drain or retries; the
	// finalizer remains until cleanup succeeds or explicit orphan deletion.
	deletionWarningAfter = 2 * time.Minute

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
	AccessLocks           *AccessLocks
	InstallationNamespace string
	// ClusterDomain is the Kubernetes DNS suffix used for backend Services.
	ClusterDomain  string
	ClientSettings cloudflare.ClientSettings
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
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;create;delete,namespace=system
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services;pods,verbs=get;list;watch
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
		return ctrl.Result{RequeueAfter: 100 * time.Millisecond}, nil
	}

	tunnelLifecycleUnchanged, err := r.canSkipTunnelLifecycle(ctx, &tunnel)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to check lifecycle dependencies: %w", err)
	}

	if tunnelLifecycleUnchanged {
		log.V(1).Info("tunnel lifecycle unchanged, skipping credential/tunnel/deployment checks",
			"generation", tunnel.Generation,
			"lastFullReconcile", tunnel.Status.LastFullReconcileTime.Time)

		deploymentName := cloudflared.DeploymentName(tunnel.Name)
		var deployment appsv1.Deployment
		if err := r.Get(ctx, types.NamespacedName{Name: deploymentName, Namespace: tunnel.Namespace}, &deployment); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to observe connector deployment: %w", err)
		}
		r.observeConnectorDeployment(&tunnel, &deployment)

		syncErr := r.syncConfiguration(ctx, &tunnel)
		if syncErr != nil {
			log.Error(syncErr, "failed to sync configuration in guard path")
			r.setCondition(&tunnel, status.ConditionTypeConfigurationSynced, metav1.ConditionFalse, status.ReasonConfigSyncError, syncErr.Error())
		} else {
			r.setCondition(&tunnel, status.ConditionTypeConfigurationSynced, metav1.ConditionTrue, status.ReasonConfigurationSynced,
				fmt.Sprintf("Configuration synced with %d ingress rules", tunnel.Status.ConnectedRouteCount))
		}

		r.updateTunnelReadiness(&tunnel)
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
	if err := r.deployCloudflared(ctx, &tunnel); err != nil {
		log.Error(err, "failed to deploy cloudflared")
		r.setCondition(&tunnel, status.ConditionTypeCloudflaredDeployed, metav1.ConditionFalse, status.ReasonDeploymentError, err.Error())
		r.setCondition(&tunnel, status.ConditionTypeReady, metav1.ConditionFalse, status.ReasonDeploymentError, "Failed to deploy cloudflared")
		if err := r.updateStatus(ctx, &tunnel); err != nil {
			log.Error(err, "failed to update status")
		}
		r.Recorder.Eventf(&tunnel, nil, corev1.EventTypeWarning, "DeploymentError", "Deploy", "%s", err.Error())
		return ctrl.Result{RequeueAfter: requeueAfterError}, nil
	}

	// 6. Sync configuration
	if err := r.syncConfiguration(ctx, &tunnel, true); err != nil {
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

	dependencyHash, err := r.lifecycleDependencyHash(ctx, r.lifecycleReader(), &tunnel)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to record lifecycle dependencies: %w", err)
	}
	if dependencyHash == "" {
		return ctrl.Result{RequeueAfter: requeueAfterError}, nil
	}

	// 7. Update status
	r.updateTunnelReadiness(&tunnel)
	tunnel.Status.ObservedGeneration = tunnel.Generation
	now := metav1.Now()
	tunnel.Status.LastSyncTime = &now
	tunnel.Status.LastFullReconcileTime = &now
	tunnel.Status.LifecycleDependencyHash = dependencyHash

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
//
// Gateway and HTTPRoute watches use GenerationChangedPredicate to prevent
// reconciliation loops from status-only updates.
func (r *CloudflareTunnelReconciler) SetupWithManager(mgr ctrl.Manager) error {
	log := mgr.GetLogger().WithName("controller").WithName("tunnel")
	log.Info("registering controller with manager")
	r.APIReader = mgr.GetAPIReader()

	controllerBuilder := ctrl.NewControllerManagedBy(mgr).
		For(&cfgatev1alpha1.CloudflareTunnel{},
			builder.WithPredicates(predicate.Or(GenerationOrDeletionPredicate, CfgateAnnotationOrGenerationPredicate)),
		).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Secret{}).
		// Watch Gateway resources that reference our tunnels
		Watches(
			&gateway.Gateway{},
			handler.EnqueueRequestsFromMapFunc(r.findTunnelsForGateway),
			builder.WithPredicates(CfgateAnnotationOrGenerationPredicate),
		).
		// Watch HTTPRoute resources that may affect tunnel configuration
		Watches(
			&gateway.HTTPRoute{},
			handler.EnqueueRequestsFromMapFunc(r.findTunnelsForHTTPRoute),
			builder.WithPredicates(CfgateAnnotationOrGenerationPredicate),
		).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.findTunnelsForCredentialSecret)).
		Watches(&cfgatev1alpha1.CloudflareAccessApplication{}, handler.EnqueueRequestsFromMapFunc(r.findTunnelsForDependency)).
		Watches(&cfgatev1alpha1.CloudflareAccessPolicy{}, handler.EnqueueRequestsFromMapFunc(r.findTunnelsForDependency)).
		Watches(&corev1.Service{}, handler.EnqueueRequestsFromMapFunc(r.findTunnelsForDependency)).
		Watches(&corev1.Namespace{}, handler.EnqueueRequestsFromMapFunc(r.findTunnelsForDependency), builder.WithPredicates(predicate.LabelChangedPredicate{})).
		Watches(&gateway.GatewayClass{}, handler.EnqueueRequestsFromMapFunc(r.findTunnelsForDependency), builder.WithPredicates(predicate.GenerationChangedPredicate{}))
	if _, err := mgr.GetRESTMapper().RESTMapping(schema.GroupKind{Group: gateway.GroupName, Kind: "ReferenceGrant"}, "v1beta1"); err == nil {
		controllerBuilder = controllerBuilder.Watches(&gatewaybeta.ReferenceGrant{}, handler.EnqueueRequestsFromMapFunc(r.findTunnelsForDependency), builder.WithPredicates(predicate.GenerationChangedPredicate{}))
	} else if !meta.IsNoMatchError(err) {
		return fmt.Errorf("discover ReferenceGrant watch: %w", err)
	}
	return controllerBuilder.Complete(withReconcileProgress("cloudflaretunnel", r))
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

	accountID, err := r.resolveAccountID(ctx, cfClient, tunnel)
	if err != nil {
		return fmt.Errorf("failed to resolve account: %w", err)
	}

	if r.InstallationNamespace == "" || tunnel.UID == "" {
		return fmt.Errorf("installation namespace and resource UID are required for tunnel ownership")
	}
	var cfTunnel *cloudflare.Tunnel
	if tunnel.Status.TunnelID != "" {
		cfTunnel, err = cfClient.GetTunnel(ctx, accountID, tunnel.Status.TunnelID)
		if err != nil {
			return fmt.Errorf("verify recorded tunnel ID: %w", err)
		}
	}
	if cfTunnel == nil {
		cfTunnel, err = cfClient.GetTunnelByName(ctx, accountID, tunnel.Spec.Tunnel.Name)
		if err != nil {
			return fmt.Errorf("find tunnel: %w", err)
		}
	}
	created := false
	if cfTunnel == nil {
		cfTunnel, err = cfClient.CreateTunnel(ctx, accountID, cloudflare.CreateTunnelParams{Name: tunnel.Spec.Tunnel.Name, ConfigSrc: "cloudflare"})
		if err != nil {
			return fmt.Errorf("create tunnel: %w", err)
		}
		created = true
	}
	if cfTunnel == nil || cfTunnel.ID == "" {
		return fmt.Errorf("cloudflare returned no tunnel identity")
	}
	if cfTunnel.AccountTag != "" && cfTunnel.AccountTag != accountID {
		return fmt.Errorf("remote tunnel account does not match requested account")
	}
	if err := r.claimTunnel(ctx, tunnel, accountID, cfTunnel.ID, created); err != nil {
		return err
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
func (r *CloudflareTunnelReconciler) deployCloudflared(ctx context.Context, tunnel *cfgatev1alpha1.CloudflareTunnel) error {
	log := log.FromContext(ctx)

	if tunnel.Status.TunnelID == "" {
		return fmt.Errorf("tunnel ID not set in status")
	}
	caRevision, err := r.originCAPoolRevision(ctx, tunnel)
	if err != nil {
		return err
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

	if err := r.verifyTunnelClaim(ctx, tunnel, accountID); err != nil {
		return err
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
	if secret.Data == nil {
		secret.Data = make(map[string][]byte)
	}
	for key, value := range secret.StringData {
		secret.Data[key] = []byte(value)
	}
	secret.StringData = nil
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
				return fmt.Errorf("token secret appeared during reconciliation: %w", err)
			} else {
				log.Info("Created token secret", "name", secret.Name)
				existingSecret = secret
			}
		} else {
			return fmt.Errorf("failed to get token secret: %w", err)
		}
	} else {
		if err := requireControllerOwner(existingSecret, tunnel); err != nil {
			return err
		}
		if !reflect.DeepEqual(existingSecret.Data, secret.Data) {
			existingSecret.Data = secret.Data
			existingSecret.StringData = nil
			if err := r.Update(ctx, existingSecret); err != nil {
				return fmt.Errorf("failed to update token secret: %w", err)
			}
		}

	}

	// Create or update Deployment
	deployment := builder.BuildDeployment(tunnel, token)
	if deployment.Spec.Template.Annotations == nil {
		deployment.Spec.Template.Annotations = make(map[string]string)
	}
	deployment.Spec.Template.Annotations["cfgate.io/tunnel-token-revision"] = string(existingSecret.UID) + "/" + existingSecret.ResourceVersion
	if caRevision != "" {
		deployment.Spec.Template.Annotations["cfgate.io/origin-ca-revision"] = caRevision
	} else {
		delete(deployment.Spec.Template.Annotations, "cfgate.io/origin-ca-revision")
	}
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
				return fmt.Errorf("connector deployment appeared during reconciliation: %w", err)
			} else {
				log.Info("Created cloudflared deployment", "name", deployment.Name)
				r.Recorder.Eventf(tunnel, nil, corev1.EventTypeNormal, "DeploymentCreated", "Create", "Created cloudflared deployment %s", deployment.Name)
			}
		} else {
			return fmt.Errorf("failed to get deployment: %w", err)
		}
	} else {
		if err := requireControllerOwner(existingDeployment, tunnel); err != nil {
			return err
		}
		// Update deployment spec
		existingDeployment.Spec = deployment.Spec
		if err := r.Update(ctx, existingDeployment); err != nil {
			return fmt.Errorf("failed to update deployment: %w", err)
		}
		log.Info("Updated cloudflared deployment", "name", deployment.Name)
	}

	// Read the committed generation so a new rollout cannot reuse stale readiness.
	if err := r.lifecycleReader().Get(ctx, types.NamespacedName{Name: deployment.Name, Namespace: deployment.Namespace}, existingDeployment); err != nil {
		return fmt.Errorf("failed to get deployment status: %w", err)
	}
	r.observeConnectorDeployment(tunnel, existingDeployment)

	return nil
}

func (r *CloudflareTunnelReconciler) originCAPoolRevision(ctx context.Context, tunnel *cfgatev1alpha1.CloudflareTunnel) (string, error) {
	ref := tunnel.Spec.OriginDefaults.CAPoolSecretRef
	if ref == nil {
		return "", nil
	}

	var secret corev1.Secret
	key := ref.Key
	if key == "" {
		key = cloudflared.DefaultOriginCAPoolSecretKey
	}
	namespacedName := types.NamespacedName{Name: ref.Name, Namespace: tunnel.Namespace}
	if err := r.Get(ctx, namespacedName, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return "", fmt.Errorf("origin CA pool Secret %s/%s not found", tunnel.Namespace, ref.Name)
		}
		return "", fmt.Errorf("failed to get origin CA pool Secret %s/%s: %w", tunnel.Namespace, ref.Name, err)
	}
	if _, ok := secret.Data[key]; !ok {
		return "", fmt.Errorf("origin CA pool Secret %s/%s missing key %q", tunnel.Namespace, ref.Name, key)
	}
	if !x509.NewCertPool().AppendCertsFromPEM(secret.Data[key]) {
		return "", fmt.Errorf("origin CA pool Secret %s/%s key %q contains no usable PEM certificates", tunnel.Namespace, ref.Name, key)
	}
	return fmt.Sprintf("%x", sha256.Sum256(secret.Data[key])), nil
}

func (r *CloudflareTunnelReconciler) validateOriginCAPoolSecretRef(ctx context.Context, tunnel *cfgatev1alpha1.CloudflareTunnel) error {
	_, err := r.originCAPoolRevision(ctx, tunnel)
	return err
}

// syncConfiguration syncs the tunnel configuration to Cloudflare.
// Collects routes from Gateway/HTTPRoute resources and pushes to Cloudflare API.
func (r *CloudflareTunnelReconciler) syncConfiguration(ctx context.Context, tunnel *cfgatev1alpha1.CloudflareTunnel, forceVerification ...bool) (syncErr error) {
	defer func() {
		if errors.Is(syncErr, cloudflare.ErrConfigurationBudget) {
			if err := r.withdrawOverloadedTunnel(ctx, tunnel); err != nil {
				syncErr = fmt.Errorf("%w; withdrawal failed: %v", syncErr, err)
			}
		}
	}()

	log := log.FromContext(ctx)

	if tunnel.Status.TunnelID == "" {
		return fmt.Errorf("tunnel ID not set in status")
	}

	accessState, releaseAccess, err := r.beginAccessSync(ctx, tunnel)
	if err != nil {
		return err
	}
	defer releaseAccess()
	collector := *r
	hasAccess := accessState != nil
	if hasAccess {
		collector.Client = directReadClient{Client: r.Client, reader: r.lifecycleReader()}
	}

	// Collect ingress rules from HTTPRoutes using fresh authorization reads for Access dependencies.
	rules, routeCount, err := collector.collectIngressRules(ctx, tunnel, accessState)
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

	if blocked := applyConnectorCompatibility(tunnel, &config); blocked > 0 && r.Recorder != nil {
		r.Recorder.Eventf(tunnel, nil, corev1.EventTypeWarning, "IncompatibleConnectorImage", "Publish", "stock cloudflared does not support h2cOrigin; blocked %d HTTP origin rules", blocked)
	}

	if err := cloudflare.ValidateTunnelConfiguration(config, r.ClientSettings); err != nil {
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

	if err := r.verifyTunnelClaim(ctx, tunnel, accountID); err != nil {
		return err
	}

	var desiredAccess []cfgatev1alpha1.TunnelAccessDependency
	if hasAccess {
		desiredAccess, err = normalizedAccessDependencies(accessState.dependencies, accountID, tunnel.Status.TunnelID)
		if err != nil {
			return err
		}
		pending, err := normalizedAccessDependencies(append(append([]cfgatev1alpha1.TunnelAccessDependency(nil), tunnel.Status.AccessDependencies...), desiredAccess...), accountID, tunnel.Status.TunnelID)
		if err != nil {
			return err
		}
		for i := range pending {
			pending[i].Pending = true
		}
		if err := r.persistAccessDependencies(ctx, tunnel, pending); err != nil {
			return err
		}
	}

	desiredHash := appliedTunnelConfigHash(accountID, tunnel.Status.TunnelID, config)
	currentHash := tunnel.Annotations[configHashAnnotation]
	verifyRemote := hasAccess || (len(forceVerification) > 0 && forceVerification[0]) || tunnel.Status.LastFullReconcileTime == nil || time.Since(tunnel.Status.LastFullReconcileTime.Time) >= fullLifecycleInterval
	if desiredHash == currentHash && !verifyRemote {
		log.V(1).Info("tunnel configuration unchanged, skipping update",
			"tunnelID", tunnel.Status.TunnelID)
		tunnel.Status.ConnectedRouteCount = int32(routeCount)
		return nil
	}

	remoteMatches := false
	if verifyRemote {
		remote, err := cfClient.GetTunnelConfiguration(ctx, accountID, tunnel.Status.TunnelID)
		if err != nil {
			return fmt.Errorf("failed to verify remote configuration: %w", err)
		}
		remoteMatches = remote != nil && equivalentTunnelConfiguration(*remote, config)
	}
	if !remoteMatches {
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

	}

	if hasAccess {
		observed, err := cfClient.GetTunnelConfiguration(ctx, accountID, tunnel.Status.TunnelID)
		if err != nil {
			return fmt.Errorf("confirm Access-dependent tunnel configuration: %w", err)
		}
		if observed == nil || !equivalentTunnelConfiguration(*observed, config) {
			return fmt.Errorf("required Access-dependent tunnel configuration is not yet confirmed")
		}
		if err := r.persistAccessDependencies(ctx, tunnel, desiredAccess); err != nil {
			return err
		}
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
func (r *CloudflareTunnelReconciler) collectIngressRules(ctx context.Context, tunnel *cfgatev1alpha1.CloudflareTunnel, accessState *accessSyncSession) ([]cloudflare.IngressRule, int, error) {
	var rules []orderedIngressRule
	routeCount := 0

	// Find Gateways that reference this tunnel
	var gateways gateway.GatewayList
	if err := r.List(ctx, &gateways); err != nil {
		return nil, 0, fmt.Errorf("failed to list gateways: %w", err)
	}

	relevantGateways := map[types.NamespacedName]gateway.Gateway{}
	classes := map[gateway.ObjectName]bool{}
	for _, gw := range gateways.Items {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		key, managed, err := managedGatewayTunnel(ctx, r.Client, &gw, classes)
		if err != nil {
			return nil, 0, err
		}
		if managed && key == client.ObjectKeyFromObject(tunnel) {
			relevantGateways[client.ObjectKeyFromObject(&gw)] = gw
		}
	}

	// Fetch routes once and validate backends once per admitted route, independent
	// of the number of parents, hostnames or path matches emitted.
	var routes gateway.HTTPRouteList
	if err := r.List(ctx, &routes); err != nil {
		return nil, 0, fmt.Errorf("failed to list httproutes: %w", err)
	}
	for _, route := range routes.Items {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		var hostnames []gateway.Hostname
		seen := map[gateway.Hostname]bool{}
		for _, parentRef := range route.Spec.ParentRefs {
			if !isGatewayParentRef(parentRef) {
				continue
			}
			parentNS := route.Namespace
			if parentRef.Namespace != nil {
				parentNS = string(*parentRef.Namespace)
			}
			gw, relevant := relevantGateways[types.NamespacedName{Namespace: parentNS, Name: string(parentRef.Name)}]
			if !relevant {
				continue
			}
			accepted, err := acceptedRouteHostnames(ctx, r.Client, &route, &gw, parentRef)
			if err != nil {
				return nil, 0, fmt.Errorf("validate listeners for HTTPRoute %s/%s: %w", route.Namespace, route.Name, err)
			}
			for _, hostname := range accepted {
				if !seen[hostname] {
					seen[hostname] = true
					hostnames = append(hostnames, hostname)
				}
			}
		}
		if len(hostnames) == 0 {
			continue
		}
		if err := validateHTTPRouteFeatures(&route); err != nil {
			continue
		}
		// An attached route with invalid backends still owns its matches. Preserve
		// those matches as HTTP 500 responses instead of falling through to a
		// less specific route, while keeping valid sibling rules available.
		resolvedRoute := route.DeepCopy()
		for ruleIndex := range route.Spec.Rules {
			singleRule := route
			singleRule.Spec.Rules = []gateway.HTTPRouteRule{route.Spec.Rules[ruleIndex]}
			condition, err := validateHTTPRouteBackendRefs(ctx, r.Client, &singleRule)
			if err != nil {
				return nil, 0, fmt.Errorf("validate backends for HTTPRoute %s/%s: %w", route.Namespace, route.Name, err)
			}
			if condition.Status != metav1.ConditionTrue {
				resolvedRoute.Spec.Rules[ruleIndex].BackendRefs = nil
			}
		}
		// Account for the catch-all before allocating hostname/match expansion.
		count := len(rules) + 1
		for _, rule := range route.Spec.Rules {
			matches := len(rule.Matches)
			if matches == 0 {
				matches = 1
			}
			for range hostnames {
				count += matches
				if err := r.ClientSettings.CheckRuleCount(count); err != nil {
					return nil, 0, err
				}
			}
		}
		routeRules, err := r.buildOrderedRulesFromHTTPRoute(resolvedRoute, hostnames, tunnel.Spec.OriginDefaults.CAPoolSecretRef != nil)
		if err != nil {
			r.Recorder.Eventf(tunnel, nil, corev1.EventTypeWarning, "HTTPRouteError", "CollectRules", "skipping HTTPRoute %s/%s: %s", route.Namespace, route.Name, err.Error())
			continue
		}
		if err := r.requiredAccessAllows(ctx, accessState, tunnel, resolvedRoute, hostnames); err != nil {
			for i := range routeRules {
				routeRules[i].Service = "http_status:503"
				routeRules[i].accessDenied = true
				routeRules[i].OriginRequest = nil
			}
			if r.Recorder != nil {
				r.Recorder.Eventf(&route, nil, corev1.EventTypeWarning, "AccessRequiredUnavailable", "Publish", "%s", err.Error())
			}
		}
		rules = append(rules, routeRules...)
		if len(routeRules) > 0 {
			routeCount++
		}
	}

	sort.SliceStable(rules, func(i, j int) bool { return ingressRuleBefore(rules[i], rules[j]) })
	return flattenIngressRules(rules), routeCount, nil
}

// buildRulesFromHTTPRoute builds ingress rules from an HTTPRoute by iterating
// all hostnames and all matches per rule. Returns an error if a rule has
// multiple backendRefs (Cloudflare tunnel ingress is 1:1) or an unsupported
// backend group/kind. Rules without an active backend produce an HTTP 500 response.
func (r *CloudflareTunnelReconciler) buildRulesFromHTTPRoute(route *gateway.HTTPRoute) ([]cloudflare.IngressRule, error) {
	return r.buildRulesFromHTTPRouteForHostnames(route, effectiveHTTPRouteHostnames(route), false)
}

func (r *CloudflareTunnelReconciler) buildRulesFromHTTPRouteForHostnames(route *gateway.HTTPRoute, hostnames []gateway.Hostname, originCAPoolMounted bool) ([]cloudflare.IngressRule, error) {
	rules, err := r.buildOrderedRulesFromHTTPRoute(route, hostnames, originCAPoolMounted)
	return flattenIngressRules(rules), err
}

func (r *CloudflareTunnelReconciler) buildOrderedRulesFromHTTPRoute(route *gateway.HTTPRoute, hostnames []gateway.Hostname, originCAPoolMounted bool) ([]orderedIngressRule, error) {
	if err := validateHTTPRouteFeatures(route); err != nil {
		return nil, err
	}
	if err := validateRouteOriginCAPool(route, originCAPoolMounted); err != nil {
		return nil, err
	}
	var rules []orderedIngressRule
	for _, hostname := range hostnames {
		for ruleIndex, rule := range route.Spec.Rules {
			if len(rule.BackendRefs) > 1 {
				return nil, fmt.Errorf("route %s/%s: multiple backendRefs not supported for tunnel ingress rules", route.Namespace, route.Name)
			}
			service := "http_status:500"
			var originRequest *cloudflare.OriginRequestConfig
			if len(rule.BackendRefs) == 1 {
				backend := rule.BackendRefs[0]
				if backend.Group != nil && *backend.Group != "" {
					return nil, fmt.Errorf("route %s/%s: unsupported backend group %q", route.Namespace, route.Name, *backend.Group)
				}
				if backend.Kind != nil && *backend.Kind != "" && *backend.Kind != "Service" {
					return nil, fmt.Errorf("route %s/%s: unsupported backend kind %q", route.Namespace, route.Name, *backend.Kind)
				}
				if backend.Weight == nil || *backend.Weight > 0 {
					port := int32(80)
					if backend.Port != nil {
						port = int32(*backend.Port)
					}
					namespace := route.Namespace
					if backend.Namespace != nil && *backend.Namespace != "" {
						namespace = string(*backend.Namespace)
					}
					protocol := annotations.GetAnnotation(route, annotations.AnnotationOriginProtocol)
					if protocol != "https" {
						protocol = "http"
					}
					domain := r.ClusterDomain
					if domain == "" {
						domain = "cluster.local"
					}
					service = fmt.Sprintf("%s://%s.%s.svc.%s:%d", protocol, backend.Name, namespace, domain, port)
					originRequest = cloudflaredOriginRequestToCloudflare(cloudflared.BuildOriginConfig(nil, route.Annotations))
				}
			}
			matches := rule.Matches
			if len(matches) == 0 {
				matches = []gateway.HTTPRouteMatch{{}}
			}
			for matchIndex, match := range matches {
				path, err := cloudflaredPathRegex(match)
				if err != nil {
					return nil, fmt.Errorf("route %s/%s: %w", route.Namespace, route.Name, err)
				}
				pathType, pathLength := routePathPrecedence(match)
				rules = append(rules, orderedIngressRule{IngressRule: cloudflare.IngressRule{Hostname: string(hostname), Path: path, Service: service, OriginRequest: originRequest}, pathType: pathType, pathLength: pathLength, created: route.CreationTimestamp, routeName: route.Namespace + "/" + route.Name, ruleIndex: ruleIndex, matchIndex: matchIndex})
			}
		}
	}
	return rules, nil
}

func validateRouteOriginCAPool(route *gateway.HTTPRoute, originCAPoolMounted bool) error {
	caPool := annotations.GetAnnotation(route, annotations.AnnotationOriginCAPool)
	if caPool == "" {
		return nil
	}
	if caPool != cloudflared.OriginCAPoolPath() {
		return fmt.Errorf("route %s/%s: %s must be %q for managed origin CA pools",
			route.Namespace, route.Name, annotations.AnnotationOriginCAPool, cloudflared.OriginCAPoolPath())
	}
	if !originCAPoolMounted {
		return fmt.Errorf("route %s/%s: %s requires CloudflareTunnel spec.originDefaults.caPoolSecretRef",
			route.Namespace, route.Name, annotations.AnnotationOriginCAPool)
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

func cloudflaredOriginRequestToCloudflare(config *cloudflared.OriginRequestConfig) *cloudflare.OriginRequestConfig {
	if config == nil {
		return nil
	}
	return &cloudflare.OriginRequestConfig{
		ConnectTimeout:   config.ConnectTimeout,
		NoTLSVerify:      config.NoTLSVerify,
		HTTP2Origin:      config.HTTP2Origin,
		H2cOrigin:        config.H2cOrigin,
		HTTPHostHeader:   config.HTTPHostHeader,
		OriginServerName: config.OriginServerName,
		CAPool:           config.CAPool,
	}
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
		path = strings.TrimRight(path, "/")
		if path == "" {
			return "^/.*$", nil
		}
		quoted := regexp.QuoteMeta(path)
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

	drained, drainErr := r.drainConnector(ctx, tunnel)
	if drainErr != nil || !drained {
		message := "Waiting for connector Pods to terminate before remote cleanup"
		if drainErr != nil {
			message = drainErr.Error()
		}
		r.setCondition(tunnel, status.ConditionTypeReady, metav1.ConditionFalse, "CleanupBlocked", message)
		if err := r.updateStatus(ctx, tunnel); err != nil {
			return ctrl.Result{}, err
		}
		if drainErr != nil {
			return ctrl.Result{}, fmt.Errorf("connector cleanup blocked: %w", drainErr)
		}
		return ctrl.Result{RequeueAfter: deletionRequeueInterval}, nil
	}

	cleanupAccountID := ""
	if tunnel.Status.TunnelID != "" {
		cfClient, err := r.getCloudflareClientForDeletion(ctx, tunnel)
		if err != nil {
			log.Error(err, "failed to create Cloudflare client for deletion")
			retryElapsed := time.Since(tunnel.DeletionTimestamp.Time)
			if retryElapsed < deletionWarningAfter {
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
			if retryElapsed < deletionWarningAfter {
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

		cleanupAccountID = accountID
		if err := r.verifyTunnelClaim(ctx, tunnel, accountID); err != nil {
			// A previous attempt may have released the claim before its finalizer patch failed.
			// Only confirmed remote absence permits completion without a claim; never mutate an unclaimed tunnel.
			if apierrors.IsNotFound(err) {
				remaining, readErr := cfClient.GetTunnel(ctx, accountID, tunnel.Status.TunnelID)
				if readErr != nil {
					return ctrl.Result{}, fmt.Errorf("confirm unclaimed tunnel absence: %w", readErr)
				}
				if remaining == nil {
					return r.removeTunnelFinalizer(ctx, tunnel)
				}
			}
			return ctrl.Result{}, fmt.Errorf("tunnel cleanup ownership: %w", err)
		}
		if err := tunnelService.Delete(ctx, accountID, tunnel.Status.TunnelID); err != nil {
			retryElapsed := time.Since(tunnel.DeletionTimestamp.Time)
			if retryElapsed < deletionWarningAfter {
				log.Error(err, "failed to delete tunnel from Cloudflare, will retry",
					"retryElapsed", retryElapsed.Round(time.Second),
					"warningAfter", deletionWarningAfter)
				r.Recorder.Eventf(tunnel, nil, corev1.EventTypeWarning, "CleanupFailed", "Delete",
					"Failed to delete tunnel %s: %v. Set annotation cfgate.io/deletion-policy=orphan to skip cleanup and remove finalizer.",
					tunnel.Status.TunnelID, err)
				return ctrl.Result{RequeueAfter: deletionRequeueInterval}, nil
			}
			log.Error(err, "cleanup warning threshold reached, will keep retrying",
				"retryElapsed", retryElapsed.Round(time.Second),
				"tunnelID", tunnel.Status.TunnelID)
			r.Recorder.Eventf(tunnel, nil, corev1.EventTypeWarning, "CleanupBlocked", "Delete",
				"Tunnel %s deletion blocked after %s of retries: %v. Set annotation cfgate.io/deletion-policy=orphan to skip cleanup and remove finalizer.",
				tunnel.Status.TunnelID, retryElapsed.Round(time.Second), err)
			return ctrl.Result{RequeueAfter: deletionRequeueInterval}, nil
		}

		remaining, err := cfClient.GetTunnel(ctx, accountID, tunnel.Status.TunnelID)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("confirm tunnel deletion: %w", err)
		}
		if remaining != nil {
			return ctrl.Result{RequeueAfter: deletionRequeueInterval}, nil
		}
		log.Info("Deleted tunnel from Cloudflare", "tunnelID", tunnel.Status.TunnelID)
		r.Recorder.Eventf(tunnel, nil, corev1.EventTypeNormal, "TunnelDeleted", "Delete", "Deleted tunnel %s from Cloudflare", tunnel.Status.TunnelID)

	}

	if tunnel.Status.TunnelID != "" {
		if err := r.releaseTunnelClaim(ctx, tunnel, cleanupAccountID); err != nil {
			return ctrl.Result{}, fmt.Errorf("release tunnel ownership claim: %w", err)
		}
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
	if !reflect.DeepEqual(a.AccessDependencies, b.AccessDependencies) {
		return false
	}
	// Compare generation
	if a.ObservedGeneration != b.ObservedGeneration ||
		a.LifecycleDependencyHash != b.LifecycleDependencyHash ||
		!a.LastFullReconcileTime.Equal(b.LastFullReconcileTime) {
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

	if err := requireCredentialGrant(ctx, r.Client, tunnel.Namespace, "CloudflareTunnel", secretNamespace, tunnel.Spec.Cloudflare.SecretRef.Name); err != nil {
		return nil, err
	}
	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      tunnel.Spec.Cloudflare.SecretRef.Name,
		Namespace: secretNamespace,
	}, secret); err != nil {
		return nil, fmt.Errorf("failed to get credentials secret: %w", err)
	}

	return cloudflare.NewClientFromSecret(ctx, secret, tunnel.Spec.Cloudflare.SecretKeys.APIToken, r.CredentialCache, r.ClientSettings)
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

	if err := requireCredentialGrant(ctx, r.Client, tunnel.Namespace, "CloudflareTunnel", fallbackNamespace, tunnel.Spec.FallbackCredentialsRef.Name); err != nil {
		return nil, err
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

	return cloudflare.NewClientFromSecret(ctx, fallbackSecret, tokenKey, r.CredentialCache, r.ClientSettings)
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

// tunnelConfigHash preserves ingress order because cloudflared uses first-match routing.
func tunnelConfigHash(config cloudflare.TunnelConfiguration) string {
	data, _ := json.Marshal(config)
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum)
}

// withdrawOverloadedTunnel replaces all forwarding without allocating the rejected plan.
// Receipts survive failed writes and readback so Access cleanup remains blocked.
func (r *CloudflareTunnelReconciler) withdrawOverloadedTunnel(ctx context.Context, tunnel *cfgatev1alpha1.CloudflareTunnel) error {
	cfClient, err := r.getCloudflareClient(ctx, tunnel)
	if err != nil {
		return err
	}
	account, err := r.resolveAccountID(ctx, cfClient, tunnel)
	if err != nil {
		return err
	}
	if err := r.verifyTunnelClaim(ctx, tunnel, account); err != nil {
		return err
	}
	config := cloudflare.TunnelConfiguration{Ingress: []cloudflare.IngressRule{{Service: "http_status:503"}}}
	if err := cfClient.UpdateTunnelConfiguration(ctx, account, tunnel.Status.TunnelID, config); err != nil {
		return err
	}
	observed, err := cfClient.GetTunnelConfiguration(ctx, account, tunnel.Status.TunnelID)
	if err != nil {
		return err
	}
	if observed == nil || !equivalentTunnelConfiguration(*observed, config) {
		return fmt.Errorf("overload withdrawal is not confirmed")
	}
	if err := r.persistAccessDependencies(ctx, tunnel, nil); err != nil {
		return err
	}
	// Invalidate the old publication hash so recovery cannot retain the denial plan.
	base := tunnel.DeepCopy()
	delete(tunnel.Annotations, configHashAnnotation)
	if err := r.Patch(ctx, tunnel, client.MergeFrom(base)); err != nil {
		return err
	}
	tunnel.Status.ConnectedRouteCount = 0
	if r.Recorder != nil {
		r.Recorder.Eventf(tunnel, nil, corev1.EventTypeWarning, "ConfigurationOverloaded", "Withdraw", "Tunnel forwarding disabled until configuration fits its limits")
	}
	return nil
}
