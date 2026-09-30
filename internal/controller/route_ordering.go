package controller

import (
	"cfgate.io/cfgate/internal/cloudflare"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gateway "sigs.k8s.io/gateway-api/apis/v1"
	"strings"
)

type orderedIngressRule struct {
	cloudflare.IngressRule
	accessDenied          bool
	pathType              gateway.PathMatchType
	pathLength            int
	created               metav1.Time
	routeName             string
	ruleIndex, matchIndex int
}

func flattenIngressRules(rules []orderedIngressRule) []cloudflare.IngressRule {
	out := make([]cloudflare.IngressRule, len(rules))
	for i := range rules {
		out[i] = rules[i].IngressRule
	}
	return out
}

func routePathPrecedence(match gateway.HTTPRouteMatch) (gateway.PathMatchType, int) {
	kind := gateway.PathMatchPathPrefix
	path := "/"
	if match.Path != nil {
		if match.Path.Type != nil {
			kind = *match.Path.Type
		}
		if match.Path.Value != nil {
			path = *match.Path.Value
		}
	}
	if kind == gateway.PathMatchPathPrefix {
		path = strings.TrimRight(path, "/")
		if path == "" {
			path = "/"
		}
	}
	return kind, len(path)
}

func pathTypeRank(kind gateway.PathMatchType) int {
	switch kind {
	case gateway.PathMatchExact:
		return 0
	case gateway.PathMatchRegularExpression:
		return 1
	default:
		return 2
	}
}

func ingressRuleBefore(a, b orderedIngressRule) bool {
	// More specific hostnames precede overlapping wildcard suffixes.
	if a.Hostname != b.Hostname {
		ah, bh := strings.TrimPrefix(a.Hostname, "*"), strings.TrimPrefix(b.Hostname, "*")
		if len(ah) != len(bh) {
			return len(ah) > len(bh)
		}
		return a.Hostname < b.Hostname
	}
	ar, br := pathTypeRank(a.pathType), pathTypeRank(b.pathType)
	if ar != br {
		return ar < br
	}
	if a.pathLength != b.pathLength {
		return a.pathLength > b.pathLength
	}
	// Equal matches must not let an older public route bypass required protection.
	if a.accessDenied != b.accessDenied {
		return a.accessDenied
	}
	if !a.created.Equal(&b.created) {
		return a.created.Before(&b.created)
	}
	if a.routeName != b.routeName {
		return a.routeName < b.routeName
	}
	if a.ruleIndex != b.ruleIndex {
		return a.ruleIndex < b.ruleIndex
	}
	return a.matchIndex < b.matchIndex
}
