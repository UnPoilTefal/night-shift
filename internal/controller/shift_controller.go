/*
Copyright 2026 UnPoilTefal.
SPDX-License-Identifier: MIT
*/

package controller

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/robfig/cron/v3"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	nightshiftv1alpha1 "github.com/UnPoilTefal/night-shift/api/v1alpha1"
)

// ConditionReady est vrai quand le déclencheur du poste est planifiable.
const ConditionReady = "Ready"

// catchUpWindows sert à retrouver la plus récente échéance manquée sans
// parcourir toutes celles écoulées depuis la dernière passe : on cherche
// d'abord dans la dernière minute, puis l'heure, le jour…
var catchUpWindows = []time.Duration{
	time.Minute, time.Hour, 24 * time.Hour, 31 * 24 * time.Hour, 366 * 24 * time.Hour, 5 * 366 * 24 * time.Hour,
}

// ShiftReconciler traduit un poste en passes : une passe par échéance du
// déclencheur, jamais deux actives à la fois, avec un historique borné.
type ShiftReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Clock  clock.PassiveClock
}

// +kubebuilder:rbac:groups=nightshift.unpoiltefal.github.io,resources=shifts,verbs=get;list;watch
// +kubebuilder:rbac:groups=nightshift.unpoiltefal.github.io,resources=shifts/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=nightshift.unpoiltefal.github.io,resources=passes,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups=nightshift.unpoiltefal.github.io,resources=shifts/finalizers,verbs=update

// Reconcile crée la passe due, nettoie l'historique et replanifie le poste à
// sa prochaine échéance.
func (r *ShiftReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var shift nightshiftv1alpha1.Shift
	if err := r.Get(ctx, req.NamespacedName, &shift); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	schedule, loc, err := parseSchedule(shift.Spec.Trigger.Schedule)
	if err != nil {
		meta.SetStatusCondition(&shift.Status.Conditions, metav1.Condition{
			Type: ConditionReady, Status: metav1.ConditionFalse, Reason: "InvalidSchedule", Message: err.Error(),
			ObservedGeneration: shift.Generation,
		})
		shift.Status.NextScheduleTime = nil
		return ctrl.Result{}, r.Status().Update(ctx, &shift)
	}

	passes, err := r.passesOf(ctx, &shift)
	if err != nil {
		return ctrl.Result{}, err
	}
	active := activePass(passes)

	now := r.Clock.Now()
	last := shift.CreationTimestamp.Time
	if shift.Status.LastScheduleTime != nil {
		last = shift.Status.LastScheduleTime.Time
	}
	due, next := dueAndNext(schedule, loc, last, now)
	if next.IsZero() {
		meta.SetStatusCondition(&shift.Status.Conditions, metav1.Condition{
			Type: ConditionReady, Status: metav1.ConditionFalse, Reason: "InvalidSchedule",
			Message: "cron " + shift.Spec.Trigger.Schedule.Cron + " never fires", ObservedGeneration: shift.Generation,
		})
		shift.Status.NextScheduleTime = nil
		return ctrl.Result{}, r.Status().Update(ctx, &shift)
	}

	if !due.IsZero() {
		if active == nil {
			p, err := r.createPass(ctx, &shift, due)
			if err != nil {
				return ctrl.Result{}, err
			}
			active = p
			log.Info("pass created", "pass", p.Name, "due", due)
		} else {
			log.Info("schedule skipped, a pass is still active", "active", active.Name, "due", due)
		}
		shift.Status.LastScheduleTime = &metav1.Time{Time: due}
	}

	if err := r.pruneHistory(ctx, &shift, passes); err != nil {
		return ctrl.Result{}, err
	}

	shift.Status.ActivePass = ""
	if active != nil {
		shift.Status.ActivePass = active.Name
	}
	shift.Status.NextScheduleTime = &metav1.Time{Time: next}
	meta.SetStatusCondition(&shift.Status.Conditions, metav1.Condition{
		Type: ConditionReady, Status: metav1.ConditionTrue, Reason: "Scheduled",
		Message: "next pass at " + next.Format(time.RFC3339), ObservedGeneration: shift.Generation,
	})
	if err := r.Status().Update(ctx, &shift); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: next.Sub(now)}, nil
}

func parseSchedule(s *nightshiftv1alpha1.ScheduleTrigger) (cron.Schedule, *time.Location, error) {
	if s == nil {
		return nil, nil, fmt.Errorf("no schedule trigger")
	}
	loc := time.UTC
	if s.TimeZone != "" {
		l, err := time.LoadLocation(s.TimeZone)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid timeZone %q: %w", s.TimeZone, err)
		}
		loc = l
	}
	sched, err := cron.ParseStandard(s.Cron)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid cron %q: %w", s.Cron, err)
	}
	return sched, loc, nil
}

