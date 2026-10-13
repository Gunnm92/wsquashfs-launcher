# Image de wsquashfs-launcher : lance les jeux .wsquashfs (format Batocera)
# dans un conteneur Debian, avec wine-tkg, le Wine de Debian, DXVK,
# VKD3D-Proton et d7vk.
#
# Construction :  docker compose build      (ou : docker build -t wsquashfs-launcher .)
# Lancement :     ./run-docker.sh /chemin/jeu.wsquashfs
#
# Les jeux lancés par Proton (images sans prefix, prefixes Heroic/Steam) ne
# sont pas couverts : ni Proton ni umu dans l'image ; ils passent par Wine.

FROM debian:bookworm-slim

# Versions des composants téléchargés (docker build --build-arg ...)
ARG WINE_TKG_VERSION=11.19
ARG DXVK_VERSION=3.1.1
ARG VKD3D_VERSION=3.0.1
ARG D7VK_VERSION=2.3
# Utilisateur du conteneur : même UID/GID que l'utilisateur de l'hôte, pour
# l'accès à son serveur X11/Wayland, à son son et aux fichiers montés.
ARG UID=1000
ARG GID=1000

ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8

# Paquets système :
#  - montage des images : squashfuse, fuse3 (fusermount3), fuse-overlayfs,
#    squashfs-tools (mode copy, --info, --pack) ;
#  - outils du lanceur : attr (--clean), zstd (--backup), util-linux (flock),
#    procps (pgrep), python3 (dualsense-usb-bridge) ;
#  - Wine de Debian (64 et 32 bits) : ses dépendances i386 servent aussi à
#    wine-tkg (non WoW64) ; libxkbcommon0:i386 pour son pilote Wayland 32 bits ;
#  - pilotes Vulkan/OpenGL Mesa 64 et 32 bits (DXVK, VKD3D, d7vk).
RUN dpkg --add-architecture i386 \
    && apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates curl xz-utils zstd unzip \
        squashfuse fuse3 fuse-overlayfs squashfs-tools \
        attr util-linux procps python3 \
        wine wine64 wine32:i386 libxkbcommon0:i386 \
        mesa-vulkan-drivers mesa-vulkan-drivers:i386 \
        libgl1-mesa-dri libgl1-mesa-dri:i386 \
        libvulkan1 libvulkan1:i386 \
    && rm -rf /var/lib/apt/lists/*

# wine-tkg (Kron4ek, staging-tkg, non WoW64) : le Wine par défaut du lanceur
# pour les jeux arcade Batocera, cherché en /usr/wine/wine-tkg comme sous
# Batocera.
RUN set -eux; \
    mkdir -p /usr/wine; \
    curl -fsSL "https://github.com/Kron4ek/Wine-Builds/releases/download/${WINE_TKG_VERSION}/wine-${WINE_TKG_VERSION}-staging-tkg-amd64.tar.xz" \
        | tar -xJ -C /usr/wine; \
    mv "/usr/wine/wine-${WINE_TKG_VERSION}-staging-tkg-amd64" /usr/wine/wine-tkg; \
    /usr/wine/wine-tkg/bin/wine --version

# DXVK, VKD3D-Proton et d7vk (versions Windows) dans /usr/share, où le
# lanceur les cherche.
RUN set -eux; \
    curl -fsSL "https://github.com/doitsujin/dxvk/releases/download/v${DXVK_VERSION}/dxvk-${DXVK_VERSION}.tar.gz" \
        | tar -xz -C /usr/share; \
    mv "/usr/share/dxvk-${DXVK_VERSION}" /usr/share/dxvk; \
    curl -fsSL "https://github.com/HansKristian-Work/vkd3d-proton/releases/download/v${VKD3D_VERSION}/vkd3d-proton-${VKD3D_VERSION}.tar.zst" \
        | tar --zstd -x -C /usr/share; \
    mv "/usr/share/vkd3d-proton-${VKD3D_VERSION}" /usr/share/vkd3d-proton; \
    curl -fsSL -o /tmp/d7vk.zip "https://github.com/WinterSnowfall/d7vk/releases/download/v${D7VK_VERSION}/d7vk-v${D7VK_VERSION}.zip"; \
    unzip -q /tmp/d7vk.zip -d /usr/share; \
    mv "/usr/share/d7vk-v${D7VK_VERSION}" /usr/share/d7vk; \
    rm -f /tmp/d7vk.zip; \
    test -f /usr/share/dxvk/x64/dxgi.dll; \
    test -f /usr/share/vkd3d-proton/x64/d3d12.dll; \
    test -f /usr/share/d7vk/x32/ddraw.dll

RUN groupadd -o -g "${GID}" wineuser \
    && useradd -m -o -u "${UID}" -g "${GID}" -s /bin/bash wineuser

COPY wsquashfs-launcher /usr/local/bin/wsquashfs-launcher
COPY tools/dualsense-usb-bridge/dualsense-usb-bridge /usr/local/bin/dualsense-usb-bridge

USER wineuser
WORKDIR /home/wineuser
# Points de montage des volumes (sauvegardes, cache) créés à l'avance : un
# volume Docker reprend leur propriétaire, wineuser.
RUN mkdir -p /home/wineuser/.local/share/wsquashfs /home/wineuser/.cache/wsquashfs

ENTRYPOINT ["/usr/local/bin/wsquashfs-launcher"]
CMD ["--help"]
