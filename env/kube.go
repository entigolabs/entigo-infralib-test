package env

import (
	"sort"
	"sync"

	"github.com/entigolabs/entigo-infralib-test/logger"
)

// KubeContextResolver returns the kubeconfig context name of the cluster the
// agent created for the environment, given the cluster's agent name
// (<prefix>-<step>-<module>). Each cloud package registers one in init(),
// since the name may need the cloud's SDK (the AWS account id for an EKS ARN).
type KubeContextResolver func(t logger.T, e *Environment, clusterName string) string

var (
	kubeMu       sync.RWMutex
	kubeContexts = map[string]KubeContextResolver{}
)

// RegisterKubeContext installs the resolver of a cloud.
func RegisterKubeContext(cloud string, resolver KubeContextResolver) {
	kubeMu.Lock()
	defer kubeMu.Unlock()
	kubeContexts[cloud] = resolver
}

// ResolveKubeContext computes the kubeconfig context of the environment's
// cluster through the cloud's registered resolver, the name the cloud's CLI
// gives it (aws eks update-kubeconfig, gcloud container clusters
// get-credentials, oci ce cluster create-kubeconfig). ok is false when no
// resolver is registered, i.e. the cloud package is not imported. The k8s
// package first looks the context up in the kubeconfig itself and uses this
// as the fallback. The executor's kubeconfig must hold the context; the
// framework does not write kubeconfigs.
func (c *Config) ResolveKubeContext(t logger.T, e *Environment) (context string, ok bool) {
	t.Helper()
	cluster := c.ClusterName(e)
	if cluster == "" {
		t.Fatalf("environment %s has no cluster module (%s)", e.Name, clusterSources[e.Cloud])
	}
	kubeMu.RLock()
	resolver, ok := kubeContexts[e.Cloud]
	kubeMu.RUnlock()
	if !ok {
		return "", false
	}
	return resolver(t, e, cluster), true
}

// RegisteredKubeContextClouds lists the clouds with a resolver, for messages.
func RegisteredKubeContextClouds() []string {
	kubeMu.RLock()
	defer kubeMu.RUnlock()
	clouds := make([]string, 0, len(kubeContexts))
	for cloud := range kubeContexts {
		clouds = append(clouds, cloud)
	}
	sort.Strings(clouds)
	return clouds
}
