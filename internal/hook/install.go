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
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Install copies the running binary into dir as spawn-runner, mode 0555, so
// an init container can populate the orchestrator's hooks directory from a
// distroless image that has no shell or cp.
func Install(dir string) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate own executable: %w", err)
	}
	src, err := os.Open(self)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	tmp, err := os.CreateTemp(dir, ".spawn-runner-*")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	if err := writeExecutable(tmp, src); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, "spawn-runner")); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}

// writeExecutable copies src into tmp, marks it 0555 and closes it.
func writeExecutable(tmp *os.File, src io.Reader) error {
	if _, err := io.Copy(tmp, src); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o555); err != nil {
		_ = tmp.Close()
		return err
	}
	return tmp.Close()
}
