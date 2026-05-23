package controller

import (
	"context"
	"errors"
	"fmt"
	"reflect"
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
	"sigs.k8s.io/controller-runtime/pkg/log"

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/cloudflare"
	"cfgate.io/cfgate/internal/controller/features"
	"cfgate.io/cfgate/internal/controller/status"
)

const (
	accessPolicyFinalizer                = "cfgate.io/access-policy-cleanup"
	accessPolicyRequeueAfterError        = 30 * time.Second
	accessPolicyRequeueAfterSuccess      = 5 * time.Minute
	AccessPolicyControllerName           = "cfgate.io/cloudflare-access-controller"
	accessDeletionRetryBudget            = 1 * time.Minute
	accessDeletionRequeueInterval        = 15 * time.Second
	accessApplicationFinalizer           = "cfgate.io/access-application-cleanup"
	accessApplicationRequeueAfterError   = 30 * time.Second
	accessApplicationRequeueAfterSuccess = 5 * time.Minute
)

// CloudflareAccessPolicyReconciler reconciles reusable Cloudflare Access policies.
type CloudflareAccessPolicyReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder events.EventRecorder

	CFClient        cloudflare.Client
	CredentialCache *cloudflare.CredentialCache
	FeatureGates    *features.FeatureGates
}

type accessPolicyCredentials struct {
	Service             *cloudflare.AccessService
	AccountID           string
	CredentialSecretRef *cfgatev1alpha1.SecretReference
}

// +kubebuilder:rbac:groups=cfgate.io,resources=cloudflareaccesspolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cfgate.io,resources=cloudflareaccesspolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cfgate.io,resources=cloudflareaccesspolicies/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

