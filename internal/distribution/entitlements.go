package distribution

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
)

var supportedEntitlements = map[string]bool{
	"application-identifier":                              true,
	"com.apple.developer.team-identifier":                 true,
	"get-task-allow":                                      true,
	"keychain-access-groups":                              true,
	"aps-environment":                                     true,
	"com.apple.developer.associated-domains":              true,
	"com.apple.developer.icloud-container-identifiers":    true,
	"com.apple.developer.icloud-services":                 true,
	"com.apple.developer.ubiquity-container-identifiers":  true,
	"com.apple.developer.ubiquity-kvstore-identifier":     true,
	"com.apple.developer.networking.networkextension":     true,
	"com.apple.developer.networking.wifi-info":            true,
	"com.apple.developer.default-data-protection":         true,
	"com.apple.developer.healthkit":                       true,
	"com.apple.developer.homekit":                         true,
	"com.apple.developer.in-app-payments":                 true,
	"com.apple.developer.pass-type-identifiers":           true,
	"com.apple.developer.siri":                            true,
	"com.apple.developer.usernotifications.communication": true,
	"com.apple.security.application-groups":               true,
}

func wildcardMatch(grant, requested string) bool {
	if grant == requested {
		return true
	}
	if strings.Count(grant, "*") != 1 || !strings.HasSuffix(grant, "*") {
		return false
	}
	return strings.HasPrefix(requested, strings.TrimSuffix(grant, "*"))
}

func entitlementValueAllowed(grant, requested any) bool {
	switch rv := requested.(type) {
	case string:
		gv, ok := grant.(string)
		return ok && wildcardMatch(gv, rv)
	case []any:
		gv, ok := grant.([]any)
		if !ok {
			return false
		}
		for _, wanted := range rv {
			matched := false
			for _, allowed := range gv {
				if entitlementValueAllowed(allowed, wanted) {
					matched = true
					break
				}
			}
			if !matched {
				return false
			}
		}
		return true
	case []string:
		for _, wanted := range rv {
			if !entitlementValueAllowed(grant, []any{wanted}) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(grant, requested)
	}
}

// ValidateEntitlements checks an explicit requested subset against profile
// grants. Unsupported keys are rejected instead of silently discarded.
func ValidateEntitlements(profile, requested map[string]any) []Problem {
	var problems []Problem
	keys := make([]string, 0, len(requested))
	for key := range requested {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		want := requested[key]
		if !supportedEntitlements[key] {
			problems = append(problems, problem("unsupported_entitlement", "entitlements."+key, fmt.Sprintf("entitlement %q is not supported by this exporter", key)))
			continue
		}
		grant, ok := profile[key]
		if !ok {
			problems = append(problems, problem("entitlement_not_granted", "entitlements."+key, fmt.Sprintf("profile does not grant entitlement %q", key)))
			continue
		}
		if !entitlementValueAllowed(grant, want) {
			problems = append(problems, problem("entitlement_value_not_granted", "entitlements."+key, fmt.Sprintf("requested value for %q is not compatible with the profile grant", key)))
		}
	}
	return problems
}

func exactSigningEntitlements(profile map[string]any, requested map[string]any, appIDPrefix, teamID, bundleID string) (map[string]any, []Problem) {
	expected := map[string]any{
		"application-identifier":              appIDPrefix + "." + bundleID,
		"com.apple.developer.team-identifier": teamID,
		"get-task-allow":                      false,
	}
	var problems []Problem
	for _, key := range []string{"application-identifier", "com.apple.developer.team-identifier", "get-task-allow"} {
		if value, present := requested[key]; present && !equalPlistValue(value, expected[key]) {
			problems = append(problems, problem("required_entitlement_conflict", "entitlements."+key, fmt.Sprintf("requested %q conflicts with the required signing value", key)))
		}
	}
	exact := cloneMap(requested)
	for key, value := range expected {
		exact[key] = value
	}
	if grant, ok := profile["keychain-access-groups"]; ok {
		if _, requestedExplicitly := requested["keychain-access-groups"]; !requestedExplicitly {
			candidate := []any{appIDPrefix + "." + bundleID}
			if entitlementValueAllowed(grant, candidate) {
				exact["keychain-access-groups"] = candidate
			}
		}
	}
	problems = append(problems, ValidateEntitlements(profile, exact)...)
	return exact, problems
}
