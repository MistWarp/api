#!/usr/bin/env python3
import argparse
import glob
import json
import os
import sys
import time
import urllib.error
import urllib.request

NOTE = "MistWarp refund: project sales have ended"
LEDGER_VERSION = 1
SETTLED = "sent"
UNRESOLVED = ("sending", "unknown")


def normalize(name):
    return str(name or "").strip().lower()


def read_json(path, default):
    try:
        with open(path, encoding="utf-8") as handle:
            return json.load(handle)
    except (OSError, ValueError):
        return default


def write_json(path, data):
    folder = os.path.dirname(os.path.abspath(path))
    os.makedirs(folder, exist_ok=True)
    temp = "%s.tmp.%d" % (path, os.getpid())
    with open(temp, "w", encoding="utf-8") as handle:
        json.dump(data, handle, indent=1, sort_keys=True)
        handle.flush()
        os.fsync(handle.fileno())
    os.replace(temp, path)


def amount_of(value):
    try:
        return round(float(value or 0), 2)
    except (TypeError, ValueError):
        return 0.0


def project_files(data_dir):
    paths = glob.glob(os.path.join(data_dir, "projects", "*.json"))
    paths += glob.glob(os.path.join(data_dir, "trash", "**", "*.json"), recursive=True)
    return sorted(paths)


def collect_purchases(data_dir):
    purchases = {}
    for path in sorted(glob.glob(os.path.join(data_dir, "users", "*.json"))):
        profile = read_json(path, {})
        if not isinstance(profile, dict):
            continue
        buyer = normalize(profile.get("username")) or normalize(os.path.basename(path)[:-5])
        for entry in profile.get("purchases") or []:
            if not isinstance(entry, dict) or (entry.get("type") or "project") != "project":
                continue
            project_id = str(entry.get("projectId") or "")
            if not buyer or not project_id:
                continue
            purchases["%s|%s" % (buyer, project_id)] = {
                "buyer": buyer,
                "projectId": project_id,
                "title": str(entry.get("title") or ""),
                "amount": amount_of(entry.get("amount")),
                "at": entry.get("at") or 0,
            }
    for path in project_files(data_dir):
        project = read_json(path, {})
        if not isinstance(project, dict) or not project.get("buyers"):
            continue
        project_id = str(project.get("id") or os.path.basename(path)[:-5])
        for entry in project.get("buyers") or []:
            if not isinstance(entry, dict):
                continue
            buyer = normalize(entry.get("user"))
            key = "%s|%s" % (buyer, project_id)
            if not buyer or key in purchases:
                continue
            purchases[key] = {
                "buyer": buyer,
                "projectId": project_id,
                "title": str(project.get("title") or ""),
                "amount": amount_of(entry.get("amount")),
                "at": entry.get("at") or 0,
            }
    for key, entry in sorted(project_purchase_keys(data_dir).items()):
        buyer = normalize(entry.get("buyer"))
        project_id = str(entry.get("projectId") or "")
        found = "%s|%s" % (buyer, project_id)
        if entry.get("paid") is True and buyer and project_id and found not in purchases:
            purchases[found] = {
                "buyer": buyer,
                "projectId": project_id,
                "title": "",
                "amount": amount_of(entry.get("price")),
                "at": entry.get("at") or 0,
            }
    return purchases


def project_purchase_keys(data_dir):
    keys = read_json(os.path.join(data_dir, "purchase-keys.json"), {}).get("keys") or {}
    return {k: v for k, v in keys.items() if str(k).startswith("mwbuy_") and isinstance(v, dict)}


def open_project_purchases(data_dir, now_ms):
    return sorted(
        key for key, entry in project_purchase_keys(data_dir).items()
        if entry.get("paid") is not True and amount_of(entry.get("expiresAt")) > now_ms
    )


def load_ledger(path):
    ledger = read_json(path, None)
    if not isinstance(ledger, dict):
        ledger = {"version": LEDGER_VERSION, "transfers": []}
    ledger.setdefault("transfers", [])
    return ledger


def settled_keys(ledger):
    keys = set()
    for transfer in ledger["transfers"]:
        if transfer.get("status") == SETTLED:
            keys.update(transfer.get("purchases") or [])
    return keys


def unresolved(ledger):
    return [t for t in ledger["transfers"] if t.get("status") in UNRESOLVED]


