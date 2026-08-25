// Command readonly-kubernetes-service-account has two subcommands: "yaml" prints the
// YAML for a readonly Kubernetes service account, and "kubeconfig" creates a kubeconfig
// which uses that service account.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/yaml"
)

const (
	saNamespace       = "default"
	yamlCmdName       = "yaml"
	kubeconfigCmdName = "kubeconfig"

	// rootCACertConfigMap exists in every namespace and holds the CA certificate of the
	// cluster. It is the fallback when the local kubeconfig has no CA certificate.
	rootCACertConfigMap = "kube-root-ca.crt"

	tokenSecretWaitTimeout = 30 * time.Second
)

// excludedCoreResources are core resources which the generated ClusterRole does not contain.
//
// Secrets are excluded because not being able to read them is the point of this tool.
//
// The exec, attach, portforward and proxy subresources are excluded because "get" on them
// is not a read. Kubectl runs exec over a WebSocket connection, and that is an HTTP GET,
// so "get pods/exec" is enough to run commands in any pod of the cluster. From inside the
// pod you can read its environment variables, its mounted secrets and its service account
// token. In other words: granting these makes "cannot read secrets" meaningless.
//
// The last group only works with create or update. A reader cannot use them anyway, so
// there is no reason to grant them.
var excludedCoreResources = map[string]bool{
	"secrets": true,

	"nodes/proxy":      true,
	"pods/attach":      true,
	"pods/exec":        true,
	"pods/portforward": true,
	"pods/proxy":       true,
	"services/proxy":   true,

	"bindings":              true,
	"namespaces/finalize":   true,
	"pods/binding":          true,
	"pods/eviction":         true,
	"serviceaccounts/token": true,
}

// isExcludedCoreResource reports whether the core resource must be left out of the ClusterRole.
func isExcludedCoreResource(name string) bool {
	return excludedCoreResources[name] || strings.HasPrefix(name, "secrets/")
}

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

// kubeconfigOptions are the options of the kubeconfig subcommand.
type kubeconfigOptions struct {
	saName      string
	namespace   string
	roleName    string
	bindingName string
	duration    time.Duration
	longLived   bool
	apply       bool
	outputPath  string
}

// parsedArgs holds the options of the subcommand which the user called. Exactly one
// field is set.
type parsedArgs struct {
	yaml       *options
	kubeconfig *kubeconfigOptions
}

// Run executes the command and returns a descriptive error when it fails.
func Run() error {
	parsed, err := parseArgs(os.Args[1:], os.Stderr)
	if err != nil {
		return err
	}

	switch {
	case parsed.yaml != nil:
		return runYAML(*parsed.yaml, os.Stdout)
	case parsed.kubeconfig != nil:
		return runKubeconfig(context.Background(), *parsed.kubeconfig, os.Stdout, os.Stderr)
	}
	return errUsage
}

func runYAML(opts options, stdout io.Writer) error {
	restConfig, _, err := loadClientConfig()
	if err != nil {
		return err
	}

	discoveryClient, err := discovery.NewDiscoveryClientForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("create discovery client: %w", err)
	}

	rules, err := buildReadonlyRules(discoveryClient)
	if err != nil {
		return err
	}

	data, err := renderResources(buildResources(opts, rules), opts.argsComment)
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