func (r *CloudflareAccessPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx).WithName("controller").WithName("accesspolicy").
		WithValues("namespace", req.Namespace, "name", req.Name)

	var policy cfgatev1alpha1.CloudflareAccessPolicy
	if err := r.Get(ctx, req.NamespacedName, &policy); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to get CloudflareAccessPolicy: %w", err)
	}

	if !policy.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &policy)
	}

	if !controllerutil.ContainsFinalizer(&policy, accessPolicyFinalizer) {
		patch := client.MergeFrom(policy.DeepCopy())
		controllerutil.AddFinalizer(&policy, accessPolicyFinalizer)
		if err := r.Patch(ctx, &policy, patch); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to add finalizer: %w", err)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	creds, err := r.resolveCredentials(ctx, &policy)
	if err != nil {
		log.Error(err, "failed to resolve credentials")
		policy.Status.Conditions = status.MergeConditions(policy.Status.Conditions,
			status.NewCondition(status.ConditionTypeCredentialsValid, metav1.ConditionFalse,
				status.ReasonCredentialsInvalid, status.Error2ConditionMsg(err), policy.Generation),
		)
		_ = r.updateStatus(ctx, &policy)
		return ctrl.Result{RequeueAfter: accessPolicyRequeueAfterError}, nil
	}
	policy.Status.AccountID = creds.AccountID
	policy.Status.CredentialSecretRef = creds.CredentialSecretRef
	policy.Status.Conditions = status.MergeConditions(policy.Status.Conditions,
		status.NewCondition(status.ConditionTypeCredentialsValid, metav1.ConditionTrue,
			status.ReasonCredentialsValid, "Credentials validated successfully.", policy.Generation),
	)

	if len(policy.Spec.ServiceTokens) > 0 {
		if err := r.syncServiceTokens(ctx, creds.Service, creds.AccountID, &policy); err != nil {
			log.Error(err, "failed to ensure service tokens")
			policy.Status.Conditions = status.MergeConditions(policy.Status.Conditions,
				status.NewCondition(status.ConditionTypeServiceTokensReady, metav1.ConditionFalse,
					status.ReasonServiceTokenError, status.Error2ConditionMsg(err), policy.Generation),
			)
			_ = r.updateStatus(ctx, &policy)
			return ctrl.Result{RequeueAfter: accessPolicyRequeueAfterError}, nil
		}
		policy.Status.Conditions = status.MergeConditions(policy.Status.Conditions,
			status.NewCondition(status.ConditionTypeServiceTokensReady, metav1.ConditionTrue,
				status.ReasonServiceTokensReady, "Service tokens ready.", policy.Generation),
		)
	} else if err := r.syncServiceTokens(ctx, creds.Service, creds.AccountID, &policy); err != nil {
		log.Error(err, "failed to sync removed service tokens")
		policy.Status.Conditions = status.MergeConditions(policy.Status.Conditions,
			status.NewCondition(status.ConditionTypeServiceTokensReady, metav1.ConditionFalse,
				status.ReasonServiceTokenError, status.Error2ConditionMsg(err), policy.Generation),
		)
		_ = r.updateStatus(ctx, &policy)
		return ctrl.Result{RequeueAfter: accessPolicyRequeueAfterError}, nil
	} else {
		policy.Status.Conditions = status.RemoveCondition(policy.Status.Conditions, status.ConditionTypeServiceTokensReady)
	}

	params, err := buildReusablePolicyParams(&policy)
	if err != nil {
		policy.Status.Conditions = status.MergeConditions(policy.Status.Conditions,
			status.NewCondition(status.ConditionTypePolicySynced, metav1.ConditionFalse,
				status.ReasonPolicyError, status.Error2ConditionMsg(err), policy.Generation),
		)
		_ = r.updateStatus(ctx, &policy)
		return ctrl.Result{RequeueAfter: accessPolicyRequeueAfterError}, nil
	}

	cfPolicy, err := creds.Service.EnsureReusablePolicy(ctx, creds.AccountID, policy.Status.PolicyID, params)
	if err != nil {
		log.Error(err, "failed to sync reusable policy")
		policy.Status.Conditions = status.MergeConditions(policy.Status.Conditions,
			status.NewCondition(status.ConditionTypePolicySynced, metav1.ConditionFalse,
				status.ReasonPolicyError, status.Error2ConditionMsg(err), policy.Generation),
		)
		_ = r.updateStatus(ctx, &policy)
		return ctrl.Result{RequeueAfter: accessPolicyRequeueAfterError}, nil
	}

	policy.Status.PolicyID = cfPolicy.ID
	policy.Status.Reusable = cfPolicy.Reusable
	policy.Status.AppCount = cfPolicy.AppCount
	policy.Status.ObservedGeneration = policy.Generation
	policy.Status.Conditions = status.MergeConditions(policy.Status.Conditions,
		status.NewCondition(status.ConditionTypePolicySynced, metav1.ConditionTrue,
			status.ReasonPolicySynced, fmt.Sprintf("Reusable Access policy %s synced.", cfPolicy.ID), policy.Generation),
	)
	policy.Status.Conditions = status.MergeConditions(policy.Status.Conditions,
		status.NewAccessPolicyReadyCondition(policy.Status.Conditions, len(policy.Spec.ServiceTokens) > 0, policy.Generation),
	)

	if err := r.updateStatus(ctx, &policy); err != nil {
		return ctrl.Result{}, err
	}
	if r.Recorder != nil {
		r.Recorder.Eventf(&policy, nil, corev1.EventTypeNormal, "Reconciled", "Reconcile", "Access policy reconciled successfully")
	}
	return ctrl.Result{RequeueAfter: accessPolicyRequeueAfterSuccess}, nil
}

func (r *CloudflareAccessPolicyReconciler) resolveCredentials(ctx context.Context, policy *cfgatev1alpha1.CloudflareAccessPolicy) (*accessPolicyCredentials, error) {
	return r.resolveCloudflareRefCredentials(ctx, policy.Namespace, &policy.Spec.CloudflareRef)
}

