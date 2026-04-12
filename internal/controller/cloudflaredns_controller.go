package controller

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/cloudflare"
	"cfgate.io/cfgate/internal/controller/annotations"
	"cfgate.io/cfgate/internal/controller/status"
)

const (
	// dnsFinalizer is the finalizer for CloudflareDNS resources.
	dnsFinalizer = "cfgate.io/dns-cleanup"

	// defaultOwnershipPrefix is the default prefix for TXT ownership records.
	dnsDefaultOwnershipPrefix = "_cfgate"

	// dnsTunnelRefNameIndex is the field indexer key for CloudflareDNS spec.tunnelRef.name.
	dnsTunnelRefNameIndex = "spec.tunnelRef.name"

	// dnsGatewayRoutesEnabledIndex is the field indexer key for CloudflareDNS spec.source.gatewayRoutes.enabled.
	dnsGatewayRoutesEnabledIndex = "spec.source.gatewayRoutes.enabled"

	// dnsDeletionRetryBudget is the maximum time to retry DNS record cleanup
	// before emitting an escalated warning. After this budget, the controller
	// keeps blocking (does not remove the finalizer). The only escape is the
	// cfgate.io/deletion-policy=orphan annotation.
	dnsDeletionRetryBudget = 1 * time.Minute

	// dnsDeletionRequeueInterval is the requeue delay between deletion retries.
	dnsDeletionRequeueInterval = 15 * time.Second
)

func extractDNSTunnelRefName(obj client.Object) []string {
	dns, ok := obj.(*cfgatev1alpha1.CloudflareDNS)
	if !ok || dns.Spec.TunnelRef == nil {
		return nil
	}
	ns := dns.Spec.TunnelRef.Namespace
	if ns == "" {
		ns = dns.Namespace
	}
	return []string{ns + "/" + dns.Spec.TunnelRef.Name}
}

func extractDNSGatewayRoutesEnabled(obj client.Object) []string {
	dns, ok := obj.(*cfgatev1alpha1.CloudflareDNS)
	if !ok {
		return nil
	}
	if dns.Spec.Source.GatewayRoutes != nil && dns.Spec.Source.GatewayRoutes.Enabled {
		return []string{"true"}
	}
	return nil
}

// HostnameConfig holds the effective DNS configuration for a hostname after
// merging route-discovered and explicit configuration sources.
type HostnameConfig struct {
	// Target overrides the resource-level resolved target for this hostname.
	// Empty means use the resource-level target passed to syncRecords.
	Target string
	// TTL is the DNS record TTL in seconds (0 means use default)
	TTL int32
	// Proxied indicates if Cloudflare proxy should be enabled (nil means use default)
	Proxied *bool
	// RecordType is the DNS record type (CNAME, A, AAAA); defaults to CNAME
	RecordType string
}

// hostnameKeys extracts hostname strings from a HostnameConfig map for functions
// that only need the list of hostnames without per-hostname configuration.
func hostnameKeys(configs map[string]HostnameConfig) []string {
	keys := make([]string, 0, len(configs))
	for k := range configs {
		keys = append(keys, k)
	}
	return keys
}

// CloudflareDNSReconciler reconciles a CloudflareDNS object.
// It manages DNS records for CloudflareTunnel resources or external targets
// by watching Gateway API routes and syncing hostnames to Cloudflare DNS.
type CloudflareDNSReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder events.EventRecorder

	// APIReader provides uncached reads to avoid informer cache staleness
	// during hostname collection. Without this, r.List() may return stale
	// Gateway data when the annotation-add event that triggered reconciliation
	// hasn't propagated to the informer cache yet.
	APIReader client.Reader

	// CFClient is the Cloudflare API client. Injected for testing.
	CFClient cloudflare.Client

	// CredentialCache caches validated Cloudflare clients to avoid repeated validations.
	CredentialCache *cloudflare.CredentialCache
}

// +kubebuilder:rbac:groups=cfgate.io,resources=cloudflarednses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cfgate.io,resources=cloudflarednses/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cfgate.io,resources=cloudflarednses/finalizers,verbs=update
// +kubebuilder:rbac:groups=cfgate.io,resources=cloudflaretunnels,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch

