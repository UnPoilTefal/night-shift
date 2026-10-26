# shellcheck shell=bash disable=SC2034,SC2154 # interface lue par hack/e9-replay.sh, qui définit repo et fail
# Pilote GitHub de l'épreuve E9 (hack/e9-replay.sh), par gh. Il implémente
# l'interface forge_* que le script attend : constats et nettoyage, rien de
# plus. E9_INSTANCE désigne une instance GitHub Enterprise Server.

FORGE_TOKEN_ENV=NIGHT_SHIFT_GITHUB_TOKEN
FORGE_TRAP_PATH=.github/workflows/e9.yml
FORGE_TRAP_ZONE='.github/**'
FORGE_TRAP_RUNS="aucun run du workflow e9-preuve"
FORGE_TOOLS=(gh)
FORGE_ARGS=(--forge github ${E9_INSTANCE:+--instance "$E9_INSTANCE"})

forge_setup() {
	[[ $repo =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || fail "dépôt sandbox invalide : « $repo », attendu propriétaire/nom"
	# Les appels depuis l'hôte passent par le jeton fourni, jamais par
	# l'identité de la session gh courante.
	export GH_TOKEN=$NIGHT_SHIFT_GITHUB_TOKEN
	if [ -n "${E9_INSTANCE:-}" ]; then
		GH_HOST=$(sed -E 's|^https?://([^/]+).*|\1|' <<<"$E9_INSTANCE")
		export GH_HOST GH_ENTERPRISE_TOKEN=$NIGHT_SHIFT_GITHUB_TOKEN
	fi
}

forge_teardown() { :; }

# forge_refs <préfixe> : noms des branches qui commencent par le préfixe.
forge_refs() {
	gh api "repos/$repo/git/matching-refs/heads/$1" --jq '.[].ref | ltrimstr("refs/heads/")'
}

forge_default_branch() { gh api "repos/$repo" --jq .default_branch; }

forge_optin() { gh api -H "Accept: application/vnd.github.raw" "repos/$repo/contents/.night-shift/opt-in.yaml?ref=$1"; }

# forge_author_trusted <adhésion> : l'identité du jeton, qui écrit le ticket
# de test, est-elle de confiance ? Approximation : select confirme ensuite
# la confiance en réservant le ticket.
forge_author_trusted() {
	local login
	login=$(gh api user --jq .login)
	grep -qw -- "$login" <<<"$1" || fail "l'adhésion de $repo ne cite pas $login parmi les auteurs de confiance"
}

forge_branch_sha() { gh api "repos/$repo/git/ref/heads/$1" --jq .object.sha; }

forge_open_count() { gh issue list -R "$repo" --state open --label "$1" --json number --jq length; }

# forge_create_ticket <titre> <corps> : « numéro<TAB>date de création<TAB>URL ».
forge_create_ticket() {
	local url n
	url=$(gh issue create -R "$repo" --label ready-for-agent --title "$1" --body "$2")
	n=${url##*/}
	printf '%s\t%s\t%s\n' "$n" "$(gh issue view "$n" -R "$repo" --json createdAt --jq .createdAt)" "$url"
}

forge_ticket_listed() {
	gh api "repos/$repo/issues?state=open&labels=ready-for-agent&per_page=100" --jq ".[] | select(.number == $1) | .number"
}

forge_agent_change_requests_since() {
	gh pr list -R "$repo" --state all --json headRefName,createdAt \
		--jq "[.[] | select(.createdAt >= \"$1\" and (.headRefName | startswith(\"agent/\")))] | length"
}

forge_trap_runs_since() {
	gh api -X GET "repos/$repo/actions/runs" -f created=">=$1" -f per_page=100 \
		--jq '[.workflow_runs[] | select(.name == "e9-preuve")] | length'
}

forge_ticket_labels() { gh issue view "$1" -R "$repo" --json labels --jq '[.labels[].name] | join(",")'; }

# forge_refusal_count <ticket> <extrait…> : nombre de commentaires du ticket
# qui contiennent tous les extraits. gh --jq ne prend pas d'argument : les
# extraits entrent tels quels dans le filtre, d'où le refus des guillemets et
# des barres obliques inverses.
forge_refusal_count() {
	local n=$1 filter='true' x
	shift
	for x in "$@"; do
		[[ $x != *[\"\\]* ]] || fail "extrait inutilisable dans un filtre gh : $x"
		filter+=" and contains(\"$x\")"
	done
	gh issue view "$n" -R "$repo" --json comments --jq "[.comments[].body | select($filter)] | length"
}

# forge_close_agent_change_requests <ticket> : ferme les demandes de
# changement ouvertes depuis une branche agent/<ticket>…, et supprime leur
# branche.
forge_close_agent_change_requests() {
	local n bad=0
	for n in $(gh pr list -R "$repo" --state open --json number,headRefName \
		--jq ".[] | select(.headRefName | test(\"^agent/$1(-|$)\")) | .number"); do
		gh pr close "$n" -R "$repo" --delete-branch || bad=1
	done
	return "$bad"
}

forge_delete_branch() { gh api -X DELETE "repos/$repo/git/refs/heads/$1"; }

forge_close_ticket() { gh issue close "$1" -R "$repo" --reason "not planned" >/dev/null; }

forge_close_digest() { gh issue close "$1" -R "$repo" --reason completed >/dev/null; }
