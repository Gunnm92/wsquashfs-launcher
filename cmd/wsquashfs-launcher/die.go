package main

// die.go : sorties d'erreur de la commande. Le paquet ui affiche, il ne
// quitte jamais ; la sortie avec cleanup (exitNow) reste ici.
//
// Règle : die et usageError sont réservés au chemin du lancement d'un jeu
// (launcherMain → launchGame et ses dépendances), où un montage, l'overlay, le
// pont manette ou la commande ATTACH peuvent être actifs et où la sortie doit
// passer par cleanup (exitNow). Les commandes autonomes (--pack, --install,
// --info, --show, --save, --trial, --update…) ne montent rien : elles
// affichent l'erreur avec fail ou usage et renvoient le code de sortie, que
// run() rend à main.

import "wsquashfs-launcher/internal/ui"

// die : erreur, puis sortie (code 1, cleanup compris).
func die(msg string, details ...string) {
	ui.Error(msg, details...)
	exitNow(1)
}

// usageError : rappel de syntaxe, puis code 1.
func usageError(syntax string) {
	ui.Usage(syntax)
	exitNow(1)
}

// fail : erreur affichée, code de sortie 1 (commande autonome, sans cleanup).
func fail(msg string, details ...string) int {
	ui.Error(msg, details...)
	return 1
}

// usage : rappel de syntaxe, code de sortie 1 (commande autonome).
func usage(syntax string) int {
	ui.Usage(syntax)
	return 1
}
