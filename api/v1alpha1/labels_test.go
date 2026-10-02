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
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/util/validation"
)

func TestLabelValue(t *testing.T) {
	if got := LabelValue("order_01ABC"); got != "order_01ABC" {
		t.Fatalf("a valid label value must pass through, got %q", got)
	}
	for _, id := range []string{strings.Repeat("a", 70), "org/order-1"} {
		got := LabelValue(id)
		if errs := validation.IsValidLabelValue(got); len(errs) != 0 {
			t.Fatalf("LabelValue(%q) = %q is not a valid label value: %v", id, got, errs)
		}
		if !strings.HasPrefix(got, "sha256-") || len(got) != len("sha256-")+16 {
			t.Fatalf("LabelValue(%q) = %q, want sha256- and 16 hex digits", id, got)
		}
		if LabelValue(id) != got {
			t.Fatalf("LabelValue(%q) is not stable", id)
		}
	}
	if LabelValue(strings.Repeat("a", 70)) == LabelValue(strings.Repeat("a", 71)) {
		t.Fatal("different IDs must map to different values")
	}
}
