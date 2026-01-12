# SleepyService

A Kubernetes operator that automatically hibernates (scales to zero) workloads when they're not in use and wakes them up on-demand when traffic arrives. Save resources and reduce costs for development, staging, and low-traffic environments.

## Overview

SleepyService creates a smart proxy in front of your Kubernetes workloads that:
- **Hibernates** your deployments, statefulsets, and databases when idle
- **Wakes them up automatically** when traffic arrives
- Shows a friendly **waiting page** with real-time progress updates
- Supports **automatic idle timeout** for hands-free cost savings
- Works with **CloudNativePG databases** for full-stack hibernation

Perfect for:
- Development and staging environments
- Preview environments for pull requests
- Demo applications with sporadic usage
- Cost optimization without sacrificing availability

## How It Works

```
┌─────────┐      ┌──────────────┐      ┌─────────────┐
│  User   │─────▶│  Wake Proxy  │─────▶│   Backend   │
└─────────┘      └──────────────┘      │ (Your App)  │
                        │               └─────────────┘
                        │                      │
                        │               ┌─────────────┐
                        │               │  Database   │
                        │               │   (CNPG)    │
                        │               └─────────────┘
                        ▼
                 ┌──────────────┐
                 │ SleepyService│
                 │  Controller  │
                 └──────────────┘
```

1. **When Sleeping**: All components (deployments, databases) are scaled to zero
2. **On Request**: Wake proxy detects traffic and triggers the wake-up sequence
3. **During Wake**: Users see a waiting page with real-time progress via SSE
4. **When Awake**: Traffic is proxied transparently to your application
5. **Auto-Sleep**: After configured idle time, components hibernate automatically

## Quick Start

### Prerequisites
- Kubernetes cluster v1.11.3+
- kubectl v1.11.3+
- Go 1.23+ (for development)
- Docker 17.03+ (for building images)

### Installation

1. Install the CRDs:
```sh
make install
```

2. Deploy the operator:
```sh
make deploy IMG=<your-registry>/sleepyservice:tag
```

Or use the installation bundle:
```sh
kubectl apply -f https://raw.githubusercontent.com/<org>/sleepyservice/<tag>/dist/install.yaml
```

### Basic Example

Create a `SleepyService` for a simple web application:

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

  # Health check path for readiness verification
  healthPath: /health

  # Backend service configuration
  backendService:
    enabled: true
    type: ClusterIP
    ports:
      - name: http
        port: 80
        targetPort: 8080

  # Components to manage (in dependency order)
  components:
    - name: postgres
      type: CNPGCluster
      ref:
        name: my-postgres-cluster

    - name: app
      type: Deployment
      ref:
        name: my-app-deployment
      replicas: 2
      dependsOn:
        - postgres
```

This creates:
- A wake proxy service at `my-app.default.svc.cluster.local`
- A backend service at `my-app-actual.default.svc.cluster.local`
- Automatic orchestration of PostgreSQL and app wake-up sequence

Access your app through the proxy service, and it will automatically wake up when traffic arrives!

## API Reference

### SleepyServiceSpec

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `components` | []Component | Yes | - | Components to manage, in dependency order |
| `backendService` | BackendServiceSpec | No | - | Configuration for the managed backend Service |
| `healthPath` | string | No | `/health` | Health check path for readiness verification |
| `wakeTimeout` | Duration | No | `5m` | Maximum time to wait for wake-up |
| `idleTimeout` | Duration | No | `0` (disabled) | Auto-hibernate after this period of inactivity |

### Component

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `name` | string | Yes | - | Unique identifier for this component |
| `type` | ComponentType | Yes | - | Type: `Deployment`, `StatefulSet`, or `CNPGCluster` |
| `ref` | ResourceRef | Yes | - | Reference to the Kubernetes resource |
| `replicas` | int32 | No | `1` | Target replicas when awake (for Deployment/StatefulSet) |
| `dependsOn` | []string | No | - | Names of components that must be ready first |

### BackendServiceSpec

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `enabled` | bool | No | `true` | Whether to create the backend Service |
| `type` | ServiceType | No | `ClusterIP` | Service type: `ClusterIP`, `NodePort`, `LoadBalancer` |
| `ports` | []ServicePort | No | Auto-detect | Ports to expose (auto-created from first container port if empty) |
| `annotations` | map[string]string | No | - | Annotations to add to the Service |
| `clusterIP` | string | No | Auto-assign | Specific ClusterIP to use |
| `externalIPs` | []string | No | - | External IPs for the Service |
| `loadBalancerIP` | string | No | - | LoadBalancer IP (for LoadBalancer type) |
| `sessionAffinity` | ServiceAffinity | No | - | Session affinity: `ClientIP` or `None` |

### ServicePort

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `name` | string | No | - | Port name (required if multiple ports) |
| `protocol` | Protocol | No | `TCP` | Protocol: `TCP`, `UDP`, or `SCTP` |
| `port` | int32 | Yes | - | Port to expose on the Service |
| `targetPort` | IntOrString | No | Same as `port` | Target port on pods |
| `nodePort` | int32 | No | Auto-assign | NodePort (for NodePort/LoadBalancer) |

### ResourceRef

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `name` | string | Yes | - | Name of the resource |
| `namespace` | string | No | Same as SleepyService | Namespace of the resource |

### SleepyServiceStatus

| Field | Type | Description |
|-------|------|-------------|
| `state` | ServiceState | Current state: `Sleeping`, `Waking`, `Awake`, `Hibernating`, `Error` |
| `desiredState` | ServiceState | Desired state set by the proxy |
| `lastTransition` | Time | When the state last changed |
| `lastActivity` | Time | When traffic was last received (set by proxy) |
| `components` | []ComponentStatus | Status of each managed component |
| `proxyDeployment` | string | Name of the created proxy deployment |
| `backendService` | string | Name of the created backend service |
| `conditions` | []Condition | Standard Kubernetes conditions |

## Examples

### Example 1: Simple Web Application

```yaml
apiVersion: sleepy.atha.gr/v1alpha1
kind: SleepyService
metadata:
  name: demo-app
