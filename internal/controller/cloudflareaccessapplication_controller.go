package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"sort"
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
	ctrlcontroller "sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1b1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/cloudflare"
	"cfgate.io/cfgate/internal/controller/annotations"
	"cfgate.io/cfgate/internal/controller/features"
	"cfgate.io/cfgate/internal/controller/status"
)

const accessApplicationTag = "cfgate"

// CloudflareAccessApplicationReconciler reconciles Gateway targets into Access Applications.
type CloudflareAccessApplicationReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder events.EventRecorder

	CFClient        cloudflare.Client
	CredentialCache *cloudflare.CredentialCache
	FeatureGates    *features.FeatureGates
}

type accessApplicationTarget struct {
	Ref       cfgatev1alpha1.PolicyTargetReference
	Kind      string
	Namespace string
	Name      string
	Host      string
	Path      string
	Domain    string
}

// +kubebuilder:rbac:groups=cfgate.io,resources=cloudflareaccessapplications,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cfgate.io,resources=cloudflareaccessapplications/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cfgate.io,resources=cloudflareaccessapplications/finalizers,verbs=update
// +kubebuilder:rbac:groups=cfgate.io,resources=cloudflareaccesspolicies,verbs=get;list;watch
// +kubebuilder:rbac:groups=cfgate.io,resources=cloudflaretunnels,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=referencegrants,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

func (r *CloudflareAccessApplicationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx).WithName("controller").WithName("accessapplication").
		WithValues("namespace", req.Namespace, "name", req.Name)

	var app cfgatev1alpha1.CloudflareAccessApplication
	if err := r.Get(ctx, req.NamespacedName, &app); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to get CloudflareAccessApplication: %w", err)
	}

	if !app.DeletionTimestamp.IsZero() {
		return r.reconcileApplicationDelete(ctx, &app)
	}

	if !controllerutil.ContainsFinalizer(&app, accessApplicationFinalizer) {
		patch := client.MergeFrom(app.DeepCopy())
		controllerutil.AddFinalizer(&app, accessApplicationFinalizer)
		if err := r.Patch(ctx, &app, patch); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to add finalizer: %w", err)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	targets, refGrantOK, err := r.resolveApplicationTargets(ctx, &app)
	if err != nil {
		log.Error(err, "failed to resolve access application targets")
		app.Status.Conditions = status.MergeConditions(app.Status.Conditions,
			status.NewTargetsResolvedCondition(false, targetResolutionReason(err), status.Error2ConditionMsg(err), app.Generation),
			status.NewCondition(status.ConditionTypeReferenceGrantValid, conditionStatus(refGrantOK), referenceGrantReason(refGrantOK), referenceGrantMessage(refGrantOK), app.Generation),
		)
		app.Status.Conditions = status.MergeConditions(app.Status.Conditions,
			status.NewAccessApplicationReadyCondition(app.Status.Conditions, app.Generation),
		)
		_ = r.updateApplicationStatus(ctx, &app)
		return ctrl.Result{RequeueAfter: accessApplicationRequeueAfterError}, nil
	}
	app.Status.Conditions = status.MergeConditions(app.Status.Conditions,
		status.NewTargetsResolvedCondition(true, status.ReasonTargetsResolved, "All targets resolved.", app.Generation),
		status.NewCondition(status.ConditionTypeReferenceGrantValid, metav1.ConditionTrue, status.ReasonResolved, "ReferenceGrants permit referenced resources.", app.Generation),
	)

	accessService, accountID, err := r.resolveApplicationCredentials(ctx, &app, targets)
	if err != nil {
		log.Error(err, "failed to resolve access application credentials")
		app.Status.Conditions = status.MergeConditions(app.Status.Conditions,
			status.NewCondition(status.ConditionTypeCredentialsValid, metav1.ConditionFalse, status.ReasonCredentialsInvalid, status.Error2ConditionMsg(err), app.Generation),
		)
		app.Status.Conditions = status.MergeConditions(app.Status.Conditions,
			status.NewAccessApplicationReadyCondition(app.Status.Conditions, app.Generation),
		)
		_ = r.updateApplicationStatus(ctx, &app)
		return ctrl.Result{RequeueAfter: accessApplicationRequeueAfterError}, nil
	}
	app.Status.Conditions = status.MergeConditions(app.Status.Conditions,
		status.NewCondition(status.ConditionTypeCredentialsValid, metav1.ConditionTrue, status.ReasonCredentialsValid, "Credentials validated successfully.", app.Generation),
	)

	policyLinks, err := r.resolveApplicationPolicyRefs(ctx, &app, accountID)
	if err != nil {
		log.Error(err, "failed to resolve access application policies")
		app.Status.Conditions = status.MergeConditions(app.Status.Conditions,
			status.NewCondition(status.ConditionTypePoliciesResolved, metav1.ConditionFalse, policyResolutionReason(err), status.Error2ConditionMsg(err), app.Generation),
		)
		app.Status.Conditions = status.MergeConditions(app.Status.Conditions,
			status.NewAccessApplicationReadyCondition(app.Status.Conditions, app.Generation),
		)
		_ = r.updateApplicationStatus(ctx, &app)
		return ctrl.Result{RequeueAfter: accessApplicationRequeueAfterError}, nil
	}
	app.Status.Conditions = status.MergeConditions(app.Status.Conditions,
		status.NewCondition(status.ConditionTypePoliciesResolved, metav1.ConditionTrue, status.ReasonPoliciesResolved, "Referenced Access policies are ready.", app.Generation),
	)

	existingIDs := applicationStatusIDs(app.Status.Applications)
	desiredDomains := accessApplicationTargetDomains(targets)
	observed := make([]cfgatev1alpha1.AccessApplicationObserved, 0, len(targets))
	for _, target := range targets {
		params := buildAccessApplicationParams(&app, target, policyLinks, len(targets) > 1)
		cfApp, err := accessService.EnsureApplicationByIDOrTags(ctx, accountID, existingIDs[target.Domain], params)
		if err != nil {
			log.Error(err, "failed to sync access application", "domain", target.Domain)
			app.Status.Conditions = status.MergeConditions(app.Status.Conditions,
				status.NewCondition(status.ConditionTypeApplicationSynced, metav1.ConditionFalse, status.ReasonApplicationError, status.Error2ConditionMsg(err), app.Generation),
			)
			app.Status.Conditions = status.MergeConditions(app.Status.Conditions,
				status.NewAccessApplicationReadyCondition(app.Status.Conditions, app.Generation),
			)
			_ = r.updateApplicationStatus(ctx, &app)
			return ctrl.Result{RequeueAfter: accessApplicationRequeueAfterError}, nil
		}
		observed = append(observed, cfgatev1alpha1.AccessApplicationObserved{
			ID:        cfApp.ID,
			AUD:       cfApp.AUD,
			Domain:    cfApp.Domain,
			TargetRef: target.Ref,
		})
	}
	if err := deleteStaleAccessApplications(ctx, accessService, accountID, app.Status.Applications, desiredDomains); err != nil {
		log.Error(err, "failed to delete stale access applications")
		app.Status.Conditions = status.MergeConditions(app.Status.Conditions,
			status.NewCondition(status.ConditionTypeApplicationSynced, metav1.ConditionFalse, status.ReasonApplicationError, status.Error2ConditionMsg(err), app.Generation),
		)
		app.Status.Conditions = status.MergeConditions(app.Status.Conditions,
			status.NewAccessApplicationReadyCondition(app.Status.Conditions, app.Generation),
		)
		_ = r.updateApplicationStatus(ctx, &app)
		return ctrl.Result{RequeueAfter: accessApplicationRequeueAfterError}, nil
	}
	sort.Slice(observed, func(i, j int) bool { return observed[i].Domain < observed[j].Domain })

	app.Status.Applications = observed
	app.Status.AttachedTargets = int32(len(targets))
	app.Status.Ancestors = applicationAncestors(targets, app.Generation)
	app.Status.ObservedGeneration = app.Generation
	app.Status.Conditions = status.MergeConditions(app.Status.Conditions,
		status.NewCondition(status.ConditionTypeApplicationSynced, metav1.ConditionTrue, status.ReasonApplicationSynced, "Access applications synced.", app.Generation),
		status.NewCondition(status.ConditionTypePoliciesLinked, metav1.ConditionTrue, status.ReasonPoliciesLinked, "Reusable policies linked to Access applications.", app.Generation),
	)
	app.Status.Conditions = status.MergeConditions(app.Status.Conditions,
		status.NewAccessApplicationReadyCondition(app.Status.Conditions, app.Generation),
	)

	if err := r.updateApplicationStatus(ctx, &app); err != nil {
		return ctrl.Result{}, err
	}
	if r.Recorder != nil {
		r.Recorder.Eventf(&app, nil, corev1.EventTypeNormal, "Reconciled", "Reconcile", "Access application reconciled successfully")
	}
	return ctrl.Result{RequeueAfter: accessApplicationRequeueAfterSuccess}, nil
}

