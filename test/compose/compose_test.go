//go:build compose

// Package compose est le test de bout en bout de l'exemple Docker Compose
// (examples/compose, ADR 0013). Il fait tourner la stack réelle, avec les
// images construites depuis l'arbre, contre une fausse forge GitLab lancée
// sur l'hôte, et constate chaque garantie en neuf scénarios. Il exige Docker
// Engine 28 ou plus, et échoue au lieu de s'ignorer : make test-compose.
package compose

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	example "github.com/UnPoilTefal/night-shift/examples/compose"
	"github.com/UnPoilTefal/night-shift/internal/control"
	"github.com/UnPoilTefal/night-shift/internal/forge"
	"github.com/UnPoilTefal/night-shift/internal/forge/gitlab"
	"github.com/UnPoilTefal/night-shift/internal/forge/gitlab/gitlabtest"
	"github.com/UnPoilTefal/night-shift/internal/pass"
	"github.com/UnPoilTefal/night-shift/internal/relay"
	"github.com/UnPoilTefal/night-shift/internal/testgit"
)

const (
	// project est le nom de la stack de test : jamais celui d'une stack
	// réelle.
	project = "night-shift-e2e"
	// repo est le projet de la fausse forge.
	repo         = "night/target"
	forgeToken   = "e2e-forge-token"
	controlToken = "e2e-control-token"
	modelToken   = "e2e-model-token"
	// adminURL est l'API d'administration de control, publiée par l'exemple.
	adminURL = "http://127.0.0.1:8081"
	// curlImage sonde le réseau des agents : curlimages/curl:8.11.1,
	// épinglée par le digest de sa liste de manifestes.
	curlImage = "curlimages/curl:8.11.1@sha256:c1fe1679c34d9784c1b0d1e5f62ac0a79fca01fb6377cdd33e90473c6f9f9a69"
	// e2eTag est l'étiquette des images de la stack de test : celle de
	// NIGHT_SHIFT_E2E_VERSION, plus fake-claude dans l'image des agents.
	e2eTag   = "e2e"
	registry = "ghcr.io/unpoiltefal"
	// passTimeout borne une passe : deux tours d'agent et deux attentes de
	// CI simulée, à la seconde.
	passTimeout = 3 * time.Minute
)

// optIn interdit, comme l'épreuve E9, la définition de CI de chaque forge.
const optIn = "version: 1\ntrustLevel: {ticketsPerPass: 1, pullRequests: draft}\nforbiddenZones: [\".github/**\", \".gitlab-ci.yml\"]\n"

// stack est la stack de test et sa fausse forge.
type stack struct {
	t          *testing.T
	env        []string
	forge      *gitlabtest.Server
	remote     *testgit.Remote
	port       string
	secretsDir string
	// rounds sont les tours d'agent dont un transcript doit être archivé :
	// passe, ticket, nombre de tours.
	rounds []archived
}

type archived struct {
	pass           string
	ticket, rounds int
}

func TestComposeStack(t *testing.T) {
	s := up(t)
	// Les scénarios partagent la stack, l'un après l'autre : chacun lui
	// prête son propre *testing.T.
	scenario := func(name string, f func(t *testing.T)) {
		t.Run(name, func(t *testing.T) {
			parent := s.t
			s.t = t
			defer func() { s.t = parent }()
			f(t)
		})
	}
	scenario("1 passe complète", s.completePass)
	scenario("2 second tour", s.secondRound)
	scenario("3 refus de zone", s.zoneRefusal)
	scenario("4 sonde réseau", s.networkProbe)
	scenario("5 secrets", s.secrets)
	scenario("6 persistance", s.persistence)
	scenario("7 marqueur forgé", s.forgedDone)
	scenario("8 regroupement", s.coalescing)
	scenario("9 transcripts", s.transcripts)
}