// runKubeconfig creates a token for the service account and writes a kubeconfig which
// uses that token.
func runKubeconfig(ctx context.Context, opts kubeconfigOptions, stdout io.Writer, stderr io.Writer) error {
	restConfig, loader, err := loadClientConfig()
	if err != nil {
		return err
	}

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("create client: %w", err)
	}

	if opts.apply {
		if err := applyResources(ctx, clientset, opts, stderr); err != nil {
			return err
		}
	}

	if _, err := clientset.CoreV1().ServiceAccounts(opts.namespace).Get(ctx, opts.saName, metav1.GetOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("service account %s/%s does not exist: apply the YAML of this tool first, or use --apply",
				opts.namespace, opts.saName)
		}
		return fmt.Errorf("get service account %s/%s: %w", opts.namespace, opts.saName, err)
	}

	var token string
	var caData []byte
	if opts.longLived {
		token, caData, err = longLivedToken(ctx, clientset, opts, stderr)
	} else {
		token, err = expiringToken(ctx, clientset, opts, stderr)
	}
	if err != nil {
		return err
	}

	if len(caData) == 0 && !restConfig.Insecure {
		caData, err = clusterCA(ctx, restConfig, clientset, opts.namespace)
		if err != nil {
			return err
		}
	}

	kubeconfig := buildKubeconfig(opts, clusterName(loader), restConfig, caData, token)
	data, err := renderKubeconfig(kubeconfig)
	if err != nil {
		return err
	}

	if opts.outputPath == "" {
		if _, err := stdout.Write(data); err != nil {
			return fmt.Errorf("write output: %w", err)
		}
		return nil
	}

	// A kubeconfig with a token in it is a credential, so keep it readable by its owner only.
	if err := os.WriteFile(opts.outputPath, data, 0o600); err != nil {
		return fmt.Errorf("write output file: %w", err)
	}
	if err := os.Chmod(opts.outputPath, 0o600); err != nil {
		return fmt.Errorf("chmod output file: %w", err)
	}
	_, _ = fmt.Fprintf(stderr, "wrote %s\n", opts.outputPath)
	_, _ = fmt.Fprintf(stderr, "try it with: KUBECONFIG=%s kubectl get pods -A\n", opts.outputPath)
	return nil
}

// loadClientConfig reads the kubeconfig of the user, the same way kubectl does.
func loadClientConfig() (*rest.Config, clientcmd.ClientConfig, error) {
	loader := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{},
	)

	restConfig, err := loader.ClientConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	return restConfig, loader, nil
}

// applyResources creates or updates the ServiceAccount, the ClusterRole and the
// ClusterRoleBinding in the cluster.
func applyResources(ctx context.Context, clientset kubernetes.Interface, opts kubeconfigOptions, stderr io.Writer) error {
	rules, err := buildReadonlyRules(clientset.Discovery())
	if err != nil {
		return err
	}

	resources := buildResources(options{
		saName:      opts.saName,
		namespace:   opts.namespace,
		roleName:    opts.roleName,
		bindingName: opts.bindingName,
	}, rules)

	if err := applyServiceAccount(ctx, clientset, resources.serviceAccount, stderr); err != nil {
		return err
	}
	if err := applyClusterRole(ctx, clientset, resources.clusterRole, stderr); err != nil {
		return err
	}
	return applyClusterRoleBinding(ctx, clientset, resources.clusterRoleBinding, stderr)
}

func applyServiceAccount(ctx context.Context, clientset kubernetes.Interface, sa *corev1.ServiceAccount, stderr io.Writer) error {
	_, err := clientset.CoreV1().ServiceAccounts(sa.Namespace).Create(ctx, sa, metav1.CreateOptions{})
	switch {
	case err == nil:
		_, _ = fmt.Fprintf(stderr, "created ServiceAccount %s/%s\n", sa.Namespace, sa.Name)
		return nil
	case apierrors.IsAlreadyExists(err):
		// A ServiceAccount has nothing to update.
		_, _ = fmt.Fprintf(stderr, "ServiceAccount %s/%s exists already\n", sa.Namespace, sa.Name)
		return nil
	default:
		return fmt.Errorf("create service account %s/%s: %w", sa.Namespace, sa.Name, err)
	}
}

func applyClusterRole(ctx context.Context, clientset kubernetes.Interface, role *rbacv1.ClusterRole, stderr io.Writer) error {
	existing, err := clientset.RbacV1().ClusterRoles().Get(ctx, role.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, err := clientset.RbacV1().ClusterRoles().Create(ctx, role, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create cluster role %s: %w", role.Name, err)
		}
		_, _ = fmt.Fprintf(stderr, "created ClusterRole %s\n", role.Name)
		return nil
	}
	if err != nil {
		return fmt.Errorf("get cluster role %s: %w", role.Name, err)
	}

	// The rules depend on the API groups of the cluster, so they can change over time.
	existing.Rules = role.Rules
	if _, err := clientset.RbacV1().ClusterRoles().Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update cluster role %s: %w", role.Name, err)
	}
	_, _ = fmt.Fprintf(stderr, "updated ClusterRole %s\n", role.Name)
	return nil
}

