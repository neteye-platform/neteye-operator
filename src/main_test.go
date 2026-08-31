// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package main

import (
	"testing"

	"go.uber.org/zap/zapcore"
)

func TestParseVerbosity(t *testing.T) {
	tests := []struct {
		raw     string
		want    int8
		wantOK  bool
		wantErr bool
	}{
		{raw: "info", wantOK: false},
		{raw: "v0", want: 0, wantOK: true},
		{raw: "v3", want: 3, wantOK: true},
		{raw: "v", wantOK: true, wantErr: true},
		{raw: "vx", wantOK: true, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got, ok, err := parseVerbosity(tt.raw)
			if ok != tt.wantOK {
				t.Errorf("ok = %v, want %v", ok, tt.wantOK)
			}
			if (err != nil) != tt.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("verbosity = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestLogLevelFromEnv(t *testing.T) {
	tests := []struct {
		env            string
		wantLevel      zapcore.Level
		wantConfigured bool
		wantErr        bool
	}{
		{env: "", wantLevel: zapcore.InfoLevel, wantConfigured: false},
		{env: "debug", wantLevel: zapcore.DebugLevel, wantConfigured: true},
		{env: "info", wantLevel: zapcore.InfoLevel, wantConfigured: true},
		{env: "warn", wantLevel: zapcore.WarnLevel, wantConfigured: true},
		{env: "error", wantLevel: zapcore.ErrorLevel, wantConfigured: true},
		{env: "v2", wantLevel: zapcore.Level(-2), wantConfigured: true},
		{env: "bogus", wantLevel: zapcore.InfoLevel, wantConfigured: false, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.env, func(t *testing.T) {
			t.Setenv(logLevelEnvVar, tt.env)
			level, _, configured, err := logLevelFromEnv()
			if configured != tt.wantConfigured {
				t.Errorf("configured = %v, want %v", configured, tt.wantConfigured)
			}
			if (err != nil) != tt.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if level != tt.wantLevel {
				t.Errorf("level = %v, want %v", level, tt.wantLevel)
			}
		})
	}
}
