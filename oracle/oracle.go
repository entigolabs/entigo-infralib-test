// Package oracle reads agent state from OCI Object Storage and waits for Oracle
// Cloud resources. Credentials resolve the way the agent and CLI do:
// OCI_CONFIG_FILE if set, otherwise the DEFAULT profile of ~/.oci/config.
package oracle

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	ocicommon "github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/objectstorage"

	"github.com/entigolabs/entigo-infralib-test/env"
	"github.com/entigolabs/entigo-infralib-test/logger"
	"github.com/entigolabs/entigo-infralib-test/retry"
	"github.com/entigolabs/entigo-infralib-test/tf"
)

func init() {
	tf.Register(env.CloudOracle, func(t logger.T, e *env.Environment, file string) map[string]any {
		t.Helper()
		return ReadJSON(t, e, Bucket(t, e), file)
	})
	// oci ce cluster create-kubeconfig names the context "context-c" followed
	// by the last 11 characters of the cluster OCID, which the oke module
	// outputs as cluster_id. Not yet verified against a real OKE kubeconfig.
	env.RegisterKubeContext(env.CloudOracle, func(t logger.T, e *env.Environment, cluster string) string {
		t.Helper()
		config := env.MustLoad(t)
		p, ok := config.ClusterModule(e)
		if !ok {
			t.Fatalf("environment %s has no oracle/oke module", e.Name)
		}
		id := tf.GetStep(t, e, p.Step.Name).String(t, p.Module.AgentName(e)+"__cluster_id")
		if len(id) < 11 {
			t.Fatalf("unexpected cluster id %q", id)
		}
		return "context-c" + id[len(id)-11:]
	})
}

// ConfigProvider returns the OCI configuration provider of the executor.
func ConfigProvider() ocicommon.ConfigurationProvider {
	if configFile := os.Getenv("OCI_CONFIG_FILE"); configFile != "" {
		return ocicommon.CustomProfileConfigProvider(configFile, "DEFAULT")
	}
	return ocicommon.DefaultConfigProvider()
}

func newClient(region string) (objectstorage.ObjectStorageClient, string, error) {
	client, err := objectstorage.NewObjectStorageClientWithConfigurationProvider(ConfigProvider())
	if err != nil {
		return objectstorage.ObjectStorageClient{}, "", err
	}
	if region != "" {
		client.SetRegion(region)
	}
	namespace, err := client.GetNamespace(context.Background(), objectstorage.GetNamespaceRequest{})
	if err != nil {
		return objectstorage.ObjectStorageClient{}, "", fmt.Errorf("failed to get object storage namespace: %w", err)
	}
	return client, *namespace.Value, nil
}

// Bucket is the agent's state bucket for the environment: <prefix>-<region>.
func Bucket(t logger.T, e *env.Environment) string {
	t.Helper()
	return fmt.Sprintf("%s-%s", e.Prefix, e.MustRegion(t))
}

// ReadObjectE returns the contents of an object.
func ReadObjectE(region, bucket, object string) ([]byte, error) {
	client, namespace, err := newClient(region)
	if err != nil {
		return nil, err
	}
	response, err := client.GetObject(context.Background(), objectstorage.GetObjectRequest{
		NamespaceName: &namespace,
		BucketName:    &bucket,
		ObjectName:    &object,
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Content.Close() }()
	return io.ReadAll(response.Content)
}

// ReadJSON reads and parses a JSON object in the environment's region.
func ReadJSON(t logger.T, e *env.Environment, bucket, object string) map[string]any {
	t.Helper()
	data, err := ReadObjectE(e.MustRegion(t), bucket, object)
	if err != nil {
		t.Fatalf("failed to read %s/%s in %s: %v", bucket, object, e.Region, err)
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("%s/%s is not JSON: %v", bucket, object, err)
	}
	return result
}

// BucketExistsE reports whether a bucket exists.
func BucketExistsE(region, bucket string) (bool, error) {
	client, namespace, err := newClient(region)
	if err != nil {
		return false, err
	}
	_, err = client.GetBucket(context.Background(), objectstorage.GetBucketRequest{
		NamespaceName: &namespace,
		BucketName:    &bucket,
	})
	if err == nil {
		return true, nil
	}
	if serviceErr, ok := ocicommon.IsServiceError(err); ok && serviceErr.GetHTTPStatusCode() == 404 {
		return false, nil
	}
	return false, err
}

// ObjectStorageNamespace returns the tenancy-wide Object Storage namespace,
// which a Bucket managed resource needs and which is not derivable from the
// compartment.
func ObjectStorageNamespace(region string) (string, error) {
	_, namespace, err := newClient(region)
	return namespace, err
}

// WaitUntilBucketExists polls until the bucket exists.
func WaitUntilBucketExists(t logger.T, region, name string, retries int, sleepBetweenRetries time.Duration) error {
	t.Helper()
	_, err := retry.DoWithRetryE(t, fmt.Sprintf("Wait for bucket %s in %s to be created", name, region), retries, sleepBetweenRetries, func() (string, error) {
		exists, err := BucketExistsE(region, name)
		if err != nil {
			return "", err
		}
		if !exists {
			return "", fmt.Errorf("bucket %s does not exist yet", name)
		}
		return "Bucket is now available", nil
	})
	return err
}

// WaitUntilBucketDeleted polls until the bucket is gone.
func WaitUntilBucketDeleted(t logger.T, region, name string, retries int, sleepBetweenRetries time.Duration) error {
	t.Helper()
	_, err := retry.DoWithRetryE(t, fmt.Sprintf("Wait for bucket %s in %s to be deleted", name, region), retries, sleepBetweenRetries, func() (string, error) {
		exists, err := BucketExistsE(region, name)
		if err != nil {
			return "", err
		}
		if exists {
			return "", fmt.Errorf("bucket %s still exists", name)
		}
		return "Bucket is now deleted", nil
	})
	return err
}

// WaitUntilBucketFileAvailable polls until an object can be read.
func WaitUntilBucketFileAvailable(t logger.T, region, bucket, object string, retries int, sleepBetweenRetries time.Duration) error {
	t.Helper()
	_, err := retry.DoWithRetryE(t, fmt.Sprintf("Wait for %s/%s", bucket, object), retries, sleepBetweenRetries, func() (string, error) {
		if _, err := ReadObjectE(region, bucket, object); err != nil {
			return "", err
		}
		return "File is now available", nil
	})
	return err
}
