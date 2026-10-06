// Package tf reads OpenTofu outputs the agent stored after applying a step and
// picks typed values out of them.
//
// The outputs live in the agent's state bucket, so reading them needs the
// cloud's SDK. To keep a test (and the per-cloud test image) free of the SDKs
// of clouds it does not use, this package does not import them: each cloud
// package registers its reader in init(), and a test imports the cloud
// packages of the environments it runs against:
//
//	import _ "github.com/entigolabs/entigo-infralib-test/aws"
package tf

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/entigolabs/entigo-infralib-test/env"
	"github.com/entigolabs/entigo-infralib-test/logger"
)

// StepOutputsReader reads <prefix>-<step>/terraform-output.json from the
// environment's state bucket.
type StepOutputsReader func(t logger.T, e *env.Environment, file string) map[string]any

var (
	readersMu sync.RWMutex
	readers   = map[string]StepOutputsReader{}
)

// Register installs the reader for a cloud. The cloud packages call it from init().
func Register(cloud string, reader StepOutputsReader) {
	readersMu.Lock()
	defer readersMu.Unlock()
	readers[cloud] = reader
}

// Outputs is the parsed terraform-output.json of a step: output name to
// {"value": ..., "type": ..., "sensitive": ...}.
type Outputs map[string]any

// Get returns the outputs of the step the calling module was deployed in.
func Get(t logger.T, e *env.Environment) Outputs {
	t.Helper()
	p := env.ModulePlacement(t, e)
	return GetStep(t, e, p.Step.Name)
}

// GetStep returns the outputs of a named step of the environment. The agent
// writes them to <prefix>-<step>/terraform-output.json in the state bucket.
func GetStep(t logger.T, e *env.Environment, step string) Outputs {
	t.Helper()
	file := fmt.Sprintf("%s-%s/terraform-output.json", e.Prefix, step)
	readersMu.RLock()
	reader, ok := readers[e.Cloud]
	registered := make([]string, 0, len(readers))
	for cloud := range readers {
		registered = append(registered, cloud)
	}
	readersMu.RUnlock()
	if !ok {
		sort.Strings(registered)
		t.Fatalf("no output reader for cloud %q (registered: %v); import github.com/entigolabs/entigo-infralib-test/%s for its side effect", e.Cloud, registered, e.Cloud)
	}
	return reader(t, e, file)
}

// Value returns the raw value of an output, failing the test when it is absent.
func (o Outputs) Value(t logger.T, key string) any {
	t.Helper()
	output, ok := o[key].(map[string]any)
	if !ok {
		t.Fatalf("output %s not found in %s", key, o.keys())
	}
	value, exists := output["value"]
	if !exists {
		t.Fatalf("output %s has no value: %v", key, output)
	}
	return value
}

// String returns a string output.
func (o Outputs) String(t logger.T, key string) string {
	t.Helper()
	value := o.Value(t, key)
	s, ok := value.(string)
	if !ok {
		t.Fatalf("output %s is not a string: %v", key, value)
	}
	return s
}

// StringList returns a list of strings output, or nil when the output is not a
// list of strings (logged, not fatal, mirroring the previous helper).
func (o Outputs) StringList(t logger.T, key string) []string {
	t.Helper()
	value := o.Value(t, key)
	list, ok := value.([]any)
	if !ok {
		logger.Logf(t, "output %s is not a list", key)
		return nil
	}
	result := make([]string, len(list))
	for i, v := range list {
		s, ok := v.(string)
		if !ok {
			logger.Logf(t, "output %s element %d is not a string", key, i)
			return nil
		}
		result[i] = s
	}
	return result
}

// Map returns an object output.
func (o Outputs) Map(t logger.T, key string) map[string]any {
	t.Helper()
	value := o.Value(t, key)
	m, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("output %s is not an object: %v", key, value)
	}
	return m
}

// Has reports whether an output exists.
func (o Outputs) Has(key string) bool {
	_, ok := o[key]
	return ok
}

// HasKeyWithPrefix reports whether any output name starts with prefix.
func (o Outputs) HasKeyWithPrefix(prefix string) bool {
	for key := range o {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

func (o Outputs) keys() []string {
	keys := make([]string, 0, len(o))
	for k := range o {
		keys = append(keys, k)
	}
	return keys
}

// Compatibility wrappers with the previous helper names.

// GetValue is Outputs.Value.
func GetValue(t logger.T, outputs map[string]any, key string) any {
	t.Helper()
	return Outputs(outputs).Value(t, key)
}

// GetStringValue is Outputs.String.
func GetStringValue(t logger.T, outputs map[string]any, key string) string {
	t.Helper()
	return Outputs(outputs).String(t, key)
}

// GetStringListValue is Outputs.StringList.
func GetStringListValue(t logger.T, outputs map[string]any, key string) []string {
	t.Helper()
	return Outputs(outputs).StringList(t, key)
}

// HasKeyWithPrefix is Outputs.HasKeyWithPrefix.
func HasKeyWithPrefix(t logger.T, outputs map[string]any, prefix string) bool {
	return Outputs(outputs).HasKeyWithPrefix(prefix)
}
