import argparse
import io
import re
import subprocess
import sys
import tokenize

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


def c_style_comments(text):
    found, line, index, quote = [], 1, 0, None
    while index < len(text):
        char = text[index]
        if quote:
            if char == "\\" and quote != "`":
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
            found.append((line, text[index:end]))
            index = end
            continue
        elif text.startswith("/*", index):
            end = text.find("*/", index + 2)
            end = len(text) if end == -1 else end + 2
            for offset, part in enumerate(text[index:end].split("\n")):
                found.append((line + offset, part))
            line += text[index:end].count("\n")
            index = end
            continue
        elif char == "\n":
            line += 1
        index += 1
    return found


def python_comments(text):
    found = []
    try:
        for token in tokenize.generate_tokens(io.StringIO(text).readline):
            if token.type == tokenize.COMMENT and not (token.start[0] == 1 and token.string.startswith("#!")):
                found.append((token.start[0], token.string))
    except (tokenize.TokenError, SyntaxError):
        return found
    return found


SCANNERS = {".go": c_style_comments, ".py": python_comments}


def main():
    parser = argparse.ArgumentParser(description="Reject code comments and em dashes in added lines")
    parser.add_argument("--base", default="origin/main")
    parser.add_argument("--staged", action="store_true")
    options = parser.parse_args()
    diff_args = ["diff", "--cached"] if options.staged else ["diff", git("merge-base", options.base, "HEAD").strip()]
    files, removed = parse_diff(git(*diff_args, "--unified=0", "--no-color", "--diff-filter=ACMRD"))
    removed_text = "\n".join(removed)
    problems = []
    for path, added in files.items():
        if not TEXT_FILE.search(path) or any(pattern.search(path) for pattern in SKIPPED):
            continue
        for line, content in added.items():
            if EM_DASH in content and content.strip() not in removed:
                problems.append(f'{path}:{line}  em dash: use a comma, parentheses or "to"')
        scan = next((scanner for suffix, scanner in SCANNERS.items() if path.endswith(suffix)), None)
        if scan is None:
            continue
        text = git("show", f":{path}") if options.staged else open(path, encoding="utf-8").read()
        flagged = set()
        for line, comment in scan(text):
            portion = comment.strip()
            if line in flagged or line not in added or DIRECTIVE.match(portion):
                continue
            if not re.search(r"[A-Za-z0-9]", portion) or portion in removed_text:
                continue
            flagged.add(line)
            problems.append(f"{path}:{line}  code comment: name things so the code explains itself")
    if problems:
        print("\n".join(problems), file=sys.stderr)
        print(f"\n{len(problems)} convention problem{'' if len(problems) == 1 else 's'} in the changed lines.", file=sys.stderr)
        return 1
    print("conventions ok")
    return 0


if __name__ == "__main__":
    sys.exit(main())
