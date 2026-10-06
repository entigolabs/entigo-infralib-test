package k8s

import (
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/entigolabs/entigo-infralib-test/logger"
	"github.com/entigolabs/entigo-infralib-test/retry"
)

// GetResourcesByGroupVersion lists the API resources served for a group/version.
func GetResourcesByGroupVersion(t logger.T, c *Client, groupVersion string) (*metav1.APIResourceList, error) {
	clientset, err := c.Clientset()
	if err != nil {
		return nil, err
	}
	return clientset.Discovery().ServerResourcesForGroupVersion(groupVersion)
}

// WaitUntilResourcesAvailable polls until every named resource is served for
// the group/version, i.e. until the CRDs are installed.
func WaitUntilResourcesAvailable(t logger.T, c *Client, groupVersion string, resources []string, retries int, sleepBetweenRetries time.Duration) error {
	t.Helper()
	_, err := retry.DoWithRetryE(t, fmt.Sprintf("Wait for resources %s in %s to be served", resources, groupVersion), retries, sleepBetweenRetries, func() (string, error) {
		list, err := GetResourcesByGroupVersion(t, c, groupVersion)
		if err != nil {
			return "", err
		}
		for _, resource := range resources {
			if !containsResource(list, resource) {
				return "", fmt.Errorf("resource %s not found in %s", resource, groupVersion)
			}
		}
		return "Resources are now available", nil
	})
	return err
}

func containsResource(list *metav1.APIResourceList, resource string) bool {
	for _, r := range list.APIResources {
		if r.Name == resource {
			return true
		}
	}
	return false
}
