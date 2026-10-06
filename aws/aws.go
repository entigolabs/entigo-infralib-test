// Package aws reads agent state from S3 and waits for AWS resources, using
// aws-sdk-go-v2 with the default credential chain of the executor.
package aws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	r53types "github.com/aws/aws-sdk-go-v2/service/route53/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/entigolabs/entigo-infralib-test/env"
	"github.com/entigolabs/entigo-infralib-test/logger"
	"github.com/entigolabs/entigo-infralib-test/retry"
	"github.com/entigolabs/entigo-infralib-test/tf"
)

func init() {
	tf.Register(env.CloudAWS, func(t logger.T, e *env.Environment, file string) map[string]any {
		t.Helper()
		return ReadJSON(t, e, Bucket(t, e), file)
	})
}

var (
	accountMu sync.Mutex
	accounts  = map[string]string{}
)

// Config loads the default AWS config for a region.
func Config(t logger.T, region string) awssdk.Config {
	t.Helper()
	cfg, err := ConfigE(region)
	if err != nil {
		t.Fatalf("aws config: %v", err)
	}
	return cfg
}

// ConfigE is Config returning an error.
func ConfigE(region string) (awssdk.Config, error) {
	return config.LoadDefaultConfig(context.Background(), config.WithRegion(region))
}

// AccountID returns the environment's account id: the configured one, or the
// caller identity of the current credentials (cached per region).
func AccountID(t logger.T, e *env.Environment) string {
	t.Helper()
	if e.Account != "" {
		return e.Account
	}
	accountMu.Lock()
	defer accountMu.Unlock()
	if id, ok := accounts[e.Region]; ok {
		return id
	}
	out, err := sts.NewFromConfig(Config(t, e.Region)).GetCallerIdentity(context.Background(), &sts.GetCallerIdentityInput{})
	if err != nil {
		t.Fatalf("sts get-caller-identity: %v", err)
	}
	accounts[e.Region] = awssdk.ToString(out.Account)
	return accounts[e.Region]
}

// Bucket is the agent's state bucket for the environment: <prefix>-<account>-<region>.
func Bucket(t logger.T, e *env.Environment) string {
	t.Helper()
	return fmt.Sprintf("%s-%s-%s", e.Prefix, AccountID(t, e), e.Region)
}

// ReadObjectE returns the contents of an S3 object.
func ReadObjectE(region, bucket, key string) ([]byte, error) {
	cfg, err := ConfigE(region)
	if err != nil {
		return nil, err
	}
	out, err := s3.NewFromConfig(cfg).GetObject(context.Background(), &s3.GetObjectInput{
		Bucket: awssdk.String(bucket),
		Key:    awssdk.String(key),
	})
	if err != nil {
		return nil, err
	}
	defer out.Body.Close()
	return io.ReadAll(out.Body)
}

// ReadJSON reads and parses a JSON object from the environment's region.
func ReadJSON(t logger.T, e *env.Environment, bucket, key string) map[string]any {
	t.Helper()
	data, err := ReadObjectE(e.Region, bucket, key)
	if err != nil {
		t.Fatalf("failed to read s3://%s/%s in %s: %v", bucket, key, e.Region, err)
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("s3://%s/%s is not JSON: %v", bucket, key, err)
	}
	return result
}

// BucketExistsE reports whether a bucket exists and is accessible.
func BucketExistsE(region, name string) (bool, error) {
	cfg, err := ConfigE(region)
	if err != nil {
		return false, err
	}
	_, err = s3.NewFromConfig(cfg).HeadBucket(context.Background(), &s3.HeadBucketInput{Bucket: awssdk.String(name)})
	if err == nil {
		return true, nil
	}
	var notFound *s3types.NotFound
	if errors.As(err, &notFound) || strings.Contains(err.Error(), "NotFound") || strings.Contains(err.Error(), "StatusCode: 404") {
		return false, nil
	}
	return false, err
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
func WaitUntilBucketFileAvailable(t logger.T, region, bucket, key string, retries int, sleepBetweenRetries time.Duration) error {
	t.Helper()
	_, err := retry.DoWithRetryE(t, fmt.Sprintf("Wait for s3://%s/%s", bucket, key), retries, sleepBetweenRetries, func() (string, error) {
		if _, err := ReadObjectE(region, bucket, key); err != nil {
			return "", err
		}
		return "File is now available", nil
	})
	return err
}

// Route53RecordExistsE reports whether a record of the given name and type exists in the hosted zone.
func Route53RecordExistsE(region, hostedZoneID, recordName, recordType string) (bool, error) {
	cfg, err := ConfigE(region)
	if err != nil {
		return false, err
	}
	name := strings.TrimSuffix(recordName, ".") + "."
	out, err := route53.NewFromConfig(cfg).ListResourceRecordSets(context.Background(), &route53.ListResourceRecordSetsInput{
		HostedZoneId:    awssdk.String(hostedZoneID),
		StartRecordName: awssdk.String(name),
		StartRecordType: r53types.RRType(recordType),
		MaxItems:        awssdk.Int32(1),
	})
	if err != nil {
		return false, err
	}
	for _, set := range out.ResourceRecordSets {
		if strings.EqualFold(awssdk.ToString(set.Name), name) && string(set.Type) == recordType {
			return true, nil
		}
	}
	return false, nil
}

// WaitUntilRoute53RecordExists polls until the record exists.
func WaitUntilRoute53RecordExists(t logger.T, region, hostedZoneID, recordName, recordType string, retries int, sleepBetweenRetries time.Duration) error {
	t.Helper()
	_, err := retry.DoWithRetryE(t, fmt.Sprintf("Wait for Route53 record %s %s", recordType, recordName), retries, sleepBetweenRetries, func() (string, error) {
		exists, err := Route53RecordExistsE(region, hostedZoneID, recordName, recordType)
		if err != nil {
			return "", err
		}
		if !exists {
			return "", fmt.Errorf("record %s %s not found yet", recordType, recordName)
		}
		return "Record is now available", nil
	})
	return err
}

// NewEC2Client returns an EC2 client for the region.
func NewEC2Client(t logger.T, region string) *ec2.Client {
	t.Helper()
	return ec2.NewFromConfig(Config(t, region))
}
