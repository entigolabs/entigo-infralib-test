package k8s

import (
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func obj(m map[string]any) *unstructured.Unstructured { return &unstructured.Unstructured{Object: m} }

func TestRouteFromObjects(t *testing.T) {
	route := obj(map[string]any{
		"metadata": map[string]any{"name": "hello", "namespace": "hello-ns"},
		"spec": map[string]any{
			"hostnames":  []any{"hello.example.com"},
			"parentRefs": []any{map[string]any{"name": "external", "namespace": "aws-alb", "sectionName": "https"}},
		},
	})
	gateway := obj(map[string]any{
		"metadata": map[string]any{"name": "external", "namespace": "aws-alb"},
		"spec": map[string]any{"listeners": []any{
			map[string]any{"name": "http", "protocol": "HTTP", "port": int64(80)},
			map[string]any{"name": "https", "protocol": "HTTPS", "port": int64(443)},
		}},
		"status": map[string]any{"addresses": []any{map[string]any{"value": "k8s-awsalb-ext-123.eu-north-1.elb.amazonaws.com"}}},
	})
	r, err := routeFromObjects(route, gateway)
	require.NoError(t, err)
	require.Equal(t, "https://hello.example.com", r.URL())
	require.Equal(t, "https", r.Listener)
	require.Equal(t, int64(443), r.Port)
	require.False(t, r.Internal())

	// No sectionName: the first listener is used; an internal ALB is recognised.
	unstructured.SetNestedSlice(route.Object, []any{map[string]any{"name": "internal", "namespace": "aws-alb"}}, "spec", "parentRefs")
	unstructured.SetNestedSlice(gateway.Object, []any{map[string]any{"value": "internal-k8s-awsalb-int-456.eu-north-1.elb.amazonaws.com"}}, "status", "addresses")
	r, err = routeFromObjects(route, gateway)
	require.NoError(t, err)
	require.Equal(t, "http://hello.example.com", r.URL())
	require.True(t, r.Internal())

	// A non-standard port shows in the URL.
	unstructured.SetNestedSlice(gateway.Object, []any{map[string]any{"name": "alt", "protocol": "HTTPS", "port": int64(8443)}}, "spec", "listeners")
	r, err = routeFromObjects(route, gateway)
	require.NoError(t, err)
	require.Equal(t, "https://hello.example.com:8443", r.URL())

	// Missing hostnames is an error.
	unstructured.RemoveNestedField(route.Object, "spec", "hostnames")
	_, err = routeFromObjects(route, gateway)
	require.ErrorContains(t, err, "no hostnames")
}

func TestIsInternalAddress(t *testing.T) {
	require.True(t, IsInternalAddress("internal-k8s-x.eu-north-1.elb.amazonaws.com"))
	require.True(t, IsInternalAddress("10.160.5.7"))
	require.True(t, IsInternalAddress("172.16.0.9"))
	require.False(t, IsInternalAddress("k8s-x.eu-north-1.elb.amazonaws.com"))
	require.False(t, IsInternalAddress("34.88.1.2"))
}