// up vérifie Docker, construit les images de test, lance la fausse forge,
// juge la configuration fusionnée, puis démarre la stack.
func up(t *testing.T) *stack {
	t.Helper()
	requireEngine(t)
	base := cmpOr(os.Getenv("NIGHT_SHIFT_E2E_VERSION"), "dev")
	buildImages(t, base)

	s := &stack{t: t}
	s.startForge()
	s.writeSecrets()
	s.env = append(os.Environ(),
		"NIGHT_SHIFT_REPO="+repo,
		"NIGHT_SHIFT_FORGE=gitlab",
		"NIGHT_SHIFT_INSTANCE=http://forge.test:"+s.port,
		"NIGHT_SHIFT_VERSION="+e2eTag,
		"NIGHT_SHIFT_SECRETS_DIR="+s.secretsDir,
		// Aucune échéance pendant le test : seules ses demandes déclenchent.
		"NIGHT_SHIFT_SCHEDULE=0 0 1 1 *",
	)

	merged := s.compose("config")
	violations, err := example.Check(exampleDir(t), []byte(merged))
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) > 0 {
		t.Fatalf("la configuration fusionnée enfreint les règles de l'exemple :\n%s", strings.Join(violations, "\n"))
	}

	t.Cleanup(func() {
		if t.Failed() {
			if out, err := s.composeErr("logs", "--no-color", "--tail", "300"); err == nil {
				t.Logf("journaux de la stack :\n%s", out)
			}
		}
		_, _ = s.composeErr("down", "--volumes", "--remove-orphans", "--timeout", "5")
	})
	_, _ = s.composeErr("down", "--volumes", "--remove-orphans", "--timeout", "5")
	s.compose("up", "--detach")
	s.waitFor("l'API d'administration de control", time.Minute, func() bool {
		_, err := s.admin("/status")
		return err == nil
	})
	return s
}

// requireEngine exige Docker Engine 28 ou plus : le mode isolated du réseau
// des agents n'existe pas avant.
func requireEngine(t *testing.T) {
	t.Helper()
	out, err := run("", nil, "docker", "version", "--format", "{{.Server.Version}}")
	if err != nil {
		t.Fatalf("Docker est injoignable : %v\n%s", err, out)
	}
	major, err := strconv.Atoi(strings.SplitN(strings.TrimSpace(out), ".", 2)[0])
	if err != nil || major < 28 {
		t.Fatalf("Docker Engine %s : le test exige la version 28 ou plus (gateway_mode_ipv4 isolated)", strings.TrimSpace(out))
	}
}

// buildImages étiquette e2e les images night-shift de la version base
// (make images), et ajoute fake-claude à celle des agents.
func buildImages(t *testing.T, base string) {
	t.Helper()
	for _, img := range []string{"night-shift", "night-shift-go"} {
		if out, err := run("", nil, "docker", "image", "inspect", registry+"/"+img+":"+base); err != nil {
			t.Fatalf("image %s/%s:%s absente : make images IMAGE_VERSION=%s\n%s", registry, img, base, base, out)
		}
	}
	mustRun(t, "", nil, "docker", "tag", registry+"/night-shift:"+base, registry+"/night-shift:"+e2eTag)
	dockerfile := "ARG BASE\nFROM ${BASE}\nCOPY --chmod=0755 fake-claude /usr/local/bin/fake-claude\n"
	ctx, err := filepath.Abs("../../internal/harness/claude/testdata")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("docker", "build", "--quiet", "--build-arg", "BASE="+registry+"/night-shift-go:"+base,
		"--tag", registry+"/night-shift-go:"+e2eTag, "--file", "-", ctx)
	cmd.Stdin = strings.NewReader(dockerfile)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("construction de l'image des agents : %v\n%s", err, out)
	}
}

func exampleDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs("../../examples/compose")
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// startForge lance la fausse forge sur toutes les adresses de l'hôte, pour
// que host-gateway y mène, avec un projet adhérent et la CI simulée : rouge
// tant que la série porte ci-red, verte sinon.
func (s *stack) startForge() {
	t := s.t
	src := testgit.New(t)
	src.Write("README.md", "bonjour\n")
	src.Write(".night-shift/opt-in.yaml", optIn)
	src.Commit("init")
	s.remote = testgit.NewRemote(t, repo, src)
	s.forge = gitlabtest.NewOn(t, forgeToken, "0.0.0.0:0")
	s.forge.ServeGit(s.remote.Base)
	u, err := url.Parse(s.forge.Base)
	if err != nil {
		t.Fatal(err)
	}
	s.port = u.Port()
	s.forge.SetFile(repo, ".night-shift/opt-in.yaml", optIn)
	s.forge.AddMember(repo, "alice", gitlab.Developer, false)
	s.forge.DefineLabel(repo, forge.LabelReady)
	// Un commit pas encore poussé n'a qu'une pipeline en cours.
	s.forge.SetPipeline("", "", "running")
	bare := s.remote.Dir()
	s.forge.SetPipelineRule(func(_, sha string) (string, []gitlabtest.Job, bool) {
		if exec.Command("git", "-C", bare, "cat-file", "-e", sha+"^{commit}").Run() != nil {
			return "", nil, false
		}
		if exec.Command("git", "-C", bare, "cat-file", "-e", sha+":ci-red").Run() == nil {
			return "failed", []gitlabtest.Job{{Name: "test", Status: "failed", WebURL: "https://gitlab.example/ci/red",
				Trace: "FAIL : ci-red est présent"}}, true
		}
		return "success", []gitlabtest.Job{{Name: "test", Status: "success", WebURL: "https://gitlab.example/ci/green"}}, true
	})
}