func (r *CloudflareAccessApplicationReconciler) resolveApplicationTargets(ctx context.Context, app *cfgatev1alpha1.CloudflareAccessApplication) ([]accessApplicationTarget, bool, error) {
	refs := accessApplicationTargetRefs(app)
	var targets []accessApplicationTarget
	refGrantOK := true
	for i := range refs {
		resolved, granted, err := r.resolveApplicationTarget(ctx, app, refs[i])
		if !granted {
			refGrantOK = false
		}
		if err != nil {
			return nil, refGrantOK, err
		}
		targets = append(targets, resolved...)
	}
	if len(targets) == 0 {
		return nil, refGrantOK, fmt.Errorf("no Access application targets resolved")
	}
	return targets, refGrantOK, nil
}

func accessApplicationTargetRefs(app *cfgatev1alpha1.CloudflareAccessApplication) []cfgatev1alpha1.PolicyTargetReference {
	if app.Spec.TargetRef != nil {
		return []cfgatev1alpha1.PolicyTargetReference{*app.Spec.TargetRef}
	}
	return append([]cfgatev1alpha1.PolicyTargetReference(nil), app.Spec.TargetRefs...)
}

func (r *CloudflareAccessApplicationReconciler) resolveApplicationTarget(ctx context.Context, app *cfgatev1alpha1.CloudflareAccessApplication, ref cfgatev1alpha1.PolicyTargetReference) ([]accessApplicationTarget, bool, error) {
	kind := ref.Kind
	namespace := app.Namespace
	if ref.Namespace != nil && *ref.Namespace != "" {
		namespace = *ref.Namespace
	}
	ref.Namespace = stringPtr(namespace)
	if ref.Group == "" {
		ref.Group = gwapiv1.GroupName
	}
	if ref.Group != gwapiv1.GroupName {
		return nil, true, fmt.Errorf("unsupported target group %q", ref.Group)
	}

	if namespace != app.Namespace {
		granted, err := r.referenceGrantPermits(ctx, app.Namespace, namespace, "cfgate.io", "CloudflareAccessApplication", gwapiv1.GroupName, kind, ref.Name)
		if err != nil {
			return nil, false, fmt.Errorf("checking target ReferenceGrant: %w", err)
		}
		if !granted {
			return nil, false, fmt.Errorf("cross-namespace target reference to %s/%s is not permitted by ReferenceGrant", namespace, ref.Name)
		}
	}

	switch kind {
	case "HTTPRoute":
		var route gwapiv1.HTTPRoute
		if err := r.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: namespace}, &route); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, true, fmt.Errorf("HTTPRoute %s/%s not found", namespace, ref.Name)
			}
			return nil, true, err
		}
		hosts, err := httpRouteAccessHostnames(&route)
		if err != nil {
			return nil, true, err
		}
		paths, err := httpRouteAccessPaths(&route, ref.SectionName, app.Spec.Application.Path)
		if err != nil {
			return nil, true, err
		}
		return buildApplicationTargets(ref, kind, namespace, ref.Name, hosts, paths), true, nil
	case "Gateway":
		var gateway gwapiv1.Gateway
		if err := r.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: namespace}, &gateway); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, true, fmt.Errorf("gateway %s/%s not found", namespace, ref.Name)
			}
			return nil, true, err
		}
		hosts, err := gatewayAccessHostnames(&gateway, ref.SectionName)
		if err != nil {
			return nil, true, err
		}
		path := app.Spec.Application.Path
		if path == "" {
			path = "/"
		}
		if err := validateAccessApplicationPath(path); err != nil {
			return nil, true, err
		}
		return buildApplicationTargets(ref, kind, namespace, ref.Name, hosts, []string{path}), true, nil
	default:
		return nil, true, fmt.Errorf("unsupported target kind %q", kind)
	}
}

