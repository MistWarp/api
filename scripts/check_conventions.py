import argparse
import re
import subprocess
import sys

EM_DASH = "\u2014"
TEXT_FILE = re.compile(r"\.(osl|go|py|md|json|yml|yaml|sh|txt)$")
SKIPPED = [re.compile(r"(^|/)go\.sum$"), re.compile(r"^data/")]
DIRECTIVE = re.compile(r"^(//|#)\s*(nolint|noqa|go:|lint:|type:\s*ignore)")


def git(*args):
    return subprocess.run(["git", *args], check=True, capture_output=True, text=True).stdout


def parse_diff(text):
    files, removed = {}, set()
    current, line = None, 0
    for row in text.split("\n"):
        if row.startswith("+++ "):
            path = row[4:]
            current = None if path == "/dev/null" else re.sub(r"^b/", "", path)
            if current is not None:
                files.setdefault(current, {})
        elif row.startswith("@@"):
            match = re.search(r"\+(\d+)", row)
            line = int(match.group(1)) if match else 0
        elif row.startswith("+") and current is not None:
            files[current][line] = row[1:]
            line += 1
        elif row.startswith("-") and not row.startswith("---"):
            removed.add(row[1:].strip())
    return files, removed


def osl_comment_lines(text):
    lines, line, index, quote = set(), 1, 0, None
    while index < len(text):
        char = text[index]
        if quote:
            if char == "\\":
                index += 1
            elif char == quote:
                quote = None
            elif char == "\n":
                line += 1
        elif char in "\"'`":
            quote = char
        elif text.startswith("//", index):
            end = text.find("\n", index)
            end = len(text) if end == -1 else end
            if not DIRECTIVE.match(text[index:end]):
                lines.add(line)
            index = end
            continue
        elif text.startswith("/*", index):
            end = text.find("*/", index + 2)
            end = len(text) if end == -1 else end + 2
            span = text[index:end]
            for offset in range(span.count("\n") + 1):
                lines.add(line + offset)
            line += span.count("\n")
            index = end
            continue
        elif char == "\n":
            line += 1
        index += 1
    return lines


def python_comment_lines(text):
    found = set()
    for number, row in enumerate(text.split("\n"), start=1):
        stripped = row.strip()
        if stripped.startswith("#") and not stripped.startswith("#!") and not DIRECTIVE.match(stripped):
            found.add(number)
    return found


def main():
    parser = argparse.ArgumentParser(description="Reject code comments and em dashes in added lines")
    parser.add_argument("--base", default="origin/main")
    parser.add_argument("--staged", action="store_true")
    options = parser.parse_args()
    diff_args = ["diff", "--cached"] if options.staged else ["diff", git("merge-base", options.base, "HEAD").strip()]
    files, removed = parse_diff(git(*diff_args, "--unified=0", "--no-color", "--diff-filter=ACMR"))
    problems = []
    for path, added in files.items():
        if not TEXT_FILE.search(path) or any(pattern.search(path) for pattern in SKIPPED):
            continue
        for line, content in added.items():
            if EM_DASH in content:
                problems.append(f'{path}:{line}  em dash: use a comma, parentheses or "to"')
        if path.endswith(".osl"):
            scan = osl_comment_lines
        elif path.endswith(".py"):
            scan = python_comment_lines
        else:
            continue
        text = git("show", f":{path}") if options.staged else open(path, encoding="utf-8").read()
        for line in sorted(scan(text)):
            content = added.get(line)
            if content is not None and content.strip() and content.strip() not in removed:
                problems.append(f"{path}:{line}  code comment: name things so the code explains itself")
    if problems:
        print("\n".join(problems), file=sys.stderr)
        print(f"\n{len(problems)} convention problem{'' if len(problems) == 1 else 's'} in the changed lines.", file=sys.stderr)
        return 1
    print("conventions ok")
    return 0


if __name__ == "__main__":
    sys.exit(main())
