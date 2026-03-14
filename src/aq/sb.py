"""sb — worktree context detection."""
from __future__ import annotations
import subprocess
from pathlib import Path
from dataclasses import dataclass


@dataclass
class Sandbox:
    branch: str
    remote: str
    worktree_path: Path
    is_linked_worktree: bool
    agent_address: str

    @classmethod
    def detect(cls, cwd: Path | None = None) -> "Sandbox":
        root = str(cwd or Path.cwd())

        def git(*args: str) -> str:
            result = subprocess.run(
                ["git", "-C", root, *args],
                capture_output=True, text=True
            )
            return result.stdout.strip()

        branch = git("rev-parse", "--abbrev-ref", "HEAD") or "unknown"
        remote_url = git("remote", "get-url", "origin")
        common_dir = git("rev-parse", "--git-common-dir")
        git_dir = git("rev-parse", "--git-dir")

        is_linked = (
            bool(common_dir) and bool(git_dir) and
            common_dir != git_dir
        )

        remote = (
            remote_url
            .replace("git@github.com:", "github.com/")
            .replace("https://github.com/", "github.com/")
            .removesuffix(".git")
        ) if remote_url else "local"

        agent_address = (
            f"{remote}/worktrees/{branch}" if is_linked
            else f"{remote}/{branch}"
        )

        return cls(
            branch=branch,
            remote=remote,
            worktree_path=Path(root),
            is_linked_worktree=is_linked,
            agent_address=agent_address,
        )
