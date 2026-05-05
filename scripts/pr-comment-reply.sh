#!/usr/bin/env bash
# pr-comment-reply.sh — Reply to and optionally resolve PR review threads.
# Designed for AI agents and developers to respond to CodeRabbit/reviewer
# comments directly from the terminal.
#
# Usage:
#   bash scripts/pr-comment-reply.sh [PR_NUMBER] --list
#   bash scripts/pr-comment-reply.sh [PR_NUMBER] --reply <THREAD_INDEX> "message"
#   bash scripts/pr-comment-reply.sh [PR_NUMBER] --reply <THREAD_INDEX> "message" --resolve
#   bash scripts/pr-comment-reply.sh [PR_NUMBER] --resolve <THREAD_INDEX>
#
# THREAD_INDEX is the 1-based number from --list output.
# PR_NUMBER is optional — auto-detects from current branch.
#
# Requires: gh (GitHub CLI), jq

set -euo pipefail

REPO_OWNER="good-native"
REPO_NAME="hover"
REPO="$REPO_OWNER/$REPO_NAME"

# --- Parse arguments ---
PR_NUMBER=""
ACTION=""
THREAD_INDEX=""
MESSAGE=""
RESOLVE=false

while [ $# -gt 0 ]; do
  case "$1" in
    --list)
      ACTION="list"
      shift
      ;;
    --reply)
      ACTION="reply"
      shift
      THREAD_INDEX="$1"
      shift
      MESSAGE="$1"
      shift
      ;;
    --resolve)
      if [ "$ACTION" = "reply" ]; then
        RESOLVE=true
      else
        ACTION="resolve"
        shift
        THREAD_INDEX="$1"
      fi
      shift
      ;;
    --help|-h)
      echo "Usage:"
      echo "  bash scripts/pr-comment-reply.sh [PR] --list"
      echo "  bash scripts/pr-comment-reply.sh [PR] --reply INDEX \"message\""
      echo "  bash scripts/pr-comment-reply.sh [PR] --reply INDEX \"message\" --resolve"
      echo "  bash scripts/pr-comment-reply.sh [PR] --resolve INDEX"
      exit 0
      ;;
    *)
      if [ -z "$PR_NUMBER" ] && [[ "$1" =~ ^[0-9]+$ ]]; then
        PR_NUMBER="$1"
      else
        echo "ERROR: Unknown argument: $1" >&2
        exit 1
      fi
      shift
      ;;
  esac
done

# --- Resolve PR number ---
if [ -z "$PR_NUMBER" ]; then
  CURRENT_BRANCH=$(git rev-parse --abbrev-ref HEAD 2>/dev/null || true)
  PR_NUMBER=$(gh pr view "$CURRENT_BRANCH" --repo "$REPO" --json number --jq '.number' 2>/dev/null || true)
  if [ -z "$PR_NUMBER" ]; then
    echo "ERROR: No PR number provided and none found for current branch." >&2
    exit 1
  fi
fi

if [ -z "$ACTION" ]; then
  echo "ERROR: No action specified. Use --list, --reply, or --resolve." >&2
  echo "Run with --help for usage." >&2
  exit 1
fi

