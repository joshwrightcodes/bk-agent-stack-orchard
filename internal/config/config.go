// Package config reads the configuration of the controller: one file for the settings and the
// profiles, and the environment for every secret.
//
// The file never carries a secret. Strict decoding rejects a key that the controller does not
// know, so a secret that somebody adds to the file stops the controller instead of reaching a
// log or a virtual machine specification.
package config

import (
	"errors"
	"fmt"
	"maps"
	"net"
	"net/url"
	"os"
	"regexp"
	"time"

	"github.com/goccy/go-yaml"

	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/profile"
	"github.com/joshwrightcodes/bk-agent-stack-orchard/internal/secret"
)

// Environment reads a variable. os.Getenv satisfies it, and a test gives its own.
type Environment func(name string) string

// Names of the variables that carry a secret or a certificate. These constants hold the name of
// a variable, never its value.
const (
	//nolint:gosec // G101: this is the name of a variable, not a credential.
	EnvBuildkiteAgentToken = "BK_AGENT_STACK_ORCHARD_BUILDKITE_AGENT_TOKEN"
	//nolint:gosec // G101: this is the name of a variable, not a credential.
	EnvOrchardToken  = "BK_AGENT_STACK_ORCHARD_ORCHARD_TOKEN"
	EnvOrchardCACert = "BK_AGENT_STACK_ORCHARD_ORCHARD_CA_CERT"
)

