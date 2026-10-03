/*
Copyright 2026 UnPoilTefal.
SPDX-License-Identifier: MIT
*/

package controller

import (
	"cmp"
	"context"
	"slices"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	nightshiftv1alpha1 "github.com/UnPoilTefal/night-shift/api/v1alpha1"
)

// OrchestratorImage est l'image du Job orchestrateur factice de l'incrément 0.
const OrchestratorImage = "busybox:1.37.0"

// waitRequeue espace les vérifications d'une passe qui attend la fin d'une
// passe plus ancienne de son poste.
const waitRequeue = 15 * time.Second

// shiftWaitTimeout est le délai au-delà duquel une passe dont le poste reste
// introuvable échoue.
const shiftWaitTimeout = 2 * time.Minute

// passDeadline borne la durée d'un Job orchestrateur, y compris un pod qui ne
// démarre jamais : sans elle, la passe resterait active et bloquerait son
// poste. Elle reprend la durée maximale de passe par défaut de la spec.
const passDeadline = 30 * time.Minute

// PassReconciler traduit une passe en Job orchestrateur et reporte l'état du
// Job dans celui de la passe.
type PassReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Clock  clock.PassiveClock
}

// +kubebuilder:rbac:groups=nightshift.unpoiltefal.github.io,resources=passes,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=nightshift.unpoiltefal.github.io,resources=passes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create
// +kubebuilder:rbac:groups=nightshift.unpoiltefal.github.io,resources=passes/finalizers,verbs=update

// Reconcile rattache la passe à son poste, lance son Job quand vient son tour
// et suit l'état du Job.
func (r *PassReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var pass nightshiftv1alpha1.Pass
	if err := r.Get(ctx, req.NamespacedName, &pass); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if pass.Status.Phase.Finished() {
		return ctrl.Result{}, nil
	}

	var shift nightshiftv1alpha1.Shift
	if err := r.Get(ctx, types.NamespacedName{Namespace: pass.Namespace, Name: pass.Spec.ShiftName}, &shift); err != nil {
		if apierrors.IsNotFound(err) {
			// Un poste et une passe appliqués ensemble peuvent arriver dans
			// le désordre : la passe attend son poste avant d'échouer.
			msg := "shift " + pass.Spec.ShiftName + " does not exist"
			if r.Clock.Since(pass.CreationTimestamp.Time) >= shiftWaitTimeout {
				return ctrl.Result{}, r.finish(ctx, &pass, nightshiftv1alpha1.PassFailed, "ShiftNotFound", msg)
			}
			pass.Status.Phase = nightshiftv1alpha1.PassPending
			meta.SetStatusCondition(&pass.Status.Conditions, metav1.Condition{
				Type: "Succeeded", Status: metav1.ConditionUnknown, Reason: "ShiftNotFound", Message: msg + " yet",
				ObservedGeneration: pass.Generation,
			})
			return ctrl.Result{RequeueAfter: waitRequeue}, r.Status().Update(ctx, &pass)
		}
		return ctrl.Result{}, err
	}

	// Une passe créée à la main est rattachée à son poste, pour l'historique
	// et le nettoyage.
	if adopted, err := r.adopt(ctx, &pass, &shift); err != nil || adopted {
		return ctrl.Result{}, err
	}

	var job batchv1.Job
	err := r.Get(ctx, types.NamespacedName{Namespace: pass.Namespace, Name: pass.Name}, &job)
	switch {
	case apierrors.IsNotFound(err):
		first, err := r.isOldestUnfinished(ctx, &pass)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !first {
			pass.Status.Phase = nightshiftv1alpha1.PassPending
			return ctrl.Result{RequeueAfter: waitRequeue}, r.Status().Update(ctx, &pass)
		}
		j, err := r.createJob(ctx, &pass)
		if err != nil {
			return ctrl.Result{}, err
		}
		log.Info("orchestrator job created", "job", j.Name)
		pass.Status.Phase = nightshiftv1alpha1.PassRunning
		pass.Status.JobName = j.Name
		pass.Status.StartTime = &metav1.Time{Time: r.Clock.Now()}
		return ctrl.Result{}, r.Status().Update(ctx, &pass)
	case err != nil:
		return ctrl.Result{}, err
	}

	switch {
	case jobCondition(&job, batchv1.JobComplete):
		return ctrl.Result{}, r.finish(ctx, &pass, nightshiftv1alpha1.PassSucceeded, "JobComplete", "orchestrator job completed")
	case jobCondition(&job, batchv1.JobFailed):
		return ctrl.Result{}, r.finish(ctx, &pass, nightshiftv1alpha1.PassFailed, "JobFailed", "orchestrator job failed")
	}
	if pass.Status.Phase != nightshiftv1alpha1.PassRunning || pass.Status.JobName != job.Name {
		pass.Status.Phase = nightshiftv1alpha1.PassRunning
		pass.Status.JobName = job.Name
		return ctrl.Result{}, r.Status().Update(ctx, &pass)
	}
	return ctrl.Result{}, nil
}

