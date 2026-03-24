// Command create-readonly-service-account prints YAML for a readonly Kubernetes service account.
package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/yaml"
)

const (
	saNamespace = "default"
)

func main() {
	if err := Run(); err != nil {
		if errors.Is(err, errUsage) {
			os.Exit(2)
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

var errUsage = errors.New("usage")

type options struct {
	saName      string
	namespace   string
	roleName    string
	bindingName string
	outputPath  string
	argsComment string
}

// Run executes the command and returns a descriptive error when it fails.
func Run() error {
	opts, err := parseArgs(os.Args[1:], os.Stderr)
	if err != nil {
		return err
	}
	return run(opts, os.Stdout)
}

func run(opts options, stdout io.Writer) error {
	configLoader := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{},
	)

	restConfig, err := configLoader.ClientConfig()
	if err != nil {
		return fmt.Errorf("load kubeconfig: %w", err)
	}

	discoveryClient, err := discovery.NewDiscoveryClientForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("create discovery client: %w", err)
	}

	rules, err := buildReadonlyRules(discoveryClient)
	if err != nil {
		return err
	}

	resources := buildResources(opts, rules)
	data, err := renderResources(resources, opts.argsComment)
	if err != nil {
		return err
	}

	if opts.outputPath != "" {
		if err := os.WriteFile(opts.outputPath, data, 0o644); err != nil {
			return fmt.Errorf("write output file: %w", err)
		}
		return nil
	}

	if _, err := fmt.Fprint(stdout, string(data)); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

func parseArgs(args []string, stderr io.Writer) (options, error) {
	var opts options
	var executed bool
	var cmd *cobra.Command

	cmd = newRootCmd(func(parsed options) error {
		executed = true
		opts = parsed
		opts.argsComment = formatArgsComment(cmd)
		return nil
	})
	cmd.SetArgs(args)
	cmd.SetOut(stderr)
	cmd.SetErr(stderr)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true

	if err := cmd.Execute(); err != nil {
		if _, writeErr := fmt.Fprint(stderr, usageText(cmd)); writeErr != nil {
			return options{}, fmt.Errorf("write usage: %w", writeErr)
		}
		return options{}, errUsage
	}
	if !executed {
		return options{}, errUsage
	}

	return opts, nil
}

func formatArgsComment(cmd *cobra.Command) string {
	type flagValue struct {
		name string
	}

	values := make([]string, 0, 8)
	for _, item := range []flagValue{
		{name: "output"},
		{name: "namespace"},
		{name: "role-name"},
		{name: "binding-name"},
	} {
		flag := cmd.Flags().Lookup(item.name)
		if flag == nil || !flag.Changed {
			continue
		}
		values = append(values, "--"+item.name)
		if flag.Value.Type() != "bool" {
			values = append(values, shellQuote(flag.Value.String()))
		}
	}

	if len(values) == 0 {
		return ""
	}
	return "# Args: " + strings.Join(values, " ") + "\n"
}

func shellQuote(arg string) string {
	if arg == "" {
		return "''"
	}
	for _, r := range arg {
		if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-._/:=@+,", r) {
			return "'" + strings.ReplaceAll(arg, "'", `'"'"'`) + "'"
		}
	}
	return arg
}

func usageText(cmd *cobra.Command) string {
	return fmt.Sprintf(`Usage: %s [flags] <sa-name>
This tool creates YAML for a service account, which can read all resources, except secrets.
The SA gets access to all core resources (except secrets), and all non-core API groups.
This tool connects to your cluster, discovers which API resources and API groups exist,
and uses that information to generate a ClusterRole with readonly permissions.
It does not apply changes to the cluster.
By default it prints the YAML to stdout. With -o it writes the YAML to a file.

Flags:
%s

Run without installing:

go run github.com/syself/readonly-kubernetes-service-account@latest -o ro-sa.yaml ro-sa

Generate output like ro-orig.yaml:

go run . \
  --namespace mgt-system \
  --role-name autopilot:autopilot-readers \
  --binding-name autopilot-reader \
  autopilot-reader
`, cmd.Name(), strings.TrimRight(cmd.Flags().FlagUsagesWrapped(80), "\n"))
}

func newRootCmd(run func(options) error) *cobra.Command {
	programName := filepath.Base(os.Args[0])

	opts := options{}
	cmd := &cobra.Command{
		Use:           programName + " [flags] <sa-name>",
		Short:         "Generate YAML for a readonly Kubernetes service account.",
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.saName = args[0]
			opts.bindingName = resolveBindingName(opts.saName, opts.roleName, opts.bindingName)
			return run(opts)
		},
	}

	cmd.Flags().StringVarP(&opts.outputPath, "output", "o", "", "write YAML to file instead of stdout")
	cmd.Flags().StringVar(&opts.namespace, "namespace", saNamespace, "namespace for the ServiceAccount subject")
	cmd.Flags().StringVar(&opts.roleName, "role-name", "read-all-except-secrets", "name of the generated ClusterRole")
	cmd.Flags().StringVar(&opts.bindingName, "binding-name", "", "name of the generated ClusterRoleBinding (default: <sa-name>-<role-name>)")
	cmd.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		_, _ = fmt.Fprint(cmd.ErrOrStderr(), usageText(cmd))
	})

	return cmd
}

