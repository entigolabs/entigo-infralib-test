package k8s

import (
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ObjectError is returned while an object exists but has not been judged available.
type ObjectError struct{ object *unstructured.Unstructured }

func (err ObjectError) Error() string { return "Object is nil" }

// ProviderNotAvailable is returned while a Crossplane Provider is not Healthy and Installed.
type ProviderNotAvailable struct{ provider *unstructured.Unstructured }

func (err ProviderNotAvailable) Error() string {
	status := getStatusMap(err.provider)
	return fmt.Sprintf("Provider %s is not available, healthy: %s, installed: %s", err.provider.GetName(), status["Healthy"], status["Installed"])
}

// CrossplaneObjectNotAvailable is returned while a managed resource is not Ready and Synced.
type CrossplaneObjectNotAvailable struct{ object *unstructured.Unstructured }

func (err CrossplaneObjectNotAvailable) Error() string {
	status := getStatusMap(err.object)
	return fmt.Sprintf("%s %s is not available, ready: %s, synced: %s", err.object.GetKind(), err.object.GetName(), status["Ready"], status["Synced"])
}

// IngressNotAvailable is returned while an Ingress has no load balancer hostname.
type IngressNotAvailable struct{ ingress *unstructured.Unstructured }

func (err IngressNotAvailable) Error() string {
	return fmt.Sprintf("Ingress %s hostname has not been set", err.ingress.GetName())
}

// GatewayNotAvailable is returned while a Gateway has no address.
type GatewayNotAvailable struct{ gateway *unstructured.Unstructured }

func (err GatewayNotAvailable) Error() string {
	return fmt.Sprintf("Gateway %s address has not been set", err.gateway.GetName())
}

// HTTPRouteNotAvailable is returned while an HTTPRoute is not Accepted with resolved refs.
type HTTPRouteNotAvailable struct{ httpRoute *unstructured.Unstructured }

func (err HTTPRouteNotAvailable) Error() string {
	return fmt.Sprintf("HTTPRoute %s is not available (Accepted or ResolvedRefs not True)", err.httpRoute.GetName())
}

// NewObjectError builds the error reported while an object is not yet available.
type NewObjectError func(object *unstructured.Unstructured) error

func DefaultObjectError(object *unstructured.Unstructured) error { return ObjectError{object} }
func NewProviderNotAvailable(provider *unstructured.Unstructured) error {
	return ProviderNotAvailable{provider}
}
func NewCrossplaneObjectNotAvailable(object *unstructured.Unstructured) error {
	return CrossplaneObjectNotAvailable{object}
}
func NewIngressNotAvailable(ingress *unstructured.Unstructured) error {
	return IngressNotAvailable{ingress}
}
func NewGatewayNotAvailable(gateway *unstructured.Unstructured) error {
	return GatewayNotAvailable{gateway}
}
func NewHTTPRouteNotAvailable(httpRoute *unstructured.Unstructured) error {
	return HTTPRouteNotAvailable{httpRoute}
}