func (r *PassReconciler) adopt(ctx context.Context, pass *nightshiftv1alpha1.Pass, shift *nightshiftv1alpha1.Shift) (bool, error) {
	changed := false
	if pass.Labels[nightshiftv1alpha1.ShiftLabel] != shift.Name {
		if pass.Labels == nil {
			pass.Labels = map[string]string{}
		}
		pass.Labels[nightshiftv1alpha1.ShiftLabel] = shift.Name
		changed = true
	}
	if metav1.GetControllerOf(pass) == nil {
		if err := controllerutil.SetControllerReference(shift, pass, r.Scheme); err != nil {
			return false, err
		}
		changed = true
	}
	if !changed {
		return false, nil
	}
	return true, r.Update(ctx, pass)
}

// isOldestUnfinished dit si la passe est la plus ancienne passe non terminée
// de son poste : elle seule peut lancer son Job, ce qui garantit une seule
// passe active même si le cache est en retard sur une passe qui vient de
// démarrer.
func (r *PassReconciler) isOldestUnfinished(ctx context.Context, pass *nightshiftv1alpha1.Pass) (bool, error) {
	var list nightshiftv1alpha1.PassList
	if err := r.List(ctx, &list, client.InNamespace(pass.Namespace),
		client.MatchingLabels{nightshiftv1alpha1.ShiftLabel: pass.Spec.ShiftName}); err != nil {
		return false, err
	}
	var unfinished []nightshiftv1alpha1.Pass
	for _, p := range list.Items {
		if !p.Status.Phase.Finished() {
			unfinished = append(unfinished, p)
		}
	}
	oldest := slices.MinFunc(append(unfinished, *pass), func(a, b nightshiftv1alpha1.Pass) int {
		return cmp.Or(a.CreationTimestamp.Compare(b.CreationTimestamp.Time), cmp.Compare(a.Name, b.Name))
	})
	return oldest.Name == pass.Name, nil
}

// createJob crée le Job orchestrateur factice. Son pod porte déjà le
// durcissement attendu des pods de night-shift (ADR 0002, 0004).
func (r *PassReconciler) createJob(ctx context.Context, pass *nightshiftv1alpha1.Pass) (*batchv1.Job, error) {
	labels := map[string]string{nightshiftv1alpha1.ShiftLabel: pass.Spec.ShiftName}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: pass.Name, Namespace: pass.Namespace, Labels: labels},
		Spec: batchv1.JobSpec{
			BackoffLimit:          ptr.To[int32](0),
			ActiveDeadlineSeconds: ptr.To(int64(passDeadline / time.Second)),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					RestartPolicy:                corev1.RestartPolicyNever,
					AutomountServiceAccountToken: ptr.To(false),
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:   ptr.To(true),
						RunAsUser:      ptr.To[int64](65534),
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Containers: []corev1.Container{{
						Name:    "orchestrator",
						Image:   OrchestratorImage,
						Command: []string{"echo", "night-shift pass " + pass.Name},
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: ptr.To(false),
							ReadOnlyRootFilesystem:   ptr.To(true),
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
						},
					}},
				},
			},
		},
	}
	if err := controllerutil.SetControllerReference(pass, job, r.Scheme); err != nil {
		return nil, err
	}
	if err := r.Create(ctx, job); err != nil && !apierrors.IsAlreadyExists(err) {
		return nil, err
	}
	return job, nil
}

func (r *PassReconciler) finish(ctx context.Context, pass *nightshiftv1alpha1.Pass, phase nightshiftv1alpha1.PassPhase, reason, msg string) error {
	pass.Status.Phase = phase
	pass.Status.CompletionTime = &metav1.Time{Time: r.Clock.Now()}
	status := metav1.ConditionTrue
	if phase == nightshiftv1alpha1.PassFailed {
		status = metav1.ConditionFalse
	}
	meta.SetStatusCondition(&pass.Status.Conditions, metav1.Condition{
		Type: "Succeeded", Status: status, Reason: reason, Message: msg, ObservedGeneration: pass.Generation,
	})
	return r.Status().Update(ctx, pass)
}

func jobCondition(job *batchv1.Job, t batchv1.JobConditionType) bool {
	for _, c := range job.Status.Conditions {
		if c.Type == t && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// SetupWithManager sets up the controller with the Manager.
func (r *PassReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Clock == nil {
		r.Clock = clock.RealClock{}
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&nightshiftv1alpha1.Pass{}).
		Owns(&batchv1.Job{}).
		Named("pass").
		Complete(r)
}