func applyClusterRoleBinding(ctx context.Context, clientset kubernetes.Interface, binding *rbacv1.ClusterRoleBinding, stderr io.Writer) error {
	existing, err := clientset.RbacV1().ClusterRoleBindings().Get(ctx, binding.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, err := clientset.RbacV1().ClusterRoleBindings().Create(ctx, binding, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create cluster role binding %s: %w", binding.Name, err)
		}
		_, _ = fmt.Fprintf(stderr, "created ClusterRoleBinding %s\n", binding.Name)
		return nil
	}
	if err != nil {
		return fmt.Errorf("get cluster role binding %s: %w", binding.Name, err)
	}

	// The roleRef of a ClusterRoleBinding cannot be changed.
	if existing.RoleRef != binding.RoleRef {
		return fmt.Errorf("ClusterRoleBinding %s points to %s %q, not to %q. Delete it first, or use --binding-name",
			binding.Name, existing.RoleRef.Kind, existing.RoleRef.Name, binding.RoleRef.Name)
	}

	existing.Subjects = binding.Subjects
	if _, err := clientset.RbacV1().ClusterRoleBindings().Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update cluster role binding %s: %w", binding.Name, err)
	}
	_, _ = fmt.Fprintf(stderr, "updated ClusterRoleBinding %s\n", binding.Name)
	return nil
}

// expiringToken asks the API server for a token which expires. Nothing gets stored in
// the cluster.
func expiringToken(ctx context.Context, clientset kubernetes.Interface, opts kubeconfigOptions, stderr io.Writer) (string, error) {
	seconds := int64(opts.duration.Seconds())
	request := &authenticationv1.TokenRequest{
		Spec: authenticationv1.TokenRequestSpec{ExpirationSeconds: &seconds},
	}

	created, err := clientset.CoreV1().ServiceAccounts(opts.namespace).CreateToken(ctx, opts.saName, request, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("create token for service account %s/%s: %w", opts.namespace, opts.saName, err)
	}

	expires := created.Status.ExpirationTimestamp.Time
	_, _ = fmt.Fprintf(stderr, "token expires at %s\n", expires.UTC().Format(time.RFC3339))
	if granted := time.Until(expires); granted < opts.duration-time.Minute {
		_, _ = fmt.Fprintf(stderr, "note: you asked for %s, but the API server only grants %s\n",
			opts.duration, granted.Round(time.Minute))
	}
	return created.Status.Token, nil
}

// longLivedToken creates a token secret and waits until Kubernetes has filled it. Such a
// token is valid until the secret gets deleted.
func longLivedToken(ctx context.Context, clientset kubernetes.Interface, opts kubeconfigOptions, stderr io.Writer) (string, []byte, error) {
	name := opts.saName + "-token"
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: opts.namespace,
			Annotations: map[string]string{
				corev1.ServiceAccountNameKey: opts.saName,
				"created-by":                 "create-readonly-service-account",
			},
		},
		Type: corev1.SecretTypeServiceAccountToken,
	}

	switch _, err := clientset.CoreV1().Secrets(opts.namespace).Create(ctx, secret, metav1.CreateOptions{}); {
	case err == nil:
		_, _ = fmt.Fprintf(stderr, "created Secret %s/%s\n", opts.namespace, name)
	case apierrors.IsAlreadyExists(err):
		_, _ = fmt.Fprintf(stderr, "Secret %s/%s exists already, reusing its token\n", opts.namespace, name)
	default:
		return "", nil, fmt.Errorf("create secret %s/%s: %w", opts.namespace, name, err)
	}

	var token, caData []byte
	err := wait.PollUntilContextTimeout(ctx, time.Second, tokenSecretWaitTimeout, true,
		func(ctx context.Context) (bool, error) {
			current, err := clientset.CoreV1().Secrets(opts.namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return false, err
			}
			if current.Annotations[corev1.ServiceAccountNameKey] != opts.saName {
				return false, fmt.Errorf("secret %s/%s belongs to service account %q",
					opts.namespace, name, current.Annotations[corev1.ServiceAccountNameKey])
			}
			token = current.Data["token"]
			caData = current.Data["ca.crt"]
			return len(token) > 0, nil
		})
	if err != nil {
		return "", nil, fmt.Errorf("wait for token in secret %s/%s: %w", opts.namespace, name, err)
	}

	_, _ = fmt.Fprintf(stderr, "this token does not expire. Delete the secret to revoke it:\n")
	_, _ = fmt.Fprintf(stderr, "  kubectl -n %s delete secret %s\n", opts.namespace, name)
	return string(token), caData, nil
}

