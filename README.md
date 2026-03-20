# readonly-kubernetes-service-account

Generate YAML for a readonly Kubernetes service account.

## Usage

```text
Usage: readonly-kubernetes-service-account [-o output.yaml] <name>
This tool creates YAML for a service account, which can read all resources, except secrets.
The SA gets access to all core resources (except secrets), and all non-core API groups.
This tool connects to your cluster, discovers which API resources and API groups exist,
and uses that information to generate a ClusterRole with readonly permissions.
It does not apply changes to the cluster.
By default it prints the YAML to stdout. With -o it writes the YAML to a file.
```

## Example

```bash
go run . -o readonly-service-account.yaml example-sa
kubectl apply -f readonly-service-account.yaml
```

## Development

Regenerate this README with:

```bash
./hack/update-readme.sh
```
