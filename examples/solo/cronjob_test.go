// Package solo vérifie l'exemple de déploiement du palier solo.
package solo

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

type envVar struct {
	Name      string `yaml:"name"`
	ValueFrom *struct {
		SecretKeyRef *struct {
			Name string `yaml:"name"`
		} `yaml:"secretKeyRef"`
	} `yaml:"valueFrom"`
}

type container struct {
	Name    string   `yaml:"name"`
	Image   string   `yaml:"image"`
	Command []string `yaml:"command"`
	Args    []string `yaml:"args"`
	Env     []envVar `yaml:"env"`
	EnvFrom []struct {
		SecretRef *struct {
			Name string `yaml:"name"`
		} `yaml:"secretRef"`
	} `yaml:"envFrom"`
	VolumeMounts []struct {
		Name      string `yaml:"name"`
		MountPath string `yaml:"mountPath"`
		ReadOnly  bool   `yaml:"readOnly"`
	} `yaml:"volumeMounts"`
}

type cronJob struct {
	Spec struct {
		ConcurrencyPolicy          string `yaml:"concurrencyPolicy"`
		SuccessfulJobsHistoryLimit *int   `yaml:"successfulJobsHistoryLimit"`
		FailedJobsHistoryLimit     *int   `yaml:"failedJobsHistoryLimit"`
		JobTemplate                struct {
			Spec struct {
				ActiveDeadlineSeconds int `yaml:"activeDeadlineSeconds"`
				Template              struct {
					Spec struct {
						AutomountServiceAccountToken *bool       `yaml:"automountServiceAccountToken"`
						InitContainers               []container `yaml:"initContainers"`
						Containers                   []container `yaml:"containers"`
						Volumes                      []struct {
							Name   string `yaml:"name"`
							Secret *struct {
								SecretName string `yaml:"secretName"`
							} `yaml:"secret"`
							Projected any `yaml:"projected"`
						} `yaml:"volumes"`
					} `yaml:"spec"`
				} `yaml:"template"`
			} `yaml:"spec"`
		} `yaml:"jobTemplate"`
	} `yaml:"spec"`
}

func load(t *testing.T) cronJob {
	t.Helper()
	b, err := os.ReadFile("cronjob.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var cj cronJob
	if err := yaml.Unmarshal(b, &cj); err != nil {
		t.Fatal(err)
	}
	return cj
}

// forgeSecrets rend les secrets d'où les étapes de confiance tirent le jeton
// de forge.
func forgeSecrets(cs []container) []string {
	var out []string
	for _, c := range cs {
		for _, e := range c.Env {
			if e.Name == "NIGHT_SHIFT_GITHUB_TOKEN" && e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil {
				out = append(out, e.ValueFrom.SecretKeyRef.Name)
			}
		}
	}
	return out
}

// stages est l'ordre des conteneurs du pod : les étapes du premier tour,
// puis celles de la relance de l'agent si la CI de la PR échoue.
var stages = []string{"select", "agent", "publish", "retry-agent", "retry-publish"}

func TestAgentContainersReceiveNoForgeToken(t *testing.T) {
	spec := load(t).Spec.JobTemplate.Spec.Template.Spec
	all := append(slices.Clone(spec.InitContainers), spec.Containers...)
	var names []string
	for _, c := range all {
		names = append(names, c.Name)
	}
	if !slices.Equal(names, stages) || len(spec.Containers) != 1 {
		t.Fatalf("conteneurs = %v, attendu les initContainers puis le conteneur %v", names, stages)
	}
	secrets := forgeSecrets(all)
	if len(secrets) == 0 {
		t.Fatal("aucun secret de forge trouvé pour select et publish")
	}
	secretVolumes := map[string]bool{}
	for _, v := range spec.Volumes {
		if (v.Secret != nil && slices.Contains(secrets, v.Secret.SecretName)) || v.Projected != nil {
			secretVolumes[v.Name] = true
		}
	}

	if len(all) != len(stages) {
		return
	}
	for _, agent := range []container{all[1], all[3]} {
		for _, e := range agent.Env {
			if slices.Contains([]string{"NIGHT_SHIFT_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN", "NIGHT_SHIFT_DISCORD_WEBHOOK"}, e.Name) {
				t.Errorf("%s reçoit la variable %s", agent.Name, e.Name)
			}
			if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil && slices.Contains(secrets, e.ValueFrom.SecretKeyRef.Name) {
				t.Errorf("%s lit le secret de forge %s", agent.Name, e.ValueFrom.SecretKeyRef.Name)
			}
		}
		for _, ef := range agent.EnvFrom {
			if ef.SecretRef != nil && slices.Contains(secrets, ef.SecretRef.Name) {
				t.Errorf("%s importe le secret de forge %s", agent.Name, ef.SecretRef.Name)
			}
		}
		for _, m := range agent.VolumeMounts {
			if secretVolumes[m.Name] {
				t.Errorf("%s monte le volume %s, qui porte un secret", agent.Name, m.Name)
			}
		}
	}
	if spec.AutomountServiceAccountToken == nil || *spec.AutomountServiceAccountToken {
		t.Error("le jeton de compte de service doit rester hors du pod")
	}
}

func TestEachTaskIsWrittenByATrustedStageAndReadOnlyForTheAgent(t *testing.T) {
	spec := load(t).Spec.JobTemplate.Spec.Template.Spec
	mounts := map[string]map[string]bool{}
	for _, c := range append(slices.Clone(spec.InitContainers), spec.Containers...) {
		ro := map[string]bool{}
		for _, m := range c.VolumeMounts {
			ro[m.Name] = m.ReadOnly
		}
		mounts[c.Name] = ro
	}
	// nom du conteneur → volume → lecture seule attendue.
	want := map[string]map[string]bool{
		"select":        {"state": false, "work": false},
		"agent":         {"state": true, "work": false},
		"publish":       {"state": true, "work": true, "state2": false, "work2": false},
		"retry-agent":   {"state2": true, "work2": false},
		"retry-publish": {"state2": true, "work2": true},
	}
	for name, vols := range want {
		for v, ro := range vols {
			got, ok := mounts[name][v]
			if !ok || got != ro {
				t.Errorf("%s : volume %s monté = %t, lecture seule = %t ; attendu monté en lecture seule = %t", name, v, ok, got, ro)
			}
		}
	}
	for _, v := range []string{"state", "work"} {
		if _, ok := mounts["retry-agent"][v]; ok {
			t.Errorf("l'agent relancé monte %s, le volume du premier tour", v)
		}
	}
	if load(t).Spec.ConcurrencyPolicy != "Forbid" {
		t.Fatal("une seule passe active à la fois : concurrencyPolicy Forbid attendu")
	}
}

func TestStageVolumeFlagsPointAtTheirMounts(t *testing.T) {
	spec := load(t).Spec.JobTemplate.Spec.Template.Spec
	for _, c := range append(slices.Clone(spec.InitContainers), spec.Containers...) {
		paths := map[string]bool{}
		for _, m := range c.VolumeMounts {
			paths[m.MountPath] = true
		}
		for _, f := range []string{"--state", "--work", "--next-state", "--next-work"} {
			if p := flagValue(c.Args, f); p != "" && !paths[p] {
				t.Errorf("%s : %s %s ne correspond à aucun montage", c.Name, f, p)
			}
		}
	}
}

// flagValue rend la valeur d'un drapeau dans des arguments.
func flagValue(args []string, name string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == name {
			return args[i+1]
		}
	}
	return ""
}