func buildApplicationTargets(ref cfgatev1alpha1.PolicyTargetReference, kind, namespace, name string, hosts, paths []string) []accessApplicationTarget {
	var targets []accessApplicationTarget
	for _, host := range hosts {
		for _, path := range paths {
			targets = append(targets, accessApplicationTarget{
				Ref:       ref,
				Kind:      kind,
				Namespace: namespace,
				Name:      name,
				Host:      host,
				Path:      path,
				Domain:    accessApplicationDomain(host, path),
			})
		}
	}
	return targets
}

func httpRouteAccessHostnames(route *gwapiv1.HTTPRoute) ([]string, error) {
	if host := annotations.GetAnnotation(route, annotations.AnnotationHostname); host != "" {
		return []string{host}, nil
	}
	if len(route.Spec.Hostnames) == 0 {
		return nil, fmt.Errorf("HTTPRoute %s/%s has no hostnames", route.Namespace, route.Name)
	}
	hosts := make([]string, 0, len(route.Spec.Hostnames))
	for _, host := range route.Spec.Hostnames {
		hosts = append(hosts, string(host))
	}
	return hosts, nil
}

func httpRouteAccessPaths(route *gwapiv1.HTTPRoute, sectionName *string, override string) ([]string, error) {
	if override != "" {
		if err := validateAccessApplicationPath(override); err != nil {
			return nil, err
		}
		if sectionName != nil {
			if !httpRouteRuleExists(route, *sectionName) {
				return nil, fmt.Errorf("HTTPRoute %s/%s has no rule named %q", route.Namespace, route.Name, *sectionName)
			}
		}
		return []string{override}, nil
	}
	if sectionName == nil || *sectionName == "" {
		return []string{"/"}, nil
	}

	pathSet := map[string]struct{}{}
	for _, rule := range route.Spec.Rules {
		if rule.Name == nil || string(*rule.Name) != *sectionName {
			continue
		}
		if len(rule.Matches) == 0 {
			pathSet["/"] = struct{}{}
			continue
		}
		for _, match := range rule.Matches {
			path, err := accessPathFromHTTPRouteMatch(match)
			if err != nil {
				return nil, err
			}
			pathSet[path] = struct{}{}
		}
	}
	if len(pathSet) == 0 {
		return nil, fmt.Errorf("HTTPRoute %s/%s has no rule named %q", route.Namespace, route.Name, *sectionName)
	}
	paths := make([]string, 0, len(pathSet))
	for path := range pathSet {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, nil
}

func httpRouteRuleExists(route *gwapiv1.HTTPRoute, name string) bool {
	for _, rule := range route.Spec.Rules {
		if rule.Name != nil && string(*rule.Name) == name {
			return true
		}
	}
	return false
}

func accessPathFromHTTPRouteMatch(match gwapiv1.HTTPRouteMatch) (string, error) {
	if match.Path == nil {
		return "/", nil
	}
	if match.Path.Type != nil && *match.Path.Type == gwapiv1.PathMatchRegularExpression {
		path := ""
		if match.Path.Value != nil {
			path = *match.Path.Value
		}
		return "", fmt.Errorf("%s: Access applications do not support RegularExpression path match %q", status.ReasonUnsupportedPathMatch, path)
	}
	path := "/"
	if match.Path.Value != nil && *match.Path.Value != "" {
		path = *match.Path.Value
	}
	if err := validateAccessApplicationPath(path); err != nil {
		return "", err
	}
	return path, nil
}

func gatewayAccessHostnames(gateway *gwapiv1.Gateway, sectionName *string) ([]string, error) {
	var hosts []string
	for _, listener := range gateway.Spec.Listeners {
		if sectionName != nil && string(listener.Name) != *sectionName {
			continue
		}
		if listener.Hostname != nil {
			hosts = append(hosts, string(*listener.Hostname))
		}
	}
	if len(hosts) == 0 {
		if sectionName != nil {
			return nil, fmt.Errorf("gateway %s/%s has no listener named %q with hostname", gateway.Namespace, gateway.Name, *sectionName)
		}
		return nil, fmt.Errorf("gateway %s/%s has no listener hostnames", gateway.Namespace, gateway.Name)
	}
	return hosts, nil
}

func validateAccessApplicationPath(path string) error {
	if path == "" {
		return fmt.Errorf("access application path cannot be empty")
	}
	if !strings.HasPrefix(path, "/") {
		return fmt.Errorf("access application path %q must start with /", path)
	}
	if strings.ContainsAny(path, "?#") {
		return fmt.Errorf("access application path %q must not contain query strings or fragments", path)
	}
	return nil
}

func accessApplicationDomain(host, path string) string {
	if path == "" || path == "/" {
		return host
	}
	return host + path
}

func (r *CloudflareAccessApplicationReconciler) resolveApplicationCredentials(ctx context.Context, app *cfgatev1alpha1.CloudflareAccessApplication, targets []accessApplicationTarget) (*cloudflare.AccessService, string, error) {
	if app.Spec.CloudflareRef != nil {
		cfClient, accountID, err := r.getAccessApplicationCloudflareClient(ctx, app.Namespace, app.Spec.CloudflareRef)
		if err != nil {
			return nil, "", err
		}
		return cloudflare.NewAccessService(cfClient, log.FromContext(ctx)), accountID, nil
	}
	if len(targets) == 0 {
		return nil, "", fmt.Errorf("no targets available for credential inheritance")
	}
	gateway, err := r.gatewayForApplicationTarget(ctx, targets[0])
	if err != nil {
		return nil, "", err
	}
	tunnelRef := annotations.GetAnnotation(gateway, annotations.AnnotationTunnelRef)
	if tunnelRef == "" {
		return nil, "", fmt.Errorf("gateway %s/%s missing %s annotation", gateway.Namespace, gateway.Name, annotations.AnnotationTunnelRef)
	}
	tunnelNS, tunnelName, err := annotations.ParseNamespacedName(tunnelRef, gateway.Namespace)
	if err != nil {
		return nil, "", fmt.Errorf("invalid tunnel reference %q: %w", tunnelRef, err)
	}
	var tunnel cfgatev1alpha1.CloudflareTunnel
	if err := r.Get(ctx, types.NamespacedName{Name: tunnelName, Namespace: tunnelNS}, &tunnel); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, "", fmt.Errorf("CloudflareTunnel %s/%s not found", tunnelNS, tunnelName)
		}
		return nil, "", err
	}
	secretRef := cfgatev1alpha1.CloudflareSecretRef{
		Name:        tunnel.Spec.Cloudflare.SecretRef.Name,
		AccountID:   tunnel.Spec.Cloudflare.AccountID,
		AccountName: tunnel.Spec.Cloudflare.AccountName,
	}
	if tunnel.Spec.Cloudflare.SecretRef.Namespace != "" {
		secretRef.Namespace = stringPtr(tunnel.Spec.Cloudflare.SecretRef.Namespace)
	}
	if secretRef.AccountID == "" {
		secretRef.AccountID = tunnel.Status.AccountID
	}
	cfClient, accountID, err := r.getAccessApplicationCloudflareClient(ctx, tunnel.Namespace, &secretRef)
	if err != nil {
		return nil, "", err
	}
	return cloudflare.NewAccessService(cfClient, log.FromContext(ctx)), accountID, nil
}

