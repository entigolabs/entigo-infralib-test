package k8s

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	kubernetesErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8sYaml "k8s.io/apimachinery/pkg/util/yaml"

	"github.com/entigolabs/entigo-infralib-test/env"
	"github.com/entigolabs/entigo-infralib-test/logger"
	"github.com/entigolabs/entigo-infralib-test/retry"
)

// Well known resources.
var (
	Providers                = schema.GroupVersionResource{Group: "pkg.crossplane.io", Version: "v1", Resource: "providers"}
	DeploymentRuntimeConfigs = schema.GroupVersionResource{Group: "pkg.crossplane.io", Version: "v1beta1", Resource: "deploymentruntimeconfigs"}
	ClusterSecretStores      = schema.GroupVersionResource{Group: "external-secrets.io", Version: "v1", Resource: "clustersecretstores"}
	KubernetesObjects        = schema.GroupVersionResource{Group: "kubernetes.crossplane.io", Version: "v1alpha2", Resource: "objects"}
	GatewayClasses           = schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gatewayclasses"}
	Gateways                 = schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gateways"}
	HTTPRoutes               = schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}
	Deployments              = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	Services                 = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "services"}
	Ingresses                = schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"}
	Jobs                     = schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}
	AWSBuckets               = schema.GroupVersionResource{Group: "s3.aws.upbound.io", Version: "v1beta2", Resource: "buckets"}
	GoogleBuckets            = schema.GroupVersionResource{Group: "storage.gcp.upbound.io", Version: "v1beta2", Resource: "buckets"}
	OracleBuckets            = schema.GroupVersionResource{Group: "objectstorage.oci.upbound.io", Version: "v1alpha1", Resource: "buckets"}
)

// BucketResource is the Crossplane bucket resource of the client's cloud.
func (c *Client) BucketResource() schema.GroupVersionResource {
	switch c.Cloud() {
	case env.CloudGoogle:
		return GoogleBuckets
	case env.CloudOracle:
		return OracleBuckets
	}
	return AWSBuckets
}

// Generic dynamic client operations. namespace "" addresses cluster scoped resources.

func (c *Client) GetObjectE(resource schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	dyn, err := c.Dynamic()
	if err != nil {
		return nil, err
	}
	return dyn.Resource(resource).Namespace(namespace).Get(context.Background(), name, metav1.GetOptions{})
}

func (c *Client) ListObjectsE(resource schema.GroupVersionResource, namespace string, options metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	dyn, err := c.Dynamic()
	if err != nil {
		return nil, err
	}
	return dyn.Resource(resource).Namespace(namespace).List(context.Background(), options)
}

func (c *Client) CreateObjectE(resource schema.GroupVersionResource, namespace string, object *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	dyn, err := c.Dynamic()
	if err != nil {
		return nil, err
	}
	return dyn.Resource(resource).Namespace(namespace).Create(context.Background(), object, metav1.CreateOptions{})
}

func (c *Client) DeleteObjectE(resource schema.GroupVersionResource, namespace, name string) error {
	dyn, err := c.Dynamic()
	if err != nil {
		return err
	}
	err = dyn.Resource(resource).Namespace(namespace).Delete(context.Background(), name, metav1.DeleteOptions{})
	if kubernetesErrors.IsNotFound(err) {
		return nil
	}
	return err
}

// CreateObject creates an object; kept with the old name for ported tests.
func CreateObject(t logger.T, c *Client, object *unstructured.Unstructured, namespace string, resource schema.GroupVersionResource) (*unstructured.Unstructured, error) {
	return c.CreateObjectE(resource, namespace, object)
}

// ReadObjectFromFile decodes a single YAML manifest.
func ReadObjectFromFile(t logger.T, templateFile string) (*unstructured.Unstructured, error) {
	data, err := os.ReadFile(templateFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", templateFile, err)
	}
	return DecodeObject(data)
}

// DecodeObject decodes a single YAML manifest.
func DecodeObject(data []byte) (*unstructured.Unstructured, error) {
	object := &unstructured.Unstructured{}
	if err := k8sYaml.Unmarshal(data, &object.Object); err != nil {
		return nil, fmt.Errorf("failed to parse manifest: %w", err)
	}
	return object, nil
}

type isObjectAvailable func(*unstructured.Unstructured) bool

