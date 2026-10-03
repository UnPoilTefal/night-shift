/*
Copyright 2026 UnPoilTefal.
SPDX-License-Identifier: MIT
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// Les commentaires des types de l'API sont en anglais : ils deviennent la
// documentation publique du CRD (kubectl explain).

// ShiftLabel links a Pass, and the Jobs it creates, to its Shift.
const ShiftLabel = "nightshift.unpoiltefal.github.io/shift"

// ShiftSpec defines the desired state of Shift.
type ShiftSpec struct {
	// trigger decides when passes start. Exactly one member must be set.
	// +required
	Trigger Trigger `json:"trigger"`

	// mission is the kind of work this shift's passes do. Exactly one member
	// must be set.
	// +required
	Mission Mission `json:"mission"`

	// successfulPassesHistoryLimit is how many succeeded passes are kept.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=3
	// +optional
	SuccessfulPassesHistoryLimit *int32 `json:"successfulPassesHistoryLimit,omitempty"`

	// failedPassesHistoryLimit is how many failed passes are kept.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=1
	// +optional
	FailedPassesHistoryLimit *int32 `json:"failedPassesHistoryLimit,omitempty"`
}

// Trigger decides when passes start. It is a union: exactly one member is set.
// +kubebuilder:validation:XValidation:rule="[has(self.schedule)].filter(x, x).size() == 1",message="exactly one trigger must be set"
type Trigger struct {
	// schedule starts a pass at each occurrence of a cron expression.
	// +optional
	Schedule *ScheduleTrigger `json:"schedule,omitempty"`
}

// ScheduleTrigger starts a pass at each occurrence of a cron expression.
type ScheduleTrigger struct {
	// cron is a standard five-field cron expression, e.g. "0 2 * * *".
	// +kubebuilder:validation:MinLength=1
	// +required
	Cron string `json:"cron"`

	// timeZone is an IANA time zone name, e.g. "Europe/Paris". Defaults to UTC.
	// +optional
	TimeZone string `json:"timeZone,omitempty"`
}

// Mission is the kind of work a shift's passes do. It is a union: exactly one
// member is set, named by the mission's slug.
// +kubebuilder:validation:XValidation:rule="[has(self.tickets)].filter(x, x).size() == 1",message="exactly one mission must be set"
type Mission struct {
	// tickets processes the ready tickets of a ticket source.
	// +optional
	Tickets *TicketsMission `json:"tickets,omitempty"`
}

// TicketsMission processes the ready tickets of a ticket source.
type TicketsMission struct {
	// source is where the ready tickets live.
	// +required
	Source TicketSource `json:"source"`
}

// TicketSource is where ready tickets live. It is a union: exactly one member
// is set.
// +kubebuilder:validation:XValidation:rule="[has(self.fake)].filter(x, x).size() == 1",message="exactly one ticket source must be set"
type TicketSource struct {
	// fake reads tickets from a ConfigMap, for demonstrations and tests.
	// +optional
	Fake *FakeTicketSource `json:"fake,omitempty"`
}

// FakeTicketSource reads tickets from a ConfigMap in the Shift's namespace.
type FakeTicketSource struct {
	// configMapName names the ConfigMap that lists the tickets.
	// +kubebuilder:validation:MinLength=1
	// +required
	ConfigMapName string `json:"configMapName"`
}

// ShiftStatus defines the observed state of Shift.
type ShiftStatus struct {
	// lastScheduleTime is the last time a pass was due.
	// +optional
	LastScheduleTime *metav1.Time `json:"lastScheduleTime,omitempty"`

	// nextScheduleTime is the next time a pass is due.
	// +optional
	NextScheduleTime *metav1.Time `json:"nextScheduleTime,omitempty"`

	// activePass names the pass currently pending or running, if any.
	// +optional
	ActivePass string `json:"activePass,omitempty"`

	// conditions represent the current state of the Shift. "Ready" is false
	// when the trigger cannot be scheduled.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Shift is a team's self-service declaration asking night-shift to run passes
// on a ticket source, on a trigger.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:validation:XValidation:rule="self.metadata.name.size() <= 52",message="name must be at most 52 characters, so that pass and job names stay valid"
// +kubebuilder:printcolumn:name="Schedule",type=string,JSONPath=`.spec.trigger.schedule.cron`
// +kubebuilder:printcolumn:name="Active",type=string,JSONPath=`.status.activePass`
// +kubebuilder:printcolumn:name="Last",type=date,JSONPath=`.status.lastScheduleTime`
// +kubebuilder:printcolumn:name="Next",type=string,JSONPath=`.status.nextScheduleTime`
type Shift struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of Shift
	// +required
	Spec ShiftSpec `json:"spec"`

	// status defines the observed state of Shift
	// +optional
	Status ShiftStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// ShiftList contains a list of Shift
type ShiftList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Shift `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &Shift{}, &ShiftList{})
		return nil
	})
}
