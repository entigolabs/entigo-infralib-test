package env

import (
	"fmt"
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

// KubeContext returns the kubeconfig context of the environment's cluster,
// the name the cloud's CLI gives it (aws eks update-kubeconfig, gcloud
// container clusters get-credentials, oci ce cluster create-kubeconfig).
// The executor's kubeconfig must hold that context; the framework does not
// write kubeconfigs.
func (c *Config) KubeContext(t logger.T, e *Environment) string {
	t.Helper()
	cluster := c.ClusterName(e)
	if cluster == "" {
		t.Fatalf("environment %s has no cluster module (%s)", e.Name, clusterSources[e.Cloud])
	}
	kubeMu.RLock()
	resolver, ok := kubeContexts[e.Cloud]
	registered := make([]string, 0, len(kubeContexts))
	for cloud := range kubeContexts {
		registered = append(registered, cloud)
	}
	kubeMu.RUnlock()
	if !ok {
		sort.Strings(registered)
		t.Fatalf("no kube context resolver for cloud %q (registered: %v); import github.com/entigolabs/entigo-infralib-test/%s for its side effect", e.Cloud, registered, e.Cloud)
	}
	return resolver(t, e, cluster)
}

// Gateway names the shared ingress gateway tests publish hostnames through.
// The k8s package derives it from the environment's gateway and DNS modules.
type Gateway struct {
	Name      string
	Namespace string
	// Domain is the DNS zone hostnames are created in; Hostname() joins it with a namespace.
	Domain string
	// Retries is how many 6 second polls hostname checks allow.
	Retries int
}

// Hostname returns the FQDN a module's namespace gets under this gateway.
func (g Gateway) Hostname(namespace string) string {
	return fmt.Sprintf("%s.%s", namespace, g.Domain)
}
