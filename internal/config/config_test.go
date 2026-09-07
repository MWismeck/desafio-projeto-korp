package config_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MWismeck/desafio-projeto-korp/internal/config"
)

// envVars are every variable read by the package; tests clear them so host values never leak in.
var envVars = []string{"APP_PORT", "APP_ENV", "LOG_LEVEL", "SHUTDOWN_TIMEOUT", "READ_HEADER_TIMEOUT"}

// clearEnv unsets the variables for the test; t.Setenv registers the restore of the original value.
// Tests using it must not call t.Parallel (t.Setenv forbids it).
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range envVars {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatalf("unset %s: %v", k, err)
		}
	}
}

func TestLoad_Default_MatchesDocumentedValues(t *testing.T) {
	clearEnv(t)

	cfg, err := config.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() err = %v, want nil", err)
	}

	want := config.Config{
		Port:              8080,
		Env:               "local",
		LogLevel:          slog.LevelInfo,
		ShutdownTimeout:   10 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
	}
	if cfg != want {
		t.Fatalf("defaults = %+v, want %+v", cfg, want)
	}
	if cfg.Addr() != ":8080" {
		t.Fatalf("Addr() = %q, want :8080", cfg.Addr())
	}
}

func TestLoad_Invalid_NamesTheVariable(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantVar string
	}{
		{name: "invalid APP_PORT zero", env: map[string]string{"APP_PORT": "0"}, wantVar: "APP_PORT"},
		{name: "invalid APP_PORT above range", env: map[string]string{"APP_PORT": "70000"}, wantVar: "APP_PORT"},
		{name: "invalid APP_PORT not a number", env: map[string]string{"APP_PORT": "abc"}, wantVar: "APP_PORT"},
		{name: "invalid APP_ENV blank", env: map[string]string{"APP_ENV": "  "}, wantVar: "APP_ENV"},
		{name: "invalid LOG_LEVEL unknown", env: map[string]string{"LOG_LEVEL": "loud"}, wantVar: "LOG_LEVEL"},
		{name: "invalid SHUTDOWN_TIMEOUT negative", env: map[string]string{"SHUTDOWN_TIMEOUT": "-1s"}, wantVar: "SHUTDOWN_TIMEOUT"},
		{name: "invalid SHUTDOWN_TIMEOUT too long", env: map[string]string{"SHUTDOWN_TIMEOUT": "2m"}, wantVar: "SHUTDOWN_TIMEOUT"},
		{name: "invalid SHUTDOWN_TIMEOUT not a duration", env: map[string]string{"SHUTDOWN_TIMEOUT": "10"}, wantVar: "SHUTDOWN_TIMEOUT"},
		{name: "invalid READ_HEADER_TIMEOUT zero", env: map[string]string{"READ_HEADER_TIMEOUT": "0s"}, wantVar: "READ_HEADER_TIMEOUT"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}

			_, err := config.Load(context.Background())
			if err == nil {
				t.Fatalf("Load() err = nil, want error naming %s", tc.wantVar)
			}
			var verr *config.ValidationError
			if !errors.As(err, &verr) {
				t.Fatalf("err %v is not *ValidationError", err)
			}
			if !strings.Contains(err.Error(), tc.wantVar+":") {
				t.Fatalf("err = %q, want mention of %s", err, tc.wantVar)
			}
		})
	}
}

func TestLoad_Invalid_ReportsAllErrorsAtOnce(t *testing.T) {
	clearEnv(t)
	t.Setenv("APP_PORT", "0")
	t.Setenv("SHUTDOWN_TIMEOUT", "0s")

	_, err := config.Load(context.Background())
	if err == nil || !strings.Contains(err.Error(), "APP_PORT:") || !strings.Contains(err.Error(), "SHUTDOWN_TIMEOUT:") {
		t.Fatalf("Load() err = %v, want both variables reported", err)
	}
}

func TestLoad_ValidOverrides(t *testing.T) {
	clearEnv(t)
	t.Setenv("APP_PORT", "18080")
	t.Setenv("APP_ENV", "prod")
	t.Setenv("LOG_LEVEL", "DEBUG")
	t.Setenv("READ_HEADER_TIMEOUT", "2s")

	cfg, err := config.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() err = %v, want nil", err)
	}
	if cfg.Addr() != ":18080" || cfg.Env != "prod" || cfg.LogLevel != slog.LevelDebug || cfg.ReadHeaderTimeout != 2*time.Second {
		t.Fatalf("overrides not applied: %+v", cfg)
	}
}

func TestValidationError_UnwrapsParseError(t *testing.T) {
	clearEnv(t)
	t.Setenv("APP_PORT", "abc")

	_, err := config.Load(context.Background())

	var verr *config.ValidationError
	if !errors.As(err, &verr) || verr.Err == nil {
		t.Fatalf("parse failure must wrap the original error: %v", err)
	}
}

// TestConfig_EnvExample_InSync guarantees every variable read by the code is documented in .env.example.
func TestConfig_EnvExample_InSync(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join("..", "..", ".env.example"))
	if err != nil {
		t.Fatalf("read .env.example: %v", err)
	}
	documented := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if name, _, ok := strings.Cut(line, "="); ok {
			documented[strings.TrimSpace(name)] = true
		}
	}
	rt := reflect.TypeFor[config.Config]()
	for i := range rt.NumField() {
		name, _, _ := strings.Cut(rt.Field(i).Tag.Get("env"), ",")
		if name = strings.TrimSpace(name); name != "" && !documented[name] {
			t.Errorf("%s is read by internal/config but missing from .env.example", name)
		}
	}
}