# --- Fetch all review threads ---
fetch_threads() {
  gh api graphql -f query='
  {
    repository(owner: "'"$REPO_OWNER"'", name: "'"$REPO_NAME"'") {
      pullRequest(number: '"$PR_NUMBER"') {
        reviewThreads(first: 100) {
          nodes {
            id
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
  }' --jq '.data.repository.pullRequest.reviewThreads.nodes' 2>/dev/null || echo "[]"
}

THREADS_JSON=$(fetch_threads)

# Filter to open threads only for actions (show all for list)
OPEN_THREADS=$(echo "$THREADS_JSON" | jq '[.[] | select(.isResolved == false)]')

# --- List ---
if [ "$ACTION" = "list" ]; then
  TOTAL=$(echo "$THREADS_JSON" | jq 'length')
  OPEN_COUNT=$(echo "$OPEN_THREADS" | jq 'length')

  echo "=== PR #${PR_NUMBER} OPEN REVIEW THREADS ($OPEN_COUNT of $TOTAL) ==="
  echo ""

  if [ "$OPEN_COUNT" -eq 0 ]; then
    echo "(no open threads)"
    exit 0
  fi

  echo "$OPEN_THREADS" | jq -r '
    to_entries[] |
    .key as $i |
    .value |
    (.comments.nodes[0]) as $c |

    (
      if ($c.body | test("🔴 Critical"; "i")) then "CRITICAL"
      elif ($c.body | test("🟠 Major"; "i")) then "MAJOR"
      elif ($c.body | test("🟡 Minor"; "i")) then "MINOR"
      elif ($c.body | test("💡"; "i")) then "SUGGESTION"
      else "INFO"
      end
    ) as $severity |

    ($c.body
      | gsub("<details>[\\s\\S]*?</details>"; ""; "m")
      | gsub("_⚠️[^_]*_[^\\n]*\\n"; "")
      | gsub("_💡[^_]*_[^\\n]*\\n"; "")
      | gsub("\\n{2,}"; "\n")
      | ltrimstr("\n")
      | split("\n")
      | map(select(length > 0))
      | first // "(no summary)"
    ) as $summary |

    "\($i + 1)\t\($severity)\t\($c.path):\($c.line // $c.originalLine // "?")\t\($summary)"
  '

  echo ""
  echo "Reply:   bash scripts/pr-comment-reply.sh --reply INDEX \"message\""
  echo "Resolve: bash scripts/pr-comment-reply.sh --resolve INDEX"
  echo "Both:    bash scripts/pr-comment-reply.sh --reply INDEX \"message\" --resolve"
  exit 0
fi

# --- Get thread ID by index ---
if [ -z "$THREAD_INDEX" ]; then
  echo "ERROR: Thread index required." >&2
  exit 1
fi

THREAD_ID=$(echo "$OPEN_THREADS" | jq -r ".[$((THREAD_INDEX - 1))].id // empty")
if [ -z "$THREAD_ID" ]; then
  echo "ERROR: Thread index $THREAD_INDEX not found. Run --list to see available threads." >&2
  exit 1
fi

THREAD_FILE=$(echo "$OPEN_THREADS" | jq -r ".[$((THREAD_INDEX - 1))].comments.nodes[0].path")
THREAD_LINE=$(echo "$OPEN_THREADS" | jq -r ".[$((THREAD_INDEX - 1))].comments.nodes[0].line // .[$((THREAD_INDEX - 1))].comments.nodes[0].originalLine // \"?\"")

# --- Reply ---
if [ "$ACTION" = "reply" ]; then
  if [ -z "$MESSAGE" ]; then
    echo "ERROR: Reply message required." >&2
    exit 1
  fi

  RESULT=$(gh api graphql \
    -f query='
      mutation($threadId: ID!, $body: String!) {
        addPullRequestReviewThreadReply(input: {
          pullRequestReviewThreadId: $threadId,
          body: $body
        }) {
          comment { id }
        }
      }' \
    -f threadId="$THREAD_ID" \
    -f body="$MESSAGE" 2>&1)

  if echo "$RESULT" | jq -e '.data.addPullRequestReviewThreadReply.comment.id' >/dev/null 2>&1; then
    echo "Replied to thread #$THREAD_INDEX ($THREAD_FILE:$THREAD_LINE)"
  else
    echo "ERROR: Failed to reply." >&2
    echo "$RESULT" >&2
    exit 1
  fi
fi

# --- Resolve ---
if [ "$ACTION" = "resolve" ] || [ "$RESOLVE" = true ]; then
  RESULT=$(gh api graphql -f query='
    mutation {
      resolveReviewThread(input: {
        threadId: "'"$THREAD_ID"'"
      }) {
        thread { id isResolved }
      }
    }' 2>&1)

  if echo "$RESULT" | jq -e '.data.resolveReviewThread.thread.isResolved' >/dev/null 2>&1; then
    echo "Resolved thread #$THREAD_INDEX ($THREAD_FILE:$THREAD_LINE)"
  else
    echo "ERROR: Failed to resolve." >&2
    echo "$RESULT" >&2
    exit 1
  fi
fi
