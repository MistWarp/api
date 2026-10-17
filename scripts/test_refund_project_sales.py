import io
import json
import os
import shutil
import sys
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, HTTPServer

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import refund_project_sales as refunds

NOW = 1790000000000.0


class FakeRotur(object):
    def __init__(self):
        self.transfers = []
        self.me_calls = 0
        self.username = "mist"
        self.balance = 1000
        self.fail_for = {}
        self.server = HTTPServer(("127.0.0.1", 0), self.handler())
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.base = "http://127.0.0.1:%d" % self.server.server_address[1]

    def stop(self):
        self.server.shutdown()
        self.server.server_close()

    def handler(self):
        fake = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def reply(self, status, body):
                data = json.dumps(body).encode("utf-8")
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

            def do_GET(self):
                if self.path != "/v2/me" or self.headers.get("Authorization") != "Bearer tok":
                    return self.reply(401, {"error": "bad token"})
                fake.me_calls += 1
                self.reply(200, {"username": fake.username, "sys.currency": fake.balance})

            def do_POST(self):
                if self.path != "/v2/me/transfers" or self.headers.get("Authorization") != "Bearer tok":
                    return self.reply(401, {"error": "bad token"})
                body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                status = fake.fail_for.get(body["to"])
                if status:
                    return self.reply(status, {"error": "recipient user not found"})
                fake.transfers.append(body)
                self.reply(200, {"message": "Transfer successful"})

        return Handler


class Args(object):
    def __init__(self, data, **values):
        self.data = data
        self.ledger = None
        self.apply = False
        self.payer = "mist"
        self.rotur_base = ""
        self.note = refunds.NOTE
        self.delay = 0
        self.resolve = None
        self.now = NOW
        for key, value in values.items():
            setattr(self, key, value)


def write(path, data):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w") as handle:
        json.dump(data, handle)


