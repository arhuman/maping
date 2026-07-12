package maping

import (
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/arhuman/maping/proto/token"
)

// defaultEndpoint is the baked-in hosted collector URL. Placeholder for M1.
const defaultEndpoint = "https://ingest.maping.dev"

// defaultFlushWindow is the accumulation interval before a Summary is shipped.
const defaultFlushWindow = 10 * time.Second

// Option configures a Recorder. Options follow the functional-options pattern;
// a code option always beats the matching env var, which beats the default
// (CONFIG.md precedence).
type Option func(*Config)

// Config is the resolved recorder configuration.
type Config struct {
	Key         string
	Endpoint    string
	Service     string
	Instance    string
	FlushWindow time.Duration
}

// WithKey sets the ingest key (beats MAPING_KEY). The key encodes the tenant.
func WithKey(key string) Option { return func(c *Config) { c.Key = key } }

// WithEndpoint overrides the collector URL (beats MAPING_ENDPOINT).
func WithEndpoint(endpoint string) Option { return func(c *Config) { c.Endpoint = endpoint } }

// WithService overrides the logical service name (beats MAPING_SERVICE).
func WithService(service string) Option { return func(c *Config) { c.Service = service } }

// WithInstance overrides the instance id (beats MAPING_INSTANCE).
func WithInstance(instance string) Option { return func(c *Config) { c.Instance = instance } }

// WithFlushWindow overrides the flush window (beats MAPING_FLUSH_SECONDS).
func WithFlushWindow(d time.Duration) Option { return func(c *Config) { c.FlushWindow = d } }

// resolveConfig applies precedence: code option > env var > default. Code
// options are applied last (into a struct pre-seeded from env/defaults) so a
// non-zero option value wins; the zero value means "not set by code".
func resolveConfig(opts []Option) Config {
	c := Config{
		Key:         os.Getenv("MAPING_KEY"),
		Service:     deriveService(),
		Instance:    deriveInstance(),
		FlushWindow: deriveFlushWindow(),
	}

	// Code options override only when they set a non-zero value.
	var override Config
	for _, opt := range opts {
		opt(&override)
	}
	if override.Key != "" {
		c.Key = override.Key
	}
	if override.Service != "" {
		c.Service = override.Service
	}
	if override.Instance != "" {
		c.Instance = override.Instance
	}
	if override.FlushWindow > 0 {
		c.FlushWindow = override.FlushWindow
	}
	// Endpoint precedence: WithEndpoint > MAPING_ENDPOINT > key-embedded origin >
	// default. The key is resolved first (above) so its embedded origin is known.
	c.Endpoint = resolveEndpoint(override.Endpoint, os.Getenv("MAPING_ENDPOINT"), c.Key)
	return c
}

// resolveEndpoint applies the collector-URL precedence. The ingest key may embed
// the deployment origin (mk_live_<origin>.<secret>), which lets a single
// MAPING_KEY configure both credential and endpoint — but only when no explicit
// endpoint (option or env) is set, and only if the embedded origin is a valid
// http(s) URL. A malformed or non-http origin falls back to the default rather
// than pointing telemetry somewhere unexpected.
func resolveEndpoint(optEndpoint, envEndpoint, key string) string {
	if optEndpoint != "" {
		return optEndpoint
	}
	if envEndpoint != "" {
		return envEndpoint
	}
	if origin, _, ok := token.Decode(key); ok && validOrigin(origin) {
		return origin
	}
	return defaultEndpoint
}

// validOrigin accepts only absolute http/https origins, so a mangled or hostile
// key can never redirect telemetry to a non-http scheme.
func validOrigin(origin string) bool {
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// deriveService resolves the service name: MAPING_SERVICE → OTEL_SERVICE_NAME →
// binary name (CONFIG.md; the module path is not available at runtime).
func deriveService() string {
	return firstNonEmpty(
		os.Getenv("MAPING_SERVICE"),
		os.Getenv("OTEL_SERVICE_NAME"),
		filepath.Base(os.Args[0]),
	)
}

// deriveInstance resolves the instance id: MAPING_INSTANCE → HOSTNAME → OS
// hostname.
func deriveInstance() string {
	if v := firstNonEmpty(os.Getenv("MAPING_INSTANCE"), os.Getenv("HOSTNAME")); v != "" {
		return v
	}
	if h, err := os.Hostname(); err == nil {
		return h
	}
	return "unknown"
}

// deriveFlushWindow parses MAPING_FLUSH_SECONDS, falling back to the default on
// absence or an unparseable/non-positive value (fail-open, CONFIG.md).
func deriveFlushWindow() time.Duration {
	raw := os.Getenv("MAPING_FLUSH_SECONDS")
	if raw == "" {
		return defaultFlushWindow
	}
	secs, err := strconv.Atoi(raw)
	if err != nil || secs <= 0 {
		slog.Warn("maping: invalid MAPING_FLUSH_SECONDS, using default", "value", raw)
		return defaultFlushWindow
	}
	return time.Duration(secs) * time.Second
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