func (r *CloudflareAccessApplicationReconciler) gatewayForApplicationTarget(ctx context.Context, target accessApplicationTarget) (*gwapiv1.Gateway, error) {
	if target.Kind == "Gateway" {
		var gateway gwapiv1.Gateway
		if err := r.Get(ctx, types.NamespacedName{Name: target.Name, Namespace: target.Namespace}, &gateway); err != nil {
			return nil, err
		}
		return &gateway, nil
	}
	if target.Kind != "HTTPRoute" {
		return nil, fmt.Errorf("cannot inherit credentials from target kind %q", target.Kind)
	}
	var route gwapiv1.HTTPRoute
	if err := r.Get(ctx, types.NamespacedName{Name: target.Name, Namespace: target.Namespace}, &route); err != nil {
		return nil, err
	}
	for _, parent := range route.Spec.ParentRefs {
		if parent.Group != nil && string(*parent.Group) != gwapiv1.GroupName {
			continue
		}
		if parent.Kind != nil && string(*parent.Kind) != "Gateway" {
			continue
		}
		namespace := route.Namespace
		if parent.Namespace != nil && *parent.Namespace != "" {
			namespace = string(*parent.Namespace)
		}
		var gateway gwapiv1.Gateway
		if err := r.Get(ctx, types.NamespacedName{Name: string(parent.Name), Namespace: namespace}, &gateway); err != nil {
			continue
		}
		return &gateway, nil
	}
	return nil, fmt.Errorf("HTTPRoute %s/%s has no resolvable Gateway parent", route.Namespace, route.Name)
}

