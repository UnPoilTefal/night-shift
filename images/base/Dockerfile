# syntax=docker/dockerfile:1
#
# Image de base générique de night-shift : le binaire night-shift, Claude
# Code, gh, git et make, plus les skills que l'agent invoque. Les images
# d'outils des dépôts cibles en héritent. Aucun secret : jetons et webhook
# sont fournis à l'exécution.

FROM --platform=$BUILDPLATFORM golang:1.27.1-trixie@sha256:3b77fc618ec235a1ab412de7737f120dd507c57e8d87de4cbb7994fb94275ed5 AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
	-ldflags "-s -w -X github.com/UnPoilTefal/night-shift/internal/version.Version=${VERSION}" \
	-o /out/night-shift ./cmd/night-shift

# Skills mattpocock, installées comme skills gérées : HOME et
# CLAUDE_CONFIG_DIR, fixés par le déploiement, ne les masquent pas, et
# l'agent les invoque sans espace de noms (/implement).
FROM --platform=$BUILDPLATFORM node:26.10.0-trixie-slim@sha256:ec7758ee051e457b468b32bde57b0879010b325bb9862718e9615225ce4aaae1 AS skills
# renovate: datasource=github-tags depName=mattpocock/skills
ARG MATTPOCOCK_SKILLS_VERSION=v1.3.1
ADD https://github.com/mattpocock/skills.git#${MATTPOCOCK_SKILLS_VERSION} /src
RUN node -e ' \
	const fs = require("fs"), path = require("path"); \
	const { skills } = JSON.parse(fs.readFileSync("/src/.claude-plugin/plugin.json")); \
	for (const s of skills) fs.cpSync(path.join("/src", s), path.join("/out", path.basename(s)), { recursive: true }); \
	'

FROM --platform=$BUILDPLATFORM node:26.10.0-trixie-slim@sha256:ec7758ee051e457b468b32bde57b0879010b325bb9862718e9615225ce4aaae1 AS gh
ARG TARGETARCH
# renovate: datasource=github-releases depName=cli/cli extractVersion=^v(?<version>.*)$
ARG GH_VERSION=2.102.0
SHELL ["/bin/bash", "-o", "pipefail", "-c"]
ADD https://github.com/cli/cli/releases/download/v${GH_VERSION}/gh_${GH_VERSION}_linux_${TARGETARCH}.tar.gz /tmp/gh.tar.gz
ADD https://github.com/cli/cli/releases/download/v${GH_VERSION}/gh_${GH_VERSION}_checksums.txt /tmp/checksums.txt
RUN echo "$(grep " gh_${GH_VERSION}_linux_${TARGETARCH}.tar.gz$" /tmp/checksums.txt | cut -d' ' -f1)  /tmp/gh.tar.gz" | sha256sum -c - \
	&& tar -xzf /tmp/gh.tar.gz -C /tmp \
	&& install -D -m 0755 /tmp/gh_${GH_VERSION}_linux_${TARGETARCH}/bin/gh /out/gh

FROM node:26.10.0-trixie-slim@sha256:ec7758ee051e457b468b32bde57b0879010b325bb9862718e9615225ce4aaae1
# renovate: datasource=npm depName=@anthropic-ai/claude-code
ARG CLAUDE_CODE_VERSION=2.1.292
RUN apt-get update \
	&& apt-get install -y --no-install-recommends ca-certificates git make \
	&& rm -rf /var/lib/apt/lists/*
RUN npm install --global "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" \
	&& npm cache clean --force
COPY --from=gh /out/gh /usr/local/bin/gh
COPY --from=skills /out /etc/claude-code/.claude/skills
COPY --from=build /out/night-shift /usr/local/bin/night-shift

# La sortie réseau de l'agent est fermée sauf liste blanche : ni mise à jour
# automatique, ni télémétrie.
ENV CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1 \
	DISABLE_AUTOUPDATER=1

RUN useradd --uid 65532 --user-group --create-home --home-dir /home/nightshift --shell /usr/sbin/nologin nightshift
USER 65532:65532
ENV HOME=/home/nightshift
WORKDIR /home/nightshift
CMD ["night-shift"]
