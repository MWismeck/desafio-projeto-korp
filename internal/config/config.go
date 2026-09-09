// Package config loads and validates the process configuration from the environment; it is the
// only package of the module allowed to read environment variables.
package config

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/sethvargo/go-envconfig"
)

// Bounds applied by Validate; anything outside them is an operator mistake, not a tuning choice.
const (
	minPort    = 1
	maxPort    = 65535
	maxTimeout = time.Minute
)

// Config is the configuration contract of http-server-projeto-korp. Names and defaults mirror
// compose.yml and .env.example; only what varies between deployments lives here.
type Config struct {
	// Port is the listening port of the API. Never published on the host: only NGINX reaches it.
	Port int `env:"APP_PORT, default=8080"`
	// Env labels the deployment (local, dev, prod) in telemetry; it never branches code paths.
	Env string `env:"APP_ENV, default=local"`
	// LogLevel accepts debug|info|warn|error (slog.Level implements TextUnmarshaler).
	LogLevel slog.Level `env:"LOG_LEVEL, default=info"`
	// ShutdownTimeout caps the drain on SIGTERM; it must stay below the compose stop_grace_period (15s).
	ShutdownTimeout time.Duration `env:"SHUTDOWN_TIMEOUT, default=10s"`
	// ReadHeaderTimeout bounds how long a client may take to send the request headers (slowloris).
	ReadHeaderTimeout time.Duration `env:"READ_HEADER_TIMEOUT, default=5s"`
}

// ValidationError names the environment variable the operator has to fix.
type ValidationError struct {
	Var    string
	Reason string
	Err    error
}

func (e *ValidationError) Error() string { return e.Var + ": " + e.Reason }

// Unwrap exposes the underlying parse error, when there is one.
func (e *ValidationError) Unwrap() error { return e.Err }

// Load reads the environment and validates it; any problem prevents the boot (fail-fast).
func Load(ctx context.Context) (Config, error) {
	var cfg Config
	if err := envconfig.Process(ctx, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse environment: %w", withEnvVar(err))
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate: %w", err)
	}
	return cfg, nil
}

// Addr is the listen address derived from Port (":8080").
func (c Config) Addr() string {
	return net.JoinHostPort("", strconv.Itoa(c.Port))
}

// Validate applies the range rules and returns every violation at once, so the operator fixes
// everything in a single attempt.
func (c Config) Validate() error {
	var errs []error
	if c.Port < minPort || c.Port > maxPort {
		errs = append(errs, &ValidationError{Var: "APP_PORT", Reason: fmt.Sprintf("must be in [%d, %d], got %d", minPort, maxPort, c.Port)})
	}
	if strings.TrimSpace(c.Env) == "" {
		errs = append(errs, &ValidationError{Var: "APP_ENV", Reason: "must not be empty"})
	}
	if err := validateTimeout(c.ShutdownTimeout); err != nil {
		errs = append(errs, &ValidationError{Var: "SHUTDOWN_TIMEOUT", Reason: err.Error()})
	}
	if err := validateTimeout(c.ReadHeaderTimeout); err != nil {
		errs = append(errs, &ValidationError{Var: "READ_HEADER_TIMEOUT", Reason: err.Error()})
	}
	return errors.Join(errs...)
}

// validateTimeout accepts (0, maxTimeout]; zero would disable the protection and a huge value would
// defeat the compose grace period.
func validateTimeout(d time.Duration) error {
	if d <= 0 || d > maxTimeout {
		return fmt.Errorf("must be in (0s, %s], got %s", maxTimeout, d)
	}
	return nil
}

// withEnvVar translates a go-envconfig parse error, which names the struct field, into a
// ValidationError naming the environment variable the operator actually sees.
func withEnvVar(err error) error {
	field, reason, found := strings.Cut(err.Error(), ": ")
	if !found {
		return err
	}
	structField, ok := reflect.TypeFor[Config]().FieldByName(field)
	if !ok {
		return err
	}
	name, _, _ := strings.Cut(structField.Tag.Get("env"), ",")
	return &ValidationError{Var: strings.TrimSpace(name), Reason: reason, Err: err}
}
