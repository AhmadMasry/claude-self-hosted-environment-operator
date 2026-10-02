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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSinkRoundTrip(t *testing.T) {
	s := &sink{replies: map[string][]byte{}}
	do := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}

	if rec := do(http.MethodGet, "/s1", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("GET unknown: got %d, want 404", rec.Code)
	}
	if rec := do(http.MethodPost, "/s1", `{"reply":"hi"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("POST /s1: got %d, want 204", rec.Code)
	}
	rec := do(http.MethodGet, "/s1", "")
	if rec.Code != http.StatusOK || rec.Body.String() != `{"reply":"hi"}` {
		t.Fatalf("GET /s1: got %d %q, want 200 with the posted body", rec.Code, rec.Body.String())
	}
	if rec := do(http.MethodPost, "/", "x"); rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /: got %d, want 400", rec.Code)
	}
}
