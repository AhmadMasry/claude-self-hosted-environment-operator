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

// Package authz decides whether a session creator may use a ClaudeEnvironment.
// The spawn-runner hook and the ClaudeRunner controller both call it, so one
// rule is applied to one list in both places.
package authz

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
)

// accountID is the tagged account ID Anthropic sends as
// CLAUDE_RUNNER_ACCOUNT_ID. Nothing else is accepted, in the list or from a
// request, so an email address never matches and never reaches a message.
var accountID = regexp.MustCompile(`^user_[A-Za-z0-9_-]{1,128}$`)

// AccessList is the parsed allow-list of one environment.
type AccessList struct {
	// Missing says why the list could not be read: the ConfigMap or its key
	// does not exist. It is empty when the list was read. A missing list
	// allows nobody.
	Missing string
	// InvalidLines holds the 1-based numbers of the lines that are not account
	// IDs. Those lines are ignored. Their text is never reported, because a
	// mistyped entry is often an email address.
	InvalidLines []int

	accounts map[string]struct{}
}

// Len is the number of accounts the list allows.
func (l *AccessList) Len() int { return len(l.accounts) }

// Parse reads one account ID per line. Blank lines and text after a # are
// ignored, and so is any line that is not an account ID, which is recorded in
// InvalidLines: a typo neither widens access nor locks out the other accounts.
func Parse(data string) *AccessList {
	l := &AccessList{accounts: map[string]struct{}{}}
	for i, line := range strings.Split(data, "\n") {
		if before, _, found := strings.Cut(line, "#"); found {
			line = before
		}
		line = strings.TrimSpace(line)
		switch {
		case line == "":
		case accountID.MatchString(line):
			l.accounts[line] = struct{}{}
		default:
			l.InvalidLines = append(l.InvalidLines, i+1)
		}
	}
	return l
}

// Load reads the access list env.Spec.Authorization names. A ConfigMap or key
// that does not exist yields a list that allows nobody, with Missing set; any
// other read failure is returned, so the caller retries instead of deciding
// on a list it could not read.
func Load(ctx context.Context, r client.Reader, env *selfhostedv1alpha1.ClaudeEnvironment) (*AccessList, error) {
	ref := env.Spec.Authorization.AllowedAccountsFrom.ConfigMapKeyRef
	cm := &corev1.ConfigMap{}
	if err := r.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: env.Namespace}, cm); err != nil {
		if apierrors.IsNotFound(err) {
			return &AccessList{Missing: fmt.Sprintf("ConfigMap %q not found", ref.Name)}, nil
		}
		return nil, err
	}
	data, ok := cm.Data[ref.Key]
	if !ok {
		return &AccessList{Missing: fmt.Sprintf("ConfigMap %q has no key %q", ref.Name, ref.Key)}, nil
	}
	return Parse(data), nil
}

// Decision is the verdict on one account.
type Decision struct {
	Allowed bool
	// Message says why the account is refused, in words fit for the session's
	// creator: Anthropic shows it in the environment's Activity tab. It names
	// the account ID so the creator can ask to be added, and nothing about the
	// cluster.
	Message string
}

// Decide reports whether the list allows account. An ID that is not a tagged
// account ID is refused without being echoed.
func (l *AccessList) Decide(account string) Decision {
	switch {
	case l.Missing != "":
		return Decision{Message: "this self-hosted environment's access list is unavailable, so every session is refused; ask the environment's administrators"}
	case !accountID.MatchString(account):
		return Decision{Message: "this session has no user account ID, and this self-hosted environment serves only the accounts on its access list"}
	}
	if _, ok := l.accounts[account]; ok {
		return Decision{Allowed: true}
	}
	return Decision{Message: fmt.Sprintf("account %s is not on this self-hosted environment's access list; ask the environment's administrators to add this account ID", account)}
}

// Check loads env's access list and decides on account. env must have
// spec.authorization set.
func Check(ctx context.Context, r client.Reader, env *selfhostedv1alpha1.ClaudeEnvironment, account string) (Decision, error) {
	l, err := Load(ctx, r, env)
	if err != nil {
		return Decision{}, err
	}
	return l.Decide(account), nil
}

// Enforced reports whether env refuses the accounts its access list does not
// allow. Only an explicit Audit mode does not: an unset mode enforces.
func Enforced(env *selfhostedv1alpha1.ClaudeEnvironment) bool {
	a := env.Spec.Authorization
	return a != nil && a.Mode != selfhostedv1alpha1.AuthorizationAudit
}
