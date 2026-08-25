package main

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/client-go/rest"
)

func TestParseArgsDefaults(t *testing.T) {
	t.Parallel()

	parsed, err := parseArgs([]string{"yaml", "example-sa"}, io.Discard)
	if err != nil {
		t.Fatalf("parseArgs() error = %v", err)
	}
	if parsed.yaml == nil {
		t.Fatal("parseArgs() did not parse the YAML command")
	}
	opts := *parsed.yaml

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

	parsed, err := parseArgs([]string{
		"yaml",
		"--namespace", "mgt-system",
		"--role-name", "autopilot:autopilot-readers",
		"--binding-name", "autopilot-reader",
		"autopilot-reader",
	}, io.Discard)
	if err != nil {
		t.Fatalf("parseArgs() error = %v", err)
	}
	if parsed.yaml == nil {
		t.Fatal("parseArgs() did not parse the YAML command")
	}
	opts := *parsed.yaml

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

	data, err := renderResources(clusterResources{}, "")
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

	parsed, err := parseArgs([]string{
		"yaml",
		"--output", "reader team.yaml",
		"--binding-name", "name'withquote",
		"example-sa",
	}, io.Discard)
	if err != nil {
		t.Fatalf("parseArgs() error = %v", err)
	}
	if parsed.yaml == nil {
		t.Fatal("parseArgs() did not parse the YAML command")
	}

	want := "# Args: --output 'reader team.yaml' --binding-name 'name'\"'\"'withquote'\n"
	if parsed.yaml.argsComment != want {
		t.Fatalf("argsComment = %q, want %q", parsed.yaml.argsComment, want)
	}
}

func TestParseArgsNeedsACommand(t *testing.T) {
	t.Parallel()

	for name, args := range map[string][]string{
		"no arguments at all":            {},
		"only the help flag":             {"--help"},
		"the old form without a command": {"example-sa"},
		"an unknown command":             {"kubecfg", "example-sa"},
	} {
		if _, err := parseArgs(args, io.Discard); !errors.Is(err, errUsage) {
			t.Errorf("parseArgs(%s) error = %v, want errUsage", name, err)
		}
	}
}

func TestIsExcludedCoreResource(t *testing.T) {
	t.Parallel()

	excluded := []string{
		"secrets",
		"secrets/status",
		"pods/exec",
		"pods/attach",
		"pods/portforward",
		"pods/proxy",
		"services/proxy",
		"nodes/proxy",
		"bindings",
		"pods/binding",
		"pods/eviction",
		"namespaces/finalize",
		"serviceaccounts/token",
	}
	for _, name := range excluded {
		if !isExcludedCoreResource(name) {
			t.Errorf("isExcludedCoreResource(%q) = false, want true", name)
		}
	}

	kept := []string{
		"pods",
		"pods/log",
		"pods/status",
		"configmaps",
		"nodes",
		"nodes/status",
		"namespaces",
		"services",
		"serviceaccounts",
		"replicationcontrollers/scale",
	}
	for _, name := range kept {
		if isExcludedCoreResource(name) {
			t.Errorf("isExcludedCoreResource(%q) = true, want false", name)
		}
	}
}

func TestParseArgsKubeconfig(t *testing.T) {
	t.Parallel()

	parsed, err := parseArgs([]string{"kubeconfig", "--duration", "8h", "--namespace", "mgt-system", "example-sa"}, io.Discard)
	if err != nil {
		t.Fatalf("parseArgs() error = %v", err)
	}
	if parsed.kubeconfig == nil {
		t.Fatal("parseArgs() did not parse the kubeconfig command")
	}

	opts := *parsed.kubeconfig
	if opts.saName != "example-sa" {
		t.Fatalf("opts.saName = %q, want %q", opts.saName, "example-sa")
	}
	if opts.namespace != "mgt-system" {
		t.Fatalf("opts.namespace = %q, want %q", opts.namespace, "mgt-system")
	}
	if opts.duration != 8*time.Hour {
		t.Fatalf("opts.duration = %v, want %v", opts.duration, 8*time.Hour)
	}
	if opts.longLived {
		t.Fatal("opts.longLived = true, want false")
	}
	if opts.bindingName != "example-sa-read-all-except-secrets" {
		t.Fatalf("opts.bindingName = %q, want the default", opts.bindingName)
	}
}