// writeSecrets écrit les quatre secrets, puis les rend lisibles par la seule
// UID 65532, comme le demande docs/compose.md : par un conteneur root, sans
// sudo.
func (s *stack) writeSecrets() {
	t := s.t
	s.secretsDir = t.TempDir()
	for name, v := range map[string]string{"forge_token": forgeToken, "discord_webhook": "", "model_token": modelToken, "control_token": controlToken} {
		if err := os.WriteFile(filepath.Join(s.secretsDir, name), []byte(v+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mustRun(t, "", nil, "docker", "run", "--rm", "--user", "0", "--network", "none", "-v", s.secretsDir+":/s",
		registry+"/night-shift:"+e2eTag, "sh", "-c", "chown 65532:65532 /s/* && chmod 0400 /s/*")
}

// compose lance docker compose sur l'exemple et sa surcharge de test.
func (s *stack) compose(args ...string) string {
	s.t.Helper()
	out, err := s.composeErr(args...)
	if err != nil {
		s.t.Fatalf("docker compose %s : %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func (s *stack) composeErr(args ...string) (string, error) {
	full := append([]string{"compose", "--project-name", project,
		"--file", filepath.Join(exampleDir(s.t), "compose.yaml"), "--file", "compose.test.yaml"}, args...)
	return run("", s.env, "docker", full...)
}

// exec lance une commande dans un service, sous son utilisateur.
func (s *stack) exec(service string, args ...string) (string, error) {
	return s.composeErr(append([]string{"exec", "-T", service}, args...)...)
}

// run rend la sortie standard de la commande ; son erreur porte la sortie
// d'erreur.
func run(dir string, env []string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir, cmd.Env = dir, env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("%w : %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// git lit le dépôt nu de la fausse forge.
func (s *stack) git(args ...string) string {
	s.t.Helper()
	return strings.TrimSpace(mustRun(s.t, "", nil, "git", append([]string{"-C", s.remote.Dir()}, args...)...))
}

func mustRun(t *testing.T, dir string, env []string, name string, args ...string) string {
	t.Helper()
	out, err := run(dir, env, name, args...)
	if err != nil {
		t.Fatalf("%s %s : %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return out
}

func cmpOr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// admin lit une route de l'API d'administration.
func (s *stack) admin(route string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, adminURL+control.AdminPrefix+route, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+controlToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	var b bytes.Buffer
	if _, err := b.ReadFrom(resp.Body); err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s : %s", route, resp.Status)
	}
	return b.Bytes(), nil
}

func (s *stack) adminJSON(route string, v any) {
	s.t.Helper()
	b, err := s.admin(route)
	if err != nil {
		s.t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		s.t.Fatalf("%s : %v", route, err)
	}
}

func (s *stack) passes() []control.Summary {
	s.t.Helper()
	var out []control.Summary
	s.adminJSON("/passes", &out)
	return out
}

func (s *stack) status() control.Status {
	s.t.Helper()
	var st control.Status
	s.adminJSON("/status", &st)
	return st
}

func (s *stack) record(passID string) control.Record {
	s.t.Helper()
	var r control.Record
	s.adminJSON("/passes/"+url.PathEscape(passID), &r)
	return r
}

func (s *stack) waitFor(what string, timeout time.Duration, ok func() bool) {
	s.t.Helper()
	deadline := time.Now().Add(timeout)
	for !ok() {
		if time.Now().After(deadline) {
			s.t.Fatalf("%s : toujours pas après %s", what, timeout)
		}
		time.Sleep(time.Second)
	}
}

// idle dit si control n'a ni passe active ni passe en attente.
func (s *stack) idle() bool {
	st := s.status()
	return st.Active == nil && st.Queued == nil
}

func (s *stack) trigger() {
	s.t.Helper()
	if out, err := s.exec("control", "night-shift", "control", "trigger"); err != nil {
		s.t.Fatalf("control trigger : %v\n%s", err, out)
	}
}

// addTicket ajoute un ticket prêt, écrit par une Developer, dont le brief
// choisit le comportement de fake-claude.
func (s *stack) addTicket(title, mode string) int {
	s.t.Helper()
	n := 1
	for _, i := range s.forge.Issues(repo) {
		n = max(n, i.IID+1)
	}
	s.forge.AddIssue(repo, gitlabtest.Issue{IID: n, Title: title, Description: "Le brief.\n\nfake-claude: " + mode,
		Author: "alice", Labels: []string{forge.LabelReady}, CreatedAt: time.Now().UTC()})
	return n
}

// runPass ajoute un ticket, demande une passe et attend qu'elle soit close.
func (s *stack) runPass(title, mode string) (int, control.Summary) {
	s.t.Helper()
	s.waitFor("control au repos avant la passe", passTimeout, s.idle)
	before := len(s.passes())
	n := s.addTicket(title, mode)
	s.trigger()
	s.waitFor("la passe du ticket #"+strconv.Itoa(n), passTimeout, func() bool { return len(s.passes()) > before && s.idle() })
	last := s.passes()[0]
	if last.Status != "finished" {
		s.t.Fatalf("passe %s : statut %q, attendu finished (%+v)", last.Pass, last.Status, last)
	}
	return n, last
}

// mergeRequest rend l'unique merge request du ticket n.
func (s *stack) mergeRequest(n int) gitlabtest.MergeRequest {
	s.t.Helper()
	var found []gitlabtest.MergeRequest
	for _, m := range s.forge.MergeRequests(repo) {
		if strings.HasPrefix(m.SourceBranch, fmt.Sprintf("agent/%d-", n)) {
			found = append(found, m)
		}
	}
	if len(found) != 1 {
		s.t.Fatalf("merge requests du ticket #%d = %+v, attendu une seule", n, found)
	}
	return found[0]
}

// files rend les fichiers de la branche, sur la forge.
func (s *stack) files(branch string) []string {
	s.t.Helper()
	return strings.Fields(s.git("ls-tree", "-r", "--name-only", branch))
}

// lifecycle rend les labels de cycle de vie du ticket n.
func (s *stack) lifecycle(n int) []string {
	var out []string
	for _, l := range s.forge.Issue(repo, n).Labels {
		if slices.Contains([]string{forge.LabelReady, forge.LabelInProgress, forge.LabelHuman}, l) {
			out = append(out, l)
		}
	}
	return out
}

func (s *stack) notes(n int) []string {
	var out []string
	for _, note := range s.forge.Issue(repo, n).Notes {
		out = append(out, note.Body)
	}
	return out
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !strings.Contains(s, p) {
			return false
		}
	}
	return true
}

// completePass : réservation, commits, MR en brouillon, CI verte, ticket
// rendu, digest.
func (s *stack) completePass(t *testing.T) {
	issues := len(s.forge.Issues(repo))
	n, sum := s.runPass("Ajouter f", "commit")
	mr := s.mergeRequest(n)
	if !mr.Draft() || mr.TargetBranch != "main" || !strings.HasPrefix(mr.Description, fmt.Sprintf("Closes #%d", n)) {
		t.Fatalf("merge request = %+v, attendu un brouillon vers main qui ferme #%d", mr, n)
	}
	log := s.git("log", "--format=%s", "main.."+mr.SourceBranch)
	if !containsAll(log, "feat: x", "test: y") {
		t.Fatalf("commits publiés :\n%s", log)
	}
	notes := strings.Join(s.notes(n), "\n---\n")
	if !strings.Contains(notes, pass.ReservationNote+sum.Pass) {
		t.Fatalf("aucune note de réservation par %s :\n%s", sum.Pass, notes)
	}
	mrURL := fmt.Sprintf("https://gitlab.example/%s/-/merge_requests/%d", repo, mr.IID)
	if !strings.Contains(notes, mrURL) {
		t.Fatalf("le ticket ne renvoie pas vers %s :\n%s", mrURL, notes)
	}
	if got := s.lifecycle(n); len(got) != 0 {
		t.Fatalf("labels de cycle de vie restants : %v", got)
	}
	all := s.forge.Issues(repo)
	if len(all) != issues+2 || !strings.Contains(all[len(all)-1].Description, mrURL) {
		t.Fatalf("issues = %+v, attendu un digest qui renvoie vers la merge request", all[issues:])
	}
	if sum.Tickets != 1 {
		t.Fatalf("passe = %+v, attendu un ticket", sum)
	}
	s.rounds = append(s.rounds, archived{sum.Pass, n, 1})
}

// secondRound : CI rouge, relance de l'agent, puis passe close par
// retry-publish.
func (s *stack) secondRound(t *testing.T) {
	n, sum := s.runPass("Ajouter f malgré la CI", "red")
	mr := s.mergeRequest(n)
	files := s.files(mr.SourceBranch)
	if slices.Contains(files, "ci-red") || !slices.Contains(files, "h.txt") {
		t.Fatalf("fichiers de la branche = %v, attendu la correction de la relance", files)
	}
	var stages []string
	for _, e := range s.record(sum.Pass).Events {
		stages = append(stages, e.Stage+":"+string(e.Type))
	}
	if !slices.Contains(stages, "retry-publish:"+string(relay.PassFinished)) || slices.Contains(stages, "publish:"+string(relay.PassFinished)) {
		t.Fatalf("événements = %v, attendu la passe close par retry-publish", stages)
	}
	if got := s.lifecycle(n); len(got) != 0 {
		t.Fatalf("labels de cycle de vie restants : %v", got)
	}
	s.rounds = append(s.rounds, archived{sum.Pass, n, 2})
}

// zoneRefusal : une série qui touche une zone interdite n'est jamais
// poussée, et le ticket revient à un humain.
func (s *stack) zoneRefusal(t *testing.T) {
	n, _ := s.runPass("Épreuve E9", "e9")
	if refs := s.git("for-each-ref", "--format=%(refname)", fmt.Sprintf("refs/heads/agent/%d-*", n)); refs != "" {
		t.Fatalf("branches poussées : %s", refs)
	}
	for _, m := range s.forge.MergeRequests(repo) {
		if strings.HasPrefix(m.SourceBranch, fmt.Sprintf("agent/%d-", n)) {
			t.Fatalf("merge request ouverte : %+v", m)
		}
	}
	if got := s.lifecycle(n); !slices.Equal(got, []string{forge.LabelHuman}) {
		t.Fatalf("labels = %v, attendu %s", got, forge.LabelHuman)
	}
	notes := s.notes(n)
	if !containsAll(notes[len(notes)-1], "refus de zone", ".github/workflows/e9.yml", ".github/**") {
		t.Fatalf("dernière note = %q, attendu le refus qui cite le chemin et la zone", notes[len(notes)-1])
	}
}

// probe lance curl sur le réseau des agents, avec forge.test vers l'hôte
// comme l'ont les étapes de confiance ; il rend la sortie et le code.
func (s *stack) probe(args ...string) (string, int) {
	s.t.Helper()
	full := append([]string{"run", "--rm", "--network", project + "_agent-net", "--add-host", "forge.test:host-gateway",
		curlImage, "--silent", "--show-error", "--max-time", "15"}, args...)
	out, err := run("", nil, "docker", full...)
	if err == nil {
		return out, 0
	}
	if ee := (*exec.ExitError)(nil); errors.As(err, &ee) {
		return out + err.Error(), ee.ExitCode()
	}
	s.t.Fatalf("docker run %s : %v\n%s", curlImage, err, out)
	return "", 0
}

// networkProbe : seule sortie de l'agent, le proxy, et seulement vers la
// liste d'autorisation.
func (s *stack) networkProbe(t *testing.T) {
	gateway := strings.TrimSpace(mustRun(t, "", nil, "docker", "network", "inspect", "--format",
		"{{range .IPAM.Config}}{{.Gateway}}{{end}}", project+"_agent-net"))
	forgeURL := "http://forge.test:" + s.port + "/api/v4/version"
	for _, c := range []struct {
		what string
		args []string
		// code, s'il n'est pas nul, est le code de curl attendu.
		code int
	}{
		{"sortie directe vers une adresse publique", []string{"--noproxy", "*", "https://1.1.1.1"}, 0},
		{"nom public non résolu", []string{"--noproxy", "*", "https://api.anthropic.com"}, 6},
		{"forge sur l'hôte, sans proxy", []string{"--noproxy", "*", forgeURL}, 0},
		{"service de l'hôte par la passerelle d'agent-net", []string{"--noproxy", "*", "http://" + gateway + ":" + s.port + "/api/v4/version"}, 0},
		{"control injoignable", []string{"--noproxy", "*", "http://control:8080/v1/passes/claim"}, 0},
		{"CONNECT hors liste refusé", []string{"--proxy", "http://proxy:3128", "https://example.com"}, 56},
		{"forge refusée par le proxy", []string{"--proxy", "http://proxy:3128", "--proxytunnel", forgeURL}, 56},
	} {
		out, code := s.probe(c.args...)
		if code == 0 || (c.code != 0 && code != c.code) {
			t.Errorf("%s : curl %v, code %d, attendu un échec %d\n%s", c.what, c.args, code, c.code, out)
		}
	}
	out, code := s.probe("--proxy", "http://proxy:3128", "--output", "/dev/null", "--write-out", "%{http_code}", "https://api.anthropic.com/")
	if code != 0 || strings.TrimSpace(out) == "000" {
		t.Errorf("tunnel vers api.anthropic.com:443 : code %d, sortie %q, attendu une réponse HTTP", code, out)
	}
	s.waitFor("le refus de example.com dans le journal du proxy", 30*time.Second, func() bool {
		logs, _ := s.composeErr("logs", "--no-color", "proxy")
		return strings.Contains(logs, "TCP_DENIED") && strings.Contains(logs, "example.com:443")
	})
	// Squid tourne avec SETUID et SETGID seulement : ses processus ont
	// quitté root pour l'utilisateur proxy (13).
	id := strings.TrimSpace(s.compose("ps", "--quiet", "proxy"))
	top := mustRun(t, "", nil, "docker", "top", id, "-o", "pid,uid,comm")
	if !slices.ContainsFunc(strings.Split(top, "\n"), func(l string) bool {
		f := strings.Fields(l)
		return len(f) == 3 && f[1] == "13" && strings.HasPrefix(f[2], "squid")
	}) {
		t.Errorf("processus du proxy :\n%s\nattendu Squid sous l'utilisateur proxy (13)", top)
	}
}

// probePass fait tourner fake-claude en mode probe et rend ce qu'il a vu au
// début de son tour.
func (s *stack) probePass(title string) map[string]string {
	s.t.Helper()
	n, _ := s.runPass(title, "probe")
	mr := s.mergeRequest(n)
	out := map[string]string{}
	for _, l := range strings.Split(s.git("show", mr.SourceBranch+":probe.txt"), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			out[k] = v
		}
	}
	return out
}

// secrets : l'agent ne voit aucun secret de la forge, et chaque service lit
// ses secrets sous l'UID 65532.
func (s *stack) secrets(t *testing.T) {
	seen := s.probePass("Sonder les secrets")
	if seen["forge_token"] != "absent" || seen["env"] != "" {
		t.Errorf("l'agent voit : forge_token=%s, variables %q ; attendu ni fichier ni variable de forge", seen["forge_token"], seen["env"])
	}
	for _, svc := range []string{"control", "select", "agent", "publish", "retry-agent", "retry-publish"} {
		out, err := s.exec(svc, "sh", "-c", `id -u; for f in /run/secrets/*; do cat "$f" > /dev/null || exit 1; done`)
		if err != nil || strings.TrimSpace(strings.SplitN(out, "\n", 2)[0]) != "65532" {
			t.Errorf("%s : secrets illisibles par l'UID 65532 : %v\n%s", svc, err, out)
		}
	}
	for _, svc := range []string{"agent", "retry-agent"} {
		if out, err := s.exec(svc, "test", "-e", "/run/secrets/forge_token"); err == nil {
			t.Errorf("%s : /run/secrets/forge_token existe\n%s", svc, out)
		}
	}
}

// persistence : un tour hostile ne laisse ni fichier ni processus au tour
// suivant du même conteneur.
func (s *stack) persistence(t *testing.T) {
	agent := strings.TrimSpace(s.compose("ps", "--quiet", "agent"))
	s.runPass("Agent hostile", "hostile")
	seen := s.probePass("Sonder les restes")
	if again := strings.TrimSpace(s.compose("ps", "--quiet", "agent")); again != agent {
		t.Fatalf("le conteneur agent a changé (%s → %s) : le constat ne vaut rien", agent, again)
	}
	if seen["home"] != "clean" || seen["config"] != "clean" || seen["survivors"] != "0" {
		t.Errorf("au tour suivant, l'agent trouve %v ; attendu ni fichier ni processus", seen)
	}
}

// forgedDone : après une fin annoncée trop tôt, la MR ne contient que la
// série que publish a copiée à l'annonce.
func (s *stack) forgedDone(t *testing.T) {
	n, _ := s.runPass("Fin annoncée trop tôt", "early-done")
	files := s.files(s.mergeRequest(n).SourceBranch)
	if !slices.Contains(files, "f.txt") || slices.Contains(files, "forged.txt") {
		t.Fatalf("fichiers de la branche = %v, attendu f.txt sans forged.txt", files)
	}
}

// coalescing : trois demandes pendant une passe donnent une seule passe à
// suivre.
func (s *stack) coalescing(t *testing.T) {
	s.waitFor("control au repos", passTimeout, s.idle)
	before := len(s.passes())
	s.addTicket("Passe lente", "sleep")
	s.trigger()
	s.waitFor("la passe active", time.Minute, func() bool { return s.status().Active != nil })
	for range 3 {
		s.trigger()
	}
	s.waitFor("la passe en attente", 10*time.Second, func() bool { return s.status().Queued != nil })
	s.waitFor("la fin des deux passes", 2*passTimeout, s.idle)
	time.Sleep(3 * time.Second)
	if got := len(s.passes()) - before; got != 2 || !s.idle() {
		t.Fatalf("%d passe(s) après trois demandes pendant une passe, attendu 2 en tout", got)
	}
}

// transcripts : publish archive le transcript de chaque tour, et aucun
// service agent ne monte l'archive.
func (s *stack) transcripts(t *testing.T) {
	if len(s.rounds) == 0 {
		t.Fatal("aucun tour à constater : les scénarios 1 et 2 n'ont pas abouti")
	}
	out, err := s.exec("control", "find", "/transcripts", "-type", "f", "-name", "*.jsonl")
	if err != nil {
		t.Fatalf("lecture de l'archive : %v\n%s", err, out)
	}
	files := strings.Fields(out)
	for _, r := range s.rounds {
		for round := 1; round <= r.rounds; round++ {
			suffix := fmt.Sprintf("-%d-round%d.jsonl", r.ticket, round)
			if !slices.ContainsFunc(files, func(f string) bool {
				return strings.HasPrefix(f, "/transcripts/"+r.pass+"/") && strings.HasSuffix(f, suffix)
			}) {
				t.Errorf("aucun transcript du tour %d du ticket #%d dans la passe %s :\n%s", round, r.ticket, r.pass, out)
			}
		}
	}
	for _, svc := range []string{"agent", "retry-agent"} {
		id := strings.TrimSpace(s.compose("ps", "--quiet", svc))
		mounts := mustRun(t, "", nil, "docker", "inspect", "--format", "{{range .Mounts}}{{.Name}}:{{.Destination}} {{end}}", id)
		if strings.Contains(mounts, "transcripts") {
			t.Errorf("%s monte l'archive des transcripts : %s", svc, mounts)
		}
	}
}
