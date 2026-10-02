package cloudflare

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ErrConfigurationBudget identifies a local configuration or dependency limit.
var ErrConfigurationBudget = errors.New("configuration budget exceeded")

// ClientSettings bounds API attempts and aggregate tunnel configuration work.
// Settings are copied into clients and must not be changed after construction.
type ClientSettings struct {
	// AttemptTimeout is the deadline for each SDK attempt, including response body reads.
	AttemptTimeout time.Duration
	// MaxIngressRules limits aggregate ingress expansion for one tunnel.
	MaxIngressRules int
	// MaxConfigurationBytes limits the encoded ingress and origin settings.
	MaxConfigurationBytes int
}

// DefaultClientSettings returns the operator's default API and work limits.
func DefaultClientSettings() ClientSettings {
	return ClientSettings{AttemptTimeout: apiAttemptTimeout, MaxIngressRules: 1000, MaxConfigurationBytes: 1 << 20}
}

// NormalizeClientSettings fills unspecified fields and rejects invalid limits.
func NormalizeClientSettings(settings ClientSettings) (ClientSettings, error) {
	defaults := DefaultClientSettings()
	if settings.AttemptTimeout == 0 {
		settings.AttemptTimeout = defaults.AttemptTimeout
	}
	if settings.MaxIngressRules == 0 {
		settings.MaxIngressRules = defaults.MaxIngressRules
	}
	if settings.MaxConfigurationBytes == 0 {
		settings.MaxConfigurationBytes = defaults.MaxConfigurationBytes
	}
	return settings, ValidateClientSettings(settings)
}

// ValidateClientSettings requires positive attempt, rule, and byte limits.
func ValidateClientSettings(settings ClientSettings) error {
	if settings.AttemptTimeout <= 0 || settings.MaxIngressRules <= 0 || settings.MaxConfigurationBytes <= 0 {
		return errors.New("API attempt timeout, ingress rule limit, and configuration byte limit must be positive")
	}
	return nil
}

// WithClientSettings supplies immutable API attempt and configuration limits.
func WithClientSettings(settings ClientSettings) ClientOption {
	return func(opts *clientOptions) { opts.settings = settings }
}

// CheckRuleCount rejects aggregate ingress expansion beyond the configured limit.
func (settings ClientSettings) CheckRuleCount(count int) error {
	normalized, err := NormalizeClientSettings(settings)
	if err != nil {
		return err
	}
	if count > normalized.MaxIngressRules {
		return fmt.Errorf("%w: tunnel configuration exceeds %d ingress rules", ErrConfigurationBudget, normalized.MaxIngressRules)
	}
	return nil
}

// ValidateTunnelConfiguration rejects oversized or inconsistent configurations before publication.
// Limits describe operator guardrails, not Cloudflare service limits.
func ValidateTunnelConfiguration(config TunnelConfiguration, settings ClientSettings) error {
	settings, err := NormalizeClientSettings(settings)
	if err != nil {
		return err
	}
	if err := settings.CheckRuleCount(len(config.Ingress)); err != nil {
		return err
	}

	// Marshal one rule at a time so oversized aggregates never allocate a full payload.
	total := len(`{"ingress":[]}`)
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	var wireRule struct {
		Hostname      string               `json:"hostname"`
		Path          string               `json:"path"`
		Service       string               `json:"service"`
		OriginRequest *OriginRequestConfig `json:"originRequest,omitempty"`
	}
	for i := range config.Ingress {
		rule := &config.Ingress[i]
		if len(rule.Hostname)+len(rule.Path)+len(rule.Service) > settings.MaxConfigurationBytes {
			return fmt.Errorf("%w: tunnel configuration exceeds %d encoded bytes", ErrConfigurationBudget, settings.MaxConfigurationBytes)
		}
		buffer.Reset()
		wireRule.Hostname, wireRule.Path, wireRule.Service, wireRule.OriginRequest = rule.Hostname, rule.Path, rule.Service, rule.OriginRequest
		if err := encoder.Encode(&wireRule); err != nil {
			return fmt.Errorf("encode ingress rule: %w", err)
		}
		total += buffer.Len() - 1 // Encoder terminates each JSON value with a newline.
		if i > 0 {
			total++
		}
		if total > settings.MaxConfigurationBytes {
			return fmt.Errorf("%w: tunnel configuration exceeds %d encoded bytes", ErrConfigurationBudget, settings.MaxConfigurationBytes)
		}
	}
	if config.OriginRequest != nil {
		encoded, err := json.Marshal(config.OriginRequest)
		if err != nil {
			return fmt.Errorf("encode origin defaults: %w", err)
		}
		total += len(`,"originRequest":`) + len(encoded)
	}
	if config.WarpRouting != nil {
		encoded, err := json.Marshal(config.WarpRouting)
		if err != nil {
			return fmt.Errorf("encode WARP routing: %w", err)
		}
		total += len(`,"warp-routing":`) + len(encoded)
	}
	if total > settings.MaxConfigurationBytes {
		return fmt.Errorf("%w: tunnel configuration exceeds %d encoded bytes", ErrConfigurationBudget, settings.MaxConfigurationBytes)
	}
	return validateOriginRequests(config)
}
