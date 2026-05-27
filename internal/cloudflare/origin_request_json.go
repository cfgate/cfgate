package cloudflare

import "encoding/json"

// MarshalJSON keeps explicit false origin booleans visible for config hashing.
func (c OriginRequestConfig) MarshalJSON() ([]byte, error) {
	type alias OriginRequestConfig
	data, err := json.Marshal(alias(c))
	if err != nil {
		return nil, err
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	if c.NoTLSVerifySet {
		fields["noTLSVerify"] = c.NoTLSVerify
	}
	if c.HTTP2OriginSet {
		fields["http2Origin"] = c.HTTP2Origin
	}
	if c.H2cOriginSet {
		fields["h2cOrigin"] = c.H2cOrigin
	}
	return json.Marshal(fields)
}
