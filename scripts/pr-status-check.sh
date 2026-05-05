#!/usr/bin/env bash
# pr-status-check.sh — Fetch CI checks, CodeRabbit comments with resolution
# status for a PR. Designed for AI agent consumption: structured, concise,
# low-token output.
#
# Usage:
#   bash scripts/pr-status-check.sh [PR_NUMBER]
#   bash scripts/pr-status-check.sh              # auto-detects from current branch
#
# Requires: gh (GitHub CLI), jq

set -euo pipefail

REPO_OWNER="good-native"
REPO_NAME="hover"
REPO="$REPO_OWNER/$REPO_NAME"

# --- Resolve PR number ---
if [ "${1:-}" != "" ]; then
  PR_NUMBER="$1"
else
  CURRENT_BRANCH=$(git rev-parse --abbrev-ref HEAD 2>/dev/null || true)
  PR_NUMBER=$(gh pr view "$CURRENT_BRANCH" --repo "$REPO" --json number --jq '.number' 2>/dev/null || true)
  if [ -z "$PR_NUMBER" ]; then
    echo "ERROR: No PR number provided and none found for current branch." >&2
    echo "Usage: bash scripts/pr-status-check.sh [PR_NUMBER]" >&2
    exit 1
  fi
fi

echo "=== PR #${PR_NUMBER} STATUS ==="
echo ""

# --- Section 1: CI Checks ---
echo "## CI CHECKS"
echo ""

CHECKS_JSON=$(gh pr view "$PR_NUMBER" --repo "$REPO" \
  --json statusCheckRollup \
  --jq '.statusCheckRollup' 2>/dev/null || echo "[]")

# Display check summary table
echo "$CHECKS_JSON" | jq -r '
  .[]
  | select(.name != null)
  | (
      if .status == "COMPLETED" then
        if .conclusion == "SUCCESS" then "PASS"
        elif .conclusion == "SKIPPED" then "SKIP"
        elif .conclusion == "NEUTRAL" then "SKIP"
        else "FAIL"
        end
      elif .status == "IN_PROGRESS" or .status == "QUEUED" then "RUNNING"
      else "PENDING"
      end
    ) as $st
  | "\($st)\t\(.name)"' | sort || echo "(no checks found)"

# Pending required checks (null-name entries = expected but not yet reported)
PENDING_COUNT=$(echo "$CHECKS_JSON" | jq '[.[] | select(.name == null)] | length')
if [ "$PENDING_COUNT" -gt 0 ]; then
  echo "PENDING	($PENDING_COUNT required checks waiting for status)"
fi

