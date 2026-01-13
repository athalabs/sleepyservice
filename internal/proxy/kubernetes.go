package proxy

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

var cnpgGVR = schema.GroupVersionResource{
	Group:    "postgresql.cnpg.io",
	Version:  "v1",
	Resource: "clusters",
}

var sleepyServiceGVR = schema.GroupVersionResource{
	Group:    "sleepy.atha.gr",
	Version:  "v1alpha1",
	Resource: "sleepyservices",
}

// K8sClient wraps Kubernetes API interactions
type K8sClient struct {
	clientset     kubernetes.Interface
	dynamicClient dynamic.Interface
}

// NewK8sClient creates a new Kubernetes client using in-cluster config
func NewK8sClient() (*K8sClient, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get in-cluster config: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create clientset: %w", err)
	}

	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create dynamic client: %w", err)
	}

	return &K8sClient{
		clientset:     clientset,
		dynamicClient: dynamicClient,
	}, nil
}

// GetDeployment retrieves a deployment
func (k *K8sClient) GetDeployment(ctx context.Context, namespace, name string) (*appsv1.Deployment, error) {
	return k.clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
}

// IsDeploymentReady checks if deployment has ready replicas
func (k *K8sClient) IsDeploymentReady(ctx context.Context, namespace, name string) (bool, error) {
	deploy, err := k.GetDeployment(ctx, namespace, name)
	if err != nil {
		return false, err
	}

	return deploy.Status.ReadyReplicas > 0, nil
}

// ScaleDeployment scales a deployment to the specified replicas
func (k *K8sClient) ScaleDeployment(ctx context.Context, namespace, name string, replicas int32) error {
	scale, err := k.clientset.AppsV1().Deployments(namespace).GetScale(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get scale: %w", err)
	}

	scale.Spec.Replicas = replicas

	_, err = k.clientset.AppsV1().Deployments(namespace).UpdateScale(ctx, name, scale, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update scale: %w", err)
	}

	return nil
}

// WaitForDeploymentReady waits for a deployment to have ready replicas
func (k *K8sClient) WaitForDeploymentReady(ctx context.Context, namespace, name string, onProgress func(string, int)) error {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	startTime := time.Now()
	maxWait := 5 * time.Minute

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			deploy, err := k.GetDeployment(ctx, namespace, name)
			if err != nil {
				continue
			}

			desired := int32(1)
			if deploy.Spec.Replicas != nil {
				desired = *deploy.Spec.Replicas
			}

			ready := deploy.Status.ReadyReplicas
			total := deploy.Status.Replicas

			// Calculate progress
			elapsed := time.Since(startTime)
			timeProgress := int(elapsed.Seconds() / maxWait.Seconds() * 100)
			if timeProgress > 95 {
				timeProgress = 95
			}

			replicaProgress := 0
			if desired > 0 {
				replicaProgress = int(ready * 100 / desired)
			}

			// Use whichever is higher
			progress := timeProgress
			if replicaProgress > progress {
				progress = replicaProgress
			}

			msg := fmt.Sprintf("Pods: %d/%d ready", ready, total)
			if onProgress != nil {
				onProgress(msg, progress)
			}

			if ready >= desired && ready > 0 {
				return nil
			}
		}
	}
}

// GetCNPGCluster retrieves a CNPG cluster
func (k *K8sClient) GetCNPGCluster(ctx context.Context, namespace, name string) (*unstructured.Unstructured, error) {
	return k.dynamicClient.Resource(cnpgGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
}

// IsCNPGHibernated checks if CNPG cluster is hibernated
func (k *K8sClient) IsCNPGHibernated(ctx context.Context, namespace, name string) (bool, error) {
	cluster, err := k.GetCNPGCluster(ctx, namespace, name)
	if err != nil {
		return false, err
	}

	annotations := cluster.GetAnnotations()
	if annotations == nil {
		return false, nil
	}

	return annotations["cnpg.io/hibernation"] == "on", nil
}

// WakeCNPGCluster removes the hibernation annotation from a CNPG cluster
func (k *K8sClient) WakeCNPGCluster(ctx context.Context, namespace, name string) error {
	cluster, err := k.GetCNPGCluster(ctx, namespace, name)
	if err != nil {
		return fmt.Errorf("failed to get cluster: %w", err)
	}

	annotations := cluster.GetAnnotations()
	if annotations == nil {
		return nil // Not hibernated
	}

	if _, exists := annotations["cnpg.io/hibernation"]; !exists {
		return nil // Not hibernated
	}

	// Remove the annotation
	delete(annotations, "cnpg.io/hibernation")
	cluster.SetAnnotations(annotations)

	_, err = k.dynamicClient.Resource(cnpgGVR).Namespace(namespace).Update(ctx, cluster, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update cluster: %w", err)
	}

	return nil
}

// HibernateCNPGCluster sets the hibernation annotation on a CNPG cluster
func (k *K8sClient) HibernateCNPGCluster(ctx context.Context, namespace, name string) error {
	cluster, err := k.GetCNPGCluster(ctx, namespace, name)
	if err != nil {
		return fmt.Errorf("failed to get cluster: %w", err)
	}

	annotations := cluster.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}

	annotations["cnpg.io/hibernation"] = "on"
	cluster.SetAnnotations(annotations)

	_, err = k.dynamicClient.Resource(cnpgGVR).Namespace(namespace).Update(ctx, cluster, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update cluster: %w", err)
	}

	return nil
}

