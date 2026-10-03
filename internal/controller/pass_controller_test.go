/*
Copyright 2026 UnPoilTefal.
SPDX-License-Identifier: MIT
*/

package controller

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	testclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	nightshiftv1alpha1 "github.com/UnPoilTefal/night-shift/api/v1alpha1"
)

func createPass(ns, name, shift string) {
	Expect(k8sClient.Create(ctx, &nightshiftv1alpha1.Pass{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       nightshiftv1alpha1.PassSpec{ShiftName: shift},
	})).To(Succeed())
}

// reconcilePass rejoue la reconciliation quelques fois, comme le feraient les
// événements successifs (adoption, création du Job…).
func reconcilePass(ns, name string) {
	reconcilePassAt(ns, name, time.Now())
}

func reconcilePassAt(ns, name string, now time.Time) {
	r := &PassReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Clock: testclock.NewFakePassiveClock(now)}
	for range 3 {
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}})
		Expect(err).NotTo(HaveOccurred())
	}
}

func getPass(ns, name string) nightshiftv1alpha1.Pass {
	var p nightshiftv1alpha1.Pass
	Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &p)).To(Succeed())
	return p
}

func getJob(ns, name string) (batchv1.Job, error) {
	var j batchv1.Job
	err := k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &j)
	return j, err
}

// finishJob simule le contrôleur de Jobs, absent d'envtest.
func finishJob(j *batchv1.Job, succeeded bool) {
	now := metav1.Now()
	j.Status.StartTime = &now
	if succeeded {
		j.Status.Succeeded = 1
		j.Status.CompletionTime = &now
		j.Status.Conditions = []batchv1.JobCondition{
			{Type: batchv1.JobSuccessCriteriaMet, Status: corev1.ConditionTrue, LastTransitionTime: now},
			{Type: batchv1.JobComplete, Status: corev1.ConditionTrue, LastTransitionTime: now},
		}
	} else {
		j.Status.Failed = 1
		j.Status.Conditions = []batchv1.JobCondition{
			{Type: batchv1.JobFailureTarget, Status: corev1.ConditionTrue, LastTransitionTime: now},
			{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, LastTransitionTime: now},
		}
	}
	Expect(k8sClient.Status().Update(ctx, j)).To(Succeed())
}

var _ = Describe("Pass controller", func() {
	var ns string
	BeforeEach(func() {
		ns = newNamespace()
		createShift(ns, "team", shiftSpec("0 2 * * *"))
	})

	It("adopts a manual pass, starts a hardened orchestrator job and follows it to success", func() {
		createPass(ns, "manual", "team")
		reconcilePass(ns, "manual")

		p := getPass(ns, "manual")
		Expect(p.Labels).To(HaveKeyWithValue(nightshiftv1alpha1.ShiftLabel, "team"))
		Expect(metav1.GetControllerOf(&p).Name).To(Equal("team"))
		Expect(p.Status.Phase).To(Equal(nightshiftv1alpha1.PassRunning))
		Expect(p.Status.JobName).To(Equal("manual"))

		job, err := getJob(ns, "manual")
		Expect(err).NotTo(HaveOccurred())
		pod := job.Spec.Template.Spec
		Expect(*pod.AutomountServiceAccountToken).To(BeFalse())
		Expect(*pod.SecurityContext.RunAsNonRoot).To(BeTrue())
		Expect(*pod.Containers[0].SecurityContext.ReadOnlyRootFilesystem).To(BeTrue())
		Expect(*pod.Containers[0].SecurityContext.AllowPrivilegeEscalation).To(BeFalse())
		Expect(pod.Containers[0].SecurityContext.Capabilities.Drop).To(ConsistOf(corev1.Capability("ALL")))

		finishJob(&job, true)
		reconcilePass(ns, "manual")
		p = getPass(ns, "manual")
		Expect(p.Status.Phase).To(Equal(nightshiftv1alpha1.PassSucceeded))
		Expect(p.Status.CompletionTime).NotTo(BeNil())
	})

	It("marks the pass failed when its job fails", func() {
		createPass(ns, "doomed", "team")
		reconcilePass(ns, "doomed")
		job, err := getJob(ns, "doomed")
		Expect(err).NotTo(HaveOccurred())

		finishJob(&job, false)
		reconcilePass(ns, "doomed")
		Expect(getPass(ns, "doomed").Status.Phase).To(Equal(nightshiftv1alpha1.PassFailed))
	})

	It("keeps a newer pass pending while an older pass of the same shift is unfinished", func() {
		createPass(ns, "a-older", "team")
		reconcilePass(ns, "a-older")
		createPass(ns, "b-newer", "team")
		reconcilePass(ns, "b-newer")

		Expect(getPass(ns, "a-older").Status.Phase).To(Equal(nightshiftv1alpha1.PassRunning))
		Expect(getPass(ns, "b-newer").Status.Phase).To(Equal(nightshiftv1alpha1.PassPending))
		_, err := getJob(ns, "b-newer")
		Expect(apierrors.IsNotFound(err)).To(BeTrue())

		job, err := getJob(ns, "a-older")
		Expect(err).NotTo(HaveOccurred())
		finishJob(&job, true)
		reconcilePass(ns, "a-older")
		reconcilePass(ns, "b-newer")
		Expect(getPass(ns, "b-newer").Status.Phase).To(Equal(nightshiftv1alpha1.PassRunning))
	})

	It("waits for a missing shift, then fails the pass after a grace period", func() {
		createPass(ns, "orphan", "nobody")
		reconcilePass(ns, "orphan")
		Expect(getPass(ns, "orphan").Status.Phase).To(Equal(nightshiftv1alpha1.PassPending))

		reconcilePassAt(ns, "orphan", time.Now().Add(shiftWaitTimeout+time.Second))
		Expect(getPass(ns, "orphan").Status.Phase).To(Equal(nightshiftv1alpha1.PassFailed))
	})

	It("runs a pass created before its shift", func() {
		createPass(ns, "early", "late-shift")
		reconcilePass(ns, "early")
		Expect(getPass(ns, "early").Status.Phase).To(Equal(nightshiftv1alpha1.PassPending))

		createShift(ns, "late-shift", shiftSpec("0 2 * * *"))
		reconcilePass(ns, "early")
		Expect(getPass(ns, "early").Status.Phase).To(Equal(nightshiftv1alpha1.PassRunning))
	})
})
