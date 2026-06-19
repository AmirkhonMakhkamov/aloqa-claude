#!/usr/bin/env bash
# One-command backend release, mirroring the frontend's tooling/scripts/release.ts
# flow and the documented project Release Flow (develop -> main, merge commit,
# tag on main, sync develop back, GitHub release).
#
# The backend version is carried entirely by the git tag (vX.Y.Z) — it is
# injected into the binary via -ldflags at build time (see Makefile), so there
# is no package.json / VERSION file to bump. The tag IS the version.
#
# Usage:
#   scripts/release.sh <patch|minor|major|X.Y.Z> [--dry-run] [--skip-checks]
#
# Examples:
#   scripts/release.sh minor            # v0.22.0 -> v0.23.0
#   scripts/release.sh patch            # v0.23.0 -> v0.23.1
#   scripts/release.sh 1.0.0            # explicit version
#   scripts/release.sh minor --dry-run  # print the plan, touch nothing
#
# Preconditions:
#   - gh authenticated, origin reachable.
#   - develop is the integration branch and is a strict descendant of main.
#   - Run from a clean tree on `develop` (the script cuts a release/vX.Y.Z
#     branch) or re-run from an existing `release/vX.Y.Z` branch to resume.
set -euo pipefail

REMOTE="${REMOTE:-origin}"
MAIN_BRANCH="${MAIN_BRANCH:-main}"
DEVELOP_BRANCH="${DEVELOP_BRANCH:-develop}"
DRY_RUN=0
SKIP_CHECKS=0
BUMP=""

log() { printf '\n=== %s ===\n' "$*" >&2; }
die() { printf 'release: %s\n' "$*" >&2; exit 1; }
run() { if [[ "${DRY_RUN}" == "1" ]]; then printf '[dry-run] %s\n' "$*" >&2; else eval "$*"; fi; }

for arg in "$@"; do
	case "${arg}" in
		--dry-run) DRY_RUN=1 ;;
		--skip-checks) SKIP_CHECKS=1 ;;
		--*) die "unknown flag: ${arg}" ;;
		*) [[ -n "${BUMP}" ]] && die "unexpected extra arg: ${arg}"; BUMP="${arg}" ;;
	esac
done
[[ -n "${BUMP}" ]] || die "missing bump: <patch|minor|major|X.Y.Z>"

# ---------- compute next version ----------
git fetch --quiet "${REMOTE}" --tags
prev_tag="$(git tag --list 'v*' --sort=-v:refname | head -1)"
prev_version="${prev_tag#v}"
[[ -n "${prev_version}" ]] || prev_version="0.0.0"
IFS='.' read -r major minor patch <<<"${prev_version}"

if [[ "${BUMP}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
	new_version="${BUMP}"
else
	case "${BUMP}" in
		major) new_version="$((major + 1)).0.0" ;;
		minor) new_version="${major}.$((minor + 1)).0" ;;
		patch) new_version="${major}.${minor}.$((patch + 1))" ;;
		*) die "invalid bump \"${BUMP}\" — expected patch/minor/major or X.Y.Z" ;;
	esac
fi
new_tag="v${new_version}"
release_branch="release/${new_tag}"

git rev-parse -q --verify "refs/tags/${new_tag}" >/dev/null 2>&1 && die "tag ${new_tag} already exists locally"
git ls-remote --exit-code --tags "${REMOTE}" "${new_tag}" >/dev/null 2>&1 && die "${new_tag} already released on ${REMOTE}"

log "release ${prev_tag:-<none>} -> ${new_tag} (dryRun=${DRY_RUN})"

# ---------- preflight ----------
[[ -z "$(git status --porcelain)" ]] || die "working tree is dirty — commit, stash, or abort"
git merge-base --is-ancestor "${REMOTE}/${MAIN_BRANCH}" "${REMOTE}/${DEVELOP_BRANCH}" \
	|| die "${REMOTE}/${MAIN_BRANCH} has commits not on ${REMOTE}/${DEVELOP_BRANCH} — sync first"

current_branch="$(git rev-parse --abbrev-ref HEAD)"
if [[ "${current_branch}" != "${release_branch}" ]]; then
	log "cutting ${release_branch} from ${REMOTE}/${DEVELOP_BRANCH}"
	run "git checkout -B '${release_branch}' '${REMOTE}/${DEVELOP_BRANCH}'"
fi

# ---------- gate: build + vet + test ----------
if [[ "${SKIP_CHECKS}" == "1" ]]; then
	log "skipping checks (--skip-checks)"
else
	log "build + vet + test"
	run "go build ./..."
	run "go vet ./..."
	run "go test ./..."
fi

# ---------- open PR -> main ----------
log "opening PR ${release_branch} -> ${MAIN_BRANCH}"
run "git push -u '${REMOTE}' '${release_branch}'"
pr_url=""
if [[ "${DRY_RUN}" != "1" ]]; then
	if ! pr_url="$(gh pr list --head "${release_branch}" --state all --json url -q '.[0].url' 2>/dev/null)" || [[ -z "${pr_url}" ]]; then
		pr_url="$(gh pr create --base "${MAIN_BRANCH}" --head "${release_branch}" \
			--title "release: ${new_tag}" \
			--body "Release ${new_tag}. Cut from ${DEVELOP_BRANCH} via scripts/release.sh." )"
	fi
	echo "  PR: ${pr_url}"
fi

# ---------- merge (merge commit, preserves history) ----------
log "merging PR with a merge commit"
run "gh pr merge '${release_branch}' --merge --delete-branch=false"

# ---------- tag main + sync develop ----------
log "tagging ${new_tag} on ${MAIN_BRANCH} and syncing ${DEVELOP_BRANCH}"
run "git checkout '${MAIN_BRANCH}'"
run "git pull --ff-only '${REMOTE}' '${MAIN_BRANCH}'"
run "git tag -a '${new_tag}' -m 'Release ${new_tag}'"
run "git push '${REMOTE}' '${MAIN_BRANCH}' --follow-tags"
run "git checkout '${DEVELOP_BRANCH}'"
run "git pull --ff-only '${REMOTE}' '${DEVELOP_BRANCH}'"
run "git merge --no-edit '${MAIN_BRANCH}'"
run "git push '${REMOTE}' '${DEVELOP_BRANCH}'"

# ---------- GitHub release ----------
log "publishing GitHub release"
notes_start=""
[[ -n "${prev_tag}" ]] && notes_start="--notes-start-tag '${prev_tag}'"
run "gh release create '${new_tag}' --target '${MAIN_BRANCH}' --title '${new_tag}' --generate-notes ${notes_start}"

log "release ${new_tag} done"
