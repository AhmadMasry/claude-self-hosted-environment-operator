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
	"fmt"
	"strings"

	"go.uber.org/zap/zapcore"
	"sigs.k8s.io/controller-runtime/pkg/cache"
)

// version is set at build time with -ldflags "-X main.version=<semver>".
var version = "dev"

// parseWatchNamespaces turns a comma-separated list into cache namespace
// config. An empty list returns nil, which watches all namespaces.
func parseWatchNamespaces(s string) map[string]cache.Config {
	var out map[string]cache.Config
	for part := range strings.SplitSeq(s, ",") {
		ns := strings.TrimSpace(part)
		if ns == "" {
			continue
		}
		if out == nil {
			out = map[string]cache.Config{}
		}
		out[ns] = cache.Config{}
	}
	return out
}

func parseLogLevel(s string) (zapcore.Level, error) {
	switch strings.ToLower(s) {
	case "debug":
		return zapcore.DebugLevel, nil
	case "info":
		return zapcore.InfoLevel, nil
	case "error":
		return zapcore.ErrorLevel, nil
	}
	return 0, fmt.Errorf("unknown log level %q (debug, info, error)", s)
}
