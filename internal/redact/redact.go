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

// Package redact removes credential-shaped substrings from text that may
// reach logs, events or status conditions.
package redact

import "regexp"

// Marker replaces every redacted substring.
const Marker = "[redacted]"

// pattern matches email addresses, JWTs (three-segment, two-segment and bare
// header forms), Anthropic API keys and environment keys.
var pattern = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}` +
	`|eyJ[A-Za-z0-9_-]+(\.[A-Za-z0-9_-]+){1,2}` +
	`|sk-ant-[A-Za-z0-9_-]+` +
	`|ccenvkey_[A-Za-z0-9_-]+` +
	`|eyJ[A-Za-z0-9_-]{20,}`)

// String replaces credential-shaped substrings of s with Marker.
func String(s string) string {
	return pattern.ReplaceAllString(s, Marker)
}