func (r *CloudflareAccessPolicyReconciler) resolveCloudflareRefCredentials(ctx context.Context, defaultNamespace string, secretRef *cfgatev1alpha1.CloudflareSecretRef) (*accessPolicyCredentials, error) {
	if secretRef.AccountID == "" && secretRef.AccountName == "" {
		return nil, fmt.Errorf("account ID or account name not specified")
	}
	cfClient, err := r.getCloudflareClient(ctx, defaultNamespace, secretRef)
	if err != nil {
		return nil, fmt.Errorf("failed to create Cloudflare client: %w", err)
	}
	accountID := secretRef.AccountID
	if accountID == "" {
		account, err := cfClient.GetAccountByName(ctx, secretRef.AccountName)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve account name %q: %w", secretRef.AccountName, err)
		}
		if account == nil {
			return nil, fmt.Errorf("account %q not found", secretRef.AccountName)
		}
		accountID = account.ID
	}
	secretNamespace := defaultNamespace
	if secretRef.Namespace != nil && *secretRef.Namespace != "" {
		secretNamespace = *secretRef.Namespace
	}
	return &accessPolicyCredentials{
		Service:   cloudflare.NewAccessService(cfClient, log.FromContext(ctx)),
		AccountID: accountID,
		CredentialSecretRef: &cfgatev1alpha1.SecretReference{
			Name:      secretRef.Name,
			Namespace: secretNamespace,
		},
	}, nil
}

func buildReusablePolicyParams(policy *cfgatev1alpha1.CloudflareAccessPolicy) (cloudflare.PolicyParams, error) {
	if err := validateAccessPolicyCompatibility(policy); err != nil {
		return cloudflare.PolicyParams{}, err
	}
	include, err := convertAccessRulesWithServiceTokens(policy.Spec.Include, policy.Status.ServiceTokenIDs)
	if err != nil {
		return cloudflare.PolicyParams{}, fmt.Errorf("include rules: %w", err)
	}
	exclude, err := convertAccessRulesWithServiceTokens(policy.Spec.Exclude, policy.Status.ServiceTokenIDs)
	if err != nil {
		return cloudflare.PolicyParams{}, fmt.Errorf("exclude rules: %w", err)
	}
	require, err := convertAccessRulesWithServiceTokens(policy.Spec.Require, policy.Status.ServiceTokenIDs)
	if err != nil {
		return cloudflare.PolicyParams{}, fmt.Errorf("require rules: %w", err)
	}
	return cloudflare.PolicyParams{
		Name:                         policy.Spec.Name,
		Decision:                     policy.Spec.Decision,
		Include:                      include,
		Exclude:                      exclude,
		Require:                      require,
		SessionDuration:              policy.Spec.SessionDuration,
		PurposeJustificationRequired: policy.Spec.PurposeJustificationRequired,
		PurposeJustificationPrompt:   policy.Spec.PurposeJustificationPrompt,
		ApprovalRequired:             policy.Spec.ApprovalRequired,
		ApprovalGroups:               convertApprovalGroups(policy.Spec.ApprovalGroups),
	}, nil
}

func validateAccessPolicyCompatibility(policy *cfgatev1alpha1.CloudflareAccessPolicy) error {
	ruleSets := []struct {
		name  string
		rules []cfgatev1alpha1.AccessRule
	}{
		{name: "include", rules: policy.Spec.Include},
		{name: "exclude", rules: policy.Spec.Exclude},
		{name: "require", rules: policy.Spec.Require},
	}
	for _, set := range ruleSets {
		for i, rule := range set.rules {
			if countAccessRuleSelectors(rule) != 1 {
				return fmt.Errorf("%s[%d]: exactly one selector must be specified", set.name, i)
			}
			if policy.Spec.Decision == "bypass" && accessRuleHasIdentitySelector(rule) {
				return fmt.Errorf("%s[%d]: bypass policies cannot use identity selectors", set.name, i)
			}
		}
	}
	if policy.Spec.Decision == "non_identity" {
		for _, rule := range policy.Spec.Include {
			if rule.ServiceToken != nil || (rule.AnyValidServiceToken != nil && *rule.AnyValidServiceToken) {
				return nil
			}
		}
		return fmt.Errorf("non_identity policies require an include serviceToken or anyValidServiceToken selector")
	}
	return nil
}

