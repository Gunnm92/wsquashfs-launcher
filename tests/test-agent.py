#!/usr/bin/env python3
"""Tests de wsquashfs-agent : sécurité des opérations, authentification,
activation par socket, arrêt après inactivité.

Lancé par tests/run-tests.sh (ou directement : python3 tests/test-agent.py).
Un faux lanceur (WSQUASHFS_AGENT_LAUNCHER) remplace wsquashfs-launcher ; rien
n'est lancé dans une vraie session.
"""
import json
import os
import shutil
import socket
import ssl
import subprocess
import sys
import tempfile
import time
import unittest
import urllib.error
import urllib.request
from pathlib import Path

HERE = Path(__file__).resolve().parent
AGENT = str(HERE.parent / "tools" / "wsquashfs-agent" / "wsquashfs-agent")


def free_port() -> int:
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def request(port: int, method: str, path: str, token: str | None = None, body: dict | None = None):
    headers = {}
    if token is not None:
        headers["Authorization"] = f"Bearer {token}"
    data = None
    if body is not None:
        data = json.dumps(body).encode()
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(f"https://127.0.0.1:{port}{path}", data=data, method=method, headers=headers)
    ctx = ssl._create_unverified_context()
    try:
        with urllib.request.urlopen(req, context=ctx, timeout=20) as resp:
            return resp.status, json.loads(resp.read() or b"{}")
    except urllib.error.HTTPError as exc:
        return exc.code, json.loads(exc.read() or b"{}")


class AgentTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        if not shutil.which("openssl"):
            raise unittest.SkipTest("openssl absent")
        cls.tmp = Path(tempfile.mkdtemp(prefix="wsa-test-"))
        cls.env = dict(os.environ, WSQUASHFS_AGENT_CONFIG=str(cls.tmp / "conf"),
                       WSQUASHFS_AGENT_LAUNCHER="/bin/true")
        cls.roms = cls.tmp / "roms" / "win"
        cls.roms.mkdir(parents=True)
        (cls.roms / "Jeu.wsquashfs").write_bytes(b"hsqs")
        # Un runner installé (vu par --doctor) et un dossier qui n'en est pas un
        cls.runner = cls.tmp / "runners" / "GE-Proton"
        (cls.runner / "files/bin").mkdir(parents=True)
        (cls.runner / "files/bin/wine").write_bytes(b"x" * 10)
        (cls.runner / "proton").symlink_to("files/bin/wine")
        cls.secret = cls.tmp / "secret"
        cls.secret.mkdir()
        cls.fake_launcher = cls.tmp / "wsquashfs-launcher"
        cls.fake_launcher.write_text(f"#!/bin/sh\necho 'Runners :'\necho '      GE-Proton  {cls.runner}'\n")
        cls.fake_launcher.chmod(0o755)
        cls.env["WSQUASHFS_AGENT_LAUNCHER"] = str(cls.fake_launcher)
        subprocess.run([AGENT, "init", "--roms", str(cls.tmp / "roms")], env=cls.env, check=True,
                       stdout=subprocess.DEVNULL)
        cls.token = json.loads((cls.tmp / "conf" / "config.json").read_text())["token"]
        cls.port = free_port()
        cls.server = subprocess.Popen([AGENT, "serve", "--standalone", "--port", str(cls.port)],
                                      env=cls.env, stderr=subprocess.DEVNULL)
        for _ in range(50):
            try:
                request(cls.port, "GET", "/v1/info", cls.token)
                break
            except OSError:
                time.sleep(0.1)

    @classmethod
    def tearDownClass(cls):
        cls.server.terminate()
        cls.server.wait(10)
        shutil.rmtree(cls.tmp, ignore_errors=True)

    # ── authentification
    def test_token_required(self):
        self.assertEqual(request(self.port, "GET", "/v1/info")[0], 401)
        self.assertEqual(request(self.port, "GET", "/v1/info", "faux")[0], 401)
        code, data = request(self.port, "GET", "/v1/info", self.token)
        self.assertEqual(code, 200)
        self.assertEqual(data["version"], "1")

    def test_unknown_route(self):
        self.assertEqual(request(self.port, "GET", "/v1/shell", self.token)[0], 404)

    # ── maintenance : liste fermée
    def test_maintenance_only_listed_actions(self):
        code, _ = request(self.port, "POST", "/v1/maintenance", self.token, {"action": "clear"})
        self.assertEqual(code, 400)
        code, _ = request(self.port, "POST", "/v1/maintenance", self.token, {"action": "pack"})
        self.assertEqual(code, 400)
        code, _ = request(self.port, "POST", "/v1/maintenance", self.token, {"action": "backup"})
        self.assertEqual(code, 400)
        # Un jeu : seulement pour saves, et seulement une image des dossiers roms/
        code, _ = request(self.port, "POST", "/v1/maintenance", self.token,
                          {"action": "clean", "rel": "win/Jeu.wsquashfs"})
        self.assertEqual(code, 400)
        code, _ = request(self.port, "POST", "/v1/maintenance", self.token,
                          {"action": "saves", "rel": "../secret/x.wsquashfs"})
        self.assertEqual(code, 400)

    # ── trial : chemins et durées
    def test_trial_refuses_escape_and_bad_input(self):
        for rel in ["../secret/x.wsquashfs", "/etc/passwd.wsquashfs", "win/../../x.wsquashfs", "win/Jeu.txt", ""]:
            code, _ = request(self.port, "POST", "/v1/trial", self.token, {"rel": rel, "duration": 60})
            self.assertEqual(code, 400, rel)
        code, _ = request(self.port, "POST", "/v1/trial", self.token, {"rel": "win/Jeu.wsquashfs", "duration": 5})
        self.assertEqual(code, 400)
        code, _ = request(self.port, "POST", "/v1/trial", self.token, {"rel": "win/Absent.wsquashfs", "duration": 60})
        self.assertEqual(code, 404)

    def test_symlink_out_of_roms_refused(self):
        (self.roms / "lien.wsquashfs").symlink_to(self.secret)
        code, _ = request(self.port, "POST", "/v1/trial", self.token, {"rel": "win/lien.wsquashfs", "duration": 60})
        self.assertEqual(code, 404)

    # ── runners : seulement ceux de --doctor
    def test_runner_list_only_installed_runners(self):
        code, data = request(self.port, "GET", f"/v1/runner/list?path={self.runner}", self.token)
        self.assertEqual(code, 200)
        self.assertEqual(data["entries"]["files/bin/wine"], 10)
        self.assertEqual(data["entries"]["proton"], 0)
        code, _ = request(self.port, "GET", "/v1/runner/list?path=/etc", self.token)
        self.assertEqual(code, 403)
        code, _ = request(self.port, "GET", f"/v1/runner/list?path={self.runner}/../../secret", self.token)
        self.assertEqual(code, 403)
        code, _ = request(self.port, "GET", "/v1/runner/list?path=relatif", self.token)
        self.assertEqual(code, 400)

    def test_doctor_lists_runners(self):
        code, data = request(self.port, "GET", "/v1/doctor", self.token)
        self.assertEqual(code, 200)
        self.assertEqual(data["runners"], [{"name": "GE-Proton", "path": str(self.runner)}])

    def test_pair_fingerprint_matches_certificate(self):
        out = subprocess.run([AGENT, "pair"], env=self.env, capture_output=True, text=True, check=True).stdout
        fp = [line.split(":", 1)[1].strip() for line in out.splitlines() if line.startswith("Empreinte")][0]
        der = ssl.PEM_cert_to_DER_cert((self.tmp / "conf" / "cert.pem").read_text())
        import hashlib
        digest = hashlib.sha256(der).hexdigest().upper()
        self.assertEqual(fp, ":".join(digest[i:i + 2] for i in range(0, len(digest), 2)))


