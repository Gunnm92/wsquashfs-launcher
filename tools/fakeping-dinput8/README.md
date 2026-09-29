# fakeping-dinput8 — faux ping ICMP pour jeux Windows sous Wine

Relais `dinput8.dll` (32 bits) pour les jeux qui ouvrent un **socket brut ICMP**
(ping) et plantent quand il est refusé. Écrit pour *Yu-Gi-Oh! 5D's Duel
Terminal 6* (Konami, e-amusement).

## Le problème

Sous Wine, les sockets sont créés par `wineserver`. Un `SOCK_RAW`/`IPPROTO_ICMP`
exige `CAP_NET_RAW`, ou à défaut un `net.ipv4.ping_group_range` qui autorise les
pings sans privilège (repli de Wine sur `SOCK_DGRAM`). Si aucun des deux n'est
disponible, Wine refuse le socket :

```
err:winediag:WSASocketW Failed to create a socket of type SOCK_RAW, this requires special permissions.
```

Yu-Gi-Oh! DT6 s'arrête alors sur une assertion (`EXCEPTION_BREAKPOINT`), 30 à
50 s après le démarrage. C'est le cas dans un conteneur non privilégié en réseau
hôte, où `ping_group_range` est celui de l'hôte. Sous Batocera (jeux lancés en
root), le socket se crée et le problème n'existe pas.

## Fonctionnement

`ws2_32` est une *KnownDLL* : Windows comme Wine chargent toujours celle du
système, jamais une copie dans le dossier du jeu. Le relais se place donc à la
place de `dinput8.dll`, que le jeu importe :

- ses 5 fonctions sont transmises au vrai `dinput8` du système ;
- à son chargement, il redirige dans la table d'imports de chaque module déjà
  chargé `socket`, `WSASocketA/W`, `bind`, `send`, `sendto`, `WSASend`,
  `WSASendTo`, `setsockopt` et `closesocket`. Les imports par nom comme par
  ordinal sont couverts, par comparaison d'adresses ;
- **seulement si** la création d'un socket brut ICMP échoue, il rend un socket
  UDP lié à 127.0.0.1. Sur ce faux socket, `bind`, les envois et `setsockopt`
  réussissent sans effet, et les réceptions ne trouvent rien : c'est un ping
  sans réponse.

Là où le vrai socket se crée, le relais ne change rien.

Les messages `fakeping : …` sont visibles avec `WINEDEBUG=+debugstr`.

## Compilation

Il faut mingw-w64 i686. Exemple dans un conteneur Debian jetable :

```bash
tar -cf - fakeping.c dinput8.def | docker run --rm -i debian:stable-slim bash -c '
  apt-get update -qq && apt-get install -y -qq gcc-mingw-w64-i686 >/dev/null
  mkdir /b && cd /b && tar -xf -
  i686-w64-mingw32-gcc -O2 -Wall -Wextra -Wno-cast-function-type -shared \
    -o dinput8.dll fakeping.c dinput8.def -static-libgcc -Wl,--enable-stdcall-fixup >&2
  i686-w64-mingw32-strip dinput8.dll && cat dinput8.dll' > dinput8.dll
```

## Installation dans une image `.wsquashfs`

1. Copier `dinput8.dll` dans le dossier de l'exécutable du jeu.
2. Forcer la version native pour cet exécutable seulement, dans le `user.reg` du
   prefix :

   ```
   [Software\\Wine\\AppDefaults\\game.exe\\DllOverrides]
   "dinput8"="native,builtin"
   ```

Pour Yu-Gi-Oh! DT6 : `drive_c/game/Yugioh 5DS Duel Terminal 6/contents/exe/`.
SHA-256 du DLL intégré à l'image le 30/09/2026 :
`186c2d6b37f218111fb7b56d0271cdacdca440c64a65ae5cd3e497727bafde49`.
