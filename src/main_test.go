/*
Copyright 2026 Wuerth IT | Italy.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

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