class SocketActivationTest(unittest.TestCase):
    """Le service démarre sur la socket fournie par systemd (LISTEN_FDS) et
    quitte seul après une période sans requête."""

    @classmethod
    def setUpClass(cls):
        if not shutil.which("openssl"):
            raise unittest.SkipTest("openssl absent")
        cls.tmp = Path(tempfile.mkdtemp(prefix="wsa-sock-"))
        cls.env = dict(os.environ, WSQUASHFS_AGENT_CONFIG=str(cls.tmp / "conf"),
                       WSQUASHFS_AGENT_LAUNCHER="/bin/true", WSQUASHFS_AGENT_IDLE="3")
        subprocess.run([AGENT, "init"], env=cls.env, check=True, stdout=subprocess.DEVNULL)
        cls.token = json.loads((cls.tmp / "conf" / "config.json").read_text())["token"]

    @classmethod
    def tearDownClass(cls):
        shutil.rmtree(cls.tmp, ignore_errors=True)

    def test_started_on_socket_and_exits_when_idle(self):
        listener = socket.socket()
        listener.bind(("127.0.0.1", 0))
        listener.listen(8)
        port = listener.getsockname()[1]
        fd = listener.fileno()
        # sh remplace son processus par l'agent : LISTEN_PID = pid de l'agent
        child = subprocess.Popen(["sh", "-c", f'LISTEN_PID=$$ LISTEN_FDS=1 exec "{AGENT}" serve'],
                                 env=self.env, pass_fds=[fd], stderr=subprocess.DEVNULL)
        try:
            # L'agent n'est pas en écoute sur un autre port : la socket est celle de l'appelant
            for _ in range(50):
                try:
                    code, data = request(port, "GET", "/v1/info", self.token)
                    break
                except OSError:
                    time.sleep(0.1)
            self.assertEqual(code, 200)
            self.assertEqual(child.wait(timeout=30), 0, "l'agent doit quitter seul après l'inactivité")
        finally:
            if child.poll() is None:
                child.kill()
            listener.close()


if __name__ == "__main__":
    unittest.main(verbosity=2)
