// Package google reads agent state from Cloud Storage and waits for Google
// Cloud resources, using the official client libraries with Application
// Default Credentials of the executor.
package google

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"cloud.google.com/go/storage"
	"google.golang.org/api/dns/v1"
	"google.golang.org/api/option"

	"github.com/entigolabs/entigo-infralib-test/env"
	"github.com/entigolabs/entigo-infralib-test/logger"
	"github.com/entigolabs/entigo-infralib-test/retry"
	"github.com/entigolabs/entigo-infralib-test/tf"
)

func init() {
	tf.Register(env.CloudGoogle, func(t logger.T, e *env.Environment, file string) map[string]any {
		t.Helper()
		return ReadJSON(t, e, Bucket(e), file)
	})
}

// Bucket is the agent's state bucket for the environment: <prefix>-<project>-<region>.
func Bucket(e *env.Environment) string {
	return fmt.Sprintf("%s-%s-%s", e.Prefix, e.Project, e.Region)
}

// ReadObjectE returns the contents of a Cloud Storage object.
func ReadObjectE(bucket, object string) ([]byte, error) {
	ctx := context.Background()
	client, err := storage.NewClient(ctx)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	reader, err := client.Bucket(bucket).Object(object).NewReader(ctx)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(reader)
}

// ReadJSON reads and parses a JSON object.
func ReadJSON(t logger.T, e *env.Environment, bucket, object string) map[string]any {
	t.Helper()
	data, err := ReadObjectE(bucket, object)
	if err != nil {
		t.Fatalf("failed to read gs://%s/%s: %v", bucket, object, err)
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("gs://%s/%s is not JSON: %v", bucket, object, err)
	}
	return result
}

// BucketExistsE reports whether a bucket exists.
func BucketExistsE(name string) (bool, error) {
	ctx := context.Background()
	client, err := storage.NewClient(ctx)
	if err != nil {
		return false, err
	}
	defer client.Close()
	_, err = client.Bucket(name).Attrs(ctx)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, storage.ErrBucketNotExist) {
		return false, nil
	}
	return false, err
}

// WaitUntilBucketExists polls until the bucket exists.
func WaitUntilBucketExists(t logger.T, name string, retries int, sleepBetweenRetries time.Duration) error {
	t.Helper()
	_, err := retry.DoWithRetryE(t, fmt.Sprintf("Wait for bucket %s to be created", name), retries, sleepBetweenRetries, func() (string, error) {
		exists, err := BucketExistsE(name)
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
func WaitUntilBucketDeleted(t logger.T, name string, retries int, sleepBetweenRetries time.Duration) error {
	t.Helper()
	_, err := retry.DoWithRetryE(t, fmt.Sprintf("Wait for bucket %s to be deleted", name), retries, sleepBetweenRetries, func() (string, error) {
		exists, err := BucketExistsE(name)
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
func WaitUntilBucketFileAvailable(t logger.T, bucket, object string, retries int, sleepBetweenRetries time.Duration) error {
	t.Helper()
	_, err := retry.DoWithRetryE(t, fmt.Sprintf("Wait for gs://%s/%s", bucket, object), retries, sleepBetweenRetries, func() (string, error) {
		if _, err := ReadObjectE(bucket, object); err != nil {
			return "", err
		}
		return "File is now available", nil
	})
	return err
}

// DNSRecordExistsE reports whether a Cloud DNS record set exists in a managed zone.
func DNSRecordExistsE(project, zone, recordName, recordType string) (bool, error) {
	ctx := context.Background()
	service, err := dns.NewService(ctx, option.WithScopes(dns.NdevClouddnsReadonlyScope))
	if err != nil {
		return false, fmt.Errorf("failed to create DNS service: %w", err)
	}
	resp, err := service.ResourceRecordSets.List(project, zone).Name(recordName + ".").Type(recordType).Do()
	if err != nil {
		return false, fmt.Errorf("failed to list DNS records: %w", err)
	}
	return len(resp.Rrsets) > 0, nil
}

// WaitUntilDNSRecordExists polls until the record exists in the environment's project.
func WaitUntilDNSRecordExists(t logger.T, e *env.Environment, zone, recordName, recordType string, retries int, sleepBetweenRetries time.Duration) error {
	t.Helper()
	_, err := retry.DoWithRetryE(t, fmt.Sprintf("Wait for DNS record %s %s in zone %s", recordType, recordName, zone), retries, sleepBetweenRetries, func() (string, error) {
		exists, err := DNSRecordExistsE(e.Project, zone, recordName, recordType)
		if err != nil {
			return "", err
		}
		if !exists {
			return "", fmt.Errorf("DNS record %s %s not found yet", recordType, recordName)
		}
		return "DNS record found", nil
	})
	return err
}