func TestParseArgsKubeconfigNeedsExactlyOneTokenKind(t *testing.T) {
	t.Parallel()

	for name, args := range map[string][]string{
		"neither flag":    {"kubeconfig", "example-sa"},
		"both flags":      {"kubeconfig", "--duration", "8h", "--long-lived", "example-sa"},
		"zero duration":   {"kubeconfig", "--duration", "0", "example-sa"},
		"missing sa name": {"kubeconfig", "--long-lived"},
	} {
		if _, err := parseArgs(args, io.Discard); !errors.Is(err, errUsage) {
			t.Errorf("parseArgs(%s) error = %v, want errUsage", name, err)
		}
	}
}

func TestBuildKubeconfig(t *testing.T) {
	t.Parallel()

	opts := kubeconfigOptions{saName: "ro-sa", namespace: "mgt-system"}
	restConfig := &rest.Config{Host: "https://api.example.com:6443"}

	config := buildKubeconfig(opts, "prod", restConfig, []byte("ca-data"), "token-data")

	if config.CurrentContext != "ro-sa@prod" {
		t.Fatalf("config.CurrentContext = %q, want %q", config.CurrentContext, "ro-sa@prod")
	}
	cluster, ok := config.Clusters["prod"]
	if !ok {
		t.Fatal("config has no cluster named prod")
	}
	if cluster.Server != "https://api.example.com:6443" {
		t.Fatalf("cluster.Server = %q, want the server of the current kubeconfig", cluster.Server)
	}
	if string(cluster.CertificateAuthorityData) != "ca-data" {
		t.Fatalf("cluster.CertificateAuthorityData = %q, want %q", cluster.CertificateAuthorityData, "ca-data")
	}
	if cluster.InsecureSkipTLSVerify {
		t.Fatal("cluster.InsecureSkipTLSVerify = true, want false")
	}
	if got := config.AuthInfos["ro-sa"].Token; got != "token-data" {
		t.Fatalf("token = %q, want %q", got, "token-data")
	}
	if got := config.Contexts["ro-sa@prod"].Namespace; got != "mgt-system" {
		t.Fatalf("context namespace = %q, want %q", got, "mgt-system")
	}
}

func TestRenderKubeconfigStartsWithComment(t *testing.T) {
	t.Parallel()

	config := buildKubeconfig(kubeconfigOptions{saName: "ro-sa", namespace: "default"},
		"prod", &rest.Config{Host: "https://api.example.com:6443"}, []byte("ca-data"), "token-data")

	data, err := renderKubeconfig(config)
	if err != nil {
		t.Fatalf("renderKubeconfig() error = %v", err)
	}
	if !strings.HasPrefix(string(data), "# Created by https://github.com/syself/readonly-kubernetes-service-account\n") {
		t.Fatalf("kubeconfig does not start with the created-by comment:\n%s", data)
	}
}

func TestBuildKubeconfigInsecureCluster(t *testing.T) {
	t.Parallel()

	restConfig := &rest.Config{Host: "https://api.example.com:6443"}
	restConfig.Insecure = true

	config := buildKubeconfig(kubeconfigOptions{saName: "ro-sa", namespace: "default"},
		"prod", restConfig, []byte("ca-data"), "token-data")

	cluster := config.Clusters["prod"]
	if !cluster.InsecureSkipTLSVerify {
		t.Fatal("cluster.InsecureSkipTLSVerify = false, want true")
	}
	// kubectl refuses a kubeconfig which has both.
	if len(cluster.CertificateAuthorityData) != 0 {
		t.Fatalf("cluster.CertificateAuthorityData = %q, want empty", cluster.CertificateAuthorityData)
	}
}
