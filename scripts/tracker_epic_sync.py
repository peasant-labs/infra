#!/usr/bin/env python3
"""Keep every epic in the peasant-labs organization on the Peasant Labs Tracker.

The workflow runs on a schedule from peasant-labs/infra. The token in
``GH_TOKEN`` needs:

- Issues: read on every repository in the organization (titles, labels, bodies,
  and sub-issue counts are how epics are recognized).
- Issues: write (the Priority field is an organization issue field).
- Projects: read and write on the organization (add items and set fields).

The sweep is idempotent. It adds an epic only when the issue is not already on
the board, and fills a field only when that field is empty. Set ``DRY_RUN=1``
to report the same decisions without writing anything.

An issue is treated as an epic when any of these is true:

- it carries the ``epic`` label;
- its title starts with ``[Epic]``, ``Epic``, or ``EPIC``;
- it has at least one sub-issue (a de facto parent issue).

The repository denylist covers frozen history and test fixtures. Everything
else in the organization, including private repositories, is scanned; GitHub
already hides items from private repositories from people who cannot read them.
"""

from __future__ import annotations

import json
import os
import re
import subprocess
import sys
from typing import Any

ORG = "peasant-labs"
PROJECT_NUMBER = 4
PROJECT_ID = "PVT_kwDOD8oAWc4BmST0"

STATUS_FIELD = "PVTSSF_lADOD8oAWc4BmST0zhk78bI"
STATUS_BACKLOG = "f75ad846"

SIZE_FIELD = "PVTSSF_lADOD8oAWc4BmST0zhk78zs"
SIZE_OPTION = {
    "XS": "eff732af",
    "S": "9592a5a3",
    "M": "9728cbdc",
    "L": "c53df028",
    "XL": "7b141a16",
}

TEAM_FIELD = "PVTSSF_lADOD8oAWc4BmST0zhk8C_M"
TEAM_OPTION = {
    "UI / UX": "84cdf5a5",
    "Data Pipelines": "aa5f7e53",
    "Village": "e30cc464",
    "Research": "22e3da04",
}

PRIORITY_FIELD = "IFSS_kgDOAl-p-g"
PRIORITY_OPTION = {
    "P0": "IFSSO_kgDOBCdRjw",
    "P1": "IFSSO_kgDOBCdRkA",
    "P2": "IFSSO_kgDOBCdRkQ",
    "P3": "IFSSO_kgDOBCdRkg",
}

SKIP_REPOS = {
    "transcript-browser",  # archived; no runtime role
    "village-prerelease-archive",  # frozen history
    "peasant-prerelease-archive",  # frozen history
    "demo-repository",  # test fixture
    "e2e-peasant-githooks-test",  # test fixture
}

DRY_RUN = os.environ.get("DRY_RUN") == "1"


def log(message: str) -> None:
    print(message, flush=True)


def warn(message: str) -> None:
    print(f"::warning::{message}", flush=True)


def error(message: str) -> None:
    print(f"::error::{message}", flush=True)


def gh(args: list[str]) -> str:
    result = subprocess.run(
        ["gh", *args],
        capture_output=True,
        text=True,
        env=os.environ.copy(),
    )
    if result.returncode != 0:
        raise RuntimeError(
            f"gh {' '.join(args[:3])} failed: {result.stderr.strip()[:500]}"
        )
    return result.stdout


def graphql(query: str, variables: dict[str, Any] | None = None) -> dict[str, Any]:
    args = ["api", "graphql", "-f", f"query={query}"]
    for key, value in (variables or {}).items():
        if value is not None:
            args += ["-f", f"{key}={value}"]
    payload = json.loads(gh(args))
    if payload.get("errors"):
        raise RuntimeError(f"GraphQL error: {payload['errors'][:2]}")
    return payload["data"]


def list_repos() -> list[dict[str, Any]]:
    output = gh(
        [
            "api",
            f"orgs/{ORG}/repos?per_page=100",
            "--paginate",
            "--jq",
            ".[] | {name, archived, fork, has_issues}",
        ]
    )
    repos = []
    for line in output.splitlines():
        if not line.strip():
            continue
        repo = json.loads(line)
        if repo["archived"] or repo["fork"]:
            continue
        if not repo.get("has_issues", True):
            continue
        if repo["name"] in SKIP_REPOS:
            continue
        repos.append(repo["name"])
    return sorted(repos)


ISSUE_QUERY = """
query($owner: String!, $repo: String!, $cursor: String) {
  repository(owner: $owner, name: $repo) {
    issues(
      first: 100
      states: OPEN
      after: $cursor
      orderBy: {field: CREATED_AT, direction: DESC}
    ) {
      pageInfo { hasNextPage endCursor }
      nodes {
        id
        number
        title
        createdAt
        body
        labels(first: 20) { nodes { name } }
        subIssuesSummary { total }
        issueFieldValues(first: 10) {
          nodes {
            ... on IssueFieldSingleSelectValue {
              name
              field { ... on IssueFieldSingleSelect { name } }
            }
          }
        }
      }
    }
  }
}
"""


