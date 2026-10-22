package k8s

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"

	"github.com/entigolabs/entigo-infralib-test/logger"
)

// PortForwardServiceE forwards a free local port to a port of one running pod
// behind a Service in the client's namespace, the way `kubectl port-forward
// svc/<name> :<port>` does. It returns the local address (127.0.0.1:<port>)
// and a function that stops the forwarding. For a test that talks to a
// workload with no route of its own: an OTLP or query endpoint, say.
func PortForwardServiceE(t logger.T, c *Client, service string, port int) (string, func(), error) {
	t.Helper()
	clientset, err := c.Clientset()
	if err != nil {
		return "", nil, err
	}
	svc, err := clientset.CoreV1().Services(c.Namespace).Get(context.Background(), service, metav1.GetOptions{})
	if err != nil {
		return "", nil, fmt.Errorf("service %s/%s: %w", c.Namespace, service, err)
	}
	var servicePort *corev1.ServicePort
	for i := range svc.Spec.Ports {
		if int(svc.Spec.Ports[i].Port) == port {
			servicePort = &svc.Spec.Ports[i]
		}
	}
	if servicePort == nil {
		return "", nil, fmt.Errorf("service %s/%s has no port %d", c.Namespace, service, port)
	}
	if len(svc.Spec.Selector) == 0 {
		return "", nil, fmt.Errorf("service %s/%s has no selector to find a pod by", c.Namespace, service)
	}
	pods, err := clientset.CoreV1().Pods(c.Namespace).List(context.Background(), metav1.ListOptions{LabelSelector: labels.SelectorFromSet(svc.Spec.Selector).String()})
	if err != nil {
		return "", nil, err
	}
	var pod *corev1.Pod
	for i := range pods.Items {
		if IsPodAvailable(&pods.Items[i]) {
			pod = &pods.Items[i]
			break
		}
	}
	if pod == nil {
		return "", nil, fmt.Errorf("service %s/%s has no running pod", c.Namespace, service)
	}
	target := servicePort.TargetPort.IntValue()
	if target == 0 {
		// A named target port: find it in the pod's containers.
		for _, container := range pod.Spec.Containers {
			for _, p := range container.Ports {
				if p.Name == servicePort.TargetPort.String() {
					target = int(p.ContainerPort)
				}
			}
		}
	}
	if target == 0 {
		target = port
	}

	config, err := c.RestConfig()
	if err != nil {
		return "", nil, err
	}
	transport, upgrader, err := spdy.RoundTripperFor(config)
	if err != nil {
		return "", nil, err
	}
	host := strings.TrimPrefix(config.Host, "https://")
	host = strings.TrimPrefix(host, "http://")
	path := fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/portforward", pod.Namespace, pod.Name)
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, &url.URL{Scheme: "https", Host: host, Path: path})
	stopCh := make(chan struct{})
	readyCh := make(chan struct{})
	errCh := make(chan error, 1)
	forwarder, err := portforward.New(dialer, []string{fmt.Sprintf("0:%d", target)}, stopCh, readyCh, io.Discard, io.Discard)
	if err != nil {
		return "", nil, err
	}
	go func() { errCh <- forwarder.ForwardPorts() }()
	select {
	case <-readyCh:
	case err := <-errCh:
		return "", nil, fmt.Errorf("port-forward to %s/%s:%d: %w", pod.Namespace, pod.Name, target, err)
	}
	ports, err := forwarder.GetPorts()
	if err != nil || len(ports) == 0 {
		close(stopCh)
		return "", nil, fmt.Errorf("port-forward to %s/%s:%d: no local port: %v", pod.Namespace, pod.Name, target, err)
	}
	address := fmt.Sprintf("127.0.0.1:%d", ports[0].Local)
	logger.Logf(t, "Forwarding %s to %s/%s:%d (service %s:%d)", address, pod.Namespace, pod.Name, target, service, port)
	var once bool
	stop := func() {
		if !once {
			once = true
			close(stopCh)
		}
	}
	return address, stop, nil
}

// PortForwardService is PortForwardServiceE failing the test.
func PortForwardService(t logger.T, c *Client, service string, port int) (string, func()) {
	t.Helper()
	address, stop, err := PortForwardServiceE(t, c, service, port)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return address, stop
}
