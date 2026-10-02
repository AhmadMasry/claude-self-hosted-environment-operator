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

package builders

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"slices"

	corev1 "k8s.io/api/core/v1"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
)

// ConfigHash digests everything that must roll runner pods when it changes:
// the runner spec, the environment secret's data, and referenced ConfigMaps.
// Replica counts and other workload-level fields are excluded on purpose.
// The input configMaps slice is not modified.
func ConfigHash(env *selfhostedv1alpha1.ClaudeEnvironment, secret *corev1.Secret, configMaps []*corev1.ConfigMap) string {
	h := sha256.New()
	enc := json.NewEncoder(h)
	_ = enc.Encode(env.Spec.Runner)
	if secret != nil {
		_, _ = h.Write([]byte{1})
		writeSortedBytes(h, secret.Data)
	}
	filteredCMs := slices.Clone(configMaps)
	filteredCMs = slices.DeleteFunc(filteredCMs, func(cm *corev1.ConfigMap) bool { return cm == nil })
	slices.SortFunc(filteredCMs, func(i, j *corev1.ConfigMap) int {
		if i.Name < j.Name {
			return -1
		}
		if i.Name > j.Name {
			return 1
		}
		return 0
	})
	for _, cm := range filteredCMs {
		_, _ = h.Write([]byte{2})
		writeField(h, []byte(cm.Name))
		writeSortedStrings(h, cm.Data)
		writeSortedBytes(h, cm.BinaryData)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func writeField(h io.Writer, b []byte) {
	var lenBuf [8]byte
	binary.BigEndian.PutUint64(lenBuf[:], uint64(len(b)))
	_, _ = h.Write(lenBuf[:])
	_, _ = h.Write(b)
}

func writeSortedBytes(h io.Writer, m map[string][]byte) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		writeField(h, []byte(k))
		writeField(h, m[k])
	}
}

func writeSortedStrings(h io.Writer, m map[string]string) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		writeField(h, []byte(k))
		writeField(h, []byte(m[k]))
	}
}
