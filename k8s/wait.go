package k8s

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/entigolabs/entigo-infralib-test/logger"
	"github.com/entigolabs/entigo-infralib-test/retry"
)

// Replacements for the terratest k8s helpers the module tests use. The E
// variants return errors, the others fail the test.

// GetDeploymentE returns a Deployment in the client's namespace.
func GetDeploymentE(t logger.T, c *Client, name string) (*appsv1.Deployment, error) {
	clientset, err := c.Clientset()
	if err != nil {
		return nil, err
	}
	return clientset.AppsV1().Deployments(c.Namespace).Get(context.Background(), name, metav1.GetOptions{})
}

// IsDeploymentAvailable reports whether the deployment finished rolling out
// and every desired replica is available.
func IsDeploymentAvailable(d *appsv1.Deployment) bool {
	for _, condition := range d.Status.Conditions {
		if condition.Type == appsv1.DeploymentProgressing && condition.Reason != "NewReplicaSetAvailable" {
			return false
		}
		if condition.Type == appsv1.DeploymentReplicaFailure && condition.Status == corev1.ConditionTrue {
			return false
		}
	}
	desired := int32(1)
	if d.Spec.Replicas != nil {
		desired = *d.Spec.Replicas
	}
	return d.Status.ObservedGeneration >= d.Generation &&
		d.Status.UpdatedReplicas == desired &&
		d.Status.AvailableReplicas == desired &&
		d.Status.UnavailableReplicas == 0
}

// WaitUntilDeploymentAvailableE polls until the deployment is available.
func WaitUntilDeploymentAvailableE(t logger.T, c *Client, name string, retries int, sleepBetweenRetries time.Duration) error {
	t.Helper()
	_, err := retry.DoWithRetryE(t, fmt.Sprintf("Wait for deployment %s/%s to be available", c.Namespace, name), retries, sleepBetweenRetries, func() (string, error) {
		d, err := GetDeploymentE(t, c, name)
		if err != nil {
			return "", err
		}
		if !IsDeploymentAvailable(d) {
			return "", fmt.Errorf("deployment %s not available: %d/%d replicas updated, %d available, %d unavailable",
				name, d.Status.UpdatedReplicas, d.Status.Replicas, d.Status.AvailableReplicas, d.Status.UnavailableReplicas)
		}
		return "Deployment is now available", nil
	})
	return err
}

// WaitUntilDeploymentAvailable fails the test when the deployment does not become available.
func WaitUntilDeploymentAvailable(t logger.T, c *Client, name string, retries int, sleepBetweenRetries time.Duration) {
	t.Helper()
	if err := WaitUntilDeploymentAvailableE(t, c, name, retries, sleepBetweenRetries); err != nil {
		t.Fatalf("%v", err)
	}
}

// GetDaemonSetE returns a DaemonSet in the client's namespace.
func GetDaemonSetE(t logger.T, c *Client, name string) (*appsv1.DaemonSet, error) {
	clientset, err := c.Clientset()
	if err != nil {
		return nil, err
	}
	return clientset.AppsV1().DaemonSets(c.Namespace).Get(context.Background(), name, metav1.GetOptions{})
}

// WaitUntilDaemonSetAvailableE polls until every scheduled pod of the daemonset is ready.
func WaitUntilDaemonSetAvailableE(t logger.T, c *Client, name string, retries int, sleepBetweenRetries time.Duration) error {
	t.Helper()
	_, err := retry.DoWithRetryE(t, fmt.Sprintf("Wait for daemonset %s/%s to be available", c.Namespace, name), retries, sleepBetweenRetries, func() (string, error) {
		ds, err := GetDaemonSetE(t, c, name)
		if err != nil {
			return "", err
		}
		if ds.Status.DesiredNumberScheduled == 0 || ds.Status.NumberReady != ds.Status.DesiredNumberScheduled || ds.Status.UpdatedNumberScheduled != ds.Status.DesiredNumberScheduled {
			return "", fmt.Errorf("daemonset %s not available: %d/%d ready", name, ds.Status.NumberReady, ds.Status.DesiredNumberScheduled)
		}
		return "DaemonSet is now available", nil
	})
	return err
}

// GetPodE returns a Pod in the client's namespace.
func GetPodE(t logger.T, c *Client, name string) (*corev1.Pod, error) {
	clientset, err := c.Clientset()
	if err != nil {
		return nil, err
	}
	return clientset.CoreV1().Pods(c.Namespace).Get(context.Background(), name, metav1.GetOptions{})
}