// clusterCA returns the CA certificate which the kubeconfig needs to trust the API server.
func clusterCA(ctx context.Context, restConfig *rest.Config, clientset kubernetes.Interface, namespace string) ([]byte, error) {
	if len(restConfig.CAData) > 0 {
		return restConfig.CAData, nil
	}

	if restConfig.CAFile != "" {
		data, err := os.ReadFile(restConfig.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read CA file %s: %w", restConfig.CAFile, err)
		}
		return data, nil
	}

	configMap, err := clientset.CoreV1().ConfigMaps(namespace).Get(ctx, rootCACertConfigMap, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("your kubeconfig has no CA certificate, and reading it from the config map %s/%s failed: %w",
			namespace, rootCACertConfigMap, err)
	}
	if data := configMap.Data["ca.crt"]; data != "" {
		return []byte(data), nil
	}
	return nil, fmt.Errorf("config map %s/%s contains no ca.crt", namespace, rootCACertConfigMap)
}

// clusterName returns the name which the current kubeconfig uses for the cluster.
func clusterName(loader clientcmd.ClientConfig) string {
	raw, err := loader.RawConfig()
	if err != nil {
		return "kubernetes"
	}
	if kubeContext, ok := raw.Contexts[raw.CurrentContext]; ok && kubeContext.Cluster != "" {
		return kubeContext.Cluster
	}
	return "kubernetes"
}

func buildKubeconfig(opts kubeconfigOptions, cluster string, restConfig *rest.Config, caData []byte, token string) *clientcmdapi.Config {
	contextName := opts.saName + "@" + cluster

	config := clientcmdapi.NewConfig()
	// A kubeconfig may contain a CA certificate or insecure-skip-tls-verify, not both.
	clusterConfig := &clientcmdapi.Cluster{Server: restConfig.Host}
	if restConfig.Insecure {
		clusterConfig.InsecureSkipTLSVerify = true
	} else {
		clusterConfig.CertificateAuthorityData = caData
	}
	config.Clusters[cluster] = clusterConfig
	config.AuthInfos[opts.saName] = &clientcmdapi.AuthInfo{Token: token}
	config.Contexts[contextName] = &clientcmdapi.Context{
		Cluster:   cluster,
		AuthInfo:  opts.saName,
		Namespace: opts.namespace,
	}
	config.CurrentContext = contextName
	return config
}

func renderKubeconfig(config *clientcmdapi.Config) ([]byte, error) {
	data, err := clientcmd.Write(*config)
	if err != nil {
		return nil, fmt.Errorf("marshal kubeconfig: %w", err)
	}
	return append([]byte("# Created by https://github.com/syself/readonly-kubernetes-service-account\n"), data...), nil
}

