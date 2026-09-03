package config

import "fmt"

const (
	maxClaudeLocalInFlight      = 10_000
	maxClaudeLocalQueueCapacity = 100_000
	maxClaudeLocalQueueSeconds  = 3_600
)

// Validate checks local non-Home Claude credential admission settings.
func (c ClaudeCodeConfig) Validate() error {
	if c.MaxInFlight < 0 || c.MaxInFlight > maxClaudeLocalInFlight {
		return fmt.Errorf("claude-code.max-in-flight must be between 0 and %d", maxClaudeLocalInFlight)
	}
	if c.QueueCapacity < 0 || c.QueueCapacity > maxClaudeLocalQueueCapacity {
		return fmt.Errorf("claude-code.queue-capacity must be between 0 and %d", maxClaudeLocalQueueCapacity)
	}
	if c.QueueTimeoutSeconds < 0 || c.QueueTimeoutSeconds > maxClaudeLocalQueueSeconds {
		return fmt.Errorf("claude-code.queue-timeout-seconds must be between 0 and %d", maxClaudeLocalQueueSeconds)
	}
	if c.QueueCapacity > 0 && c.MaxInFlight == 0 {
		return fmt.Errorf("claude-code.queue-capacity requires a positive max-in-flight")
	}
	return nil
}

// ValidateClaudeAdmission checks provider defaults and effective API-key overrides.
func (cfg *Config) ValidateClaudeAdmission() error {
	if cfg == nil {
		return nil
	}
	if errValidate := cfg.ClaudeCode.Validate(); errValidate != nil {
		return errValidate
	}
	for index := range cfg.ClaudeKey {
		entry := cfg.ClaudeKey[index]
		effective := cfg.ClaudeCode
		if entry.MaxInFlight != nil {
			effective.MaxInFlight = *entry.MaxInFlight
			if *entry.MaxInFlight == 0 && entry.QueueCapacity == nil {
				// An explicit credential-level disable must not inherit a provider
				// queue that can never be serviced.
				effective.QueueCapacity = 0
			}
		}
		if entry.QueueCapacity != nil {
			effective.QueueCapacity = *entry.QueueCapacity
		}
		if entry.QueueTimeoutSeconds != nil {
			effective.QueueTimeoutSeconds = *entry.QueueTimeoutSeconds
		}
		if errValidate := effective.Validate(); errValidate != nil {
			return fmt.Errorf("claude-api-key[%d]: %w", index, errValidate)
		}
	}
	return nil
}
