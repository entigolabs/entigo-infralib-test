package k8s

import (
	"testing"

	"github.com/stretchr/testify/require"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func TestMatchingContexts(t *testing.T) {
	kubeconfig := clientcmdapi.NewConfig()
	for _, name := range []string{
		"arn:aws:eks:eu-north-1:123456789012:cluster/exbiz-infra-eks",
		"arn:aws:eks:eu-north-1:123456789012:cluster/expri-infra-eks",
		"arn:aws:eks:us-east-1:123456789012:cluster/biz-infra-eks",
		"gke_proj_europe-north1_exbiz-infra-gke",
		"context-cwoke4rm6ba",
		"minikube",
	} {
		kubeconfig.Contexts[name] = clientcmdapi.NewContext()
	}
	require.Equal(t, []string{"arn:aws:eks:eu-north-1:123456789012:cluster/exbiz-infra-eks"}, matchingContexts(kubeconfig, "aws", "exbiz-infra-eks"))
	require.Equal(t, []string{"gke_proj_europe-north1_exbiz-infra-gke"}, matchingContexts(kubeconfig, "google", "exbiz-infra-gke"))
	require.Empty(t, matchingContexts(kubeconfig, "aws", "infra-eks"), "suffix must match the whole name")
	require.Empty(t, matchingContexts(kubeconfig, "oracle", "exbiz-infra-oke"), "OKE contexts carry no cluster name")
}
