// Package cloudflared provides utilities for managing cloudflared Kubernetes resources.
package cloudflared

import (
	"errors"
	"fmt"

	"go.yaml.in/yaml/v3"

	cfgatev1alpha1 "cfgate.io/cfgate/api/v1alpha1"
	"cfgate.io/cfgate/internal/controller/annotations"

	"k8s.io/utils/ptr"
)

// TunnelConfig represents the cloudflared configuration file structure.
// This is used when running cloudflared with a config file instead of remote config.
type TunnelConfig struct {
	// TunnelID is the tunnel UUID.
	TunnelID string `yaml:"tunnel"`

	// CredentialsFile is the path to the credentials file.
	CredentialsFile string `yaml:"credentials-file,omitempty"`

	// Ingress is the list of ingress rules.
	Ingress []IngressRule `yaml:"ingress"`

	// OriginRequest contains default origin settings.
	OriginRequest *OriginRequestConfig `yaml:"originRequest,omitempty"`

	// WarpRouting enables WARP routing.
	WarpRouting *WarpRoutingConfig `yaml:"warp-routing,omitempty"`

	// Protocol is the tunnel transport protocol.
	Protocol string `yaml:"protocol,omitempty"`

	// LogLevel is the log level.
	LogLevel string `yaml:"loglevel,omitempty"`

	// NoAutoUpdate disables auto-updates.
	NoAutoUpdate bool `yaml:"no-autoupdate,omitempty"`

	// Metrics is the metrics endpoint address.
	Metrics string `yaml:"metrics,omitempty"`
}

// IngressRule represents a single ingress rule in the config.
type IngressRule struct {
	// Hostname is the hostname to match.
	Hostname string `yaml:"hostname,omitempty"`

	// Path is the path regex to match.
	Path string `yaml:"path,omitempty"`

	// Service is the origin service URL.
	Service string `yaml:"service"`

	// OriginRequest contains per-rule origin settings.
	OriginRequest *OriginRequestConfig `yaml:"originRequest,omitempty"`
}

// OriginRequestConfig contains origin connection settings.
type OriginRequestConfig struct {
	ConnectTimeout         string `yaml:"connectTimeout,omitempty"`
	TLSTimeout             string `yaml:"tlsTimeout,omitempty"`
	TCPKeepAlive           string `yaml:"tcpKeepAlive,omitempty"`
	NoHappyEyeballs        bool   `yaml:"noHappyEyeballs,omitempty"`
	KeepAliveConnections   int    `yaml:"keepAliveConnections,omitempty"`
	KeepAliveTimeout       string `yaml:"keepAliveTimeout,omitempty"`
	HTTPHostHeader         string `yaml:"httpHostHeader,omitempty"`
	OriginServerName       string `yaml:"originServerName,omitempty"`
	CAPool                 string `yaml:"caPool,omitempty"`
	NoTLSVerify            *bool  `yaml:"noTLSVerify,omitempty"`
	DisableChunkedEncoding bool   `yaml:"disableChunkedEncoding,omitempty"`
	BastionMode            bool   `yaml:"bastionMode,omitempty"`
	ProxyAddress           string `yaml:"proxyAddress,omitempty"`
	ProxyPort              int    `yaml:"proxyPort,omitempty"`
	ProxyType              string `yaml:"proxyType,omitempty"`
	HTTP2Origin            *bool  `yaml:"http2Origin,omitempty"`
	H2cOrigin              *bool  `yaml:"h2cOrigin,omitempty"`
}

// WarpRoutingConfig contains WARP routing settings.
type WarpRoutingConfig struct {
	Enabled bool `yaml:"enabled"`
}

// NewTunnelConfig creates a new TunnelConfig with defaults from a CloudflareTunnel.
func NewTunnelConfig(tunnel *cfgatev1alpha1.CloudflareTunnel, tunnelID string) *TunnelConfig {
	config := &TunnelConfig{
		TunnelID:     tunnelID,
		NoAutoUpdate: true,
		Ingress:      []IngressRule{},
	}
	if metricsEnabled(tunnel) {
		config.Metrics = fmt.Sprintf("0.0.0.0:%d", getMetricsPort(tunnel))
	}

	// Set protocol if specified
	if tunnel.Spec.Cloudflared.Protocol != "" && tunnel.Spec.Cloudflared.Protocol != "auto" {
		config.Protocol = tunnel.Spec.Cloudflared.Protocol
	}

	// Set default origin settings
	config.OriginRequest = BuildOriginConfig(&tunnel.Spec.OriginDefaults, nil)
	if tunnel.Spec.OriginDefaults.CAPoolSecretRef != nil {
		if config.OriginRequest == nil {
			config.OriginRequest = &OriginRequestConfig{}
		}
		config.OriginRequest.CAPool = OriginCAPoolPath()
	}

	// Add catch-all rule (required for valid config)
	fallback := tunnel.Spec.FallbackTarget
	if fallback == "" {
		fallback = "http_status:404"
	}
	config.Ingress = append(config.Ingress, IngressRule{
		Service: fallback,
	})

	return config
}

