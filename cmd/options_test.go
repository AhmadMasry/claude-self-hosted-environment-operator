/*
Copyright 2026 Ahmad Masry.

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
	"flag"
	"testing"

	"go.uber.org/zap/zapcore"
)

func TestParseWatchNamespaces(t *testing.T) {
	if got := parseWatchNamespaces(""); got != nil {
		t.Fatalf("empty must mean all namespaces (nil), got %v", got)
	}
	got := parseWatchNamespaces(" a, b ,,c")
	if len(got) != 3 {
		t.Fatalf("want 3 namespaces, got %v", got)
	}
	for _, n := range []string{"a", "b", "c"} {
		if _, ok := got[n]; !ok {
			t.Fatalf("missing %s", n)
		}
	}
}

func TestParseLogLevel(t *testing.T) {
	levels := map[string]zapcore.Level{
		"debug": zapcore.DebugLevel, "info": zapcore.InfoLevel, "error": zapcore.ErrorLevel,
	}
	for in, want := range levels {
		got, err := parseLogLevel(in)
		if err != nil || got != want {
			t.Fatalf("%s: got %v %v", in, got, err)
		}
	}
	if _, err := parseLogLevel("loud"); err == nil {
		t.Fatal("invalid level must error")
	}
}

func TestHookImageDefault(t *testing.T) {
	t.Setenv("OPERATOR_IMAGE", "ghcr.io/x/op:1")
	if got := defaultHookImage(); got != "ghcr.io/x/op:1" {
		t.Fatalf("got %q", got)
	}
	t.Setenv("OPERATOR_IMAGE", "")
	if got := defaultHookImage(); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestRegisterFlags(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	o := registerFlags(fs)
	if fs.Lookup("log-level") == nil {
		t.Fatal("log-level flag must be registered")
	}
	if fs.Lookup("zap-log-level") != nil {
		t.Fatal("zap-log-level must not be registered")
	}
	if err := fs.Parse([]string{"--log-level=debug"}); err != nil || o.logLevel != "debug" {
		t.Fatalf("got %q %v", o.logLevel, err)
	}
}