func resolveBindingName(saName string, roleName string, bindingName string) string {
	if bindingName != "" {
		return bindingName
	}
	return saName + "-" + roleName
}

func buildResources(opts options, rules []rbacv1.PolicyRule) []any {
	return []any{
		&corev1.ServiceAccount{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "v1",
				Kind:       "ServiceAccount",
			},
			ObjectMeta: objectMeta(opts.saName, opts.namespace),
		},
		&rbacv1.ClusterRole{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "rbac.authorization.k8s.io/v1",
				Kind:       "ClusterRole",
			},
			ObjectMeta: objectMeta(opts.roleName, ""),
			Rules:      rules,
		},
		&rbacv1.ClusterRoleBinding{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "rbac.authorization.k8s.io/v1",
				Kind:       "ClusterRoleBinding",
			},
			ObjectMeta: objectMeta(opts.bindingName, ""),
			RoleRef: rbacv1.RoleRef{
				APIGroup: rbacv1.GroupName,
				Kind:     "ClusterRole",
				Name:     opts.roleName,
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      opts.saName,
					Namespace: opts.namespace,
				},
			},
		},
	}
}

func objectMeta(name string, namespace string) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name:      name,
		Namespace: namespace,
		Annotations: map[string]string{
			"created-by": "create-readonly-service-account",
		},
	}
}

func renderResources(resources []any, argsComment string) ([]byte, error) {
	var buf bytes.Buffer

	buf.WriteString("# Created by https://github.com/syself/readonly-kubernetes-service-account\n")
	if argsComment != "" {
		buf.WriteString(argsComment)
	}
	for i, resource := range resources {
		if i > 0 {
			buf.WriteString("---\n")
		}
		data, err := yaml.Marshal(resource)
		if err != nil {
			return nil, fmt.Errorf("marshal resource yaml: %w", err)
		}
		buf.Write(data)
	}

	return buf.Bytes(), nil
}

func buildReadonlyRules(discoveryClient discovery.DiscoveryInterface) ([]rbacv1.PolicyRule, error) {
	coreResources, err := coreResourceNames(discoveryClient)
	if err != nil {
		return nil, err
	}

	groups, err := nonCoreGroups(discoveryClient)
	if err != nil {
		return nil, err
	}

	rules := []rbacv1.PolicyRule{
		{
			APIGroups: []string{""},
			Resources: coreResources,
			Verbs:     []string{"get", "list", "watch"},
		},
	}

	if len(groups) > 0 {
		rules = append(rules, rbacv1.PolicyRule{
			APIGroups: groups,
			Resources: []string{"*"},
			Verbs:     []string{"get", "list", "watch"},
		})
	}

	return rules, nil
}

func coreResourceNames(discoveryClient discovery.DiscoveryInterface) ([]string, error) {
	resources, err := discoveryClient.ServerResourcesForGroupVersion("v1")
	if err != nil {
		return nil, fmt.Errorf("discover core resources: %w", err)
	}

	names := make([]string, 0, len(resources.APIResources))
	for i := range resources.APIResources {
		resource := &resources.APIResources[i]
		if resource.Name == "secrets" || strings.HasPrefix(resource.Name, "secrets/") {
			continue
		}
		names = append(names, resource.Name)
	}

	if len(names) == 0 {
		return nil, errors.New("no core resources discovered")
	}

	sort.Strings(names)
	return names, nil
}

func nonCoreGroups(discoveryClient discovery.DiscoveryInterface) ([]string, error) {
	groups, err := discoveryClient.ServerGroups()
	if err != nil {
		return nil, fmt.Errorf("discover api groups: %w", err)
	}

	names := make([]string, 0, len(groups.Groups))
	for i := range groups.Groups {
		group := &groups.Groups[i]
		if group.Name == "" {
			continue
		}
		names = append(names, group.Name)
	}

	sort.Strings(names)
	return names, nil
}