// Reconcile handles the reconciliation loop for CloudflareDNS resources.
// It collects hostnames from routes, resolves zones, and syncs DNS records.
//
// The reconciliation proceeds through these phases:
//  1. Fetch the CloudflareDNS resource
//  2. Handle deletion via finalizers (cleanup DNS records)
//  3. Resolve target (tunnel domain or external target)
//  4. Collect hostnames from explicit config and Gateway routes
//  5. Validate Cloudflare API credentials
//  6. Resolve zone names to zone IDs
//  7. Sync DNS records with conflict detection
//  8. Verify ownership TXT records (if enabled)
//  9. Update status conditions
//
// On error, the controller requeues after 30 seconds. On success, it requeues
// after 5 minutes for periodic DNS sync.
func (r *CloudflareDNSReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithName("controller").WithName("dns")
	logger.Info("starting reconciliation", "namespace", req.Namespace, "name", req.Name)

	// 1. Fetch CloudflareDNS resource
	var dns cfgatev1alpha1.CloudflareDNS
	if err := r.Get(ctx, req.NamespacedName, &dns); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("CloudflareDNS not found, ignoring")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to get CloudflareDNS: %w", err)
	}

	// 2. Handle deletion
	if !dns.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &dns)
	}

	// Add finalizer if not present (using patch to reduce lock contention)
	if !controllerutil.ContainsFinalizer(&dns, dnsFinalizer) {
		patch := client.MergeFrom(dns.DeepCopy())
		controllerutil.AddFinalizer(&dns, dnsFinalizer)
		if err := r.Patch(ctx, &dns, patch); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to add finalizer: %w", err)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// 3. Resolve target (tunnel or external)
	target, tunnel, err := r.resolveTarget(ctx, &dns)
	if err != nil {
		logger.Error(err, "failed to resolve target")
		r.setCondition(&dns, status.ConditionTypeReady, metav1.ConditionFalse, status.ReasonTargetResolutionFailed, err.Error())
		if updateErr := r.updateStatus(ctx, &dns); updateErr != nil {
			logger.Error(updateErr, "failed to update status")
		}
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// Update resolved target in status
	dns.Status.ResolvedTarget = target

	// 4. Collect hostnames
	hostnames, err := r.collectHostnames(ctx, &dns, tunnel)
	if err != nil {
		logger.Error(err, "failed to collect hostnames")
		r.setCondition(&dns, status.ConditionTypeReady, metav1.ConditionFalse, status.ReasonHostnameCollectionFailed, err.Error())
		if updateErr := r.updateStatus(ctx, &dns); updateErr != nil {
			logger.Error(updateErr, "failed to update status")
		}
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// If gatewayRoutes is enabled but 0 hostnames found, distinguish between:
	//   (a) Initial provisioning: routes haven't appeared yet → short requeue
	//   (b) Route removal: routes were deleted after prior sync → proceed to syncRecords
	//       so deleteOrphanedRecords can clean up the now-orphaned DNS records
	//
	// Only applies in tunnel-ref mode: in external target mode (tunnel == nil),
	// gateway route discovery is structurally unavailable and 0 hostnames is
	// permanent, not transient.
	hasPreviouslySyncedRecords := dns.Status.SyncedRecords > 0 || len(dns.Status.Records) > 0
	if tunnel != nil &&
		dns.Spec.Source.GatewayRoutes != nil &&
		dns.Spec.Source.GatewayRoutes.Enabled &&
		len(hostnames) == 0 &&
		len(dns.Spec.Source.Explicit) == 0 &&
		!hasPreviouslySyncedRecords {
		logger.Info("no hostnames discovered with gatewayRoutes enabled, requeueing",
			"requeueAfter", "10s",
		)
		r.setCondition(&dns, status.ConditionTypeRecordsSynced, metav1.ConditionUnknown,
			status.ReasonNoHostnamesDiscovered,
			"Gateway routes enabled but no hostnames found yet; retrying",
		)
		r.setCondition(&dns, status.ConditionTypeReady, metav1.ConditionUnknown,
			status.ReasonNoHostnamesDiscovered,
			"Waiting for hostname discovery from gateway routes",
		)
		if updateErr := r.updateStatus(ctx, &dns); updateErr != nil {
			logger.Error(updateErr, "failed to update status")
		}
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	// 5. Get Cloudflare client
	cfClient, err := r.getCloudflareClient(ctx, &dns, tunnel)
	if err != nil {
		logger.Error(err, "failed to create Cloudflare client")
		r.setCondition(&dns, status.ConditionTypeCredentialsValid, metav1.ConditionFalse, status.ReasonCredentialsInvalid, err.Error())
		if updateErr := r.updateStatus(ctx, &dns); updateErr != nil {
			logger.Error(updateErr, "failed to update status")
		}
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	r.setCondition(&dns, status.ConditionTypeCredentialsValid, metav1.ConditionTrue, status.ReasonCredentialsValid, "Credentials are valid")

	dnsService := cloudflare.NewDNSService(cfClient, logger).WithCache(cloudflare.NewDNSRecordCache())

	// 6. Resolve zones
	zones, err := r.resolveZones(ctx, &dns, dnsService)
	if err != nil {
		logger.Error(err, "failed to resolve zones")
		r.setCondition(&dns, status.ConditionTypeZonesResolved, metav1.ConditionFalse, status.ReasonZoneResolutionFailed, err.Error())
		if updateErr := r.updateStatus(ctx, &dns); updateErr != nil {
			logger.Error(updateErr, "failed to update status")
		}
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	r.setCondition(&dns, status.ConditionTypeZonesResolved, metav1.ConditionTrue, status.ReasonZonesResolved, "All zones resolved successfully")

	// 7. Sync records
	if err := r.syncRecords(ctx, &dns, target, hostnames, zones, dnsService); err != nil {
		logger.Error(err, "failed to sync records")
		r.setCondition(&dns, status.ConditionTypeRecordsSynced, metav1.ConditionFalse, status.ReasonRecordSyncFailed, err.Error())
		if updateErr := r.updateStatus(ctx, &dns); updateErr != nil {
			logger.Error(updateErr, "failed to update status")
		}
		r.Recorder.Eventf(&dns, nil, corev1.EventTypeWarning, "SyncFailed", "Sync", "%s", err.Error())
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	if dns.Status.FailedRecords > 0 {
		msg := fmt.Sprintf("%d of %d records failed to sync", dns.Status.FailedRecords, dns.Status.SyncedRecords+dns.Status.FailedRecords)
		r.setCondition(&dns, status.ConditionTypeRecordsSynced, metav1.ConditionFalse, status.ReasonRecordSyncFailed, msg)
	} else {
		r.setCondition(&dns, status.ConditionTypeRecordsSynced, metav1.ConditionTrue, status.ReasonRecordsSynced, "DNS records synced successfully")
	}

	// 8. Verify ownership records
	ownershipVerified, err := r.verifyOwnership(ctx, &dns, zones, hostnameKeys(hostnames), dnsService)
	if err != nil {
		logger.V(1).Info("ownership verification issue", "error", err.Error())
		r.setCondition(&dns, status.ConditionTypeOwnershipVerified, metav1.ConditionFalse, status.ReasonOwnershipFailed, err.Error())
	} else if ownershipVerified {
		r.setCondition(&dns, status.ConditionTypeOwnershipVerified, metav1.ConditionTrue, status.ReasonOwnershipVerified, "Ownership records verified")
	} else {
		r.setCondition(&dns, status.ConditionTypeOwnershipVerified, metav1.ConditionFalse, status.ReasonOwnershipFailed, "Ownership TXT records disabled")
	}

	// 9. Update overall Ready status
	if dns.Status.FailedRecords > 0 {
		msg := fmt.Sprintf("DNS sync partially failed: %d record(s) failed", dns.Status.FailedRecords)
		r.setCondition(&dns, status.ConditionTypeReady, metav1.ConditionFalse, status.ReasonSyncPartiallyFailed, msg)
	} else {
		r.setCondition(&dns, status.ConditionTypeReady, metav1.ConditionTrue, status.ReasonReady, "DNS sync is operational")
	}
	dns.Status.ObservedGeneration = dns.Generation
	now := metav1.Now()
	dns.Status.LastSyncTime = &now

	if err := r.updateStatus(ctx, &dns); err != nil {
		logger.Error(err, "failed to update status")
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	if dns.Status.FailedRecords > 0 {
		r.Recorder.Eventf(&dns, nil, corev1.EventTypeWarning, "Reconciled", "Reconcile", "DNS sync completed with %d failure(s)", dns.Status.FailedRecords)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	r.Recorder.Eventf(&dns, nil, corev1.EventTypeNormal, "Reconciled", "Reconcile", "DNS sync completed successfully")
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

// SetupWithManager sets up the controller with the Manager.
//
// Watched resources:
//   - CloudflareDNS (primary, with GenerationChangedPredicate)
//   - CloudflareTunnel (via spec.tunnelRef, with GenerationChangedPredicate)
//   - HTTPRoute (for hostname collection, with CfgateAnnotationOrGenerationPredicate)
//   - Gateway (for tunnel reference, with CfgateAnnotationOrGenerationPredicate)
//
// Uses GenerationChangedPredicate for CRD-only resources to prevent status-only
// reconciliation loops. Uses CfgateAnnotationOrGenerationPredicate on Gateway/HTTPRoute
// watchers where cfgate.io/* annotation changes (which don't increment generation on
// CRDs with status subresource) are meaningful triggers for DNS sync.
func (r *CloudflareDNSReconciler) SetupWithManager(mgr ctrl.Manager) error {
	log := mgr.GetLogger().WithName("controller").WithName("dns")
	log.Info("registering controller with manager")

	r.APIReader = mgr.GetAPIReader()

	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &cfgatev1alpha1.CloudflareDNS{}, dnsTunnelRefNameIndex, extractDNSTunnelRefName); err != nil {
		return fmt.Errorf("failed to create tunnelRef.name index: %w", err)
	}

	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &cfgatev1alpha1.CloudflareDNS{}, dnsGatewayRoutesEnabledIndex, extractDNSGatewayRoutesEnabled); err != nil {
		return fmt.Errorf("failed to create gatewayRoutes.enabled index: %w", err)
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&cfgatev1alpha1.CloudflareDNS{},
			builder.WithPredicates(GenerationOrDeletionPredicate),
		).
		Watches(
			&cfgatev1alpha1.CloudflareTunnel{},
			handler.EnqueueRequestsFromMapFunc(r.findAffectedDNSByTunnel),
			builder.WithPredicates(predicate.GenerationChangedPredicate{}),
		).
		Watches(
			&gateway.HTTPRoute{},
			handler.EnqueueRequestsFromMapFunc(r.findAffectedDNSByRoute),
			builder.WithPredicates(CfgateAnnotationOrGenerationPredicate),
		).
		Watches(
			&gateway.Gateway{},
			handler.EnqueueRequestsFromMapFunc(r.findAffectedDNSByGateway),
			builder.WithPredicates(CfgateAnnotationOrGenerationPredicate, GatewayCreateAnnotationFilter),
		).
		Complete(r)
}

// findAffectedDNSByTunnel finds all CloudflareDNS resources that reference
// the given CloudflareTunnel via spec.tunnelRef using a field index.
func (r *CloudflareDNSReconciler) findAffectedDNSByTunnel(ctx context.Context, obj client.Object) []reconcile.Request {
	logger := log.FromContext(ctx).WithName("controller").WithName("dns")

	tunnel, ok := obj.(*cfgatev1alpha1.CloudflareTunnel)
	if !ok {
		return nil
	}

	var dnsList cfgatev1alpha1.CloudflareDNSList
	if err := r.List(ctx, &dnsList, client.MatchingFields{
		dnsTunnelRefNameIndex: tunnel.Namespace + "/" + tunnel.Name,
	}); err != nil {
		logger.Error(err, "failed to list CloudflareDNS resources by tunnel index")
		return nil
	}

	requests := make([]reconcile.Request, 0, len(dnsList.Items))
	for _, dns := range dnsList.Items {
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      dns.Name,
				Namespace: dns.Namespace,
			},
		})
	}

	if len(requests) > 0 {
		logger.Info("CloudflareTunnel change triggering DNS reconciliation",
			"tunnel", tunnel.Namespace+"/"+tunnel.Name,
			"affectedDNS", len(requests),
		)
	}

	return requests
}

// findAffectedDNSByRoute finds all CloudflareDNS resources that may be affected
// by a change to an HTTPRoute using a field index on gatewayRoutes.enabled.
func (r *CloudflareDNSReconciler) findAffectedDNSByRoute(ctx context.Context, obj client.Object) []reconcile.Request {
	logger := log.FromContext(ctx).WithName("controller").WithName("dns")

	var dnsList cfgatev1alpha1.CloudflareDNSList
	if err := r.List(ctx, &dnsList, client.MatchingFields{
		dnsGatewayRoutesEnabledIndex: "true",
	}); err != nil {
		logger.Error(err, "failed to list CloudflareDNS resources by gateway routes index")
		return nil
	}

	requests := make([]reconcile.Request, 0, len(dnsList.Items))
	for _, dns := range dnsList.Items {
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      dns.Name,
				Namespace: dns.Namespace,
			},
		})
	}

	if len(requests) > 0 {
		logger.Info("HTTPRoute change triggering DNS reconciliation",
			"route", obj.GetNamespace()+"/"+obj.GetName(),
			"affectedDNS", len(requests),
		)
	}

	return requests
}

// findAffectedDNSByGateway finds all CloudflareDNS resources that may be affected
// by a change to a Gateway using a field index on gatewayRoutes.enabled.
func (r *CloudflareDNSReconciler) findAffectedDNSByGateway(ctx context.Context, obj client.Object) []reconcile.Request {
	logger := log.FromContext(ctx).WithName("controller").WithName("dns")

	var dnsList cfgatev1alpha1.CloudflareDNSList
	if err := r.List(ctx, &dnsList, client.MatchingFields{
		dnsGatewayRoutesEnabledIndex: "true",
	}); err != nil {
		logger.Error(err, "failed to list CloudflareDNS resources by gateway routes index")
		return nil
	}

	requests := make([]reconcile.Request, 0, len(dnsList.Items))
	for _, dns := range dnsList.Items {
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      dns.Name,
				Namespace: dns.Namespace,
			},
		})
	}

	if len(requests) > 0 {
		logger.Info("Gateway change triggering DNS reconciliation",
			"gateway", obj.GetNamespace()+"/"+obj.GetName(),
			"affectedDNS", len(requests),
		)
	}

	return requests
}

