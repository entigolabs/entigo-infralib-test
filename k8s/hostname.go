package k8s

import (
	_ "embed"
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/entigolabs/entigo-infralib-test/logger"
	"github.com/entigolabs/entigo-infralib-test/random"
)

//go:embed templates/job.yaml
var healthCheckJob []byte

// WaitUntilHostnameAvailableWithAddress runs an in-cluster curl Job against
// targetURL with connections pinned to targetAddress and the Host header set,
// so the check works for internal load balancers and does not depend on
// public DNS having propagated. WaitUntilRouteReachable derives both from the
// HTTPRoute and its Gateway; this is the building block beneath it.
func WaitUntilHostnameAvailableWithAddress(t logger.T, c *Client, targetAddress, targetURL, successCode string, retries int, sleepBetweenRetries time.Duration) error {
	t.Helper()
	jobName := fmt.Sprintf("%s-health-check-%s", c.Namespace, random.LowerId(4))

	targetDomain := strings.SplitN(strings.SplitN(targetURL, "://", 2)[1], "/", 2)[0]
	targetPort := "80"
	if strings.HasPrefix(targetURL, "https://") {
		targetPort = "443"
	}

	logger.Logf(t, "Creating health check job %s for %s via %s", jobName, targetURL, targetAddress)
	job, err := DecodeObject(healthCheckJob)
	if err != nil {
		return err
	}
	job.SetName(jobName)
	envVars := []any{
		map[string]any{"name": "TARGET_URL", "value": targetURL},
		map[string]any{"name": "TARGET_DOMAIN", "value": targetDomain},
		map[string]any{"name": "TARGET_PORT", "value": targetPort},
		map[string]any{"name": "TARGET_IP", "value": targetAddress},
		map[string]any{"name": "SUCCESS_CODE", "value": successCode},
	}
	containers, _, err := unstructured.NestedSlice(job.Object, "spec", "template", "spec", "containers")
	if err != nil {
		return fmt.Errorf("failed to get containers from job spec: %w", err)
	}
	if err := unstructured.SetNestedSlice(containers[0].(map[string]any), envVars, "env"); err != nil {
		return fmt.Errorf("failed to set environment variables: %w", err)
	}
	if err := unstructured.SetNestedSlice(job.Object, containers, "spec", "template", "spec", "containers"); err != nil {
		return fmt.Errorf("failed to set containers: %w", err)
	}
	if _, err := c.CreateObjectE(Jobs, c.Namespace, job); err != nil {
		return fmt.Errorf("failed to create job: %w", err)
	}
	if err := WaitUntilJobSucceedE(t, c, jobName, retries, sleepBetweenRetries); err != nil {
		return fmt.Errorf("health check job %s: %w", jobName, err)
	}
	if err := c.DeleteObjectE(Jobs, c.Namespace, jobName); err != nil {
		return fmt.Errorf("failed to delete job: %w", err)
	}
	return nil
}
