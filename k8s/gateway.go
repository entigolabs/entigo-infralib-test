package k8s

import (
	"fmt"

	"github.com/entigolabs/entigo-infralib-test/env"
	"github.com/entigolabs/entigo-infralib-test/logger"
	"github.com/entigolabs/entigo-infralib-test/tf"
)

// Gateway derives the environment's ingress gateway from its modules, the
// way the agent configured them:
//
//   - namespace: the agent name of the gateway module (aws-alb, google-gateway
//     or oracle-gateway), which is also its ArgoCD application namespace;
//   - name: the module's global.externalGateway or global.internalGateway
//     input, falling back to the chart's default;
//   - domain: the pub_domain output of the environment's DNS module
//     (route53 or dns) for "external", int_domain for "internal".
//
// kind is "external" or "internal".
func Gateway(t logger.T, e *env.Environment, kind string) env.Gateway {
	t.Helper()
	config := env.MustLoad(t)
	gw, ok := config.GatewayModule(e)
	if !ok {
		t.Fatalf("environment %s has no gateway module (aws-alb, google-gateway or oracle-gateway)", e.Name)
	}
	dns, ok := config.DNSModule(e)
	if !ok {
		t.Fatalf("environment %s has no DNS module (route53 or dns)", e.Name)
	}
	var inputKey, outputKey string
	switch kind {
	case "external":
		inputKey, outputKey = "externalGateway", "pub_domain"
	case "internal":
		inputKey, outputKey = "internalGateway", "int_domain"
	default:
		t.Fatalf("gateway kind %q: external or internal", kind)
	}
	name := GetStringValue(gw.Module.Ref.Inputs, "global", inputKey)
	if name == "" {
		name = kind
		if e.Cloud == env.CloudGoogle {
			name = "google-gateway-" + kind
		}
	}
	domain := tf.GetStep(t, e, dns.Step.Name).String(t, fmt.Sprintf("%s__%s", dns.Module.AgentName(e), outputKey))
	retries := 100
	if e.Cloud == env.CloudGoogle {
		retries = 400
	}
	return env.Gateway{Name: name, Namespace: gw.Module.AgentName(e), Domain: domain, Retries: retries}
}