spec:
  idleTimeout: 30m
  healthPath: /

  components:
    - name: web
      type: Deployment
      ref:
        name: demo-web
      replicas: 1
```

### Example 2: Full Stack with Database

```yaml
apiVersion: sleepy.atha.gr/v1alpha1
kind: SleepyService
metadata:
  name: fullstack-app
spec:
  idleTimeout: 1h
  wakeTimeout: 10m
  healthPath: /api/health

  backendService:
    type: ClusterIP
    ports:
      - name: http
        port: 80
        targetPort: 3000
      - name: metrics
        port: 9090
        targetPort: 9090
    annotations:
      prometheus.io/scrape: "true"
      prometheus.io/port: "9090"

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

    # Worker depends on database
    - name: worker
      type: Deployment
      ref:
        name: background-worker
      replicas: 2
      dependsOn:
        - database
```

### Example 3: StatefulSet Application

```yaml
apiVersion: sleepy.atha.gr/v1alpha1
kind: SleepyService
metadata:
  name: stateful-app
spec:
  idleTimeout: 2h
  healthPath: /health

  components:
    - name: app
      type: StatefulSet
      ref:
        name: my-statefulset
      replicas: 3
```

### Example 4: Multi-Namespace Setup

```yaml
apiVersion: sleepy.atha.gr/v1alpha1
kind: SleepyService
metadata:
  name: cross-ns-app
  namespace: frontend
spec:
  components:
    # Database in different namespace
    - name: db
      type: CNPGCluster
      ref:
        name: shared-postgres
        namespace: databases

    # App in same namespace as SleepyService
    - name: web
      type: Deployment
      ref:
        name: web-app
      dependsOn:
        - db
```

### Example 5: Custom Backend Service Configuration

```yaml
apiVersion: sleepy.atha.gr/v1alpha1
kind: SleepyService
metadata:
  name: loadbalancer-app
spec:
  backendService:
    type: LoadBalancer
    loadBalancerIP: "203.0.113.100"
    sessionAffinity: ClientIP
    ports:
      - name: https
        port: 443
        targetPort: 8443
        protocol: TCP
    annotations:
      service.beta.kubernetes.io/aws-load-balancer-type: "nlb"

  components:
    - name: app
      type: Deployment
      ref:
        name: secure-app
```

## Component Types

### Deployment
Standard Kubernetes Deployment. Scaled to 0 when sleeping, scaled to `replicas` when awake.

### StatefulSet
Kubernetes StatefulSet with persistent volumes. Scaled to 0 when sleeping, maintaining PVC claims.

### CNPGCluster
[CloudNativePG](https://cloudnative-pg.io/) PostgreSQL cluster. Hibernated using the `cnpg.io/hibernation: "on"` annotation.

## State Transitions

```
     ┌──────────┐
     │ Sleeping │◀────────────┐
     └─────┬────┘             │
           │                  │
      Traffic Detected        │
           │              Idle Timeout
           ▼                  │
      ┌─────────┐             │
      │ Waking  │             │
      └────┬────┘             │
           │                  │
   All Components Ready       │
           │                  │
           ▼                  │
       ┌───────┐              │
       │ Awake │──────────────┘
       └───────┘
