package compose

import (
	"bytes"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/UnPoilTefal/night-shift/internal/cli"
)

func load(t *testing.T) example {
	t.Helper()
	ex, err := loadDir(".", nil)
	if err != nil {
		t.Fatal(err)
	}
	return ex
}

func TestExampleKeepsEveryGuarantee(t *testing.T) {
	if v := check(load(t)); len(v) > 0 {
		t.Fatalf("règles enfreintes :\n%s", strings.Join(v, "\n"))
	}
}

func TestTheDocumentedGatewayExceptionIsAdmitted(t *testing.T) {
	ex := load(t)
	squidReplace("http_access deny private_dst",
		"acl llm_gateway_name dstdomain llm.internal.example\nacl llm_gateway_addr dst 10.20.30.40/32\n"+
			"http_access allow CONNECT SSL_ports llm_gateway_name llm_gateway_addr\nhttp_access deny private_dst")(&ex)
	if v := check(ex); len(v) > 0 {
		t.Fatalf("l'exception documentée (docs/compose.md) est refusée :\n%s", strings.Join(v, "\n"))
	}
}

// mutation modifie une copie de l'exemple ; want est un fragment de la
// règle qui doit alors être enfreinte.
type mutation struct {
	name   string
	mutate func(ex *example)
	want   string
}

func setSvc(name, key string, value any) func(ex *example) {
	return func(ex *example) { ex.service(name)[key] = value }
}

func setEnv(name, key, value string) func(ex *example) {
	return func(ex *example) {
		e := asMap(ex.service(name)["environment"])
		// Les ancres partagent la table : on la copie avant d'écrire.
		c := map[string]any{}
		for k, v := range e {
			c[k] = v
		}
		c[key] = value
		ex.service(name)["environment"] = c
	}
}

func addVolume(name, spec string) func(ex *example) {
	return func(ex *example) {
		ex.service(name)["volumes"] = append(slices.Clone(asList(ex.service(name)["volumes"])), spec)
	}
}

func squidReplace(old, repl string) func(ex *example) {
	return func(ex *example) {
		if !strings.Contains(ex.squid, old) {
			panic("fragment absent de squid.conf : " + old)
		}
		ex.squid = strings.Replace(ex.squid, old, repl, 1)
	}
}

