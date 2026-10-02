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

package v1alpha1

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

func TestSamplesDecodeStrictly(t *testing.T) {
	files, err := filepath.Glob("../../examples/*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	more, _ := filepath.Glob("../../config/samples/*claudeenvironment*.yaml")
	files = append(files, more...)
	if len(files) == 0 {
		t.Fatal("no sample files found")
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for doc := range strings.SplitSeq(string(data), "\n---") {
			if !strings.Contains(doc, "kind: ClaudeEnvironment") {
				continue
			}
			var env ClaudeEnvironment
			if err := yaml.UnmarshalStrict([]byte(doc), &env); err != nil {
				t.Errorf("%s: %v", f, err)
			}
			if env.Spec.Runner.Image == "" || strings.HasSuffix(env.Spec.Runner.Image, ":latest") {
				t.Errorf("%s: sample image must be pinned", f)
			}
		}
	}
}