func TestStagesChainTheRetryAndFitTheDeadline(t *testing.T) {
	cj := load(t)
	spec := cj.Spec.JobTemplate.Spec.Template.Spec
	all := append(slices.Clone(spec.InitContainers), spec.Containers...)
	if len(all) != len(stages) {
		t.Fatalf("%d conteneurs, attendu %v", len(all), stages)
	}
	publish, retry := all[2].Args, all[4].Args
	if flagValue(publish, "--next-state") == "" || flagValue(publish, "--next-work") == "" || !slices.Contains(retry, "--follow-up") {
		t.Fatalf("publish %q, retry-publish %q : attendu la préparation puis la Publication de la relance", publish, retry)
	}
	var total time.Duration
	for _, c := range []struct {
		args []string
		flag string
	}{{all[1].Args, "--timeout"}, {publish, "--ci-timeout"}, {all[3].Args, "--timeout"}, {retry, "--ci-timeout"}} {
		d, err := time.ParseDuration(flagValue(c.args, c.flag))
		if err != nil {
			t.Fatalf("%q : %s illisible : %v", c.args, c.flag, err)
		}
		total += d
	}
	// Chaque étape garde quelques minutes pour cloner, pousser et rendre le
	// ticket avant l'échéance du pod.
	if deadline := time.Duration(cj.Spec.JobTemplate.Spec.ActiveDeadlineSeconds) * time.Second; deadline < total+5*time.Minute {
		t.Fatalf("activeDeadlineSeconds = %s, attendu au moins %s (agents et attentes de CI, plus une marge)", deadline, total+5*time.Minute)
	}
}

func TestEveryStageRunsNightShiftFromThePublishedImages(t *testing.T) {
	spec := load(t).Spec.JobTemplate.Spec.Template.Spec
	images := map[string]string{
		"select":        "ghcr.io/unpoiltefal/night-shift:",
		"agent":         "ghcr.io/unpoiltefal/night-shift-go:",
		"publish":       "ghcr.io/unpoiltefal/night-shift:",
		"retry-agent":   "ghcr.io/unpoiltefal/night-shift-go:",
		"retry-publish": "ghcr.io/unpoiltefal/night-shift:",
	}
	for _, c := range append(slices.Clone(spec.InitContainers), spec.Containers...) {
		// Les images n'ont pas d'ENTRYPOINT : la commande doit être explicite.
		cmd := strings.TrimPrefix(c.Name, "retry-")
		if !slices.Equal(c.Command, []string{"night-shift"}) || len(c.Args) == 0 || c.Args[0] != cmd {
			t.Errorf("%s : command %q, args %q ; attendu night-shift %s …", c.Name, c.Command, c.Args, cmd)
		}
		if want, ok := images[c.Name]; !ok || !strings.HasPrefix(c.Image, want) {
			t.Errorf("%s : image %q, attendu %s<version>", c.Name, c.Image, images[c.Name])
		}
	}
}

func TestRecentPassesStayInspectableInTheCluster(t *testing.T) {
	spec := load(t).Spec
	// Le détail d'une passe ne vit que dans les logs de ses pods : sans
	// historique, Kubernetes ne garde qu'un seul Job échoué.
	for name, limit := range map[string]*int{
		"successfulJobsHistoryLimit": spec.SuccessfulJobsHistoryLimit,
		"failedJobsHistoryLimit":     spec.FailedJobsHistoryLimit,
	} {
		if limit == nil || *limit < 7 {
			t.Errorf("%s = %v, attendu au moins une semaine de passes (7)", name, limit)
		}
	}
}
