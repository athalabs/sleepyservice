// Copyright 2026 AthaLabs
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
)

const (
	testModifiedValue = "modified"
)

// =============================================================================
// Phase 1: Test Infrastructure - Helper Functions
// =============================================================================

// createTestSleepyServiceSpec creates a fully populated SleepyServiceSpec for testing
func createTestSleepyServiceSpec() *SleepyServiceSpec {
	return &SleepyServiceSpec{
		HealthPath:  "/health",
		WakeTimeout: metav1.Duration{Duration: 5 * time.Minute},
		IdleTimeout: metav1.Duration{Duration: 10 * time.Minute},
		BackendService: &BackendServiceSpec{
			Enabled:         ptr.To(true),
			Type:            corev1.ServiceTypeLoadBalancer,
			ClusterIP:       "10.0.0.1",
			ExternalIPs:     []string{"1.2.3.4", "5.6.7.8"},
			LoadBalancerIP:  "9.10.11.12",
			SessionAffinity: corev1.ServiceAffinityClientIP,
			Annotations:     map[string]string{"key": "value", "foo": "bar"},
			Ports: []ServicePort{
				{
					Name:       "http",
					Protocol:   corev1.ProtocolTCP,
					Port:       80,
					TargetPort: intstr.FromInt(8080),
					NodePort:   30080,
				},
				{
					Name:       "https",
					Protocol:   corev1.ProtocolTCP,
					Port:       443,
					TargetPort: intstr.FromString("https"),
					NodePort:   30443,
				},
			},
		},
		Components: []Component{
			{
				Name:      "app",
				Type:      ComponentTypeDeployment,
				Replicas:  ptr.To(int32(3)),
				DependsOn: []string{"db"},
				Ref: ResourceRef{
					Name:      "my-app",
					Namespace: "default",
				},
			},
			{
				Name:      "db",
				Type:      ComponentTypeStatefulSet,
				Replicas:  ptr.To(int32(1)),
				DependsOn: nil,
				Ref: ResourceRef{
					Name:      "postgres",
					Namespace: "default",
				},
			},
		},
	}
}

// createTestBackendServiceSpec creates a fully populated BackendServiceSpec
func createTestBackendServiceSpec() *BackendServiceSpec {
	return &BackendServiceSpec{
		Enabled:         ptr.To(true),
		Type:            corev1.ServiceTypeClusterIP,
		ClusterIP:       "10.96.0.1",
		ExternalIPs:     []string{"203.0.113.1"},
		LoadBalancerIP:  "198.51.100.1",
		SessionAffinity: corev1.ServiceAffinityNone,
		Annotations:     map[string]string{"service.beta.kubernetes.io/aws-load-balancer-type": "nlb"},
		Ports: []ServicePort{
			{Name: "web", Protocol: corev1.ProtocolTCP, Port: 8080, TargetPort: intstr.FromInt(8080)},
		},
	}
}

// createTestComponent creates a fully populated Component
func createTestComponent() *Component {
	return &Component{
		Name:      "test-component",
		Type:      ComponentTypeDeployment,
		Replicas:  ptr.To(int32(2)),
		DependsOn: []string{"dependency1", "dependency2"},
		Ref: ResourceRef{
			Name:      "test-deployment",
			Namespace: "test-namespace",
		},
	}
}

// createTestSleepyService creates a fully populated SleepyService
func createTestSleepyService() *SleepyService {
	now := metav1.Now()
	return &SleepyService{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "sleepy.atha.gr/v1alpha1",
			Kind:       "SleepyService",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test-service",
			Namespace:         "default",
			UID:               "test-uid-12345",
			ResourceVersion:   "12345",
			Generation:        1,
			CreationTimestamp: now,
			Labels: map[string]string{
				"app":     "sleepy",
				"version": "v1",
			},
			Annotations: map[string]string{
				"description": "Test service",
			},
		},
		Spec:   *createTestSleepyServiceSpec(),
		Status: *createTestSleepyServiceStatus(),
	}
}

// createTestSleepyServiceStatus creates a fully populated SleepyServiceStatus
func createTestSleepyServiceStatus() *SleepyServiceStatus {
	now := metav1.Now()
	return &SleepyServiceStatus{
		State:           StateAwake,
		DesiredState:    StateAwake,
		LastTransition:  &now,
		LastActivity:    &now,
		ProxyDeployment: "test-proxy",
		BackendService:  "test-backend-svc",
		Components: []ComponentStatus{
			{Name: "app", Ready: true, Message: "Running"},
			{Name: "db", Ready: true, Message: "Ready"},
		},
		Conditions: []metav1.Condition{
			{
				Type:               "Ready",
				Status:             metav1.ConditionTrue,
				ObservedGeneration: 1,
				LastTransitionTime: now,
				Reason:             "AllReady",
				Message:            "All components ready",
			},
		},
	}
}

