package ambassador

import (
	"bytes"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"text/template"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/spf13/pflag"

	"github.com/kubeshop/kusk/generators"
	"github.com/kubeshop/kusk/options"
)

var (
	mappingTemplate     *template.Template
	rateLimitTemplate   *template.Template
	reDuplicateNewlines = regexp.MustCompile(`\s*\n+`)
	rePathSymbols       = regexp.MustCompile(`[/{}]`)
)

func init() {
	mappingTemplate = template.New("mapping")
	mappingTemplate = template.Must(mappingTemplate.Parse(mappingTemplateRaw))

	rateLimitTemplate = template.New("rateLimit")
	rateLimitTemplate = template.Must(rateLimitTemplate.Parse(rateLimitTemplateRaw))
}

func init() {
	generators.Registry["ambassador"] = &Generator{}
}

type Generator struct{}

func (g *Generator) ShortDescription() string {
	return "Generates Ambassador Mappings for your service"
}

func (g *Generator) LongDescription() string {
	return g.ShortDescription()
}

func (g *Generator) Cmd() string {
	return "ambassador"
}

func (g *Generator) Flags() *pflag.FlagSet {
	fs := pflag.NewFlagSet("ambassador", pflag.ExitOnError)

	fs.String(
		"path.base",
		"/",
		"a base path for Service endpoints",
	)

	fs.String(
		"path.trim_prefix",
		"",
		"a prefix to trim from the URL before forwarding to the upstream Service",
	)

	fs.Bool(
		"path.split",
		false,
		"force Kusk to generate a separate Mapping for each operation",
	)

	fs.Uint32(
		"rate_limits.rps",
		0,
		"request per second rate limit",
	)

	fs.Uint32(
		"rate_limits.burst",
		0,
		"request per second burst",
	)

	fs.Uint32(
		"timeouts.request_timeout",
		0,
		"total request timeout (seconds)",
	)

	fs.Uint32(
		"timeouts.idle_timeout",
		0,
		"idle connection timeout (seconds)",
	)

	fs.String(
		"host",
		"",
		"the Host header value to listen on",
	)

	return fs
}

