package k8s

import (
	_ "embed"
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/entigolabs/entigo-infralib-test/env"
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
//
// The extra CA certificates of the environment (environments/ca-certificates)
// are mounted into the Job through a ConfigMap and appended to the image's
// bundle, so the probe trusts what the agent trusts.
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
	var certs []env.CACertificate
	if c.Env != nil {
		certs = c.Env.CACertificates
	}
	if len(certs) > 0 {
		if err := mountCACertificates(c, job, jobName, certs); err != nil {
			return err
		}
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
	if len(certs) > 0 {
		if err := c.DeleteObjectE(ConfigMaps, c.Namespace, jobName); err != nil {
			return fmt.Errorf("failed to delete configmap: %w", err)
		}
	}
	return nil
}

// mountCACertificates creates a ConfigMap named after the Job with the
// certificate files and mounts it at /extra-ca, where the Job's script adds
// them to curl's bundle. The ConfigMap shares the Job's fate.
func mountCACertificates(c *Client, job *unstructured.Unstructured, name string, certs []env.CACertificate) error {
	data := map[string]any{}
	for _, cert := range certs {
		data[cert.Name] = string(cert.Content)
	}
	configMap := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": name},
		"data":       data,
	}}
	if _, err := c.CreateObjectE(ConfigMaps, c.Namespace, configMap); err != nil {
		return fmt.Errorf("failed to create configmap: %w", err)
	}
	volumes := []any{map[string]any{"name": "extra-ca", "configMap": map[string]any{"name": name}}}
	if err := unstructured.SetNestedSlice(job.Object, volumes, "spec", "template", "spec", "volumes"); err != nil {
		return fmt.Errorf("failed to set volumes: %w", err)
	}
	containers, _, _ := unstructured.NestedSlice(job.Object, "spec", "template", "spec", "containers")
	mounts := []any{map[string]any{"name": "extra-ca", "mountPath": "/extra-ca", "readOnly": true}}
	if err := unstructured.SetNestedSlice(containers[0].(map[string]any), mounts, "volumeMounts"); err != nil {
		return fmt.Errorf("failed to set volume mounts: %w", err)
	}
	return unstructured.SetNestedSlice(job.Object, containers, "spec", "template", "spec", "containers")
}