// resolveTarget resolves the DNS record target value.
// Returns the target domain or IP, the resolved tunnel (if tunnelRef), and any error.
func (r *CloudflareDNSReconciler) resolveTarget(ctx context.Context, dns *cfgatev1alpha1.CloudflareDNS) (string, *cfgatev1alpha1.CloudflareTunnel, error) {
	// 1. Tunnel reference mode
	if dns.Spec.TunnelRef != nil {
		tunnel, err := r.resolveTunnel(ctx, dns)
		if err != nil {
			return "", nil, err
		}
		if tunnel.Status.TunnelDomain == "" {
			return "", tunnel, fmt.Errorf("tunnel %s not ready (no tunnelDomain)", tunnel.Name)
		}
		return tunnel.Status.TunnelDomain, tunnel, nil
	}

	// 2. External target mode
	if dns.Spec.ExternalTarget != nil {
		return dns.Spec.ExternalTarget.Value, nil, nil
	}

	// CEL validation should prevent this
	return "", nil, fmt.Errorf("neither tunnelRef nor externalTarget specified")
}

// resolveTunnel resolves the referenced CloudflareTunnel.
func (r *CloudflareDNSReconciler) resolveTunnel(ctx context.Context, dns *cfgatev1alpha1.CloudflareDNS) (*cfgatev1alpha1.CloudflareTunnel, error) {
	namespace := dns.Spec.TunnelRef.Namespace
	if namespace == "" {
		namespace = dns.Namespace
	}

	var tunnel cfgatev1alpha1.CloudflareTunnel
	if err := r.Get(ctx, types.NamespacedName{
		Name:      dns.Spec.TunnelRef.Name,
		Namespace: namespace,
	}, &tunnel); err != nil {
		return nil, fmt.Errorf("failed to get tunnel %s/%s: %w", namespace, dns.Spec.TunnelRef.Name, err)
	}

	return &tunnel, nil
}

// collectHostnames collects hostnames from Gateway API routes and explicit configuration.
// Returns a map of hostname -> HostnameConfig for per-hostname DNS settings.
func (r *CloudflareDNSReconciler) collectHostnames(ctx context.Context, dns *cfgatev1alpha1.CloudflareDNS, tunnel *cfgatev1alpha1.CloudflareTunnel) (map[string]HostnameConfig, error) {
	hostnames := make(map[string]HostnameConfig)

	// Determine record type at resource level
	recordType := "CNAME"
	if dns.Spec.ExternalTarget != nil && dns.Spec.ExternalTarget.Type != "" {
		recordType = string(dns.Spec.ExternalTarget.Type)
	}

	// Collect from Gateway routes first.
	if dns.Spec.Source.GatewayRoutes != nil && dns.Spec.Source.GatewayRoutes.Enabled {
		routeHostnames, err := r.collectHostnamesFromRoutes(ctx, dns, tunnel)
		if err != nil {
			return nil, err
		}
		for h, config := range routeHostnames {
			config.RecordType = recordType
			hostnames[h] = config
		}
	}

	// Overlay explicit hostnames last so user-authored config wins on collisions.
	for _, explicit := range dns.Spec.Source.Explicit {
		hostnames[explicit.Hostname] = HostnameConfig{
			Target:     resolveExplicitHostnameTarget(dns, tunnel, explicit),
			TTL:        explicit.TTL,
			Proxied:    explicit.Proxied,
			RecordType: recordType,
		}
	}

	return hostnames, nil
}

func resolveExplicitHostnameTarget(dns *cfgatev1alpha1.CloudflareDNS, tunnel *cfgatev1alpha1.CloudflareTunnel, explicit cfgatev1alpha1.DNSExplicitHostname) string {
	if explicit.Target == "" {
		switch {
		case tunnel != nil && tunnel.Status.TunnelDomain != "":
			return tunnel.Status.TunnelDomain
		case dns.Spec.ExternalTarget != nil:
			return dns.Spec.ExternalTarget.Value
		default:
			return ""
		}
	}

	if dns.Spec.TunnelRef == nil || tunnel == nil || tunnel.Status.TunnelDomain == "" {
		return explicit.Target
	}

	target := explicit.Target
	target = strings.ReplaceAll(target, "{{ .TunnelDomain }}", tunnel.Status.TunnelDomain)
	target = strings.ReplaceAll(target, "{{.TunnelDomain}}", tunnel.Status.TunnelDomain)
	return target
}