type objectAvailability struct {
	resource    schema.GroupVersionResource
	namespace   string
	name        string
	isAvailable isObjectAvailable
	objectError NewObjectError
}

func available(resource schema.GroupVersionResource, namespace, name string) objectAvailability {
	return objectAvailability{resource: resource, namespace: namespace, name: name, isAvailable: isObjectNotNil, objectError: DefaultObjectError}
}

func (a objectAvailability) with(isAvailable isObjectAvailable, objectError NewObjectError) objectAvailability {
	a.isAvailable = isAvailable
	a.objectError = objectError
	return a
}

func waitUntilObjectAvailable(t logger.T, c *Client, a objectAvailability, retries int, sleepBetweenRetries time.Duration) (*unstructured.Unstructured, error) {
	t.Helper()
	var object *unstructured.Unstructured
	_, err := retry.DoWithRetryE(t, fmt.Sprintf("Wait for %s %s to be available", a.resource.Resource, a.name), retries, sleepBetweenRetries, func() (string, error) {
		var err error
		object, err = c.GetObjectE(a.resource, a.namespace, a.name)
		if err != nil {
			return "", err
		}
		if !a.isAvailable(object) {
			return "", a.objectError(object)
		}
		return fmt.Sprintf("%s %s is now available", a.resource.Resource, a.name), nil
	})
	if err != nil {
		return nil, err
	}
	return object, nil
}

func waitUntilObjectDeleted(t logger.T, c *Client, resource schema.GroupVersionResource, namespace, name string, retries int, sleepBetweenRetries time.Duration) error {
	t.Helper()
	_, err := retry.DoWithRetryE(t, fmt.Sprintf("Wait for %s %s to be deleted", resource.Resource, name), retries, sleepBetweenRetries, func() (string, error) {
		_, err := c.GetObjectE(resource, namespace, name)
		if err == nil {
			return "", fmt.Errorf("%s %s still exists", resource.Resource, name)
		}
		if kubernetesErrors.IsNotFound(err) {
			return fmt.Sprintf("%s %s is now deleted", resource.Resource, name), nil
		}
		return "", err
	})
	return err
}

// WaitUntilObjectAvailable waits for any object to exist and satisfy isAvailable (nil = exist).
func WaitUntilObjectAvailable(t logger.T, c *Client, resource schema.GroupVersionResource, namespace, name string, isAvailable func(*unstructured.Unstructured) bool, retries int, sleepBetweenRetries time.Duration) (*unstructured.Unstructured, error) {
	t.Helper()
	a := available(resource, namespace, name)
	if isAvailable != nil {
		a.isAvailable = isAvailable
	}
	return waitUntilObjectAvailable(t, c, a, retries, sleepBetweenRetries)
}

// WaitUntilObjectDeleted waits for any object to be gone.
func WaitUntilObjectDeleted(t logger.T, c *Client, resource schema.GroupVersionResource, namespace, name string, retries int, sleepBetweenRetries time.Duration) error {
	t.Helper()
	return waitUntilObjectDeleted(t, c, resource, namespace, name, retries, sleepBetweenRetries)
}

// Crossplane.

func WaitUntilClusterSecretStoreAvailable(t logger.T, c *Client, name string, retries int, sleepBetweenRetries time.Duration) (*unstructured.Unstructured, error) {
	t.Helper()
	return waitUntilObjectAvailable(t, c, available(ClusterSecretStores, "", name), retries, sleepBetweenRetries)
}

func WaitUntilProviderAvailable(t logger.T, c *Client, name string, retries int, sleepBetweenRetries time.Duration) (*unstructured.Unstructured, error) {
	t.Helper()
	return waitUntilObjectAvailable(t, c, available(Providers, "", name).with(isProviderAvailable, NewProviderNotAvailable), retries, sleepBetweenRetries)
}

func WaitUntilDeploymentRuntimeConfigAvailable(t logger.T, c *Client, name string, retries int, sleepBetweenRetries time.Duration) (*unstructured.Unstructured, error) {
	t.Helper()
	return waitUntilObjectAvailable(t, c, available(DeploymentRuntimeConfigs, "", name), retries, sleepBetweenRetries)
}

