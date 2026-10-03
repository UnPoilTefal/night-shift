/*
Copyright 2026 UnPoilTefal.
SPDX-License-Identifier: MIT
*/

package controller

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/robfig/cron/v3"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	testclock "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	nightshiftv1alpha1 "github.com/UnPoilTefal/night-shift/api/v1alpha1"
)

// newNamespace isole chaque test dans son propre namespace.
func newNamespace() string {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "ns-"}}
	Expect(k8sClient.Create(ctx, ns)).To(Succeed())
	return ns.Name
}

func shiftSpec(cronExpr string) nightshiftv1alpha1.ShiftSpec {
	return nightshiftv1alpha1.ShiftSpec{
		Trigger: nightshiftv1alpha1.Trigger{Schedule: &nightshiftv1alpha1.ScheduleTrigger{Cron: cronExpr}},
		Mission: nightshiftv1alpha1.Mission{Tickets: &nightshiftv1alpha1.TicketsMission{
			Source: nightshiftv1alpha1.TicketSource{Fake: &nightshiftv1alpha1.FakeTicketSource{ConfigMapName: "tickets"}},
		}},
	}
}

func createShift(ns, name string, spec nightshiftv1alpha1.ShiftSpec) *nightshiftv1alpha1.Shift {
	s := &nightshiftv1alpha1.Shift{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, Spec: spec}
	Expect(k8sClient.Create(ctx, s)).To(Succeed())
	return s
}

func reconcileShift(clk *testclock.FakePassiveClock, ns, name string) reconcile.Result {
	r := &ShiftReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Clock: clk}
	res, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}})
	Expect(err).NotTo(HaveOccurred())
	return res
}

func passesOf(ns, shift string) []nightshiftv1alpha1.Pass {
	var list nightshiftv1alpha1.PassList
	Expect(k8sClient.List(ctx, &list, client.InNamespace(ns),
		client.MatchingLabels{nightshiftv1alpha1.ShiftLabel: shift})).To(Succeed())
	return list.Items
}

func setPassPhase(p *nightshiftv1alpha1.Pass, phase nightshiftv1alpha1.PassPhase, completed time.Time) {
	p.Status.Phase = phase
	p.Status.CompletionTime = &metav1.Time{Time: completed}
	Expect(k8sClient.Status().Update(ctx, p)).To(Succeed())
}

func nextAfter(cronExpr string, t time.Time) time.Time {
	s, err := cron.ParseStandard(cronExpr)
	Expect(err).NotTo(HaveOccurred())
	return s.Next(t.UTC())
}