func parseArgs(args []string, stderr io.Writer) (parsedArgs, error) {
	var parsed parsedArgs

	rootCmd := newRootCmd(
		func(opts options) error {
			parsed.yaml = &opts
			return nil
		},
		func(opts kubeconfigOptions) error {
			parsed.kubeconfig = &opts
			return nil
		},
	)
	rootCmd.SetArgs(args)
	rootCmd.SetOut(stderr)
	rootCmd.SetErr(stderr)

	// The subcommand which cobra called, which is the one whose usage we want to print.
	calledCmd, err := rootCmd.ExecuteC()
	if calledCmd == nil {
		calledCmd = rootCmd
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %v\n\n", err)
		if _, writeErr := fmt.Fprint(stderr, usageFor(calledCmd)); writeErr != nil {
			return parsedArgs{}, fmt.Errorf("write usage: %w", writeErr)
		}
		return parsedArgs{}, errUsage
	}
	if parsed.yaml == nil && parsed.kubeconfig == nil {
		return parsedArgs{}, errUsage
	}

	return parsed, nil
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

// usageFor returns the usage text of the command which cobra called.
func usageFor(cmd *cobra.Command) string {
	switch cmd.Name() {
	case yamlCmdName:
		return yamlUsageText(cmd)
	case kubeconfigCmdName:
		return kubeconfigUsageText(cmd)
	default:
		return rootUsageText(cmd)
	}
}

func rootUsageText(cmd *cobra.Command) string {
	return fmt.Sprintf(`Usage: %s <command> [flags] <sa-name>
Creates a Kubernetes service account which can read everything, except secrets.

Commands:
  %-10s print the YAML for the ServiceAccount, the ClusterRole and the binding
  %-10s create a kubeconfig which uses the service account

Run "%s <command> --help" to see the flags of a command.

Run without installing:

go run github.com/syself/readonly-kubernetes-service-account@latest yaml -o ro-sa.yaml ro-sa
`, cmd.Name(), yamlCmdName, kubeconfigCmdName, cmd.Name())
}

func yamlUsageText(cmd *cobra.Command) string {
	return fmt.Sprintf(`Usage: %s %s [flags] <sa-name>
Prints the YAML for a service account which can read all resources, except secrets:
a ServiceAccount, a ClusterRole and a ClusterRoleBinding.
The SA gets access to all core resources (except secrets), and all non-core API groups.
Exec, attach, portforward and proxy are left out, because they would give access to
the inside of a pod, and that includes the secrets the pod uses.
This command connects to your cluster, discovers which API resources and API groups
exist, and uses that information to generate the ClusterRole.
It changes nothing in the cluster. Apply the YAML with kubectl, or let the %s
command do that for you with --apply.
By default it prints the YAML to stdout. With -o it writes the YAML to a file.

Flags:
%s

Example:

  %s %s -o ro-sa.yaml ro-sa
`, cmd.Root().Name(), cmd.Name(), kubeconfigCmdName,
		strings.TrimRight(cmd.Flags().FlagUsagesWrapped(80), "\n"),
		cmd.Root().Name(), cmd.Name())
}

func kubeconfigUsageText(cmd *cobra.Command) string {
	return fmt.Sprintf(`Usage: %s %s [flags] <sa-name>
Creates a kubeconfig which uses the service account, and prints it to stdout.
The service account must exist in the cluster, unless you use --apply.
You have to say how long the token is valid: --duration or --long-lived.

Flags:
%s

Examples:

  # The service account already exists, and a token for one day is enough:
  %s %s --duration 24h -o ro-sa.kubeconfig ro-sa

  # Create everything, with a token which never expires (for CI or monitoring):
  %s %s --apply --long-lived -o ro-sa.kubeconfig ro-sa
`, cmd.Root().Name(), cmd.Name(),
		strings.TrimRight(cmd.Flags().FlagUsagesWrapped(80), "\n"),
		cmd.Root().Name(), cmd.Name(),
		cmd.Root().Name(), cmd.Name())
}

func newRootCmd(runYAMLCmd func(options) error, runKubeconfigCmd func(kubeconfigOptions) error) *cobra.Command {
	programName := filepath.Base(os.Args[0])

	// The root command has no work of its own. Without a subcommand it prints the help.
	cmd := &cobra.Command{
		Use:           programName + " <command> [flags] <sa-name>",
		Short:         "Create a Kubernetes service account which can read everything, except secrets.",
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	cmd.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		_, _ = fmt.Fprint(cmd.ErrOrStderr(), usageFor(cmd))
	})

	cmd.AddCommand(newYAMLCmd(runYAMLCmd))
	cmd.AddCommand(newKubeconfigCmd(runKubeconfigCmd))

	return cmd
}

func newYAMLCmd(run func(options) error) *cobra.Command {
	opts := options{}
	cmd := &cobra.Command{
		Use:           yamlCmdName + " [flags] <sa-name>",
		Short:         "Print the YAML for the ServiceAccount, the ClusterRole and the binding.",
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.saName = args[0]
			opts.bindingName = resolveBindingName(opts.saName, opts.roleName, opts.bindingName)
			opts.argsComment = formatArgsComment(cmd)
			if run == nil {
				return errors.New("no handler for the yaml command")
			}
			return run(opts)
		},
	}

	cmd.Flags().StringVarP(&opts.outputPath, "output", "o", "", "write YAML to file instead of stdout")
	cmd.Flags().StringVar(&opts.namespace, "namespace", saNamespace, "namespace for the ServiceAccount subject")
	cmd.Flags().StringVar(&opts.roleName, "role-name", "read-all-except-secrets", "name of the generated ClusterRole")
	cmd.Flags().StringVar(&opts.bindingName, "binding-name", "", "name of the generated ClusterRoleBinding (default: <sa-name>-<role-name>)")

	cmd.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		_, _ = fmt.Fprint(cmd.ErrOrStderr(), yamlUsageText(cmd))
	})

	return cmd
}