def list_open_issues(repo: str) -> list[dict[str, Any]]:
    issues: list[dict[str, Any]] = []
    cursor = None
    while True:
        data = graphql(
            ISSUE_QUERY,
            {"owner": ORG, "repo": repo, "cursor": cursor},
        )
        node = data.get("repository")
        if not node or not node.get("issues"):
            return issues
        page = node["issues"]
        issues.extend(page["nodes"])
        if not page["pageInfo"]["hasNextPage"]:
            return issues
        cursor = page["pageInfo"]["endCursor"]


PROJECT_QUERY = """
query($cursor: String) {
  organization(login: "peasant-labs") {
    projectV2(number: 4) {
      items(first: 100, after: $cursor) {
        pageInfo { hasNextPage endCursor }
        nodes {
          id
          content {
            __typename
            ... on Issue {
              id
              number
              title
              repository { name }
              issueFieldValues(first: 10) {
                nodes {
                  ... on IssueFieldSingleSelectValue {
                    name
                    field { ... on IssueFieldSingleSelect { name } }
                  }
                }
              }
            }
          }
          fieldValues(first: 20) {
            nodes {
              __typename
              ... on ProjectV2ItemFieldSingleSelectValue {
                name
                field { ... on ProjectV2SingleSelectField { name } }
              }
              ... on ProjectV2ItemFieldIterationValue { title }
            }
          }
        }
      }
    }
  }
}
"""


def project_items() -> dict[str, dict[str, Any]]:
    """Map issue node id -> project item id and the fields it already has."""
    items: dict[str, dict[str, Any]] = {}
    cursor = None
    while True:
        data = graphql(PROJECT_QUERY, {"cursor": cursor})
        page = data["organization"]["projectV2"]["items"]
        for node in page["nodes"]:
            content = node.get("content") or {}
            if content.get("__typename") != "Issue":
                continue
            fields: dict[str, str] = {}
            for value in node["fieldValues"]["nodes"]:
                if value.get("__typename") == "ProjectV2ItemFieldSingleSelectValue":
                    name = (value.get("field") or {}).get("name")
                    if name:
                        fields[name] = value.get("name")
            for value in (content.get("issueFieldValues") or {}).get("nodes", []):
                name = (value.get("field") or {}).get("name")
                if name:
                    fields[name] = value.get("name")
            items[content["id"]] = {
                "item_id": node["id"],
                "fields": fields,
                "number": content["number"],
                "title": content["title"],
                "repo": content["repository"]["name"],
            }
        if not page["pageInfo"]["hasNextPage"]:
            return items
        cursor = page["pageInfo"]["endCursor"]


def labels_of(issue: dict[str, Any]) -> set[str]:
    return {label["name"].lower() for label in issue["labels"]["nodes"]}


def is_epic(issue: dict[str, Any]) -> bool:
    if "epic" in labels_of(issue):
        return True
    if re.match(r"(?i)^\s*(\[epic\]|epic\b)", issue["title"]):
        return True
    return sub_issue_count(issue) > 0


def sub_issue_count(issue: dict[str, Any]) -> int:
    return (issue.get("subIssuesSummary") or {}).get("total", 0)


def issue_fields(issue: dict[str, Any]) -> dict[str, str]:
    fields: dict[str, str] = {}
    for value in (issue.get("issueFieldValues") or {}).get("nodes", []):
        name = (value.get("field") or {}).get("name")
        if name:
            fields[name] = value.get("name")
    return fields


def priority_for(issue: dict[str, Any]) -> str:
    match = re.search(r"\[\s*P([0-3])\s*\]", issue["title"], re.I)
    if match:
        return f"P{match.group(1)}"
    body = issue.get("body") or ""
    for pattern in (
        r"Severity:\s*\**P([0-3])\**",
        r"\**Priority[:\s*]*P([0-3])\**",
        r"deferred\s*\(P([0-3])\)",
        r"remains\s+P([0-3])",
    ):
        match = re.search(pattern, body, re.I)
        if match:
            return f"P{match.group(1)}"
    if re.search(r"\blow priority\b", body, re.I):
        return "P3"
    if "blocker" in labels_of(issue):
        return "P1"
    return "P2"