func countAccessRuleSelectors(rule cfgatev1alpha1.AccessRule) int {
	count := 0
	if rule.IP != nil {
		count++
	}
	if rule.IPList != nil {
		count++
	}
	if rule.Country != nil {
		count++
	}
	if rule.Everyone != nil {
		count++
	}
	if rule.ServiceToken != nil {
		count++
	}
	if rule.AnyValidServiceToken != nil {
		count++
	}
	if rule.Email != nil {
		count++
	}
	if rule.EmailList != nil {
		count++
	}
	if rule.EmailDomain != nil {
		count++
	}
	if rule.OIDCClaim != nil {
		count++
	}
	if rule.GSuiteGroup != nil {
		count++
	}
	if rule.Group != nil {
		count++
	}
	return count
}

func accessRuleHasIdentitySelector(rule cfgatev1alpha1.AccessRule) bool {
	return rule.Email != nil ||
		rule.EmailList != nil ||
		rule.EmailDomain != nil ||
		rule.OIDCClaim != nil ||
		rule.GSuiteGroup != nil ||
		rule.Group != nil
}

func convertAccessRules(crdRules []cfgatev1alpha1.AccessRule) ([]cloudflare.AccessRuleParam, error) {
	return convertAccessRulesWithServiceTokens(crdRules, nil)
}

func convertAccessRulesWithServiceTokens(crdRules []cfgatev1alpha1.AccessRule, serviceTokenIDs map[string]string) ([]cloudflare.AccessRuleParam, error) {
	var rules []cloudflare.AccessRuleParam
	for _, r := range crdRules {
		switch {
		case r.IP != nil:
			for _, cidr := range r.IP.Ranges {
				value := cidr
				rules = append(rules, cloudflare.AccessRuleParam{IPRange: &value})
			}
		case r.IPList != nil:
			if r.IPList.ID == "" {
				return nil, fmt.Errorf("ipList rule specifies name %q without listID; listID is required for IP list rules", r.IPList.Name)
			}
			value := r.IPList.ID
			rules = append(rules, cloudflare.AccessRuleParam{IPListID: &value})
		case r.Country != nil:
			for _, code := range r.Country.Codes {
				value := code
				rules = append(rules, cloudflare.AccessRuleParam{Country: &value})
			}
		case r.Everyone != nil && *r.Everyone:
			value := true
			rules = append(rules, cloudflare.AccessRuleParam{Everyone: &value})
		case r.ServiceToken != nil:
			tokenID := r.ServiceToken.TokenID
			if tokenID == "" && r.ServiceToken.Name != "" {
				var ok bool
				tokenID, ok = serviceTokenIDs[r.ServiceToken.Name]
				if !ok || tokenID == "" {
					return nil, fmt.Errorf("service token %q is not ready", r.ServiceToken.Name)
				}
			}
			if tokenID == "" {
				return nil, fmt.Errorf("serviceToken rule must specify tokenId or name")
			}
			rules = append(rules, cloudflare.AccessRuleParam{ServiceTokenID: &tokenID})
		case r.AnyValidServiceToken != nil && *r.AnyValidServiceToken:
			value := true
			rules = append(rules, cloudflare.AccessRuleParam{AnyValidServiceToken: &value})
		case r.Email != nil:
			for _, addr := range r.Email.Addresses {
				value := addr
				rules = append(rules, cloudflare.AccessRuleParam{Email: &value})
			}
		case r.EmailList != nil:
			if r.EmailList.ID == "" {
				return nil, fmt.Errorf("emailList rule specifies name %q without listID; listID is required for email list rules", r.EmailList.Name)
			}
			value := r.EmailList.ID
			rules = append(rules, cloudflare.AccessRuleParam{EmailListID: &value})
		case r.EmailDomain != nil:
			value := r.EmailDomain.Domain
			rules = append(rules, cloudflare.AccessRuleParam{EmailDomain: &value})
		case r.OIDCClaim != nil:
			rules = append(rules, cloudflare.AccessRuleParam{OIDCClaim: &cloudflare.OIDCClaimParam{
				IdentityProviderID: r.OIDCClaim.IdentityProviderID,
				ClaimName:          r.OIDCClaim.ClaimName,
				ClaimValue:         r.OIDCClaim.ClaimValue,
			}})
		case r.GSuiteGroup != nil:
			rules = append(rules, cloudflare.AccessRuleParam{GSuiteGroup: &cloudflare.GSuiteGroupParam{
				IdentityProviderID: r.GSuiteGroup.IdentityProviderID,
				Email:              r.GSuiteGroup.Email,
			}})
		case r.Group != nil:
			value := r.Group.ID
			rules = append(rules, cloudflare.AccessRuleParam{GroupID: &value})
		}
	}
	return rules, nil
}