class RefundTests(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.mkdtemp(prefix="mw-refund-test-")
        self.data = os.path.join(self.dir, "data")
        write(os.path.join(self.data, "users", "alice.json"), {"username": "Alice", "purchases": [
            {"projectId": "p1", "title": "Maze", "seller": "sam", "amount": 2, "at": 1},
            {"projectId": "p2", "title": "Gone", "seller": "sam", "amount": 100, "at": 2},
            {"projectId": "p3", "productId": "gem", "title": "Gem", "amount": 5, "at": 3, "type": "game_product"},
        ]})
        write(os.path.join(self.data, "users", "bob.json"), {"username": "bob", "purchases": [
            {"projectId": "p1", "title": "Maze", "seller": "sam", "amount": 2, "at": 4},
        ]})
        write(os.path.join(self.data, "users", "mist.json"), {"username": "mist", "purchases": [
            {"projectId": "p1", "title": "Maze", "seller": "sam", "amount": 2, "at": 5},
        ]})
        write(os.path.join(self.data, "users", "sam.json"), {"username": "sam"})
        write(os.path.join(self.data, "projects", "p1.json"), {"id": "p1", "owner": "sam", "price": 2, "buyers": [
            {"user": "alice", "at": 1, "amount": 2},
            {"user": "bob", "at": 4, "amount": 2},
            {"user": "mist", "at": 5, "amount": 2},
            {"user": "Carol", "at": 6, "amount": 1},
        ]})
        write(os.path.join(self.data, "trash", "p4.json"), {"id": "p4", "owner": "sam", "price": 10, "buyers": [
            {"user": "dave", "at": 7, "amount": 10},
        ]})
        write(os.path.join(self.data, "purchase-keys.json"), {"keys": {
            "mwbuy_paid": {"projectId": "p5", "buyer": "erin", "price": 3, "paid": True, "expiresAt": NOW - 1},
            "mwbuy_stale": {"projectId": "p5", "buyer": "frank", "price": 3, "expiresAt": NOW - 1},
            "mwgame_open": {"projectId": "p3", "buyer": "gina", "price": 5, "expiresAt": NOW + 60000},
        }})
        self.rotur = FakeRotur()
        self.ledger = os.path.join(self.data, "project-sale-refunds.json")

    def tearDown(self):
        self.rotur.stop()
        shutil.rmtree(self.dir)

    def client(self):
        return refunds.Rotur(self.rotur.base, "tok", timeout=5)

    def run_script(self, **values):
        out = io.StringIO()
        code = refunds.run(Args(self.data, **values), out=out, rotur=self.client(), sleep=lambda _: None)
        return code, out.getvalue()

    def sent(self):
        return sorted((t["to"], t["amount"]) for t in self.rotur.transfers)

    def test_finds_every_project_purchase_once(self):
        purchases = refunds.collect_purchases(self.data)
        self.assertEqual(sorted(purchases), [
            "alice|p1", "alice|p2", "bob|p1", "carol|p1", "dave|p4", "erin|p5", "mist|p1",
        ])
        self.assertEqual(purchases["alice|p2"]["amount"], 100)
        self.assertEqual(purchases["erin|p5"]["amount"], 3)

    def test_dry_run_sends_nothing_and_writes_nothing(self):
        code, out = self.run_script()
        self.assertEqual(code, 0)
        self.assertEqual(self.rotur.transfers, [])
        self.assertEqual(self.rotur.me_calls, 0)
        self.assertFalse(os.path.exists(self.ledger))
        self.assertIn("Project purchases found: 7 by 6 buyers, 120.0 credits.", out)
        self.assertIn("Would send 102.0 credits to alice for 2 purchases.", out)
        self.assertIn("Skipped mist|p1: bought by the paying account.", out)
        self.assertIn("To refund now: 118.0 credits to 5 buyers.", out)
        self.assertIn("Dry run: nothing was sent.", out)

    def test_apply_pays_each_buyer_once_and_a_rerun_pays_nothing(self):
        code, out = self.run_script(apply=True)
        self.assertEqual(code, 0, out)
        self.assertEqual(self.sent(), [("alice", 102.0), ("bob", 2.0), ("carol", 1.0), ("dave", 10.0), ("erin", 3.0)])
        self.assertTrue(all(t["note"] == refunds.NOTE for t in self.rotur.transfers))
        self.assertLessEqual(len(refunds.NOTE), 50)
        ledger = refunds.read_json(self.ledger, {})
        self.assertEqual({t["status"] for t in ledger["transfers"]}, {"sent"})

        code, out = self.run_script(apply=True)
        self.assertEqual(code, 0, out)
        self.assertEqual(len(self.rotur.transfers), 5)
        self.assertIn("Already refunded: 6 purchases.", out)
        self.assertIn("Nothing to send.", out)

    def test_a_late_purchase_is_refunded_on_the_next_run(self):
        self.run_script(apply=True)
        write(os.path.join(self.data, "users", "bob.json"), {"username": "bob", "purchases": [
            {"projectId": "p6", "title": "Late", "seller": "sam", "amount": 4, "at": 8},
            {"projectId": "p1", "title": "Maze", "seller": "sam", "amount": 2, "at": 4},
        ]})
        code, out = self.run_script(apply=True)
        self.assertEqual(code, 0, out)
        self.assertEqual(self.rotur.transfers[-1], {"to": "bob", "amount": 4.0, "note": refunds.NOTE})
        self.assertEqual(len(self.rotur.transfers), 6)

    def test_a_refused_transfer_is_tried_again(self):
        self.rotur.fail_for["carol"] = 400
        code, out = self.run_script(apply=True)
        self.assertEqual(code, 1)
        self.assertIn("failed: 1.0 credits to carol (recipient user not found)", out)
        self.assertNotIn("carol", [t["to"] for t in self.rotur.transfers])
        del self.rotur.fail_for["carol"]
        code, out = self.run_script(apply=True)
        self.assertEqual(code, 0, out)
        self.assertEqual(self.rotur.transfers[-1]["to"], "carol")
        self.assertEqual(len(self.rotur.transfers), 5)

    def test_an_unclear_answer_stops_until_it_is_resolved(self):
        self.rotur.fail_for["bob"] = 502
        code, out = self.run_script(apply=True)
        self.assertEqual(code, 1)
        self.assertIn("Stopped: Rotur answered 502 for bob.", out)
        self.assertEqual(self.sent(), [("alice", 102.0)])
        del self.rotur.fail_for["bob"]

        code, out = self.run_script(apply=True)
        self.assertEqual(code, 1)
        self.assertIn("may or may not have gone through", out)
        self.assertIn("Nothing was sent.", out)
        self.assertEqual(len(self.rotur.transfers), 1)

        ledger = refunds.read_json(self.ledger, {})
        stuck = [t["id"] for t in ledger["transfers"] if t["status"] == "unknown"]
        self.assertEqual(len(stuck), 1)
        code, _ = self.run_script(resolve=[stuck[0] + "=sent"])
        self.assertEqual(code, 0)
        code, out = self.run_script(apply=True)
        self.assertEqual(code, 0, out)
        self.assertEqual(self.sent(), [("alice", 102.0), ("carol", 1.0), ("dave", 10.0), ("erin", 3.0)])

    def test_open_project_purchases_block_sending(self):
        keys = refunds.read_json(os.path.join(self.data, "purchase-keys.json"), {})
        keys["keys"]["mwbuy_open"] = {"projectId": "p1", "buyer": "hal", "price": 2, "expiresAt": NOW + 60000}
        write(os.path.join(self.data, "purchase-keys.json"), keys)
        code, out = self.run_script(apply=True)
        self.assertEqual(code, 1)
        self.assertIn("1 project purchases can still be paid on Rotur.", out)
        self.assertEqual(self.rotur.transfers, [])

    def test_a_token_for_someone_else_sends_nothing(self):
        self.rotur.username = "someone"
        code, out = self.run_script(apply=True)
        self.assertEqual(code, 2)
        self.assertIn("The token does not belong to mist", out)
        self.assertEqual(self.rotur.transfers, [])
        self.assertFalse(os.path.exists(self.ledger))

    def test_too_few_credits_sends_nothing(self):
        self.rotur.balance = 50
        code, out = self.run_script(apply=True)
        self.assertEqual(code, 2)
        self.assertIn("mist has 50.0 credits but the refunds need 118.0.", out)
        self.assertEqual(self.rotur.transfers, [])

    def test_apply_needs_a_payer(self):
        code, out = self.run_script(apply=True, payer=None)
        self.assertEqual(code, 2)
        self.assertEqual(self.rotur.transfers, [])


if __name__ == "__main__":
    unittest.main()
