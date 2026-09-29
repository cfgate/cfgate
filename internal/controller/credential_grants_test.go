package controller

import (
	cfg "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/cloudflare"
	"cfgate.io/cfgate/internal/cloudflared"
	"context"
	"errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	g "sigs.k8s.io/gateway-api/apis/v1beta1"
	"testing"
)

func credentialGrant(fromNS, toNS, name string, kinds ...string) *g.ReferenceGrant {
	grant := &g.ReferenceGrant{ObjectMeta: metav1.ObjectMeta{Name: "credentials-" + name, Namespace: toNS}, Spec: g.ReferenceGrantSpec{To: []g.ReferenceGrantTo{{Group: "", Kind: "Secret", Name: ptr.To(g.ObjectName(name))}}}}
	for _, kind := range kinds {
		grant.Spec.From = append(grant.Spec.From, g.ReferenceGrantFrom{Group: "cfgate.io", Kind: g.Kind(kind), Namespace: g.Namespace(fromNS)})
	}
	return grant
}

type secretReadRecorder struct {
	client.Client
	reads int
}

func (r *secretReadRecorder) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, ok := obj.(*corev1.Secret); ok {
		r.reads++
	}
	return r.Client.Get(ctx, key, obj, opts...)
}

func TestCredentialGrantsBeforeSecretRead(t *testing.T) {
	for _, kind := range []string{"CloudflareTunnel", "CloudflareDNS", "CloudflareAccessApplication", "CloudflareAccessPolicy"} {
		t.Run(kind, func(t *testing.T) {
			secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "key", Namespace: "infra", UID: "uid", ResourceVersion: "1"}, Data: map[string][]byte{"custom": []byte("test-token")}}
			scheme := controllerTestScheme(t)
			if err := g.Install(scheme); err != nil {
				t.Fatal(err)
			}
			recorder := &secretReadRecorder{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()}
			ref := &cfg.CloudflareSecretRef{Name: "key", Namespace: ptr.To("infra"), SecretKeys: cfg.SecretKeys{APIToken: "custom"}}
			cloudflareConfig := cfg.CloudflareConfig{SecretRef: cfg.SecretRef{Name: "key", Namespace: "infra"}, SecretKeys: cfg.SecretKeys{APIToken: "custom"}}
			get := func() (cloudflare.Client, error) {
				switch kind {
				case "CloudflareTunnel":
					return (&CloudflareTunnelReconciler{Client: recorder}).getCloudflareClient(context.Background(), &cfg.CloudflareTunnel{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant"}, Spec: cfg.CloudflareTunnelSpec{Cloudflare: cloudflareConfig}})
				case "CloudflareDNS":
					return (&CloudflareDNSReconciler{Client: recorder}).getCloudflareClient(context.Background(), &cfg.CloudflareDNS{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant"}, Spec: cfg.CloudflareDNSSpec{Cloudflare: &cloudflareConfig}}, nil)
				case "CloudflareAccessApplication":
					return (&CloudflareAccessApplicationReconciler{Client: recorder}).getCloudflareClient(context.Background(), "tenant", ref)
				default:
					return (&CloudflareAccessPolicyReconciler{Client: recorder}).getCloudflareClient(context.Background(), "tenant", ref)
				}
			}
			if _, err := get(); !errors.Is(err, errReferenceNotPermitted) {
				t.Fatalf("missing grant error=%v", err)
			}
			if recorder.reads != 0 {
				t.Fatal("denied reference read Secret")
			}
			wrong := credentialGrant("other", "infra", "key", kind)
			if err := recorder.Create(context.Background(), wrong); err != nil {
				t.Fatal(err)
			}
			if _, err := get(); !errors.Is(err, errReferenceNotPermitted) {
				t.Fatalf("wrong source grant error=%v", err)
			}
			if recorder.reads != 0 {
				t.Fatal("wrong source grant read Secret")
			}
			wrong.Spec.From[0].Namespace = "tenant"
			if err := recorder.Update(context.Background(), wrong); err != nil {
				t.Fatal(err)
			}
			if _, err := get(); err != nil {
				t.Fatal(err)
			}
			if recorder.reads != 1 {
				t.Fatalf("allowed Secret reads=%d", recorder.reads)
			}
			if err := recorder.Delete(context.Background(), wrong); err != nil {
				t.Fatal(err)
			}
			if _, err := get(); !errors.Is(err, errReferenceNotPermitted) {
				t.Fatalf("revoked grant error=%v", err)
			}
			if recorder.reads != 1 {
				t.Fatal("revocation did not stop Secret use")
			}
		})
	}
}

func TestConnectorObjectsRequireOwnerUID(t *testing.T) {
	for _, kind := range []string{"Secret", "Deployment"} {
		t.Run(kind, func(t *testing.T) {
			r, tunnel, _ := lifecycleFixture(t)
			ctx := context.Background()
			var object client.Object
			if kind == "Secret" {
				object = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: cloudflared.TokenSecretName(tunnel.Name), Namespace: tunnel.Namespace}}
			} else {
				object = &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: cloudflared.DeploymentName(tunnel.Name), Namespace: tunnel.Namespace}}
			}
			if err := r.Get(ctx, client.ObjectKeyFromObject(object), object); err != nil {
				t.Fatal(err)
			}
			object.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: cfg.GroupVersion.String(), Kind: "CloudflareTunnel", Name: tunnel.Name, UID: "foreign", Controller: ptr.To(true)}})
			if err := r.Update(ctx, object); err != nil {
				t.Fatal(err)
			}
			version := object.GetResourceVersion()
			if err := r.deployCloudflared(ctx, tunnel); err == nil {
				t.Fatal("foreign connector object accepted")
			}
			if err := r.Get(ctx, client.ObjectKeyFromObject(object), object); err != nil {
				t.Fatal(err)
			}
			if object.GetResourceVersion() != version {
				t.Fatal("foreign object was mutated")
			}
		})
	}
}
