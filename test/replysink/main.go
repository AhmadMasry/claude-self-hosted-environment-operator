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

// Command replysink is a tiny HTTP service for the real-environment test.
// Runner Stop hooks POST their last assistant message to /<session_id>; the
// test GETs the same path. It is in-memory and single-replica by design.
package main

import (
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type sink struct {
	mu      sync.Mutex
	replies map[string][]byte
}

func (s *sink) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id := strings.Trim(r.URL.Path, "/")
	if id == "" || strings.Contains(id, "/") {
		http.Error(w, "path must be /<session_id>", http.StatusBadRequest)
		return
	}
	// Keys are session ids, not secrets: the log shows which requests arrived.
	log.Printf("%s /%s", r.Method, id)
	switch r.Method {
	case http.MethodPost:
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.replies[id] = body
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case http.MethodGet:
		s.mu.Lock()
		body, ok := s.replies[id]
		s.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func main() {
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	srv := &http.Server{Addr: addr, Handler: &sink{replies: map[string][]byte{}}, ReadHeaderTimeout: 5 * time.Second}
	log.Printf("replysink listening on %s", addr)
	log.Fatal(srv.ListenAndServe())
}