// WaitForCNPGReady waits for CNPG cluster to be ready
func (k *K8sClient) WaitForCNPGReady(ctx context.Context, namespace, name string, onProgress func(string, int)) error {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	startTime := time.Now()
	maxWait := 3 * time.Minute

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			cluster, err := k.GetCNPGCluster(ctx, namespace, name)
			if err != nil {
				continue
			}

			// Get status
			status, found, err := unstructured.NestedMap(cluster.Object, "status")
			if err != nil || !found {
				continue
			}

			// Check phase
			phase, _, _ := unstructured.NestedString(status, "phase")

			// Check ready instances
			readyInstances, _, _ := unstructured.NestedInt64(status, "readyInstances")
			instances, _, _ := unstructured.NestedInt64(status, "instances")

			// Calculate progress
			elapsed := time.Since(startTime)
			timeProgress := int(elapsed.Seconds() / maxWait.Seconds() * 100)
			if timeProgress > 95 {
				timeProgress = 95
			}

			instanceProgress := 0
			if instances > 0 {
				instanceProgress = int(readyInstances * 100 / instances)
			}

			progress := timeProgress
			if instanceProgress > progress {
				progress = instanceProgress
			}

			msg := fmt.Sprintf("Database: %s (%d/%d instances)", phase, readyInstances, instances)
			if onProgress != nil {
				onProgress(msg, progress)
			}

			// Check if ready
			if phase == "Cluster in healthy state" && readyInstances > 0 {
				return nil
			}
		}
	}
}

// GetSleepyService retrieves a SleepyService resource
func (k *K8sClient) GetSleepyService(ctx context.Context, namespace, name string) (*unstructured.Unstructured, error) {
	return k.dynamicClient.Resource(sleepyServiceGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
}

// UpdateSleepyServiceStatus updates the status subresource of a SleepyService
func (k *K8sClient) UpdateSleepyServiceStatus(ctx context.Context, namespace, name, desiredState string, lastActivity *metav1.Time) error {
	hs, err := k.GetSleepyService(ctx, namespace, name)
	if err != nil {
		return fmt.Errorf("failed to get SleepyService: %w", err)
	}

	// Update status fields
	status, _, _ := unstructured.NestedMap(hs.Object, "status")
	if status == nil {
		status = make(map[string]interface{})
	}

	status["desiredState"] = desiredState
	if lastActivity != nil {
		status["lastActivity"] = lastActivity.Format(time.RFC3339)
	}

	if err := unstructured.SetNestedMap(hs.Object, status, "status"); err != nil {
		return fmt.Errorf("failed to set status: %w", err)
	}

	// Update status subresource
	_, err = k.dynamicClient.Resource(sleepyServiceGVR).Namespace(namespace).UpdateStatus(ctx, hs, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update status: %w", err)
	}

	return nil
}

// GetSleepyServiceState gets the current state from SleepyService status
func (k *K8sClient) GetSleepyServiceState(ctx context.Context, namespace, name string) (string, error) {
	hs, err := k.GetSleepyService(ctx, namespace, name)
	if err != nil {
		return "", fmt.Errorf("failed to get SleepyService: %w", err)
	}

	state, found, err := unstructured.NestedString(hs.Object, "status", "state")
	if err != nil || !found {
		return "", fmt.Errorf("state not found in status")
	}

	return state, nil
}

// WaitForSleepyServiceAwake polls the SleepyService until State becomes Awake
func (k *K8sClient) WaitForSleepyServiceAwake(ctx context.Context, namespace, name string, onProgress func(string, int)) error {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	startTime := time.Now()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			hs, err := k.GetSleepyService(ctx, namespace, name)
			if err != nil {
				continue
			}

			// Get state and components status
			state, _, _ := unstructured.NestedString(hs.Object, "status", "state")
			components, _, _ := unstructured.NestedSlice(hs.Object, "status", "components")

			// Calculate progress based on component readiness
			readyCount := 0
			totalCount := len(components)
			for _, comp := range components {
				compMap, ok := comp.(map[string]interface{})
				if !ok {
					continue
				}
				ready, _, _ := unstructured.NestedBool(compMap, "ready")
				if ready {
					readyCount++
				}
			}

			progress := 0
			if totalCount > 0 {
				progress = readyCount * 100 / totalCount
			}

			// Cap progress at 95% until Awake
			if progress > 95 && state != "Awake" {
				progress = 95
			}

			// Update progress message
			elapsed := time.Since(startTime)
			msg := fmt.Sprintf("Waking service (%d/%d components ready, %ds elapsed)", readyCount, totalCount, int(elapsed.Seconds()))
			if onProgress != nil {
				onProgress(msg, progress)
			}

			// Check if awake
			if state == "Awake" {
				return nil
			}
		}
	}
}
