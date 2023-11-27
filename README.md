# Korrel8r Operator

Deploys [Korrel8r](https://github.com/korrel8r/korrel8r#readme) as a REST service in a Kubernetes cluster.
See the [Korrel8r Documentation](https://korrel8r.github.io/korrel8r/) for more about Korrel8r.

## Getting Started

### Install the operator

To install the operator bundle image:

1. Log into your cluster as an admin user.
1. Install the `operator-sdk` command, see https://sdk.operatorframework.io/docs/installation/
1. Run the bundle image:
   ```sh
   operator-sdk run bundle quay.io/korrel8r/operator-bundle:latest
   ```

The operator is now active in you cluster.

### Create a Korrel8r resource

To create a korrel8r instance with default configuration:

```yaml
apiVersion: korrel8r.openshift.io/v1alpha1
kind: Korrel8r
metadata:
  name: some-name
  namespace: some-namespace
```

### Enable the openshift console preview

To enable the korrel8r preview in the openshift console, must install openshift logging, and add this annotation to your `ClusterLogging` resource:

``` yaml
apiVersion: logging.openshift.io/v1
kind: ClusterLogging
metadata:
  annotations:
    logging.openshift.io/preview-korrel8r-console: enabled
```

## Uninstall the operator

To delete the operator and custom resource definitions:

```sh
operator-sdk cleanup korrel8r
```

## Contributing

- See [HACKING.md](HACKING.md) for information on how to contribute to this operator.
- See the [Korrel8r Project](https://github.com/korrel8r/korrel8r) for information about contributing to korrel8r itself.