# Show details for failed checks
FAILED_CHECKS=$(echo "$CHECKS_JSON" | jq -r '
  .[]
  | select(.name != null and .status == "COMPLETED" and .conclusion != "SUCCESS" and .conclusion != "SKIPPED" and .conclusion != "NEUTRAL")
  | "\(.name)\t\(.conclusion)\t\(.detailsUrl // "")"')

if [ -n "$FAILED_CHECKS" ]; then
  echo ""
  echo "## FAILED CHECK DETAILS"
  echo ""

  # Get the PR branch for log lookup
  PR_BRANCH=$(gh pr view "$PR_NUMBER" --repo "$REPO" --json headRefName --jq '.headRefName' 2>/dev/null || true)

  echo "$FAILED_CHECKS" | while IFS=$'\t' read -r CHECK_NAME CONCLUSION DETAILS_URL; do
    echo "--- $CHECK_NAME ($CONCLUSION)"
    [ -n "$DETAILS_URL" ] && echo "URL: $DETAILS_URL"

    # Try to fetch failed run logs (last 30 lines)
    if [ -n "$PR_BRANCH" ]; then
      RUN_ID=$(gh run list --repo "$REPO" --branch "$PR_BRANCH" --workflow "$CHECK_NAME" \
        --status failure --json databaseId --jq '.[0].databaseId' 2>/dev/null || true)

      if [ -n "$RUN_ID" ] && [ "$RUN_ID" != "null" ]; then
        echo "LOG (last 30 lines):"
        gh run view "$RUN_ID" --repo "$REPO" --log-failed 2>/dev/null | tail -30 || echo "  (could not fetch logs)"
      fi
    fi
    echo ""
  done
fi

echo ""

# --- Section 2: CodeRabbit Comments (via GraphQL for resolution status) ---
echo "## CODERABBIT REVIEW COMMENTS"
echo ""

THREADS_JSON=$(gh api graphql -f query='
{
  repository(owner: "'"$REPO_OWNER"'", name: "'"$REPO_NAME"'") {
    pullRequest(number: '"$PR_NUMBER"') {
      reviewThreads(first: 100) {
        nodes {
          isResolved
          isOutdated
          comments(first: 1) {
            nodes {
              author { login }
              path
              line
              originalLine
              body
            }
          }
        }
      }
    }
  }
}' --jq '.data.repository.pullRequest.reviewThreads.nodes' 2>/dev/null || echo "[]")

# Filter to CodeRabbit threads only
CR_THREADS=$(echo "$THREADS_JSON" | jq '
  [.[] | select(.comments.nodes[0].author.login == "coderabbitai") |
    {
      resolved: .isResolved,
      outdated: .isOutdated,
      path: .comments.nodes[0].path,
      line: (.comments.nodes[0].line // .comments.nodes[0].originalLine // null),
      body: .comments.nodes[0].body
    }
  ]
')

THREAD_COUNT=$(echo "$CR_THREADS" | jq 'length')
OPEN_COUNT=$(echo "$CR_THREADS" | jq '[.[] | select(.resolved == false)] | length')
RESOLVED_COUNT=$(echo "$CR_THREADS" | jq '[.[] | select(.resolved == true)] | length')

if [ "$THREAD_COUNT" -eq 0 ]; then
  echo "(no CodeRabbit review threads found)"
else
  echo "Threads: $THREAD_COUNT total ($OPEN_COUNT open, $RESOLVED_COUNT resolved)"
  echo ""

  # Show open threads first, then resolved
  echo "$CR_THREADS" | jq -r '
    sort_by(.resolved) | .[] |

    # Status label
    (if .resolved then "RESOLVED" elif .outdated then "OUTDATED" else "OPEN" end) as $status |

    # Severity from body prefix
    (
      if (.body | test("🔴 Critical"; "i")) then "CRITICAL"
      elif (.body | test("🟠 Major"; "i")) then "MAJOR"
      elif (.body | test("🟡 Minor"; "i")) then "MINOR"
      elif (.body | test("💡"; "i")) then "SUGGESTION"
      else "INFO"
      end
    ) as $severity |

    # Extract actionable summary: strip details blocks and severity prefix
    (.body
      | gsub("<details>[\\s\\S]*?</details>"; ""; "m")
      | gsub("_⚠️[^_]*_[^\\n]*\\n"; "")
      | gsub("_💡[^_]*_[^\\n]*\\n"; "")
      | gsub("\\n{2,}"; "\n")
      | ltrimstr("\n")
      | split("\n")
      | map(select(length > 0))
      | first // "(no summary)"
    ) as $summary |

    "---"
    + "\nSTATUS: \($status)"
    + "\nFILE: \(.path)"
    + "\nLINE: \(.line // "N/A")"
    + "\nSEVERITY: \($severity)"
    + "\nSUMMARY: \($summary)"
  '
fi

echo ""

# --- Section 3: Agent prompt from latest CodeRabbit review ---
echo "## CODERABBIT AGENT PROMPT"
echo ""

LATEST_REVIEW_ID=$(gh api "repos/$REPO/pulls/$PR_NUMBER/reviews" \
  --jq '[.[] | select(.user.login == "coderabbitai[bot]") | .id] | last // empty' 2>/dev/null || true)

if [ -n "$LATEST_REVIEW_ID" ]; then
  REVIEW_BODY=$(gh api "repos/$REPO/pulls/$PR_NUMBER/reviews/$LATEST_REVIEW_ID" \
    --jq '.body' 2>/dev/null || echo "")

  if [ -n "$REVIEW_BODY" ]; then
    # Extract the agent-friendly prompt code block
    AGENT_PROMPT=$(echo "$REVIEW_BODY" | sed -n '/^```$/,/^```$/{ /^```$/d; p; }' | head -40)

    if [ -n "$AGENT_PROMPT" ]; then
      echo "$AGENT_PROMPT"
    else
      echo "$REVIEW_BODY" | head -3
    fi
  else
    echo "(no review summary)"
  fi
else
  echo "(no CodeRabbit reviews found)"
fi

echo ""
echo "=== END ==="
