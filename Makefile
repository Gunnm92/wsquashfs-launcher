# Makefile pour WSquashFS Launcher

.PHONY: help build install uninstall test test-update lint deps clean build-docker run-docker

INSTALL_DIR ?= /usr/local/bin

help:
	@echo "WSquashFS Launcher"
	@echo "=================="
	@echo ""
	@echo "  make build          Compiler wsquashfs-launcher (binaire Go statique)"
	@echo "  make install        Installer wsquashfs-launcher (wsquashfs-launcher --setup)"
	@echo "  make uninstall      Désinstaller wsquashfs-launcher"
	@echo "  make test           Lancer les tests de non-régression (tests/)"
	@echo "  make test-update    Réécrire la trace attendue des tests"
	@echo "  make lint           Analyser le code (go vet, gofmt, shellcheck)"
	@echo "  make deps           Vérifier les dépendances"
	@echo "  make clean          Supprimer les copies de travail"
	@echo "  make build-docker   Construire l'image Docker"
	@echo "  make run-docker     Aide pour lancer un jeu via Docker"
	@echo ""

# Binaire Go statique (cmd/wsquashfs-launcher/), sans cgo (montage FUSE de l'image en Go pur). Le binaire
# versionné à la racine est régénéré par cette cible.
build:
	@command -v go >/dev/null || { echo "go absent (Go 1.26.7 ou plus récent ; go1.27.4 pour reproduire l'empreinte du binaire versionné)"; exit 1; }
	@cd cmd/wsquashfs-launcher && CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-s -w" -o ../../wsquashfs-launcher .
	@sha256sum wsquashfs-launcher > wsquashfs-launcher.sha256
	@echo "✓ wsquashfs-launcher compilé (empreinte : wsquashfs-launcher.sha256, à committer avec lui)"

# Le binaire du dépôt s'installe lui-même (install.sh ne sert qu'à le
# télécharger sur une machine qui ne l'a pas encore).
install:
	@./wsquashfs-launcher --setup

uninstall:
	@./wsquashfs-launcher --setup uninstall

test:
	@cd cmd/wsquashfs-launcher && go test ./...
	@tests/run-tests.sh

test-update:
	@tests/run-tests.sh --update

lint:
	@cd cmd/wsquashfs-launcher && go vet ./... && test -z "`gofmt -l .`" && echo "✓ go vet, gofmt"
	@command -v shellcheck >/dev/null || { echo "shellcheck absent"; exit 1; }
	@shellcheck install.sh tests/run-tests.sh && echo "✓ shellcheck"

deps:
	@echo "Vérification des dépendances..."
	@echo ""
	@command -v wine          >/dev/null && echo "✓ wine"          || echo "✗ wine (requis)"
	@command -v fusermount3   >/dev/null && echo "✓ fusermount3 (image montée par le lanceur)" || echo "- fusermount3 (fuse3, optionnel si unsquashfs présent)"
	@command -v unsquashfs    >/dev/null && echo "✓ unsquashfs"    || echo "- unsquashfs (optionnel si fusermount3 présent)"
	@command -v fuse-overlayfs >/dev/null && echo "✓ fuse-overlayfs (mode overlay)" || echo "- fuse-overlayfs absent (mode copy utilisé)"
	@echo ""
	@command -v wine >/dev/null && (command -v fusermount3 >/dev/null || command -v unsquashfs >/dev/null) \
		&& echo "✓ Installation fonctionnelle" \
		|| echo "✗ Dépendances manquantes — voir README.md"

clean:
	@echo "Suppression des copies de travail..."
	@rm -rf ~/.cache/wsquashfs/mnt ~/.cache/wsquashfs/wine ~/.cache/wsquashfs/work
	@echo "✓ Copies supprimées"
	@echo "  Les sauvegardes overlay sont préservées dans ~/.local/share/wsquashfs/saves/"

build-docker:
	@echo "Construction de l'image Docker..."
	@docker compose build
	@echo "✓ Image construite"

run-docker:
	@echo "Lancer un jeu avec Docker :"
	@echo "  ./run-docker.sh /path/to/game.wsquashfs"
	@echo "Ou :"
	@echo "  docker compose run --rm wsquashfs-launcher --doctor"