// resolveSelectedNamespaces returns the set of namespace names that match the given selector.
// It uses union semantics: namespaces matching matchLabels (all labels must match) OR appearing
// in matchNames are included. Reads via APIReader for consistency with uncached reads used
// elsewhere in hostname collection.
func (r *CloudflareDNSReconciler) resolveSelectedNamespaces(ctx context.Context, selector *cfgatev1alpha1.DNSNamespaceSelector) (map[string]bool, error) {
	// Empty selector matches all namespaces (Kubernetes convention).
	if len(selector.MatchLabels) == 0 && len(selector.MatchNames) == 0 {
		return nil, nil
	}

	var namespaceList corev1.NamespaceList
	if err := r.APIReader.List(ctx, &namespaceList); err != nil {
		return nil, fmt.Errorf("failed to list namespaces: %w", err)
	}

	allowed := make(map[string]bool)

	for i := range namespaceList.Items {
		ns := &namespaceList.Items[i]

		if len(selector.MatchLabels) > 0 {
			allMatch := true
			for k, v := range selector.MatchLabels {
				if ns.Labels[k] != v {
					allMatch = false
					break
				}
			}
			if allMatch {
				allowed[ns.Name] = true
				continue
			}
		}

		if len(selector.MatchNames) > 0 {
			for _, name := range selector.MatchNames {
				if ns.Name == name {
					allowed[ns.Name] = true
					break
				}
			}
		}
	}

	return allowed, nil
}

// collectHostnamesFromRoutes collects hostnames from HTTPRoutes that reference
// Gateways associated with the given tunnel. Returns a map of hostname to
// HostnameConfig with per-route annotation settings.
func (r *CloudflareDNSReconciler) collectHostnamesFromRoutes(ctx context.Context, dns *cfgatev1alpha1.CloudflareDNS, tunnel *cfgatev1alpha1.CloudflareTunnel) (map[string]HostnameConfig, error) {
	if tunnel == nil {
		return nil, nil
	}
	if dns.Spec.Source.GatewayRoutes == nil {
		return nil, nil
	}

	gatewayRoutes := dns.Spec.Source.GatewayRoutes
	var allowedNamespaces map[string]bool
	if gatewayRoutes.NamespaceSelector != nil {
		var err error
		allowedNamespaces, err = r.resolveSelectedNamespaces(ctx, gatewayRoutes.NamespaceSelector)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve namespace selector: %w", err)
		}
	}

	hostnames := make(map[string]HostnameConfig)

	var gateways gateway.GatewayList
	if err := r.APIReader.List(ctx, &gateways); err != nil {
		return nil, fmt.Errorf("failed to list gateways: %w", err)
	}

	var relevantGateways []gateway.Gateway

	for _, gw := range gateways.Items {
		ref := annotations.GetAnnotation(&gw, annotations.AnnotationTunnelRef)
		if ref == "" {
			continue
		}
		ns, name, err := annotations.ParseNamespacedName(ref, gw.Namespace)
		if err != nil {
			continue
		}
		if name == tunnel.Name && ns == tunnel.Namespace {
			relevantGateways = append(relevantGateways, gw)
		}
	}

	if len(relevantGateways) == 0 {
		return hostnames, nil
	}

	// List all HTTPRoutes once, then filter per gateway in memory
	var routes gateway.HTTPRouteList
	if err := r.APIReader.List(ctx, &routes); err != nil {
		return nil, fmt.Errorf("failed to list httproutes: %w", err)
	}

	for _, gw := range relevantGateways {
		for _, route := range routes.Items {
			if allowedNamespaces != nil && !allowedNamespaces[route.Namespace] {
				continue
			}
			if filter := gatewayRoutes.AnnotationFilter; filter != "" {
				if parts := strings.SplitN(filter, "=", 2); len(parts) == 2 {
					if val, ok := route.Annotations[parts[0]]; !ok || val != parts[1] {
						continue
					}
				} else {
					if _, ok := route.Annotations[filter]; !ok {
						continue
					}
				}
			}

			for _, parentRef := range route.Spec.ParentRefs {
				if !isGatewayParentRef(parentRef) {
					continue
				}

				parentNS := route.Namespace
				if parentRef.Namespace != nil {
					parentNS = string(*parentRef.Namespace)
				}

				if string(parentRef.Name) == gw.Name && parentNS == gw.Namespace {
					dnsConfig := annotations.ParseDNSConfig(&route)
					config := HostnameConfig{
						TTL: int32(dnsConfig.TTL),
					}
					if proxiedStr := annotations.GetAnnotation(&route, annotations.AnnotationCloudflareProxied); proxiedStr != "" {
						proxied := dnsConfig.Proxied
						config.Proxied = &proxied
					}

					for _, h := range route.Spec.Hostnames {
						hostnames[string(h)] = config
					}
				}
			}
		}
	}

	return hostnames, nil
}

// isGatewayParentRef returns true if the parentRef targets a Gateway resource.
// Unset Group defaults to gateway.networking.k8s.io, unset Kind defaults to Gateway,
// per Gateway API spec.
func isGatewayParentRef(ref gateway.ParentReference) bool {
	if ref.Group != nil && string(*ref.Group) != gateway.GroupName {
		return false
	}
	if ref.Kind != nil && string(*ref.Kind) != "Gateway" {
		return false
	}
	return true
}

// resolveZones resolves zone names to zone IDs.
// Uses cached IDs if provided, otherwise looks up via API.
func (r *CloudflareDNSReconciler) resolveZones(ctx context.Context, dns *cfgatev1alpha1.CloudflareDNS, dnsService *cloudflare.DNSService) (map[string]string, error) {
	zones := make(map[string]string)

	for _, zoneConfig := range dns.Spec.Zones {
		if zoneConfig.ID != "" {
			// Use cached ID
			zones[zoneConfig.Name] = zoneConfig.ID
		} else {
			// Look up zone
			zone, err := dnsService.ResolveZone(ctx, zoneConfig.Name)
			if err != nil {
				return nil, fmt.Errorf("failed to resolve zone %s: %w", zoneConfig.Name, err)
			}
			if zone == nil {
				return nil, fmt.Errorf("zone %s not found", zoneConfig.Name)
			}
			zones[zoneConfig.Name] = zone.ID
		}
	}

	return zones, nil
}