func (g *Generator) Generate(opts *options.Options, spec *openapi3.T) (string, error) {
	if err := opts.FillDefaultsAndValidate(); err != nil {
		return "", fmt.Errorf("failed to validate options: %w", err)
	}

	var mappings []mappingTemplateData
	rateLimits := make(map[string]*rateLimitTemplateData)

	serviceURL := g.getServiceURL(opts)

	if g.shouldSplit(opts, spec) {
		// generate a mapping for each operation
		basePath := opts.Path.Base
		if basePath == "/" {
			basePath = ""
		}

		host := opts.Host

		for path, pathItem := range spec.Paths {
			pathSubOptions, _ := opts.PathSubOptions[path]

			if pathSubOptions.Host != "" && pathSubOptions.Host != host {
				host = pathSubOptions.Host
			}

			for method, operation := range pathItem.Operations() {
				if opts.IsOperationDisabled(path, method) {
					continue
				}

				opSubOptions := opts.OperationSubOptions[method+path]
				if opSubOptions.Host != "" && opSubOptions.Host != host {
					host = opSubOptions.Host
				}

				mappingPath, regex := g.generateMappingPath(path, operation)
				mappingName := g.generateMappingName(opts.Service.Name, method, path, operation)

				op := mappingTemplateData{
					MappingName:      mappingName,
					MappingNamespace: opts.Namespace,
					ServiceURL:       serviceURL,
					BasePath:         basePath,
					TrimPrefix:       opts.Path.TrimPrefix,
					Method:           method,
					Path:             mappingPath,
					Regex:            regex,
					Host:             host,
				}

				// take global CORS options
				corsOpts := opts.CORS

				// if non-zero path-level CORS options are different, override with them
				if pathSubOpts, ok := opts.PathSubOptions[path]; ok {
					if !reflect.DeepEqual(options.CORSOptions{}, pathSubOpts.CORS) &&
						!reflect.DeepEqual(corsOpts, pathSubOpts.CORS) {
						corsOpts = pathSubOpts.CORS
					}
				}

				// if non-zero operation-level CORS options are different, override them
				if opSubOpts, ok := opts.OperationSubOptions[path]; ok {
					if !reflect.DeepEqual(options.CORSOptions{}, opSubOpts.CORS) &&
						!reflect.DeepEqual(corsOpts, opSubOpts.CORS) {
						corsOpts = opSubOpts.CORS
					}
				}

				// if final CORS options are not empty, include them
				if !reflect.DeepEqual(options.CORSOptions{}, corsOpts) {
					op.CORSEnabled = true
					op.CORS = g.corsTemplateData(&corsOpts)
				}

				// take global rate limit options
				rateLimitOpts := opts.RateLimits

				// if non-zero path-level rate limit options are different, override with them
				if pathSubOpts, ok := opts.PathSubOptions[path]; ok {
					if !reflect.DeepEqual(options.RateLimitOptions{}, pathSubOpts.RateLimits) &&
						!reflect.DeepEqual(rateLimitOpts, pathSubOpts.RateLimits) {
						rateLimitOpts = pathSubOpts.RateLimits
					}
				}

				// if non-zero operation-level rate limit options are different, override them
				if opSubOpts, ok := opts.OperationSubOptions[path]; ok {
					if !reflect.DeepEqual(options.RateLimitOptions{}, opSubOpts.RateLimits) &&
						!reflect.DeepEqual(rateLimitOpts, opSubOpts.RateLimits) {
						rateLimitOpts = opSubOpts.RateLimits
					}
				}

				// if final rate limit options are not empty, include them
				if !reflect.DeepEqual(options.RateLimitOptions{}, rateLimitOpts) {
					rps := rateLimitOpts.RPS

					var burstFactor uint32

					if burst := rateLimitOpts.Burst; burst != 0 && rps != 0 {
						// https://www.getambassador.io/docs/edge-stack/1.13/topics/using/rate-limits/rate-limits/
						// ambassador uses a burst multiplier to configure burst for a rate limited path,
						// i.e. burst = rps * burstMultiplier

						burstFactor = burst / rps
						if burstFactor < 1 {
							burstFactor = 1
						}
					}

					if rateLimitOpts.Group != "" {
						// rate limit uses group, check that it wasn't already configured
						if rl, ok := rateLimits[rateLimitOpts.Group]; ok {
							// rate limit already configured within this group, replace limits if new ones are lower
							if burstFactor < rl.BurstFactor {
								rl.BurstFactor = burstFactor
							}

							if rps < rl.Rate {
								rl.Rate = rps
							}
						} else {
							rateLimits[rateLimitOpts.Group] = &rateLimitTemplateData{
								Name:        opts.Service.Name + "-" + rateLimitOpts.Group,
								Operation:   mappingName,
								Rate:        rps,
								BurstFactor: burstFactor,
								Group:       rateLimitOpts.Group,
							}
						}
					} else {
						// rate limit on this operation does not use grouping
						rateLimits[mappingName] = &rateLimitTemplateData{
							Name:        opts.Service.Name + "-" + mappingName,
							Operation:   mappingName,
							Rate:        rps,
							BurstFactor: burstFactor,
						}
					}

					op.LabelsEnabled = true
					op.RateLimitGroup = rateLimitOpts.Group
				}

				// take global timeout options
				timeoutOpts := opts.Timeouts

				// if non-zero path-level timeout options are different, override with them
				if pathSubOpts, ok := opts.PathSubOptions[path]; ok {
					if !reflect.DeepEqual(options.TimeoutOptions{}, pathSubOpts.Timeouts) &&
						!reflect.DeepEqual(timeoutOpts, pathSubOpts.Timeouts) {
						timeoutOpts = pathSubOpts.Timeouts
					}
				}

				// if non-zero operation-level timeout options are different, override them
				if opSubOpts, ok := opts.OperationSubOptions[path]; ok {
					if !reflect.DeepEqual(options.TimeoutOptions{}, opSubOpts.Timeouts) &&
						!reflect.DeepEqual(timeoutOpts, opSubOpts.Timeouts) {
						timeoutOpts = opSubOpts.Timeouts
					}
				}

				// if final timeout options are not empty, include them
				if !reflect.DeepEqual(options.TimeoutOptions{}, timeoutOpts) {
					op.RequestTimeout = timeoutOpts.RequestTimeout * 1000
					op.IdleTimeout = timeoutOpts.IdleTimeout * 1000
				}

				mappings = append(mappings, op)
			}
		}
	} else if !opts.Disabled {
		op := mappingTemplateData{
			MappingName:      opts.Service.Name,
			MappingNamespace: opts.Namespace,
			ServiceURL:       serviceURL,
			BasePath:         opts.Path.Base,
			TrimPrefix:       opts.Path.TrimPrefix,
			RequestTimeout:   opts.Timeouts.RequestTimeout * 1000,
			IdleTimeout:      opts.Timeouts.IdleTimeout * 1000,
			Host:             opts.Host,
		}

		// if global CORS options are defined, take them
		if !reflect.DeepEqual(options.CORSOptions{}, opts.CORS) {
			op.CORSEnabled = true
			op.CORS = g.corsTemplateData(&opts.CORS)
		}

		// if global rate limit options are defined, take them
		if !reflect.DeepEqual(options.RateLimitOptions{}, opts.RateLimits) {
			op.LabelsEnabled = true

			if opts.RateLimits.Group == "" {
				opts.RateLimits.Group = "default"
			}

			op.RateLimitGroup = opts.RateLimits.Group

			rps := opts.RateLimits.RPS

			var burstFactor uint32

			if burst := opts.RateLimits.Burst; burst != 0 && rps != 0 {
				// https://www.getambassador.io/docs/edge-stack/1.13/topics/using/rate-limits/rate-limits/
				// ambassador uses a burst multiplier to configure burst for a rate limited path,
				// i.e. burst = rps * burstMultiplier

				burstFactor = burst / rps
				if burstFactor < 1 {
					burstFactor = 1
				}
			}

			rateLimits["default"] = &rateLimitTemplateData{
				Name:        "default",
				Operation:   opts.Service.Name,
				Rate:        rps,
				BurstFactor: burstFactor,
				Group:       opts.RateLimits.Group,
			}
		}

		mappings = append(mappings, op)
	}

	// We need to sort mappings as in the process of conversion of YAML to JSON
	// the Go map's access mechanics randomize the order and therefore the output is shuffled.
	// Not only it makes tests fail, it would also affect people who would use this in order to
	// generate manifests and use them in GitOps processes
	sort.Slice(mappings, func(i, j int) bool {
		return mappings[i].MappingName < mappings[j].MappingName
	})

	// flat out rate limits from map to array to sort them
	var rateLimitsArray []rateLimitTemplateData
	for _, rl := range rateLimits {
		rateLimitsArray = append(rateLimitsArray, *rl)
	}

	sort.Slice(rateLimitsArray, func(i, j int) bool {
		return rateLimitsArray[i].Operation < rateLimitsArray[j].Operation
	})

	var buf bytes.Buffer

	err := mappingTemplate.Execute(&buf, mappings)
	if err != nil {
		return "", fmt.Errorf("failed to execute mapping template: %w", err)
	}

	err = rateLimitTemplate.Execute(&buf, rateLimits)
	if err != nil {
		return "", fmt.Errorf("failed to execute rate limit template: %w", err)
	}

	res := buf.String()

	return reDuplicateNewlines.ReplaceAllString(res, "\n"), nil
}