func convertApprovalGroups(groups []cfgatev1alpha1.ApprovalGroup) []cloudflare.ApprovalGroupParam {
	var result []cloudflare.ApprovalGroupParam
	for _, g := range groups {
		result = append(result, cloudflare.ApprovalGroupParam{
			EmailAddresses:  g.Emails,
			EmailListUUID:   g.EmailListUUID,
			ApprovalsNeeded: g.ApprovalsNeeded,
		})
	}
	return result
}

func (r *CloudflareAccessPolicyReconciler) syncServiceTokens(ctx context.Context, accessService *cloudflare.AccessService, accountID string, policy *cfgatev1alpha1.CloudflareAccessPolicy) error {
	if policy.Status.ServiceTokenIDs == nil {
		if len(policy.Spec.ServiceTokens) == 0 {
			return nil
		}
		policy.Status.ServiceTokenIDs = make(map[string]string)
	}
	desired := make(map[string]struct{}, len(policy.Spec.ServiceTokens))
	for _, tokenConfig := range policy.Spec.ServiceTokens {
		desired[tokenConfig.Name] = struct{}{}
	}
	for name, tokenID := range policy.Status.ServiceTokenIDs {
		if _, ok := desired[name]; ok {
			continue
		}
		if err := accessService.Client().DeleteServiceToken(ctx, accountID, tokenID); err != nil {
			return fmt.Errorf("failed to delete removed service token %s (%s): %w", name, tokenID, err)
		}
		delete(policy.Status.ServiceTokenIDs, name)
	}
	for _, tokenConfig := range policy.Spec.ServiceTokens {
		secretWriter := &k8sSecretWriter{
			client:    r.Client,
			namespace: policy.Namespace,
			secretRef: tokenConfig.SecretRef,
			owner:     policy,
			scheme:    r.Scheme,
		}
		token, err := accessService.EnsureServiceToken(ctx, accountID, cloudflare.ServiceTokenParams{
			Name:     tokenConfig.Name,
			Duration: tokenConfig.Duration,
		}, secretWriter)
		if err != nil {
			return fmt.Errorf("failed to ensure service token %s: %w", tokenConfig.Name, err)
		}
		policy.Status.ServiceTokenIDs[tokenConfig.Name] = token.ID
	}
	return nil
}

type k8sSecretWriter struct {
	client    client.Client
	namespace string
	secretRef cfgatev1alpha1.ServiceTokenSecretRef
	owner     *cfgatev1alpha1.CloudflareAccessPolicy
	scheme    *runtime.Scheme
}

func (w *k8sSecretWriter) WriteSecret(ctx context.Context, name string, data map[string][]byte) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: w.secretRef.Name, Namespace: w.namespace},
		Type:       corev1.SecretTypeOpaque,
		Data:       data,
	}
	if err := controllerutil.SetControllerReference(w.owner, secret, w.scheme); err != nil {
		return fmt.Errorf("setting owner reference: %w", err)
	}
	existing := &corev1.Secret{}
	err := w.client.Get(ctx, client.ObjectKeyFromObject(secret), existing)
	if apierrors.IsNotFound(err) {
		return w.client.Create(ctx, secret)
	}
	if err != nil {
		return err
	}
	if err := controllerutil.SetControllerReference(w.owner, existing, w.scheme); err != nil {
		var ownedErr *controllerutil.AlreadyOwnedError
		if errors.As(err, &ownedErr) {
			return fmt.Errorf("secret %s/%s is already controlled by %s/%s", existing.Namespace, existing.Name, ownedErr.Owner.Kind, ownedErr.Owner.Name)
		}
		return fmt.Errorf("setting owner reference: %w", err)
	}
	existing.Data = data
	return w.client.Update(ctx, existing)
}

