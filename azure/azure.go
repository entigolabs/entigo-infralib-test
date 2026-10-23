// Package azure reads agent state from Azure Blob Storage and waits for Azure
// resources. Credentials resolve the way the agent's do: the service
// principal in AZURE_TENANT_ID, AZURE_CLIENT_ID and AZURE_CLIENT_SECRET, or
// whatever else azidentity.DefaultAzureCredential finds (an az login, a
// managed identity). Importing the package registers the state reader of
// azure environments.
package azure

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"

	"github.com/entigolabs/entigo-infralib-test/env"
	"github.com/entigolabs/entigo-infralib-test/logger"
	"github.com/entigolabs/entigo-infralib-test/retry"
	"github.com/entigolabs/entigo-infralib-test/tf"
)

// StateContainer is the blob container the agent keeps OpenTofu state and
// outputs in, inside its storage account.
const StateContainer = "tfstate"

func init() {
	tf.Register(env.CloudAzure, func(t logger.T, e *env.Environment, file string) map[string]any {
		t.Helper()
		return ReadJSON(t, e, StorageAccount(t, e), file)
	})
	// az aks get-credentials names the context after the cluster, which
	// k8s.KubeContext matches first; no resolver is needed.
}

// CredentialE is the token credential of the executor.
func CredentialE() (azcore.TokenCredential, error) {
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, fmt.Errorf("azure credential: %w", err)
	}
	return cred, nil
}

// Credential is CredentialE failing the test.
func Credential(t logger.T) azcore.TokenCredential {
	t.Helper()
	cred, err := CredentialE()
	if err != nil {
		t.Fatalf("%v", err)
	}
	return cred
}

// ResourceGroup is the agent's resource group of the environment:
// <prefix>-<location>, unless AZURE_RESOURCE_GROUP names a pre-made one, as
// the agent's own configuration allows.
func ResourceGroup(t logger.T, e *env.Environment) string {
	t.Helper()
	if group := strings.TrimSpace(e.Getenv("AZURE_RESOURCE_GROUP")); group != "" {
		return group
	}
	return e.Prefix + "-" + e.MustRegion(t)
}

// Account is the agent's state storage account: its name and blob endpoint.
type Account struct {
	Name         string
	BlobEndpoint string
}

var (
	accountsMu sync.Mutex
	accounts   = map[string]Account{}
)

// StorageAccountE finds the agent's state storage account in the resource
// group: the agent names it <prefix><region code><unique suffix>, lowercase
// without hyphens, so it is the one account whose name starts with the
// prefix. Found accounts are remembered for the process.
func StorageAccountE(subscription, resourceGroup, prefix string) (Account, error) {
	key := subscription + "/" + resourceGroup + "/" + prefix
	accountsMu.Lock()
	cached, ok := accounts[key]
	accountsMu.Unlock()
	if ok {
		return cached, nil
	}
	cred, err := CredentialE()
	if err != nil {
		return Account{}, err
	}
	client, err := armstorage.NewAccountsClient(subscription, cred, nil)
	if err != nil {
		return Account{}, fmt.Errorf("storage accounts client: %w", err)
	}
	want := strings.ReplaceAll(strings.ToLower(prefix), "-", "")
	var found []Account
	pager := client.NewListByResourceGroupPager(resourceGroup, nil)
	for pager.More() {
		page, err := pager.NextPage(context.Background())
		if err != nil {
			return Account{}, fmt.Errorf("failed to list storage accounts in %s: %w", resourceGroup, err)
		}
		for _, a := range page.Value {
			if a.Name == nil || !strings.HasPrefix(*a.Name, want) {
				continue
			}
			account := Account{Name: *a.Name, BlobEndpoint: fmt.Sprintf("https://%s.blob.core.windows.net/", *a.Name)}
			if a.Properties != nil && a.Properties.PrimaryEndpoints != nil && a.Properties.PrimaryEndpoints.Blob != nil {
				account.BlobEndpoint = *a.Properties.PrimaryEndpoints.Blob
			}
			found = append(found, account)
		}
	}
	switch len(found) {
	case 1:
		accountsMu.Lock()
		accounts[key] = found[0]
		accountsMu.Unlock()
		return found[0], nil
	case 0:
		return Account{}, fmt.Errorf("no storage account named %s* in resource group %s: has the agent bootstrapped the environment?", want, resourceGroup)
	default:
		names := make([]string, len(found))
		for i, a := range found {
			names[i] = a.Name
		}
		return Account{}, fmt.Errorf("several storage accounts named %s* in resource group %s: %s", want, resourceGroup, strings.Join(names, ", "))
	}
}

// StorageAccount is the agent's state storage account of the environment.
func StorageAccount(t logger.T, e *env.Environment) Account {
	t.Helper()
	account, err := StorageAccountE(e.MustSubscriptionID(t), ResourceGroup(t, e), e.Prefix)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return account
}

// ReadBlobE returns a blob of the account.
func ReadBlobE(account Account, container, blob string) ([]byte, error) {
	cred, err := CredentialE()
	if err != nil {
		return nil, err
	}
	client, err := azblob.NewClient(account.BlobEndpoint, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("blob client for %s: %w", account.Name, err)
	}
	response, err := client.DownloadStream(context.Background(), container, blob, nil)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	return io.ReadAll(response.Body)
}

// ReadJSON reads a JSON blob of the state container, failing the test when
// it is missing or malformed.
func ReadJSON(t logger.T, e *env.Environment, account Account, blob string) map[string]any {
	t.Helper()
	data, err := ReadBlobE(account, StateContainer, blob)
	if err != nil {
		t.Fatalf("failed to read %s/%s in %s: %v", StateContainer, blob, account.Name, err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("%s/%s is not JSON: %v", StateContainer, blob, err)
	}
	return out
}

// BlobExistsE reports whether a blob exists in the account.
func BlobExistsE(account Account, container, blob string) (bool, error) {
	_, err := ReadBlobE(account, container, blob)
	if err == nil {
		return true, nil
	}
	if strings.Contains(err.Error(), "BlobNotFound") || strings.Contains(err.Error(), "404") {
		return false, nil
	}
	return false, err
}

// WaitUntilBlobExists waits for a blob to appear in the account.
func WaitUntilBlobExists(t logger.T, account Account, container, blob string, attempts int, sleep time.Duration) {
	t.Helper()
	retry.DoWithRetry(t, fmt.Sprintf("Wait for blob %s/%s in %s", container, blob, account.Name), attempts, sleep, func() (string, error) {
		exists, err := BlobExistsE(account, container, blob)
		if err != nil {
			return "", err
		}
		if !exists {
			return "", fmt.Errorf("not there yet")
		}
		return "found", nil
	})
}
