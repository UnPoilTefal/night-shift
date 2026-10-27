// Package compose vérifie l'exemple Docker Compose du palier solo, sans
// Docker : compose.yaml, squid.conf et les listes d'autorisation. Chaque
// règle a son cas d'échec, joué sur une copie modifiée de l'exemple.
package compose

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/UnPoilTefal/night-shift/internal/cli"
)

// Rôles des services.
var (
	agents  = []string{"agent", "retry-agent"}
	trusted = []string{"select", "publish", "retry-publish"}
	// passVolumes sont les volumes de la passe que chaque agent monte : la
	// tâche, en lecture seule, et le travail. select les vide avant chaque
	// passe.
	passVolumes = map[string][2]string{"agent": {"state", "work"}, "retry-agent": {"state2", "work2"}}
)

// example est l'exemple chargé : le projet Compose et les fichiers du proxy.
type example struct {
	project map[string]any
	squid   string
	lists   map[string]string
}

func load(t *testing.T) example {
	t.Helper()
	b, err := os.ReadFile("compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var p map[string]any
	if err := yaml.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	squid, err := os.ReadFile("squid.conf")
	if err != nil {
		t.Fatal(err)
	}
	ex := example{project: p, squid: string(squid), lists: map[string]string{}}
	names, err := filepath.Glob("allowlist/*.txt")
	if err != nil || len(names) == 0 {
		t.Fatalf("listes d'autorisation introuvables : %v", err)
	}
	for _, n := range names {
		b, err := os.ReadFile(n) // #nosec G304 -- fichier de l'exemple
		if err != nil {
			t.Fatal(err)
		}
		ex.lists[filepath.Base(n)] = string(b)
	}
	return ex
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

func (ex example) services() map[string]any { return asMap(ex.project["services"]) }
func (ex example) service(name string) map[string]any {
	return asMap(ex.services()[name])
}

// strs rend une liste YAML de chaînes, ou les clés d'une table (forme longue
// des réseaux d'un service).
func strs(v any) []string {
	var out []string
	for _, x := range asList(v) {
		if s, ok := x.(string); ok {
			out = append(out, s)
		} else if m := asMap(x); m != nil {
			if s, ok := m["source"].(string); ok {
				out = append(out, s)
			}
		}
	}
	for k := range asMap(v) {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// env rend l'environnement d'un service, forme table ou liste.
func env(svc map[string]any) map[string]string {
	out := map[string]string{}
	for k, v := range asMap(svc["environment"]) {
		out[k] = fmt.Sprint(v)
		if v == nil {
			out[k] = ""
		}
	}
	for _, kv := range strs(svc["environment"]) {
		if k, v, ok := strings.Cut(kv, "="); ok {
			out[k] = v
		}
	}
	return out
}

// mount est un montage de volume d'un service.
type mount struct {
	source, target string
	readOnly, bind bool
}

func mounts(svc map[string]any) []mount {
	var out []mount
	for _, v := range asList(svc["volumes"]) {
		var m mount
		switch x := v.(type) {
		case string:
			parts := strings.Split(x, ":")
			m.source = parts[0]
			if len(parts) > 1 {
				m.target = parts[1]
			}
			m.readOnly = len(parts) > 2 && slices.Contains(strings.Split(parts[2], ","), "ro")
		case map[string]any:
			m.source, _ = x["source"].(string)
			m.target, _ = x["target"].(string)
			m.readOnly, _ = x["read_only"].(bool)
			if typ, _ := x["type"].(string); typ == "bind" {
				m.bind = true
			}
		}
		if strings.HasPrefix(m.source, ".") || strings.HasPrefix(m.source, "/") || strings.HasPrefix(m.source, "$") || strings.HasPrefix(m.source, "~") {
			m.bind = true
		}
		out = append(out, m)
	}
	return out
}

// tmpfsPaths rend les points de montage tmpfs d'un service.
func tmpfsPaths(svc map[string]any) []string {
	var out []string
	for _, v := range append(asList(svc["tmpfs"]), svc["tmpfs"]) {
		if s, ok := v.(string); ok {
			out = append(out, strings.SplitN(s, ":", 2)[0])
		}
	}
	return out
}

// secretEnvNames sont les variables de secret des étapes de confiance, sous
// leurs deux formes.
func secretEnvNames(names ...string) []string {
	var out []string
	for _, n := range names {
		out = append(out, n, n+cli.FileSuffix)
	}
	return out
}

var (
	trustedSecretEnv = secretEnvNames(append(cli.ForgeSecretEnv(), cli.WebhookEnv)...)
	modelSecretEnv   = secretEnvNames("CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY")
)

// allStrings rend toutes les chaînes d'une valeur YAML, récursivement.
func allStrings(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case []any:
		var out []string
		for _, e := range x {
			out = append(out, allStrings(e)...)
		}
		return out
	case map[string]any:
		var out []string
		for k, e := range x {
			out = append(append(out, k), allStrings(e)...)
		}
		return out
	}
	return nil
}

var interpolation = regexp.MustCompile(`\$\{?([A-Za-z_][A-Za-z0-9_]*)`)

// check rend les règles que l'exemple enfreint ; aucune si elles tiennent
// toutes.
func check(ex example) []string {
	var v []string
	fail := func(format string, a ...any) { v = append(v, fmt.Sprintf(format, a...)) }

	// Aucun fichier ni service hérité : tout ce qui tourne est dans
	// compose.yaml.
	if _, ok := ex.project["include"]; ok {
		fail("include interdit")
	}
	if ex.project["name"] != "night-shift" {
		fail("name : night-shift attendu")
	}
	for name, s := range ex.services() {
		svc := asMap(s)
		for _, k := range []string{"env_file", "extends"} {
			if _, ok := svc[k]; ok {
				fail("%s : %s interdit", name, k)
			}
		}
	}
	want := append(append([]string{"control", "proxy"}, agents...), trusted...)
	var got []string
	for name := range ex.services() {
		got = append(got, name)
	}
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		fail("services = %v, attendu %v", got, want)
	}

	v = append(v, checkHardening(ex)...)
	v = append(v, checkAgents(ex)...)
	v = append(v, checkTrusted(ex)...)
	v = append(v, checkNetworks(ex)...)
	v = append(v, checkSquid(ex.squid)...)
	v = append(v, checkLists(ex.lists)...)
	return v
}

func checkHardening(ex example) []string {
	var v []string
	fail := func(format string, a ...any) { v = append(v, fmt.Sprintf(format, a...)) }
	for name, s := range ex.services() {
		svc := asMap(s)
		if svc["restart"] != "unless-stopped" {
			fail("%s : restart: unless-stopped attendu", name)
		}
		if svc["read_only"] != true {
			fail("%s : read_only: true attendu", name)
		}
		if !slices.Contains(strs(svc["cap_drop"]), "ALL") {
			fail("%s : cap_drop: [ALL] attendu", name)
		}
		if !slices.Contains(strs(svc["security_opt"]), "no-new-privileges:true") {
			fail("%s : security_opt no-new-privileges:true attendu", name)
		}
		if _, ok := svc["privileged"]; ok {
			fail("%s : privileged interdit", name)
		}
		for _, k := range []string{"network_mode", "pid", "ipc", "userns_mode", "devices"} {
			if _, ok := svc[k]; ok {
				fail("%s : %s interdit", name, k)
			}
		}
		for _, m := range mounts(svc) {
			if strings.Contains(m.source, "docker.sock") {
				fail("%s : le socket Docker n'est monté nulle part", name)
			}
		}
		logging := asMap(svc["logging"])
		opts := asMap(logging["options"])
		if logging["driver"] != "json-file" || opts["max-size"] != "10m" || opts["max-file"] != "5" {
			fail("%s : journaux json-file, max-size 10m, max-file 5 attendus", name)
		}
		image, _ := svc["image"].(string)
		if name == "proxy" {
			if caps := strs(svc["cap_add"]); !slices.Equal(caps, []string{"SETGID", "SETUID"}) {
				fail("proxy : cap_add = %v, attendu [SETUID, SETGID] seulement", caps)
			}
			if !regexp.MustCompile(`^ubuntu/squid@sha256:[0-9a-f]{64}$`).MatchString(image) {
				fail("proxy : image %q, attendu ubuntu/squid épinglée par digest", image)
			}
			for _, p := range []string{"/var/log/squid", "/var/spool/squid", "/run"} {
				if !slices.Contains(tmpfsPaths(svc), p) {
					fail("proxy : tmpfs %s attendu", p)
				}
			}
			continue
		}
		if _, ok := svc["cap_add"]; ok {
			fail("%s : cap_add interdit", name)
		}
		if svc["user"] != "65532:65532" {
			fail("%s : user 65532:65532 attendu", name)
		}
		if !strings.HasPrefix(image, "ghcr.io/unpoiltefal/night-shift") {
			fail("%s : image %q, attendu une image night-shift", name, image)
		}
		if !slices.Contains(tmpfsPaths(svc), "/tmp") {
			fail("%s : tmpfs /tmp attendu", name)
		}
	}
	return v
}

func checkAgents(ex example) []string {
	var v []string
	fail := func(format string, a ...any) { v = append(v, fmt.Sprintf(format, a...)) }
	for _, name := range agents {
		svc := ex.service(name)
		if nets := strs(svc["networks"]); !slices.Equal(nets, []string{"agent-net"}) {
			fail("%s : réseaux = %v, attendu agent-net seulement", name, nets)
		}
		for _, k := range []string{"extra_hosts", "cap_add", "dns", "dns_search", "dns_opt", "ports", "expose", "links", "external_links"} {
			if _, ok := svc[k]; ok {
				fail("%s : %s interdit", name, k)
			}
		}
		if secrets := strs(svc["secrets"]); !slices.Equal(secrets, []string{"model_token"}) {
			fail("%s : secrets = %v, attendu model_token seulement", name, secrets)
		}
		e := env(svc)
		for k := range e {
			if slices.Contains(trustedSecretEnv, k) {
				fail("%s : variable %s, secret des étapes de confiance", name, k)
			}
		}
		// Aucune interpolation ne fait entrer un secret de confiance sous
		// un autre nom.
		for _, s := range allStrings(svc) {
			for _, m := range interpolation.FindAllStringSubmatch(s, -1) {
				if slices.Contains(trustedSecretEnv, m[1]) || strings.Contains(m[1], "SECRETS_DIR") {
					fail("%s : interpolation de %s", name, m[1])
				}
			}
		}
		pv := passVolumes[name]
		for _, m := range mounts(svc) {
			switch {
			case m.bind:
				fail("%s : montage de l'hôte %s interdit", name, m.source)
			case m.source == pv[0] && !m.readOnly:
				fail("%s : %s doit être en lecture seule", name, m.source)
			case m.source != pv[0] && m.source != pv[1]:
				fail("%s : volume %s hors de la passe", name, m.source)
			}
		}
		tmp := tmpfsPaths(svc)
		for _, k := range []string{"HOME", "CLAUDE_CONFIG_DIR"} {
			if !slices.Contains(tmp, e[k]) {
				fail("%s : %s = %q, attendu un tmpfs", name, k, e[k])
			}
		}
		for _, k := range []string{"HTTPS_PROXY", "HTTP_PROXY", "https_proxy", "http_proxy"} {
			if e[k] != "http://proxy:3128" {
				fail("%s : %s = %q, attendu http://proxy:3128", name, k, e[k])
			}
		}
		for _, k := range []string{"NO_PROXY", "no_proxy"} {
			if val, ok := e[k]; !ok || val != "" {
				fail("%s : %s doit être présente et vide", name, k)
			}
		}
		if g := e["GOPROXY"]; g == "" || strings.Contains(g, "direct") {
			fail("%s : GOPROXY = %q, attendu un proxy sans direct", name, g)
		}
		if svc["init"] != false {
			fail("%s : init: false attendu, l'agent est le PID 1", name)
		}
		for _, k := range []string{"pids_limit", "mem_limit"} {
			if _, ok := svc[k]; !ok {
				fail("%s : %s attendu", name, k)
			}
		}
	}
	return v
}

func checkTrusted(ex example) []string {
	var v []string
	fail := func(format string, a ...any) { v = append(v, fmt.Sprintf(format, a...)) }
	for _, name := range append(slices.Clone(trusted), "control", "proxy") {
		svc := ex.service(name)
		if slices.Contains(strs(svc["secrets"]), "model_token") {
			fail("%s : secret du modèle", name)
		}
		for k := range env(svc) {
			if slices.Contains(modelSecretEnv, k) {
				fail("%s : variable %s, secret du modèle", name, k)
			}
		}
	}
	if s := strs(ex.service("control")["secrets"]); !slices.Equal(s, []string{"control_token"}) {
		fail("control : secrets = %v, attendu control_token seulement", s)
	}
	for k := range env(ex.service("control")) {
		if slices.Contains(trustedSecretEnv, k) {
			fail("control : variable %s, secret de forge ou webhook", k)
		}
	}
	// Ports : seule l'administration de control, sur 127.0.0.1.
	for name, s := range ex.services() {
		ports := strs(asMap(s)["ports"])
		if name != "control" && len(ports) > 0 {
			fail("%s : ports publiés interdits", name)
		}
		for _, p := range ports {
			if !strings.HasPrefix(p, "127.0.0.1:") {
				fail("%s : port %q, attendu sur 127.0.0.1 seulement", name, p)
			}
		}
	}
	// Volumes de confiance : transcripts aux étapes publish et à control,
	// history à control seul.
	for name, s := range ex.services() {
		for _, m := range mounts(asMap(s)) {
			switch m.source {
			case "history":
				if name != "control" {
					fail("%s : history est réservé à control", name)
				}
			case "transcripts":
				if !slices.Contains([]string{"publish", "retry-publish", "control"}, name) {
					fail("%s : transcripts est réservé aux étapes de confiance et à control", name)
				}
			}
		}
	}
	return v
}

func checkNetworks(ex example) []string {
	var v []string
	fail := func(format string, a ...any) { v = append(v, fmt.Sprintf(format, a...)) }
	nets := asMap(ex.project["networks"])
	for name, n := range nets {
		if _, ok := asMap(n)["enable_ipv6"]; ok {
			fail("réseau %s : enable_ipv6 interdit", name)
		}
		if _, ok := asMap(n)["external"]; ok {
			fail("réseau %s : réseau externe interdit", name)
		}
	}
	agentNet := asMap(nets["agent-net"])
	if agentNet["internal"] != true || asMap(agentNet["driver_opts"])["com.docker.network.bridge.gateway_mode_ipv4"] != "isolated" {
		fail("agent-net : internal et gateway_mode_ipv4 isolated attendus")
	}
	if asMap(nets["control-net"])["internal"] != true {
		fail("control-net : internal attendu")
	}
	members := map[string][]string{}
	for name, s := range ex.services() {
		for _, n := range strs(asMap(s)["networks"]) {
			members[n] = append(members[n], name)
		}
	}
	for n := range members {
		slices.Sort(members[n])
	}
	if got, want := members["agent-net"], []string{"agent", "proxy", "retry-agent"}; !slices.Equal(got, want) {
		fail("agent-net = %v, attendu %v : seul le proxy relie les agents à l'extérieur", got, want)
	}
	if got, want := members["control-net"], []string{"control", "publish", "retry-publish", "select"}; !slices.Equal(got, want) {
		fail("control-net = %v, attendu %v", got, want)
	}
	if got := members["admin-net"]; !slices.Equal(got, []string{"control"}) {
		fail("admin-net = %v, attendu control seul", got)
	}
	return v
}

// squidRules sont les lignes actives de squid.conf, sans commentaire.
func squidRules(conf string) [][]string {
	var out [][]string
	for _, l := range strings.Split(conf, "\n") {
		if i := strings.IndexByte(l, '#'); i >= 0 {
			l = l[:i]
		}
		if f := strings.Fields(l); len(f) > 0 {
			out = append(out, f)
		}
	}
	return out
}

// privateRanges doivent toutes figurer dans l'ACL private_dst.
var privateRanges = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.0/8", "169.254.0.0/16", "100.64.0.0/10", "0.0.0.0/8", "::1/128", "fc00::/7", "fe80::/10"}

func checkSquid(conf string) []string {
	var v []string
	fail := func(format string, a ...any) { v = append(v, fmt.Sprintf(format, a...)) }
	acls := map[string][]string{}
	aclType := map[string]string{}
	var access [][]string
	for _, r := range squidRules(conf) {
		switch r[0] {
		case "acl":
			if len(r) < 4 {
				fail("squid.conf : acl incomplète %v", r)
				continue
			}
			acls[r[1]] = append(acls[r[1]], r[3:]...)
			aclType[r[1]] = r[2]
		case "http_access":
			access = append(access, r[1:])
		case "http_port":
			if len(r) != 2 || r[1] != "3128" {
				fail("squid.conf : http_port 3128 seul attendu")
			}
		case "cache":
			if strings.Join(r[1:], " ") != "deny all" {
				fail("squid.conf : cache deny all attendu")
			}
		case "cache_dir", "ssl_bump", "https_port", "tcp_outgoing_address", "never_direct", "cache_peer", "include":
			fail("squid.conf : directive %s interdite", r[0])
		}
	}
	if !slices.Equal(acls["SSL_ports"], []string{"443"}) || aclType["SSL_ports"] != "port" {
		fail("squid.conf : SSL_ports doit être le seul port 443")
	}
	if aclType["CONNECT"] != "method" || !slices.Equal(acls["CONNECT"], []string{"CONNECT"}) {
		fail("squid.conf : acl CONNECT method CONNECT attendue")
	}
	for _, r := range privateRanges {
		if !slices.Contains(acls["private_dst"], r) {
			fail("squid.conf : private_dst sans %s", r)
		}
	}
	if slices.Contains(acls["private_dst"], "::ffff:0:0/96") || slices.Contains(acls["private_dst"], "0.0.0.0/0") {
		fail("squid.conf : private_dst ne doit pas couvrir tout IPv4")
	}
	if aclType["private_dst"] != "dst" {
		fail("squid.conf : private_dst doit être une acl dst")
	}
	if !squidContains(conf, "cache deny all") {
		fail("squid.conf : cache deny all attendu")
	}

	// Ordre : refus de ce qui n'est pas CONNECT, des ports autres que 443,
	// au plus une exception privée, refus des plages privées, autorisations
	// des listes, refus final.
	line := func(r []string) string { return strings.Join(r, " ") }
	if len(access) < 4 || line(access[0]) != "deny !CONNECT" || line(access[1]) != "deny !SSL_ports" {
		fail("squid.conf : les deux premières règles doivent refuser ce qui n'est pas CONNECT, puis les ports autres que 443")
		return v
	}
	if line(access[len(access)-1]) != "deny all" {
		fail("squid.conf : deny all doit être la dernière règle")
	}
	i := 2
	// Une autorisation avant le refus des plages privées n'est admise que
	// comme l'exception d'une passerelle, qui nomme une adresse.
	if i < len(access) && access[i][0] == "allow" && slices.ContainsFunc(access[i][1:], func(t string) bool { return aclType[t] == "dst" }) {
		if err := privateException(access[i], acls, aclType); err != "" {
			fail("squid.conf : %s", err)
		}
		i++
	}
	if i >= len(access) || line(access[i]) != "deny private_dst" {
		fail("squid.conf : le refus des plages privées doit précéder toute autorisation des listes, après au plus une exception")
		return v
	}
	for _, r := range access[i+1 : len(access)-1] {
		if len(r) != 2 || r[0] != "allow" || aclType[r[1]] != "dstdomain" || !strings.HasPrefix(firstOf(acls[r[1]]), `"/etc/squid/allowlist/`) {
			fail("squid.conf : règle %q, attendu allow d'une liste d'allowlist/", line(r))
		}
	}
	for _, r := range access {
		if line(r) == "allow all" {
			fail("squid.conf : allow all interdit")
		}
	}
	return v
}

func firstOf(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

func squidContains(conf, rule string) bool {
	for _, r := range squidRules(conf) {
		if strings.Join(r, " ") == rule {
			return true
		}
	}
	return false
}

// privateException juge l'exception d'une passerelle LLM privée : elle
// exige CONNECT, le port 443, un domaine et une adresse exacte.
func privateException(r []string, acls map[string][]string, aclType map[string]string) string {
	terms := r[1:]
	if !slices.Contains(terms, "CONNECT") || !slices.Contains(terms, "SSL_ports") {
		return "l'exception privée doit porter CONNECT et SSL_ports"
	}
	var domain, addr bool
	for _, t := range terms {
		switch aclType[t] {
		case "dstdomain":
			domain = len(acls[t]) == 1 && !strings.HasPrefix(acls[t][0], ".") && !strings.HasPrefix(acls[t][0], `"`)
		case "dst":
			addr = len(acls[t]) == 1 && (strings.HasSuffix(acls[t][0], "/32") || strings.HasSuffix(acls[t][0], "/128"))
		}
	}
	if !domain || !addr {
		return "l'exception privée exige un domaine exact et une adresse exacte (/32 ou /128)"
	}
	return ""
}

// forgeDomains sont les domaines de forge qu'aucune liste de l'agent ne
// porte : l'agent ne parle jamais à la forge.
var forgeDomains = []string{"github.com", "githubusercontent.com", "gitlab.com", "githubassets.com"}

func checkLists(lists map[string]string) []string {
	var v []string
	fail := func(format string, a ...any) { v = append(v, fmt.Sprintf(format, a...)) }
	if lists["model.txt"] == "" || lists["toolchain.txt"] == "" {
		fail("allowlist : model.txt et toolchain.txt attendus")
	}
	for name, content := range lists {
		for _, l := range strings.Split(content, "\n") {
			d := strings.TrimSpace(l)
			if d == "" || strings.HasPrefix(d, "#") {
				continue
			}
			bare := strings.TrimPrefix(d, ".")
			switch {
			case strings.ContainsAny(d, "*/ :"):
				fail("%s : %q, attendu un nom de domaine", name, d)
			case !strings.Contains(bare, "."):
				fail("%s : %q est un domaine de premier niveau entier", name, d)
			}
			for _, f := range forgeDomains {
				if bare == f || strings.HasSuffix(bare, "."+f) {
					fail("%s : %q est un domaine de forge", name, d)
				}
			}
		}
	}
	return v
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
