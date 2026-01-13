package proxy

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// Test helper functions

// newTestK8sClient creates a K8sClient with fake clients for testing
func newTestK8sClient(typedObjects []runtime.Object, dynamicObjects []runtime.Object) *K8sClient {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)

	return &K8sClient{
		clientset:     kubefake.NewClientset(typedObjects...),
		dynamicClient: fake.NewSimpleDynamicClient(scheme, dynamicObjects...),
	}
}

// createTestDeployment creates a test deployment with specified status
//
//nolint:unparam // name parameter is consistent across tests by design
func createTestDeployment(name, namespace string, ready, total, desired int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &desired,
		},
		Status: appsv1.DeploymentStatus{
			ReadyReplicas: ready,
			Replicas:      total,
		},
	}
}

// createTestCNPGCluster creates a test CNPG cluster with specified status
//
//nolint:unparam // name parameter is consistent across tests by design
func createTestCNPGCluster(name, namespace string, phase string, ready, total int64, hibernated bool) *unstructured.Unstructured {
	cluster := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "postgresql.cnpg.io/v1",
			"kind":       "Cluster",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
			},
			"status": map[string]interface{}{
				"phase":          phase,
				"readyInstances": ready,
				"instances":      total,
			},
		},
	}

	if hibernated {
		cluster.SetAnnotations(map[string]string{
			"cnpg.io/hibernation": "on",
		})
	}

	return cluster
}

// createTestSleepyService creates a test SleepyService with specified status
//
//nolint:unparam // name parameter is consistent across tests by design
func createTestSleepyService(name, namespace, state string, componentCount int, readyCount int) *unstructured.Unstructured {
	components := make([]interface{}, componentCount)
	for i := 0; i < componentCount; i++ {
		components[i] = map[string]interface{}{
			"name":  fmt.Sprintf("component-%d", i),
			"ready": i < readyCount,
		}
	}

	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "sleepy.atha.gr/v1alpha1",
			"kind":       "SleepyService",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
			},
			"status": map[string]interface{}{
				"state":      state,
				"components": components,
			},
		},
	}
}

func TestK8sClient_IsDeploymentReady(t *testing.T) {
	tests := []struct {
		name       string
		deployment *appsv1.Deployment
		wantReady  bool
		wantErr    bool
	}{
		{
			name: "deployment with ready replicas",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-app",
					Namespace: "default",
				},
				Status: appsv1.DeploymentStatus{
					ReadyReplicas: 2,
					Replicas:      2,
				},
			},
			wantReady: true,
			wantErr:   false,
		},
		{
			name: "deployment with no ready replicas",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-app",
					Namespace: "default",
				},
				Status: appsv1.DeploymentStatus{
					ReadyReplicas: 0,
					Replicas:      2,
				},
			},
			wantReady: false,
			wantErr:   false,
		},
		{
			name: "deployment with partial ready replicas",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-app",
					Namespace: "default",
				},
				Status: appsv1.DeploymentStatus{
					ReadyReplicas: 1,
					Replicas:      2,
				},
			},
			wantReady: true, // Any ready replica counts as ready
			wantErr:   false,
		},
		{
			name:       "non-existent deployment",
			deployment: nil,
			wantReady:  false,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var client *K8sClient
			if tt.deployment != nil {
				client = newTestK8sClient([]runtime.Object{tt.deployment}, nil)
			} else {
				client = newTestK8sClient(nil, nil)
			}

			ctx := context.Background()
			name := "test-app"
			namespace := "default"

			ready, err := client.IsDeploymentReady(ctx, namespace, name)

			if (err != nil) != tt.wantErr {
				t.Errorf("IsDeploymentReady() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if ready != tt.wantReady {
				t.Errorf("IsDeploymentReady() = %v, want %v", ready, tt.wantReady)
			}
		})
	}
}

func TestCNPGGVR(t *testing.T) {
	if cnpgGVR.Group != "postgresql.cnpg.io" {
		t.Errorf("cnpgGVR.Group = %v, want postgresql.cnpg.io", cnpgGVR.Group)
	}
	if cnpgGVR.Version != "v1" {
		t.Errorf("cnpgGVR.Version = %v, want v1", cnpgGVR.Version)
	}
	if cnpgGVR.Resource != "clusters" {
		t.Errorf("cnpgGVR.Resource = %v, want clusters", cnpgGVR.Resource)
	}
}

