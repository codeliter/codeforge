package src

import (
	"fmt"
	"strings"

	"github.com/codeliter/codeforge/src/config"
)

func resolveModelArguments(args []string, policy *config.ProviderPolicy, catalog []string) ([]string, []config.ModelRoute, error) {
	out := append([]string(nil), args...)
	var routes []config.ModelRoute
	for i := 0; i < len(out); i++ {
		flag, value, equal := strings.Cut(out[i], "=")
		if flag == "--model" {
			at := i
			if !equal {
				if i+1 >= len(out) {
					return nil, nil, fmt.Errorf("%w: --model needs a value", config.ErrModelName)
				}
				i++
				value = out[i]
			}
			route, err := config.ResolveModelReference("command-line model", value, policy, catalog)
			if err != nil {
				return nil, nil, err
			}
			if equal {
				out[at] = "--model=" + route.Primary
			} else {
				out[i] = route.Primary
			}
			routes = append(routes, route)
		} else if flagsWithValue[flag] && !equal {
			i++
		}
	}
	return out, routes, nil
}

func argumentsNeedCatalog(args []string) bool {
	for i := 0; i < len(args); i++ {
		flag, value, equal := strings.Cut(args[i], "=")
		if flag == "--model" {
			if !equal {
				if i+1 >= len(args) {
					return true
				}
				i++
				value = args[i]
			}
			if !config.ValidModelID(value) {
				return true
			}
		} else if flagsWithValue[flag] && !equal {
			i++
		}
	}
	return false
}