def plan_refunds(purchases, ledger, payer):
    done = settled_keys(ledger)
    blocked = set()
    for transfer in unresolved(ledger):
        blocked.update(transfer.get("purchases") or [])
    by_buyer = {}
    skipped = []
    for key, purchase in sorted(purchases.items()):
        if key in done or key in blocked:
            continue
        if purchase["amount"] <= 0:
            skipped.append((key, "nothing was paid"))
            continue
        if payer and purchase["buyer"] == normalize(payer):
            skipped.append((key, "bought by the paying account"))
            continue
        by_buyer.setdefault(purchase["buyer"], []).append(key)
    plan = []
    for buyer in sorted(by_buyer):
        keys = by_buyer[buyer]
        plan.append({
            "to": buyer,
            "purchases": keys,
            "amount": round(sum(purchases[k]["amount"] for k in keys), 2),
        })
    return plan, skipped, len(done)


class Rotur(object):
    def __init__(self, base, token, timeout=30):
        self.base = base.rstrip("/")
        self.token = token
        self.timeout = timeout

    def call(self, method, path, body=None):
        data = None if body is None else json.dumps(body).encode("utf-8")
        request = urllib.request.Request(self.base + path, data=data, method=method)
        request.add_header("Authorization", "Bearer " + self.token)
        request.add_header("Accept", "application/json")
        if data is not None:
            request.add_header("Content-Type", "application/json")
        try:
            with urllib.request.urlopen(request, timeout=self.timeout) as response:
                return response.status, read_body(response)
        except urllib.error.HTTPError as error:
            return error.code, read_body(error)

    def me(self):
        return self.call("GET", "/v2/me")

    def transfer(self, to, amount, note):
        return self.call("POST", "/v2/me/transfers", {"to": to, "amount": amount, "note": note})


def read_body(response):
    raw = response.read().decode("utf-8", "replace")
    try:
        return json.loads(raw)
    except ValueError:
        return {"raw": raw[:200]}


def balance_of(me):
    for key in ("sys.currency", "currency", "credits"):
        if key in me:
            try:
                return float(me[key])
            except (TypeError, ValueError):
                return None
    return None


def resolve(ledger, ledger_path, rulings):
    for ruling in rulings:
        transfer_id, _, status = ruling.partition("=")
        if status not in (SETTLED, "failed"):
            raise SystemExit("--resolve takes ID=sent or ID=failed, not %r" % ruling)
        match = [t for t in ledger["transfers"] if t.get("id") == transfer_id]
        if not match or match[0].get("status") not in UNRESOLVED:
            raise SystemExit("no unresolved transfer %s in the ledger" % transfer_id)
        match[0]["status"] = status
        match[0]["resolvedAt"] = int(time.time() * 1000)
    write_json(ledger_path, ledger)


