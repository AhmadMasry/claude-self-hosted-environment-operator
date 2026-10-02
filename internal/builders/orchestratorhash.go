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
	"encoding/hex"
	"encoding/json"

	corev1 "k8s.io/api/core/v1"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
)

// OrchestratorConfigHash digests what must roll orchestrator pods: the
// orchestrator settings except replicas, the runner image (the default
// orchestrator image), the hook image, the concurrency cap and the
// environment secret.
func OrchestratorConfigHash(env *selfhostedv1alpha1.ClaudeEnvironment, secret *corev1.Secret, hookImage string) string {
	o := env.Spec.OnDemand.Orchestrator.DeepCopy()
	o.Replicas = nil
	h := sha256.New()
	enc := json.NewEncoder(h)
	_ = enc.Encode(o)
	writeField(h, []byte(env.Spec.Runner.Image))
	writeField(h, []byte(hookImage))
	_ = enc.Encode(env.Spec.OnDemand.MaxConcurrentRunners)
	if secret != nil {
		_, _ = h.Write([]byte{1})
		writeSortedBytes(h, secret.Data)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}
