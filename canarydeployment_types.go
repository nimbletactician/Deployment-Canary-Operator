// Package v1alpha1 contains the API definitions for the Canary Deployment Operator
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=canarydeployments,shortName=canary,scope=Namespaced
// +kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// CanaryDeployment is the Schema for the canarydeployments API
// It defines a canary deployment strategy that gradually shifts traffic
// from the primary deployment to a canary deployment
type CanaryDeployment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec defines the desired state of the CanaryDeployment
	Spec CanaryDeploymentSpec `json:"spec"`

	// Status defines the observed state of the CanaryDeployment
	// +optional
	Status CanaryDeploymentStatus `json:"status,omitempty"`
}

// CanaryDeploymentSpec defines the desired state of a CanaryDeployment
type CanaryDeploymentSpec struct {
	// PrimaryDeployment is the name of the existing deployment resource
	// that will be the primary/stable deployment
	// +kubebuilder:validation:Required
	PrimaryDeployment string `json:"primaryDeployment"`

	// CanaryImage is the container image to be used for the canary deployment
	// This is the image that will be tested against the primary deployment
	// +kubebuilder:validation:Required
	CanaryImage string `json:"canaryImage"`

	// Service is the name of the service resource that routes traffic
	// to both the primary and canary deployments
	// +kubebuilder:validation:Required
	Service string `json:"service"`

	// Steps defines the incremental steps to roll out the canary deployment
	// Each step represents a percentage of traffic to route to the canary deployment
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	Steps []CanaryStep `json:"steps"`

	// Analysis defines the metrics to evaluate for promoting or rolling back the canary
	// +optional
	Analysis *CanaryAnalysis `json:"analysis,omitempty"`

	// AutoPromote determines whether to automatically promote the canary when
	// all metrics are successful, or require manual promotion
	// +optional
	// +kubebuilder:default=false
	AutoPromote bool `json:"autoPromote,omitempty"`

	// MaxSurge defines the maximum number of pods that can be created
	// over the desired number during the canary deployment
	// +optional
	// +kubebuilder:default="25%"
	MaxSurge string `json:"maxSurge,omitempty"`
}

// CanaryStep defines a single step in the canary deployment process
type CanaryStep struct {
	// Weight is the percentage of traffic to route to the canary deployment in this step
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	Weight int32 `json:"weight"`

	// Pause defines how long to wait after setting this traffic weight
	// before proceeding to the next step
	// The format is a duration string (e.g., "5m", "1h")
	// +kubebuilder:validation:Required
	Pause string `json:"pause"`

	// Metrics defines the metrics to evaluate at this step
	// +optional
	Metrics []MetricCheck `json:"metrics,omitempty"`
}

// CanaryAnalysis defines the metrics to evaluate for promoting or rolling back the canary
type CanaryAnalysis struct {
	// Interval is how frequently to check the metrics
	// The format is a duration string (e.g., "30s", "1m")
	// +kubebuilder:validation:Required
	Interval string `json:"interval"`

	// MaxWeight is the maximum traffic percentage a canary deployment can receive
	// when metrics are failing
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	// +kubebuilder:default=50
	MaxWeight int32 `json:"maxWeight,omitempty"`

	// Threshold defines how many consecutive successful metric checks are required
	// before proceeding to the next step
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=3
	Threshold int32 `json:"threshold,omitempty"`

	// Metrics is a list of metrics to check
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	Metrics []MetricCheck `json:"metrics"`
}

// MetricCheck defines a specific metric to check
type MetricCheck struct {
	// Name is a descriptive name for the metric
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// PrometheusQuery is the PromQL query to execute
	// +kubebuilder:validation:Required
	PrometheusQuery string `json:"prometheusQuery"`

	// Threshold defines the maximum allowed value for the metric
	// +kubebuilder:validation:Required
	Threshold string `json:"threshold"`

	// ThresholdRange defines a range for acceptable metric values
	// Useful for metrics that should stay within a specific range
	// +optional
	ThresholdRange *RangeCheck `json:"thresholdRange,omitempty"`
}

// RangeCheck defines an acceptable range for a metric
type RangeCheck struct {
	// Min is the minimum acceptable value for the metric
	// +optional
	Min string `json:"min,omitempty"`

	// Max is the maximum acceptable value for the metric
	// +optional
	Max string `json:"max,omitempty"`
}

// CanaryDeploymentStatus defines the observed state of a CanaryDeployment
type CanaryDeploymentStatus struct {
	// Phase is the current phase of the canary deployment
	// +optional
	Phase CanaryPhase `json:"phase,omitempty"`

	// CurrentStep is the index of the current step in the deployment process
	// +optional
	CurrentStep int32 `json:"currentStep,omitempty"`

	// CurrentWeight is the current traffic percentage routed to the canary
	// +optional
	CurrentWeight int32 `json:"currentWeight,omitempty"`

	// CanaryDeploymentName is the name of the generated canary deployment
	// +optional
	CanaryDeploymentName string `json:"canaryDeploymentName,omitempty"`

	// StartedAt is when the canary deployment started
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// CompletedAt is when the canary deployment completed (either succeeded or failed)
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`

	// Conditions represents the latest available observations of the canary deployment
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// MetricResults contains the latest metric analysis results
	// +optional
	MetricResults []MetricResult `json:"metricResults,omitempty"`
}

// MetricResult contains the result of a metric check
type MetricResult struct {
	// Name is the name of the metric
	Name string `json:"name"`

	// Value is the measured value
	Value string `json:"value"`

	// Success indicates whether the check passed
	Success bool `json:"success"`

	// Message provides additional information about the check
	// +optional
	Message string `json:"message,omitempty"`

	// Timestamp is when the check was performed
	Timestamp metav1.Time `json:"timestamp"`
}

// CanaryPhase represents the current phase of a canary deployment
// +kubebuilder:validation:Enum=Initializing;Progressing;Analyzing;Promoting;Succeeded;Failed;Cancelled
type CanaryPhase string

const (
	// CanaryPhaseInitializing means the canary deployment is being initialized
	CanaryPhaseInitializing CanaryPhase = "Initializing"

	// CanaryPhaseProgressing means the canary deployment is progressing through steps
	CanaryPhaseProgressing CanaryPhase = "Progressing"

	// CanaryPhaseAnalyzing means the canary deployment is being analyzed
	CanaryPhaseAnalyzing CanaryPhase = "Analyzing"

	// CanaryPhasePromoting means the canary deployment is being promoted
	CanaryPhasePromoting CanaryPhase = "Promoting"

	// CanaryPhaseSucceeded means the canary deployment has succeeded
	CanaryPhaseSucceeded CanaryPhase = "Succeeded"

	// CanaryPhaseFailed means the canary deployment has failed
	CanaryPhaseFailed CanaryPhase = "Failed"

	// CanaryPhaseCancelled means the canary deployment was cancelled
	CanaryPhaseCancelled CanaryPhase = "Cancelled"
)

// +kubebuilder:object:root=true

// CanaryDeploymentList contains a list of CanaryDeployment resources
type CanaryDeploymentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CanaryDeployment `json:"items"`
}

func init() {
	SchemeBuilder.Register(&CanaryDeployment{}, &CanaryDeploymentList{})
}