// controllerIDFormat keeps the identifier short enough for a virtual machine name, which carries
// the identifier and a job UUID and has a maximum of 63 characters.
var controllerIDFormat = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,21}[a-z0-9])?$`)

// Buildkite holds the settings of the Stacks API client.
type Buildkite struct {
	Queue string `yaml:"queue"`
	// AgentToken comes from the environment, never from the file.
	AgentToken             secret.String `yaml:"-"`
	Endpoint               string        `yaml:"endpoint,omitempty"`
	PollInterval           time.Duration `yaml:"pollInterval,omitempty"`
	JobStatesInterval      time.Duration `yaml:"jobStatesInterval,omitempty"`
	ReservationExpiry      time.Duration `yaml:"reservationExpiry,omitempty"`
	ReservationRenewBefore time.Duration `yaml:"reservationRenewBefore,omitempty"`
	JobTokenLifetime       time.Duration `yaml:"jobTokenLifetime,omitempty"`
}

// Orchard holds the settings of the Orchard client.
type Orchard struct {
	URL               string        `yaml:"url"`
	ServiceAccount    string        `yaml:"serviceAccount"`
	InventoryInterval time.Duration `yaml:"inventoryInterval,omitempty"`

	// WorkerOfflineTimeout is how long a worker may be silent before this controller counts it
	// as offline. It MUST match the timeout of the Orchard controller: a value that is shorter
	// makes this controller hold back slots that Orchard still uses, and a value that is longer
	// makes it start jobs for a worker that Orchard has already given up on.
	WorkerOfflineTimeout time.Duration `yaml:"workerOfflineTimeout,omitempty"`

	// Token and CACert come from the environment, never from the file.
	Token  secret.String `yaml:"-"`
	CACert string        `yaml:"-"`
}

// Defaults holds the values that a profile takes when it states none.
type Defaults struct {
	Timeouts profile.Timeouts `yaml:"timeouts,omitempty"`
}

// Config is the whole configuration of one controller.
type Config struct {
	ControllerID   string            `yaml:"controllerID"`
	Buildkite      Buildkite         `yaml:"buildkite"`
	Orchard        Orchard           `yaml:"orchard"`
	MaxInFlight    uint64            `yaml:"maxInFlight,omitempty"`
	StateFreshness time.Duration     `yaml:"stateFreshness,omitempty"`
	HealthAddress  string            `yaml:"healthAddress,omitempty"`
	Defaults       Defaults          `yaml:"defaults,omitempty"`
	Profiles       []profile.Profile `yaml:"profiles"`

	// Warnings comes from the validation, not from the file.
	Warnings []string `yaml:"-"`
}

// Load reads the file, adds the defaults, reads the secrets, and validates everything. It
// reports every problem that it finds, not only the first.
func Load(path string, environment Environment) (*Config, error) {
	body, err := os.ReadFile(path) //nolint:gosec // the operator gives this path
	if err != nil {
		return nil, fmt.Errorf("reading the configuration: %w", err)
	}

	cfg := defaults()
	if err := yaml.UnmarshalWithOptions(body, cfg, yaml.Strict()); err != nil {
		return nil, fmt.Errorf("reading the configuration of %s: %w", path, err)
	}

	cfg.applyProfileDefaults()
	cfg.readSecrets(environment)

	if err := cfg.validate(environment); err != nil {
		return nil, err
	}

	for _, p := range cfg.Profiles {
		cfg.Warnings = append(cfg.Warnings, p.Warnings()...)
	}

	return cfg, nil
}

// defaults gives the values of the target design, section 8.
func defaults() *Config {
	return &Config{
		Buildkite: Buildkite{
			Endpoint:               "https://agent.buildkite.com/v3",
			PollInterval:           time.Second,
			JobStatesInterval:      5 * time.Second,
			ReservationExpiry:      300 * time.Second,
			ReservationRenewBefore: 120 * time.Second,
			JobTokenLifetime:       1800 * time.Second,
		},
		Orchard: Orchard{
			InventoryInterval:    time.Second,
			WorkerOfflineTimeout: 3 * time.Minute,
		},
		StateFreshness: 60 * time.Second,
		HealthAddress:  ":8080",
		Defaults: Defaults{
			Timeouts: profile.Timeouts{
				Unplaced:      2 * time.Minute,
				Boot:          15 * time.Minute,
				Acquire:       5 * time.Minute,
				MaxVMLifetime: 25 * time.Hour,
			},
		},
	}
}

// applyProfileDefaults gives every profile the transport that the design selects, unless the
// profile states another one.
func (c *Config) applyProfileDefaults() {
	for i := range c.Profiles {
		if c.Profiles[i].StartupTransport == "" {
			c.Profiles[i].StartupTransport = profile.TransportGuestAgent
		}
	}
}

// readSecrets puts each secret beside the settings that use it.
func (c *Config) readSecrets(environment Environment) {
	c.Buildkite.AgentToken = secret.String(environment(EnvBuildkiteAgentToken))
	c.Orchard.Token = secret.String(environment(EnvOrchardToken))
	c.Orchard.CACert = environment(EnvOrchardCACert)
}

func (c *Config) validate(environment Environment) error {
	var problems []error

	if !controllerIDFormat.MatchString(c.ControllerID) {
		problems = append(problems, fmt.Errorf(
			"controllerID %q must be lower-case letters, digits, and dashes, and 23 characters or fewer, "+
				"because it shares a virtual machine name with a job UUID", c.ControllerID))
	}

	problems = append(problems, c.validateBuildkite()...)
	problems = append(problems, c.validateOrchard()...)
	problems = append(problems, c.validateProfiles()...)
	problems = append(problems, c.validateFreshness()...)

	if _, _, err := net.SplitHostPort(c.HealthAddress); err != nil {
		problems = append(problems, fmt.Errorf("healthAddress %q is not a host and a port: %w",
			c.HealthAddress, err))
	}
	problems = append(problems, c.validateSecrets(environment)...)

	return errors.Join(problems...)
}

func (c *Config) validateBuildkite() []error {
	var problems []error

	if c.Buildkite.Queue == "" {
		problems = append(problems, errors.New("buildkite.queue must name the queue of the fleet"))
	}
	if _, err := url.ParseRequestURI(c.Buildkite.Endpoint); err != nil {
		problems = append(problems, fmt.Errorf("buildkite.endpoint %q is not a URL", c.Buildkite.Endpoint))
	}

	for name, value := range map[string]time.Duration{
		"buildkite.pollInterval":           c.Buildkite.PollInterval,
		"buildkite.jobStatesInterval":      c.Buildkite.JobStatesInterval,
		"buildkite.reservationExpiry":      c.Buildkite.ReservationExpiry,
		"buildkite.reservationRenewBefore": c.Buildkite.ReservationRenewBefore,
		"buildkite.jobTokenLifetime":       c.Buildkite.JobTokenLifetime,
		"stateFreshness":                   c.StateFreshness,
	} {
		if value <= 0 {
			problems = append(problems, fmt.Errorf("%s must be more than zero", name))
		}
	}

	if c.Buildkite.ReservationRenewBefore >= c.Buildkite.ReservationExpiry {
		problems = append(problems, fmt.Errorf(
			"buildkite.reservationRenewBefore (%s) must be shorter than buildkite.reservationExpiry (%s), "+
				"so that a renewal happens before the reservation lapses",
			c.Buildkite.ReservationRenewBefore, c.Buildkite.ReservationExpiry))
	}

	return problems
}

// validateFreshness keeps each source inside the freshness limit. Admission and the reaper act only
// on an answer newer than stateFreshness, so a source that is read less often than that stops them
// for good, and the controller would look healthy while it started and removed nothing.
func (c *Config) validateFreshness() []error {
	var problems []error

	for _, source := range []struct {
		name     string
		interval time.Duration
	}{
		{"buildkite.pollInterval", c.Buildkite.PollInterval},
		{"buildkite.jobStatesInterval", c.Buildkite.JobStatesInterval},
		{"orchard.inventoryInterval", c.Orchard.InventoryInterval},
	} {
		if source.interval >= c.StateFreshness {
			problems = append(problems, fmt.Errorf(
				"%s (%s) must be shorter than stateFreshness (%s), so that its answer is fresh "+
					"when admission and the reaper read it", source.name, source.interval,
				c.StateFreshness))
		}
	}

	return problems
}

func (c *Config) validateOrchard() []error {
	var problems []error

	if _, err := url.ParseRequestURI(c.Orchard.URL); err != nil {
		problems = append(problems, fmt.Errorf("orchard.url %q is not a URL", c.Orchard.URL))
	}
	if c.Orchard.ServiceAccount == "" {
		problems = append(problems, errors.New("orchard.serviceAccount must name the service account"))
	}
	if c.Orchard.InventoryInterval <= 0 {
		problems = append(problems, errors.New("orchard.inventoryInterval must be more than zero"))
	}
	if c.Orchard.WorkerOfflineTimeout <= 0 {
		problems = append(problems,
			errors.New("orchard.workerOfflineTimeout must be more than zero"))
	}

	return problems
}

func (c *Config) validateProfiles() []error {
	if len(c.Profiles) == 0 {
		return []error{errors.New("profiles must contain at least one profile")}
	}

	var problems []error

	names := make(map[string]bool, len(c.Profiles))
	for i, p := range c.Profiles {
		if err := p.Validate(); err != nil {
			problems = append(problems, err)
		}
		if names[p.Name] {
			problems = append(problems, fmt.Errorf("profile name %q appears more than once", p.Name))
		}
		names[p.Name] = true

		// A job selects exactly one profile, so two profiles must not match the same tags.
		for _, other := range c.Profiles[:i] {
			if maps.Equal(p.Match, other.Match) {
				problems = append(problems, fmt.Errorf(
					"profiles %q and %q have the same match map, so a job cannot select one of them",
					other.Name, p.Name))
			}
		}

		problems = append(problems, c.validateProfileTimeouts(p))
	}

	return problems
}

// validateProfileTimeouts keeps a healthy but slow virtual machine inside the life of its job
// acquisition token. A token that expires first fails a job that would have run.
func (c *Config) validateProfileTimeouts(p profile.Profile) error {
	sum := p.EffectiveTimeouts(c.Defaults.Timeouts).StartSum()
	if sum <= c.Buildkite.JobTokenLifetime {
		return nil
	}

	return fmt.Errorf(
		"profile %q: unplaced, boot, and acquire add to %s, which is longer than buildkite.jobTokenLifetime (%s)",
		p.Name, sum, c.Buildkite.JobTokenLifetime)
}

func (c *Config) validateSecrets(environment Environment) []error {
	var problems []error

	for name, value := range map[string]secret.String{
		EnvBuildkiteAgentToken: c.Buildkite.AgentToken,
		EnvOrchardToken:        c.Orchard.Token,
	} {
		if value == "" {
			problems = append(problems, fmt.Errorf("%s must hold a secret, because the file holds none", name))
		}
	}

	// A profile that uses SSH names two variables. They must hold a value before a virtual
	// machine starts with them, because Orchard uses admin/admin when a specification has none.
	for _, p := range c.Profiles {
		if p.StartupTransport != profile.TransportSSH || p.VMCredentials == nil {
			continue
		}

		for _, name := range []string{p.VMCredentials.UsernameEnv, p.VMCredentials.PasswordEnv} {
			if name != "" && environment(name) == "" {
				problems = append(problems, fmt.Errorf(
					"profile %q names the variable %s, which holds no value", p.Name, name))
			}
		}
	}

	return problems
}
