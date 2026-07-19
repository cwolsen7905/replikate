package controller

import (
	"context"
	"encoding/json"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// admissionReqFor builds an admission request carrying cm as the incoming object.
func admissionReqFor(t *testing.T, cm *corev1.ConfigMap) admission.Request {
	t.Helper()
	raw, err := json.Marshal(cm)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
		Object: runtime.RawExtension{Raw: raw},
	}}
}

func TestSelectorValidator(t *testing.T) {
	v := &SelectorValidator{Keys: testKeys}

	cases := []struct {
		name        string
		annotations map[string]string
		wantAllowed bool
	}{
		{"no annotation", nil, true},
		{"valid selector", map[string]string{testKeys.SyncAnnotation: "team=web"}, true},
		{"empty selector means all namespaces", map[string]string{testKeys.SyncAnnotation: ""}, true},
		{"invalid selector", map[string]string{testKeys.SyncAnnotation: "a=b=c"}, false},
		{"malformed operator", map[string]string{testKeys.SyncAnnotation: "team in web"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
				Name:        "cfg",
				Namespace:   "default",
				Annotations: tc.annotations,
			}}
			resp := v.Handle(context.Background(), admissionReqFor(t, cm))
			if resp.Allowed != tc.wantAllowed {
				t.Errorf("allowed = %v, want %v (message: %q)",
					resp.Allowed, tc.wantAllowed, resp.Result.Message)
			}
		})
	}
}

func TestSelectorValidator_RejectsMalformedObject(t *testing.T) {
	v := &SelectorValidator{Keys: testKeys}
	req := admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
		Object: runtime.RawExtension{Raw: []byte("{not json")},
	}}
	resp := v.Handle(context.Background(), req)
	if resp.Allowed {
		t.Error("expected malformed object to be rejected")
	}
}
