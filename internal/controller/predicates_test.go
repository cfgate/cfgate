package controller

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/event"

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
)

// ---------------------------------------------------------------------------
// cfgateAnnotationsChanged
// ---------------------------------------------------------------------------

func TestCfgateAnnotationsChanged(t *testing.T) {
	tests := []struct {
		name string
		old  map[string]string
		new  map[string]string
		want bool
	}{
		{
			name: "both nil",
			old:  nil,
			new:  nil,
			want: false,
		},
		{
			name: "both empty",
			old:  map[string]string{},
			new:  map[string]string{},
			want: false,
		},
		{
			name: "cfgate annotation added",
			old:  map[string]string{},
			new:  map[string]string{"cfgate.io/ttl": "300"},
			want: true,
		},
		{
			name: "cfgate annotation removed",
			old:  map[string]string{"cfgate.io/ttl": "300"},
			new:  map[string]string{},
			want: true,
		},
		{
			name: "cfgate annotation value changed",
			old:  map[string]string{"cfgate.io/ttl": "300"},
			new:  map[string]string{"cfgate.io/ttl": "600"},
			want: true,
		},
		{
			name: "cfgate annotation unchanged",
			old:  map[string]string{"cfgate.io/ttl": "300"},
			new:  map[string]string{"cfgate.io/ttl": "300"},
			want: false,
		},
		{
			name: "non-cfgate annotation changed",
			old:  map[string]string{"helm.sh/x": "a"},
			new:  map[string]string{"helm.sh/x": "b"},
			want: false,
		},
		{
			name: "non-cfgate annotation added",
			old:  map[string]string{},
			new:  map[string]string{"kubectl.io/x": "a"},
			want: false,
		},
		{
			name: "cfgate unchanged and non-cfgate changed",
			old:  map[string]string{"cfgate.io/ttl": "1", "helm.sh/x": "a"},
			new:  map[string]string{"cfgate.io/ttl": "1", "helm.sh/x": "b"},
			want: false,
		},
		{
			name: "cfgate changed and non-cfgate unchanged",
			old:  map[string]string{"cfgate.io/ttl": "1", "helm.sh/x": "a"},
			new:  map[string]string{"cfgate.io/ttl": "2", "helm.sh/x": "a"},
			want: true,
		},
		{
			name: "multiple cfgate annotations with one changed",
			old:  map[string]string{"cfgate.io/ttl": "1", "cfgate.io/origin-protocol": "http"},
			new:  map[string]string{"cfgate.io/ttl": "2", "cfgate.io/origin-protocol": "http"},
			want: true,
		},
		{
			name: "nil old with cfgate in new",
			old:  nil,
			new:  map[string]string{"cfgate.io/ttl": "1"},
			want: true,
		},
		{
			name: "cfgate in old with nil new",
			old:  map[string]string{"cfgate.io/ttl": "1"},
			new:  nil,
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cfgateAnnotationsChanged(tt.old, tt.new)
			if got != tt.want {
				t.Errorf("cfgateAnnotationsChanged() = %v, want %v", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// CfgateAnnotationOrGenerationPredicate
// ---------------------------------------------------------------------------

func TestCfgateAnnotationOrGenerationPredicate(t *testing.T) {
	t.Run("update: generation changed", func(t *testing.T) {
		old := &unstructured.Unstructured{}
		old.SetGeneration(1)
		old.SetAnnotations(map[string]string{})
		new := &unstructured.Unstructured{}
		new.SetGeneration(2)
		new.SetAnnotations(map[string]string{})

		got := CfgateAnnotationOrGenerationPredicate.Update(event.UpdateEvent{
			ObjectOld: old, ObjectNew: new,
		})
		if !got {
			t.Error("expected true when generation changed")
		}
	})

	t.Run("update: cfgate annotation added", func(t *testing.T) {
		old := &unstructured.Unstructured{}
		old.SetGeneration(1)
		old.SetAnnotations(map[string]string{})
		new := &unstructured.Unstructured{}
		new.SetGeneration(1)
		new.SetAnnotations(map[string]string{"cfgate.io/ttl": "300"})

		got := CfgateAnnotationOrGenerationPredicate.Update(event.UpdateEvent{
			ObjectOld: old, ObjectNew: new,
		})
		if !got {
			t.Error("expected true when cfgate annotation added")
		}
	})

	t.Run("update: no change", func(t *testing.T) {
		old := &unstructured.Unstructured{}
		old.SetGeneration(1)
		old.SetAnnotations(map[string]string{"cfgate.io/ttl": "300"})
		new := &unstructured.Unstructured{}
		new.SetGeneration(1)
		new.SetAnnotations(map[string]string{"cfgate.io/ttl": "300"})

		got := CfgateAnnotationOrGenerationPredicate.Update(event.UpdateEvent{
			ObjectOld: old, ObjectNew: new,
		})
		if got {
			t.Error("expected false when nothing changed")
		}
	})

	t.Run("update: non-cfgate annotation changed only", func(t *testing.T) {
		old := &unstructured.Unstructured{}
		old.SetGeneration(1)
		old.SetAnnotations(map[string]string{"helm.sh/x": "a"})
		new := &unstructured.Unstructured{}
		new.SetGeneration(1)
		new.SetAnnotations(map[string]string{"helm.sh/x": "b"})

		got := CfgateAnnotationOrGenerationPredicate.Update(event.UpdateEvent{
			ObjectOld: old, ObjectNew: new,
		})
		if got {
			t.Error("expected false when only non-cfgate annotation changed")
		}
	})

	t.Run("update: both generation and cfgate annotation changed", func(t *testing.T) {
		old := &unstructured.Unstructured{}
		old.SetGeneration(1)
		old.SetAnnotations(map[string]string{"cfgate.io/ttl": "1"})
		new := &unstructured.Unstructured{}
		new.SetGeneration(2)
		new.SetAnnotations(map[string]string{"cfgate.io/ttl": "2"})

		got := CfgateAnnotationOrGenerationPredicate.Update(event.UpdateEvent{
			ObjectOld: old, ObjectNew: new,
		})
		if !got {
			t.Error("expected true when both changed")
		}
	})

	t.Run("update: cfgate annotation removed", func(t *testing.T) {
		old := &unstructured.Unstructured{}
		old.SetGeneration(1)
		old.SetAnnotations(map[string]string{"cfgate.io/ttl": "300"})
		new := &unstructured.Unstructured{}
		new.SetGeneration(1)
		new.SetAnnotations(map[string]string{})

		got := CfgateAnnotationOrGenerationPredicate.Update(event.UpdateEvent{
			ObjectOld: old, ObjectNew: new,
		})
		if !got {
			t.Error("expected true when cfgate annotation removed")
		}
	})

	t.Run("update: cfgate annotation value changed", func(t *testing.T) {
		old := &unstructured.Unstructured{}
		old.SetGeneration(1)
		old.SetAnnotations(map[string]string{"cfgate.io/origin-protocol": "http"})
		new := &unstructured.Unstructured{}
		new.SetGeneration(1)
		new.SetAnnotations(map[string]string{"cfgate.io/origin-protocol": "https"})

		got := CfgateAnnotationOrGenerationPredicate.Update(event.UpdateEvent{
			ObjectOld: old, ObjectNew: new,
		})
		if !got {
			t.Error("expected true when cfgate annotation value changed")
		}
	})

	t.Run("update: empty annotations both sides", func(t *testing.T) {
		old := &unstructured.Unstructured{}
		old.SetGeneration(1)
		new := &unstructured.Unstructured{}
		new.SetGeneration(1)

		got := CfgateAnnotationOrGenerationPredicate.Update(event.UpdateEvent{
			ObjectOld: old, ObjectNew: new,
		})
		if got {
			t.Error("expected false with empty annotations both sides")
		}
	})

	t.Run("create: passes all", func(t *testing.T) {
		obj := &unstructured.Unstructured{}
		got := CfgateAnnotationOrGenerationPredicate.Create(event.CreateEvent{Object: obj})
		if !got {
			t.Error("expected true for create event")
		}
	})

	t.Run("delete: passes all", func(t *testing.T) {
		obj := &unstructured.Unstructured{}
		got := CfgateAnnotationOrGenerationPredicate.Delete(event.DeleteEvent{Object: obj})
		if !got {
			t.Error("expected true for delete event")
		}
	})

	t.Run("generic: passes all", func(t *testing.T) {
		obj := &unstructured.Unstructured{}
		got := CfgateAnnotationOrGenerationPredicate.Generic(event.GenericEvent{Object: obj})
		if !got {
			t.Error("expected true for generic event")
		}
	})
}

// ---------------------------------------------------------------------------
// GenerationOrDeletionPredicate
// ---------------------------------------------------------------------------

func TestGenerationOrDeletionPredicate(t *testing.T) {
	now := metav1.NewTime(time.Now())

	t.Run("update: generation changed", func(t *testing.T) {
		old := &unstructured.Unstructured{}
		old.SetGeneration(1)
		new := &unstructured.Unstructured{}
		new.SetGeneration(2)

		got := GenerationOrDeletionPredicate.Update(event.UpdateEvent{
			ObjectOld: old, ObjectNew: new,
		})
		if !got {
			t.Error("expected true when generation changed")
		}
	})

	t.Run("update: deletion timestamp just set", func(t *testing.T) {
		old := &unstructured.Unstructured{}
		old.SetGeneration(1)
		new := &unstructured.Unstructured{}
		new.SetGeneration(1)
		new.SetDeletionTimestamp(&now)

		got := GenerationOrDeletionPredicate.Update(event.UpdateEvent{
			ObjectOld: old, ObjectNew: new,
		})
		if !got {
			t.Error("expected true when deletion timestamp just set")
		}
	})

	t.Run("update: deletion timestamp already existed", func(t *testing.T) {
		old := &unstructured.Unstructured{}
		old.SetGeneration(1)
		old.SetDeletionTimestamp(&now)
		new := &unstructured.Unstructured{}
		new.SetGeneration(1)
		new.SetDeletionTimestamp(&now)

		got := GenerationOrDeletionPredicate.Update(event.UpdateEvent{
			ObjectOld: old, ObjectNew: new,
		})
		if got {
			t.Error("expected false when deletion timestamp already existed on old")
		}
	})

	t.Run("update: neither changed", func(t *testing.T) {
		old := &unstructured.Unstructured{}
		old.SetGeneration(1)
		new := &unstructured.Unstructured{}
		new.SetGeneration(1)

		got := GenerationOrDeletionPredicate.Update(event.UpdateEvent{
			ObjectOld: old, ObjectNew: new,
		})
		if got {
			t.Error("expected false when neither generation nor deletion changed")
		}
	})

	t.Run("update: nil old object", func(t *testing.T) {
		new := &unstructured.Unstructured{}
		new.SetGeneration(1)

		got := GenerationOrDeletionPredicate.Update(event.UpdateEvent{
			ObjectOld: nil, ObjectNew: new,
		})
		if got {
			t.Error("expected false with nil old object")
		}
	})

	t.Run("update: nil new object", func(t *testing.T) {
		old := &unstructured.Unstructured{}
		old.SetGeneration(1)

		got := GenerationOrDeletionPredicate.Update(event.UpdateEvent{
			ObjectOld: old, ObjectNew: nil,
		})
		if got {
			t.Error("expected false with nil new object")
		}
	})

	t.Run("update: generation changed and deletion set", func(t *testing.T) {
		old := &unstructured.Unstructured{}
		old.SetGeneration(1)
		new := &unstructured.Unstructured{}
		new.SetGeneration(2)
		new.SetDeletionTimestamp(&now)

		got := GenerationOrDeletionPredicate.Update(event.UpdateEvent{
			ObjectOld: old, ObjectNew: new,
		})
		if !got {
			t.Error("expected true when both generation and deletion changed")
		}
	})

	t.Run("create: passes all", func(t *testing.T) {
		obj := &unstructured.Unstructured{}
		got := GenerationOrDeletionPredicate.Create(event.CreateEvent{Object: obj})
		if !got {
			t.Error("expected true for create event")
		}
	})

	t.Run("delete: passes all", func(t *testing.T) {
		obj := &unstructured.Unstructured{}
		got := GenerationOrDeletionPredicate.Delete(event.DeleteEvent{Object: obj})
		if !got {
			t.Error("expected true for delete event")
		}
	})

	t.Run("generic: passes all", func(t *testing.T) {
		obj := &unstructured.Unstructured{}
		got := GenerationOrDeletionPredicate.Generic(event.GenericEvent{Object: obj})
		if !got {
			t.Error("expected true for generic event")
		}
	})
}

// ---------------------------------------------------------------------------
// AccessPolicyReferenceChangedPredicate
// ---------------------------------------------------------------------------

func TestAccessPolicyReferenceChangedPredicate(t *testing.T) {
	t.Run("update: resource version changed", func(t *testing.T) {
		old := &unstructured.Unstructured{}
		old.SetGeneration(1)
		old.SetResourceVersion("1")
		new := &unstructured.Unstructured{}
		new.SetGeneration(1)
		new.SetResourceVersion("2")

		got := AccessPolicyReferenceChangedPredicate.Update(event.UpdateEvent{
			ObjectOld: old, ObjectNew: new,
		})
		if !got {
			t.Error("expected true when resource version changed")
		}
	})

	t.Run("update: resource version unchanged", func(t *testing.T) {
		old := &unstructured.Unstructured{}
		old.SetGeneration(1)
		old.SetResourceVersion("1")
		new := &unstructured.Unstructured{}
		new.SetGeneration(1)
		new.SetResourceVersion("1")

		got := AccessPolicyReferenceChangedPredicate.Update(event.UpdateEvent{
			ObjectOld: old, ObjectNew: new,
		})
		if got {
			t.Error("expected false when resource version did not change")
		}
	})
}

// ---------------------------------------------------------------------------
// DataResourceChangedPredicate
// ---------------------------------------------------------------------------

func TestDataResourceChangedPredicate(t *testing.T) {
	t.Run("configmap update: resource version changed with same generation", func(t *testing.T) {
		old := &corev1.ConfigMap{}
		old.SetResourceVersion("1")
		new := &corev1.ConfigMap{}
		new.SetResourceVersion("2")

		got := DataResourceChangedPredicate.Update(event.UpdateEvent{
			ObjectOld: old, ObjectNew: new,
		})
		if !got {
			t.Error("expected true when ConfigMap resource version changed")
		}
	})

	t.Run("secret update: resource version changed with same generation", func(t *testing.T) {
		old := &corev1.Secret{}
		old.SetResourceVersion("1")
		new := &corev1.Secret{}
		new.SetResourceVersion("2")

		got := DataResourceChangedPredicate.Update(event.UpdateEvent{
			ObjectOld: old, ObjectNew: new,
		})
		if !got {
			t.Error("expected true when Secret resource version changed")
		}
	})

	t.Run("update: resource version unchanged", func(t *testing.T) {
		old := &corev1.ConfigMap{}
		old.SetResourceVersion("1")
		new := &corev1.ConfigMap{}
		new.SetResourceVersion("1")

		got := DataResourceChangedPredicate.Update(event.UpdateEvent{
			ObjectOld: old, ObjectNew: new,
		})
		if got {
			t.Error("expected false when resource version did not change")
		}
	})
}

// ---------------------------------------------------------------------------
// TunnelIDChangedPredicate
// ---------------------------------------------------------------------------

func TestTunnelIDChangedPredicate(t *testing.T) {
	t.Run("create: tunnel with non-empty TunnelID", func(t *testing.T) {
		tunnel := &cfgatev1alpha1.CloudflareTunnel{
			Status: cfgatev1alpha1.CloudflareTunnelStatus{TunnelID: "abc-123"},
		}
		got := TunnelIDChangedPredicate.Create(event.CreateEvent{Object: tunnel})
		if !got {
			t.Error("expected true when TunnelID is non-empty")
		}
	})

	t.Run("create: tunnel with empty TunnelID", func(t *testing.T) {
		tunnel := &cfgatev1alpha1.CloudflareTunnel{}
		got := TunnelIDChangedPredicate.Create(event.CreateEvent{Object: tunnel})
		if got {
			t.Error("expected false when TunnelID is empty")
		}
	})

	t.Run("create: non-CloudflareTunnel object", func(t *testing.T) {
		obj := &unstructured.Unstructured{}
		got := TunnelIDChangedPredicate.Create(event.CreateEvent{Object: obj})
		if got {
			t.Error("expected false for non-CloudflareTunnel object")
		}
	})

	t.Run("update: TunnelID changed", func(t *testing.T) {
		old := &cfgatev1alpha1.CloudflareTunnel{
			Status: cfgatev1alpha1.CloudflareTunnelStatus{TunnelID: ""},
		}
		new := &cfgatev1alpha1.CloudflareTunnel{
			Status: cfgatev1alpha1.CloudflareTunnelStatus{TunnelID: "abc-123"},
		}
		got := TunnelIDChangedPredicate.Update(event.UpdateEvent{
			ObjectOld: old, ObjectNew: new,
		})
		if !got {
			t.Error("expected true when TunnelID changed")
		}
	})

	t.Run("update: TunnelID unchanged", func(t *testing.T) {
		old := &cfgatev1alpha1.CloudflareTunnel{
			Status: cfgatev1alpha1.CloudflareTunnelStatus{TunnelID: "abc-123"},
		}
		new := &cfgatev1alpha1.CloudflareTunnel{
			Status: cfgatev1alpha1.CloudflareTunnelStatus{TunnelID: "abc-123"},
		}
		got := TunnelIDChangedPredicate.Update(event.UpdateEvent{
			ObjectOld: old, ObjectNew: new,
		})
		if got {
			t.Error("expected false when TunnelID unchanged")
		}
	})

	t.Run("update: TunnelID cleared", func(t *testing.T) {
		old := &cfgatev1alpha1.CloudflareTunnel{
			Status: cfgatev1alpha1.CloudflareTunnelStatus{TunnelID: "abc-123"},
		}
		new := &cfgatev1alpha1.CloudflareTunnel{
			Status: cfgatev1alpha1.CloudflareTunnelStatus{TunnelID: ""},
		}
		got := TunnelIDChangedPredicate.Update(event.UpdateEvent{
			ObjectOld: old, ObjectNew: new,
		})
		if !got {
			t.Error("expected true when TunnelID cleared")
		}
	})

	t.Run("update: non-CloudflareTunnel old", func(t *testing.T) {
		old := &unstructured.Unstructured{}
		new := &cfgatev1alpha1.CloudflareTunnel{
			Status: cfgatev1alpha1.CloudflareTunnelStatus{TunnelID: "abc"},
		}
		got := TunnelIDChangedPredicate.Update(event.UpdateEvent{
			ObjectOld: old, ObjectNew: new,
		})
		if got {
			t.Error("expected false when old is not CloudflareTunnel")
		}
	})

	t.Run("update: non-CloudflareTunnel new", func(t *testing.T) {
		old := &cfgatev1alpha1.CloudflareTunnel{
			Status: cfgatev1alpha1.CloudflareTunnelStatus{TunnelID: "abc"},
		}
		new := &unstructured.Unstructured{}
		got := TunnelIDChangedPredicate.Update(event.UpdateEvent{
			ObjectOld: old, ObjectNew: new,
		})
		if got {
			t.Error("expected false when new is not CloudflareTunnel")
		}
	})

	t.Run("delete: always false", func(t *testing.T) {
		tunnel := &cfgatev1alpha1.CloudflareTunnel{
			Status: cfgatev1alpha1.CloudflareTunnelStatus{TunnelID: "abc-123"},
		}
		got := TunnelIDChangedPredicate.Delete(event.DeleteEvent{Object: tunnel})
		if got {
			t.Error("expected false for delete event")
		}
	})

	t.Run("generic: default passes", func(t *testing.T) {
		tunnel := &cfgatev1alpha1.CloudflareTunnel{
			Status: cfgatev1alpha1.CloudflareTunnelStatus{TunnelID: "abc-123"},
		}
		got := TunnelIDChangedPredicate.Generic(event.GenericEvent{Object: tunnel})
		if !got {
			t.Error("expected true for generic event (predicate.Funcs nil GenericFunc defaults to true)")
		}
	})
}

// ---------------------------------------------------------------------------
// GatewayCreateAnnotationFilter
// ---------------------------------------------------------------------------

func TestGatewayCreateAnnotationFilter(t *testing.T) {
	t.Run("create: no annotations", func(t *testing.T) {
		obj := &unstructured.Unstructured{}
		got := GatewayCreateAnnotationFilter.Create(event.CreateEvent{Object: obj})
		if got {
			t.Error("expected false with no annotations")
		}
	})

	t.Run("create: non-cfgate annotations only", func(t *testing.T) {
		obj := &unstructured.Unstructured{}
		obj.SetAnnotations(map[string]string{"helm.sh/x": "a"})
		got := GatewayCreateAnnotationFilter.Create(event.CreateEvent{Object: obj})
		if got {
			t.Error("expected false with only non-cfgate annotations")
		}
	})

	t.Run("create: one cfgate annotation", func(t *testing.T) {
		obj := &unstructured.Unstructured{}
		obj.SetAnnotations(map[string]string{"cfgate.io/tunnel-ref": "my-tunnel"})
		got := GatewayCreateAnnotationFilter.Create(event.CreateEvent{Object: obj})
		if !got {
			t.Error("expected true with cfgate annotation")
		}
	})

	t.Run("create: multiple cfgate annotations", func(t *testing.T) {
		obj := &unstructured.Unstructured{}
		obj.SetAnnotations(map[string]string{
			"cfgate.io/tunnel-ref":      "my-tunnel",
			"cfgate.io/origin-protocol": "https",
		})
		got := GatewayCreateAnnotationFilter.Create(event.CreateEvent{Object: obj})
		if !got {
			t.Error("expected true with multiple cfgate annotations")
		}
	})

	t.Run("create: mixed annotations including cfgate", func(t *testing.T) {
		obj := &unstructured.Unstructured{}
		obj.SetAnnotations(map[string]string{
			"cfgate.io/tunnel-ref": "my-tunnel",
			"helm.sh/x":            "a",
		})
		got := GatewayCreateAnnotationFilter.Create(event.CreateEvent{Object: obj})
		if !got {
			t.Error("expected true with mixed annotations including cfgate")
		}
	})

	t.Run("create: nil annotation map", func(t *testing.T) {
		obj := &unstructured.Unstructured{}
		obj.SetAnnotations(nil)
		got := GatewayCreateAnnotationFilter.Create(event.CreateEvent{Object: obj})
		if got {
			t.Error("expected false with nil annotation map")
		}
	})

	t.Run("update: always true", func(t *testing.T) {
		old := &unstructured.Unstructured{}
		new := &unstructured.Unstructured{}
		got := GatewayCreateAnnotationFilter.Update(event.UpdateEvent{
			ObjectOld: old, ObjectNew: new,
		})
		if !got {
			t.Error("expected true for update event")
		}
	})

	t.Run("delete: always true", func(t *testing.T) {
		obj := &unstructured.Unstructured{}
		got := GatewayCreateAnnotationFilter.Delete(event.DeleteEvent{Object: obj})
		if !got {
			t.Error("expected true for delete event")
		}
	})

	t.Run("generic: always true", func(t *testing.T) {
		obj := &unstructured.Unstructured{}
		got := GatewayCreateAnnotationFilter.Generic(event.GenericEvent{Object: obj})
		if !got {
			t.Error("expected true for generic event")
		}
	})
}