// syncRecords syncs DNS records to Cloudflare.
// Compares desired state with actual state and applies changes respecting policy.
// The hostnameConfigs map provides the effective per-hostname target, TTL,
// proxied, and record type after source merging.
func (r *CloudflareDNSReconciler) syncRecords(ctx context.Context, dns *cfgatev1alpha1.CloudflareDNS, target string, hostnameConfigs map[string]HostnameConfig, zones map[string]string, dnsService *cloudflare.DNSService) error {
	logger := log.FromContext(ctx).WithName("controller").WithName("dns")

	ownershipPrefix := dns.Spec.Ownership.TXTRecord.Prefix
	if ownershipPrefix == "" {
		ownershipPrefix = dnsDefaultOwnershipPrefix
	}

	ownerID := dns.Spec.Ownership.OwnerID
	if ownerID == "" {
		ownerID = fmt.Sprintf("%s/%s", dns.Namespace, dns.Name)
	}

	policy := cloudflare.DNSPolicy(dns.Spec.Policy)
	if policy == "" {
		policy = cloudflare.PolicySync
	}

	var recordStatuses []cfgatev1alpha1.DNSRecordSyncStatus
	var syncedCount, pendingCount, failedCount int32

	for hostname, hostnameConfig := range hostnameConfigs {
		recordType := hostnameConfig.RecordType
		if recordType == "" {
			recordType = "CNAME"
		}

		// Determine zone for this hostname
		zoneName := cloudflare.ExtractZoneFromHostname(hostname)
		zoneID, ok := zones[zoneName]
		if !ok {
			logger.Info("zone not configured for hostname",
				"hostname", hostname,
				"zone", zoneName,
			)
			recordStatuses = append(recordStatuses, cfgatev1alpha1.DNSRecordSyncStatus{
				Hostname: hostname,
				Type:     recordType,
				Status:   "Failed",
				Error:    fmt.Sprintf("zone %s not configured", zoneName),
			})
			failedCount++
			continue
		}

		// Get TTL and proxied settings from defaults
		ttl := dns.Spec.Defaults.TTL
		if ttl == 0 {
			ttl = 1 // auto
		}
		proxied := dns.Spec.Defaults.Proxied
		recordTarget := target
		if hostnameConfig.Target != "" {
			recordTarget = hostnameConfig.Target
		}
		if hostnameConfig.TTL != 0 {
			ttl = hostnameConfig.TTL
		}
		if hostnameConfig.Proxied != nil {
			proxied = *hostnameConfig.Proxied
		}

		comment := "managed by cfgate"
		desired := cloudflare.BuildDNSRecord(hostname, recordTarget, recordType, proxied, int(ttl), comment)

		// Warn on deep subdomains unless suppressed by annotation
		if cloudflare.ValidateHostnameDepth(hostname, zoneName) {
			if dns.Annotations[annotations.AnnotationAllowDeepSubdomains] != "true" {
				r.Recorder.Eventf(dns, nil, corev1.EventTypeWarning, "DeepSubdomain", "Sync",
					"hostname %s has multiple subdomain levels; Cloudflare Universal SSL may not cover it. Set annotation %s=true to suppress this warning",
					hostname, annotations.AnnotationAllowDeepSubdomains)
			}
		}

		// Sync record using policy
		record, modified, err := dnsService.SyncRecordWithPolicy(ctx, zoneID, desired, ownerID, policy)
		if err != nil {
			logger.Error(err, "failed to sync DNS record", "hostname", hostname)
			recordStatuses = append(recordStatuses, cfgatev1alpha1.DNSRecordSyncStatus{
				Hostname: hostname,
				Type:     recordType,
				Status:   "Failed",
				Error:    err.Error(),
			})
			failedCount++
			continue
		}

		// Create ownership TXT record if enabled
		if r.shouldCreateTXTRecords(dns) {
			ownershipParams := cloudflare.OwnershipParams{
				Hostname: hostname,
				OwnerID:  ownerID,
				Resource: fmt.Sprintf("CloudflareDNS/%s/%s", dns.Namespace, dns.Name),
				Prefix:   ownershipPrefix,
			}
			if err := dnsService.CreateOwnershipRecord(ctx, zoneID, ownershipParams); err != nil {
				// Non-fatal: ownership records are supplementary, don't fail sync
				logger.V(1).Info("ownership record sync issue",
					"hostname", hostname,
					"error", err.Error(),
				)
			}
		}

		recordStatus := "Synced"
		if modified {
			logger.Info("DNS record modified",
				"hostname", hostname,
				"recordID", record.ID,
			)
			r.Recorder.Eventf(dns, nil, corev1.EventTypeNormal, "RecordSynced", "Sync", "DNS record synced: %s", hostname)
		}

		recordStatuses = append(recordStatuses, cfgatev1alpha1.DNSRecordSyncStatus{
			Hostname: hostname,
			Type:     record.Type,
			Target:   record.Content,
			Proxied:  record.Proxied,
			TTL:      int32(record.TTL),
			Status:   recordStatus,
			RecordID: record.ID,
			ZoneID:   zoneID,
		})
		syncedCount++
	}

	// Delete orphaned records (previously synced but no longer wanted)
	// Only if policy allows deletion
	if policy == cloudflare.PolicySync {
		if err := r.deleteOrphanedRecords(ctx, dns, hostnameKeys(hostnameConfigs), zones, dnsService, ownerID, ownershipPrefix); err != nil {
			logger.Error(err, "failed to delete orphaned records")
			// Non-fatal, continue
		}
	}

	// Update status
	dns.Status.Records = recordStatuses
	dns.Status.SyncedRecords = syncedCount
	dns.Status.PendingRecords = pendingCount
	dns.Status.FailedRecords = failedCount

	return nil
}

// deleteOrphanedRecords deletes records that were previously synced but are no longer wanted.
// Respects CleanupPolicy.DeleteOnRouteRemoval — when explicitly set to false, orphaned
// records are retained even if their source routes are removed.
func (r *CloudflareDNSReconciler) deleteOrphanedRecords(ctx context.Context, dns *cfgatev1alpha1.CloudflareDNS, hostnames []string, zones map[string]string, dnsService *cloudflare.DNSService, ownerID, ownershipPrefix string) error {
	logger := log.FromContext(ctx).WithName("controller").WithName("dns")

	// Check DeleteOnRouteRemoval policy (nil defaults to true)
	if dns.Spec.CleanupPolicy.DeleteOnRouteRemoval != nil && !*dns.Spec.CleanupPolicy.DeleteOnRouteRemoval {
		logger.V(1).Info("skipping orphaned record deletion, deleteOnRouteRemoval is disabled")
		return nil
	}

	for _, prevRecord := range dns.Status.Records {
		found := false
		for _, hostname := range hostnames {
			if prevRecord.Hostname == hostname {
				found = true
				break
			}
		}
		if !found && prevRecord.RecordID != "" {
			// This record was previously synced but hostname is no longer wanted
			zoneName := cloudflare.ExtractZoneFromHostname(prevRecord.Hostname)
			zoneID := prevRecord.ZoneID
			if zoneID == "" {
				var ok bool
				zoneID, ok = zones[zoneName]
				if !ok {
					continue
				}
			}

			deleted, err := r.deleteManagedStatusRecord(ctx, dns, dnsService, zoneID, prevRecord, ownerID, ownershipPrefix)
			if err != nil {
				logger.Error(err, "failed to delete orphaned DNS record",
					"hostname", prevRecord.Hostname,
					"recordID", prevRecord.RecordID,
				)
				continue
			}
			if deleted {
				logger.Info("deleted orphaned DNS record",
					"hostname", prevRecord.Hostname,
					"recordID", prevRecord.RecordID,
				)
				r.Recorder.Eventf(dns, nil, corev1.EventTypeNormal, "RecordDeleted", "Delete", "DNS record deleted: %s", prevRecord.Hostname)
			}
		}
	}

	return nil
}