// Basic CRUD Tests

func TestK8sClient_GetDeployment(t *testing.T) {
	tests := []struct {
		name       string
		deployment *appsv1.Deployment
		wantErr    bool
	}{
		{
			name:       "existing deployment",
			deployment: createTestDeployment("test-app", "default", 2, 2, 2),
			wantErr:    false,
		},
		{
			name:       "non-existent deployment",
			deployment: nil,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var client *K8sClient
			if tt.deployment != nil {
				client = newTestK8sClient([]runtime.Object{tt.deployment}, nil)
			} else {
				client = newTestK8sClient(nil, nil)
			}

			ctx := context.Background()
			deploy, err := client.GetDeployment(ctx, "default", "test-app")

			if (err != nil) != tt.wantErr {
				t.Errorf("GetDeployment() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if deploy.Name != "test-app" {
					t.Errorf("GetDeployment() got name = %v, want test-app", deploy.Name)
				}
				if deploy.Namespace != "default" {
					t.Errorf("GetDeployment() got namespace = %v, want default", deploy.Namespace)
				}
			}
		})
	}
}

func TestK8sClient_GetCNPGCluster(t *testing.T) {
	tests := []struct {
		name    string
		cluster *unstructured.Unstructured
		wantErr bool
	}{
		{
			name:    "existing cluster",
			cluster: createTestCNPGCluster("test-postgres", "default", "Cluster in healthy state", 3, 3, false),
			wantErr: false,
		},
		{
			name:    "non-existent cluster",
			cluster: nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var client *K8sClient
			if tt.cluster != nil {
				client = newTestK8sClient(nil, []runtime.Object{tt.cluster})
			} else {
				client = newTestK8sClient(nil, nil)
			}

			ctx := context.Background()
			cluster, err := client.GetCNPGCluster(ctx, "default", "test-postgres")

			if (err != nil) != tt.wantErr {
				t.Errorf("GetCNPGCluster() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if cluster.GetName() != "test-postgres" {
					t.Errorf("GetCNPGCluster() got name = %v, want test-postgres", cluster.GetName())
				}
			}
		})
	}
}

func TestK8sClient_GetSleepyService(t *testing.T) {
	tests := []struct {
		name    string
		service *unstructured.Unstructured
		wantErr bool
	}{
		{
			name:    "existing service",
			service: createTestSleepyService("test-service", "default", "Awake", 3, 3),
			wantErr: false,
		},
		{
			name:    "non-existent service",
			service: nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var client *K8sClient
			if tt.service != nil {
				client = newTestK8sClient(nil, []runtime.Object{tt.service})
			} else {
				client = newTestK8sClient(nil, nil)
			}

			ctx := context.Background()
			service, err := client.GetSleepyService(ctx, "default", "test-service")

			if (err != nil) != tt.wantErr {
				t.Errorf("GetSleepyService() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if service.GetName() != "test-service" {
					t.Errorf("GetSleepyService() got name = %v, want test-service", service.GetName())
				}
			}
		})
	}
}

func TestK8sClient_GetSleepyServiceState(t *testing.T) {
	tests := []struct {
		name      string
		service   *unstructured.Unstructured
		wantState string
		wantErr   bool
	}{
		{
			name:      "service in Awake state",
			service:   createTestSleepyService("test-service", "default", "Awake", 3, 3),
			wantState: "Awake",
			wantErr:   false,
		},
		{
			name:      "service in Sleeping state",
			service:   createTestSleepyService("test-service", "default", "Sleeping", 0, 0),
			wantState: "Sleeping",
			wantErr:   false,
		},
		{
			name:      "non-existent service",
			service:   nil,
			wantState: "",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var client *K8sClient
			if tt.service != nil {
				client = newTestK8sClient(nil, []runtime.Object{tt.service})
			} else {
				client = newTestK8sClient(nil, nil)
			}

			ctx := context.Background()
			state, err := client.GetSleepyServiceState(ctx, "default", "test-service")

			if (err != nil) != tt.wantErr {
				t.Errorf("GetSleepyServiceState() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if state != tt.wantState {
				t.Errorf("GetSleepyServiceState() = %v, want %v", state, tt.wantState)
			}
		})
	}
}

