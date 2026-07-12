package storage

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestConfigFromEnvDefault(t *testing.T) {
	t.Setenv("MAPING_CLICKHOUSE_DSN", "")
	cfg := ConfigFromEnv()
	assert.Equal(t, defaultDSN, cfg.DSN)
	assert.Equal(t, DefaultFlushInterval, cfg.FlushInterval)
	assert.Equal(t, DefaultFlushRows, cfg.FlushRows)
	assert.Equal(t, DefaultInsertTimeout, cfg.InsertTimeout)
}

func TestConfigFromEnvOverride(t *testing.T) {
	t.Setenv("MAPING_CLICKHOUSE_DSN", "clickhouse://u:p@ch:9000/db")
	cfg := ConfigFromEnv()
	assert.Equal(t, "clickhouse://u:p@ch:9000/db", cfg.DSN)
}

func TestSketchConstantsFrozen(t *testing.T) {
	// Guardrail against an accidental change to the frozen contract.
	assert.Equal(t, 1.01, SketchGamma)
	assert.Equal(t, 2.01, SketchGammaPlusOne)
	assert.Equal(t, 2*time.Second, DefaultFlushInterval)
	assert.Equal(t, 100_000, DefaultFlushRows)
}
