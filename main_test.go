package main

import (
	"io"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	rbacv1 "k8s.io/api/rbac/v1"
)

func TestParseArgsDefaults(t *testing.T) {
	t.Parallel()

	opts, err := parseArgs([]string{"example-sa"}, io.Discard)
	if err != nil {
		t.Fatalf("parseArgs() error = %v", err)
	}

	if opts.saName != "example-sa" {
		t.Fatalf("opts.saName = %q, want %q", opts.saName, "example-sa")
	}
	if opts.namespace != saNamespace {
		t.Fatalf("opts.namespace = %q, want %q", opts.namespace, saNamespace)
	}
	if opts.roleName != "read-all-except-secrets" {
		t.Fatalf("opts.roleName = %q, want %q", opts.roleName, "read-all-except-secrets")
	}
	if opts.bindingName != "example-sa-read-all-except-secrets" {
		t.Fatalf("opts.bindingName = %q, want %q", opts.bindingName, "example-sa-read-all-except-secrets")
	}
	if opts.argsComment != "" {
		t.Fatalf("opts.argsComment = %q, want empty string", opts.argsComment)
	}
}

func TestParseArgsCustomFlags(t *testing.T) {
	t.Parallel()

	opts, err := parseArgs([]string{
		"--namespace", "mgt-system",
		"--role-name", "autopilot:autopilot-readers",
		"--binding-name", "autopilot-reader",
		"autopilot-reader",
	}, io.Discard)
	if err != nil {
		t.Fatalf("parseArgs() error = %v", err)
	}

	if opts.namespace != "mgt-system" {
		t.Fatalf("opts.namespace = %q, want %q", opts.namespace, "mgt-system")
	}
	if opts.roleName != "autopilot:autopilot-readers" {
		t.Fatalf("opts.roleName = %q, want %q", opts.roleName, "autopilot:autopilot-readers")
	}
	if opts.bindingName != "autopilot-reader" {
		t.Fatalf("opts.bindingName = %q, want %q", opts.bindingName, "autopilot-reader")
	}
	if opts.argsComment != "# Args: --namespace mgt-system --role-name autopilot:autopilot-readers --binding-name autopilot-reader\n" {
		t.Fatalf("opts.argsComment = %q, want exact raw args comment", opts.argsComment)
	}
}

func TestRenderResourcesIncludesHeaderArgsCommentAndAnnotations(t *testing.T) {
	t.Parallel()

	opts := options{
		saName:      "autopilot-reader",
		namespace:   "mgt-system",
		roleName:    "autopilot:autopilot-readers",
		bindingName: "autopilot-reader",
		argsComment: "# Args: --namespace mgt-system --role-name autopilot:autopilot-readers --binding-name autopilot-reader\n",
	}
	rules := []rbacv1.PolicyRule{
		{
			APIGroups: []string{""},
			Resources: []string{"pods"},
			Verbs:     []string{"get", "list", "watch"},
		},
	}

	data, err := renderResources(buildResources(opts, rules), opts.argsComment)
	if err != nil {
		t.Fatalf("renderResources() error = %v", err)
	}

	got := string(data)
	if !strings.HasPrefix(got, "# Created by https://github.com/syself/readonly-kubernetes-service-account\n"+opts.argsComment) {
		t.Fatalf("rendered YAML missing expected header:\n%s", got)
	}
	if !strings.Contains(got, "annotations:") {
		t.Fatalf("rendered YAML missing annotations:\n%s", got)
	}
	if !strings.Contains(got, "created-by: create-readonly-service-account") {
		t.Fatalf("rendered YAML missing created-by annotation:\n%s", got)
	}
	if !strings.Contains(got, "namespace: mgt-system") {
		t.Fatalf("rendered YAML missing namespace override:\n%s", got)
	}
	if !strings.Contains(got, "name: autopilot:autopilot-readers") {
		t.Fatalf("rendered YAML missing custom role name:\n%s", got)
	}
	if !strings.Contains(got, "name: autopilot-reader") {
		t.Fatalf("rendered YAML missing custom names:\n%s", got)
	}
}

func TestRenderResourcesOmitsArgsLineWhenEmpty(t *testing.T) {
	t.Parallel()

	data, err := renderResources(nil, "")
	if err != nil {
		t.Fatalf("renderResources() error = %v", err)
	}

	got := string(data)
	want := "# Created by https://github.com/syself/readonly-kubernetes-service-account\n"
	if got != want {
		t.Fatalf("renderResources() = %q, want %q", got, want)
	}
}

func TestFormatArgsCommentQuotesFlagValues(t *testing.T) {
	t.Parallel()

	var got string
	var cmd *cobra.Command
	cmd = newRootCmd(func(opts options) error {
		got = formatArgsComment(cmd)
		return nil
	})
	cmd.SetArgs([]string{"--output", "reader team.yaml", "--binding-name", "name'withquote", "example-sa"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("cmd.Execute() error = %v", err)
	}

	want := "# Args: --output 'reader team.yaml' --binding-name 'name'\"'\"'withquote'\n"
	if got != want {
		t.Fatalf("formatArgsComment() = %q, want %q", got, want)
	}
}
