import glob
import os
import re
import sys

METHODS = {"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "HEAD", "ANY"}
DEF = re.compile(r"^def (\w+)\((.*?)\)\s*\w*\s*\($")
CALL = re.compile(r"^\s*(?:void\s+)?(\w+)\((.*)\)\s*$")
GROUP = re.compile(r"^\s*\*serve\.Router (\w+) = (\w+)\.group\(\"([^\"]*)\"\)\s*$")
ROUTER = re.compile(r"^\s*\*serve\.Router (\w+) = serve\.new\(\)\s*$")
METHOD = re.compile(r"^\s*(\w+)\.(\w+)\((.*)\)\s*$")


def split_args(text):
    args, depth, current, quote = [], 0, "", False
    for i, ch in enumerate(text):
        if ch == "\"" and (i == 0 or text[i - 1] != "\\"):
            quote = not quote
        if not quote:
            if ch in "([{":
                depth += 1
            elif ch in ")]}":
                depth -= 1
            elif ch == "," and depth == 0:
                args.append(current.strip())
                current = ""
                continue
        current += ch
    if current.strip():
        args.append(current.strip())
    return args


def load_functions(root):
    functions = {}
    for path in glob.glob(os.path.join(root, "**", "*.osl"), recursive=True):
        if path.endswith(".test.osl") or "/.claude/" in path or "/.opal/" in path:
            continue
        lines = open(path).read().split("\n")
        i = 0
        while i < len(lines):
            match = DEF.match(lines[i])
            if match:
                params = [p.strip().split()[-1] for p in split_args(match.group(2)) if p.strip()]
                body = []
                i += 1
                while i < len(lines) and lines[i] != ")":
                    body.append(lines[i])
                    i += 1
                functions[match.group(1)] = (params, body)
            i += 1
    return functions


class Dump:
    def __init__(self, functions):
        self.functions = functions
        self.routes = []
        self.order = {}

    def run(self, lines, routers, depth=0):
        if depth > 20:
            return
        for line in lines:
            if line.strip().startswith("//"):
                continue
            match = ROUTER.match(line)
            if match:
                routers[match.group(1)] = {"prefix": "", "mw": []}
                continue
            match = GROUP.match(line)
            if match and match.group(2) in routers:
                parent = routers[match.group(2)]
                routers[match.group(1)] = {"prefix": parent["prefix"] + match.group(3), "mw": list(parent["mw"])}
                continue
            match = METHOD.match(line)
            if match and match.group(1) in routers:
                router = routers[match.group(1)]
                name = match.group(2)
                args = split_args(match.group(3))
                if name == "use":
                    router["mw"].extend(args)
                elif name.upper() in METHODS or name in ("static", "ws", "staticFile"):
                    path = router["prefix"] + args[0].strip("\"")
                    kind = name.upper() if name.upper() in METHODS else name
                    self.routes.append("%s %s | %s" % (kind, path, " , ".join(router["mw"] + args[1:])))
                    self.order.setdefault(path, []).append(kind)
                else:
                    self.routes.append("%s.%s(%s) on %s" % ("router", name, ", ".join(args), router["prefix"] or "/"))
                continue
            match = CALL.match(line)
            if match and match.group(1) in self.functions:
                params, body = self.functions[match.group(1)]
                args = split_args(match.group(2))
                scope = dict(routers)
                for param, arg in zip(params, args):
                    if arg in routers:
                        scope[param] = routers[arg]
                if any(arg in routers for arg in args) or any(re.search(r"\b(%s)\.\w+\(" % "|".join(map(re.escape, scope)), l) for l in body):
                    self.run(body, scope, depth + 1)


def main():
    root = sys.argv[1] if len(sys.argv) > 1 else "."
    functions = load_functions(root)
    lines = [l for l in open(os.path.join(root, "main.osl")).read().split("\n")]
    top = []
    skip = False
    for line in lines:
        if DEF.match(line):
            skip = True
            continue
        if skip:
            if line == ")":
                skip = False
            continue
        top.append(line)
    dump = Dump(functions)
    dump.run(top, {})
    for route in sorted(dump.routes):
        print(route)
    for path in sorted(dump.order):
        print("ORDER %s %s" % (path, ",".join(dump.order[path])))


if __name__ == "__main__":
    main()
