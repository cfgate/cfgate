// Package accesstags contains shared helpers for cfgate Cloudflare Access tags.
package accesstags

import "strings"

// IsOwnerTag reports whether name is a generated cfgate Access owner tag.
func IsOwnerTag(name string) bool {
	const prefix = "cfgate:"
	if len(name) != len(prefix)+28 || !strings.HasPrefix(name, prefix) {
		return false
	}
	for _, r := range name[len(prefix):] {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// ApplicationTagNames normalizes Cloudflare SDK Access application tags.
func ApplicationTagNames(tags interface{}) []string {
	switch typed := tags.(type) {
	case nil:
		return nil
	case []string:
		return typed
	case []interface{}:
		names := make([]string, 0, len(typed))
		for _, tag := range typed {
			if name, ok := tag.(string); ok {
				names = append(names, name)
			}
		}
		return names
	default:
		return nil
	}
}