func TestEachRuleFailsWhenBroken(t *testing.T) {
	for _, m := range []mutation{
		{"env_file", setSvc("agent", "env_file", ".env"), "env_file interdit"},
		{"extends", setSvc("select", "extends", map[string]any{"service": "agent"}), "extends interdit"},
		{"include", func(ex *example) { ex.project["include"] = []any{"other.yaml"} }, "include interdit"},
		{"restart", setSvc("publish", "restart", "no"), "publish : restart"},
		{"read_only", setSvc("control", "read_only", false), "control : read_only"},
		{"cap_drop", setSvc("select", "cap_drop", []any{"NET_RAW"}), "select : cap_drop"},
		{"no-new-privileges", setSvc("agent", "security_opt", []any{}), "agent : security_opt"},
		{"utilisateur", setSvc("retry-publish", "user", "0:0"), "retry-publish : user"},
		{"journaux", setSvc("proxy", "logging", map[string]any{"driver": "local"}), "proxy : journaux"},
		{"socket Docker", addVolume("control", "/var/run/docker.sock:/var/run/docker.sock"), "socket Docker"},
		{"privileged", setSvc("publish", "privileged", true), "privileged interdit"},
		{"cap_add du proxy", setSvc("proxy", "cap_add", []any{"SETUID", "SETGID", "NET_ADMIN"}), "proxy : cap_add"},
		{"digest du proxy", setSvc("proxy", "image", "ubuntu/squid:latest"), "épinglée par digest"},
		{"cap_add hors proxy", setSvc("control", "cap_add", []any{"CHOWN"}), "control : cap_add interdit"},
		{"réseau de l'agent", setSvc("agent", "networks", []any{"agent-net", "trusted-net"}), "agent : réseaux"},
		{"network_mode", setSvc("retry-agent", "network_mode", "host"), "network_mode interdit"},
		{"extra_hosts", setSvc("agent", "extra_hosts", []any{"host.docker.internal:host-gateway"}), "extra_hosts interdit"},
		{"dns", setSvc("agent", "dns", []any{"1.1.1.1"}), "dns interdit"},
		{"port de l'agent", setSvc("agent", "ports", []any{"127.0.0.1:9000:9000"}), "agent : ports"},
		{"secret de forge à l'agent", setSvc("agent", "secrets", []any{"model_token", "forge_token"}), "agent : secrets"},
		{"variable de forge à l'agent", setEnv("retry-agent", cli.TokenEnv+cli.FileSuffix, "/run/secrets/forge_token"), "secret des étapes de confiance"},
		{"interpolation", setEnv("agent", "DEPLOY", "${NIGHT_SHIFT_GITLAB_TOKEN}"), "interpolation de NIGHT_SHIFT_GITLAB_TOKEN"},
		{"volume persistant à l'agent", addVolume("agent", "transcripts:/transcripts"), "hors de la passe"},
		{"dossier des secrets à l'agent", addVolume("agent", "./secrets:/secrets:ro"), "montage de l'hôte"},
		{"tâche en écriture", func(ex *example) {
			ex.service("agent")["volumes"] = []any{"state:/state", "work:/work"}
		}, "lecture seule"},
		{"CLAUDE_CONFIG_DIR hors tmpfs", setEnv("agent", "CLAUDE_CONFIG_DIR", "/work/.claude"), "CLAUDE_CONFIG_DIR"},
		{"proxy de l'agent", setEnv("agent", "HTTPS_PROXY", ""), "HTTPS_PROXY"},
		{"NO_PROXY", setEnv("retry-agent", "NO_PROXY", "*"), "NO_PROXY"},
		{"GOPROXY direct", setEnv("agent", "GOPROXY", "https://proxy.golang.org,direct"), "GOPROXY"},
		{"init", setSvc("agent", "init", true), "init: false"},
		{"pids_limit", func(ex *example) { delete(ex.service("retry-agent"), "pids_limit") }, "pids_limit"},
		{"secret du modèle en confiance", setSvc("publish", "secrets", []any{"forge_token", "model_token"}), "publish : secret du modèle"},
		{"variable du modèle en confiance", setEnv("select", "ANTHROPIC_API_KEY_FILE", "/run/secrets/model_token"), "secret du modèle"},
		{"port hors control", setSvc("publish", "ports", []any{"127.0.0.1:9000:9000"}), "publish : ports publiés"},
		{"port sur toutes les adresses", setSvc("control", "ports", []any{"8081:8081"}), "127.0.0.1 seulement"},
		{"history hors control", addVolume("publish", "history:/history"), "history est réservé"},
		{"transcripts à select", addVolume("select", "transcripts:/transcripts"), "transcripts est réservé"},
		{"agent-net externe", func(ex *example) { asMap(asMap(ex.project["networks"])["agent-net"])["internal"] = false }, "agent-net : internal"},
		{"control-net externe", func(ex *example) { asMap(ex.project["networks"])["control-net"] = map[string]any{} }, "control-net : internal"},
		{"ipv6", func(ex *example) { asMap(ex.project["networks"])["trusted-net"] = map[string]any{"enable_ipv6": true} }, "enable_ipv6"},
		{"service de plus sur agent-net", setSvc("control", "networks", []any{"control-net", "admin-net", "agent-net"}), "agent-net ="},
		{"service de plus sur control-net", func(ex *example) {
			ex.service("proxy")["networks"] = []any{"agent-net", "trusted-net", "control-net"}
		}, "control-net ="},
		{"HTTP en clair", squidReplace("http_access deny !CONNECT\n", ""), "les deux premières règles"},
		{"port autre que 443", squidReplace("acl SSL_ports port 443", "acl SSL_ports port 443 22"), "SSL_ports"},
		{"plages privées après les listes", squidReplace("http_access deny private_dst\nhttp_access allow model\n", "http_access allow model\nhttp_access deny private_dst\n"), "plages privées"},
		{"plage privée manquante", squidReplace("10.0.0.0/8 ", ""), "private_dst sans 10.0.0.0/8"},
		{"allow all", squidReplace("http_access deny all", "http_access allow all"), "deny all doit être la dernière"},
		{"cache", squidReplace("cache deny all", "cache allow all"), "cache deny all"},
		{"deux exceptions privées", squidReplace("http_access deny private_dst",
			"acl gw dstdomain llm.example\nacl gwip dst 10.0.0.1/32\nhttp_access allow CONNECT SSL_ports gw gwip\nhttp_access allow CONNECT SSL_ports gw gwip\nhttp_access deny private_dst"), "plages privées"},
		{"exception sans adresse", squidReplace("http_access deny private_dst",
			"acl gw dstdomain llm.example\nacl gwnet dst 10.0.0.0/8\nhttp_access allow CONNECT SSL_ports gw gwnet\nhttp_access deny private_dst"), "adresse exacte"},
		{"liste hors allowlist", squidReplace("http_access allow toolchain", "http_access allow toolchain\nacl any_domain dstdomain .example.com\nhttp_access allow any_domain"), "attendu allow d'une liste"},
		{"domaine de premier niveau", func(ex *example) { ex.lists["toolchain.txt"] += ".com\n" }, "premier niveau"},
		{"domaine de forge", func(ex *example) { ex.lists["model.txt"] += "api.github.com\n" }, "domaine de forge"},
	} {
		t.Run(m.name, func(t *testing.T) {
			ex := load(t)
			m.mutate(&ex)
			v := check(ex)
			if !slices.ContainsFunc(v, func(s string) bool { return strings.Contains(s, m.want) }) {
				t.Fatalf("règle %q non signalée ; violations :\n%s", m.want, strings.Join(v, "\n"))
			}
		})
	}
}