func (w *k8sSecretWriter) ServiceTokenSecretNeedsRefresh(ctx context.Context, _ string, clientID string) (bool, error) {
	var secret corev1.Secret
	err := w.client.Get(ctx, types.NamespacedName{Name: w.secretRef.Name, Namespace: w.namespace}, &secret)
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	storedClientID := secret.Data["CF_ACCESS_CLIENT_ID"]
	storedClientSecret := secret.Data["CF_ACCESS_CLIENT_SECRET"]
	if len(storedClientID) == 0 || len(storedClientSecret) == 0 {
		return true, nil
	}
	return string(storedClientID) != clientID, nil
}

func (r *CloudflareAccessPolicyReconciler) reconcileDelete(ctx context.Context, policy *cfgatev1alpha1.CloudflareAccessPolicy) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(policy, accessPolicyFinalizer) {
		return ctrl.Result{}, nil
	}
	if policy.Annotations["cfgate.io/deletion-policy"] == "orphan" {
		return r.removeFinalizer(ctx, policy)
	}
	creds, err := r.resolvePolicyDeletionCredentials(ctx, policy)
	if err != nil {
		return r.blockAccessDeletion(ctx, policy, fmt.Sprintf("Failed to resolve credentials: %s", err.Error()))
	}
	if policy.Status.PolicyID != "" {
		cfPolicy, err := creds.Service.Client().GetAccessPolicy(ctx, creds.AccountID, policy.Status.PolicyID)
		if err != nil {
			return r.blockAccessDeletion(ctx, policy, fmt.Sprintf("Failed to get Access policy %s: %s", policy.Status.PolicyID, err.Error()))
		}
		if cfPolicy != nil && cfPolicy.AppCount > 0 {
			return r.blockAccessDeletion(ctx, policy, fmt.Sprintf("Access policy %s is still linked to %d application(s)", policy.Status.PolicyID, cfPolicy.AppCount))
		}
		if err := creds.Service.Client().DeleteAccessPolicy(ctx, creds.AccountID, policy.Status.PolicyID); err != nil {
			return r.blockAccessDeletion(ctx, policy, fmt.Sprintf("Failed to delete Access policy %s: %s", policy.Status.PolicyID, err.Error()))
		}
	}
	for name, tokenID := range policy.Status.ServiceTokenIDs {
		if err := creds.Service.Client().DeleteServiceToken(ctx, creds.AccountID, tokenID); err != nil {
			return r.blockAccessDeletion(ctx, policy, fmt.Sprintf("Failed to revoke service token %s (%s): %s", name, tokenID, err.Error()))
		}
	}
	return r.removeFinalizer(ctx, policy)
}

func (r *CloudflareAccessPolicyReconciler) resolvePolicyDeletionCredentials(ctx context.Context, policy *cfgatev1alpha1.CloudflareAccessPolicy) (*accessPolicyCredentials, error) {
	var cachedErr error
	if policy.Status.AccountID != "" && policy.Status.CredentialSecretRef != nil && policy.Status.CredentialSecretRef.Name != "" {
		secretNamespace := policy.Status.CredentialSecretRef.Namespace
		secretRef := cfgatev1alpha1.CloudflareSecretRef{
			Name:      policy.Status.CredentialSecretRef.Name,
			Namespace: &secretNamespace,
			AccountID: policy.Status.AccountID,
		}
		creds, err := r.resolveCloudflareRefCredentials(ctx, policy.Namespace, &secretRef)
		if err == nil {
			return creds, nil
		}
		cachedErr = err
	}

	specRef := policy.Spec.CloudflareRef
	if policy.Status.AccountID != "" {
		specRef.AccountID = policy.Status.AccountID
		specRef.AccountName = ""
	}
	creds, err := r.resolveCloudflareRefCredentials(ctx, policy.Namespace, &specRef)
	if err == nil {
		return creds, nil
	}
	if cachedErr != nil {
		return nil, fmt.Errorf("cached credentials failed: %v; spec credentials failed: %w", cachedErr, err)
	}
	return nil, err
}