var _ = Describe("Shift controller", func() {
	var ns string
	BeforeEach(func() { ns = newNamespace() })

	It("creates a pass when the schedule is due, and not before", func() {
		s := createShift(ns, "nightly", shiftSpec("0 2 * * *"))
		due := nextAfter("0 2 * * *", s.CreationTimestamp.Time)
		clk := testclock.NewFakePassiveClock(due.Add(-time.Minute))

		res := reconcileShift(clk, ns, "nightly")
		Expect(passesOf(ns, "nightly")).To(BeEmpty())
		Expect(res.RequeueAfter).To(Equal(time.Minute))

		clk.SetTime(due.Add(time.Second))
		reconcileShift(clk, ns, "nightly")
		reconcileShift(clk, ns, "nightly") // rejouée : pas de doublon

		passes := passesOf(ns, "nightly")
		Expect(passes).To(HaveLen(1))
		Expect(passes[0].Name).To(Equal(fmt.Sprintf("nightly-%d", due.Unix())))
		Expect(passes[0].Spec.ShiftName).To(Equal("nightly"))
		Expect(metav1.GetControllerOf(&passes[0]).Name).To(Equal("nightly"))
	})

	It("never creates a pass while another one is active", func() {
		s := createShift(ns, "busy", shiftSpec("* * * * *"))
		first := nextAfter("* * * * *", s.CreationTimestamp.Time)
		clk := testclock.NewFakePassiveClock(first.Add(time.Second))
		reconcileShift(clk, ns, "busy")
		Expect(passesOf(ns, "busy")).To(HaveLen(1))

		clk.SetTime(first.Add(5 * time.Minute))
		reconcileShift(clk, ns, "busy")
		passes := passesOf(ns, "busy")
		Expect(passes).To(HaveLen(1))

		var shift nightshiftv1alpha1.Shift
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: "busy"}, &shift)).To(Succeed())
		Expect(shift.Status.ActivePass).To(Equal(passes[0].Name))

		setPassPhase(&passes[0], nightshiftv1alpha1.PassSucceeded, clk.Now())
		clk.SetTime(first.Add(6*time.Minute + time.Second))
		reconcileShift(clk, ns, "busy")
		Expect(passesOf(ns, "busy")).To(HaveLen(2))
	})

	It("keeps a bounded history of finished passes", func() {
		spec := shiftSpec("0 2 * * *")
		spec.SuccessfulPassesHistoryLimit = ptr.To[int32](2)
		spec.FailedPassesHistoryLimit = ptr.To[int32](1)
		createShift(ns, "history", spec)
		base := time.Now().Add(-time.Hour)
		for i, phase := range []nightshiftv1alpha1.PassPhase{
			nightshiftv1alpha1.PassSucceeded, nightshiftv1alpha1.PassSucceeded, nightshiftv1alpha1.PassSucceeded,
			nightshiftv1alpha1.PassSucceeded, nightshiftv1alpha1.PassFailed, nightshiftv1alpha1.PassFailed,
		} {
			p := &nightshiftv1alpha1.Pass{
				ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("history-%d", i), Namespace: ns,
					Labels: map[string]string{nightshiftv1alpha1.ShiftLabel: "history"}},
				Spec: nightshiftv1alpha1.PassSpec{ShiftName: "history"},
			}
			Expect(k8sClient.Create(ctx, p)).To(Succeed())
			setPassPhase(p, phase, base.Add(time.Duration(i)*time.Minute))
		}

		reconcileShift(testclock.NewFakePassiveClock(time.Now()), ns, "history")

		var names []string
		for _, p := range passesOf(ns, "history") {
			if p.DeletionTimestamp == nil {
				names = append(names, p.Name)
			}
		}
		Expect(names).To(ConsistOf("history-2", "history-3", "history-5"))
	})

	It("rejects a shift without a trigger at admission", func() {
		spec := shiftSpec("0 2 * * *")
		spec.Trigger = nightshiftv1alpha1.Trigger{}
		err := k8sClient.Create(ctx, &nightshiftv1alpha1.Shift{
			ObjectMeta: metav1.ObjectMeta{Name: "no-trigger", Namespace: ns}, Spec: spec,
		})
		Expect(err).To(MatchError(ContainSubstring("exactly one trigger must be set")))
	})

	It("rejects a shift without a ticket source at admission", func() {
		spec := shiftSpec("0 2 * * *")
		spec.Mission.Tickets.Source = nightshiftv1alpha1.TicketSource{}
		err := k8sClient.Create(ctx, &nightshiftv1alpha1.Shift{
			ObjectMeta: metav1.ObjectMeta{Name: "no-source", Namespace: ns}, Spec: spec,
		})
		Expect(err).To(MatchError(ContainSubstring("exactly one ticket source must be set")))
	})

	It("reports an invalid cron expression without creating passes", func() {
		createShift(ns, "broken", shiftSpec("every night"))
		reconcileShift(testclock.NewFakePassiveClock(time.Now().Add(48*time.Hour)), ns, "broken")

		var shift nightshiftv1alpha1.Shift
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: "broken"}, &shift)).To(Succeed())
		cond := meta.FindStatusCondition(shift.Status.Conditions, ConditionReady)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal("InvalidSchedule"))
		Expect(passesOf(ns, "broken")).To(BeEmpty())
	})
})
