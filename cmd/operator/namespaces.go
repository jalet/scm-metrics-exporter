package main

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/cache"
)

// parseWatchNamespaces splits the --watch-namespaces value into unique, valid namespace names.
func parseWatchNamespaces(value string) ([]string, error) {
	var namespaces []string
	seen := map[string]struct{}{}
	for _, part := range strings.Split(value, ",") {
		ns := strings.TrimSpace(part)
		if ns == "" {
			continue
		}
		if errs := validation.IsDNS1123Label(ns); len(errs) > 0 {
			return nil, fmt.Errorf("namespace %q: %s", ns, strings.Join(errs, "; "))
		}
		if _, dup := seen[ns]; dup {
			continue
		}
		seen[ns] = struct{}{}
		namespaces = append(namespaces, ns)
	}
	return namespaces, nil
}

// cacheOptions limits the manager's informers to namespaces, so namespaced RBAC suffices.
func cacheOptions(namespaces []string) cache.Options {
	if len(namespaces) == 0 {
		return cache.Options{}
	}
	defaults := make(map[string]cache.Config, len(namespaces))
	for _, ns := range namespaces {
		defaults[ns] = cache.Config{}
	}
	return cache.Options{DefaultNamespaces: defaults}
}