// generateMappingPath returns the final pattern that should go to mapping
// and whether the regex should be used
func (g *Generator) generateMappingPath(path string, op *openapi3.Operation) (string, bool) {
	containsPathParameter := false
	for _, param := range op.Parameters {
		if param.Value.In == "path" {
			containsPathParameter = true
			break
		}
	}

	if !containsPathParameter {
		return path, false
	}

	// replace each parameter with appropriate regex
	for _, param := range op.Parameters {
		if param.Value.In != "path" {
			continue
		}

		// the regex evaluation for mapping routes is actually done
		// within Envoy, which uses ECMA-262 regex grammar
		// https://www.envoyproxy.io/docs/envoy/v1.5.0/api-v1/route_config/route#route
		// https://en.cppreference.com/w/cpp/regex/ecmascript
		// https://www.getambassador.io/docs/edge-stack/latest/topics/using/rewrites/#regex_rewrite

		replaceWith := `([a-zA-Z0-9]*)`

		oldParam := "{" + param.Value.Name + "}"

		path = strings.ReplaceAll(path, oldParam, replaceWith)
	}

	return path, true
}

func (g *Generator) generateMappingName(serviceName, method, path string, operation *openapi3.Operation) string {
	var res strings.Builder

	if operation.OperationID != "" {
		res.WriteString(serviceName)
		res.WriteString("-")
		res.WriteString(operation.OperationID)
		return strings.ToLower(res.String())
	}

	// generate proper mapping name if operationId is missing
	res.WriteString(serviceName)
	res.WriteString("-")
	res.WriteString(method)
	res.WriteString(rePathSymbols.ReplaceAllString(path, ""))

	return strings.ToLower(res.String())
}

