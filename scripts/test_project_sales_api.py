import io
import json
import os
import shutil
import socket
import subprocess
import tempfile
import time
import unittest
import urllib.error
import urllib.request
import uuid
import zipfile

BINARY = os.environ.get("MWAPI_BIN", "")

PROJECT_JSON = {
    "targets": [{
        "isStage": True, "name": "Stage", "variables": {}, "lists": {}, "broadcasts": {}, "blocks": {},
        "comments": {}, "currentCostume": 0, "costumes": [], "sounds": [], "volume": 100, "layerOrder": 0,
        "tempo": 60, "videoTransparency": 50, "videoState": "on", "textToSpeechLanguage": None,
    }],
    "monitors": [], "extensions": [], "meta": {"semver": "3.0.0", "vm": "0.2.0", "agent": "test"},
}


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def sb3():
    buffer = io.BytesIO()
    with zipfile.ZipFile(buffer, "w") as archive:
        archive.writestr("project.json", json.dumps(PROJECT_JSON))
    return buffer.getvalue()


@unittest.skipUnless(BINARY, "set MWAPI_BIN to a compiled mistwarp-api binary")
class ProjectSalesApiTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.dir = tempfile.mkdtemp(prefix="mw-sales-api-")
        cls.port = free_port()
        cls.base = "http://127.0.0.1:%d/v1" % cls.port
        data = os.path.join(cls.dir, "data")
        os.makedirs(os.path.join(data, "sessions"))
        with open(os.path.join(cls.dir, ".env"), "w") as env:
            env.write("PORT=%d\nAPP_URL=http://127.0.0.1:%d\nROTUR_BASE=http://127.0.0.1:9\n" % (cls.port, cls.port))
        users = {"sam": "u_sam", "alice": "u_alice", "bob": "u_bob"}
        with open(os.path.join(data, "userids.json"), "w") as handle:
            json.dump({"users": {uid: name for name, uid in users.items()}}, handle)
        for name, uid in users.items():
            with open(os.path.join(data, "sessions", "tok_%s.json" % name), "w") as handle:
                json.dump({"username": name, "userId": uid, "createdAt": int(time.time() * 1000)}, handle)
        cls.log = open(os.path.join(cls.dir, "server.log"), "w")
        cls.server = subprocess.Popen([os.path.abspath(BINARY)], cwd=cls.dir, stdout=cls.log, stderr=subprocess.STDOUT)
        for _ in range(150):
            try:
                urllib.request.urlopen("http://127.0.0.1:%d/" % cls.port, timeout=1)
                break
            except urllib.error.HTTPError:
                break
            except OSError:
                time.sleep(0.2)
        try:
            cls.project = cls.make_paid_project()
        except Exception:
            cls.tearDownClass()
            raise

    @classmethod
    def tearDownClass(cls):
        cls.server.terminate()
        cls.server.wait(10)
        cls.log.close()
        shutil.rmtree(cls.dir)

    @classmethod
    def call(cls, method, path, who=None, body=None, raw=None, content_type="application/json"):
        data = raw if raw is not None else (None if body is None else json.dumps(body).encode("utf-8"))
        url = path if path.startswith("http") else cls.base + path
        request = urllib.request.Request(url, data=data, method=method)
        if who:
            request.add_header("Authorization", "Bearer tok_" + who)
        if data is not None:
            request.add_header("Content-Type", content_type)
        try:
            with urllib.request.urlopen(request, timeout=20) as response:
                status, text = response.status, response.read()
        except urllib.error.HTTPError as error:
            status, text = error.code, error.read()
        try:
            return status, json.loads(text or b"{}")
        except ValueError:
            return status, {"raw": text[:200]}

    @classmethod
    def upload(cls, project_id, who):
        boundary = uuid.uuid4().hex
        body = (
            ("--%s\r\nContent-Disposition: form-data; name=\"project\"; filename=\"p.sb3\"\r\n"
             "Content-Type: application/octet-stream\r\n\r\n" % boundary).encode()
            + sb3() + ("\r\n--%s--\r\n" % boundary).encode()
        )
        return cls.call("POST", "/projects/%s/upload" % project_id, who, raw=body,
                        content_type="multipart/form-data; boundary=" + boundary)

    @classmethod
    def project_path(cls, project_id):
        return os.path.join(cls.dir, "data", "projects", project_id + ".json")

    @classmethod
    def edit_project(cls, project_id, change):
        with open(cls.project_path(project_id)) as handle:
            project = json.load(handle)
        change(project)
        with open(cls.project_path(project_id), "w") as handle:
            json.dump(project, handle)

    @classmethod
    def make_paid_project(cls):
        status, created = cls.call("POST", "/projects", "sam", {"title": "Paid maze"})
        assert status in (200, 201), (status, created)
        project_id = created["id"]
        status, uploaded = cls.upload(project_id, "sam")
        assert status == 200, (status, uploaded)
        status, shared = cls.call("POST", "/projects/%s/visibility" % project_id, "sam", {"visibility": "public"})
        assert status == 200, (status, shared)

        def paid(project):
            project["price"] = 5
            project["revenue"] = 4.5
            project["buyers"] = [{"user": "alice", "at": int(time.time() * 1000) - 60000, "amount": 5}]
            project["saleHistory"] = {str(int(time.time() // 86400)): 4.5}
        cls.edit_project(project_id, paid)
        profile = os.path.join(cls.dir, "data", "users", "alice.json")
        os.makedirs(os.path.dirname(profile), exist_ok=True)
        with open(profile, "w") as handle:
            json.dump({"username": "alice", "userId": "u_alice", "followers": [], "following": [],
                       "purchases": [{"projectId": project_id, "title": "Paid maze", "seller": "sam", "amount": 5,
                                      "at": 1760000000000}]}, handle)
        return project_id

    def test_a_paid_project_is_free_for_everyone(self):
        for who in (None, "bob", "alice"):
            status, data = self.call("GET", "/projects/" + self.project, who)
            self.assertEqual(status, 200, data)
            project = data["project"]
            for field in ("price", "locked", "bought"):
                self.assertNotIn(field, project)
            self.assertTrue(project["projectJsonUrl"], who)
            self.assertTrue(project["canSeeInside"], who)
            self.assertFalse(project["contributionOnly"], who)
            status, _ = self.call("GET", project["projectJsonUrl"])
            self.assertEqual(status, 200, who)

    def test_setting_a_price_is_refused(self):
        status, data = self.call("PUT", "/projects/" + self.project, "sam", {"price": 10})
        self.assertEqual(status, 400)
        self.assertEqual(data.get("code"), "project_sales_ended")
        status, data = self.call("PUT", "/projects/" + self.project, "sam", {"price": 0, "title": "Free maze"})
        self.assertEqual(status, 200, data)
        self.assertEqual(data["project"]["title"], "Free maze")

    def test_buying_is_refused(self):
        for path in ("/purchase/intent", "/purchase/confirm"):
            for who in (None, "bob"):
                status, data = self.call("POST", "/projects/" + self.project + path, who, {"returnUrl": "http://x/", "key": "mwbuy_x"})
                self.assertEqual(status, 410, (path, who, data))
                self.assertEqual(data.get("code"), "project_sales_ended")

    def test_old_purchases_stay_readable(self):
        status, data = self.call("GET", "/me/purchases", "alice")
        self.assertEqual(status, 200, data)
        self.assertEqual([p["projectId"] for p in data["purchases"]], [self.project])
        self.assertEqual(data["purchases"][0]["amount"], 5)
        status, data = self.call("GET", "/projects/" + self.project, "sam")
        self.assertEqual(data["project"]["analytics"]["buyers"][0]["user"], "alice")
        self.assertEqual(data["project"]["analytics"]["revenue"], 4.5)
        status, data = self.call("GET", "/me/stats", "sam")
        self.assertEqual(status, 200, data)
        self.assertEqual(data["stats"]["totalBuyers"], 1)

    def test_anyone_can_react_and_comment(self):
        status, data = self.call("POST", "/projects/%s/react" % self.project, "bob", {"type": "heart"})
        self.assertEqual(status, 200, data)
        status, data = self.call("POST", "/projects/%s/comments" % self.project, "bob", {"content": "Nice maze"})
        self.assertIn(status, (200, 201), data)

    def test_remixes_can_be_shared(self):
        status, data = self.call("POST", "/projects", "bob", {"title": "My maze", "remixParent": self.project})
        self.assertIn(status, (200, 201), data)
        remix = data["id"]
        status, data = self.call("GET", "/projects/" + remix, "bob")
        self.assertFalse(data["project"]["contributionOnly"])
        self.assertEqual(self.upload(remix, "bob")[0], 200)
        self.edit_project(remix, lambda project: project.update({"contributionOnly": True}))
        status, data = self.call("POST", "/projects/%s/visibility" % remix, "bob", {"visibility": "public"})
        self.assertEqual(status, 200, data)
        self.assertEqual(data["project"]["visibility"], "public")

    def test_perks_no_longer_list_a_project_price(self):
        status, data = self.call("GET", "/perks")
        self.assertEqual(status, 200, data)
        text = json.dumps(data)
        self.assertNotIn("maxProjectPrice", text)


if __name__ == "__main__":
    unittest.main()
