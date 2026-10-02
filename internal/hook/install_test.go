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
	"os"
	"path/filepath"
	"testing"
)

func TestInstallCopiesExecutable(t *testing.T) {
	dir := t.TempDir()
	if err := Install(dir); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(dir, "spawn-runner"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o555 {
		t.Fatalf("mode %o, want 0555", st.Mode().Perm())
	}
	self, _ := os.Executable()
	want, _ := os.ReadFile(self)
	got, _ := os.ReadFile(filepath.Join(dir, "spawn-runner"))
	if len(got) == 0 || len(got) != len(want) {
		t.Fatalf("copied %d bytes, want %d", len(got), len(want))
	}
	// Re-running must replace atomically, not fail.
	if err := Install(dir); err != nil {
		t.Fatal(err)
	}
}
