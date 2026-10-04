# Images conteneur de night-shift : make images (local) ou la CI (release).

variable "VERSION" {
  default = "dev"
}

variable "REGISTRY" {
  default = "ghcr.io/unpoiltefal"
}

group "default" {
  targets = ["base", "go"]
}

target "base" {
  context    = "."
  dockerfile = "images/base/Dockerfile"
  args       = { VERSION = VERSION }
  tags       = ["${REGISTRY}/night-shift:${VERSION}"]
}

target "go" {
  context    = "."
  dockerfile = "images/go/Dockerfile"
  contexts   = { base = "target:base" }
  tags       = ["${REGISTRY}/night-shift-go:${VERSION}"]
}