// AddRule adds an ingress rule to the configuration.
// Rules are inserted before the catch-all rule.
func (c *TunnelConfig) AddRule(rule IngressRule) {
	if len(c.Ingress) == 0 {
		c.Ingress = append(c.Ingress, rule)
		return
	}

	// Insert before last rule (catch-all)
	lastIdx := len(c.Ingress) - 1
	catchAll := c.Ingress[lastIdx]

	// Check if last rule is catch-all
	if catchAll.Hostname == "" && catchAll.Path == "" {
		c.Ingress = append(c.Ingress[:lastIdx], rule, catchAll)
	} else {
		c.Ingress = append(c.Ingress, rule)
	}
}

// SetCatchAll sets the catch-all rule (must be last).
func (c *TunnelConfig) SetCatchAll(service string) {
	// Remove existing catch-all if present
	if len(c.Ingress) > 0 {
		lastIdx := len(c.Ingress) - 1
		lastRule := c.Ingress[lastIdx]
		if lastRule.Hostname == "" && lastRule.Path == "" {
			c.Ingress = c.Ingress[:lastIdx]
		}
	}

	// Add new catch-all
	c.Ingress = append(c.Ingress, IngressRule{
		Service: service,
	})
}

// Validate validates the configuration.
// Returns an error if the configuration is invalid.
func (c *TunnelConfig) Validate() error {
	if c.TunnelID == "" {
		return errors.New("tunnel ID is required")
	}

	if c.OriginRequest != nil && ptr.Deref(c.OriginRequest.HTTP2Origin, false) && ptr.Deref(c.OriginRequest.H2cOrigin, false) {
		return errors.New("http2Origin and h2cOrigin are mutually exclusive")
	}

	if len(c.Ingress) == 0 {
		return errors.New("at least one ingress rule is required")
	}

	// Last rule must be catch-all (no hostname or path)
	lastRule := c.Ingress[len(c.Ingress)-1]
	if lastRule.Hostname != "" || lastRule.Path != "" {
		return errors.New("last ingress rule must be a catch-all (no hostname or path)")
	}

	// Validate each rule
	for i, rule := range c.Ingress {
		if rule.Service == "" {
			return fmt.Errorf("ingress rule %d: service is required", i)
		}
	}

	return nil
}

// Marshal serializes the configuration to YAML.
func (c *TunnelConfig) Marshal() ([]byte, error) {
	return yaml.Marshal(c)
}

// ParseConfig parses a YAML configuration file.
func ParseConfig(data []byte) (*TunnelConfig, error) {
	var config TunnelConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	return &config, nil
}

// BuildOriginConfig builds an OriginRequestConfig from tunnel defaults and annotations.
func BuildOriginConfig(defaults *cfgatev1alpha1.OriginDefaults, values map[string]string) *OriginRequestConfig {
	config := &OriginRequestConfig{}

	// Apply defaults
	if defaults != nil {
		config.ConnectTimeout = defaults.ConnectTimeout
		if defaults.NoTLSVerify {
			config.NoTLSVerify = ptr.To(true)
		}
		if defaults.HTTP2Origin {
			config.HTTP2Origin = ptr.To(true)
		}
		if defaults.H2cOrigin {
			config.H2cOrigin = ptr.To(true)
		}
	}

	// Apply annotation overrides
	if values != nil {
		if v, ok := values["cfgate.io/origin-connect-timeout"]; ok {
			config.ConnectTimeout = v
		}
		if v, ok := values["cfgate.io/origin-ssl-verify"]; ok {
			if parsed, err := annotations.ParseBoolean(v); err == nil {
				config.NoTLSVerify = ptr.To(!parsed)
			}
		}
		if v, ok := values["cfgate.io/origin-http-host-header"]; ok {
			config.HTTPHostHeader = v
		}
		if v, ok := values["cfgate.io/origin-server-name"]; ok {
			config.OriginServerName = v
		}
		if v, ok := values["cfgate.io/origin-ca-pool"]; ok {
			config.CAPool = v
		}
		if v, ok := values["cfgate.io/origin-http2"]; ok {
			if parsed, err := annotations.ParseBoolean(v); err == nil {
				config.HTTP2Origin = ptr.To(parsed)
			}
		}
		if v, ok := values["cfgate.io/origin-h2c"]; ok {
			if parsed, err := annotations.ParseBoolean(v); err == nil {
				config.H2cOrigin = ptr.To(parsed)
			}
		}
	}

	// Return nil if no settings configured
	if config.ConnectTimeout == "" &&
		config.NoTLSVerify == nil &&
		config.HTTPHostHeader == "" &&
		config.OriginServerName == "" &&
		config.CAPool == "" &&
		config.HTTP2Origin == nil &&
		config.H2cOrigin == nil {
		return nil
	}

	return config
}
