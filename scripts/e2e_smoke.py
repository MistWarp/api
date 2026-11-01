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
import threading
import time
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import uuid
import zipfile
import zlib

PROJECT_JSON = {
    "targets": [{
        "isStage": True, "name": "Stage", "variables": {}, "lists": {}, "broadcasts": {}, "blocks": {},
        "comments": {}, "currentCostume": 0, "costumes": [], "sounds": [], "volume": 100, "layerOrder": 0,
        "tempo": 60, "videoTransparency": 50, "videoState": "on", "textToSpeechLanguage": None,
    }],
    "monitors": [], "extensions": [], "meta": {"semver": "3.0.0", "vm": "0.2.0", "agent": "smoke"},
}

USERS = {"sam": "u_sam", "alice": "u_alice", "bob": "u_bob"}
SCOPED_SESSIONS = {"sam_editor": ("sam", "editor")}
VOLATILE_NAMES = {"token", "viewKey", "expires", "created", "edited", "firstSeen", "historyBytes"}
VOLATILE_FIELDS = {("notifications", "id"), ("largest", "bytes"), ("", "mistwarp")}
SAVE_PATH = re.compile(r"^/v1/projects/([^/]+)(/upload)?$")
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


class FakeRotur(BaseHTTPRequestHandler):
    def do_GET(self):
        name = self.path.split("?", 1)[0][len("/profile/"):] if self.path.startswith("/profile/") else ""
        if name not in USERS:
            self.close_connection = True
            return
        body = json.dumps({"id": USERS[name], "username": name}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        self.close_connection = True

    def log_message(self, *args):
        pass


class Server:
    def __init__(self, binary, keep):
        self.binary = os.path.abspath(binary)
        self.keep = keep
        self.dir = tempfile.mkdtemp(prefix="mw-smoke-")
        self.port = free_port()
        self.base = "http://127.0.0.1:%d" % self.port
        self.rotur = ThreadingHTTPServer(("127.0.0.1", 0), FakeRotur)
        threading.Thread(target=self.rotur.serve_forever, daemon=True).start()
        data = os.path.join(self.dir, "data")
        os.makedirs(os.path.join(data, "sessions"))
        with open(os.path.join(self.dir, ".env"), "w") as env:
            env.write("PORT=%d\nAPP_URL=http://127.0.0.1:%d\nROTUR_BASE=http://127.0.0.1:%d\n" % (self.port, self.port, self.rotur.server_address[1]))
        with open(os.path.join(data, "agreement.json"), "w") as handle:
            json.dump({"version": 1, "text": "", "updatedAt": 1700000000000}, handle)
        with open(os.path.join(data, "agreement.md"), "w") as handle:
            handle.write("Smoke test agreement")
        with open(os.path.join(data, "userids.json"), "w") as handle:
            json.dump({"users": {uid: name for name, uid in USERS.items()}}, handle)
        for name, uid in USERS.items():
            with open(os.path.join(data, "sessions", "tok_%s.json" % name), "w") as handle:
                json.dump({"username": name, "userId": uid, "createdAt": int(time.time() * 1000)}, handle)
        for token, (name, scope) in SCOPED_SESSIONS.items():
            with open(os.path.join(data, "sessions", "tok_%s.json" % token), "w") as handle:
                json.dump({"username": name, "userId": USERS[name], "scope": scope, "parent": "tok_" + name, "createdAt": int(time.time() * 1000), "expiresAt": int(time.time() * 1000) + 86400000}, handle)
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
        self.rotur.shutdown()
        self.rotur.server_close()
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
        return self.form("/v1/projects/%s/upload" % project_id, who, {}, {"project": sb3})

    def form(self, path, who, fields, files):
        boundary = uuid.uuid4().hex
        body = b""
        for name, value in fields.items():
            body += ("--%s\r\nContent-Disposition: form-data; name=\"%s\"\r\n\r\n" % (boundary, name)).encode() + value.encode() + b"\r\n"
        for name, value in files.items():
            body += ("--%s\r\nContent-Disposition: form-data; name=\"%s\"; filename=\"%s.bin\"\r\n"
                     "Content-Type: application/octet-stream\r\n\r\n" % (boundary, name, name)).encode() + value + b"\r\n"
        body += ("--%s--\r\n" % boundary).encode()
        return self.call("POST", path, who, raw=body, content_type="multipart/form-data; boundary=" + boundary)

    def read_project(self, project_id):
        with open(os.path.join(self.dir, "data", "projects", project_id + ".json")) as handle:
            return json.load(handle)

    def blob_inventory(self):
        inventory = {}
        root = os.path.join(self.dir, "data", "blobs")
        for folder, _, names in os.walk(root):
            for name in names:
                path = os.path.relpath(os.path.join(folder, name), root)
                prefix = path.split(os.sep)[0]
                if prefix in ("assets", "project-json"):
                    inventory.setdefault(prefix, []).append(path)
                else:
                    inventory[prefix] = inventory.get(prefix, 0) + 1
        for prefix, value in inventory.items():
            if isinstance(value, list):
                value.sort()
        return inventory


SMOKE_AUTHOR = b"Smoke <smoke@mistwarp.org> 1700000000 +0000"
ZERO_HEAD = "0" * 40
EMBEDDED_HEX = re.compile(r"[0-9a-f]{32,64}")
CONTENT_NAME = re.compile(r"^(assets/)?[0-9a-f]{32}\.[a-z0-9]+$|^project-json/[0-9a-f]{64}\.json\.gz$")
ACCESS_KEY = re.compile(r"([?&]k=)[A-Za-z0-9]+")
EMBEDDED_TIME = re.compile(r"(?<![0-9])1[0-9]{12}(?![0-9])")


def sb3_archive(project_json, files=None):
    buffer = io.BytesIO()
    with zipfile.ZipFile(buffer, "w") as archive:
        if project_json is not None:
            archive.writestr("project.json", json.dumps(project_json, ensure_ascii=False))
        for name, content in (files or {}).items():
            archive.writestr(name, content)
    return buffer.getvalue()


def zip_files(files):
    buffer = io.BytesIO()
    with zipfile.ZipFile(buffer, "w") as archive:
        for name, content in sorted(files.items()):
            archive.writestr(name, content)
    return buffer.getvalue()


def svg_asset(label):
    content = ("<svg xmlns=\"http://www.w3.org/2000/svg\"><text>%s</text></svg>" % label).encode()
    return hashlib.md5(content).hexdigest() + ".svg", content


def project_with_costumes(names, extension_urls=None, targets=True):
    project = json.loads(json.dumps(PROJECT_JSON))
    if not targets:
        project.pop("targets")
        return project
    project["targets"][0]["costumes"] = [
        {"name": "c%d" % index, "assetId": name.split(".")[0], "md5ext": name, "dataFormat": "svg", "rotationCenterX": 0, "rotationCenterY": 0}
        for index, name in enumerate(names)
    ]
    if extension_urls:
        project["extensionURLs"] = extension_urls
        project["extensions"] = list(extension_urls)
    return project


def git_object(kind, body):
    raw = b"%s %d\x00" % (kind.encode(), len(body)) + body
    return hashlib.sha1(raw).hexdigest(), zlib.compress(raw)


class History:
    def __init__(self, data):
        with zipfile.ZipFile(io.BytesIO(data)) as archive:
            self.entries = {name: archive.read(name) for name in archive.namelist() if not name.endswith("/")}
        self.branch = self.entries[".git/HEAD"].decode().strip()[len("ref: refs/heads/"):]
        self.refs = {name[len(".git/refs/heads/"):]: value.decode().strip() for name, value in self.entries.items() if name.startswith(".git/refs/heads/")}
        self.objects = {name[len(".git/objects/"):].replace("/", ""): value for name, value in self.entries.items() if name.startswith(".git/objects/")}
        self.added = {}

    def head(self):
        return self.refs[self.branch]

    def read(self, oid):
        raw = zlib.decompress(self.objects.get(oid) or self.added[oid])
        header, body = raw.split(b"\x00", 1)
        return header.split(b" ")[0].decode(), body

    def tree_of(self, commit):
        return self.read(commit)[1].split(b"\n", 1)[0].decode()[len("tree "):]

    def entries_of(self, tree):
        body = self.read(tree)[1]
        entries, position = [], 0
        while position < len(body):
            space = body.index(b" ", position)
            nul = body.index(b"\x00", space)
            entries.append((body[position:space], body[space + 1:nul], body[nul + 1:nul + 21]))
            position = nul + 21
        return entries

    def files(self, tree, prefix=""):
        found = {}
        for mode, name, raw_oid in self.entries_of(tree):
            path = prefix + name.decode()
            if mode in (b"40000", b"040000"):
                found.update(self.files(raw_oid.hex(), path + "/"))
            else:
                found[path] = self.read(raw_oid.hex())[1]
        return found

    def add(self, kind, body):
        oid, compressed = git_object(kind, body)
        if oid not in self.objects:
            self.added[oid] = compressed
        return oid

    def tree_with(self, tree, name, content):
        blob = self.add("blob", content)
        entries = [entry for entry in self.entries_of(tree) if entry[1] != name.encode()]
        entries.append((b"100644", name.encode(), bytes.fromhex(blob)))
        entries.sort(key=lambda entry: entry[1] + (b"/" if entry[0] in (b"40000", b"040000") else b""))
        return self.add("tree", b"".join(mode + b" " + entry_name + b"\x00" + raw_oid for mode, entry_name, raw_oid in entries))

    def commit(self, tree, parents, message):
        lines = [b"tree " + tree.encode()] + [b"parent " + parent.encode() for parent in parents]
        lines += [b"author " + SMOKE_AUTHOR, b"committer " + SMOKE_AUTHOR]
        return self.add("commit", b"\n".join(lines) + b"\n\n" + message.encode() + b"\n")

    def archive(self, head, delta):
        refs = dict(self.refs) if not delta else {}
        refs[self.branch] = head
        manifest = self.metadata(head, delta)
        manifest.update({"format": "mistwarp-project", "version": 1, "commits": [], "graph": {}})
        buffer = io.BytesIO()
        with zipfile.ZipFile(buffer, "w") as archive:
            archive.writestr("mwp.json", json.dumps(manifest))
            archive.writestr(".git/HEAD", "ref: refs/heads/%s\n" % self.branch)
            for branch, oid in sorted(refs.items()):
                archive.writestr(".git/refs/heads/" + branch, oid + "\n")
            objects = dict(self.added) if delta else dict(self.objects, **self.added)
            for oid, compressed in sorted(objects.items()):
                archive.writestr(".git/objects/%s/%s" % (oid[:2], oid[2:]), compressed)
        return buffer.getvalue()

    def metadata(self, head, delta):
        return {"head": head, "branch": self.branch, "delta": delta, "baseHead": self.head() if delta else None, "worktree": False}


def raw_get(server, path, who):
    request = urllib.request.Request(server.base + path, method="GET")
    request.add_header("Authorization", "Bearer tok_" + who)
    with urllib.request.urlopen(request, timeout=30) as response:
        data = response.read()
    return gzip.decompress(data) if data[:2] == b"\x1f\x8b" else data


def real_id(ids, alias):
    return next(real for real, known in ids.items() if known == alias)


def committed_workspace(server, ids, alias, who, message, delta=False, same_tree=False):
    history = History(raw_get(server, "/v1/projects/%s/workspace.mwp" % real_id(ids, alias), who))
    tree = history.tree_of(history.head())
    if not same_tree:
        tree = history.tree_with(tree, "smoke-notes.txt", message.encode())
    head = history.commit(tree, [history.head()], message)
    return history.archive(head, delta), json.dumps(history.metadata(head, delta))


def staged_part(server, step, path, who, kind, data, name):
    status, started = step(name + " start", "POST", path + "/upload-parts", who, {"kind": kind, "name": "", "size": len(data), "chunks": 1})
    token = started.get("token", "") if isinstance(started, dict) else ""
    status, chunk = step(name + " chunk", "POST", path + "/upload-parts/%s/chunk" % token, who, form=({"index": "0"}, {"chunk": data}))
    for field in ("received", "size"):
        if isinstance(chunk, dict) and chunk.get(field) == len(data):
            chunk[field] = "<bytes sent>"
    return token


def upload_flow(server, step, ids):
    status, created = step("history create", "POST", "/v1/projects", "sam", {"title": "History smoke"})
    ids[str(created.get("id", ""))] = "<history>"
    path = "/v1/projects/<history>"
    asset_name, asset_content = svg_asset("first")
    other_name, other_content = svg_asset("second")
    plain = sb3_archive(project_with_costumes([]))

    def upload(name, fields=None, files=None, who="sam"):
        return step(name, "POST", path + "/upload", who, form=(fields or {}, files if files is not None else {"project": plain}))

    def head_and_edited():
        status, project = server.call("GET", "/v1/projects/" + real_id(ids, "<history>"), "sam")[:2]
        return str(project.get("gitHead") or (project.get("project") or {}).get("gitHead") or ""), str(project.get("edited") or (project.get("project") or {}).get("edited") or "")

    upload("upload refused for stranger", who="alice")
    upload("upload missing project file", files={})
    upload("upload invalid sb3", files={"project": b"not a zip"})
    upload("upload without targets", files={"project": sb3_archive(project_with_costumes([], targets=False))})
    upload("upload missing asset", files={"project": sb3_archive(project_with_costumes([asset_name]))})
    upload("upload asset hash mismatch", files={"project": sb3_archive(project_with_costumes([asset_name]), {asset_name: other_content})})
    upload("upload invalid extension sources", {"extensions": "not json"})
    upload("upload unsavable extension url", files={"project": sb3_archive(project_with_costumes([], {"bad": "ftp://example.com/x.js"}))})
    upload("upload unread extension source", files={"project": sb3_archive(project_with_costumes([], {"ext": "https://example.com/ext.js"}))})
    with_asset = sb3_archive(project_with_costumes([asset_name], {"ext": "https://example.com/ext.js"}), {asset_name: asset_content})
    upload("upload with asset and extension", {"extensions": json.dumps({"https://example.com/ext.js": "class SmokeExtension {}"})},
           {"project": with_asset, "thumbnail": b"\x89PNG smoke thumbnail"})
    step("check assets", "POST", path + "/assets/check", "sam", {"assets": [asset_name, other_name]})
    step("check invalid asset name", "POST", path + "/assets/check", "sam", {"assets": ["nope"]})
    sparse = sb3_archive(project_with_costumes([asset_name], {"ext": "https://example.com/ext.js"}))
    upload("upload sparse", {"extensions": json.dumps({"https://example.com/ext.js": "class SmokeExtension {}"})}, {"project": sparse})
    upload("upload second asset", files={"project": sb3_archive(project_with_costumes([asset_name, other_name]), {other_name: other_content})})
    wide_name, wide_content = svg_asset("\u00fcn\u00efc\u00f6d\u00e9 \u2713 " * 400)
    wide_project = project_with_costumes([asset_name, other_name, wide_name])
    wide_project["targets"][0]["variables"] = {"wide": ["\u00e9\u2713", "\u00e9\u2713\U0001F600" * 3000]}
    upload("upload non-ascii project", files={"project": sb3_archive(wide_project, {wide_name: wide_content})})

    head, edited = head_and_edited()
    upload("upload stale edited", {"expectedEdited": "1"})
    upload("upload stale head", {"expectedHead": ZERO_HEAD})
    upload("upload replace without head", {"replaceHistory": "true"})
    upload("upload replace without workspace", {"replaceHistory": "true", "expectedHead": head})
    upload("upload invalid git metadata", {"git": "not json"})
    upload("upload stale delta base", {"git": json.dumps({"delta": True, "baseHead": ZERO_HEAD})})
    upload("upload invalid restore commit", {"restoreCommit": "xyz"})
    upload("upload invalid workspace token", {"workspaceToken": "!!"})
    upload("upload expired workspace token", {"workspaceToken": "abc123"})
    upload("upload invalid merge tree token", {"mergeTreeToken": "!!"})
    upload("upload unsafe workspace", files={"project": plain, "workspace": b"not a zip"})
    upload("upload replace as editor session", {"replaceHistory": "true", "expectedHead": head}, who="sam_editor")

    unchanged = History(raw_get(server, "/v1/projects/%s/workspace.mwp" % real_id(ids, "<history>"), "sam"))
    upload("upload unchanged workspace", {"git": json.dumps(unchanged.metadata(unchanged.head(), False))},
           {"project": plain, "workspace": unchanged.archive(unchanged.head(), False)})
    archive, metadata = committed_workspace(server, ids, "<history>", "sam", "Same tree", same_tree=True)
    upload("upload same tree commit", {"git": metadata}, {"project": plain, "workspace": archive})
    archive, metadata = committed_workspace(server, ids, "<history>", "sam", "Full save")
    upload("upload full workspace", {"git": metadata}, {"project": plain, "workspace": archive})
    archive, metadata = committed_workspace(server, ids, "<history>", "sam", "Delta save", delta=True)
    upload("upload delta workspace", {"git": metadata}, {"project": plain, "workspace": archive})
    archive, metadata = committed_workspace(server, ids, "<history>", "sam", "Staged save", delta=True)
    token = staged_part(server, step, path, "sam", "workspace", archive, "stage workspace")
    upload("upload staged workspace", {"git": metadata, "workspaceToken": token})
    upload("upload reused workspace token", {"git": metadata, "workspaceToken": token})

    first = History(raw_get(server, "/v1/projects/%s/workspace.mwp" % real_id(ids, "<history>"), "sam"))
    oldest = first.head()
    while True:
        parents = [line[7:].decode() for line in first.read(oldest)[1].split(b"\n\n", 1)[0].split(b"\n") if line.startswith(b"parent ")]
        if not parents:
            break
        oldest = parents[0]
    upload("upload restore", {"restoreCommit": oldest, "restoreMessage": "Restore the first version"})
    upload("upload restore unknown commit", {"restoreCommit": "1" * 40})

    step("create branch", "POST", path + "/branches", "sam", {"name": "feature"})
    status, listed = step("list branches", "GET", path + "/branches", "sam")
    heads = {branch.get("name"): branch.get("head") for branch in (listed.get("branches") or [])} if isinstance(listed, dict) else {}
    merged = History(raw_get(server, "/v1/projects/%s/workspace.mwp" % real_id(ids, "<history>"), "sam"))
    tree_files = merged.files(merged.tree_of(merged.head()))
    tree_files["smoke-merge.txt"] = b"merged"
    merge_tree = zip_files(tree_files)
    branch_fields = {"mergeSourceBranch": "feature", "mergeTargetBranch": "main", "mergeSourceHead": heads.get("feature", ""), "mergeTargetHead": heads.get("main", "")}
    upload("upload branch merge stale", dict(branch_fields, mergeSourceHead=ZERO_HEAD), {"project": plain, "mergeTree": merge_tree})
    upload("upload branch merge same branch", dict(branch_fields, mergeSourceBranch="main", mergeSourceHead=heads.get("main", "")), {"project": plain, "mergeTree": merge_tree})
    upload("upload branch merge invalid tree", branch_fields, {"project": plain, "mergeTree": b"not a zip"})
    upload("upload branch merge", branch_fields, {"project": plain, "mergeTree": merge_tree})
    upload("upload pull not open", {"pullId": "99"}, {"project": plain, "mergeTree": merge_tree})
    upload("upload merge tree without target", {}, {"project": plain, "mergeTree": merge_tree})

    step("share team", "PUT", path, "sam", {"collaborators": [{"username": "alice", "role": "editor"}]})
    upload("upload live editor without edited", who="alice")
    upload("upload live editor history refused", {"expectedEdited": "1", "restoreCommit": oldest}, who="alice")
    head, edited = head_and_edited()
    upload("upload live editor", {"expectedEdited": edited}, who="alice")

    step("share history", "POST", path + "/visibility", "sam", {"visibility": "public"})
    status, remixed = step("remix history", "POST", path + "/remix", "alice", {})
    remix_id = str(remixed.get("id", "") or (remixed.get("project") or {}).get("id", "")) if isinstance(remixed, dict) else ""
    ids[remix_id] = "<history-remix>"
    archive, metadata = committed_workspace(server, ids, "<history-remix>", "alice", "Remix change")
    step("upload remix change", "POST", "/v1/projects/<history-remix>/upload", "alice", form=({"git": metadata}, {"project": plain, "workspace": archive}))
    status, pull = step("open pull", "POST", path + "/pulls", "alice", {"sourceProjectId": remix_id, "title": "Smoke pull", "body": "From the remix"})
    pull_index = str(((pull.get("pull") or {}).get("index", "")) if isinstance(pull, dict) else "")
    remix = History(raw_get(server, "/v1/projects/%s/workspace.mwp" % remix_id, "alice"))
    pull_tree = zip_files(remix.files(remix.tree_of(remix.head())))
    head, edited = head_and_edited()
    upload("upload pull merge stale head", {"pullId": pull_index, "expectedHead": ZERO_HEAD}, {"project": plain, "mergeTree": pull_tree})
    tree_token = staged_part(server, step, path, "sam", "mergeTree", pull_tree, "stage merge tree")
    upload("upload pull merge with unfinished tree", {"pullId": pull_index, "expectedHead": head, "mergeTreeToken": tree_token})
    upload("upload pull merge", {"pullId": pull_index, "expectedHead": head}, {"project": plain, "mergeTree": pull_tree})
    step("merged pull", "GET", path + "/pulls/" + pull_index, "sam")
    upload("upload merged pull again", {"pullId": pull_index}, {"project": plain, "mergeTree": pull_tree})

    head, edited = head_and_edited()
    fresh = History(raw_get(server, "/v1/projects/%s/workspace.mwp" % real_id(ids, "<history>"), "sam"))
    root_tree = fresh.add("tree", b"100644 replaced.txt\x00" + bytes.fromhex(fresh.add("blob", b"replaced")))
    root = fresh.commit(root_tree, [], "Replaced history")
    fresh.objects = {}
    fresh.refs = {fresh.branch: root}
    upload("upload replace history", {"replaceHistory": "true", "expectedHead": head, "git": json.dumps(fresh.metadata(root, False))},
           {"project": plain, "workspace": fresh.archive(root, False)})
    step("history after replace", "GET", path + "/branches", "sam")
    step("history quota", "GET", "/v1/me/quota", "sam")
    step("get history project", "GET", path, "sam")


def update_flow(server, step, ids):
    status, created = step("update create", "POST", "/v1/projects", "sam", {"title": "Update smoke"})
    ids[str(created.get("id", ""))] = "<update>"
    path = "/v1/projects/<update>"

    def patch(name, body, who="sam"):
        return step(name, "PUT", path, who, body)

    patch("patch anonymous", {"title": "x"}, who=None)
    patch("patch by stranger", {"title": "x"}, who="alice")
    patch("patch text", {"title": "  Updated smoke  ", "description": "About", "instructions": "Press space", "notes": "Thanks"})
    patch("patch empty title", {"title": "   "})
    patch("patch long title", {"title": "t" * 101})
    patch("patch long description", {"description": "d" * 5001})
    patch("patch long instructions", {"instructions": "i" * 5001})
    patch("patch long notes", {"notes": "n" * 5001})
    patch("patch non-ascii description", {"description": "\u00e9\u2713" * 2400})
    patch("patch long non-ascii notes", {"notes": "\u00e9" * 5001})
    patch("patch credits", {"credits": [
        {"who": "Ada", "role": "Art", "url": "https://example.com/ada"},
        {"who": "Bo", "role": "Music", "url": None},
        {"who": "", "role": "skipped"},
        {"who": "w" * 61, "role": "skipped"},
    ]})
    patch("patch credit bad url", {"credits": [{"who": "Ada", "role": "Art", "url": "ftp://example.com"}]})
    patch("patch credit long url", {"credits": [{"who": "Ada", "role": "Art", "url": "https://example.com/" + "u" * 500}]})
    patch("patch too many credits", {"credits": [{"who": "A", "role": "B"}] * 51})
    patch("patch flags", {"commentsOff": True, "remixable": False, "seeInside": True})
    patch("patch price", {"price": 5})
    patch("patch zero price", {"price": 0, "remixable": True})
    patch("patch remix share", {"remixShare": 20})
    patch("patch remix share too high", {"remixShare": 80})
    patch("patch tags", {"tags": ["Games", "games", "Puzzle", "a", "b", "c", "d", "e", "f", "g", "h", "i", "j"]})
    items = [
        {"id": "Sword", "name": "Sword", "visual": {"type": "image", "url": "https://example.com/s.png"}},
        {"id": "gem", "name": "Gem", "image": "https://example.com/g.png"},
        {"id": "hat", "name": "Hat", "visual": {"type": "costume", "assetId": "abcdefgh12", "dataFormat": "PNG"}, "gameAwardable": False},
        {"id": "pixel", "name": "Pixel", "visual": {"type": "image", "url": "data:image/png;base64,AAAA"}},
    ]
    product = {"id": "pack", "name": "Pack", "description": "Starter", "price": 10, "grantsItem": "sword", "icon": "https://example.com/i.png"}
    patch("patch game items and products", {"gameItems": items, "gameProducts": [product]})
    patch("patch product bad id", {"gameProducts": [dict(product, id="Bad Id")]})
    patch("patch product duplicate id", {"gameProducts": [product, product]})
    patch("patch product bad name", {"gameProducts": [dict(product, name="")]})
    patch("patch product bad price", {"gameProducts": [dict(product, price=2000)]})
    patch("patch product bad icon", {"gameProducts": [dict(product, icon="ftp://example.com/i.png")]})
    patch("patch product big icon", {"gameProducts": [dict(product, icon="data:image/png;base64," + "A" * 100001)]})
    patch("patch product bad grant", {"gameProducts": [dict(product, grantsItem="BAD!")]})
    patch("patch product undefined grant", {"gameProducts": [dict(product, grantsItem="ghost")]})
    patch("patch too many products", {"gameProducts": [product] * 51})
    patch("patch item bad id", {"gameItems": [dict(items[0], id="Bad Id")]})
    patch("patch item bad name", {"gameItems": [dict(items[0], name="")]})
    patch("patch item http image", {"gameItems": [dict(items[0], visual={"type": "image", "url": "http://example.com/s.png"})]})
    patch("patch item big data image", {"gameItems": [dict(items[0], visual={"type": "image", "url": "data:image/png;base64," + "A" * 100001})]})
    patch("patch item bad costume", {"gameItems": [dict(items[0], visual={"type": "costume", "assetId": "short", "dataFormat": "png"})]})
    patch("patch item without visual", {"gameItems": [{"id": "plain", "name": "Plain"}]})
    patch("patch too many items", {"gameItems": [items[1]] * 101})
    patch("patch items dropping a granted item", {"gameItems": [items[1]]})
    patch("patch inventory bad mode", {"inventoryPolicy": {"mode": "some"}})
    patch("patch inventory too large", {"inventoryPolicy": {"mode": "all", "allowedItems": ["p:x"] * 101}})
    patch("patch inventory bad item", {"inventoryPolicy": {"mode": "allowlist", "allowedItems": ["nope"]}})
    patch("patch inventory bad project", {"inventoryPolicy": {"mode": "allowlist", "allowedProjects": ["../x"]}})
    patch("patch inventory", {"inventoryPolicy": {"mode": "Allowlist", "allowedItems": ["p1:sword", "p1:sword"], "allowedProjects": ["p1", "p1"]}})
    patch("patch compatibility", {"compatibility": {"mobile": True, "keyboard": False, "controller": "yes"}})
    patch("patch branding bad accent", {"branding": {"accentColor": "red"}})
    patch("patch branding long tagline", {"branding": {"tagline": "t" * 121}})
    patch("patch branding without plan", {"branding": {"accentColor": "#123456", "tagline": "Hi"}})
    patch("patch branding cleared", {"branding": {}})
    patch("patch vanity without plan", {"vanitySlug": "smoke-game"})
    patch("patch vanity cleared", {"vanitySlug": ""})
    patch("patch group tag invalid", {"groupTag": "!!!"})
    patch("patch group tag not managed", {"groupTag": "somegroup"})
    patch("patch group tag cleared", {"groupTag": "  "})
    patch("patch team share too high", {"collaborators": [{"username": "alice", "role": "editor", "share": 60}]})
    patch("patch team unknown user", {"collaborators": [{"username": "ghost_user", "role": "editor"}]})
    patch("patch team duplicate", {"collaborators": [{"username": "alice", "role": "editor"}, {"username": "Alice", "role": "tester"}]})
    patch("patch team total share", {"collaborators": [{"username": "alice", "role": "editor", "share": 40}, {"username": "bob", "role": "tester", "share": 40}]})
    patch("patch team", {"collaborators": [
        {"username": "alice", "role": "Maintainer", "share": 10},
        {"username": "bob", "role": "tester"},
        {"username": "sam", "role": "editor"},
        {"username": "carol", "role": "owner"},
    ]})
    patch("patch team same roles", {"collaborators": [{"username": "alice", "role": "maintainer", "share": 10}, {"username": "bob", "role": "editor"}]})
    step("alice notifications", "GET", "/v1/notifications", "alice")
    step("bob notifications", "GET", "/v1/notifications", "bob")
    patch("patch remix share as maintainer", {"remixShare": 5}, who="alice")
    patch("patch team as maintainer", {"collaborators": []}, who="alice")
    patch("patch title as maintainer", {"title": "Maintained"}, who="alice")
    patch("patch editor session allowed", {"title": "From the editor"}, who="sam_editor")
    patch("patch editor session refused", {"collaborators": []}, who="sam_editor")
    patch("patch transfer as maintainer", {"owner": "bob"}, who="alice")
    patch("patch transfer unknown", {"owner": "ghost_user"})
    patch("patch transfer", {"owner": "bob", "keepGroup": True, "title": "Given away"})
    patch("patch after transfer", {"title": "Mine again"})
    step("get updated project", "GET", path, "bob")


def volatile_key(key):
    return key in VOLATILE_NAMES


def normalize(value, ids, key="", parent=""):
    if isinstance(value, dict):
        return {k: normalize(v, ids, k, key) for k, v in sorted(value.items())}
    if isinstance(value, list):
        return [normalize(v, ids, key, parent) for v in value]
    if (parent, key) in VOLATILE_FIELDS:
        return "<volatile>"
    if isinstance(value, str):
        value = re.sub(r"127\.0\.0\.1:\d+", "127.0.0.1:<port>", value)
        for real, alias in ids.items():
            value = value.replace(real, alias)
        if volatile_key(key) or HEX.match(value) or ISO.match(value):
            return "<volatile>"
        value = ACCESS_KEY.sub(r"\1<key>", value)
        if CONTENT_NAME.match(value):
            return value
        return EMBEDDED_TIME.sub("<time>", EMBEDDED_HEX.sub("<hex>", value))
    if isinstance(value, (int, float)) and not isinstance(value, bool):
        if volatile_key(key) or value >= 1e12:
            return "<volatile>"
    return value


def run(binary, sb3, keep):
    server = Server(binary, keep)
    ids = {}
    steps = []

    def step(name, method, path, who=None, body=None, upload=None, headers=(), form=None):
        real_path = path
        for alias, real in ((a, r) for r, a in ids.items()):
            real_path = real_path.replace(alias, real)
        if upload:
            real_path = "/v1/projects/%s/upload" % upload
            status, parsed, response_headers = server.upload(upload, who, sb3)
        elif form is not None:
            status, parsed, response_headers = server.form(real_path, who, form[0], form[1])
        else:
            status, parsed, response_headers = server.call(method, real_path, who, body)
        record = {"step": name, "status": status, "body": parsed}
        for header in headers:
            record[header] = response_headers.get(header, "")
        steps.append(record)
        saved = SAVE_PATH.match(real_path)
        if saved and 200 <= status < 300 and (method, bool(saved.group(2))) in (("PUT", False), ("POST", True)):
            steps.append({"step": name + " stored", "status": 0, "body": server.read_project(saved.group(1))})
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
        step("delete shared project refused", "DELETE", "/v1/projects/<project>", "sam")
        step("unshare", "POST", "/v1/projects/<project>/unpublish", "sam", {})
        step("delete project", "DELETE", "/v1/projects/<project>", "sam")
        step("trash sam", "GET", "/v1/me/trash", "sam")
        step("missing project", "GET", "/v1/projects/nope")
        step("wrong method", "PATCH", "/v1/projects/<project>", "sam", {}, headers=("Allow",))
        step("legacy prefix", "GET", "/api/agreement")
        step("health", "GET", "/v1/health")
        upload_flow(server, step, ids)
        update_flow(server, step, ids)
        time.sleep(1.5)
        for real, alias in list(ids.items()):
            if alias in ("<history>", "<history-remix>", "<update>"):
                steps.append({"step": "stored " + alias, "status": 0, "body": server.read_project(real)})
        steps.append({"step": "blob inventory", "status": 0, "body": server.blob_inventory()})
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
