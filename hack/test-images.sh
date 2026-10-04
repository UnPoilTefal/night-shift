#!/usr/bin/env bash
# Test de fumée des images conteneur : outils présents, version injectée,
# utilisateur non root, skills livrées, et fonctionnement sur un système de
# fichiers racine en lecture seule comme dans le CronJob d'exemple.
#
# usage : hack/test-images.sh <image-de-base> <image-go> <version-attendue>
# PLATFORM (ex. linux/arm64) choisit la plateforme d'une image multi-arch.
set -euo pipefail

base=$1 go=$2 version=$3
tool=${CONTAINER_TOOL:-docker}

fail() {
	echo "test-images : $*" >&2
	exit 1
}

run() {
	"$tool" run --rm ${PLATFORM:+--platform "$PLATFORM"} --read-only --tmpfs /tmp -e HOME=/tmp "$@"
}

for img in "$base" "$go"; do
	echo "== $img"
	got=$(run "$img" night-shift version) || fail "$img : night-shift version a échoué"
	[ "$got" = "$version" ] || fail "$img : night-shift version = $got, attendu $version"
	uid=$(run "$img" id -u)
	[ "$uid" != 0 ] || fail "$img : l'image tourne en root"
	for cmd in "claude --version" "gh --version" "git --version" "make --version"; do
		# shellcheck disable=SC2086 # découpage voulu de la commande
		run "$img" $cmd >/dev/null || fail "$img : « $cmd » a échoué"
	done
	for skill in implement tdd code-review; do
		run "$img" test -f "/etc/claude-code/.claude/skills/$skill/SKILL.md" ||
			fail "$img : skill $skill absente du répertoire géré de Claude Code"
	done
done

run "$go" go version >/dev/null || fail "$go : go absent"
echo "images conformes"