def team_for(issue: dict[str, Any], repo: str) -> str:
    title = issue["title"]
    body = (issue.get("body") or "")[:1200].lower()
    labels = labels_of(issue)

    if repo in {"ai-native-academia-2026", "chi-2026-memory-study"}:
        return "Research"
    if repo in {"fairtrade-design-system", "website"}:
        return "UI / UX"
    if repo == "village":
        if "frontend" in labels or re.search(
            r"\b(ui|dialog|page|view|nav|visual|share-card|screen|responsive)\b",
            title,
            re.I,
        ):
            return "UI / UX"
        return "Village"
    if repo == "schema":
        if re.search(r"\bvillage\b", title, re.I):
            return "Village"
        return "Data Pipelines"
    if repo == "peasant":
        if "frontend" in labels or re.search(
            r"\b(web|dashboard|tui|ui)\b", title, re.I
        ):
            return "UI / UX"
        return "Data Pipelines"
    if repo == "polyrepo":
        blob = f"{title.lower()} {body}"
        if re.search(r"fairtrade|\bui\b|design system", blob):
            return "UI / UX"
        if re.search(r"\bvillage\b", blob) and "peasant" not in blob:
            return "Village"
        if re.search(r"research|paper|study", blob):
            return "Research"
        return "Data Pipelines"
    return "Data Pipelines"


def size_for(issue: dict[str, Any]) -> str:
    return "XL" if sub_issue_count(issue) > 10 else "L"


def add_to_project(issue: dict[str, Any]) -> str:
    data = graphql(
        """
        mutation($project: ID!, $content: ID!) {
          addProjectV2ItemById(input: {projectId: $project, contentId: $content}) {
            item { id }
          }
        }
        """,
        {"project": PROJECT_ID, "content": issue["id"]},
    )
    return data["addProjectV2ItemById"]["item"]["id"]


def set_single(item_id: str, field_id: str, option_id: str) -> None:
    gh(
        [
            "project",
            "item-edit",
            "--project-id",
            PROJECT_ID,
            "--id",
            item_id,
            "--field-id",
            field_id,
            "--single-select-option-id",
            option_id,
        ]
    )


def set_priority(issue_id: str, priority: str) -> None:
    graphql(
        """
        mutation($issue: ID!, $field: ID!, $option: ID!) {
          setIssueFieldValue(
            input: {
              issueId: $issue
              issueFields: {fieldId: $field, singleSelectOptionId: $option}
            }
          ) { clientMutationId }
        }
        """,
        {
            "issue": issue_id,
            "field": PRIORITY_FIELD,
            "option": PRIORITY_OPTION[priority],
        },
    )


def apply_fields(
    issue: dict[str, Any], item_id: str, fields: dict[str, str], repo: str
) -> None:
    if not fields.get("Status"):
        set_single(item_id, STATUS_FIELD, STATUS_BACKLOG)
    if not fields.get("Size"):
        set_single(item_id, SIZE_FIELD, SIZE_OPTION[size_for(issue)])
    if not fields.get("Team"):
        set_single(item_id, TEAM_FIELD, TEAM_OPTION[team_for(issue, repo)])
    if not fields.get("Priority"):
        set_priority(issue["id"], priority_for(issue))


def main() -> int:
    if not os.environ.get("GH_TOKEN"):
        error("PROJECT_TOKEN is not configured; refusing to run without a token.")
        return 1

    log(f"scanning {ORG} for epics (dry_run={DRY_RUN})")
    board = project_items()
    log(f"project #{PROJECT_NUMBER} currently holds {len(board)} issue items")

    repos = list_repos()
    log(f"scanning {len(repos)} repositories: {', '.join(repos)}")

    added: list[str] = []
    repaired: list[str] = []
    planned_adds = 0
    planned_repairs = 0
    failures: list[str] = []

    for repo in repos:
        try:
            issues = list_open_issues(repo)
        except Exception as exc:  # noqa: BLE001
            warn(f"could not read issues from {repo}: {exc}")
            failures.append(repo)
            continue

        epics = [issue for issue in issues if is_epic(issue)]
        for issue in epics:
            key = f"{repo}#{issue['number']}"
            existing = board.get(issue["id"])
            if existing is None:
                if DRY_RUN:
                    planned_adds += 1
                    log(f"would add {key} ({issue['title']})")
                    continue
                try:
                    item_id = add_to_project(issue)
                    apply_fields(issue, item_id, issue_fields(issue), repo)
                    added.append(key)
                    log(f"added {key} ({issue['title']})")
                except Exception as exc:  # noqa: BLE001
                    warn(f"could not add {key}: {exc}")
                    failures.append(key)
                continue

            needed = not all(
                existing["fields"].get(field)
                for field in ("Status", "Size", "Team", "Priority")
            )
            if not needed:
                continue
            if DRY_RUN:
                planned_repairs += 1
                log(f"would repair fields on {key} ({issue['title']})")
                continue
            try:
                apply_fields(issue, existing["item_id"], existing["fields"], repo)
                repaired.append(key)
                log(f"repaired fields on {key} ({issue['title']})")
            except Exception as exc:  # noqa: BLE001
                warn(f"could not repair {key}: {exc}")
                failures.append(key)

    log(
        f"summary: {len(added)} added, {len(repaired)} repaired, "
        f"{planned_adds} planned adds, {planned_repairs} planned repairs, "
        f"{len(failures)} failures, dry_run={DRY_RUN}"
    )
    if failures:
        warn(f"incomplete sweep for: {', '.join(failures[:20])}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