func WaitUntilProviderConfigAvailable(t logger.T, c *Client, resource schema.GroupVersionResource, name string, retries int, sleepBetweenRetries time.Duration) (*unstructured.Unstructured, error) {
	t.Helper()
	return waitUntilObjectAvailable(t, c, available(resource, "", name), retries, sleepBetweenRetries)
}

// WaitUntilCrossplaneResourceAvailable waits for a managed resource to be Ready and Synced.
func WaitUntilCrossplaneResourceAvailable(t logger.T, c *Client, resource schema.GroupVersionResource, name string, retries int, sleepBetweenRetries time.Duration) (*unstructured.Unstructured, error) {
	t.Helper()
	return waitUntilObjectAvailable(t, c, available(resource, "", name).with(isCrossplaneObjectAvailable, NewCrossplaneObjectNotAvailable), retries, sleepBetweenRetries)
}

func DeleteCrossplaneResource(t logger.T, c *Client, resource schema.GroupVersionResource, name string) error {
	t.Helper()
	logger.Logf(t, "Deleting %s %s", resource.Resource, name)
	return c.DeleteObjectE(resource, "", name)
}

func WaitUntilCrossplaneResourceDeleted(t logger.T, c *Client, resource schema.GroupVersionResource, name string, retries int, sleepBetweenRetries time.Duration) error {
	t.Helper()
	return waitUntilObjectDeleted(t, c, resource, "", name, retries, sleepBetweenRetries)
}

func WaitUntilK8SBucketAvailable(t logger.T, c *Client, name string, retries int, sleepBetweenRetries time.Duration) (*unstructured.Unstructured, error) {
	t.Helper()
	return WaitUntilCrossplaneResourceAvailable(t, c, c.BucketResource(), name, retries, sleepBetweenRetries)
}

func WaitUntilK8SBucketDeleted(t logger.T, c *Client, name string, retries int, sleepBetweenRetries time.Duration) error {
	t.Helper()
	return waitUntilObjectDeleted(t, c, c.BucketResource(), "", name, retries, sleepBetweenRetries)
}

func CreateK8SBucket(t logger.T, c *Client, name string, templateFile string) (*unstructured.Unstructured, error) {
	t.Helper()
	logger.Logf(t, "Creating bucket %s", name)
	object, err := ReadObjectFromFile(t, templateFile)
	if err != nil {
		return nil, err
	}
	object.SetName(name)
	return c.CreateObjectE(c.BucketResource(), "", object)
}

func DeleteK8SBucket(t logger.T, c *Client, name string) error {
	t.Helper()
	logger.Logf(t, "Deleting bucket %s", name)
	return c.DeleteObjectE(c.BucketResource(), "", name)
}

func WaitUntilK8SObjectAvailable(t logger.T, c *Client, name string, retries int, sleepBetweenRetries time.Duration) (*unstructured.Unstructured, error) {
	t.Helper()
	return waitUntilObjectAvailable(t, c, available(KubernetesObjects, "", name).with(isCrossplaneObjectAvailable, NewCrossplaneObjectNotAvailable), retries, sleepBetweenRetries)
}

func WaitUntilK8SObjectDeleted(t logger.T, c *Client, name string, retries int, sleepBetweenRetries time.Duration) error {
	t.Helper()
	return waitUntilObjectDeleted(t, c, KubernetesObjects, "", name, retries, sleepBetweenRetries)
}

// CreateK8SObject creates a provider-kubernetes Object from a template, naming
// the wrapped manifest after it and placing it in the client's namespace.
func CreateK8SObject(t logger.T, c *Client, name string, templateFile string) (*unstructured.Unstructured, error) {
	t.Helper()
	logger.Logf(t, "Creating k8s provider object %s", name)
	object, err := ReadObjectFromFile(t, templateFile)
	if err != nil {
		return nil, err
	}
	object.SetName(name)
	if err := unstructured.SetNestedField(object.Object, name, "spec", "forProvider", "manifest", "metadata", "name"); err != nil {
		return nil, err
	}
	if err := unstructured.SetNestedField(object.Object, c.Namespace, "spec", "forProvider", "manifest", "metadata", "namespace"); err != nil {
		return nil, err
	}
	return c.CreateObjectE(KubernetesObjects, "", object)
}

func DeleteK8SObject(t logger.T, c *Client, name string) error {
	t.Helper()
	logger.Logf(t, "Deleting k8s provider object %s", name)
	return c.DeleteObjectE(KubernetesObjects, "", name)
}

