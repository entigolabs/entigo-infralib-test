package k8s

import (
	"fmt"
	"net"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/entigolabs/entigo-infralib-test/logger"
)

// Route is what a reachability check needs, read from an HTTPRoute and its
// parent Gateway rather than typed in by the test: the hostname the route
// serves, the listener's scheme and port, and the gateway's address.
type Route struct {
	Name      string
	Namespace string
	Hostname  string
	// Scheme is https or http, from the listener the route attaches to.
	Scheme string
	Port   int64
	// Gateway is the parent Gateway (namespace/name) and Listener its sectionName.
	Gateway          string
	GatewayNamespace string
	Listener         string
	// Address is the gateway's load balancer hostname or IP. Checks pin
	// connections to it, so they do not depend on public DNS having caught up.
	Address string
}

// URL is the route's base URL, e.g. https://app.example.com.
func (r Route) URL() string {
	if (r.Scheme == "https" && r.Port == 443) || (r.Scheme == "http" && r.Port == 80) || r.Port == 0 {
		return fmt.Sprintf("%s://%s", r.Scheme, r.Hostname)
	}
	return fmt.Sprintf("%s://%s:%d", r.Scheme, r.Hostname, r.Port)
}

// Internal reports whether the gateway's address is reachable only from
// inside the network: an AWS internal load balancer hostname, or a private IP.
func (r Route) Internal() bool { return IsInternalAddress(r.Address) }

// IsInternalAddress recognises internal load balancer addresses: AWS names
// them internal-<name>.elb.amazonaws.com; Google and Oracle hand out private
// IPs for internal load balancers.
func IsInternalAddress(address string) bool {
	if strings.HasPrefix(address, "internal-") {
		return true
	}
	if ip := net.ParseIP(address); ip != nil {
		return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()
	}
	return false
}

// RouteE reads the HTTPRoute of the given name in the client's namespace and
// its parent Gateway.
func (c *Client) RouteE(name string) (Route, error) {
	route, err := c.GetObjectE(HTTPRoutes, c.Namespace, name)
	if err != nil {
		return Route{}, fmt.Errorf("httproute %s/%s: %w", c.Namespace, name, err)
	}
	parents, _, _ := unstructured.NestedSlice(route.Object, "spec", "parentRefs")
	if len(parents) == 0 {
		return Route{}, fmt.Errorf("httproute %s/%s has no parentRefs", c.Namespace, name)
	}
	parent, _ := parents[0].(map[string]any)
	gwNamespace := GetStringValue(parent, "namespace")
	if gwNamespace == "" {
		gwNamespace = c.Namespace
	}
	gateway, err := c.GetObjectE(Gateways, gwNamespace, GetStringValue(parent, "name"))
	if err != nil {
		return Route{}, fmt.Errorf("gateway %s/%s of httproute %s: %w", gwNamespace, GetStringValue(parent, "name"), name, err)
	}
	return routeFromObjects(route, gateway)
}

// routeFromObjects derives a Route from an HTTPRoute and its parent Gateway.
func routeFromObjects(route, gateway *unstructured.Unstructured) (Route, error) {
	r := Route{Name: route.GetName(), Namespace: route.GetNamespace(), Gateway: gateway.GetName(), GatewayNamespace: gateway.GetNamespace()}
	hostnames, _, _ := unstructured.NestedStringSlice(route.Object, "spec", "hostnames")
	if len(hostnames) == 0 {
		return r, fmt.Errorf("httproute %s/%s has no hostnames", r.Namespace, r.Name)
	}
	r.Hostname = hostnames[0]
	parents, _, _ := unstructured.NestedSlice(route.Object, "spec", "parentRefs")
	if len(parents) > 0 {
		parent, _ := parents[0].(map[string]any)
		r.Listener = GetStringValue(parent, "sectionName")
	}
	listeners, _, _ := unstructured.NestedSlice(gateway.Object, "spec", "listeners")
	var chosen map[string]any
	for _, l := range listeners {
		listener, _ := l.(map[string]any)
		if r.Listener == "" || GetStringValue(listener, "name") == r.Listener {
			chosen = listener
			break
		}
	}
	if chosen == nil {
		return r, fmt.Errorf("gateway %s/%s has no listener %q", r.GatewayNamespace, r.Gateway, r.Listener)
	}
	r.Listener = GetStringValue(chosen, "name")
	r.Port, _, _ = unstructured.NestedInt64(chosen, "port")
	switch strings.ToUpper(GetStringValue(chosen, "protocol")) {
	case "HTTPS", "TLS":
		r.Scheme = "https"
	default:
		r.Scheme = "http"
	}
	r.Address = GetK8SGatewayAddress(gateway)
	return r, nil
}

// WaitUntilRouteReachable waits for the HTTPRoute of the given name in the
// client's namespace to be accepted by its gateway and for GET / to answer
// 200 through the gateway's address, probed from inside the cluster. It
// returns the derived Route so a test can assert on it (Internal, Hostname).
func WaitUntilRouteReachable(t logger.T, c *Client, name string, retries int, sleepBetweenRetries time.Duration) Route {
	t.Helper()
	return WaitUntilRouteAnswers(t, c, name, "/", "200", retries, sleepBetweenRetries)
}

// WaitUntilRouteAnswers is WaitUntilRouteReachable for a path and status code.
func WaitUntilRouteAnswers(t logger.T, c *Client, name, path, successCode string, retries int, sleepBetweenRetries time.Duration) Route {
	t.Helper()
	if _, err := WaitUntilK8SHTTPRouteAvailable(t, c, name, retries, sleepBetweenRetries); err != nil {
		t.Fatalf("httproute %s/%s not accepted: %v", c.Namespace, name, err)
	}
	var route Route
	for i := 0; ; i++ {
		var err error
		route, err = c.RouteE(name)
		if err != nil {
			t.Fatalf("%v", err)
		}
		if route.Address != "" {
			break
		}
		if i >= retries {
			t.Fatalf("gateway %s/%s has no address", route.GatewayNamespace, route.Gateway)
		}
		logger.Logf(t, "Waiting for gateway %s/%s to get an address", route.GatewayNamespace, route.Gateway)
		time.Sleep(sleepBetweenRetries)
	}
	url := route.URL() + "/" + strings.TrimPrefix(path, "/")
	logger.Logf(t, "Checking %s via %s (listener %s)", url, route.Address, route.Listener)
	if err := WaitUntilHostnameAvailableWithAddress(t, c, route.Address, url, successCode, retries, sleepBetweenRetries); err != nil {
		t.Fatalf("%v", err)
	}
	return route
}
