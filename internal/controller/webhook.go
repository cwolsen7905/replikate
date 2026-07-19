package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// SelectorWebhookPath is the URL path the validating webhook is served on.
const SelectorWebhookPath = "/validate-sync-selector"

// SelectorValidator is an admission webhook that rejects a ConfigMap or Secret
// whose sync annotation carries a namespace selector that does not parse. It
// turns what the controller would otherwise only log (and skip) at reconcile
// time into an apply-time error, so the mistake surfaces to whoever applied it.
type SelectorValidator struct {
	Keys Keys
}

// Handle validates the incoming object's sync-annotation selector. Objects
// without the annotation, and objects whose selector parses, are allowed.
func (v *SelectorValidator) Handle(_ context.Context, req admission.Request) admission.Response {
	// Only the object's annotations matter, so decode just those — this keeps
	// the handler independent of the object kind (ConfigMap or Secret).
	var obj struct {
		Metadata struct {
			Annotations map[string]string `json:"annotations"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(req.Object.Raw, &obj); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	value, ok := obj.Metadata.Annotations[v.Keys.SyncAnnotation]
	if !ok {
		return admission.Allowed("not a Replikate source")
	}
	if _, err := labels.Parse(value); err != nil {
		return admission.Denied(fmt.Sprintf(
			"invalid %s selector %q: %v", v.Keys.SyncAnnotation, value, err))
	}
	return admission.Allowed("valid sync selector")
}