// Gateway API.

func CreateK8SGatewayClass(t logger.T, c *Client, object *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	t.Helper()
	logger.Logf(t, "Creating GatewayClass %s", object.GetName())
	return c.CreateObjectE(GatewayClasses, "", object)
}

func DeleteK8SGatewayClass(t logger.T, c *Client, name string) error {
	t.Helper()
	logger.Logf(t, "Deleting GatewayClass %s", name)
	return c.DeleteObjectE(GatewayClasses, "", name)
}

func CreateK8SGateway(t logger.T, c *Client, object *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	t.Helper()
	logger.Logf(t, "Creating Gateway %s", object.GetName())
	return c.CreateObjectE(Gateways, c.Namespace, object)
}

func DeleteK8SGateway(t logger.T, c *Client, name string) error {
	t.Helper()
	logger.Logf(t, "Deleting Gateway %s", name)
	return c.DeleteObjectE(Gateways, c.Namespace, name)
}

func WaitUntilK8SGatewayDeleted(t logger.T, c *Client, name string, retries int, sleepBetweenRetries time.Duration) error {
	t.Helper()
	return waitUntilObjectDeleted(t, c, Gateways, c.Namespace, name, retries, sleepBetweenRetries)
}

func WaitUntilK8SGatewayAvailable(t logger.T, c *Client, name string, retries int, sleepBetweenRetries time.Duration) (*unstructured.Unstructured, error) {
	t.Helper()
	return waitUntilObjectAvailable(t, c, available(Gateways, c.Namespace, name).with(isGatewayAvailable, NewGatewayNotAvailable), retries, sleepBetweenRetries)
}

func CreateK8SHTTPRoute(t logger.T, c *Client, object *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	t.Helper()
	logger.Logf(t, "Creating HTTPRoute %s", object.GetName())
	return c.CreateObjectE(HTTPRoutes, c.Namespace, object)
}

func DeleteK8SHTTPRoute(t logger.T, c *Client, name string) error {
	t.Helper()
	logger.Logf(t, "Deleting HTTPRoute %s", name)
	return c.DeleteObjectE(HTTPRoutes, c.Namespace, name)
}

func WaitUntilK8SHTTPRouteAvailable(t logger.T, c *Client, name string, retries int, sleepBetweenRetries time.Duration) (*unstructured.Unstructured, error) {
	t.Helper()
	return waitUntilObjectAvailable(t, c, available(HTTPRoutes, c.Namespace, name).with(isHTTPRouteAvailable, NewHTTPRouteNotAvailable), retries, sleepBetweenRetries)
}

func WaitUntilK8SHTTPRouteDeleted(t logger.T, c *Client, name string, retries int, sleepBetweenRetries time.Duration) error {
	t.Helper()
	return waitUntilObjectDeleted(t, c, HTTPRoutes, c.Namespace, name, retries, sleepBetweenRetries)
}

// GetK8SGatewayAddress returns the first status address of a Gateway.
func GetK8SGatewayAddress(gateway *unstructured.Unstructured) string {
	addresses, found, err := unstructured.NestedSlice(gateway.Object, "status", "addresses")
	if !found || err != nil || len(addresses) == 0 {
		return ""
	}
	addressMap, ok := addresses[0].(map[string]any)
	if !ok {
		return ""
	}
	value, _ := addressMap["value"].(string)
	return value
}

// Core workloads through the dynamic client, for manifests kept as YAML templates.

func CreateK8SDeployment(t logger.T, c *Client, object *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	t.Helper()
	logger.Logf(t, "Creating Deployment %s", object.GetName())
	return c.CreateObjectE(Deployments, c.Namespace, object)
}

func DeleteK8SDeployment(t logger.T, c *Client, name string) error {
	t.Helper()
	logger.Logf(t, "Deleting Deployment %s", name)
	return c.DeleteObjectE(Deployments, c.Namespace, name)
}

func CreateK8SService(t logger.T, c *Client, object *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	t.Helper()
	logger.Logf(t, "Creating Service %s", object.GetName())
	return c.CreateObjectE(Services, c.Namespace, object)
}

func DeleteK8SService(t logger.T, c *Client, name string) error {
	t.Helper()
	logger.Logf(t, "Deleting Service %s", name)
	return c.DeleteObjectE(Services, c.Namespace, name)
}