func (r *CloudflareDNSReconciler) deleteManagedStatusRecord(ctx context.Context, dns *cfgatev1alpha1.CloudflareDNS, dnsService *cloudflare.DNSService, zoneID string, statusRecord cfgatev1alpha1.DNSRecordSyncStatus, ownerID, ownershipPrefix string) (bool, error) {
	logger := log.FromContext(ctx).WithName("controller").WithName("dns")

	existingRecord, err := dnsService.FindRecordByName(ctx, zoneID, statusRecord.Hostname, statusRecord.Type)
	if err != nil {
		return false, fmt.Errorf("find current record: %w", err)
	}

	txtName := fmt.Sprintf("%s.%s", ownershipPrefix, statusRecord.Hostname)
	txtRecord, err := dnsService.FindRecordByName(ctx, zoneID, txtName, "TXT")
	if err != nil {
		return false, fmt.Errorf("find ownership record: %w", err)
	}
	if txtRecord != nil && ownerID != "" && !cloudflare.IsOwnedByCfgate(txtRecord, ownerID) {
		logger.Info("skipping DNS cleanup because ownership TXT belongs to a different resource",
			"hostname", statusRecord.Hostname,
			"recordID", statusRecord.RecordID,
		)
		return false, nil
	}

	if existingRecord != nil && statusRecord.RecordID != "" && existingRecord.ID != statusRecord.RecordID {
		logger.Info("skipping DNS cleanup because record identity changed",
			"hostname", statusRecord.Hostname,
			"expectedRecordID", statusRecord.RecordID,
			"currentRecordID", existingRecord.ID,
		)
		return false, nil
	}
	if existingRecord != nil && statusRecord.RecordID == "" {
		logger.Info("skipping DNS cleanup because status record ID is missing",
			"hostname", statusRecord.Hostname,
			"type", statusRecord.Type,
		)
		return false, nil
	}

	deleted := false
	if statusRecord.RecordID != "" {
		if err := dnsService.DeleteRecord(ctx, zoneID, statusRecord.RecordID); err != nil {
			return false, fmt.Errorf("delete dns record: %w", err)
		}
		deleted = existingRecord != nil
	}

	if r.shouldCreateTXTRecords(dns) {
		if err := dnsService.DeleteOwnershipRecord(ctx, zoneID, statusRecord.Hostname, ownershipPrefix, ownerID); err != nil {
			return deleted, fmt.Errorf("delete ownership record: %w", err)
		}
	}

	return deleted, nil
}

// verifyOwnership verifies that TXT ownership records exist for all managed hostnames.
func (r *CloudflareDNSReconciler) verifyOwnership(ctx context.Context, dns *cfgatev1alpha1.CloudflareDNS, zones map[string]string, hostnames []string, dnsService *cloudflare.DNSService) (bool, error) {
	if !r.shouldCreateTXTRecords(dns) {
		return false, nil // Ownership tracking disabled
	}

	ownershipPrefix := dns.Spec.Ownership.TXTRecord.Prefix
	if ownershipPrefix == "" {
		ownershipPrefix = dnsDefaultOwnershipPrefix
	}

	ownerID := dns.Spec.Ownership.OwnerID
	if ownerID == "" {
		ownerID = fmt.Sprintf("%s/%s", dns.Namespace, dns.Name)
	}

	for _, hostname := range hostnames {
		zoneName := cloudflare.ExtractZoneFromHostname(hostname)
		zoneID, ok := zones[zoneName]
		if !ok {
			continue
		}

		txtName := fmt.Sprintf("%s.%s", ownershipPrefix, hostname)
		record, err := dnsService.FindRecordByName(ctx, zoneID, txtName, "TXT")
		if err != nil {
			return false, fmt.Errorf("failed to verify ownership for %s: %w", hostname, err)
		}

		if record == nil {
			return false, fmt.Errorf("ownership record missing for %s", hostname)
		}

		// Check ownership content matches
		if !cloudflare.IsOwnedByCfgate(record, ownerID) {
			return false, fmt.Errorf("ownership conflict for %s: owned by different controller", hostname)
		}
	}

	return true, nil
}

// shouldCreateTXTRecords reports whether TXT ownership tracking is enabled
// (defaults to true if not explicitly disabled).
func (r *CloudflareDNSReconciler) shouldCreateTXTRecords(dns *cfgatev1alpha1.CloudflareDNS) bool {
	if dns.Spec.Ownership.TXTRecord.Enabled == nil {
		return true // default enabled
	}
	return *dns.Spec.Ownership.TXTRecord.Enabled
}

// reconcileDelete handles deletion of CloudflareDNS, cleaning up DNS records
// from Cloudflare if policy allows. Cleanup failure blocks finalizer removal
// and requeues. Set cfgate.io/deletion-policy=orphan to skip cleanup and allow
// deletion to proceed. Uses fallback credentials if the tunnel's credentials
// are unavailable.
func (r *CloudflareDNSReconciler) reconcileDelete(ctx context.Context, dns *cfgatev1alpha1.CloudflareDNS) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithName("controller").WithName("dns")
	logger.Info("handling DNS deletion",
		"name", dns.Name,
	)

	if !controllerutil.ContainsFinalizer(dns, dnsFinalizer) {
		return ctrl.Result{}, nil
	}

	if dns.Annotations["cfgate.io/deletion-policy"] == "orphan" {
		logger.Info("deletion policy is orphan, skipping DNS cleanup")
		return r.removeDNSFinalizer(ctx, dns)
	}

	// Only delete records if policy is "sync" AND CleanupPolicy.DeleteOnResourceRemoval is true (or nil, defaulting to true).
	// upsert-only and create-only policies never delete records.
	shouldDeleteOnResourceRemoval := dns.Spec.CleanupPolicy.DeleteOnResourceRemoval == nil || *dns.Spec.CleanupPolicy.DeleteOnResourceRemoval
	if dns.Spec.Policy == cfgatev1alpha1.DNSPolicySync && shouldDeleteOnResourceRemoval {
		if err := r.cleanupRecordsWithFallback(ctx, dns); err != nil {
			retryElapsed := time.Since(dns.DeletionTimestamp.Time)
			if retryElapsed < dnsDeletionRetryBudget {
				logger.Error(err, "failed to cleanup DNS records, will retry",
					"retryElapsed", retryElapsed.Round(time.Second),
					"retryBudget", dnsDeletionRetryBudget)
				r.Recorder.Eventf(dns, nil, corev1.EventTypeWarning, "CleanupFailed", "Delete",
					"Failed to delete DNS records: %v. Set annotation cfgate.io/deletion-policy=orphan to skip cleanup and remove finalizer.", err)
				return ctrl.Result{RequeueAfter: dnsDeletionRequeueInterval}, nil
			}
			logger.Error(err, "retry budget exhausted, cleanup still blocked",
				"retryElapsed", retryElapsed.Round(time.Second))
			r.Recorder.Eventf(dns, nil, corev1.EventTypeWarning, "CleanupBlocked", "Delete",
				"DNS record deletion blocked after %s of retries: %v. Set annotation cfgate.io/deletion-policy=orphan to skip cleanup and remove finalizer.",
				retryElapsed.Round(time.Second), err)
			return ctrl.Result{RequeueAfter: dnsDeletionRequeueInterval}, nil
		}
	} else if dns.Spec.Policy != cfgatev1alpha1.DNSPolicySync {
		logger.Info("skipping DNS cleanup due to policy", "policy", dns.Spec.Policy)
	} else if !shouldDeleteOnResourceRemoval {
		logger.Info("skipping DNS cleanup, deleteOnResourceRemoval is disabled")
	}

	return r.removeDNSFinalizer(ctx, dns)
}