// interpolate remplace les variables de Compose par leur défaut, ou par
// celles de vars pour une variable requise.
func interpolate(s string, vars map[string]string) string {
	re := regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::([-?])([^}]*))?\}`)
	return re.ReplaceAllStringFunc(s, func(m string) string {
		p := re.FindStringSubmatch(m)
		if v, ok := vars[p[1]]; ok {
			return v
		}
		if p[2] == "-" {
			return p[3]
		}
		return ""
	})
}

// TestEveryOptionExistsInTheBinary fait lire la commande de chaque service
// au vrai binaire, sans aucun secret : chaque étape doit refuser de démarrer
// faute de jeton ou hors du PID 1, avant tout effet, jamais sur une option
// inconnue ou mal formée.
func TestEveryOptionExistsInTheBinary(t *testing.T) {
	for _, k := range append(append(slices.Clone(trustedSecretEnv), modelSecretEnv...), secretEnvNames(cli.ControlTokenEnv)...) {
		t.Setenv(k, "")
	}
	vars := map[string]string{"NIGHT_SHIFT_REPO": "owner/repo"}
	ex := load(t)
	for name, s := range ex.services() {
		if name == "proxy" {
			continue
		}
		var args []string
		for _, a := range asList(asMap(s)["command"]) {
			args = append(args, interpolate(fmt.Sprint(a), vars))
		}
		t.Run(name, func(t *testing.T) {
			if len(args) < 2 || args[0] != "night-shift" {
				t.Fatalf("commande = %q, attendu night-shift …", args)
			}
			var stdout, stderr bytes.Buffer
			done := make(chan int, 1)
			go func() { done <- cli.Run(args[1:], &stdout, &stderr) }()
			select {
			case code := <-done:
				out := stderr.String()
				for _, bad := range []string{"flag provided but not defined", "invalid value", "inconnue", "requis", "exige --control"} {
					if strings.Contains(out, bad) {
						t.Fatalf("commande %q refusée par le binaire :\n%s", args, out)
					}
				}
				if code != 2 {
					t.Fatalf("code = %d, attendu 2 (secret absent ou hors PID 1) :\n%s", code, out)
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("commande %q : le binaire a démarré au lieu de s'arrêter faute de secret", args)
			}
		})
	}
}
