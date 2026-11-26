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

package authz

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	selfhostedv1alpha1 "github.com/AhmadMasry/claude-self-hosted-environment-operator/api/v1alpha1"
)

const (
	alice = "user_01EXAMPLE0000000000000001"
	bob   = "user_01EXAMPLE0000000000000002"

	listKey     = "accounts"
	unavailable = "access list is unavailable"
)

func TestParse(t *testing.T) {
	l := Parse("# production access\n" +
		alice + "   # platform lead\n" +
		"\n" +
		"  " + bob + "\r\n" +
		"alice@example.com\n" +
		"user_\n" +
		"- " + alice + "\n" +
		alice + " " + bob + "\n" +
		alice + "\n")
	if l.Len() != 2 {
		t.Fatalf("want 2 accounts (duplicates collapse), got %d", l.Len())
	}
	if want := []int{5, 6, 7, 8}; !slices.Equal(l.InvalidLines, want) {
		t.Fatalf("invalid lines: got %v want %v", l.InvalidLines, want)
	}
	for _, id := range []string{alice, bob} {
		if !l.Decide(id).Allowed {
			t.Fatalf("%s must be allowed", id)
		}
	}
	if empty := Parse(""); empty.Len() != 0 || len(empty.InvalidLines) != 0 || empty.Decide(alice).Allowed {
		t.Fatalf("an empty list must allow nobody: %+v", empty)
	}
}

func TestDecide(t *testing.T) {
	l := Parse(alice)
	cases := []struct {
		name, account string
		list          *AccessList
		allowed       bool
		message       string
	}{
		{"listed", alice, l, true, ""},
		{"not listed", bob, l, false, "account " + bob + " is not on"},
		{"no account, as a Claude Tag session", "", l, false, "has no user account ID"},
		{"an email is never an account", "alice@example.com", l, false, "has no user account ID"},
		{"a prefix of a listed ID", alice[:len(alice)-1], l, false, "is not on"},
		{"missing list", alice, &AccessList{Missing: `ConfigMap "x" not found`}, false, unavailable},
	}
	for _, tc := range cases {
		d := tc.list.Decide(tc.account)
		if d.Allowed != tc.allowed || !strings.Contains(d.Message, tc.message) {
			t.Errorf("%s: got %+v", tc.name, d)
		}
		if strings.Contains(d.Message, "@") || strings.Contains(d.Message, "ConfigMap") {
			t.Errorf("%s: the message must not carry an email or cluster detail: %q", tc.name, d.Message)
		}
	}
}

func scheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = selfhostedv1alpha1.AddToScheme(s)
	return s
}

func envWithList(mode selfhostedv1alpha1.AuthorizationMode) *selfhostedv1alpha1.ClaudeEnvironment {
	return &selfhostedv1alpha1.ClaudeEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: "prod", Namespace: "claude"},
		Spec: selfhostedv1alpha1.ClaudeEnvironmentSpec{Authorization: &selfhostedv1alpha1.AuthorizationSpec{Mode: mode,
			AllowedAccountsFrom: selfhostedv1alpha1.AccountListSource{
				ConfigMapKeyRef: selfhostedv1alpha1.ConfigMapKeyRef{Name: "prod-access", Key: listKey}}}},
	}
}

func accessList(namespace string, data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "prod-access", Namespace: namespace}, Data: data}
}

func TestCheck(t *testing.T) {
	ctx := context.Background()
	env := envWithList("")
	cases := []struct {
		name    string
		objs    []client.Object
		allowed bool
		message string
	}{
		{"listed", []client.Object{accessList("claude", map[string]string{listKey: alice + "\n"})}, true, ""},
		{"not listed", []client.Object{accessList("claude", map[string]string{listKey: bob})}, false, "is not on"},
		{"no ConfigMap", nil, false, unavailable},
		{"no key", []client.Object{accessList("claude", map[string]string{"users": alice})}, false, unavailable},
		{"a list in another namespace", []client.Object{accessList("other", map[string]string{listKey: alice})}, false, unavailable},
	}
	for _, tc := range cases {
		c := fake.NewClientBuilder().WithScheme(scheme()).WithObjects(tc.objs...).Build()
		d, err := Check(ctx, c, env, alice)
		if err != nil || d.Allowed != tc.allowed || !strings.Contains(d.Message, tc.message) {
			t.Errorf("%s: got %+v, %v", tc.name, d, err)
		}
	}
}

func TestCheckReturnsReadFailures(t *testing.T) {
	forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, "prod-access", errors.New("denied"))
	c := fake.NewClientBuilder().WithScheme(scheme()).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return forbidden
		},
	}).Build()
	if d, err := Check(context.Background(), c, envWithList(""), alice); !apierrors.IsForbidden(err) || d.Allowed {
		t.Fatalf("a list that cannot be read must be an error, never a verdict: %+v, %v", d, err)
	}
}

func TestEnforced(t *testing.T) {
	if !Enforced(envWithList("")) || !Enforced(envWithList(selfhostedv1alpha1.AuthorizationEnforce)) {
		t.Fatal("an unset mode and Enforce must enforce")
	}
	if Enforced(envWithList(selfhostedv1alpha1.AuthorizationAudit)) {
		t.Fatal("Audit must not enforce")
	}
	if Enforced(&selfhostedv1alpha1.ClaudeEnvironment{}) {
		t.Fatal("an environment without spec.authorization enforces nothing")
	}
}
