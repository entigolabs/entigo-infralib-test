package azure

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/dns/armdns"

	"github.com/entigolabs/entigo-infralib-test/env"
	"github.com/entigolabs/entigo-infralib-test/logger"
	"github.com/entigolabs/entigo-infralib-test/retry"
)

// DNSRecordExistsE reports whether a record set exists in a public DNS zone of
// the environment's subscription.
func DNSRecordExistsE(subscriptionID, resourceGroup, zone, record, recordType string) (bool, error) {
	cred, err := CredentialE()
	if err != nil {
		return false, err
	}
	client, err := armdns.NewRecordSetsClient(subscriptionID, cred, nil)
	if err != nil {
		return false, fmt.Errorf("dns client: %w", err)
	}
	_, err = client.Get(context.Background(), resourceGroup, zone, record, armdns.RecordType(recordType), nil)
	if err == nil {
		return true, nil
	}
	if strings.Contains(err.Error(), "NotFound") || strings.Contains(err.Error(), "404") {
		return false, nil
	}
	return false, err
}

// WaitUntilDNSRecordExists waits for a record set to appear in a public DNS
// zone of the environment's subscription; resourceGroup is the zone's.
func WaitUntilDNSRecordExists(t logger.T, e *env.Environment, resourceGroup, zone, record, recordType string, retries int, sleepBetweenRetries time.Duration) error {
	t.Helper()
	subscription := e.MustSubscriptionID(t)
	_, err := retry.DoWithRetryE(t, fmt.Sprintf("Wait for DNS record %s (%s) in zone %s", record, recordType, zone), retries, sleepBetweenRetries, func() (string, error) {
		exists, err := DNSRecordExistsE(subscription, resourceGroup, zone, record, recordType)
		if err != nil {
			return "", err
		}
		if !exists {
			return "", fmt.Errorf("record %s (%s) not in zone %s yet", record, recordType, zone)
		}
		return "DNS record found", nil
	})
	return err
}

// UniqueSuffix is the eight character base36 suffix the azure modules append
// to names that must be unique across Azure (storage accounts, key vaults):
// the first 64 bits of sha256("<prefix>/<subscription>/<location>").
func UniqueSuffix(prefix, subscriptionID, location string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{prefix, subscriptionID, location}, "/")))
	x := binary.BigEndian.Uint64(sum[:8])
	const digits = "0123456789abcdefghijklmnopqrstuvwxyz"
	out := make([]byte, 8)
	for i := range out {
		out[i] = digits[x%36]
		x /= 36
	}
	return string(out)
}

// AccountNamed is the storage account of a known name with the public blob
// endpoint, for blobs outside the agent's state account (a module's log or
// metrics store).
func AccountNamed(name string) Account {
	return Account{Name: name, BlobEndpoint: fmt.Sprintf("https://%s.blob.core.windows.net/", name)}
}
