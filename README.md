# readonly-kubernetes-service-account

Generate YAML for a readonly Kubernetes service account, and a kubeconfig which uses it.

## Usage

<!-- usage:start -->
```text
Usage: readonly-kubernetes-service-account [flags] <sa-name>
This tool creates YAML for a service account, which can read all resources, except secrets.
The SA gets access to all core resources (except secrets), and all non-core API groups.
Exec, attach, portforward and proxy are left out, because they would give access to
the inside of a pod, and that includes the secrets the pod uses.
This tool connects to your cluster, discovers which API resources and API groups exist,
and uses that information to generate a ClusterRole with readonly permissions.
This command does not apply changes to the cluster, the kubeconfig subcommand can.
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

Commands:
  kubeconfig  create a kubeconfig which uses the service account (see "readonly-kubernetes-service-account kubeconfig")

Run without installing:

go run github.com/syself/readonly-kubernetes-service-account@latest -o ro-sa.yaml ro-sa
```
<!-- usage:end -->

## Create a kubeconfig

Most people do not want the YAML, they want a kubeconfig which uses the service account.
The `kubeconfig` subcommand creates one. It reads your current kubeconfig to find the
cluster, so use it while you are still connected as a user who may create tokens.

Create the service account, the ClusterRole and the binding, and write a kubeconfig with
a token which never expires:

```bash
go run github.com/syself/readonly-kubernetes-service-account@latest \
    kubeconfig --apply --long-lived -o ro-sa.kubeconfig ro-sa
```

If you applied the YAML yourself, leave out `--apply`. If a token for one day is enough,
use `--duration 24h` instead of `--long-lived`:

```bash
kubectl apply -f ro-sa.yaml
go run github.com/syself/readonly-kubernetes-service-account@latest \
    kubeconfig --duration 24h -o ro-sa.kubeconfig ro-sa
```

Then use it:

```bash
export KUBECONFIG=ro-sa.kubeconfig
kubectl get pods -A       # works
kubectl get secrets -A    # forbidden
```

<!-- kubeconfig-usage:start -->
```text
Usage: readonly-kubernetes-service-account kubeconfig [flags] <sa-name>
Creates a kubeconfig which uses the service account, and prints it to stdout.
The service account must exist in the cluster, unless you use --apply.
You have to say how long the token is valid: --duration or --long-lived.

Flags:
      --apply                 create the ServiceAccount, the ClusterRole
                              and the ClusterRoleBinding in the cluster first
      --binding-name string   name of the ClusterRoleBinding, only used
                              with --apply (default: <sa-name>-<role-name>)
      --duration duration     how long the token is valid, for example
                              24h. The API server may grant less
  -h, --help                  help for kubeconfig
      --long-lived            use a token which never expires. It is
                              stored in a secret named <sa-name>-token
      --namespace string      namespace of the ServiceAccount (default
                              "default")
  -o, --output string         write kubeconfig to file (mode 0600) instead
                              of stdout
      --role-name string      name of the ClusterRole, only used with
                              --apply (default "read-all-except-secrets")

Examples:

  # The service account already exists, and a token for one day is enough:
  readonly-kubernetes-service-account kubeconfig --duration 24h -o ro-sa.kubeconfig ro-sa

  # Create everything, with a token which never expires (for CI or monitoring):
  readonly-kubernetes-service-account kubeconfig --apply --long-lived -o ro-sa.kubeconfig ro-sa
```
<!-- kubeconfig-usage:end -->

## Which kind of token?

There is no default. You have to say what you want:

`--duration 8h` asks the API server for a token which expires. Nothing is stored in the
cluster. Some clusters limit how long such a token may live, so you may get less time than
you asked for. The tool prints the expiration date, and says so when you got less. When
the token expires, run the command again.

`--long-lived` creates a secret named `<sa-name>-token`. Kubernetes puts a token into that
secret, and the token works until you delete the secret. Use this for things which have to
keep working without a human around: CI, monitoring, a dashboard, a script on a server.

## Revoking access

A long-lived token stops working when you delete its secret:

```bash
kubectl -n default delete secret ro-sa-token
```

Tokens from `--duration` cannot be revoked one by one. Deleting the service account
invalidates all of its tokens:

```bash
kubectl -n default delete serviceaccount ro-sa
```

In both cases the token keeps working for a few more seconds, because the API server
caches the result of the last check.

## What the service account can do, and what it cannot

It can read every resource of the cluster, except secrets.

It cannot use exec, attach, port-forward or the proxy subresources. Those look like reads,
but they are not: with `get pods/exec` you can run commands in any pod, and from inside a
pod you can read its environment variables, its mounted secrets and its service account
token. A "readonly" account which can do that can read your secrets, so this tool leaves
those permissions out.

If you want to check the permissions yourself, use the `--subresource` form. The short
form is misleading: `kubectl auth can-i get pods/exec` answers yes, although exec is
denied.

```bash
export KUBECONFIG=ro-sa.kubeconfig
kubectl auth can-i get pods --subresource=exec   # no
kubectl auth can-i get pods --subresource=log    # yes
kubectl exec some-pod -- env                     # forbidden
```

Keep in mind what "read everything except secrets" still includes: config maps, pod logs,
and passwords which somebody wrote directly into a pod spec as an environment variable.
Treat the kubeconfig like a password. A secret which a pod uses via `secretKeyRef` is not
readable, because the pod spec only contains the name of the secret, not its value.

## Feedback?

Please create an issue if you have an idea for how we could improve this tool.
