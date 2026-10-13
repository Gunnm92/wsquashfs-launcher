# Guide de contribution

Merci de votre intérêt pour contribuer à WSquashFS Launcher !

## 🚀 Comment contribuer

### Signaler un bug

1. Vérifiez que le bug n'a pas déjà été signalé dans les [Issues](../../issues)
2. Créez une nouvelle issue en incluant :
   - Une description claire du problème
   - Les étapes pour reproduire le bug
   - Le comportement attendu vs observé
   - Votre environnement (OS, version de Wine, Docker, etc.)
   - Les logs pertinents

### Proposer une fonctionnalité

1. Ouvrez une issue pour discuter de la fonctionnalité
2. Expliquez le cas d'usage et les bénéfices
3. Attendez les retours avant de commencer à coder

### Soumettre une Pull Request

1. Forkez le projet
2. Créez une branche pour votre fonctionnalité (`git checkout -b feature/ma-fonctionnalite`)
3. Committez vos changements (`git commit -m 'Ajout de ma fonctionnalité'`)
4. Pushez vers la branche (`git push origin feature/ma-fonctionnalite`)
5. Ouvrez une Pull Request

## 📝 Standards de code

### Scripts Bash

- Utilisez `#!/bin/bash` en première ligne
- Indentez avec 4 espaces
- Ajoutez des commentaires pour les sections complexes
- Vérifiez les erreurs avec `set -e` quand approprié
- Utilisez des variables en majuscules pour les constantes

### Structure

```bash
#!/bin/bash

# Description du script

# Fonction helper
ma_fonction() {
    local param=$1
    echo "$param"
}

# Variables
MA_CONSTANTE="valeur"

# Code principal
```

## 🧪 Tests

Le lanceur a une suite de tests de non-régression (`tests/run-tests.sh`) :
elle lance le vrai script sur des dizaines de scénarios, avec de vrais
montages FUSE mais un faux Wine/Proton/umu qui journalise ses appels, et
compare la trace obtenue à `tests/expected.out`.

```bash
make test          # doit afficher « ✓ N scénarios conformes »
make lint          # shellcheck
make test-update   # après un changement de comportement VOULU : relire
                   # le diff de tests/expected.out avant de commiter
```

Prérequis : squashfuse, fuse-overlayfs, squashfs-tools, attr (getfattr),
util-linux (flock) et l'accès à `/dev/fuse`. Un nouveau comportement
s'accompagne d'un scénario dans `tests/run-tests.sh`.

Avant de soumettre une PR :

1. Testez votre code sur votre système
2. Vérifiez que les scripts existants fonctionnent toujours
3. Testez avec différents fichiers wsquashfs si possible
4. Vérifiez la compatibilité Docker si vous modifiez cette partie

## 🎯 Priorités actuelles

- [ ] Support complet de Proton
- [ ] Installation automatique de DXVK/VKD3D
- [ ] Support de multiples versions de Wine dans Docker
- [x] Tests automatisés (`make test`)
- [ ] CI/CD avec GitHub Actions
- [ ] Documentation améliorée

## 💡 Idées de contribution

- Améliorer la gestion des erreurs
- Ajouter plus d'exemples d'utilisation
- Améliorer la documentation
- Supporter d'autres formats de compression
- Ajouter une GUI (optionnel)
- Optimiser les performances

## 📚 Ressources utiles

- [Documentation Batocera](https://wiki.batocera.org/)
- [Documentation Wine](https://wiki.winehq.org/)
- [Documentation Docker](https://docs.docker.com/)
- [SquashFS Tools](https://github.com/vasi/squashfuse)

## ❓ Questions

Si vous avez des questions, n'hésitez pas à :
- Ouvrir une issue de type "Question"
- Consulter les discussions existantes
- Contacter les mainteneurs

Merci de contribuer ! 🎉
