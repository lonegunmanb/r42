import json
import re
import sys
from pathlib import Path


QUOTE_ID_RE = re.compile(
    r"(?<![A-Za-z0-9_-])([A-Za-z0-9][A-Za-z0-9_-]*-quote-[A-Za-z0-9][A-Za-z0-9_-]*)(?![A-Za-z0-9_-])"
)
ARTIFACT_ID_RE = re.compile(
    r"^artifact-(?:[0-9a-f]{32}|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$"
)
MATCH_MODES = (
    "typed_tool_validated",
    "exact",
    "line_ending_equivalent",
    "whitespace_equivalent",
    "unicode_equivalent",
    "not_found",
)
MAX_RETURNED_ISSUES = 25


def new_issue(code, message, path, repair_hint):
    return {
        "code": code,
        "message": message,
        "path": path,
        "repair_hint": repair_hint,
    }


def empty_result():
    return {
        "pass": False,
        "report_quote_ids": 0,
        "knowledge_quote_ids": 0,
        "knowledge_artifacts": 0,
        "artifacts_checked": 0,
        "conflicts": 0,
        "match_modes": {mode: 0 for mode in MATCH_MODES},
        "issue_count": 0,
        "issues": [],
        "audit_path": "",
    }


def read_text(path):
    return path.read_text(encoding="utf-8-sig")


def read_json(path):
    return json.loads(read_text(path))


def workspace_blocks_root(workspace):
    workspace = workspace.resolve()
    blocks = workspace.parent
    if blocks.name.lower() != "blocks" or blocks.parent.parent.name.lower() != "runs":
        return None
    return blocks.resolve()


def is_within(path, root):
    try:
        path.resolve().relative_to(root.resolve())
        return True
    except (OSError, ValueError):
        return False


def body_quote_ids(report):
    body_lines = []
    for line in report.splitlines():
        if "|" in line and QUOTE_ID_RE.search(line):
            continue
        body_lines.append(line)
    return set(QUOTE_ID_RE.findall("\n".join(body_lines)))


def validate_input_path(raw, expected_name, root, field, issues):
    path = Path(str(raw)).expanduser()
    if not path.is_absolute():
        issues.append(
            new_issue(
                "invalid_path",
                f"{field} must be absolute",
                field,
                "Use the absolute path supplied in the synthesis task prompt.",
            )
        )
        return None
    path = path.resolve()
    if path.name != expected_name or not is_within(path, root):
        issues.append(
            new_issue(
                "invalid_path",
                f"{field} must name {expected_name} under the current run's blocks directory",
                field,
                "Use the planned artifact path without changing its run or block directory.",
            )
        )
        return None
    return path


def load_knowledge(paths, root, issues):
    quotes = {}
    loaded = 0
    seen_paths = set()
    for index, raw in enumerate(paths):
        field = f"knowledge_paths[{index}]"
        path = validate_input_path(raw, "knowledge.json", root, field, issues)
        if path is None:
            continue
        if path in seen_paths:
            issues.append(
                new_issue(
                    "duplicate_knowledge_artifact",
                    f"knowledge artifact is listed more than once: {path}",
                    field,
                    "Pass every knowledge artifact exactly once.",
                )
            )
            continue
        seen_paths.add(path)
        try:
            document = read_json(path)
        except (OSError, UnicodeError, json.JSONDecodeError) as error:
            issues.append(
                new_issue(
                    "invalid_knowledge_artifact",
                    f"cannot read knowledge artifact: {error}",
                    str(path),
                    "Regenerate a valid UTF-8 knowledge.json artifact.",
                )
            )
            continue
        loaded += 1
        records = document.get("quotes")
        if not isinstance(records, list):
            issues.append(
                new_issue(
                    "invalid_knowledge_artifact",
                    "quotes must be a list",
                    str(path),
                    "Regenerate knowledge.json with the submit_knowledge tool.",
                )
            )
            continue
        for quote_index, quote in enumerate(records):
            quote_path = f"{path}#quotes[{quote_index}]"
            if not isinstance(quote, dict):
                issues.append(
                    new_issue(
                        "invalid_quote",
                        "quote must be an object",
                        quote_path,
                        "Regenerate knowledge.json with a structured quote record.",
                    )
                )
                continue
            quote_id = str(quote.get("id", "")).strip()
            if not quote_id:
                issues.append(
                    new_issue(
                        "invalid_quote_id",
                        "quote id is required",
                        quote_path,
                        "Give every quote a globally unique ID.",
                    )
                )
                continue
            quote["_artifact_path"] = str(path)
            quote["_record_path"] = quote_path
            quotes[quote_id] = quote
    return quotes, loaded


