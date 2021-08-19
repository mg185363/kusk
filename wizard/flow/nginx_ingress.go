package flow

import (
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/kubeshop/kusk/generators/nginx_ingress"
	"github.com/kubeshop/kusk/options"
)

type nginxIngressFlow struct {
	baseFlow
}

func (n nginxIngressFlow) Start() (Response, error) {
	var basePathSuggestions []string
	for _, server := range n.apiSpec.Servers {
		basePathSuggestions = append(basePathSuggestions, server.URL)
	}

	basePath := n.prompt.SelectOneOf("Base path prefix", basePathSuggestions, true)
	trimPrefix := n.prompt.Input("Prefix to trim from the URL (rewrite)", "")

	separateMappings := false
	if basePath != "" {
		separateMappings = n.prompt.Confirm("Generate ingress resource for each endpoint separately?")
	}

	var timeoutOptions options.TimeoutOptions

	// Support only request timeout as nginx-ingress generator doesn't support idle timeout
	if requestTimeout := n.prompt.Input("Request timeout, leave empty to skip", ""); requestTimeout != "" {
		if rTimeout, err := strconv.Atoi(requestTimeout); err != nil {
			log.Printf("WARN: %s is not a valid request timeout value. Skipping\n", requestTimeout)
		} else {
			timeoutOptions.RequestTimeout = uint32(rTimeout)
		}
	}

	opts := &options.Options{
		Namespace: n.targetNamespace,
		Service: options.ServiceOptions{
			Namespace: n.targetNamespace,
			Name:      n.targetService,
		},
		Path: options.PathOptions{
			Base:       basePath,
			TrimPrefix: trimPrefix,
			Split:      separateMappings,
		},
		Timeouts: timeoutOptions,
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("kusk ambassador -i %s ", n.apiSpecPath))
	sb.WriteString(fmt.Sprintf("--namespace=%s ", n.targetNamespace))
	sb.WriteString(fmt.Sprintf("--service.namespace=%s ", n.targetNamespace))
	sb.WriteString(fmt.Sprintf("--service.name=%s ", n.targetService))
	sb.WriteString(fmt.Sprintf("--path.base=%s ", basePath))

	if trimPrefix != "" {
		sb.WriteString(fmt.Sprintf("--path.trim_prefix=%s ", trimPrefix))
	}

	if separateMappings {
		sb.WriteString("--path.split ")
	}

	var ingressGenerator nginx_ingress.Generator
	ingresses, err := ingressGenerator.Generate(opts, n.apiSpec)
	if err != nil {
		return Response{}, fmt.Errorf("Failed to generate ingresses: %s\n", err)
	}

	return Response{
		EquivalentCmd: sb.String(),
		Manifests:     ingresses,
	}, nil
}
