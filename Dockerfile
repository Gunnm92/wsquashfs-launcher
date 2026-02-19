# Dockerfile pour WSquashFS Launcher
# Basé sur Debian avec Wine, SquashFuse et les dépendances nécessaires

FROM debian:bookworm-slim

# Métadonnées
LABEL maintainer="votre-email@example.com"
LABEL description="Launcher pour fichiers WSquashFS de Batocera avec Wine"
LABEL version="1.0"

# Variables d'environnement
ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8 \
    DISPLAY=:0 \
    WINEDEBUG=-all

# Installation des dépendances système
RUN dpkg --add-architecture i386 && \
    apt-get update && \
    apt-get install -y --no-install-recommends \
    # Outils de base
    ca-certificates \
    wget \
    gnupg2 \
    software-properties-common \
    apt-transport-https \
    # Outils nécessaires pour le script
    squashfuse \
    fuse \
    dos2unix \
    # Wine et dépendances
    wine \
    wine32 \
    wine64 \
    winetricks \
    # Bibliothèques graphiques pour X11
    libx11-6 \
    libxext6 \
    libxrender1 \
    libxtst6 \
    libxi6 \
    # Audio (optionnel)
    pulseaudio \
    alsa-utils \
    # Nettoyage
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

# Créer un utilisateur non-root pour Wine
RUN useradd -m -s /bin/bash wineuser && \
    echo "wineuser ALL=(ALL) NOPASSWD: ALL" >> /etc/sudoers

# Copier le script launcher
COPY wsquashfs-launcher-docker /usr/local/bin/wsquashfs-launcher
RUN chmod +x /usr/local/bin/wsquashfs-launcher

# Passer à l'utilisateur wineuser
USER wineuser
WORKDIR /home/wineuser

# Initialiser Wine (création du préfixe)
RUN wine wineboot --init && \
    wineserver -w

# Installer des composants Wine de base avec winetricks (optionnel)
# Décommenter si nécessaire pour certains jeux
# RUN winetricks -q vcrun2019 d3dx9 dotnet48

# Script d'entrée
ENTRYPOINT ["/usr/local/bin/wsquashfs-launcher"]

# Aide par défaut si aucun argument
CMD ["--help"]