func (r *CloudflareAccessApplicationReconciler) getAccessApplicationCloudflareClient(ctx context.Context, defaultNamespace string, ref *cfgatev1alpha1.CloudflareSecretRef) (cloudflare.Client, string, error) {
	if ref.AccountID == "" && ref.AccountName == "" {
		return nil, "", fmt.Errorf("account ID or account name not specified")
	}
	cfClient, err := r.getCloudflareClient(ctx, defaultNamespace, ref)
	if err != nil {
		return nil, "", err
	}
	accountID := ref.AccountID
	if accountID == "" {
		account, err := cfClient.GetAccountByName(ctx, ref.AccountName)
		if err != nil {
			return nil, "", fmt.Errorf("failed to resolve account name %q: %w", ref.AccountName, err)
		}
		if account == nil {
			return nil, "", fmt.Errorf("account %q not found", ref.AccountName)
		}
		accountID = account.ID
	}
	return cfClient, accountID, nil
}

func (r *CloudflareAccessApplicationReconciler) getCloudflareClient(ctx context.Context, defaultNamespace string, secretRef *cfgatev1alpha1.CloudflareSecretRef) (cloudflare.Client, error) {
	if r.CFClient != nil {
		return r.CFClient, nil
	}
	secretNamespace := defaultNamespace
	if secretRef.Namespace != nil && *secretRef.Namespace != "" {
		secretNamespace = *secretRef.Namespace
	}
	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: secretRef.Name, Namespace: secretNamespace}, secret); err != nil {
		return nil, fmt.Errorf("failed to get credentials secret: %w", err)
	}
	if r.CredentialCache != nil {
		return r.CredentialCache.GetOrCreate(ctx, secret, func() (cloudflare.Client, error) {
			return r.createClientFromSecret(secret)
		})
	}
	return r.createClientFromSecret(secret)
}

func (r *CloudflareAccessApplicationReconciler) createClientFromSecret(secret *corev1.Secret) (cloudflare.Client, error) {
	token, ok := secret.Data["CLOUDFLARE_API_TOKEN"]
	if !ok {
		return nil, fmt.Errorf("API token key %q not found in secret", "CLOUDFLARE_API_TOKEN")
	}
	return cloudflare.NewClient(string(token))
}

func (r *CloudflareAccessApplicationReconciler) resolveApplicationPolicyRefs(ctx context.Context, app *cfgatev1alpha1.CloudflareAccessApplication, accountID string) ([]cloudflare.ApplicationPolicyLink, error) {
	seen := map[string]struct{}{}
	links := make([]cloudflare.ApplicationPolicyLink, 0, len(app.Spec.PolicyRefs))
	for i, ref := range app.Spec.PolicyRefs {
		namespace := app.Namespace
		if ref.Namespace != "" {
			namespace = ref.Namespace
		}
		key := namespace + "/" + ref.Name
		if _, ok := seen[key]; ok {
			return nil, fmt.Errorf("duplicate policyRef %s", key)
		}
		seen[key] = struct{}{}

		if namespace != app.Namespace {
			granted, err := r.referenceGrantPermits(ctx, app.Namespace, namespace, "cfgate.io", "CloudflareAccessApplication", "cfgate.io", "CloudflareAccessPolicy", ref.Name)
			if err != nil {
				return nil, fmt.Errorf("checking policy ReferenceGrant: %w", err)
			}
			if !granted {
				return nil, fmt.Errorf("cross-namespace policy reference to %s is not permitted by ReferenceGrant", key)
			}
		}

		var policy cfgatev1alpha1.CloudflareAccessPolicy
		if err := r.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: namespace}, &policy); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, fmt.Errorf("CloudflareAccessPolicy %s not found", key)
			}
			return nil, err
		}
		if !status.ConditionTrue(policy.Status.Conditions, status.ConditionTypeReady) {
			return nil, fmt.Errorf("CloudflareAccessPolicy %s is not ready", key)
		}
		if policy.Status.PolicyID == "" {
			return nil, fmt.Errorf("CloudflareAccessPolicy %s has no policyId", key)
		}
		if policy.Status.AccountID != accountID {
			return nil, fmt.Errorf("%s: CloudflareAccessPolicy %s account %q does not match application account %q", status.ReasonAccountMismatch, key, policy.Status.AccountID, accountID)
		}
		precedence := i + 1
		if ref.Precedence != nil {
			precedence = *ref.Precedence
		}
		links = append(links, cloudflare.ApplicationPolicyLink{ID: policy.Status.PolicyID, Precedence: precedence})
	}
	return links, nil
}