// dueAndNext rend la plus récente échéance de (last, now], zéro s'il n'y en a
// pas : seule celle-ci donne lieu à une passe, comme pour un CronJob. Elle
// rend aussi la prochaine échéance après now, zéro si le cron ne se déclenche
// plus jamais (par exemple un 30 février).
func dueAndNext(s cron.Schedule, loc *time.Location, last, now time.Time) (due, next time.Time) {
	next = s.Next(now.In(loc))
	start := last
	for _, w := range catchUpWindows {
		c := now.Add(-w)
		if !c.After(last) {
			break
		}
		if t := s.Next(c.In(loc)); !t.IsZero() && !t.After(now) {
			start = c
			break
		}
	}
	for t := s.Next(start.In(loc)); !t.IsZero() && !t.After(now); t = s.Next(t) {
		due = t
	}
	return due, next
}

func (r *ShiftReconciler) passesOf(ctx context.Context, shift *nightshiftv1alpha1.Shift) ([]nightshiftv1alpha1.Pass, error) {
	var list nightshiftv1alpha1.PassList
	if err := r.List(ctx, &list, client.InNamespace(shift.Namespace),
		client.MatchingLabels{nightshiftv1alpha1.ShiftLabel: shift.Name}); err != nil {
		return nil, err
	}
	return list.Items, nil
}

func activePass(passes []nightshiftv1alpha1.Pass) *nightshiftv1alpha1.Pass {
	for i := range passes {
		if !passes[i].Status.Phase.Finished() {
			return &passes[i]
		}
	}
	return nil
}

// createPass crée la passe d'une échéance. Son nom est déterministe : une
// reconciliation rejouée ne crée pas de doublon.
func (r *ShiftReconciler) createPass(ctx context.Context, shift *nightshiftv1alpha1.Shift, due time.Time) (*nightshiftv1alpha1.Pass, error) {
	p := &nightshiftv1alpha1.Pass{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%d", shift.Name, due.Unix()),
			Namespace: shift.Namespace,
			Labels:    map[string]string{nightshiftv1alpha1.ShiftLabel: shift.Name},
		},
		Spec: nightshiftv1alpha1.PassSpec{ShiftName: shift.Name},
	}
	if err := controllerutil.SetControllerReference(shift, p, r.Scheme); err != nil {
		return nil, err
	}
	if err := r.Create(ctx, p); err != nil && !apierrors.IsAlreadyExists(err) {
		return nil, err
	}
	return p, nil
}

// pruneHistory supprime les passes terminées au-delà des limites d'historique,
// les plus anciennes d'abord.
func (r *ShiftReconciler) pruneHistory(ctx context.Context, shift *nightshiftv1alpha1.Shift, passes []nightshiftv1alpha1.Pass) error {
	limits := map[nightshiftv1alpha1.PassPhase]int{
		nightshiftv1alpha1.PassSucceeded: limit(shift.Spec.SuccessfulPassesHistoryLimit, 3),
		nightshiftv1alpha1.PassFailed:    limit(shift.Spec.FailedPassesHistoryLimit, 1),
	}
	for phase, keep := range limits {
		var done []nightshiftv1alpha1.Pass
		for _, p := range passes {
			if p.Status.Phase == phase {
				done = append(done, p)
			}
		}
		slices.SortFunc(done, func(a, b nightshiftv1alpha1.Pass) int {
			return cmp.Or(finishedAt(b).Compare(finishedAt(a)), cmp.Compare(a.Name, b.Name))
		})
		for i := keep; i < len(done); i++ {
			if err := r.Delete(ctx, &done[i], client.PropagationPolicy(metav1.DeletePropagationBackground)); client.IgnoreNotFound(err) != nil {
				return err
			}
		}
	}
	return nil
}

func limit(v *int32, def int) int {
	if v == nil {
		return def
	}
	return int(*v)
}

func finishedAt(p nightshiftv1alpha1.Pass) time.Time {
	if p.Status.CompletionTime != nil {
		return p.Status.CompletionTime.Time
	}
	return p.CreationTimestamp.Time
}

// SetupWithManager sets up the controller with the Manager.
func (r *ShiftReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Clock == nil {
		r.Clock = clock.RealClock{}
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&nightshiftv1alpha1.Shift{}).
		Owns(&nightshiftv1alpha1.Pass{}).
		Named("shift").
		Complete(r)
}