```

## Wake Proxy Features

The automatically created wake proxy provides:

- **Waiting Page**: Beautiful UI with real-time progress updates via Server-Sent Events
- **Health Monitoring**: Continuously checks backend health after wake-up
- **Idle Detection**: Tracks traffic and triggers auto-hibernation
- **Manual Controls**: HTTP API for manual wake/hibernate operations

### Proxy Endpoints

- `/_wake/status` - JSON status of current state
- `/_wake/events` - SSE stream for real-time updates
- `/_wake/trigger` - POST to manually trigger wake-up
- `/_wake/hibernate` - POST to manually hibernate
- `/_wake/health` - Proxy health check

All other paths are proxied to your backend application.

## Development

### Running Locally

```sh
# Install CRDs
make install

# Run controller locally
make run

# Run tests
make test

# Build binary
make build
```

### Building and Deploying

```sh
# Build and push image
make docker-build docker-push IMG=<your-registry>/sleepyservice:tag

# Deploy to cluster
make deploy IMG=<your-registry>/sleepyservice:tag

# View logs
kubectl logs -n sleepyservice-system deployment/sleepyservice-controller-manager -f
```

### Cleanup

```sh
# Delete sample instances
kubectl delete -k config/samples/

# Uninstall CRDs
make uninstall

# Remove controller
make undeploy
```

## Architecture Details

### Operator Components

1. **Controller**: Reconciles SleepyService resources, manages component scaling
2. **Wake Proxy**: Lightweight proxy that handles traffic interception and wake-up
3. **RBAC Setup**: ServiceAccount, Role, and RoleBinding for proxy permissions

### How Components Wake Up

1. **Proxy** receives traffic while services are sleeping
2. **Proxy** updates `SleepyService.status.desiredState` to `Awake`
3. **Controller** detects state change and starts wake-up sequence
4. **Controller** scales components in dependency order
5. **Controller** updates `SleepyService.status.state` to `Waking`
6. **Proxy** polls status and updates waiting page in real-time
7. **Controller** detects all components ready, sets state to `Awake`
8. **Proxy** performs health check on backend
9. **Proxy** starts forwarding traffic

### Resource Ownership

- Wake proxy Deployment, Service, ServiceAccount, Role, and RoleBinding are owned by the SleepyService
- Backend Service (if created) is owned by the SleepyService
- Managed components (Deployments, StatefulSets, CNPG clusters) are NOT owned - only scaled

## Troubleshooting

### Check SleepyService Status

```sh
kubectl get sleepyservice my-app -o yaml
```

Look at:
- `.status.state` - Current state
- `.status.components` - Per-component readiness
- `.status.conditions` - Detailed conditions

### Check Proxy Logs

```sh
kubectl logs deployment/my-app-wakeproxy -f
```

### Check Controller Logs

```sh
kubectl logs -n sleepyservice-system deployment/sleepyservice-controller-manager -f
```

### Common Issues

**Components not waking up**
- Verify RBAC permissions for the controller
- Check component selectors and references are correct
- Ensure components exist in specified namespaces

**Wake proxy shows error**
- Check backend service configuration
- Verify health check path is correct
- Ensure target deployments have correct labels

**Auto-hibernation not working**
- Confirm `idleTimeout` is set and non-zero
- Check proxy is receiving and tracking traffic
- Verify proxy has permissions to update SleepyService status

## Project Distribution

### YAML Bundle

1. Build the installer:
```sh
make build-installer IMG=<your-registry>/sleepyservice:tag
```

2. Distribute the generated `dist/install.yaml`:
```sh
kubectl apply -f https://raw.githubusercontent.com/<org>/sleepyservice/<tag>/dist/install.yaml
```

### Helm Chart

1. Generate Helm chart:
```sh
kubebuilder edit --plugins=helm/v1-alpha
```

2. Chart is available in `dist/chart/`

## Contributing

Contributions are welcome! This project uses:
- [Kubebuilder](https://book.kubebuilder.io/) for operator scaffolding
- Standard Go testing with `make test`
- `golangci-lint` for code quality

Run `make help` for all available targets.

## License

Copyright 2026 AthaLabs

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