func buildAccessApplicationParams(app *cfgatev1alpha1.CloudflareAccessApplication, target accessApplicationTarget, policies []cloudflare.ApplicationPolicyLink, multipleTargets bool) cloudflare.ApplicationParams {
	cfg := app.Spec.Application
	name := cfg.Name
	if name == "" {
		name = app.Name
	}
	if multipleTargets {
		name = name + "-" + sanitizeAccessApplicationName(target.Domain)
	}
	httpOnly := cfg.HttpOnlyCookieAttribute
	return cloudflare.ApplicationParams{
		Name:                        name,
		Domain:                      target.Domain,
		Destinations:                []string{target.Domain},
		Tags:                        []string{accessApplicationTag, accessApplicationOwnerTag(app)},
		Policies:                    append([]cloudflare.ApplicationPolicyLink(nil), policies...),
		Type:                        cfg.Type,
		SessionDuration:             cfg.SessionDuration,
		AllowedIdps:                 cfg.AllowedIdps,
		AutoRedirectToIdentity:      cfg.AutoRedirectToIdentity,
		EnableBindingCookie:         cfg.EnableBindingCookie,
		HttpOnlyCookieAttribute:     &httpOnly,
		SameSiteCookieAttribute:     cfg.SameSiteCookieAttribute,
		SkipInterstitial:            cfg.SkipInterstitial,
		LogoURL:                     cfg.LogoURL,
		AppLauncherVisible:          cfg.AppLauncherVisible == nil || *cfg.AppLauncherVisible,
		CustomDenyMessage:           cfg.CustomDenyMessage,
		CustomDenyURL:               cfg.CustomDenyURL,
		CORSHeaders:                 convertApplicationCORS(cfg.CORSHeaders),
		OptionsPreflightBypass:      cfg.OptionsPreflightBypass,
		PathCookieAttribute:         cfg.PathCookieAttribute,
		ServiceAuth401Redirect:      cfg.ServiceAuth401Redirect,
		CustomNonIdentityDenyURL:    cfg.CustomNonIdentityDenyURL,
		ReadServiceTokensFromHeader: cfg.ReadServiceTokensFromHeader,
	}
}

func convertApplicationCORS(cors *cfgatev1alpha1.CORSHeaders) *cloudflare.CORSHeadersParam {
	if cors == nil {
		return nil
	}
	methods := make([]string, 0, len(cors.AllowedMethods))
	for _, method := range cors.AllowedMethods {
		methods = append(methods, string(method))
	}
	maxAge := 0
	if cors.MaxAge != nil {
		maxAge = *cors.MaxAge
	}
	return &cloudflare.CORSHeadersParam{
		AllowAllHeaders:  cors.AllowAllHeaders,
		AllowAllMethods:  cors.AllowAllMethods,
		AllowAllOrigins:  cors.AllowAllOrigins,
		AllowCredentials: cors.AllowCredentials,
		AllowedHeaders:   append([]string(nil), cors.AllowedHeaders...),
		AllowedMethods:   methods,
		AllowedOrigins:   append([]string(nil), cors.AllowedOrigins...),
		MaxAge:           maxAge,
	}
}

func sanitizeAccessApplicationName(value string) string {
	value = strings.ToLower(value)
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('-')
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 63 {
		out = strings.Trim(out[:63], "-")
	}
	if out == "" {
		return "target"
	}
	return out
}

func accessApplicationOwnerTag(app *cfgatev1alpha1.CloudflareAccessApplication) string {
	sum := sha256.Sum256([]byte(app.Namespace + "/" + app.Name))
	return "cfgate:" + hex.EncodeToString(sum[:])[:28]
}

func applicationStatusIDs(applications []cfgatev1alpha1.AccessApplicationObserved) map[string]string {
	ids := make(map[string]string, len(applications))
	for _, app := range applications {
		ids[app.Domain] = app.ID
	}
	return ids
}

func accessApplicationTargetDomains(targets []accessApplicationTarget) map[string]struct{} {
	domains := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		domains[target.Domain] = struct{}{}
	}
	return domains
}

func deleteStaleAccessApplications(ctx context.Context, accessService *cloudflare.AccessService, accountID string, existing []cfgatev1alpha1.AccessApplicationObserved, desiredDomains map[string]struct{}) error {
	for _, observed := range existing {
		if _, ok := desiredDomains[observed.Domain]; ok {
			continue
		}
		if observed.ID == "" {
			continue
		}
		if err := accessService.Client().DeleteAccessApplication(ctx, accountID, observed.ID); err != nil {
			return fmt.Errorf("delete stale Access application %s for %s: %w", observed.ID, observed.Domain, err)
		}
	}
	return nil
}

func applicationAncestors(targets []accessApplicationTarget, generation int64) []cfgatev1alpha1.PolicyAncestorStatus {
	ancestors := make([]cfgatev1alpha1.PolicyAncestorStatus, 0, len(targets))
	for _, target := range targets {
		ancestors = append(ancestors, cfgatev1alpha1.PolicyAncestorStatus{
			AncestorRef:    target.Ref,
			ControllerName: AccessPolicyControllerName,
			Conditions: []metav1.Condition{
				status.NewPolicyAcceptedCondition(true, status.PolicyReasonAccepted, "Access application target accepted.", generation),
			},
		})
	}
	return ancestors
}