// removeDNSFinalizer removes the DNS finalizer using a patch to reduce lock contention.
func (r *CloudflareDNSReconciler) removeDNSFinalizer(ctx context.Context, dns *cfgatev1alpha1.CloudflareDNS) (ctrl.Result, error) {
	patch := client.MergeFrom(dns.DeepCopy())
	controllerutil.RemoveFinalizer(dns, dnsFinalizer)
	if err := r.Patch(ctx, dns, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to remove finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}

// updateStatus updates the CloudflareDNS status only if it has changed.
// This avoids unnecessary API calls and prevents watch events from status-only updates.
func (r *CloudflareDNSReconciler) updateStatus(ctx context.Context, dns *cfgatev1alpha1.CloudflareDNS) error {
	// Re-fetch to avoid conflicts
	var current cfgatev1alpha1.CloudflareDNS
	if err := r.Get(ctx, types.NamespacedName{Name: dns.Name, Namespace: dns.Namespace}, &current); err != nil {
		return fmt.Errorf("failed to re-fetch DNS: %w", err)
	}

	// Check if status actually changed (excluding LastSyncTime which always changes)
	if dnsStatusEqual(&current.Status, &dns.Status) {
		return nil // No update needed
	}

	// Copy status
	current.Status = dns.Status

	if err := r.Status().Update(ctx, &current); err != nil {
		return fmt.Errorf("failed to update status: %w", err)
	}

	return nil
}

// dnsStatusEqual compares two CloudflareDNS statuses for equality, ignoring
// LastSyncTime which changes on every reconciliation to avoid spurious updates.
func dnsStatusEqual(a, b *cfgatev1alpha1.CloudflareDNSStatus) bool {
	// Compare generation
	if a.ObservedGeneration != b.ObservedGeneration {
		return false
	}

	// Compare resolved target
	if a.ResolvedTarget != b.ResolvedTarget {
		return false
	}

	// Compare record counts
	if a.SyncedRecords != b.SyncedRecords ||
		a.PendingRecords != b.PendingRecords ||
		a.FailedRecords != b.FailedRecords {
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

	// Compare records
	if !reflect.DeepEqual(a.Records, b.Records) {
		return false
	}

	return true
}

// getCloudflareClient creates or returns the Cloudflare client.
// Uses credential cache to avoid repeated API validations.
func (r *CloudflareDNSReconciler) getCloudflareClient(ctx context.Context, dns *cfgatev1alpha1.CloudflareDNS, tunnel *cfgatev1alpha1.CloudflareTunnel) (cloudflare.Client, error) {
	// If injected client exists, use it (for testing)
	if r.CFClient != nil {
		return r.CFClient, nil
	}

	// If tunnel is available, use tunnel's credentials
	if tunnel != nil {
		return r.getClientFromTunnel(ctx, tunnel)
	}

	// Otherwise, use DNS resource's own credentials (external target mode)
	if dns.Spec.Cloudflare == nil || dns.Spec.Cloudflare.SecretRef.Name == "" {
		return nil, fmt.Errorf("cloudflare credentials required for external target mode")
	}

	secretNamespace := dns.Spec.Cloudflare.SecretRef.Namespace
	if secretNamespace == "" {
		secretNamespace = dns.Namespace
	}

	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      dns.Spec.Cloudflare.SecretRef.Name,
		Namespace: secretNamespace,
	}, secret); err != nil {
		return nil, fmt.Errorf("failed to get credentials secret: %w", err)
	}

	tokenKey := "CLOUDFLARE_API_TOKEN"
	if dns.Spec.Cloudflare.SecretKeys.APIToken != "" {
		tokenKey = dns.Spec.Cloudflare.SecretKeys.APIToken
	}

	// Use cache if available
	if r.CredentialCache != nil {
		return r.CredentialCache.GetOrCreate(ctx, secret, func() (cloudflare.Client, error) {
			return r.createClientFromSecret(secret, tokenKey)
		})
	}

	return r.createClientFromSecret(secret, tokenKey)
}

// getClientFromTunnel creates a Cloudflare client from tunnel credentials.
func (r *CloudflareDNSReconciler) getClientFromTunnel(ctx context.Context, tunnel *cfgatev1alpha1.CloudflareTunnel) (cloudflare.Client, error) {
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

	tokenKey := tunnel.Spec.Cloudflare.SecretKeys.APIToken
	if tokenKey == "" {
		tokenKey = "CLOUDFLARE_API_TOKEN"
	}

	// Use cache if available
	if r.CredentialCache != nil {
		return r.CredentialCache.GetOrCreate(ctx, secret, func() (cloudflare.Client, error) {
			return r.createClientFromSecret(secret, tokenKey)
		})
	}

	return r.createClientFromSecret(secret, tokenKey)
}

// createClientFromSecret creates a Cloudflare client from a secret.
func (r *CloudflareDNSReconciler) createClientFromSecret(secret *corev1.Secret, tokenKey string) (cloudflare.Client, error) {
	if tokenKey == "" {
		tokenKey = "CLOUDFLARE_API_TOKEN"
	}

	token, ok := secret.Data[tokenKey]
	if !ok {
		return nil, fmt.Errorf("API token key %q not found in secret", tokenKey)
	}

	return cloudflare.NewClient(string(token))
}

// getCloudflareClientWithFallback tries tunnel credentials, then fallback credentials.
// Used during deletion when the tunnel or its secret may have been deleted.
func (r *CloudflareDNSReconciler) getCloudflareClientWithFallback(ctx context.Context, dns *cfgatev1alpha1.CloudflareDNS) (cloudflare.Client, error) {
	logger := log.FromContext(ctx).WithName("controller").WithName("dns")

	var tunnelErr, dnsOwnErr, fallbackErr error

	// Try tunnel credentials first (if tunnelRef is set)
	if dns.Spec.TunnelRef != nil {
		tunnel, err := r.resolveTunnel(ctx, dns)
		if err == nil {
			cfClient, err := r.getClientFromTunnel(ctx, tunnel)
			if err == nil {
				logger.V(1).Info("credential path resolved", "path", "tunnel", "tunnel", tunnel.Namespace+"/"+tunnel.Name)
				return cfClient, nil
			}
			tunnelErr = fmt.Errorf("secret unavailable: %w", err)
		} else {
			tunnelErr = fmt.Errorf("tunnel not found: %w", err)
		}
		logger.V(1).Info("tunnel credentials unavailable", "error", tunnelErr.Error())
	}

	// Try DNS resource's own credentials (external target mode)
	if dns.Spec.Cloudflare != nil && dns.Spec.Cloudflare.SecretRef.Name != "" {
		cfClient, err := r.getCloudflareClient(ctx, dns, nil)
		if err == nil {
			logger.V(1).Info("credential path resolved", "path", "dns-own")
			return cfClient, nil
		}
		dnsOwnErr = err
		logger.V(1).Info("dns credentials unavailable", "error", err.Error())
	}

	// Check if we have fallback credentials
	if dns.Spec.FallbackCredentialsRef == nil {
		return nil, fmt.Errorf("all credential paths failed: tunnel=[%v], dns-own=[%v], fallback=[not configured]", tunnelErr, dnsOwnErr)
	}

	logger.Info("using fallback credentials for DNS cleanup",
		"fallbackSecret", dns.Spec.FallbackCredentialsRef.Name,
		"fallbackNamespace", dns.Spec.FallbackCredentialsRef.Namespace,
	)

	// Try fallback credentials
	fallbackNamespace := dns.Spec.FallbackCredentialsRef.Namespace
	if fallbackNamespace == "" {
		fallbackNamespace = dns.Namespace
	}

	fallbackSecret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      dns.Spec.FallbackCredentialsRef.Name,
		Namespace: fallbackNamespace,
	}, fallbackSecret); err != nil {
		fallbackErr = fmt.Errorf("secret read failed: %w", err)
		return nil, fmt.Errorf("all credential paths failed: tunnel=[%v], dns-own=[%v], fallback=[%v]", tunnelErr, dnsOwnErr, fallbackErr)
	}

	token, ok := fallbackSecret.Data["CLOUDFLARE_API_TOKEN"]
	if !ok {
		fallbackErr = fmt.Errorf("CLOUDFLARE_API_TOKEN key missing in secret %s/%s", fallbackNamespace, dns.Spec.FallbackCredentialsRef.Name)
		return nil, fmt.Errorf("all credential paths failed: tunnel=[%v], dns-own=[%v], fallback=[%v]", tunnelErr, dnsOwnErr, fallbackErr)
	}

	logger.V(1).Info("credential path resolved", "path", "fallback", "secret", fallbackNamespace+"/"+dns.Spec.FallbackCredentialsRef.Name)
	return cloudflare.NewClient(string(token))
}

