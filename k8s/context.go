package k8s

import (
	"fmt"
	"sort"
	"strings"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/entigolabs/entigo-infralib-test/env"
	"github.com/entigolabs/entigo-infralib-test/logger"
)

// KubeContext returns the kubeconfig context of the environment's cluster,
// the agent-named <prefix>-<step>-<module> of its aws/eks, google/gke or
// oracle/oke module. It looks for the context the cloud CLI creates for that
// cluster in the executor's kubeconfig (KUBECONFIG or ~/.kube/config):
// `aws eks update-kubeconfig` names it after the cluster ARN, which ends in
// ":cluster/<name>"; `gcloud container clusters get-credentials` names it
// "gke_<project>_<location>_<name>". When no context matches by name (OKE
// contexts carry no cluster name), the cloud package's resolver decides,
// which needs that package imported.
func KubeContext(t logger.T, e *env.Environment) string {
	t.Helper()
	config := env.MustLoad(t)
	cluster := config.ClusterName(e)
	if cluster == "" {
		t.Fatalf("environment %s has no cluster module", e.Name)
	}
	kubeconfig, err := clientcmd.NewDefaultClientConfigLoadingRules().Load()
	if err != nil {
		t.Fatalf("kubeconfig: %v", err)
	}
	matches := matchingContexts(kubeconfig, e.Cloud, cluster)
	switch len(matches) {
	case 1:
		return matches[0]
	case 0:
		if context, ok := config.ResolveKubeContext(t, e); ok {
			return context
		}
		t.Fatalf("no kubeconfig context for cluster %s of environment %s; %s", cluster, e.Name, kubeconfigHint(e, cluster))
	default:
		if context, ok := config.ResolveKubeContext(t, e); ok {
			return context
		}
		t.Fatalf("several kubeconfig contexts match cluster %s of environment %s: %s", cluster, e.Name, strings.Join(matches, ", "))
	}
	return ""
}

// matchingContexts lists the contexts of kubeconfig the cloud's CLI would
// have created for the cluster, sorted.
func matchingContexts(kubeconfig *clientcmdapi.Config, cloud, cluster string) []string {
	var matches []string
	for name := range kubeconfig.Contexts {
		switch cloud {
		case env.CloudAWS:
			if strings.HasPrefix(name, "arn:aws:eks:") && strings.HasSuffix(name, ":cluster/"+cluster) {
				matches = append(matches, name)
			}
		case env.CloudGoogle:
			if strings.HasPrefix(name, "gke_") && strings.HasSuffix(name, "_"+cluster) {
				matches = append(matches, name)
			}
		}
	}
	sort.Strings(matches)
	return matches
}

func kubeconfigHint(e *env.Environment, cluster string) string {
	switch e.Cloud {
	case env.CloudAWS:
		return fmt.Sprintf("run: aws eks update-kubeconfig --region %s --name %s", e.Region, cluster)
	case env.CloudGoogle:
		return fmt.Sprintf("run: gcloud container clusters get-credentials %s --region %s --project %s", cluster, e.Region, e.Project)
	}
	return "run: oci ce cluster create-kubeconfig for the cluster, and import github.com/entigolabs/entigo-infralib-test/oracle in the test"
}
