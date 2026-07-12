package storage

import (
	"testing"
	"time"
)

func TestSelectTier(t *testing.T) {
	base := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		width     time.Duration
		wantTable string
		wantStep  time.Duration
	}{
		{"one hour uses raw", time.Hour, tableRaw, 10 * time.Second},
		{"exactly 2h uses raw", 2 * time.Hour, tableRaw, 10 * time.Second},
		{"just over 2h uses 1m", 2*time.Hour + time.Minute, table1m, time.Minute},
		{"one day uses 1m", 24 * time.Hour, table1m, time.Minute},
		{"exactly 2d uses 1m", 48 * time.Hour, table1m, time.Minute},
		{"one week uses 1h", 7 * 24 * time.Hour, table1h, time.Hour},
		{"exactly 60d uses 1h", 60 * 24 * time.Hour, table1h, time.Hour},
		{"one year uses 1d", 365 * 24 * time.Hour, table1d, 24 * time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := selectTier(base, base.Add(tt.width))
			if got.table != tt.wantTable {
				t.Errorf("table = %q, want %q", got.table, tt.wantTable)
			}
			if got.minStep != tt.wantStep {
				t.Errorf("minStep = %s, want %s", got.minStep, tt.wantStep)
			}
			if _, ok := tierTables[got.table]; !ok {
				t.Errorf("selected table %q not in the allowlist", got.table)
			}
		})
	}
}