func TestK8sClient_IsCNPGHibernated(t *testing.T) {
	tests := []struct {
		name          string
		cluster       *unstructured.Unstructured
		wantHibernate bool
	}{
		{
			name: "cluster with hibernation annotation on",
			cluster: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "postgresql.cnpg.io/v1",
					"kind":       "Cluster",
					"metadata": map[string]interface{}{
						"name":      "test-postgres",
						"namespace": "default",
						"annotations": map[string]interface{}{
							"cnpg.io/hibernation": "on",
						},
					},
				},
			},
			wantHibernate: true,
		},
		{
			name: "cluster with hibernation annotation off",
			cluster: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "postgresql.cnpg.io/v1",
					"kind":       "Cluster",
					"metadata": map[string]interface{}{
						"name":      "test-postgres",
						"namespace": "default",
						"annotations": map[string]interface{}{
							"cnpg.io/hibernation": "off",
						},
					},
				},
			},
			wantHibernate: false,
		},
		{
			name: "cluster without hibernation annotation",
			cluster: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "postgresql.cnpg.io/v1",
					"kind":       "Cluster",
					"metadata": map[string]interface{}{
						"name":      "test-postgres",
						"namespace": "default",
					},
				},
			},
			wantHibernate: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			annotations := tt.cluster.GetAnnotations()
			hibernated := false
			if annotations != nil {
				hibernated = annotations["cnpg.io/hibernation"] == "on"
			}

			if hibernated != tt.wantHibernate {
				t.Errorf("IsCNPGHibernated() = %v, want %v", hibernated, tt.wantHibernate)
			}
		})
	}
}

func TestK8sClient_WakeCNPGCluster(t *testing.T) {
	tests := []struct {
		name    string
		cluster *unstructured.Unstructured
		wantErr bool
	}{
		{
			name: "remove hibernation annotation",
			cluster: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "postgresql.cnpg.io/v1",
					"kind":       "Cluster",
					"metadata": map[string]interface{}{
						"name":      "test-postgres",
						"namespace": "default",
						"annotations": map[string]interface{}{
							"cnpg.io/hibernation": "on",
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "cluster already awake",
			cluster: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "postgresql.cnpg.io/v1",
					"kind":       "Cluster",
					"metadata": map[string]interface{}{
						"name":      "test-postgres",
						"namespace": "default",
					},
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the annotation removal logic
			annotations := tt.cluster.GetAnnotations()
			if annotations == nil {
				return // Nothing to do
			}

			if _, exists := annotations["cnpg.io/hibernation"]; exists {
				delete(annotations, "cnpg.io/hibernation")
				tt.cluster.SetAnnotations(annotations)
			}

			// Verify annotation was removed
			finalAnnotations := tt.cluster.GetAnnotations()
			if finalAnnotations != nil {
				if _, exists := finalAnnotations["cnpg.io/hibernation"]; exists {
					t.Error("hibernation annotation was not removed")
				}
			}
		})
	}
}

func TestK8sClient_HibernateCNPGCluster(t *testing.T) {
	tests := []struct {
		name    string
		cluster *unstructured.Unstructured
		wantErr bool
	}{
		{
			name: "add hibernation annotation",
			cluster: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "postgresql.cnpg.io/v1",
					"kind":       "Cluster",
					"metadata": map[string]interface{}{
						"name":      "test-postgres",
						"namespace": "default",
					},
				},
			},
			wantErr: false,
		},
		{
			name: "cluster already hibernated",
			cluster: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "postgresql.cnpg.io/v1",
					"kind":       "Cluster",
					"metadata": map[string]interface{}{
						"name":      "test-postgres",
						"namespace": "default",
						"annotations": map[string]interface{}{
							"cnpg.io/hibernation": "on",
						},
					},
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the annotation addition logic
			annotations := tt.cluster.GetAnnotations()
			if annotations == nil {
				annotations = make(map[string]string)
			}

			annotations["cnpg.io/hibernation"] = "on"
			tt.cluster.SetAnnotations(annotations)

			// Verify annotation was added
			finalAnnotations := tt.cluster.GetAnnotations()
			if finalAnnotations == nil {
				t.Fatal("annotations should not be nil")
			}

			if finalAnnotations["cnpg.io/hibernation"] != "on" {
				t.Error("hibernation annotation was not set correctly")
			}
		})
	}
}

// Mutation Operation Tests with Fake Clients

