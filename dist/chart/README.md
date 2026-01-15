# SleepyService Helm Chart

A Kubernetes operator that automatically hibernates (scales to zero) workloads when they're not in use and wakes them up on-demand when traffic arrives. Perfect for cost optimization in development, staging, and low-traffic environments.

## Features

- **Automatic Hibernation**: Scale workloads to zero during idle periods
- **On-Demand Wake-Up**: Automatically wake services when traffic arrives
- **Interactive Waiting Page**: Show users real-time progress while services wake up
- **Smart Dependency Management**: Wake components in the correct order
- **Multi-Component Support**: Manage Deployments, StatefulSets, and CloudNativePG databases
- **Configurable Timeouts**: Set custom idle and wake timeouts per service
- **Zero Downtime**: Services are transparently available through wake proxy

## Prerequisites

- Kubernetes 1.11.3+
- Helm 3.0+

## Installation

### Install from OCI Registry

```bash
# Install latest version
helm install sleepyservice oci://ghcr.io/athalabs/charts/sleepyservice

# Install specific version
helm install sleepyservice oci://ghcr.io/athalabs/charts/sleepyservice --version 0.1.0

# Install in custom namespace
helm install sleepyservice oci://ghcr.io/athalabs/charts/sleepyservice \
  --namespace sleepyservice-system \
  --create-namespace
```

### Install with Custom Values

```bash
helm install sleepyservice oci://ghcr.io/athalabs/charts/sleepyservice \
  --set controllerManager.replicas=2 \
  --set controllerManager.container.resources.limits.cpu=1000m \
  --set metrics.enable=true \
  --set prometheus.enable=true
```

Or use a values file:

```bash
helm install sleepyservice oci://ghcr.io/athalabs/charts/sleepyservice \
  -f my-values.yaml
```

## Configuration

The following table lists the configurable parameters of the SleepyService chart and their default values.

### Controller Manager

| Parameter | Description | Default |
|-----------|-------------|---------|
| `controllerManager.replicas` | Number of controller manager replicas | `1` |
| `controllerManager.container.image.repository` | Controller manager image repository | `ghcr.io/athalabs/sleepyservice` |
| `controllerManager.container.image.tag` | Controller manager image tag | `<chart-version>` |
| `controllerManager.container.resources.limits.cpu` | CPU limit for controller manager | `500m` |
| `controllerManager.container.resources.limits.memory` | Memory limit for controller manager | `128Mi` |
| `controllerManager.container.resources.requests.cpu` | CPU request for controller manager | `10m` |
| `controllerManager.container.resources.requests.memory` | Memory request for controller manager | `64Mi` |
| `controllerManager.terminationGracePeriodSeconds` | Termination grace period | `10` |
| `controllerManager.serviceAccountName` | ServiceAccount name to use | `sleepyservice-controller-manager` |

### Controller Manager Arguments

| Parameter | Description | Default |
|-----------|-------------|---------|
| `controllerManager.container.args` | Additional arguments for controller manager | `["--leader-elect", "--metrics-bind-address=:8443", "--health-probe-bind-address=:8081"]` |

### Health Probes

| Parameter | Description | Default |
|-----------|-------------|---------|
| `controllerManager.container.livenessProbe.initialDelaySeconds` | Initial delay for liveness probe | `15` |
| `controllerManager.container.livenessProbe.periodSeconds` | Period for liveness probe | `20` |
| `controllerManager.container.readinessProbe.initialDelaySeconds` | Initial delay for readiness probe | `5` |
| `controllerManager.container.readinessProbe.periodSeconds` | Period for readiness probe | `10` |

### Security Context

| Parameter | Description | Default |
|-----------|-------------|---------|
| `controllerManager.securityContext.runAsNonRoot` | Run as non-root user | `true` |
| `controllerManager.container.securityContext.allowPrivilegeEscalation` | Allow privilege escalation | `false` |
| `controllerManager.container.securityContext.capabilities.drop` | Dropped capabilities | `["ALL"]` |

### RBAC

| Parameter | Description | Default |
|-----------|-------------|---------|
| `rbac.enable` | Enable RBAC resources | `true` |

### CRDs

| Parameter | Description | Default |
|-----------|-------------|---------|
| `crd.enable` | Install Custom Resource Definitions | `true` |
| `crd.keep` | Keep CRDs on chart uninstall (adds `helm.sh/resource-policy: keep` annotation) | `true` |

### Metrics

| Parameter | Description | Default |
|-----------|-------------|---------|
| `metrics.enable` | Enable metrics export | `true` |

### Prometheus

| Parameter | Description | Default |
|-----------|-------------|---------|
| `prometheus.enable` | Enable Prometheus ServiceMonitor | `false` |

### Cert-Manager

| Parameter | Description | Default |
|-----------|-------------|---------|
| `certmanager.enable` | Enable cert-manager webhook integration | `false` |

### Network Policy