func (r *CloudflareAccessApplicationReconciler) referenceGrantPermits(ctx context.Context, fromNamespace, toNamespace, fromGroup, fromKind, toGroup, toKind, toName string) (bool, error) {
	if fromNamespace == toNamespace {
		return true, nil
	}
	if r.FeatureGates != nil && !r.FeatureGates.HasReferenceGrantSupport() {
		return false, nil
	}
	var grants gwapiv1b1.ReferenceGrantList
	if err := r.List(ctx, &grants, client.InNamespace(toNamespace)); err != nil {
		return false, err
	}
	for _, grant := range grants.Items {
		fromOK := false
		for _, from := range grant.Spec.From {
			if string(from.Group) == fromGroup && string(from.Kind) == fromKind && string(from.Namespace) == fromNamespace {
				fromOK = true
				break
			}
		}
		if !fromOK {
			continue
		}
		for _, to := range grant.Spec.To {
			if string(to.Group) != toGroup || string(to.Kind) != toKind {
				continue
			}
			if to.Name == nil || string(*to.Name) == toName {
				return true, nil
			}
		}
	}
	return false, nil
}

func (r *CloudflareAccessApplicationReconciler) reconcileApplicationDelete(ctx context.Context, app *cfgatev1alpha1.CloudflareAccessApplication) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(app, accessApplicationFinalizer) {
		return ctrl.Result{}, nil
	}
	if app.Annotations["cfgate.io/deletion-policy"] == "orphan" {
		return r.removeApplicationFinalizer(ctx, app)
	}
	if len(app.Status.Applications) == 0 {
		return r.removeApplicationFinalizer(ctx, app)
	}
	targets := make([]accessApplicationTarget, 0, len(app.Status.Applications))
	for _, observed := range app.Status.Applications {
		namespace := app.Namespace
		if observed.TargetRef.Namespace != nil && *observed.TargetRef.Namespace != "" {
			namespace = *observed.TargetRef.Namespace
		}
		targets = append(targets, accessApplicationTarget{Ref: observed.TargetRef, Kind: observed.TargetRef.Kind, Namespace: namespace, Name: observed.TargetRef.Name, Domain: observed.Domain})
	}
	accessService, accountID, err := r.resolveApplicationCredentials(ctx, app, targets)
	if err != nil {
		return r.blockApplicationDeletion(ctx, app, fmt.Sprintf("Failed to resolve credentials: %s", err.Error()))
	}
	for _, observed := range app.Status.Applications {
		if observed.ID == "" {
			continue
		}
		if err := accessService.Client().DeleteAccessApplication(ctx, accountID, observed.ID); err != nil {
			return r.blockApplicationDeletion(ctx, app, fmt.Sprintf("Failed to delete Access application %s: %s", observed.ID, err.Error()))
		}
	}
	return r.removeApplicationFinalizer(ctx, app)
}

func (r *CloudflareAccessApplicationReconciler) blockApplicationDeletion(ctx context.Context, app *cfgatev1alpha1.CloudflareAccessApplication, detail string) (ctrl.Result, error) {
	retryElapsed := time.Since(app.DeletionTimestamp.Time)
	suffix := " Set annotation cfgate.io/deletion-policy=orphan to skip cleanup and remove finalizer."
	reason := "CleanupFailed"
	message := detail + "." + suffix
	if retryElapsed >= accessDeletionRetryBudget {
		reason = "CleanupBlocked"
		message = fmt.Sprintf("%s (blocked after %s of retries).%s", detail, retryElapsed.Round(time.Second), suffix)
	}
	if r.Recorder != nil {
		r.Recorder.Eventf(app, nil, corev1.EventTypeWarning, reason, "Delete", "%s", message)
	}
	return ctrl.Result{RequeueAfter: accessDeletionRequeueInterval}, nil
}

func (r *CloudflareAccessApplicationReconciler) removeApplicationFinalizer(ctx context.Context, app *cfgatev1alpha1.CloudflareAccessApplication) (ctrl.Result, error) {
	patch := client.MergeFrom(app.DeepCopy())
	controllerutil.RemoveFinalizer(app, accessApplicationFinalizer)
	if err := r.Patch(ctx, app, patch); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to remove finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}

func (r *CloudflareAccessApplicationReconciler) updateApplicationStatus(ctx context.Context, app *cfgatev1alpha1.CloudflareAccessApplication) error {
	var current cfgatev1alpha1.CloudflareAccessApplication
	if err := r.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: app.Namespace}, &current); err != nil {
		return fmt.Errorf("failed to re-fetch application: %w", err)
	}
	if accessApplicationStatusEqual(&current.Status, &app.Status) {
		return nil
	}
	current.Status = app.Status
	return r.Status().Update(ctx, &current)
}

func accessApplicationStatusEqual(a, b *cfgatev1alpha1.CloudflareAccessApplicationStatus) bool {
	if a.AttachedTargets != b.AttachedTargets || a.ObservedGeneration != b.ObservedGeneration {
		return false
	}
	if !reflect.DeepEqual(a.Applications, b.Applications) || !reflect.DeepEqual(a.Ancestors, b.Ancestors) {
		return false
	}
	return conditionsEqual(a.Conditions, b.Conditions)
}

func conditionStatus(ok bool) metav1.ConditionStatus {
	if ok {
		return metav1.ConditionTrue
	}
	return metav1.ConditionFalse
}

