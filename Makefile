# Makefile pour WSquashFS Launcher

.PHONY: help install install-desktop build-docker test clean

help:
	@echo "WSquashFS Launcher - Makefile"
	@echo "=============================="
	@echo ""
	@echo "Commandes disponibles :"
	@echo ""
	@echo "  make install          - Installer wsquashfs-run dans /usr/local/bin"
	@echo "  make test             - Vérifier les dépendances"
	@echo "  make clean            - Nettoyer les fichiers temporaires"
	@echo "  make build-docker     - Construire l'image Docker (optionnel)"
	@echo "  make help             - Afficher cette aide"
	@echo ""

install:
	@echo "Installation de wsquashfs-run..."
	@chmod +x wsquashfs-run
	@sudo cp wsquashfs-run /usr/local/bin/
	@echo "✅ Installation terminée !"
	@echo ""
	@echo "Utilisation :"
	@echo "  wsquashfs-run <fichier.wsquashfs>"
	@echo ""
	@echo "Pour Pegasus Frontend, voir : pegasus-example/README.md"

install-desktop: install

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
