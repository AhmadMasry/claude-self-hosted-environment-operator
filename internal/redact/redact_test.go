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

package redact

import "testing"

func TestString(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"three-segment JWT", "token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2ln end", "token " + Marker + " end"},
		{"two-segment JWT", "token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0 end", "token " + Marker + " end"},
		{"bare header", "token eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9 end", "token " + Marker + " end"},
		{"not a JWT", "plain text with a.b.c and eyx.foo", "plain text with a.b.c and eyx.foo"},
		{"email", "mail bob@example.com now", "mail " + Marker + " now"},
		{"api key", "key sk-ant-abc_123 here", "key " + Marker + " here"},
		{"environment key", "key ccenvkey_abc123 here", "key " + Marker + " here"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := String(tc.in); got != tc.want {
				t.Fatalf("String(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