// cleanupRecordsWithFallback deletes managed DNS records using fallback credentials if needed.
// Returns an error if any record deletions failed, even though it attempts all deletions
// (continue-on-error). The caller uses this signal for accurate events/conditions.
func (r *CloudflareDNSReconciler) cleanupRecordsWithFallback(ctx context.Context, dns *cfgatev1alpha1.CloudflareDNS) error {
	logger := log.FromContext(ctx).WithName("controller").WithName("dns")

	// Get Cloudflare client (with fallback)
	cfClient, err := r.getCloudflareClientWithFallback(ctx, dns)
	if err != nil {
		return fmt.Errorf("failed to get Cloudflare client: %w", err)
	}

	dnsService := cloudflare.NewDNSService(cfClient, logger)

	ownerID := dns.Spec.Ownership.OwnerID
	if ownerID == "" {
		ownerID = fmt.Sprintf("%s/%s", dns.Namespace, dns.Name)
	}

	ownershipPrefix := dns.Spec.Ownership.TXTRecord.Prefix
	if ownershipPrefix == "" {
		ownershipPrefix = dnsDefaultOwnershipPrefix
	}

	if len(dns.Spec.Zones) == 0 && len(dns.Status.Records) == 0 {
		logger.Info("cleanup: no zones configured, nothing to clean", "ownerID", ownerID)
		return nil
	}

	logger.V(1).Info("cleanup: starting record deletion",
		"ownerID", ownerID,
		"zoneCount", len(dns.Spec.Zones),
	)

	var deleteErrors []string
	var totalDeleted int

	resolveZoneID := func(zoneName string) (string, error) {
		for _, zoneConfig := range dns.Spec.Zones {
			if zoneConfig.Name != zoneName {
				continue
			}
			if zoneConfig.ID != "" {
				return zoneConfig.ID, nil
			}
			zone, err := dnsService.ResolveZone(ctx, zoneName)
			if err != nil {
				return "", err
			}
			if zone == nil {
				return "", fmt.Errorf("zone %s not found", zoneName)
			}
			return zone.ID, nil
		}
		if zoneName == "" {
			return "", fmt.Errorf("zone name is empty")
		}
		zone, err := dnsService.ResolveZone(ctx, zoneName)
		if err != nil {
			return "", err
		}
		if zone == nil {
			return "", fmt.Errorf("zone %s not found", zoneName)
		}
		return zone.ID, nil
	}

	if len(dns.Status.Records) > 0 {
		logger.V(1).Info("cleanup: deleting records from status inventory",
			"recordCount", len(dns.Status.Records),
			"ownerID", ownerID,
		)
		for _, statusRecord := range dns.Status.Records {
			if statusRecord.Status == "Failed" && statusRecord.RecordID == "" && statusRecord.ZoneID == "" {
				logger.V(1).Info("cleanup: skipping failed status record with no materialized Cloudflare record",
					"hostname", statusRecord.Hostname,
					"type", statusRecord.Type,
				)
				continue
			}

			zoneID := statusRecord.ZoneID
			if zoneID == "" {
				zoneName := cloudflare.ExtractZoneFromHostname(statusRecord.Hostname)
				var err error
				zoneID, err = resolveZoneID(zoneName)
				if err != nil {
					logger.Error(err, "failed to resolve zone for status-backed cleanup", "hostname", statusRecord.Hostname)
					deleteErrors = append(deleteErrors, fmt.Sprintf("record %s: resolve failed: %v", statusRecord.Hostname, err))
					continue
				}
			}

			deleted, err := r.deleteManagedStatusRecord(ctx, dns, dnsService, zoneID, statusRecord, ownerID, ownershipPrefix)
			if err != nil {
				logger.Error(err, "failed to delete DNS record from status inventory",
					"record", statusRecord.Hostname,
					"recordID", statusRecord.RecordID,
					"type", statusRecord.Type,
				)
				deleteErrors = append(deleteErrors, fmt.Sprintf("record %s: %v", statusRecord.Hostname, err))
				continue
			}
			if deleted {
				totalDeleted++
				logger.Info("deleted DNS record",
					"record", statusRecord.Hostname,
					"recordID", statusRecord.RecordID,
					"type", statusRecord.Type,
				)
			}
		}
	}

	// For each zone, find and delete managed records
	if len(dns.Status.Records) == 0 {
		for _, zoneConfig := range dns.Spec.Zones {
			zoneID := zoneConfig.ID
			if zoneID == "" {
				zone, err := dnsService.ResolveZone(ctx, zoneConfig.Name)
				if err != nil {
					logger.Error(err, "failed to resolve zone for cleanup", "zone", zoneConfig.Name)
					deleteErrors = append(deleteErrors, fmt.Sprintf("zone %s: resolve failed: %v", zoneConfig.Name, err))
					continue
				}
				if zone == nil {
					logger.Info("zone not found for cleanup, skipping", "zone", zoneConfig.Name)
					continue
				}
				zoneID = zone.ID
				logger.V(1).Info("cleanup: zone resolved", "zone", zoneConfig.Name, "zoneID", zoneID)
			}

			// List managed records with TXT cross-reference to prevent
			// deleting records owned by other CloudflareDNS resources.
			records, err := dnsService.ListManagedRecords(ctx, zoneID, ownerID, ownershipPrefix)
			if err != nil {
				logger.Error(err, "failed to list managed records", "zone", zoneConfig.Name)
				deleteErrors = append(deleteErrors, fmt.Sprintf("zone %s: list failed: %v", zoneConfig.Name, err))
				continue
			}

			if len(records) == 0 {
				logger.Info("cleanup: no managed records found in zone",
					"zone", zoneConfig.Name,
					"ownerID", ownerID,
				)
				continue
			}

			logger.V(1).Info("cleanup: found managed records",
				"zone", zoneConfig.Name,
				"recordCount", len(records),
			)

			for _, record := range records {
				// OnlyManaged nil defaults to true (only delete managed records)
				onlyManaged := dns.Spec.CleanupPolicy.OnlyManaged == nil || *dns.Spec.CleanupPolicy.OnlyManaged
				if cloudflare.IsOwnedByCfgate(&record, ownerID) || !onlyManaged {
					if err := dnsService.DeleteRecord(ctx, zoneID, record.ID); err != nil {
						logger.Error(err, "failed to delete DNS record",
							"record", record.Name,
							"recordID", record.ID,
							"type", record.Type,
						)
						deleteErrors = append(deleteErrors, fmt.Sprintf("record %s: %v", record.Name, err))
					} else {
						totalDeleted++
						logger.Info("deleted DNS record",
							"record", record.Name,
							"recordID", record.ID,
							"type", record.Type,
						)
					}
				}
			}
		}
	}

	logger.V(1).Info("cleanup: finished",
		"totalDeleted", totalDeleted,
		"errors", len(deleteErrors),
	)

	if len(deleteErrors) > 0 {
		return fmt.Errorf("partial cleanup failure (%d errors): %s", len(deleteErrors), strings.Join(deleteErrors, "; "))
	}
	return nil
}

// setCondition sets a condition on the DNS status.
func (r *CloudflareDNSReconciler) setCondition(dns *cfgatev1alpha1.CloudflareDNS, conditionType string, condStatus metav1.ConditionStatus, reason, message string) {
	condition := metav1.Condition{
		Type:               conditionType,
		Status:             condStatus,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.Now(),
		ObservedGeneration: dns.Generation,
	}

	dns.Status.Conditions = status.MergeConditions(dns.Status.Conditions, condition)
}