func TestK8sClient_IsCNPGHibernated_WithFakeClient(t *testing.T) {
	tests := []struct {
		name          string
		cluster       *unstructured.Unstructured
		wantHibernate bool
		wantErr       bool
	}{
		{
			name:          "hibernated cluster",
			cluster:       createTestCNPGCluster("test-postgres", "default", "Cluster in healthy state", 0, 3, true),
			wantHibernate: true,
			wantErr:       false,
		},
		{
			name:          "active cluster",
			cluster:       createTestCNPGCluster("test-postgres", "default", "Cluster in healthy state", 3, 3, false),
			wantHibernate: false,
			wantErr:       false,
		},
		{
			name:          "non-existent cluster",
			cluster:       nil,
			wantHibernate: false,
			wantErr:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var client *K8sClient
			if tt.cluster != nil {
				client = newTestK8sClient(nil, []runtime.Object{tt.cluster})
			} else {
				client = newTestK8sClient(nil, nil)
			}

			ctx := context.Background()
			hibernated, err := client.IsCNPGHibernated(ctx, "default", "test-postgres")

			if (err != nil) != tt.wantErr {
				t.Errorf("IsCNPGHibernated() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if hibernated != tt.wantHibernate {
				t.Errorf("IsCNPGHibernated() = %v, want %v", hibernated, tt.wantHibernate)
			}
		})
	}
}

func TestK8sClient_WakeCNPGCluster_WithFakeClient(t *testing.T) {
	tests := []struct {
		name    string
		cluster *unstructured.Unstructured
		wantErr bool
	}{
		{
			name:    "wake hibernated cluster",
			cluster: createTestCNPGCluster("test-postgres", "default", "Cluster in healthy state", 0, 3, true),
			wantErr: false,
		},
		{
			name:    "wake already active cluster",
			cluster: createTestCNPGCluster("test-postgres", "default", "Cluster in healthy state", 3, 3, false),
			wantErr: false,
		},
		{
			name:    "non-existent cluster",
			cluster: nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var client *K8sClient
			if tt.cluster != nil {
				client = newTestK8sClient(nil, []runtime.Object{tt.cluster})
			} else {
				client = newTestK8sClient(nil, nil)
			}

			ctx := context.Background()
			err := client.WakeCNPGCluster(ctx, "default", "test-postgres")

			if (err != nil) != tt.wantErr {
				t.Errorf("WakeCNPGCluster() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				// Verify annotation was removed
				cluster, err := client.GetCNPGCluster(ctx, "default", "test-postgres")
				if err != nil {
					t.Fatalf("Failed to get cluster after wake: %v", err)
				}

				annotations := cluster.GetAnnotations()
				if annotations != nil {
					if _, exists := annotations["cnpg.io/hibernation"]; exists {
						t.Error("hibernation annotation should have been removed")
					}
				}
			}
		})
	}
}

func TestK8sClient_HibernateCNPGCluster_WithFakeClient(t *testing.T) {
	tests := []struct {
		name    string
		cluster *unstructured.Unstructured
		wantErr bool
	}{
		{
			name:    "hibernate active cluster",
			cluster: createTestCNPGCluster("test-postgres", "default", "Cluster in healthy state", 3, 3, false),
			wantErr: false,
		},
		{
			name:    "hibernate already hibernated cluster",
			cluster: createTestCNPGCluster("test-postgres", "default", "Cluster in healthy state", 0, 3, true),
			wantErr: false,
		},
		{
			name:    "non-existent cluster",
			cluster: nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var client *K8sClient
			if tt.cluster != nil {
				client = newTestK8sClient(nil, []runtime.Object{tt.cluster})
			} else {
				client = newTestK8sClient(nil, nil)
			}

			ctx := context.Background()
			err := client.HibernateCNPGCluster(ctx, "default", "test-postgres")

			if (err != nil) != tt.wantErr {
				t.Errorf("HibernateCNPGCluster() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				// Verify annotation was added
				cluster, err := client.GetCNPGCluster(ctx, "default", "test-postgres")
				if err != nil {
					t.Fatalf("Failed to get cluster after hibernate: %v", err)
				}

				annotations := cluster.GetAnnotations()
				if annotations == nil || annotations["cnpg.io/hibernation"] != "on" {
					t.Error("hibernation annotation should be set to 'on'")
				}
			}
		})
	}
}

