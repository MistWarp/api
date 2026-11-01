import argparse
import difflib
import gzip
import hashlib
import io
import json
import os
import re
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
import uuid
import zipfile

PROJECT_JSON = {
    "targets": [{
        "isStage": True, "name": "Stage", "variables": {}, "lists": {}, "broadcasts": {}, "blocks": {},
        "comments": {}, "currentCostume": 0, "costumes": [], "sounds": [], "volume": 100, "layerOrder": 0,
        "tempo": 60, "videoTransparency": 50, "videoState": "on", "textToSpeechLanguage": None,
    }],
    "monitors": [], "extensions": [], "meta": {"semver": "3.0.0", "vm": "0.2.0", "agent": "smoke"},
}

USERS = {"sam": "u_sam", "alice": "u_alice"}
VOLATILE_KEYS = re.compile(r"(^|_)(id|_id|at|time|timestamp|created|updated|modified|expires|sha|commit|head|token|etag|key|url)$|At$|Id$|Ms$|Url$|Key$|Sha$", re.I)
APPROX_KEYS = {"bytes", "mistwarp", "sizeBytes"}
HEX = re.compile(r"^[0-9a-f]{32,64}$")
ISO = re.compile(r"^\d{4}-\d{2}-\d{2}T")


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def default_sb3():
    buffer = io.BytesIO()
    with zipfile.ZipFile(buffer, "w") as archive:
        archive.writestr("project.json", json.dumps(PROJECT_JSON))
    return buffer.getvalue()


class Server:
    def __init__(self, binary, keep):
        self.binary = os.path.abspath(binary)
        self.keep = keep
        self.dir = tempfile.mkdtemp(prefix="mw-smoke-")
        self.port = free_port()
        self.base = "http://127.0.0.1:%d" % self.port
        data = os.path.join(self.dir, "data")
        os.makedirs(os.path.join(data, "sessions"))
        with open(os.path.join(self.dir, ".env"), "w") as env:
            env.write("PORT=%d\nAPP_URL=http://127.0.0.1:%d\nROTUR_BASE=http://127.0.0.1:9\n" % (self.port, self.port))
        with open(os.path.join(data, "userids.json"), "w") as handle:
            json.dump({"users": {uid: name for name, uid in USERS.items()}}, handle)
        for name, uid in USERS.items():
            with open(os.path.join(data, "sessions", "tok_%s.json" % name), "w") as handle:
                json.dump({"username": name, "userId": uid, "createdAt": int(time.time() * 1000)}, handle)
        self.log = open(os.path.join(self.dir, "server.log"), "w")
        self.process = subprocess.Popen([self.binary], cwd=self.dir, stdout=self.log, stderr=subprocess.STDOUT, start_new_session=True)
        for _ in range(300):
            if self.process.poll() is not None:
                raise RuntimeError("server exited early, see " + self.log.name)
            try:
                urllib.request.urlopen(self.base + "/v1/health", timeout=1)
                return
            except urllib.error.HTTPError:
                return
            except OSError:
                time.sleep(0.2)
        raise RuntimeError("server did not start, see " + self.log.name)

    def stop(self):
        self.process.terminate()
        try:
            self.process.wait(10)
        except subprocess.TimeoutExpired:
            self.process.kill()
        self.log.close()
        if not self.keep:
            shutil.rmtree(self.dir)

    def call(self, method, path, who=None, body=None, raw=None, content_type="application/json"):
        data = raw if raw is not None else (None if body is None else json.dumps(body).encode("utf-8"))
        request = urllib.request.Request(self.base + path, data=data, method=method)
        if who:
            request.add_header("Authorization", "Bearer tok_" + who)
        if data is not None:
            request.add_header("Content-Type", content_type)
        try:
            with urllib.request.urlopen(request, timeout=30) as response:
                status, text, headers = response.status, response.read(), response.headers
        except urllib.error.HTTPError as error:
            status, text, headers = error.code, error.read(), error.headers
        if text[:2] == b"\x1f\x8b":
            text = gzip.decompress(text)
        try:
            parsed = json.loads(text or b"{}")
        except ValueError:
            parsed = {"rawBytes": len(text), "sha256": hashlib.sha256(text).hexdigest()}
        return status, parsed, headers

    def upload(self, project_id, who, sb3):
        boundary = uuid.uuid4().hex
        body = (
            ("--%s\r\nContent-Disposition: form-data; name=\"project\"; filename=\"p.sb3\"\r\n"
             "Content-Type: application/octet-stream\r\n\r\n" % boundary).encode()
            + sb3 + ("\r\n--%s--\r\n" % boundary).encode()
        )
        return self.call("POST", "/v1/projects/%s/upload" % project_id, who, raw=body,
                         content_type="multipart/form-data; boundary=" + boundary)


