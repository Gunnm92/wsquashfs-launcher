package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCompletionInstallAndRemove : --setup écrit le script et la ligne de ~/.bashrc une seule fois ;
// la désinstallation les retire sans toucher au reste de ~/.bashrc.
func TestCompletionInstallAndRemove(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	old, oldEnv := wsqData, env
	t.Cleanup(func() { wsqData, env = old, oldEnv })
	wsqData = filepath.Join(home, ".local", "share", "wsquashfs")
	env = Env{CompletionScript: "# script de complétion d'essai\n"}
	rc := filepath.Join(home, ".bashrc")
	if err := os.WriteFile(rc, []byte("alias ll='ls -l'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := installCompletion(); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(rc)
	if strings.Count(string(data), completionSourceLine()) != 1 {
		t.Errorf(".bashrc : %q", data)
	}
	if script, _ := os.ReadFile(completionPath()); string(script) != env.CompletionScript {
		t.Error("script de complétion absent")
	}
	if !removeCompletion() {
		t.Error("rien retiré")
	}
	data, _ = os.ReadFile(rc)
	if strings.Contains(string(data), "wsquashfs") || !strings.Contains(string(data), "alias ll='ls -l'") {
		t.Errorf(".bashrc après retrait : %q", data)
	}
	if isFile(completionPath()) {
		t.Error("script toujours là")
	}
}