func newKubeconfigCmd(run func(kubeconfigOptions) error) *cobra.Command {
	opts := kubeconfigOptions{}
	cmd := &cobra.Command{
		Use:           kubeconfigCmdName + " [flags] <sa-name>",
		Short:         "Create a kubeconfig which uses the service account.",
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.saName = args[0]
			opts.bindingName = resolveBindingName(opts.saName, opts.roleName, opts.bindingName)
			if !opts.longLived && opts.duration <= 0 {
				return errors.New("--duration must be positive")
			}
			if run == nil {
				return errors.New("no handler for the kubeconfig command")
			}
			return run(opts)
		},
	}

	cmd.Flags().StringVarP(&opts.outputPath, "output", "o", "", "write kubeconfig to file (mode 0600) instead of stdout")
	cmd.Flags().StringVar(&opts.namespace, "namespace", saNamespace, "namespace of the ServiceAccount")
	cmd.Flags().DurationVar(&opts.duration, "duration", 0, "how long the token is valid, for example 24h. The API server may grant less")
	cmd.Flags().BoolVar(&opts.longLived, "long-lived", false, "use a token which never expires. It is stored in a secret named <sa-name>-token")
	cmd.Flags().BoolVar(&opts.apply, "apply", false, "create the ServiceAccount, the ClusterRole and the ClusterRoleBinding in the cluster first")
	cmd.Flags().StringVar(&opts.roleName, "role-name", "read-all-except-secrets", "name of the ClusterRole, only used with --apply")
	cmd.Flags().StringVar(&opts.bindingName, "binding-name", "", "name of the ClusterRoleBinding, only used with --apply (default: <sa-name>-<role-name>)")

	cmd.MarkFlagsMutuallyExclusive("duration", "long-lived")
	cmd.MarkFlagsOneRequired("duration", "long-lived")

	cmd.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		_, _ = fmt.Fprint(cmd.ErrOrStderr(), kubeconfigUsageText(cmd))
	})

	return cmd
}

func resolveBindingName(saName string, roleName string, bindingName string) string {
	if bindingName != "" {
		return bindingName
	}
	return saName + "-" + roleName
}

// clusterResources are the objects which this tool creates.
type clusterResources struct {
	serviceAccount     *corev1.ServiceAccount
	clusterRole        *rbacv1.ClusterRole
	clusterRoleBinding *rbacv1.ClusterRoleBinding
}

// objects returns the resources in the order in which they get written to the YAML.
func (r clusterResources) objects() []any {
	objects := make([]any, 0, 3)
	if r.serviceAccount != nil {
		objects = append(objects, r.serviceAccount)
	}
	if r.clusterRole != nil {
		objects = append(objects, r.clusterRole)
	}
	if r.clusterRoleBinding != nil {
		objects = append(objects, r.clusterRoleBinding)
	}
	return objects
}

func buildResources(opts options, rules []rbacv1.PolicyRule) clusterResources {
	return clusterResources{
		serviceAccount: &corev1.ServiceAccount{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "v1",
				Kind:       "ServiceAccount",
			},
			ObjectMeta: objectMeta(opts.saName, opts.namespace),
		},
		clusterRole: &rbacv1.ClusterRole{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "rbac.authorization.k8s.io/v1",
				Kind:       "ClusterRole",
			},
			ObjectMeta: objectMeta(opts.roleName, ""),
			Rules:      rules,
		},
		clusterRoleBinding: &rbacv1.ClusterRoleBinding{
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

func renderResources(resources clusterResources, argsComment string) ([]byte, error) {
	var buf bytes.Buffer

	buf.WriteString("# Created by https://github.com/syself/readonly-kubernetes-service-account\n")
	if argsComment != "" {
		buf.WriteString(argsComment)
	}
	for i, resource := range resources.objects() {
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
		if isExcludedCoreResource(resource.Name) {
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
