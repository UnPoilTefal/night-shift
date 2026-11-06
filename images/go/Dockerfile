# syntax=docker/dockerfile:1
#
# Image d'outils Go : l'image de base de night-shift, plus la chaîne Go. Le
# contexte nommé « base » désigne l'image de base : docker-bake.hcl le relie
# à la cible base ; en dehors de bake, le fournir avec
# --build-context base=docker-image://ghcr.io/unpoiltefal/night-shift:<version>.

FROM golang:1.27.3-trixie@sha256:af1bfc2614ebb1dbd31af514b78740fd678850e76498762b425ecee4c5c6ea80 AS go

FROM base
COPY --from=go /usr/local/go /usr/local/go
# La sortie réseau de l'agent ne permet pas de télécharger une autre chaîne :
# celle de l'image fait foi.
ENV PATH=/usr/local/go/bin:$PATH \
	GOTOOLCHAIN=local