// ListPodsE lists pods in the client's namespace matching a label selector.
func ListPodsE(t logger.T, c *Client, labelSelector string) ([]corev1.Pod, error) {
	clientset, err := c.Clientset()
	if err != nil {
		return nil, err
	}
	list, err := clientset.CoreV1().Pods(c.Namespace).List(context.Background(), metav1.ListOptions{LabelSelector: labelSelector})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

// IsPodAvailable reports whether the pod is running with every container ready.
func IsPodAvailable(pod *corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, status := range pod.Status.ContainerStatuses {
		if !status.Ready {
			return false
		}
	}
	return len(pod.Status.ContainerStatuses) > 0
}

// WaitUntilPodAvailableE polls until the pod is running and ready.
func WaitUntilPodAvailableE(t logger.T, c *Client, name string, retries int, sleepBetweenRetries time.Duration) error {
	t.Helper()
	_, err := retry.DoWithRetryE(t, fmt.Sprintf("Wait for pod %s/%s to be available", c.Namespace, name), retries, sleepBetweenRetries, func() (string, error) {
		pod, err := GetPodE(t, c, name)
		if err != nil {
			return "", err
		}
		if !IsPodAvailable(pod) {
			return "", fmt.Errorf("pod %s is %s and not ready", name, pod.Status.Phase)
		}
		return "Pod is now available", nil
	})
	return err
}

// GetServiceE returns a Service in the client's namespace.
func GetServiceE(t logger.T, c *Client, name string) (*corev1.Service, error) {
	clientset, err := c.Clientset()
	if err != nil {
		return nil, err
	}
	return clientset.CoreV1().Services(c.Namespace).Get(context.Background(), name, metav1.GetOptions{})
}

// IsServiceAvailable reports whether a service can be reached: LoadBalancer
// services need an ingress address, the others only need to exist.
func IsServiceAvailable(service *corev1.Service) bool {
	if service.Spec.Type != corev1.ServiceTypeLoadBalancer {
		return true
	}
	return len(service.Status.LoadBalancer.Ingress) > 0
}

// WaitUntilServiceAvailableE polls until the service is available.
func WaitUntilServiceAvailableE(t logger.T, c *Client, name string, retries int, sleepBetweenRetries time.Duration) error {
	t.Helper()
	_, err := retry.DoWithRetryE(t, fmt.Sprintf("Wait for service %s/%s to be available", c.Namespace, name), retries, sleepBetweenRetries, func() (string, error) {
		service, err := GetServiceE(t, c, name)
		if err != nil {
			return "", err
		}
		if !IsServiceAvailable(service) {
			return "", fmt.Errorf("service %s has no load balancer address yet", name)
		}
		return "Service is now available", nil
	})
	return err
}

// WaitUntilServiceAvailable fails the test when the service does not become available.
func WaitUntilServiceAvailable(t logger.T, c *Client, name string, retries int, sleepBetweenRetries time.Duration) {
	t.Helper()
	if err := WaitUntilServiceAvailableE(t, c, name, retries, sleepBetweenRetries); err != nil {
		t.Fatalf("%v", err)
	}
}

// GetJobE returns a Job in the client's namespace.
func GetJobE(t logger.T, c *Client, name string) (*batchv1.Job, error) {
	clientset, err := c.Clientset()
	if err != nil {
		return nil, err
	}
	return clientset.BatchV1().Jobs(c.Namespace).Get(context.Background(), name, metav1.GetOptions{})
}

// WaitUntilJobSucceedE polls until the job reports Complete, and stops early
// with an error when it reports Failed.
func WaitUntilJobSucceedE(t logger.T, c *Client, name string, retries int, sleepBetweenRetries time.Duration) error {
	t.Helper()
	_, err := retry.DoWithRetryE(t, fmt.Sprintf("Wait for job %s/%s to succeed", c.Namespace, name), retries, sleepBetweenRetries, func() (string, error) {
		job, err := GetJobE(t, c, name)
		if err != nil {
			return "", err
		}
		for _, condition := range job.Status.Conditions {
			if condition.Status != corev1.ConditionTrue {
				continue
			}
			switch condition.Type {
			case batchv1.JobComplete:
				return "Job succeeded", nil
			case batchv1.JobFailed:
				return "", retry.FatalError{Underlying: fmt.Errorf("job %s failed: %s %s", name, condition.Reason, condition.Message)}
			}
		}
		return "", fmt.Errorf("job %s not finished: %d active, %d succeeded, %d failed", name, job.Status.Active, job.Status.Succeeded, job.Status.Failed)
	})
	return err
}

// GetIngressE returns an Ingress in the client's namespace.
func GetIngressE(t logger.T, c *Client, name string) (*networkingv1.Ingress, error) {
	clientset, err := c.Clientset()
	if err != nil {
		return nil, err
	}
	return clientset.NetworkingV1().Ingresses(c.Namespace).Get(context.Background(), name, metav1.GetOptions{})
}
