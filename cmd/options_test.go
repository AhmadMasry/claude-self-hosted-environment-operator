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