// createTestSleepyServiceList creates a SleepyServiceList with multiple items
func createTestSleepyServiceList() *SleepyServiceList {
	return &SleepyServiceList{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "sleepy.atha.gr/v1alpha1",
			Kind:       "SleepyServiceList",
		},
		ListMeta: metav1.ListMeta{
			ResourceVersion: "12345",
			Continue:        "token",
		},
		Items: []SleepyService{
			*createTestSleepyService(),
			{
				ObjectMeta: metav1.ObjectMeta{Name: "service-2", Namespace: "default"},
				Spec:       SleepyServiceSpec{HealthPath: "/status", Components: []Component{{Name: "api"}}},
			},
		},
	}
}

// =============================================================================
// Phase 2: DeepCopy Tests
// =============================================================================

func TestBackendServiceSpec_DeepCopy(t *testing.T) {
	tests := []struct {
		name     string
		source   *BackendServiceSpec
		validate func(t *testing.T, src, copy *BackendServiceSpec)
	}{
		{
			name:   "full spec with all fields",
			source: createTestBackendServiceSpec(),
			validate: func(t *testing.T, src, copy *BackendServiceSpec) {
				if *copy.Enabled != *src.Enabled {
					t.Error("Enabled field not copied correctly")
				}
				if copy.Type != src.Type {
					t.Error("Type field not copied correctly")
				}
				if copy.ClusterIP != src.ClusterIP {
					t.Error("ClusterIP not copied correctly")
				}
				// Verify pointer independence
				if copy.Enabled == src.Enabled {
					t.Error("Enabled pointer should be independent")
				}
				// Verify slice independence
				if len(copy.ExternalIPs) > 0 && &copy.ExternalIPs[0] == &src.ExternalIPs[0] {
					t.Error("ExternalIPs slice shares backing array")
				}
				// Verify map independence
				if len(copy.Annotations) > 0 && &copy.Annotations == &src.Annotations {
					t.Error("Annotations map should be independent")
				}
			},
		},
		{
			name:   "empty spec",
			source: &BackendServiceSpec{},
			validate: func(t *testing.T, src, copy *BackendServiceSpec) {
				if copy == nil {
					t.Error("DeepCopy should not return nil for non-nil source")
				}
			},
		},
		{
			name:   "nil pointer fields",
			source: &BackendServiceSpec{Enabled: nil, Ports: nil, Annotations: nil},
			validate: func(t *testing.T, src, copy *BackendServiceSpec) {
				if copy.Enabled != nil {
					t.Error("Nil Enabled pointer should remain nil")
				}
				if copy.Ports != nil {
					t.Error("Nil Ports should remain nil")
				}
				if copy.Annotations != nil {
					t.Error("Nil Annotations should remain nil")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			copy := tt.source.DeepCopy()
			if copy == nil {
				t.Fatal("DeepCopy returned nil for non-nil source")
			}
			if copy == tt.source {
				t.Error("DeepCopy returned same pointer")
			}
			tt.validate(t, tt.source, copy)
		})
	}
}

func TestComponent_DeepCopy(t *testing.T) {
	tests := []struct {
		name     string
		source   *Component
		validate func(t *testing.T, src, copy *Component)
	}{
		{
			name:   "full component",
			source: createTestComponent(),
			validate: func(t *testing.T, src, copy *Component) {
				if copy.Name != src.Name {
					t.Error("Name not copied")
				}
				if copy.Type != src.Type {
					t.Error("Type not copied")
				}
				if *copy.Replicas != *src.Replicas {
					t.Error("Replicas value not copied")
				}
				if copy.Replicas == src.Replicas {
					t.Error("Replicas pointer should be independent")
				}
				if len(copy.DependsOn) > 0 && &copy.DependsOn[0] == &src.DependsOn[0] {
					t.Error("DependsOn slice shares backing array")
				}
			},
		},
		{
			name:   "component with nil replicas",
			source: &Component{Name: "test", Type: ComponentTypeDeployment, Replicas: nil},
			validate: func(t *testing.T, src, copy *Component) {
				if copy.Replicas != nil {
					t.Error("Nil Replicas should remain nil")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			copy := tt.source.DeepCopy()
			if copy == nil {
				t.Fatal("DeepCopy returned nil")
			}
			tt.validate(t, tt.source, copy)
		})
	}
}

func TestComponentStatus_DeepCopy(t *testing.T) {
	source := &ComponentStatus{
		Name:    "test-component",
		Ready:   true,
		Message: "Component is ready",
	}

	copy := source.DeepCopy()

	if copy == nil {
		t.Fatal("DeepCopy returned nil")
	}
	if copy == source {
		t.Error("DeepCopy returned same pointer")
	}
	if copy.Name != source.Name || copy.Ready != source.Ready || copy.Message != source.Message {
		t.Error("Fields not copied correctly")
	}
}

func TestResourceRef_DeepCopy(t *testing.T) {
	source := &ResourceRef{
		Name:      "my-resource",
		Namespace: "my-namespace",
	}

	copy := source.DeepCopy()

	if copy == nil {
		t.Fatal("DeepCopy returned nil")
	}
	if copy == source {
		t.Error("DeepCopy returned same pointer")
	}
	if copy.Name != source.Name || copy.Namespace != source.Namespace {
		t.Error("Fields not copied correctly")
	}
}

func TestServicePort_DeepCopy(t *testing.T) {
	tests := []struct {
		name   string
		source *ServicePort
	}{
		{
			name: "full port with int target",
			source: &ServicePort{
				Name:       "http",
				Protocol:   corev1.ProtocolTCP,
				Port:       80,
				TargetPort: intstr.FromInt(8080),
				NodePort:   30080,
			},
		},
		{
			name: "port with string target",
			source: &ServicePort{
				Name:       "https",
				Protocol:   corev1.ProtocolTCP,
				Port:       443,
				TargetPort: intstr.FromString("https"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			copy := tt.source.DeepCopy()
			if copy == nil {
				t.Fatal("DeepCopy returned nil")
			}
			if copy == tt.source {
				t.Error("DeepCopy returned same pointer")
			}
			if copy.Name != tt.source.Name {
				t.Error("Name not copied")
			}
			if copy.Port != tt.source.Port {
				t.Error("Port not copied")
			}
		})
	}
}

func TestSleepyServiceSpec_DeepCopy(t *testing.T) {
	tests := []struct {
		name     string
		source   *SleepyServiceSpec
		validate func(t *testing.T, src, copy *SleepyServiceSpec)
	}{
		{
			name:   "full spec",
			source: createTestSleepyServiceSpec(),
			validate: func(t *testing.T, src, copy *SleepyServiceSpec) {
				if copy.HealthPath != src.HealthPath {
					t.Error("HealthPath not copied")
				}
				if copy.BackendService == src.BackendService {
					t.Error("BackendService pointer not deep copied")
				}
				if len(copy.Components) > 0 && &copy.Components[0] == &src.Components[0] {
					t.Error("Components slice shares backing array")
				}
			},
		},
		{
			name:   "empty spec",
			source: &SleepyServiceSpec{},
			validate: func(t *testing.T, src, copy *SleepyServiceSpec) {
				if copy == nil {
					t.Error("Empty spec should still create new instance")
				}
			},
		},
		{
			name: "nil backend service",
			source: &SleepyServiceSpec{
				HealthPath:     "/health",
				BackendService: nil,
				Components:     []Component{{Name: "app"}},
			},
			validate: func(t *testing.T, src, copy *SleepyServiceSpec) {
				if copy.BackendService != nil {
					t.Error("Nil BackendService should remain nil")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			copy := tt.source.DeepCopy()
			if copy == nil {
				t.Fatal("DeepCopy returned nil")
			}
			tt.validate(t, tt.source, copy)
		})
	}
}

func TestSleepyServiceStatus_DeepCopy(t *testing.T) {
	tests := []struct {
		name     string
		source   *SleepyServiceStatus
		validate func(t *testing.T, src, copy *SleepyServiceStatus)
	}{
		{
			name:   "full status",
			source: createTestSleepyServiceStatus(),
			validate: func(t *testing.T, src, copy *SleepyServiceStatus) {
				if copy.State != src.State {
					t.Error("State not copied")
				}
				if copy.LastTransition == src.LastTransition {
					t.Error("LastTransition pointer should be independent")
				}
				if len(copy.Components) > 0 && &copy.Components[0] == &src.Components[0] {
					t.Error("Components slice shares backing array")
				}
			},
		},
		{
			name: "nil time pointers",
			source: &SleepyServiceStatus{
				State:          StateSleeping,
				LastTransition: nil,
				LastActivity:   nil,
			},
			validate: func(t *testing.T, src, copy *SleepyServiceStatus) {
				if copy.LastTransition != nil {
					t.Error("Nil LastTransition should remain nil")
				}
				if copy.LastActivity != nil {
					t.Error("Nil LastActivity should remain nil")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			copy := tt.source.DeepCopy()
			if copy == nil {
				t.Fatal("DeepCopy returned nil")
			}
			tt.validate(t, tt.source, copy)
		})
	}
}

func TestSleepyService_DeepCopy(t *testing.T) {
	tests := []struct {
		name   string
		source *SleepyService
	}{
		{
			name:   "full sleepy service",
			source: createTestSleepyService(),
		},
		{
			name: "minimal sleepy service",
			source: &SleepyService{
				ObjectMeta: metav1.ObjectMeta{Name: "minimal", Namespace: "default"},
				Spec:       SleepyServiceSpec{Components: []Component{{Name: "app"}}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			copy := tt.source.DeepCopy()
			if copy == nil {
				t.Fatal("DeepCopy returned nil")
			}
			if copy == tt.source {
				t.Error("DeepCopy returned same pointer")
			}
			if copy.Name != tt.source.Name {
				t.Error("Name not copied")
			}
			if &copy.Spec == &tt.source.Spec {
				t.Error("Spec should be deep copied")
			}
		})
	}
}

func TestSleepyServiceList_DeepCopy(t *testing.T) {
	source := createTestSleepyServiceList()

	copy := source.DeepCopy()

	if copy == nil {
		t.Fatal("DeepCopy returned nil")
	}
	if copy == source {
		t.Error("DeepCopy returned same pointer")
	}
	if len(copy.Items) != len(source.Items) {
		t.Error("Items count not preserved")
	}
	if len(copy.Items) > 0 && &copy.Items[0] == &source.Items[0] {
		t.Error("Items slice shares backing array")
	}
}

// =============================================================================
// Phase 3: DeepCopyInto Tests
// =============================================================================

func TestBackendServiceSpec_DeepCopyInto(t *testing.T) {
	tests := []struct {
		name     string
		source   *BackendServiceSpec
		dest     *BackendServiceSpec
		validate func(t *testing.T, src, dest *BackendServiceSpec)
	}{
		{
			name:   "copy into fresh destination",
			source: createTestBackendServiceSpec(),
			dest:   &BackendServiceSpec{},
			validate: func(t *testing.T, src, dest *BackendServiceSpec) {
				if *dest.Enabled != *src.Enabled {
					t.Error("Enabled not copied")
				}
				if dest.Enabled == src.Enabled {
					t.Error("Enabled pointer should be independent")
				}
				if len(dest.Ports) != len(src.Ports) {
					t.Error("Ports length mismatch")
				}
			},
		},
		{
			name:   "copy over existing data",
			source: &BackendServiceSpec{Enabled: ptr.To(true), ClusterIP: "10.0.0.1"},
			dest:   &BackendServiceSpec{Enabled: ptr.To(false), ClusterIP: "10.0.0.2"},
			validate: func(t *testing.T, src, dest *BackendServiceSpec) {
				if *dest.Enabled != true {
					t.Error("Source should override destination")
				}
				if dest.ClusterIP != "10.0.0.1" {
					t.Error("ClusterIP should be overridden")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.source.DeepCopyInto(tt.dest)
			tt.validate(t, tt.source, tt.dest)
		})
	}
}

func TestComponent_DeepCopyInto(t *testing.T) {
	source := createTestComponent()
	dest := &Component{}

	source.DeepCopyInto(dest)

	if dest.Name != source.Name {
		t.Error("Name not copied")
	}
	if dest.Type != source.Type {
		t.Error("Type not copied")
	}
	if dest.Replicas == source.Replicas {
		t.Error("Replicas pointer should be independent")
	}
	if *dest.Replicas != *source.Replicas {
		t.Error("Replicas value not copied")
	}
}

func TestComponentStatus_DeepCopyInto(t *testing.T) {
	source := &ComponentStatus{Name: "test", Ready: true, Message: "OK"}
	dest := &ComponentStatus{}

	source.DeepCopyInto(dest)

	if dest.Name != source.Name || dest.Ready != source.Ready || dest.Message != source.Message {
		t.Error("Fields not copied correctly")
	}
}

func TestResourceRef_DeepCopyInto(t *testing.T) {
	source := &ResourceRef{Name: "my-res", Namespace: "my-ns"}
	dest := &ResourceRef{}

	source.DeepCopyInto(dest)

	if dest.Name != source.Name || dest.Namespace != source.Namespace {
		t.Error("Fields not copied correctly")
	}
}

func TestServicePort_DeepCopyInto(t *testing.T) {
	source := &ServicePort{
		Name:       "http",
		Protocol:   corev1.ProtocolTCP,
		Port:       80,
		TargetPort: intstr.FromInt(8080),
		NodePort:   30080,
	}
	dest := &ServicePort{}

	source.DeepCopyInto(dest)

	if dest.Name != source.Name {
		t.Error("Name not copied")
	}
	if dest.Port != source.Port {
		t.Error("Port not copied")
	}
	if dest.Protocol != source.Protocol {
		t.Error("Protocol not copied")
	}
}

func TestSleepyServiceSpec_DeepCopyInto(t *testing.T) {
	source := createTestSleepyServiceSpec()
	dest := &SleepyServiceSpec{}

	source.DeepCopyInto(dest)

	if dest.HealthPath != source.HealthPath {
		t.Error("HealthPath not copied")
	}
	if dest.BackendService == source.BackendService {
		t.Error("BackendService pointer should be independent")
	}
	if len(dest.Components) > 0 && &dest.Components[0] == &source.Components[0] {
		t.Error("Components slice should be independent")
	}
}

func TestSleepyServiceStatus_DeepCopyInto(t *testing.T) {
	source := createTestSleepyServiceStatus()
	dest := &SleepyServiceStatus{}

	source.DeepCopyInto(dest)

	if dest.State != source.State {
		t.Error("State not copied")
	}
	if dest.LastTransition == source.LastTransition {
		t.Error("LastTransition pointer should be independent")
	}
}

func TestSleepyService_DeepCopyInto(t *testing.T) {
	source := createTestSleepyService()
	dest := &SleepyService{}

	source.DeepCopyInto(dest)

	if dest.Name != source.Name {
		t.Error("Name not copied")
	}
	if &dest.Spec == &source.Spec {
		t.Error("Spec should be deep copied")
	}
	if &dest.Status == &source.Status {
		t.Error("Status should be deep copied")
	}
}

func TestSleepyServiceList_DeepCopyInto(t *testing.T) {
	source := createTestSleepyServiceList()
	dest := &SleepyServiceList{}

	source.DeepCopyInto(dest)

	if len(dest.Items) != len(source.Items) {
		t.Error("Items length mismatch")
	}
	if len(dest.Items) > 0 && &dest.Items[0] == &source.Items[0] {
		t.Error("Items should be deep copied")
	}
}

// =============================================================================
// Phase 4: DeepCopyObject Tests
// =============================================================================

func TestSleepyService_DeepCopyObject(t *testing.T) {
	tests := []struct {
		name   string
		source *SleepyService
	}{
		{
			name:   "full sleepy service",
			source: createTestSleepyService(),
		},
		{
			name:   "minimal sleepy service",
			source: &SleepyService{ObjectMeta: metav1.ObjectMeta{Name: "test"}},
		},
		{
			name:   "nil source",
			source: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.source.DeepCopyObject()

			if tt.source == nil {
				if result != nil {
					t.Error("DeepCopyObject of nil should return nil")
				}
				return
			}

			copied, ok := result.(*SleepyService)
			if !ok {
				t.Fatal("DeepCopyObject did not return *SleepyService")
			}
			if copied == tt.source {
				t.Error("DeepCopyObject returned same pointer")
			}
			if copied.Name != tt.source.Name {
				t.Error("Name not copied")
			}
		})
	}
}

func TestSleepyServiceList_DeepCopyObject(t *testing.T) {
	tests := []struct {
		name   string
		source *SleepyServiceList
	}{
		{
			name:   "full list",
			source: createTestSleepyServiceList(),
		},
		{
			name:   "empty list",
			source: &SleepyServiceList{Items: []SleepyService{}},
		},
		{
			name:   "nil source",
			source: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.source.DeepCopyObject()

			if tt.source == nil {
				if result != nil {
					t.Error("DeepCopyObject of nil should return nil")
				}
				return
			}

			copied, ok := result.(*SleepyServiceList)
			if !ok {
				t.Fatal("DeepCopyObject did not return *SleepyServiceList")
			}
			if copied == tt.source {
				t.Error("DeepCopyObject returned same pointer")
			}
			if len(copied.Items) != len(tt.source.Items) {
				t.Error("Items count mismatch")
			}
		})
	}
}

// =============================================================================
// Phase 5: Mutation Independence Tests
// =============================================================================

func TestMutationIndependence_BackendServiceSpec(t *testing.T) {
	original := createTestBackendServiceSpec()
	copy := original.DeepCopy()

	// Mutate the copy
	*copy.Enabled = false
	copy.ClusterIP = "192.168.1.1"
	copy.ExternalIPs[0] = "modified-ip"
	copy.Annotations["new-key"] = "new-value"
	copy.Ports[0].Port = 9999

	// Verify original unchanged
	if *original.Enabled != true {
		t.Error("Mutation in copy affected original Enabled")
	}
	if original.ClusterIP != "10.96.0.1" {
		t.Error("Mutation in copy affected original ClusterIP")
	}
	if original.ExternalIPs[0] != "203.0.113.1" {
		t.Error("Mutation in copy's slice affected original")
	}
	// Verify original annotations are unchanged
	originalKey := "service.beta.kubernetes.io/aws-load-balancer-type"
	if original.Annotations[originalKey] != "nlb" {
		t.Error("Mutation in copy's annotations affected original")
	}
	// Verify the new key doesn't exist in original
	if _, exists := original.Annotations["new-key"]; exists {
		t.Error("New annotation in copy appeared in original")
	}
	if original.Ports[0].Port != 8080 {
		t.Error("Mutation in copy's port affected original")
	}
}

func TestMutationIndependence_Component(t *testing.T) {
	original := &Component{
		Name:      "database",
		Type:      ComponentTypeStatefulSet,
		Replicas:  ptr.To(int32(3)),
		DependsOn: []string{"backup"},
		Ref: ResourceRef{
			Name:      "postgres",
			Namespace: "default",
		},
	}

	copy := original.DeepCopy()

	// Mutate the copy
	*copy.Replicas = 5
	copy.DependsOn[0] = testModifiedValue
	copy.Ref.Namespace = "modified-ns"

	// Verify original unchanged
	if *original.Replicas != 3 {
		t.Error("Mutation in copy affected original Replicas")
	}
	if original.DependsOn[0] != "backup" {
		t.Error("Mutation in copy's slice affected original")
	}
	if original.Ref.Namespace != "default" {
		t.Error("Mutation in copy's nested value affected original")
	}
}

func TestMutationIndependence_SleepyService(t *testing.T) {
	original := createTestSleepyService()
	copy := original.DeepCopy()

	// Mutate the copy
	copy.Name = "modified-name"
	copy.Labels["app"] = testModifiedValue
	copy.Spec.HealthPath = "/modified"
	if copy.Spec.BackendService != nil {
		*copy.Spec.BackendService.Enabled = false
	}
	if len(copy.Spec.Components) > 0 {
		copy.Spec.Components[0].Name = "modified-component"
	}

	// Verify original unchanged
	if original.Name != "test-service" {
		t.Error("Mutation in copy affected original Name")
	}
	if original.Labels["app"] != "sleepy" {
		t.Error("Mutation in copy's labels affected original")
	}
	if original.Spec.HealthPath != "/health" {
		t.Error("Mutation in copy's spec affected original")
	}
	if original.Spec.BackendService != nil && *original.Spec.BackendService.Enabled != true {
		t.Error("Mutation in copy's nested object affected original")
	}
	if len(original.Spec.Components) > 0 && original.Spec.Components[0].Name != "app" {
		t.Error("Mutation in copy's component slice affected original")
	}
}

func TestMutationIndependence_ComplexNested(t *testing.T) {
	original := createTestSleepyService()
	copy := original.DeepCopy()

	// Deep mutation in nested structures
	if len(copy.Spec.Components) > 0 && copy.Spec.Components[0].Replicas != nil {
		*copy.Spec.Components[0].Replicas = 999
	}
	if len(copy.Status.Components) > 0 {
		copy.Status.Components[0].Ready = false
	}

	// Verify deep nesting independence
	if len(original.Spec.Components) > 0 && original.Spec.Components[0].Replicas != nil {
		if *original.Spec.Components[0].Replicas != 3 {
			t.Error("Deep mutation in copy affected original nested replicas")
		}
	}
	if len(original.Status.Components) > 0 {
		if !original.Status.Components[0].Ready {
			t.Error("Mutation in copy's status component affected original")
		}
	}
}

// =============================================================================
// Phase 6: Edge Cases & Validation Tests
// =============================================================================

func TestAllTypes_NilPointerSafety(t *testing.T) {
	tests := []struct {
		name   string
		testFn func(t *testing.T)
	}{
		{
			name: "nil BackendServiceSpec.DeepCopy",
			testFn: func(t *testing.T) {
				var b *BackendServiceSpec
				result := b.DeepCopy()
				if result != nil {
					t.Error("nil receiver should return nil")
				}
			},
		},
		{
			name: "nil Component.DeepCopy",
			testFn: func(t *testing.T) {
				var c *Component
				result := c.DeepCopy()
				if result != nil {
					t.Error("nil receiver should return nil")
				}
			},
		},
		{
			name: "nil ComponentStatus.DeepCopy",
			testFn: func(t *testing.T) {
				var cs *ComponentStatus
				result := cs.DeepCopy()
				if result != nil {
					t.Error("nil receiver should return nil")
				}
			},
		},
		{
			name: "nil ResourceRef.DeepCopy",
			testFn: func(t *testing.T) {
				var r *ResourceRef
				result := r.DeepCopy()
				if result != nil {
					t.Error("nil receiver should return nil")
				}
			},
		},
		{
			name: "nil ServicePort.DeepCopy",
			testFn: func(t *testing.T) {
				var sp *ServicePort
				result := sp.DeepCopy()
				if result != nil {
					t.Error("nil receiver should return nil")
				}
			},
		},
		{
			name: "nil SleepyServiceSpec.DeepCopy",
			testFn: func(t *testing.T) {
				var s *SleepyServiceSpec
				result := s.DeepCopy()
				if result != nil {
					t.Error("nil receiver should return nil")
				}
			},
		},
		{
			name: "nil SleepyServiceStatus.DeepCopy",
			testFn: func(t *testing.T) {
				var s *SleepyServiceStatus
				result := s.DeepCopy()
				if result != nil {
					t.Error("nil receiver should return nil")
				}
			},
		},
		{
			name: "nil SleepyService.DeepCopy",
			testFn: func(t *testing.T) {
				var ss *SleepyService
				result := ss.DeepCopy()
				if result != nil {
					t.Error("nil receiver should return nil")
				}
			},
		},
		{
			name: "nil SleepyServiceList.DeepCopy",
			testFn: func(t *testing.T) {
				var sl *SleepyServiceList
				result := sl.DeepCopy()
				if result != nil {
					t.Error("nil receiver should return nil")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.testFn(t)
		})
	}
}

func TestEmptySlicesAndMaps(t *testing.T) {
	tests := []struct {
		name   string
		source *SleepyServiceSpec
	}{
		{
			name: "empty Components slice",
			source: &SleepyServiceSpec{
				Components: []Component{}, // empty, not nil
			},
		},
		{
			name: "nil Components slice",
			source: &SleepyServiceSpec{
				Components: nil, // nil, not empty
			},
		},
		{
			name: "empty BackendService.Annotations map",
			source: &SleepyServiceSpec{
				BackendService: &BackendServiceSpec{
					Annotations: map[string]string{}, // empty, not nil
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			copied := tt.source.DeepCopy()

			// Empty slice should remain empty, not become nil
			if tt.source.Components != nil && len(tt.source.Components) == 0 {
				if copied.Components == nil {
					t.Error("Empty slice became nil after DeepCopy")
				}
			}

			// Nil slice should remain nil
			if tt.source.Components == nil && copied.Components != nil {
				t.Error("Nil slice became non-nil after DeepCopy")
			}

			// Empty map should remain empty, not become nil
			if tt.source.BackendService != nil && tt.source.BackendService.Annotations != nil {
				if copied.BackendService == nil || copied.BackendService.Annotations == nil {
					t.Error("Empty map became nil after DeepCopy")
				}
			}
		})
	}
}

func TestComponentType_EnumValues(t *testing.T) {
	validTypes := []ComponentType{
		ComponentTypeDeployment,
		ComponentTypeStatefulSet,
		ComponentTypeCNPGCluster,
	}

	for _, ct := range validTypes {
		t.Run(string(ct), func(t *testing.T) {
			// Verify enum constant is usable
			component := Component{
				Name: "test",
				Type: ct,
			}
			if component.Type != ct {
				t.Errorf("ComponentType not set correctly: got %s, want %s", component.Type, ct)
			}
		})
	}
}

func TestServiceState_EnumValues(t *testing.T) {
	validStates := []ServiceState{
		StateSleeping,
		StateWaking,
		StateAwake,
		StateHibernating,
		StateError,
	}

	for _, state := range validStates {
		t.Run(string(state), func(t *testing.T) {
			// Verify enum constant is usable
			status := &SleepyServiceStatus{
				State:        state,
				DesiredState: state,
			}
			if status.State != state {
				t.Errorf("ServiceState not set correctly: got %s, want %s", status.State, state)
			}
		})
	}
}

func TestSleepyService_MetadataHandling(t *testing.T) {
	now := metav1.Now()
	original := &SleepyService{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "sleepy.atha.gr/v1alpha1",
			Kind:       "SleepyService",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test-service",
			Namespace:         "kube-system",
			UID:               "test-uid",
			ResourceVersion:   "12345",
			Generation:        1,
			CreationTimestamp: now,
			Labels: map[string]string{
				"app":     "sleepy",
				"version": "v1",
			},
			Annotations: map[string]string{
				"description": "Test service",
			},
			Finalizers: []string{"test.finalizer.io"},
		},
	}

	copied := original.DeepCopy()

	// Verify metadata is deeply copied
	if copied.Name != original.Name {
		t.Error("Name should be copied")
	}
	if copied.Namespace != original.Namespace {
		t.Error("Namespace should be copied")
	}
	if &copied.Labels == &original.Labels {
		t.Error("Labels map should be independent")
	}
	if &copied.Annotations == &original.Annotations {
		t.Error("Annotations map should be independent")
	}
	if len(copied.Finalizers) > 0 && &copied.Finalizers[0] == &original.Finalizers[0] {
		t.Error("Finalizers slice should be independent")
	}

	// Mutate copied metadata
	copied.Labels["app"] = testModifiedValue
	if original.Labels["app"] != "sleepy" {
		t.Error("Mutation in copy's labels affected original")
	}
}

func TestSleepyServiceStatus_ConditionsHandling(t *testing.T) {
	now := metav1.Now()
	original := &SleepyServiceStatus{
		State:          StateAwake,
		DesiredState:   StateAwake,
		LastTransition: &now,
		LastActivity:   &now,
		Conditions: []metav1.Condition{
			{
				Type:               "Ready",
				Status:             metav1.ConditionTrue,
				ObservedGeneration: 1,
				LastTransitionTime: now,
				Reason:             "AllComponentsReady",
				Message:            "All components are ready",
			},
		},
	}

	copied := original.DeepCopy()

	// Verify conditions are deep copied
	if len(copied.Conditions) != len(original.Conditions) {
		t.Error("Conditions length mismatch")
	}
	if len(copied.Conditions) > 0 && &copied.Conditions[0] == &original.Conditions[0] {
		t.Error("Conditions slice should be independent")
	}

	// Verify time pointers are independent
	if copied.LastTransition == original.LastTransition {
		t.Error("LastTransition pointer should be independent")
	}
	if copied.LastActivity == original.LastActivity {
		t.Error("LastActivity pointer should be independent")
	}

	// Mutate copied conditions
	if len(copied.Conditions) > 0 {
		copied.Conditions[0].Message = "Modified message"
		if original.Conditions[0].Message != "All components are ready" {
			t.Error("Mutation in copy's conditions affected original")
		}
	}
}

func TestSleepyServiceList_ItemsHandling(t *testing.T) {
	original := createTestSleepyServiceList()

	copied := original.DeepCopy()

	// Verify items array is independent
	if &copied.Items == &original.Items {
		t.Error("Items array should be independent")
	}

	// Verify each item is independently copied
	if len(copied.Items) > 0 && len(original.Items) > 0 {
		if &copied.Items[0] == &original.Items[0] {
			t.Error("First item should be independent")
		}
	}

	// Mutate copied item
	if len(copied.Items) > 0 {
		copied.Items[0].Name = testModifiedValue
		if original.Items[0].Name == testModifiedValue {
			t.Error("Mutation in copy affected original")
		}
	}
}

func TestComplexNestedStructures_DeepCopy(t *testing.T) {
	complex := createTestSleepyService()

	copied := complex.DeepCopy()

	// Verify all nested structures are independent
	if copied == complex {
		t.Error("Pointer should be different")
	}
	if copied.Spec.BackendService == complex.Spec.BackendService {
		t.Error("Nested BackendService should be independent")
	}
	if len(copied.Spec.Components) > 0 && len(complex.Spec.Components) > 0 {
		if &copied.Spec.Components[0] == &complex.Spec.Components[0] {
			t.Error("Components slice should be independent")
		}
	}
	if len(copied.Status.Components) > 0 && len(complex.Status.Components) > 0 {
		if &copied.Status.Components[0] == &complex.Status.Components[0] {
			t.Error("Status components should be independent")
		}
	}

	// Test deep mutation doesn't affect original
	if len(copied.Spec.Components) > 0 && len(copied.Spec.Components[0].DependsOn) > 0 {
		copied.Spec.Components[0].DependsOn[0] = "deeply-modified"
		if len(complex.Spec.Components) > 0 && len(complex.Spec.Components[0].DependsOn) > 0 {
			if complex.Spec.Components[0].DependsOn[0] == "deeply-modified" {
				t.Error("Deep mutation affected original")
			}
		}
	}
}

// =============================================================================
// Legacy test (keep for baseline compatibility)
// =============================================================================

func Test_Structs(t *testing.T) {
	a := &SleepyServiceSpec{
		HealthPath:     "/health",
		BackendService: &BackendServiceSpec{},
	}

	var out SleepyServiceSpec
	a.DeepCopyInto(&out)

	if out.HealthPath != a.HealthPath {
		t.Errorf("DeepCopyInto failed for SleepyServiceSpec.HealthPath")
	}

	b := a.DeepCopy()
	if b.BackendService == nil {
		t.Errorf("DeepCopy failed for SleepyServiceSpec.BackendService")
	}
}
