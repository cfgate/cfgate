package cloudflare

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"time"
)

// UnmarshalJSON accepts Cloudflare's integer-second durations and connector
// duration strings while retaining fields unknown to the upstream SDK.
func (r *OriginRequestConfig) UnmarshalJSON(data []byte) error {
	type plainOrigin OriginRequestConfig
	var result OriginRequestConfig
	wire := struct {
		*plainOrigin
		ConnectTimeout   json.RawMessage `json:"connectTimeout"`
		TLSTimeout       json.RawMessage `json:"tlsTimeout"`
		TCPKeepAlive     json.RawMessage `json:"tcpKeepAlive"`
		KeepAliveTimeout json.RawMessage `json:"keepAliveTimeout"`
	}{plainOrigin: (*plainOrigin)(&result)}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	for _, field := range []struct {
		name   string
		raw    json.RawMessage
		target *string
	}{
		{"connectTimeout", wire.ConnectTimeout, &result.ConnectTimeout},
		{"tlsTimeout", wire.TLSTimeout, &result.TLSTimeout},
		{"tcpKeepAlive", wire.TCPKeepAlive, &result.TCPKeepAlive},
		{"keepAliveTimeout", wire.KeepAliveTimeout, &result.KeepAliveTimeout},
	} {
		value, err := decodeOriginDuration(field.raw)
		if err != nil {
			return fmt.Errorf("%s: %w", field.name, err)
		}
		*field.target = value
	}
	*r = result
	return nil
}

func decodeOriginDuration(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", fmt.Errorf("duration must not be null")
	}
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		if value == "" {
			return "", nil
		}
		duration, err := time.ParseDuration(value)
		if err != nil || duration < 0 {
			return "", fmt.Errorf("invalid nonnegative duration string")
		}
		return duration.String(), nil
	}
	var seconds int64
	if err := json.Unmarshal(raw, &seconds); err != nil || seconds < 0 || seconds > math.MaxInt64/int64(time.Second) {
		return "", fmt.Errorf("duration must be nonnegative whole seconds within range")
	}
	return (time.Duration(seconds) * time.Second).String(), nil
}

// CanonicalOriginConnectTimeout describes the existing API write conversion.
// Call this only for desired configuration, not observed remote values: an
// observed zero remains zero even though the writer defaults nonpositive input.
func CanonicalOriginConnectTimeout(value string) string {
	if value == "" {
		return ""
	}
	return (time.Duration(parseDurationSeconds(value, 30)) * time.Second).String()
}