func (r *CloudflareAccessPolicyReconciler) blockAccessDeletion(ctx context.Context, policy *cfgatev1alpha1.CloudflareAccessPolicy, detail string) (ctrl.Result, error) {
	retryElapsed := time.Since(policy.DeletionTimestamp.Time)
	suffix := " Set annotation cfgate.io/deletion-policy=orphan to skip cleanup and remove finalizer."
	reason := "CleanupFailed"
	message := detail + "." + suffix
	if retryElapsed >= accessDeletionRetryBudget {
		reason = "CleanupBlocked"
		message = fmt.Sprintf("%s (blocked after %s of retries).%s", detail, retryElapsed.Round(time.Second), suffix)
	}
	if r.Recorder != nil {
		r.Recorder.Eventf(policy, nil, corev1.EventTypeWarning, reason, "Delete", "%s", message)
	}
	return ctrl.Result{RequeueAfter: accessDeletionRequeueInterval}, nil
}

func (r *CloudflareAccessPolicyReconciler) removeFinalizer(ctx context.Context, policy *cfgatev1alpha1.CloudflareAccessPolicy) (ctrl.Result, error) {
	patch := client.MergeFrom(policy.DeepCopy())
	controllerutil.RemoveFinalizer(policy, accessPolicyFinalizer)
	if err := r.Patch(ctx, policy, patch); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to remove finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}

func (r *CloudflareAccessPolicyReconciler) updateStatus(ctx context.Context, policy *cfgatev1alpha1.CloudflareAccessPolicy) error {
	var current cfgatev1alpha1.CloudflareAccessPolicy
	if err := r.Get(ctx, types.NamespacedName{Name: policy.Name, Namespace: policy.Namespace}, &current); err != nil {
		return fmt.Errorf("failed to re-fetch policy: %w", err)
	}
	if accessPolicyStatusEqual(&current.Status, &policy.Status) {
		return nil
	}
	current.Status = policy.Status
	return r.Status().Update(ctx, &current)
}

func accessPolicyStatusEqual(a, b *cfgatev1alpha1.CloudflareAccessPolicyStatus) bool {
	if a.PolicyID != b.PolicyID || a.AccountID != b.AccountID || a.Reusable != b.Reusable ||
		a.AppCount != b.AppCount || a.ObservedGeneration != b.ObservedGeneration {
		return false
	}
	if !reflect.DeepEqual(a.ServiceTokenIDs, b.ServiceTokenIDs) {
		return false
	}
	if !reflect.DeepEqual(a.CredentialSecretRef, b.CredentialSecretRef) {
		return false
	}
	return conditionsEqual(a.Conditions, b.Conditions)
}

func conditionsEqual(a, b []metav1.Condition) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Type != b[i].Type || a[i].Status != b[i].Status ||
			a[i].Reason != b[i].Reason || a[i].Message != b[i].Message {
			return false
		}
	}
	return true
}

func (r *CloudflareAccessPolicyReconciler) getCloudflareClient(ctx context.Context, policyNamespace string, secretRef *cfgatev1alpha1.CloudflareSecretRef) (cloudflare.Client, error) {
	if r.CFClient != nil {
		return r.CFClient, nil
	}
	secretNamespace := policyNamespace
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

func (r *CloudflareAccessPolicyReconciler) createClientFromSecret(secret *corev1.Secret) (cloudflare.Client, error) {
	token, ok := secret.Data["CLOUDFLARE_API_TOKEN"]
	if !ok {
		return nil, fmt.Errorf("API token key %q not found in secret", "CLOUDFLARE_API_TOKEN")
	}
	return cloudflare.NewClient(string(token))
}

func (r *CloudflareAccessPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(ctrlcontroller.Options{MaxConcurrentReconciles: 4}).
		For(&cfgatev1alpha1.CloudflareAccessPolicy{}, builder.WithPredicates(GenerationOrDeletionPredicate)).
		Owns(&corev1.Secret{}).
		Complete(r)
}
