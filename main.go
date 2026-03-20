// Command create-readonly-service-account prints YAML for a readonly Kubernetes service account.
package main

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

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

// Run executes the command and returns a descriptive error when it fails.
func Run() error {
	if len(os.Args) != 2 || strings.HasPrefix(os.Args[1], "-") {
		usage()
		return errUsage
	}

	saName := os.Args[1]

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

	roleName := "read-all-except-secrets"
	bindingName := saName + "-" + roleName

	resources := []any{
		&corev1.ServiceAccount{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "v1",
				Kind:       "ServiceAccount",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      saName,
				Namespace: saNamespace,
				Annotations: map[string]string{
					"created-by": "create-readonly-service-account",
				},
			},
		},
		&rbacv1.ClusterRole{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "rbac.authorization.k8s.io/v1",
				Kind:       "ClusterRole",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: roleName,
				Annotations: map[string]string{
					"created-by": "create-readonly-service-account",
				},
			},
			Rules: rules,
		},
		&rbacv1.ClusterRoleBinding{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "rbac.authorization.k8s.io/v1",
				Kind:       "ClusterRoleBinding",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: bindingName,
				Annotations: map[string]string{
					"created-by": "create-readonly-service-account",
				},
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: rbacv1.GroupName,
				Kind:     "ClusterRole",
				Name:     roleName,
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      saName,
					Namespace: saNamespace,
				},
			},
		},
	}

	fmt.Println("# Created by create-readonly-service-account")
	for i, resource := range resources {
		if i > 0 {
			fmt.Print("---\n")
		}
		data, err := yaml.Marshal(resource)
		if err != nil {
			return fmt.Errorf("marshal resource yaml: %w", err)
		}
		fmt.Print(string(data))
	}
	return nil
}

func usage() {
	fmt.Fprintf(os.Stderr, `Usage: %s <name>
This tool creates YAML for a service account, which can read all resources, except secrets.
The SA gets access to al core resources (except secrets), and all non-core API Groups.
`, os.Args[0])
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