func referenceGrantReason(ok bool) string {
	if ok {
		return status.ReasonResolved
	}
	return status.ReasonReferenceGrantRequired
}

func referenceGrantMessage(ok bool) string {
	if ok {
		return "ReferenceGrants permit referenced resources."
	}
	return "ReferenceGrant is required for a cross-namespace reference."
}

func targetResolutionReason(err error) string {
	if err != nil && strings.Contains(err.Error(), status.ReasonUnsupportedPathMatch) {
		return status.ReasonUnsupportedPathMatch
	}
	if err != nil && strings.Contains(err.Error(), "not found") {
		return status.ReasonTargetNotFound
	}
	return status.ReasonTargetResolutionFailed
}

func policyResolutionReason(err error) string {
	if err != nil && strings.Contains(err.Error(), status.ReasonAccountMismatch) {
		return status.ReasonAccountMismatch
	}
	if err != nil && strings.Contains(err.Error(), "ReferenceGrant") {
		return status.ReasonReferenceGrantRequired
	}
	return status.ReasonPolicyError
}

func stringPtr(v string) *string {
	return &v
}

func (r *CloudflareAccessApplicationReconciler) findApplicationsForGatewayTarget(ctx context.Context, obj client.Object) []reconcile.Request {
	gateway := obj.(*gwapiv1.Gateway)
	return r.findApplicationsReferencingTarget(ctx, "Gateway", gateway.Namespace, gateway.Name)
}

func (r *CloudflareAccessApplicationReconciler) findApplicationsForHTTPRouteTarget(ctx context.Context, obj client.Object) []reconcile.Request {
	route := obj.(*gwapiv1.HTTPRoute)
	return r.findApplicationsReferencingTarget(ctx, "HTTPRoute", route.Namespace, route.Name)
}

func (r *CloudflareAccessApplicationReconciler) findApplicationsForPolicy(ctx context.Context, obj client.Object) []reconcile.Request {
	policy := obj.(*cfgatev1alpha1.CloudflareAccessPolicy)
	var apps cfgatev1alpha1.CloudflareAccessApplicationList
	if err := r.List(ctx, &apps); err != nil {
		log.FromContext(ctx).Error(err, "failed to list CloudflareAccessApplications")
		return nil
	}
	var requests []reconcile.Request
	for _, app := range apps.Items {
		for _, ref := range app.Spec.PolicyRefs {
			namespace := app.Namespace
			if ref.Namespace != "" {
				namespace = ref.Namespace
			}
			if ref.Name == policy.Name && namespace == policy.Namespace {
				requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Name: app.Name, Namespace: app.Namespace}})
				break
			}
		}
	}
	return requests
}

func (r *CloudflareAccessApplicationReconciler) findApplicationsReferencingTarget(ctx context.Context, kind, namespace, name string) []reconcile.Request {
	var apps cfgatev1alpha1.CloudflareAccessApplicationList
	if err := r.List(ctx, &apps); err != nil {
		log.FromContext(ctx).Error(err, "failed to list CloudflareAccessApplications")
		return nil
	}
	var requests []reconcile.Request
	for _, app := range apps.Items {
		for _, ref := range accessApplicationTargetRefs(&app) {
			refNS := app.Namespace
			if ref.Namespace != nil && *ref.Namespace != "" {
				refNS = *ref.Namespace
			}
			if ref.Kind == kind && ref.Name == name && refNS == namespace {
				requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Name: app.Name, Namespace: app.Namespace}})
				break
			}
		}
	}
	return requests
}

func (r *CloudflareAccessApplicationReconciler) findAllAccessApplications(ctx context.Context, _ client.Object) []reconcile.Request {
	var apps cfgatev1alpha1.CloudflareAccessApplicationList
	if err := r.List(ctx, &apps); err != nil {
		log.FromContext(ctx).Error(err, "failed to list CloudflareAccessApplications")
		return nil
	}
	requests := make([]reconcile.Request, 0, len(apps.Items))
	for _, app := range apps.Items {
		requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Name: app.Name, Namespace: app.Namespace}})
	}
	return requests
}

func (r *CloudflareAccessApplicationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(ctrlcontroller.Options{MaxConcurrentReconciles: 4}).
		For(&cfgatev1alpha1.CloudflareAccessApplication{}, builder.WithPredicates(GenerationOrDeletionPredicate)).
		Watches(&gwapiv1.Gateway{}, handler.EnqueueRequestsFromMapFunc(r.findApplicationsForGatewayTarget), builder.WithPredicates(CfgateAnnotationOrGenerationPredicate)).
		Watches(&gwapiv1.HTTPRoute{}, handler.EnqueueRequestsFromMapFunc(r.findApplicationsForHTTPRouteTarget), builder.WithPredicates(CfgateAnnotationOrGenerationPredicate)).
		Watches(&cfgatev1alpha1.CloudflareAccessPolicy{}, handler.EnqueueRequestsFromMapFunc(r.findApplicationsForPolicy), builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&cfgatev1alpha1.CloudflareTunnel{}, handler.EnqueueRequestsFromMapFunc(r.findAllAccessApplications), builder.WithPredicates(TunnelIDChangedPredicate)).
		Watches(&gwapiv1b1.ReferenceGrant{}, handler.EnqueueRequestsFromMapFunc(r.findAllAccessApplications)).
		Complete(r)
}