def audit(payload, workspace=None):
    result = empty_result()
    issues = []
    matches = []
    if not isinstance(payload, dict):
        issues.append(
            new_issue(
                "invalid_input",
                "tool input must be an object",
                "input",
                "Call the tool once with report_path, knowledge_paths, and resolution_path.",
            )
        )
        result["issues"] = issues
        result["issue_count"] = len(issues)
        return result

    workspace = Path.cwd() if workspace is None else Path(workspace)
    workspace = workspace.expanduser().resolve()
    root = workspace_blocks_root(workspace)
    if root is None:
        issues.append(
            new_issue(
                "invalid_workspace",
                "tool working directory must be a block workspace under .r42/runs/<run>/blocks",
                "workspace",
                "Run the tool through the synthesis QC session.",
            )
        )
        result["issues"] = issues
        result["issue_count"] = len(issues)
        return result

    report_path = Path(str(payload.get("report_path", ""))).expanduser()
    expected_report = (workspace / "report.md").resolve()
    if not report_path.is_absolute() or report_path.resolve() != expected_report:
        issues.append(
            new_issue(
                "invalid_report_path",
                "report_path must be the current synthesis workspace's report.md",
                "report_path",
                "Use the report artifact path supplied in the current synthesis task.",
            )
        )
        result["issues"] = issues
        result["issue_count"] = len(issues)
        return result
    report_path = validate_input_path(
        report_path, "report.md", root, "report_path", issues
    )
    knowledge_paths = payload.get("knowledge_paths")
    if not isinstance(knowledge_paths, list) or not knowledge_paths:
        issues.append(
            new_issue(
                "invalid_knowledge_paths",
                "knowledge_paths must contain every upstream knowledge.json artifact",
                "knowledge_paths",
                "Copy the complete ordered path list from the synthesis task prompt.",
            )
        )
        knowledge_paths = []
    resolution_path = validate_input_path(
        payload.get("resolution_path", ""),
        "resolution.json",
        root,
        "resolution_path",
        issues,
    )

    report = ""
    if report_path is not None:
        try:
            report = read_text(report_path)
        except (OSError, UnicodeError) as error:
            issues.append(
                new_issue(
                    "invalid_report",
                    f"cannot read report: {error}",
                    str(report_path),
                    "Regenerate report.md as UTF-8 Markdown.",
                )
            )

    quotes, loaded = load_knowledge(knowledge_paths, root, issues)
    result["knowledge_artifacts"] = loaded
    result["knowledge_quote_ids"] = len(quotes)

    if resolution_path is not None:
        try:
            resolution = read_json(resolution_path)
            conflicts = resolution.get("conflicts", [])
            if not isinstance(conflicts, list):
                raise ValueError("conflicts must be a list")
            result["conflicts"] = len(conflicts)
        except (OSError, UnicodeError, json.JSONDecodeError, ValueError) as error:
            issues.append(
                new_issue(
                    "invalid_resolution_artifact",
                    f"cannot read conflict resolution: {error}",
                    str(resolution_path),
                    "Regenerate a valid UTF-8 resolution.json artifact.",
                )
            )

    body_ids = body_quote_ids(report)
    report_ids = set(body_ids)
    result["report_quote_ids"] = len(report_ids)

    for quote_id in sorted(body_ids & quotes.keys()):
        quote = quotes[quote_id]
        artifact_id = str(quote.get("artifact_id", "")).strip()
        record_path = quote.get("_record_path", quote_id)
        if not ARTIFACT_ID_RE.fullmatch(artifact_id):
            issues.append(
                new_issue(
                    "invalid_artifact_id",
                    f"quote {quote_id} does not reference a registered artifact ID",
                    record_path,
                    "Retain the artifact_id accepted by the upstream Research typed tool.",
                )
            )
            result["match_modes"]["not_found"] += 1
            continue
        result["artifacts_checked"] += 1
        mode = "typed_tool_validated"
        result["match_modes"][mode] += 1
        matches.append(
            {
                "quote_id": quote_id,
                "match_mode": mode,
                "artifact_id": artifact_id,
            }
        )

    audit_path = report_path.parent / "synthesis-audit.json" if report_path else None
    result["pass"] = len(issues) == 0
    result["issue_count"] = len(issues)
    result["issues"] = issues[:MAX_RETURNED_ISSUES]
    if audit_path is not None:
        result["audit_path"] = str(audit_path)
        full_result = dict(result)
        full_result["issues"] = issues
        full_result["matches"] = matches
        try:
            audit_path.write_text(
                json.dumps(full_result, ensure_ascii=False, indent=2) + "\n",
                encoding="utf-8",
            )
        except OSError as error:
            write_issue = new_issue(
                "audit_write_failed",
                f"cannot write synthesis audit: {error}",
                str(audit_path),
                "Ensure the synthesis block workspace is writable.",
            )
            issues.append(write_issue)
            result["pass"] = False
            result["issue_count"] = len(issues)
            result["issues"] = issues[:MAX_RETURNED_ISSUES]
    return result


def main():
    try:
        payload = json.load(sys.stdin)
        output = audit(payload)
        response = {"accepted": True, "output": output}
    except Exception as error:  # The process must always return one typed response.
        output = empty_result()
        output["issues"] = [
            new_issue(
                "audit_failed",
                f"synthesis audit failed: {error}",
                "input",
                "Correct the tool input using the planned absolute artifact paths.",
            )
        ]
        output["issue_count"] = 1
        response = {"accepted": True, "output": output}
    json.dump(response, sys.stdout, ensure_ascii=False)
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()