def run(args, out=sys.stdout, rotur=None, sleep=time.sleep):
    data_dir = args.data
    ledger_path = args.ledger or os.path.join(data_dir, "project-sale-refunds.json")
    ledger = load_ledger(ledger_path)

    if args.resolve:
        resolve(ledger, ledger_path, args.resolve)
        print("Updated %s." % ledger_path, file=out)
        return 0

    purchases = collect_purchases(data_dir)
    plan, skipped, already = plan_refunds(purchases, ledger, args.payer)
    total = round(sum(item["amount"] for item in plan), 2)
    buyers = len({p["buyer"] for p in purchases.values()})
    credits = round(sum(p["amount"] for p in purchases.values()), 2)

    print("Project purchases found: %d by %d buyers, %s credits." % (len(purchases), buyers, credits), file=out)
    print("Already refunded: %d purchases." % already, file=out)
    for key, reason in skipped:
        print("Skipped %s: %s." % (key, reason), file=out)
    for item in plan:
        print("%s %s credits to %s for %d purchases." % (
            "Send" if args.apply else "Would send", item["amount"], item["to"], len(item["purchases"])), file=out)
    print("To refund now: %s credits to %d buyers." % (total, len(plan)), file=out)

    problems = []
    pending = open_project_purchases(data_dir, args.now if args.now is not None else time.time() * 1000)
    if pending:
        problems.append("%d project purchases can still be paid on Rotur. Wait 15 minutes and run this again." % len(pending))
    for transfer in unresolved(ledger):
        problems.append("Transfer %s of %s credits to %s may or may not have gone through. Check the payer's Rotur transactions, then run --resolve %s=sent or %s=failed." % (
            transfer["id"], transfer["amount"], transfer["to"], transfer["id"], transfer["id"]))
    for problem in problems:
        print(problem, file=out)

    if not args.apply:
        print("Dry run: nothing was sent. Add --apply to send these refunds.", file=out)
        return 1 if problems else 0
    if problems:
        print("Nothing was sent.", file=out)
        return 1
    if not plan:
        print("Nothing to send.", file=out)
        return 0
    if not args.payer:
        print("--apply needs --payer, the Rotur account the refunds come from.", file=out)
        return 2

    if rotur is None:
        token = os.environ.get("ROTUR_REFUND_TOKEN", "")
        if not token:
            print("Set ROTUR_REFUND_TOKEN to a token of the paying account.", file=out)
            return 2
        rotur = Rotur(args.rotur_base, token)

    status, me = rotur.me()
    if status != 200 or normalize(me.get("username")) != normalize(args.payer):
        print("The token does not belong to %s (Rotur answered %s). Nothing was sent." % (args.payer, status), file=out)
        return 2
    balance = balance_of(me)
    if balance is not None and balance < total:
        print("%s has %s credits but the refunds need %s. Nothing was sent." % (args.payer, balance, total), file=out)
        return 2

    failed = 0
    for item in plan:
        transfer = {
            "id": "refund-%d-%s" % (int(time.time() * 1000), item["to"]),
            "to": item["to"],
            "amount": item["amount"],
            "purchases": item["purchases"],
            "from": normalize(args.payer),
            "status": "sending",
            "at": int(time.time() * 1000),
        }
        ledger["transfers"].append(transfer)
        write_json(ledger_path, ledger)
        try:
            status, body = rotur.transfer(item["to"], item["amount"], args.note)
        except Exception as error:
            transfer["status"] = "unknown"
            transfer["error"] = str(error)[:200]
            write_json(ledger_path, ledger)
            print("Stopped: the transfer to %s didn't answer (%s). Check it, then --resolve %s." % (item["to"], error, transfer["id"]), file=out)
            return 1
        if status == 200:
            transfer["status"] = SETTLED
        elif 400 <= status < 500:
            transfer["status"] = "failed"
            transfer["error"] = str(body.get("error") or body)[:200]
            failed += 1
        else:
            transfer["status"] = "unknown"
            transfer["error"] = "Rotur answered %s" % status
            write_json(ledger_path, ledger)
            print("Stopped: Rotur answered %s for %s. Check it, then --resolve %s." % (status, item["to"], transfer["id"]), file=out)
            return 1
        transfer["answeredAt"] = int(time.time() * 1000)
        write_json(ledger_path, ledger)
        print("%s: %s credits to %s%s" % (
            transfer["status"], item["amount"], item["to"],
            "" if status == 200 else " (" + transfer["error"] + ")"), file=out)
        sleep(args.delay)

    print("Done: %d sent, %d failed. Failed ones are tried again next run." % (len(plan) - failed, failed), file=out)
    return 1 if failed else 0


def parse_args(argv):
    parser = argparse.ArgumentParser(
        description="Refund every credit spent buying MistWarp projects, from the Rotur account whose token is in ROTUR_REFUND_TOKEN. Without --apply nothing is sent. What was sent is kept in a ledger, so a purchase is never refunded twice.")
    parser.add_argument("--data", default="data", help="mistwarp-api's data folder (default: data)")
    parser.add_argument("--ledger", help="where refunds are recorded (default: <data>/project-sale-refunds.json)")
    parser.add_argument("--apply", action="store_true", help="send the refunds; without it nothing is sent")
    parser.add_argument("--payer", help="the Rotur account the refunds come from, checked against the token")
    parser.add_argument("--rotur-base", default="https://api.rotur.dev", help="Rotur API base URL")
    parser.add_argument("--note", default=NOTE, help="note on each transfer (Rotur keeps 50 characters)")
    parser.add_argument("--delay", type=float, default=0.5, help="seconds between transfers")
    parser.add_argument("--resolve", action="append", metavar="ID=sent|failed", help="settle a transfer that didn't answer")
    parser.add_argument("--now", type=float, help=argparse.SUPPRESS)
    return parser.parse_args(argv)


if __name__ == "__main__":
    sys.exit(run(parse_args(sys.argv[1:])))
