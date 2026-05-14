package accesstags

import (
	"reflect"
	"testing"
)

func TestIsOwnerTag(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{name: "cfgate:0123456789abcdef0123456789ab", want: true},
		{name: "cfgate", want: false},
		{name: "cfgate:0123456789abcdef0123456789a", want: false},
		{name: "cfgate:0123456789abcdef0123456789abc", want: false},
		{name: "cfgate:0123456789abcdef0123456789az", want: false},
		{name: "cfgate:0123456789ABCDEF0123456789AB", want: false},
		{name: "e2e-cfgate:0123456789abcdef0123456789ab", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsOwnerTag(tt.name); got != tt.want {
				t.Errorf("IsOwnerTag(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestApplicationTagNames(t *testing.T) {
	tests := []struct {
		name string
		tags interface{}
		want []string
	}{
		{name: "nil", tags: nil, want: nil},
		{name: "strings", tags: []string{"cfgate", "owner"}, want: []string{"cfgate", "owner"}},
		{name: "interfaces", tags: []interface{}{"cfgate", 123, "owner"}, want: []string{"cfgate", "owner"}},
		{name: "unsupported", tags: "cfgate", want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ApplicationTagNames(tt.tags); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ApplicationTagNames(%#v) = %#v, want %#v", tt.tags, got, tt.want)
			}
		})
	}
}
