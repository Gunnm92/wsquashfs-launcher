# Makefile pour WSquashFS Launcher

.PHONY: help install uninstall test clean build-docker

help:
	@echo "WSquashFS Launcher - Makefile"
	@echo "=============================="
	@echo ""
	@echo "Commandes disponibles :"
	@echo ""
	@echo "  make install          - Installer wsquashfs-run (lance install.sh)"
	@echo "  make uninstall        - Désinstaller wsquashfs-run"
	@echo "  make test             - Vérifier les dépendances"
	@echo "  make clean            - Nettoyer les fichiers temporaires"
	@echo "  make build-docker     - Construire l'image Docker (optionnel)"
	@echo "  make help             - Afficher cette aide"
	@echo ""

install:
	@chmod +x install.sh
	@./install.sh

uninstall:
	@chmod +x install.sh
	@./install.sh --uninstall

build-docker:
	@echo "Construction de l'image Docker..."
	@docker-compose build
	@echo "✅ Image Docker construite avec succès !"

run-docker:
	@echo "Pour lancer un jeu avec Docker, utilisez :"
	@echo "  ./run-docker.sh /path/to/game.wsquashfs"
	@echo "Ou :"
	@echo "  docker-compose run --rm wsquashfs-launcher /games/game.wsquashfs"

test:
	@echo "Vérification des dépendances..."
	@which squashfuse > /dev/null || echo "❌ squashfuse non trouvé"
	@which wine > /dev/null || echo "❌ wine non trouvé"
	@which dos2unix > /dev/null || echo "❌ dos2unix non trouvé"
	@which docker > /dev/null || echo "⚠️  docker non trouvé (optionnel)"
	@echo "✅ Vérification terminée"

clean:
	@echo "Nettoyage des fichiers temporaires..."
	@rm -rf ~/.cache/wsquashfs/mnt/*
	@rm -rf ~/.cache/wsquashfs/wine/*
	@rm -rf ~/.cache/wsquashfs/work/*
	@echo "✅ Nettoyage terminé"
	@echo ""
	@echo "Note: Les sauvegardes dans ~/.local/share/wsquashfs/saves/ sont préservées"
