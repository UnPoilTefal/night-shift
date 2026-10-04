// Package solo vérifie l'exemple de déploiement du palier solo.
package solo

import (
	"os"
	"slices"
	"strings"
	"testing"

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
		Name     string `yaml:"name"`
		ReadOnly bool   `yaml:"readOnly"`
	} `yaml:"volumeMounts"`
}

type cronJob struct {
	Spec struct {
		ConcurrencyPolicy string `yaml:"concurrencyPolicy"`
		JobTemplate       struct {
			Spec struct {
				Template struct {
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

func TestAgentContainerReceivesNoForgeToken(t *testing.T) {
	spec := load(t).Spec.JobTemplate.Spec.Template.Spec
	all := append(slices.Clone(spec.InitContainers), spec.Containers...)
	byName := map[string]container{}
	for _, c := range all {
		byName[c.Name] = c
	}
	agent, ok := byName["agent"]
	if !ok || len(spec.InitContainers) != 2 || spec.InitContainers[0].Name != "select" || spec.InitContainers[1].Name != "agent" ||
		len(spec.Containers) != 1 || spec.Containers[0].Name != "publish" {
		t.Fatal("attendu les initContainers select puis agent, et le conteneur publish")
	}
	secrets := forgeSecrets(all)
	if len(secrets) == 0 {
		t.Fatal("aucun secret de forge trouvé pour select et publish")
	}

	for _, e := range agent.Env {
		if slices.Contains([]string{"NIGHT_SHIFT_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN", "NIGHT_SHIFT_DISCORD_WEBHOOK"}, e.Name) {
			t.Errorf("l'agent reçoit la variable %s", e.Name)
		}
		if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil && slices.Contains(secrets, e.ValueFrom.SecretKeyRef.Name) {
			t.Errorf("l'agent lit le secret de forge %s", e.ValueFrom.SecretKeyRef.Name)
		}
	}
	for _, ef := range agent.EnvFrom {
		if ef.SecretRef != nil && slices.Contains(secrets, ef.SecretRef.Name) {
			t.Errorf("l'agent importe le secret de forge %s", ef.SecretRef.Name)
		}
	}
	secretVolumes := map[string]bool{}
	for _, v := range spec.Volumes {
		if (v.Secret != nil && slices.Contains(secrets, v.Secret.SecretName)) || v.Projected != nil {
			secretVolumes[v.Name] = true
		}
	}
	for _, m := range agent.VolumeMounts {
		if secretVolumes[m.Name] {
			t.Errorf("l'agent monte le volume %s, qui porte un secret", m.Name)
		}
	}
	if spec.AutomountServiceAccountToken == nil || *spec.AutomountServiceAccountToken {
		t.Error("le jeton de compte de service doit rester hors du pod")
	}
}

func TestOnlySelectWritesTheTaskAndPublishOnlyReads(t *testing.T) {
	spec := load(t).Spec.JobTemplate.Spec.Template.Spec
	mounts := func(c container) map[string]bool {
		ro := map[string]bool{}
		for _, m := range c.VolumeMounts {
			ro[m.Name] = m.ReadOnly
		}
		return ro
	}
	agent, publish := mounts(spec.InitContainers[1]), mounts(spec.Containers[0])
	if !agent["state"] || !publish["state"] || !publish["work"] {
		t.Fatalf("montages : agent %v, publish %v ; attendu state en lecture seule pour l'agent, state et work pour publish", agent, publish)
	}
	if load(t).Spec.ConcurrencyPolicy != "Forbid" {
		t.Fatal("une seule passe active à la fois : concurrencyPolicy Forbid attendu")
	}
}

func TestEveryStageRunsNightShiftFromThePublishedImages(t *testing.T) {
	spec := load(t).Spec.JobTemplate.Spec.Template.Spec
	images := map[string]string{
		"select":  "ghcr.io/unpoiltefal/night-shift:",
		"agent":   "ghcr.io/unpoiltefal/night-shift-go:",
		"publish": "ghcr.io/unpoiltefal/night-shift:",
	}
	for _, c := range append(slices.Clone(spec.InitContainers), spec.Containers...) {
		// Les images n'ont pas d'ENTRYPOINT : la commande doit être explicite.
		if !slices.Equal(c.Command, []string{"night-shift"}) || len(c.Args) == 0 || c.Args[0] != c.Name {
			t.Errorf("%s : command %q, args %q ; attendu night-shift %s …", c.Name, c.Command, c.Args, c.Name)
		}
		if want, ok := images[c.Name]; !ok || !strings.HasPrefix(c.Image, want) {
			t.Errorf("%s : image %q, attendu %s<version>", c.Name, c.Image, images[c.Name])
		}
	}
}