func WaitUntilK8SIngressAvailable(t logger.T, c *Client, name string, retries int, sleepBetweenRetries time.Duration) (*unstructured.Unstructured, error) {
	t.Helper()
	return waitUntilObjectAvailable(t, c, available(Ingresses, c.Namespace, name).with(isIngressAvailable, NewIngressNotAvailable), retries, sleepBetweenRetries)
}

func WaitUntilK8SIngressDeleted(t logger.T, c *Client, name string, retries int, sleepBetweenRetries time.Duration) error {
	t.Helper()
	return waitUntilObjectDeleted(t, c, Ingresses, c.Namespace, name, retries, sleepBetweenRetries)
}

func CreateK8SIngress(t logger.T, c *Client, object *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	t.Helper()
	logger.Logf(t, "Creating Ingress %s", object.GetName())
	return c.CreateObjectE(Ingresses, c.Namespace, object)
}

func DeleteK8SIngress(t logger.T, c *Client, name string) error {
	t.Helper()
	logger.Logf(t, "Deleting Ingress %s", name)
	return c.DeleteObjectE(Ingresses, c.Namespace, name)
}

// Availability predicates.

func isObjectNotNil(object *unstructured.Unstructured) bool { return object != nil }

func isProviderAvailable(provider *unstructured.Unstructured) bool {
	status := getStatusMap(provider)
	return status["Healthy"] == "True" && status["Installed"] == "True"
}

func isCrossplaneObjectAvailable(object *unstructured.Unstructured) bool {
	status := getStatusMap(object)
	return status["Ready"] == "True" && status["Synced"] == "True"
}

func isGatewayAvailable(gateway *unstructured.Unstructured) bool {
	return GetK8SGatewayAddress(gateway) != ""
}

func isIngressAvailable(ingress *unstructured.Unstructured) bool {
	entries, found, err := unstructured.NestedSlice(ingress.Object, "status", "loadBalancer", "ingress")
	if !found || err != nil || len(entries) == 0 {
		return false
	}
	entry, ok := entries[0].(map[string]any)
	if !ok {
		return false
	}
	hostname, _ := entry["hostname"].(string)
	ip, _ := entry["ip"].(string)
	return hostname != "" || ip != ""
}

func isHTTPRouteAvailable(httpRoute *unstructured.Unstructured) bool {
	parents, found, err := unstructured.NestedSlice(httpRoute.Object, "status", "parents")
	if !found || err != nil || len(parents) == 0 {
		return false
	}
	for _, p := range parents {
		parent, ok := p.(map[string]any)
		if !ok {
			return false
		}
		conditions, _, _ := unstructured.NestedSlice(parent, "conditions")
		status := map[string]string{}
		for _, cond := range conditions {
			condition, ok := cond.(map[string]any)
			if !ok {
				continue
			}
			status[GetStringValue(condition, "type")] = GetStringValue(condition, "status")
		}
		if status["Accepted"] != "True" || status["ResolvedRefs"] != "True" {
			return false
		}
	}
	return true
}

// getStatusMap maps status.conditions[].type to status for an object.
func getStatusMap(object *unstructured.Unstructured) map[string]string {
	result := map[string]string{}
	if object == nil {
		return result
	}
	conditions, _, _ := unstructured.NestedSlice(object.Object, "status", "conditions")
	for _, cond := range conditions {
		condition, ok := cond.(map[string]any)
		if !ok {
			continue
		}
		result[GetStringValue(condition, "type")] = GetStringValue(condition, "status")
	}
	return result
}

// GetStringValue returns a nested string field or "".
func GetStringValue(object map[string]any, fields ...string) string {
	value, _, _ := unstructured.NestedString(object, fields...)
	return value
}

// SetNestedSliceString sets label=value on the index-th map element of a nested slice.
func SetNestedSliceString(object map[string]any, index int, label string, value string, fields ...string) error {
	slice, found, err := unstructured.NestedSlice(object, fields...)
	if err != nil {
		return err
	}
	if !found || index >= len(slice) {
		return errors.New("nested slice or index not found")
	}
	element, ok := slice[index].(map[string]any)
	if !ok {
		return fmt.Errorf("element %d is not an object", index)
	}
	element[label] = value
	slice[index] = element
	return unstructured.SetNestedSlice(object, slice, fields...)
}