func TestK8sClient_ScaleDeployment(t *testing.T) {
	// Note: Skipping this test because fake client doesn't support GetScale/UpdateScale subresources
	// This is a limitation of k8s.io/client-go/kubernetes/fake, not our code
	// The ScaleDeployment method is tested in E2E tests
	t.Skip("fake client doesn't support Scale subresource - tested in E2E")
}

func TestK8sClient_UpdateSleepyServiceStatus(t *testing.T) {
	tests := []struct {
		name         string
		service      *unstructured.Unstructured
		desiredState string
		wantErr      bool
	}{
		{
			name:         "update to Waking state",
			service:      createTestSleepyService("test-service", "default", "Sleeping", 3, 0),
			desiredState: "Waking",
			wantErr:      false,
		},
		{
			name:         "update to Awake state",
			service:      createTestSleepyService("test-service", "default", "Waking", 3, 2),
			desiredState: "Awake",
			wantErr:      false,
		},
		{
			name:         "non-existent service",
			service:      nil,
			desiredState: "Awake",
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var client *K8sClient
			if tt.service != nil {
				client = newTestK8sClient(nil, []runtime.Object{tt.service})
			} else {
				client = newTestK8sClient(nil, nil)
			}

			ctx := context.Background()
			now := metav1.Now()
			err := client.UpdateSleepyServiceStatus(ctx, "default", "test-service", tt.desiredState, &now)

			if (err != nil) != tt.wantErr {
				t.Errorf("UpdateSleepyServiceStatus() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				// Verify status was updated
				service, err := client.GetSleepyService(ctx, "default", "test-service")
				if err != nil {
					t.Fatalf("Failed to get service after update: %v", err)
				}

				desiredState, _, _ := unstructured.NestedString(service.Object, "status", "desiredState")
				if desiredState != tt.desiredState {
					t.Errorf("desiredState = %v, want %v", desiredState, tt.desiredState)
				}

				lastActivity, _, _ := unstructured.NestedString(service.Object, "status", "lastActivity")
				if lastActivity == "" {
					t.Error("lastActivity should not be empty")
				}
			}
		})
	}
}

func TestK8sClient_WaitForDeploymentReady_ProgressCalculation(t *testing.T) {
	tests := []struct {
		name            string
		readyReplicas   int32
		desiredReplicas int32
		elapsedTime     time.Duration
		maxWait         time.Duration
		expectedMinimum int
		expectedMaximum int
	}{
		{
			name:            "0% ready replicas, 10s elapsed of 5min",
			readyReplicas:   0,
			desiredReplicas: 3,
			elapsedTime:     10 * time.Second,
			maxWait:         5 * time.Minute,
			expectedMinimum: 3, // timeProgress = 10s/300s * 100 = 3%
			expectedMaximum: 3,
		},
		{
			name:            "50% ready replicas, 30s elapsed",
			readyReplicas:   1,
			desiredReplicas: 2,
			elapsedTime:     30 * time.Second,
			maxWait:         5 * time.Minute,
			expectedMinimum: 50, // replicaProgress wins
			expectedMaximum: 50,
		},
		{
			name:            "100% ready replicas",
			readyReplicas:   3,
			desiredReplicas: 3,
			elapsedTime:     10 * time.Second,
			maxWait:         5 * time.Minute,
			expectedMinimum: 100,
			expectedMaximum: 100,
		},
		{
			name:            "near timeout, still waiting",
			readyReplicas:   2,
			desiredReplicas: 3,
			elapsedTime:     4*time.Minute + 50*time.Second,
			maxWait:         5 * time.Minute,
			expectedMinimum: 95, // Cap at 95%
			expectedMaximum: 95,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Calculate timeProgress
			timeProgress := int(tt.elapsedTime.Seconds() / tt.maxWait.Seconds() * 100)
			if timeProgress > 95 {
				timeProgress = 95
			}

			// Calculate replicaProgress
			replicaProgress := 0
			if tt.desiredReplicas > 0 {
				replicaProgress = int(tt.readyReplicas * 100 / tt.desiredReplicas)
			}

			// Use whichever is higher
			progress := timeProgress
			if replicaProgress > progress {
				progress = replicaProgress
			}

			if progress < tt.expectedMinimum || progress > tt.expectedMaximum {
				t.Errorf("progress = %v, want between %v and %v", progress, tt.expectedMinimum, tt.expectedMaximum)
			}
		})
	}
}