| Parameter | Description | Default |
|-----------|-------------|---------|
| `networkPolicy.enable` | Enable NetworkPolicies | `false` |

## Usage

After installing the chart, create a `SleepyService` resource to manage your workloads:

### Basic Example

```yaml
apiVersion: sleepy.atha.gr/v1alpha1
kind: SleepyService
metadata:
  name: my-app
  namespace: default
spec:
  # Auto-hibernate after 15 minutes of no traffic
  idleTimeout: 15m

  # Wait up to 5 minutes for wake-up
  wakeTimeout: 5m

  # Backend service configuration
  backendService:
    enabled: true
    type: ClusterIP
    ports:
      - name: http
        port: 80
        targetPort: 8080

  # Components to manage
  components:
    - name: app
      type: Deployment
      ref:
        name: my-app-deployment
      replicas: 2
```

### Full Stack Example with Database

```yaml
apiVersion: sleepy.atha.gr/v1alpha1
kind: SleepyService
metadata:
  name: fullstack-app
  namespace: default
spec:
  idleTimeout: 1h
  wakeTimeout: 10m

  backendService:
    type: ClusterIP
    ports:
      - name: http
        port: 80
        targetPort: 3000

  components:
    # Database wakes first
    - name: database
      type: CNPGCluster
      ref:
        name: app-db

    # API depends on database
    - name: api
      type: Deployment
      ref:
        name: api-server
      replicas: 3
      dependsOn:
        - database
```

## Upgrading

### Upgrade to Latest Version

```bash
helm upgrade sleepyservice oci://ghcr.io/athalabs/charts/sleepyservice
```

### Upgrade to Specific Version

```bash
helm upgrade sleepyservice oci://ghcr.io/athalabs/charts/sleepyservice --version 0.2.0
```

### Upgrade with New Values

```bash
helm upgrade sleepyservice oci://ghcr.io/athalabs/charts/sleepyservice \
  --set controllerManager.replicas=3 \
  --reuse-values
```

## Uninstalling

```bash
helm uninstall sleepyservice
```

**Note**: If `crd.keep` is set to `true` (default), the CRDs will remain installed. To remove them manually:

```bash
kubectl delete crd sleepyservices.sleepy.atha.gr
```

**Warning**: Deleting CRDs will also delete all `SleepyService` resources in your cluster.

## Advanced Configuration

### Enable Prometheus Monitoring

```yaml
metrics:
  enable: true

prometheus:
  enable: true
```

This creates a `ServiceMonitor` resource for Prometheus to scrape metrics from the controller manager.

### Enable Cert-Manager Webhooks

```yaml
certmanager:
  enable: true
```

Enables cert-manager integration for webhook certificates.

### Enable Network Policies

```yaml
networkPolicy:
  enable: true
```

Creates NetworkPolicies to restrict traffic to the controller manager.

### Customize Resource Limits

```yaml
controllerManager:
  replicas: 2
  container:
    resources:
      limits:
        cpu: 1000m
        memory: 256Mi
      requests:
        cpu: 100m
        memory: 128Mi
```

### Use Custom Image

```yaml
controllerManager:
  container:
    image:
      repository: my-registry.example.com/sleepyservice
      tag: custom-v1.0.0
```

## Troubleshooting

### Check Controller Logs

```bash
kubectl logs -n sleepyservice-system deployment/sleepyservice-controller-manager -f
```

### Check SleepyService Status

```bash
kubectl get sleepyservice
kubectl describe sleepyservice my-app
```

### Check CRD Installation

```bash
kubectl get crd sleepyservices.sleepy.atha.gr
```

### Common Issues

**Controller not starting**
- Check resource limits and node capacity
- Verify RBAC permissions are properly configured
- Check image pull secrets if using private registry

**SleepyService resources not reconciling**
- Ensure CRDs are installed: `kubectl get crd`
- Check controller logs for errors
- Verify referenced resources (Deployments, StatefulSets) exist

**Metrics not available**
- Ensure `metrics.enable=true` is set
- Check that metrics service is created: `kubectl get svc -n sleepyservice-system`
- For Prometheus, verify ServiceMonitor is created: `kubectl get servicemonitor -n sleepyservice-system`

## Learn More

- [Project Repository](https://github.com/athalabs/sleepyservice)
- [Full Documentation](https://github.com/athalabs/sleepyservice#readme)
- [API Reference](https://github.com/athalabs/sleepyservice#api-reference)
- [Examples](https://github.com/athalabs/sleepyservice#examples)

## Support

For issues and questions:
- [GitHub Issues](https://github.com/athalabs/sleepyservice/issues)
- [GitHub Discussions](https://github.com/athalabs/sleepyservice/discussions)

## License

Copyright 2026 AthaLabs

Licensed under the Apache License, Version 2.0. See [LICENSE](https://github.com/athalabs/sleepyservice/blob/main/LICENSE) for details.