func (g *Generator) getServiceURL(options *options.Options) string {
	if options.Service.Port > 0 {
		return fmt.Sprintf(
			"%s.%s:%d",
			options.Service.Name,
			options.Service.Namespace,
			options.Service.Port,
		)
	}

	return fmt.Sprintf("%s.%s", options.Service.Name, options.Service.Namespace)
}

func (g *Generator) shouldSplit(opts *options.Options, spec *openapi3.T) bool {
	if opts.Path.Split {
		return true
	}

	for path, pathItem := range spec.Paths {
		for method := range pathItem.Operations() {
			if opts.IsOperationDisabled(path, method) {
				return true
			}
		}
		if opts.IsPathDisabled(path) {
			return true
		}
	}

	for path, pathItem := range spec.Paths {
		if pathSubOptions, ok := opts.PathSubOptions[path]; ok {
			// a path has non-zero, different from global scope CORS options
			if !reflect.DeepEqual(options.CORSOptions{}, pathSubOptions.CORS) &&
				!reflect.DeepEqual(opts.CORS, pathSubOptions.CORS) {
				return true
			}

			// a path has non-zero, different from global scope rate limits options
			if !reflect.DeepEqual(options.RateLimitOptions{}, pathSubOptions.RateLimits) &&
				!reflect.DeepEqual(opts.RateLimits, pathSubOptions.RateLimits) {
				return true
			}

			// a path has non-zero, different from global scope timeouts options
			if !reflect.DeepEqual(options.TimeoutOptions{}, pathSubOptions.Timeouts) &&
				!reflect.DeepEqual(opts.Timeouts, pathSubOptions.Timeouts) {
				return true
			}
		}

		for method := range pathItem.Operations() {
			if opSubOptions, ok := opts.OperationSubOptions[method+path]; ok {
				// an operation has non-zero, different from global CORS options
				if !reflect.DeepEqual(options.CORSOptions{}, opSubOptions.CORS) &&
					!reflect.DeepEqual(opts.CORS, opSubOptions.CORS) {
					return true
				}

				// an operation has non-zero, different from global scope rate limits options
				if !reflect.DeepEqual(options.RateLimitOptions{}, opSubOptions.RateLimits) &&
					!reflect.DeepEqual(opts.RateLimits, opSubOptions.RateLimits) {
					return true
				}

				// an operation has non-zero, different from global timeouts options
				if !reflect.DeepEqual(options.TimeoutOptions{}, opSubOptions.Timeouts) &&
					!reflect.DeepEqual(opts.Timeouts, opSubOptions.Timeouts) {
					return true
				}
			}
		}
	}

	return false
}

func (g *Generator) corsTemplateData(corsOpts *options.CORSOptions) corsTemplateData {
	res := corsTemplateData{
		Origins:        strings.Join(corsOpts.Origins, ","),
		Methods:        strings.Join(corsOpts.Methods, ","),
		Headers:        strings.Join(corsOpts.Headers, ","),
		ExposedHeaders: strings.Join(corsOpts.ExposeHeaders, ","),
		MaxAge:         fmt.Sprint(corsOpts.MaxAge),
	}

	if corsOpts.Credentials != nil {
		res.Credentials = *corsOpts.Credentials
	}

	return res
}
