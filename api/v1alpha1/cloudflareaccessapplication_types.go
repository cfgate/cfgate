package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// AccessPolicyReference references a reusable CloudflareAccessPolicy.
type AccessPolicyReference struct {
	// Name is the CloudflareAccessPolicy name.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`

	// Namespace is the CloudflareAccessPolicy namespace. Empty defaults to application namespace.
	// Cross-namespace references require ReferenceGrant.
	// +kubebuilder:default=""
	// +kubebuilder:validation:MaxLength=253
	Namespace string `json:"namespace"`

	// Precedence determines policy evaluation order for the application. Lower values run first.
	// When omitted, the controller uses list order starting at 1.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=9999
	Precedence *int `json:"precedence,omitempty"`
}

// AccessApplicationObserved records a Cloudflare Access Application created for one host/path target.
type AccessApplicationObserved struct {
	// ID is the Cloudflare Access Application ID.
	// +kubebuilder:validation:MaxLength=36
	ID string `json:"id,omitempty"`

	// AUD is the Application Audience Tag.
	// +kubebuilder:validation:MaxLength=255
	AUD string `json:"aud,omitempty"`

	// Domain is the protected hostname/path in Cloudflare.
	// +kubebuilder:validation:MaxLength=1024
	Domain string `json:"domain,omitempty"`

	// TargetRef identifies the Gateway API target that produced this application.
	TargetRef PolicyTargetReference `json:"targetRef,omitempty"`
}

// CloudflareAccessApplicationSpec defines Gateway API target bindings to reusable Access policies.
//
// +kubebuilder:validation:XValidation:rule="has(self.targetRef) || has(self.targetRefs)",message="either targetRef or targetRefs must be specified"
// +kubebuilder:validation:XValidation:rule="!(has(self.targetRef) && has(self.targetRefs))",message="targetRef and targetRefs are mutually exclusive"
// +kubebuilder:validation:XValidation:rule="self.policyRefs.all(p, !has(p.precedence)) || self.policyRefs.all(p, has(p.precedence))",message="policyRefs must either all omit precedence or all specify precedence"
// +kubebuilder:validation:XValidation:rule="self.policyRefs.all(p, !has(p.precedence)) || self.policyRefs.all(p, has(p.precedence) && self.policyRefs.exists_one(q, has(q.precedence) && q.precedence == p.precedence))",message="policyRefs precedence values must be unique"
type CloudflareAccessApplicationSpec struct {
	// TargetRef identifies a single Gateway API target.
	// +optional
	TargetRef *PolicyTargetReference `json:"targetRef,omitempty"`

	// TargetRefs identifies multiple Gateway API targets.
	// +optional
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	TargetRefs []PolicyTargetReference `json:"targetRefs,omitempty"`

	// CloudflareRef references Cloudflare credentials. When omitted, credentials
	// are inherited from the target Gateway's tunnel binding.
	// +optional
	CloudflareRef *CloudflareSecretRef `json:"cloudflareRef,omitempty"`

	// Application defines Access Application settings shared by generated apps.
	// The path field overrides any path derived from HTTPRoute rules.
	Application AccessApplication `json:"application,omitempty"`

	// PolicyRefs lists reusable CloudflareAccessPolicy resources to attach.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	// +listType=map
	// +listMapKey=name
	// +listMapKey=namespace
	PolicyRefs []AccessPolicyReference `json:"policyRefs"`
}

// CloudflareAccessApplicationStatus defines observed Access application state.
type CloudflareAccessApplicationStatus struct {
	// Applications are Cloudflare Access Applications managed by this resource.
	// +kubebuilder:validation:MaxItems=64
	Applications []AccessApplicationObserved `json:"applications,omitempty"`

	// AccountID is the resolved Cloudflare account ID used for Access application cleanup.
	// +kubebuilder:validation:MaxLength=32
	AccountID string `json:"accountId,omitempty"`

	// CredentialSecretRef is the resolved credentials Secret used for cleanup.
	// The namespace is always stored explicitly.
	CredentialSecretRef *SecretReference `json:"credentialSecretRef,omitempty"`

	// AttachedTargets is the count of successfully attached Gateway API targets.
	AttachedTargets int32 `json:"attachedTargets,omitempty"`

	// ObservedGeneration is the last generation processed.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Ancestors contains status for each targetRef.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	Ancestors []PolicyAncestorStatus `json:"ancestors,omitempty"`

	// Conditions describe current state.
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// CloudflareAccessApplication binds Gateway API targets to reusable Cloudflare Access policies.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=cfaa;cfaccessapp
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Targets",type="integer",JSONPath=".status.attachedTargets"
// +kubebuilder:printcolumn:name="Application",type="string",JSONPath=".status.applications[0].id"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type CloudflareAccessApplication struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CloudflareAccessApplicationSpec   `json:"spec,omitempty"`
	Status CloudflareAccessApplicationStatus `json:"status,omitempty"`
}

// CloudflareAccessApplicationList contains a list of CloudflareAccessApplication resources.
//
// +kubebuilder:object:root=true
type CloudflareAccessApplicationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CloudflareAccessApplication `json:"items"`
}

func init() {
	SchemeBuilder.Register(&CloudflareAccessApplication{}, &CloudflareAccessApplicationList{})
}