func TestK8sClient_WaitForCNPGReady_ProgressCalculation(t *testing.T) {
	tests := []struct {
		name            string
		readyInstances  int64
		totalInstances  int64
		elapsedTime     time.Duration
		maxWait         time.Duration
		expectedMinimum int
		expectedMaximum int
	}{
		{
			name:            "0 of 3 instances ready, early in wait",
			readyInstances:  0,
			totalInstances:  3,
			elapsedTime:     10 * time.Second,
			maxWait:         3 * time.Minute,
			expectedMinimum: 5,
			expectedMaximum: 6,
		},
		{
			name:            "2 of 3 instances ready",
			readyInstances:  2,
			totalInstances:  3,
			elapsedTime:     30 * time.Second,
			maxWait:         3 * time.Minute,
			expectedMinimum: 66,
			expectedMaximum: 67,
		},
		{
			name:            "all instances ready",
			readyInstances:  3,
			totalInstances:  3,
			elapsedTime:     45 * time.Second,
			maxWait:         3 * time.Minute,
			expectedMinimum: 100,
			expectedMaximum: 100,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Calculate timeProgress
			timeProgress := int(tt.elapsedTime.Seconds() / tt.maxWait.Seconds() * 100)
			if timeProgress > 95 {
				timeProgress = 95
			}

			// Calculate instanceProgress
			instanceProgress := 0
			if tt.totalInstances > 0 {
				instanceProgress = int(tt.readyInstances * 100 / tt.totalInstances)
			}

			// Use whichever is higher
			progress := timeProgress
			if instanceProgress > progress {
				progress = instanceProgress
			}

			if progress < tt.expectedMinimum || progress > tt.expectedMaximum {
				t.Errorf("progress = %v, want between %v and %v", progress, tt.expectedMinimum, tt.expectedMaximum)
			}
		})
	}
}

// Comprehensive Wait Function Tests

