// Package k8s connects module tests to the environment's cluster and waits for
// Kubernetes and Crossplane objects. It replaces the terratest k8s module: a
// Client stands in for KubectlOptions and is built from the kubeconfig the
// executor provides (KUBECONFIG or ~/.kube/config) and the environment's
// kube_context.
package k8s

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"

	authv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/entigolabs/entigo-infralib-test/env"
	"github.com/entigolabs/entigo-infralib-test/logger"
)

// Client addresses one namespace of one cluster.
type Client struct {
	// Env is the environment the client was connected for; nil for ad hoc clients.
	Env *env.Environment
	// Context is the kubeconfig context; empty selects the current context.
	Context string
	// ConfigPath is an explicit kubeconfig file; empty follows the default loading rules.
	ConfigPath string
	Namespace  string

	mu         sync.Mutex
	restConfig *rest.Config
	clientset  *kubernetes.Clientset
	dynamic    dynamic.Interface
}

// New returns a client for a context, kubeconfig path and namespace, the shape
// of terratest's NewKubectlOptions.
func New(contextName, configPath, namespace string) *Client {
	return &Client{Context: contextName, ConfigPath: configPath, Namespace: namespace}
}

// Connect returns a client for the environment's cluster, in the namespace of
// the calling module's ArgoCD application (its agent module name), after
// checking the kubeconfig can list pods there.
func Connect(t logger.T, e *env.Environment) *Client {
	t.Helper()
	p := env.ModulePlacement(t, e)
	return ConnectNamespace(t, e, p.Module.Name)
}

// ConnectNamespace is Connect for an explicit namespace.
func ConnectNamespace(t logger.T, e *env.Environment, namespace string) *Client {
	t.Helper()
	if e.KubeContext == "" {
		t.Fatalf("environment %s has no kube_context", e.Name)
	}
	c := &Client{Env: e, Context: e.KubeContext, Namespace: namespace}
	allowed, err := c.CanIE("get", "pods")
	if err != nil {
		t.Fatalf("unable to connect to context %s: %v", e.KubeContext, err)
	}
	if !allowed {
		t.Fatalf("context %s may not get pods in namespace %s", e.KubeContext, namespace)
	}
	return c
}

// WithNamespace returns a client for another namespace of the same cluster.
func (c *Client) WithNamespace(namespace string) *Client {
	n := New(c.Context, c.ConfigPath, namespace)
	n.Env = c.Env
	return n
}

// Cloud returns the environment's cloud, guessing from the context name for ad hoc clients.
func (c *Client) Cloud() string {
	if c.Env != nil {
		return c.Env.Cloud
	}
	switch {
	case strings.HasPrefix(c.Context, "gke_"):
		return env.CloudGoogle
	case strings.HasPrefix(c.Context, "arn:aws:eks:"):
		return env.CloudAWS
	}
	return ""
}

// RestConfig builds (once) the REST config for the client.
func (c *Client) RestConfig() (*rest.Config, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.restConfig != nil {
		return c.restConfig, nil
	}
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if c.ConfigPath != "" {
		rules.ExplicitPath = c.ConfigPath
	}
	overrides := &clientcmd.ConfigOverrides{CurrentContext: c.Context}
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("kubeconfig context %q: %w", c.Context, err)
	}
	config.QPS = 50
	config.Burst = 100
	c.restConfig = config
	return config, nil
}

// Clientset returns the typed client.
func (c *Client) Clientset() (*kubernetes.Clientset, error) {
	config, err := c.RestConfig()
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.clientset == nil {
		c.clientset, err = kubernetes.NewForConfig(config)
		if err != nil {
			return nil, err
		}
	}
	return c.clientset, nil
}

// Dynamic returns the dynamic client.
func (c *Client) Dynamic() (dynamic.Interface, error) {
	config, err := c.RestConfig()
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dynamic == nil {
		c.dynamic, err = dynamic.NewForConfig(config)
		if err != nil {
			return nil, err
		}
	}
	return c.dynamic, nil
}

// CanIE answers `kubectl auth can-i <verb> <resource>` in the client's namespace.
func (c *Client) CanIE(verb, resource string) (bool, error) {
	clientset, err := c.Clientset()
	if err != nil {
		return false, err
	}
	review := &authv1.SelfSubjectAccessReview{
		Spec: authv1.SelfSubjectAccessReviewSpec{
			ResourceAttributes: &authv1.ResourceAttributes{Namespace: c.Namespace, Verb: verb, Resource: resource},
		},
	}
	result, err := clientset.AuthorizationV1().SelfSubjectAccessReviews().Create(context.Background(), review, metav1.CreateOptions{})
	if err != nil {
		return false, err
	}
	return result.Status.Allowed, nil
}

// RunKubectlAndGetOutputE runs kubectl against the client's context and
// namespace and returns its combined output.
func RunKubectlAndGetOutputE(t logger.T, c *Client, args ...string) (string, error) {
	t.Helper()
	cmdArgs := []string{}
	if c.Context != "" {
		cmdArgs = append(cmdArgs, "--context", c.Context)
	}
	if c.ConfigPath != "" {
		cmdArgs = append(cmdArgs, "--kubeconfig", c.ConfigPath)
	}
	if c.Namespace != "" {
		cmdArgs = append(cmdArgs, "--namespace", c.Namespace)
	}
	cmdArgs = append(cmdArgs, args...)
	logger.Logf(t, "Running kubectl %s", strings.Join(args, " "))
	cmd := exec.Command("kubectl", cmdArgs...)
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	output := strings.TrimSpace(string(out))
	if err != nil {
		return output, fmt.Errorf("kubectl %s: %w: %s", strings.Join(args, " "), err, output)
	}
	return output, nil
}

// RunKubectlE runs kubectl and logs its output.
func RunKubectlE(t logger.T, c *Client, args ...string) error {
	t.Helper()
	out, err := RunKubectlAndGetOutputE(t, c, args...)
	if out != "" {
		logger.Logf(t, "%s", out)
	}
	return err
}
