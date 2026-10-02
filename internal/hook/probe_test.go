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

package hook

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProbeConnected(t *testing.T) {
	body := `{"status":"ok","connected":false}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
	defer srv.Close()
	if ok, err := ProbeConnected(context.Background(), srv.URL); err != nil || ok {
		t.Fatalf("connected=false must probe false: %v %v", ok, err)
	}
	body = `{"status":"ok","connected":true,"queue_counts":{"pending":0}}`
	if ok, err := ProbeConnected(context.Background(), srv.URL); err != nil || !ok {
		t.Fatalf("connected=true must probe true: %v %v", ok, err)
	}
	body = `not json`
	if _, err := ProbeConnected(context.Background(), srv.URL); err == nil {
		t.Fatal("unparseable body must error")
	}
	if _, err := ProbeConnected(context.Background(), "http://127.0.0.1:1/healthz"); err == nil {
		t.Fatal("unreachable must error")
	}
}
