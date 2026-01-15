// Copyright 2026 AthaLabs
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// SleepyServiceSpec defines the desired state of SleepyService.
type SleepyServiceSpec struct {
	// Debug enabled /_wake/debug/wake and /_wake/debug/sleep endpoints
	// +optional
	// +kubebuilder:default:=false
	Debug *bool `json:"debug,omitempty"`

	// BackendService configuration for the managed backend Service
	// +optional
	BackendService *BackendServiceSpec `json:"backendService,omitempty"`

	// WakeTimeout is how long to wait for the stack to wake
	// +kubebuilder:default:="5m"
	WakeTimeout metav1.Duration `json:"wakeTimeout,omitempty"`

	// IdleTimeout triggers auto-hibernate after no activity (0 = disabled)
	// +kubebuilder:default:="0"
	IdleTimeout metav1.Duration `json:"idleTimeout,omitempty"`

	// Components to manage, in dependency order
	// +kubebuilder:validation:MinItems=1
	Components []Component `json:"components"`
}

// BackendServiceSpec defines how to create the backend Service
type BackendServiceSpec struct {
	// Enabled controls whether to create the backend Service
	// +kubebuilder:default:=true
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// Type of service (ClusterIP, NodePort, LoadBalancer, ExternalName)
	// +kubebuilder:default:="ClusterIP"
	// +kubebuilder:validation:Enum=ClusterIP;NodePort;LoadBalancer;ExternalName
	// +optional
	Type corev1.ServiceType `json:"type,omitempty"`

	// Ports to expose on the Service
	// If not specified, will auto-create based on the first container port
	// +optional
	Ports []ServicePort `json:"ports,omitempty"`

	// ClusterIP to use (optional, usually left empty for auto-assignment)
	// +optional
	ClusterIP string `json:"clusterIP,omitempty"`

	// ExternalIPs that will route to this Service
	// +optional
	ExternalIPs []string `json:"externalIPs,omitempty"`

	// LoadBalancerIP for LoadBalancer type Services
	// +optional
	LoadBalancerIP string `json:"loadBalancerIP,omitempty"`

	// SessionAffinity (ClientIP or None)
	// +kubebuilder:validation:Enum=ClientIP;None
	// +optional
	SessionAffinity corev1.ServiceAffinity `json:"sessionAffinity,omitempty"`

	// Annotations to add to the backend Service
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// ServicePort defines a port to expose on the backend Service
type ServicePort struct {
	// Name of the port (required if multiple ports)
	// +optional
	Name string `json:"name,omitempty"`

	// Protocol (TCP, UDP, SCTP)
	// +kubebuilder:default:="TCP"
	// +kubebuilder:validation:Enum=TCP;UDP;SCTP
	// +optional
	Protocol corev1.Protocol `json:"protocol,omitempty"`

	// Port to expose on the Service
	Port int32 `json:"port"`

	// TargetPort on the pod (can be port number or name)
	// If not specified, defaults to Port
	// +optional
	TargetPort intstr.IntOrString `json:"targetPort,omitempty"`

	// NodePort for NodePort/LoadBalancer type (30000-32767)
	// +optional
	NodePort int32 `json:"nodePort,omitempty"`
}

type Component struct {
	// Name is a unique identifier for this component
	Name string `json:"name"`

	// Type of resource: Deployment, StatefulSet, CNPGCluster
	// +kubebuilder:validation:Enum=Deployment;StatefulSet;CNPGCluster
	Type ComponentType `json:"type"`

	// Ref identifies the resource
	Ref ResourceRef `json:"ref"`

	// Replicas to scale to when waking (for Deployment/StatefulSet)
	// +kubebuilder:default:=1
	Replicas *int32 `json:"replicas,omitempty"`

	// DependsOn lists component names that must be ready before this one
	DependsOn []string `json:"dependsOn,omitempty"`
}

type ComponentType string

const (
	ComponentTypeDeployment  ComponentType = "Deployment"
	ComponentTypeStatefulSet ComponentType = "StatefulSet"
	ComponentTypeCNPGCluster ComponentType = "CNPGCluster"
)

type ResourceRef struct {
	// Name of the resource
	Name string `json:"name"`

	// Namespace, defaults to the SleepyService's namespace
	Namespace string `json:"namespace,omitempty"`
}

// SleepyServiceStatus defines the observed state of SleepyService.
type SleepyServiceStatus struct {
	// DesiredState is what the proxy wants the service to be (set by proxy)
	// +optional
	// +kubebuilder:validation:Enum=Sleeping;Waking;Awake;Hibernating;Error
	DesiredState ServiceState `json:"desiredState,omitempty"`

	// State is the current state of the service (set by controller)
	// +kubebuilder:validation:Enum=Sleeping;Waking;Awake;Hibernating;Error
	State ServiceState `json:"state,omitempty"`

	// LastTransition is when the state last changed
	LastTransition *metav1.Time `json:"lastTransition,omitempty"`

	// LastActivity is when the service last received traffic (set by proxy)
	LastActivity *metav1.Time `json:"lastActivity,omitempty"`

	// Components status
	Components []ComponentStatus `json:"components,omitempty"`

	// ProxyDeployment is the name of the created proxy
	ProxyDeployment string `json:"proxyDeployment,omitempty"`

	// BackendService is the name of the created backend Service
	BackendService string `json:"backendService,omitempty"`

	// Conditions for standard k8s status reporting
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

type ServiceState string

const (
	StateSleeping    ServiceState = "Sleeping"
	StateWaking      ServiceState = "Waking"
	StateAwake       ServiceState = "Awake"
	StateHibernating ServiceState = "Hibernating"
	StateError       ServiceState = "Error"
)

type ComponentStatus struct {
	Name    string `json:"name"`
	Ready   bool   `json:"ready"`
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:categories=all

// SleepyService is the Schema for the sleepyservices API.
type SleepyService struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SleepyServiceSpec   `json:"spec,omitempty"`
	Status SleepyServiceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SleepyServiceList contains a list of SleepyService.
type SleepyServiceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SleepyService `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SleepyService{}, &SleepyServiceList{})
}