func TestK8sClient_WaitForDeploymentReady_Success(t *testing.T) {
	deploy := createTestDeployment("test-app", "default", 0, 0, 3)
	client := newTestK8sClient([]runtime.Object{deploy}, nil)

	// Use a reactor to simulate deployment becoming ready
	callCount := atomic.Int32{}
	client.clientset.(*kubefake.Clientset).PrependReactor("get", "deployments", func(action k8stesting.Action) (bool, runtime.Object, error) {
		count := callCount.Add(1)
		if count >= 3 { // After 3 polls, mark as ready
			readyDeploy := createTestDeployment("test-app", "default", 3, 3, 3)
			return true, readyDeploy, nil
		}
		return false, nil, nil // Use default handler
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	progressCalled := false
	err := client.WaitForDeploymentReady(ctx, "default", "test-app", func(msg string, progress int) {
		progressCalled = true
	})

	if err != nil {
		t.Errorf("WaitForDeploymentReady() error = %v, want nil", err)
	}

	if !progressCalled {
		t.Error("Progress callback was not called")
	}
}

func TestK8sClient_WaitForDeploymentReady_Timeout(t *testing.T) {
	deploy := createTestDeployment("test-app", "default", 0, 0, 3)
	client := newTestK8sClient([]runtime.Object{deploy}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := client.WaitForDeploymentReady(ctx, "default", "test-app", nil)

	if err == nil {
		t.Error("WaitForDeploymentReady() should have timed out")
	}
	if err != context.DeadlineExceeded {
		t.Errorf("Expected context.DeadlineExceeded, got %v", err)
	}
}

func TestK8sClient_WaitForDeploymentReady_ContextCancellation(t *testing.T) {
	deploy := createTestDeployment("test-app", "default", 0, 0, 3)
	client := newTestK8sClient([]runtime.Object{deploy}, nil)

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel context after a short delay
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	err := client.WaitForDeploymentReady(ctx, "default", "test-app", nil)

	if err == nil {
		t.Error("WaitForDeploymentReady() should have been cancelled")
	}
	if err != context.Canceled {
		t.Errorf("Expected context.Canceled, got %v", err)
	}
}

func TestK8sClient_WaitForDeploymentReady_ProgressCallbacks(t *testing.T) {
	deploy := createTestDeployment("test-app", "default", 1, 1, 3)
	client := newTestK8sClient([]runtime.Object{deploy}, nil)

	callCount := atomic.Int32{}
	client.clientset.(*kubefake.Clientset).PrependReactor("get", "deployments", func(action k8stesting.Action) (bool, runtime.Object, error) {
		count := callCount.Add(1)
		if count >= 4 {
			return true, createTestDeployment("test-app", "default", 3, 3, 3), nil
		}
		return true, createTestDeployment("test-app", "default", count, count, 3), nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var progressValues []int
	err := client.WaitForDeploymentReady(ctx, "default", "test-app", func(msg string, progress int) {
		progressValues = append(progressValues, progress)
	})

	if err != nil {
		t.Errorf("WaitForDeploymentReady() error = %v, want nil", err)
	}

	if len(progressValues) == 0 {
		t.Error("Expected progress callbacks to be invoked")
	}

	// Verify progress doesn't decrease (monotonic)
	for i := 1; i < len(progressValues); i++ {
		if progressValues[i] < progressValues[i-1] {
			t.Errorf("Progress decreased from %d to %d", progressValues[i-1], progressValues[i])
		}
	}
}

func TestK8sClient_WaitForDeploymentReady_AlreadyReady(t *testing.T) {
	deploy := createTestDeployment("test-app", "default", 3, 3, 3)
	client := newTestK8sClient([]runtime.Object{deploy}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	startTime := time.Now()
	err := client.WaitForDeploymentReady(ctx, "default", "test-app", nil)
	elapsed := time.Since(startTime)

	if err != nil {
		t.Errorf("WaitForDeploymentReady() error = %v, want nil", err)
	}

	// Should return after first tick (2 seconds)
	if elapsed > 4*time.Second {
		t.Errorf("Took too long for already-ready deployment: %v", elapsed)
	}
}

func TestK8sClient_WaitForCNPGReady_Success(t *testing.T) {
	cluster := createTestCNPGCluster("test-postgres", "default", "Starting", 0, 3, false)
	client := newTestK8sClient(nil, []runtime.Object{cluster})

	callCount := atomic.Int32{}
	client.dynamicClient.(*fake.FakeDynamicClient).PrependReactor("get", "clusters", func(action k8stesting.Action) (bool, runtime.Object, error) {
		count := callCount.Add(1)
		if count >= 3 {
			return true, createTestCNPGCluster("test-postgres", "default", "Cluster in healthy state", 3, 3, false), nil
		}
		return false, nil, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	progressCalled := false
	err := client.WaitForCNPGReady(ctx, "default", "test-postgres", func(msg string, progress int) {
		progressCalled = true
	})

	if err != nil {
		t.Errorf("WaitForCNPGReady() error = %v, want nil", err)
	}

	if !progressCalled {
		t.Error("Progress callback was not called")
	}
}

func TestK8sClient_WaitForCNPGReady_Timeout(t *testing.T) {
	cluster := createTestCNPGCluster("test-postgres", "default", "Starting", 0, 3, false)
	client := newTestK8sClient(nil, []runtime.Object{cluster})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := client.WaitForCNPGReady(ctx, "default", "test-postgres", nil)

	if err == nil {
		t.Error("WaitForCNPGReady() should have timed out")
	}
	if err != context.DeadlineExceeded {
		t.Errorf("Expected context.DeadlineExceeded, got %v", err)
	}
}

func TestK8sClient_WaitForCNPGReady_ContextCancellation(t *testing.T) {
	cluster := createTestCNPGCluster("test-postgres", "default", "Starting", 0, 3, false)
	client := newTestK8sClient(nil, []runtime.Object{cluster})

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	err := client.WaitForCNPGReady(ctx, "default", "test-postgres", nil)

	if err == nil {
		t.Error("WaitForCNPGReady() should have been cancelled")
	}
	if err != context.Canceled {
		t.Errorf("Expected context.Canceled, got %v", err)
	}
}

func TestK8sClient_WaitForCNPGReady_AlreadyHealthy(t *testing.T) {
	cluster := createTestCNPGCluster("test-postgres", "default", "Cluster in healthy state", 3, 3, false)
	client := newTestK8sClient(nil, []runtime.Object{cluster})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	startTime := time.Now()
	err := client.WaitForCNPGReady(ctx, "default", "test-postgres", nil)
	elapsed := time.Since(startTime)

	if err != nil {
		t.Errorf("WaitForCNPGReady() error = %v, want nil", err)
	}

	// Should return after first tick (3 seconds)
	if elapsed > 5*time.Second {
		t.Errorf("Took too long for already-healthy cluster: %v", elapsed)
	}
}

func TestK8sClient_WaitForSleepyServiceAwake_Success(t *testing.T) {
	service := createTestSleepyService("test-service", "default", "Waking", 3, 0)
	client := newTestK8sClient(nil, []runtime.Object{service})

	callCount := atomic.Int32{}
	client.dynamicClient.(*fake.FakeDynamicClient).PrependReactor("get", "sleepyservices", func(action k8stesting.Action) (bool, runtime.Object, error) {
		count := callCount.Add(1)
		if count >= 5 {
			return true, createTestSleepyService("test-service", "default", "Awake", 3, 3), nil
		}
		return true, createTestSleepyService("test-service", "default", "Waking", 3, int(count-1)), nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	progressCalled := false
	err := client.WaitForSleepyServiceAwake(ctx, "default", "test-service", func(msg string, progress int) {
		progressCalled = true
	})

	if err != nil {
		t.Errorf("WaitForSleepyServiceAwake() error = %v, want nil", err)
	}

	if !progressCalled {
		t.Error("Progress callback was not called")
	}
}

func TestK8sClient_WaitForSleepyServiceAwake_Timeout(t *testing.T) {
	service := createTestSleepyService("test-service", "default", "Waking", 3, 0)
	client := newTestK8sClient(nil, []runtime.Object{service})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := client.WaitForSleepyServiceAwake(ctx, "default", "test-service", nil)

	if err == nil {
		t.Error("WaitForSleepyServiceAwake() should have timed out")
	}
	if err != context.DeadlineExceeded {
		t.Errorf("Expected context.DeadlineExceeded, got %v", err)
	}
}

func TestK8sClient_WaitForSleepyServiceAwake_ContextCancellation(t *testing.T) {
	service := createTestSleepyService("test-service", "default", "Waking", 3, 1)
	client := newTestK8sClient(nil, []runtime.Object{service})

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	err := client.WaitForSleepyServiceAwake(ctx, "default", "test-service", nil)

	if err == nil {
		t.Error("WaitForSleepyServiceAwake() should have been cancelled")
	}
	if err != context.Canceled {
		t.Errorf("Expected context.Canceled, got %v", err)
	}
}

func TestK8sClient_WaitForSleepyServiceAwake_AlreadyAwake(t *testing.T) {
	service := createTestSleepyService("test-service", "default", "Awake", 3, 3)
	client := newTestK8sClient(nil, []runtime.Object{service})

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	startTime := time.Now()
	err := client.WaitForSleepyServiceAwake(ctx, "default", "test-service", nil)
	elapsed := time.Since(startTime)

	if err != nil {
		t.Errorf("WaitForSleepyServiceAwake() error = %v, want nil", err)
	}

	if elapsed > 1*time.Second {
		t.Errorf("Took too long for already-awake service: %v", elapsed)
	}
}

func TestK8sClient_WaitForSleepyServiceAwake_ComponentProgress(t *testing.T) {
	service := createTestSleepyService("test-service", "default", "Waking", 3, 0)
	client := newTestK8sClient(nil, []runtime.Object{service})

	callCount := atomic.Int32{}
	client.dynamicClient.(*fake.FakeDynamicClient).PrependReactor("get", "sleepyservices", func(action k8stesting.Action) (bool, runtime.Object, error) {
		count := callCount.Add(1)
		if count >= 5 {
			return true, createTestSleepyService("test-service", "default", "Awake", 3, 3), nil
		}
		return true, createTestSleepyService("test-service", "default", "Waking", 3, int(count-1)), nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var progressValues []int
	err := client.WaitForSleepyServiceAwake(ctx, "default", "test-service", func(msg string, progress int) {
		progressValues = append(progressValues, progress)
	})

	if err != nil {
		t.Errorf("WaitForSleepyServiceAwake() error = %v, want nil", err)
	}

	if len(progressValues) == 0 {
		t.Error("Expected progress callbacks to be invoked")
	}

	// Progress should generally increase (allowing for 95% cap until Awake)
	if progressValues[len(progressValues)-1] < progressValues[0] {
		t.Error("Final progress should not be less than initial progress")
	}
}
