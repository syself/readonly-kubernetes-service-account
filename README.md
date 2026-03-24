# readonly-kubernetes-service-account

Generate YAML for a readonly Kubernetes service account.

## Usage

<!-- usage:start -->
```text
Usage: readonly-kubernetes-service-account [flags] <sa-name>
This tool creates YAML for a service account, which can read all resources, except secrets.
The SA gets access to all core resources (except secrets), and all non-core API groups.
This tool connects to your cluster, discovers which API resources and API groups exist,
and uses that information to generate a ClusterRole with readonly permissions.
It does not apply changes to the cluster.
By default it prints the YAML to stdout. With -o it writes the YAML to a file.

Flags:
      --binding-name string   name of the generated ClusterRoleBinding
                              (default: <sa-name>-<role-name>)
  -h, --help                  help for readonly-kubernetes-service-account
      --namespace string      namespace for the ServiceAccount subject
                              (default "default")
  -o, --output string         write YAML to file instead of stdout
      --role-name string      name of the generated ClusterRole (default
                              "read-all-except-secrets")

Run without installing:

go run github.com/syself/readonly-kubernetes-service-account@latest -o ro-sa.yaml ro-sa
```
<!-- usage:end -->

## Feedback?

Please create an issue if you have an idea for how we could improve this tool.
