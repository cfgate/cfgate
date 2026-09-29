// Package e2ecleanup identifies resources created by the E2E test harness.
package e2ecleanup

import (
	"regexp"
	"strings"
	"time"
	"unicode"
)

var runIDPattern = regexp.MustCompile(`^[a-z0-9]{1,20}$`)
var resourcePattern = regexp.MustCompile(`^e2e-([a-z0-9]{1,20})-[a-z0-9-]+-[0-9]+-[0-9]+(?:-token)?$`)

// ValidRunID reports whether an override fits names and Kubernetes run labels.
func ValidRunID(id string) bool { return runIDPattern.MatchString(id) }

// Matches reports whether a resource name or domain contains a complete test marker.
func Matches(name, runID string) bool {
	for _, part := range strings.FieldsFunc(name, func(r rune) bool { return r == '.' || r == '/' || unicode.IsSpace(r) }) {
		match := resourcePattern.FindStringSubmatch(part)
		if len(match) > 1 && (runID == "" || match[1] == runID) {
			return true
		}
	}
	return false
}

// Select permits current-run teardown or explicitly enabled, aged orphan cleanup.
func Select(name, runID string, created, now time.Time, current, orphans bool, minAge time.Duration) bool {
	if ValidRunID(runID) && Matches(name, runID) {
		return current
	}
	return orphans && Matches(name, "") && minAge > 0 && !created.IsZero() && now.Sub(created) >= minAge
}

// NamespaceSelected keeps ordinary cleanup confined to paired run labels.
func NamespaceSelected(labels map[string]string, runID string, created, now time.Time, orphans bool, minAge time.Duration) bool {
	if labels["cfgate.io/e2e-test"] != "true" {
		return false
	}
	if ValidRunID(runID) && labels["cfgate.io/e2e-run"] == runID {
		return true
	}
	return orphans && ValidRunID(labels["cfgate.io/e2e-run"]) && minAge > 0 && !created.IsZero() && now.Sub(created) >= minAge
}