def normalize(value, ids, key=""):
    if isinstance(value, dict):
        return {k: normalize(v, ids, k) for k, v in sorted(value.items())}
    if isinstance(value, list):
        return [normalize(v, ids, key) for v in value]
    if isinstance(value, str):
        value = re.sub(r"127\.0\.0\.1:\d+", "127.0.0.1:<port>", value)
        for real, alias in ids.items():
            value = value.replace(real, alias)
        if HEX.match(value) or ISO.match(value):
            return "<volatile>"
        if VOLATILE_KEYS.search(key) and not value.startswith("<project"):
            return "<volatile>"
        return value
    if isinstance(value, (int, float)) and not isinstance(value, bool):
        if key in APPROX_KEYS:
            return "~%d KiB" % round(value / 1024)
        if VOLATILE_KEYS.search(key) or value >= 1e12:
            return "<volatile>"
    return value


def run(binary, sb3, keep):
    server = Server(binary, keep)
    ids = {}
    steps = []

    def step(name, method, path, who=None, body=None, upload=None, headers=()):
        real_path = path
        for alias, real in ((a, r) for r, a in ids.items()):
            real_path = real_path.replace(alias, real)
        if upload:
            status, parsed, response_headers = server.upload(upload, who, sb3)
        else:
            status, parsed, response_headers = server.call(method, real_path, who, body)
        record = {"step": name, "status": status, "body": parsed}
        for header in headers:
            record[header] = response_headers.get(header, "")
        steps.append(record)
        return status, parsed

    try:
        step("agreement", "GET", "/v1/agreement")
        step("explore", "GET", "/v1/explore")
        step("classroom available", "GET", "/v1/classroom/available")
        step("me", "GET", "/v1/me", "sam")
        step("accept agreement", "POST", "/v1/agreement/accept", "sam", {})
        step("accept agreement alice", "POST", "/v1/agreement/accept", "alice", {})
        status, created = step("create project", "POST", "/v1/projects", "sam", {"title": "Smoke test"})
        project_id = str(created.get("id", "")) if isinstance(created, dict) else ""
        if project_id:
            ids[project_id] = "<project>"
        step("upload", "POST", "", "sam", upload=project_id)
        step("get project", "GET", "/v1/projects/<project>", "sam")
        step("get project json", "GET", "/v1/projects/<project>/project.json", "sam")
        step("quota", "GET", "/v1/me/quota", "sam")
        step("share", "POST", "/v1/projects/<project>/visibility", "sam", {"visibility": "public"})
        step("get project anon", "GET", "/v1/projects/<project>")
        status, remixed = step("remix", "POST", "/v1/projects/<project>/remix", "alice", {})
        remix_id = ""
        if isinstance(remixed, dict):
            remix_id = str(remixed.get("id", "") or (remixed.get("project") or {}).get("id", ""))
        if remix_id:
            ids[remix_id] = "<project-remix>"
        step("get remix", "GET", "/v1/projects/<project-remix>", "alice")
        step("remixes", "GET", "/v1/projects/<project>/remixes")
        step("quota alice", "GET", "/v1/me/quota", "alice")
        step("delete remix", "DELETE", "/v1/projects/<project-remix>", "alice")
        step("trash", "GET", "/v1/me/trash", "alice")
        step("purge remix", "DELETE", "/v1/trash/<project-remix>", "alice")
        step("delete project", "DELETE", "/v1/projects/<project>", "sam")
        step("missing project", "GET", "/v1/projects/nope")
        step("wrong method", "PATCH", "/v1/projects/<project>", "sam", {}, headers=("Allow",))
        step("legacy prefix", "GET", "/api/agreement")
        step("health", "GET", "/v1/health")
    finally:
        server.stop()
    for record in steps:
        record["body"] = normalize(record["body"], ids)
    return steps


def main():
    parser = argparse.ArgumentParser(description="Offline end to end smoke run of a compiled mistwarp-api binary")
    parser.add_argument("binaries", nargs="+", help="one binary to record, or two to compare")
    parser.add_argument("--sb3", help="sb3 file to upload")
    parser.add_argument("--out", help="directory for the normalized responses")
    parser.add_argument("--keep", action="store_true", help="keep each server's data directory and log")
    args = parser.parse_args()
    sb3 = open(args.sb3, "rb").read() if args.sb3 else default_sb3()
    outputs = []
    broken = False
    for binary in args.binaries:
        steps = run(binary, sb3, args.keep)
        text = json.dumps(steps, indent=2, sort_keys=True) + "\n"
        outputs.append(text)
        if args.out:
            os.makedirs(args.out, exist_ok=True)
            with open(os.path.join(args.out, os.path.basename(binary) + ".json"), "w") as handle:
                handle.write(text)
        failures = [s for s in steps if s["status"] >= 500]
        print("%s: %d steps, %d server errors" % (binary, len(steps), len(failures)))
        for record in steps:
            print("  %3d %s" % (record["status"], record["step"]))
        broken = broken or bool(failures)
    if len(outputs) == 2:
        diff = list(difflib.unified_diff(outputs[0].splitlines(), outputs[1].splitlines(), args.binaries[0], args.binaries[1], lineterm=""))
        if diff:
            print("\n".join(diff))
            sys.exit(1)
        print("responses match")
    if broken:
        sys.exit(1)


if __name__ == "__main__":
    main()
