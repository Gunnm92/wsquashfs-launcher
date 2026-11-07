# Makefile pour WSquashFS Launcher

.PHONY: help build install uninstall test test-update lint deps clean build-docker run-docker

INSTALL_DIR ?= /usr/local/bin

help:
	@echo "WSquashFS Launcher"
	@echo "=================="
	@echo ""
	@echo "  make build          Compiler wsquashfs-launcher (binaire Go, sans dépendance)"
	@echo "  make install        Installer wsquashfs-launcher (lance install.sh)"
	@echo "  make uninstall      Désinstaller wsquashfs-launcher"
	@echo "  make test           Lancer les tests de non-régression (tests/)"
	@echo "  make test-update    Réécrire la trace attendue des tests"
	@echo "  make lint           Analyser le code (go vet, gofmt, shellcheck)"
	@echo "  make deps           Vérifier les dépendances"
	@echo "  make clean          Supprimer les copies de travail"
	@echo "  make build-docker   Construire l'image Docker"
	@echo "  make run-docker     Aide pour lancer un jeu via Docker"
	@echo ""

# Binaire Go statique, sans dépendance (cmd/wsquashfs-launcher/). Le binaire
# versionné à la racine est régénéré par cette cible.
build:
	@command -v go >/dev/null || { echo "go absent (Go 1.22 ou plus récent)"; exit 1; }
	@cd cmd/wsquashfs-launcher && CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-s -w" -o ../../wsquashfs-launcher .
	@sha256sum wsquashfs-launcher > wsquashfs-launcher.sha256
	@echo "✓ wsquashfs-launcher compilé (empreinte : wsquashfs-launcher.sha256, à committer avec lui)"

install:
	@chmod +x install.sh
	@./install.sh

uninstall:
	@chmod +x install.sh
	@./install.sh --uninstall

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
	@command -v squashfuse    >/dev/null && echo "✓ squashfuse"    || echo "- squashfuse (optionnel si unsquashfs présent)"
	@command -v unsquashfs    >/dev/null && echo "✓ unsquashfs"    || echo "- unsquashfs (optionnel si squashfuse présent)"
	@command -v fuse-overlayfs >/dev/null && echo "✓ fuse-overlayfs (mode overlay)" || echo "- fuse-overlayfs absent (mode copy utilisé)"
	@echo ""
	@command -v wine >/dev/null && (command -v squashfuse >/dev/null || command -v unsquashfs >/dev/null) \
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
