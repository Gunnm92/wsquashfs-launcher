# Makefile pour WSquashFS Launcher

.PHONY: help install uninstall test clean build-docker run-docker

INSTALL_DIR ?= /usr/local/bin

help:
	@echo "WSquashFS Launcher"
	@echo "=================="
	@echo ""
	@echo "  make install        Installer wsquashfs-launcher (lance install.sh)"
	@echo "  make uninstall      Désinstaller wsquashfs-launcher"
	@echo "  make test           Vérifier les dépendances"
	@echo "  make clean          Supprimer les copies de travail"
	@echo "  make build-docker   Construire l'image Docker"
	@echo "  make run-docker     Aide pour lancer un jeu via Docker"
	@echo ""

install:
	@chmod +x install.sh wsquashfs-launcher
	@./install.sh

uninstall:
	@chmod +x install.sh
	@./install.sh --uninstall

test:
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
	@docker-compose build
	@echo "✓ Image construite"

run-docker:
	@echo "Lancer un jeu avec Docker :"
	@echo "  ./run-docker.sh /path/to/game.wsquashfs"
	@echo "Ou :"
	@echo "  docker-compose run --rm wsquashfs-launcher /games/game.wsquashfs"
