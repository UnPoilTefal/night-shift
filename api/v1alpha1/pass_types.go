/*
Copyright 2026 UnPoilTefal.
SPDX-License-Identifier: MIT
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// PassSpec defines the desired state of Pass.
type PassSpec struct {
	// shiftName names the Shift, in the same namespace, this pass belongs to.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="shiftName is immutable"
	// +required
	ShiftName string `json:"shiftName"`
}

// PassPhase is where a pass is in its lifecycle.
// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed
type PassPhase string

// Pass phases.
const (
	// PassPending: the pass waits for an older pass of its shift to finish.
	PassPending PassPhase = "Pending"
	// PassRunning: the pass's orchestrator Job is running.
	PassRunning PassPhase = "Running"
	// PassSucceeded: the orchestrator Job completed.
	PassSucceeded PassPhase = "Succeeded"
	// PassFailed: the orchestrator Job failed, or the pass could not start.
	PassFailed PassPhase = "Failed"
)

// Finished reports whether the pass reached a terminal phase.
func (p PassPhase) Finished() bool { return p == PassSucceeded || p == PassFailed }

// PassStatus defines the observed state of Pass.
type PassStatus struct {
	// phase is where the pass is in its lifecycle.
	// +optional
	Phase PassPhase `json:"phase,omitempty"`

	// jobName names the pass's orchestrator Job.
	// +optional
	JobName string `json:"jobName,omitempty"`

	// startTime is when the orchestrator Job was created.
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// completionTime is when the pass reached a terminal phase.
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// conditions represent the current state of the Pass.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Pass is one execution of a shift: a snapshot of the queue, a capped number
// of tickets processed, then a digest. Scheduled passes are created by their
// Shift; a pass created by hand runs the same way.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:validation:XValidation:rule="self.metadata.name.size() <= 63",message="name must be at most 63 characters, so that the job name stays valid"
// +kubebuilder:printcolumn:name="Shift",type=string,JSONPath=`.spec.shiftName`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type Pass struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of Pass
	// +required
	Spec PassSpec `json:"spec"`

	// status defines the observed state of Pass
	// +optional
	Status PassStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// PassList contains a list of Pass
type PassList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Pass `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &Pass{}, &PassList{})
		return nil
	})
}
