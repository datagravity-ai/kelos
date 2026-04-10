Cherry-pick commits from the current feature branch onto a new `-prod` branch for the Anomalo fork contributing workflow.

## Instructions

1. Detect the current branch name with `git branch --show-current`.
   - If the current branch ends with `-prod`, warn the user that they appear to already be on a prod branch and ask if they want to continue.

2. Identify the commits on this branch that are not on `main`:
   ```
   git log --oneline origin/main..HEAD
   ```
   Show these commits to the user.

3. If there are no commits, inform the user and stop.

4. If there are multiple commits, ask the user:
   - **Squash first**: squash all commits into a single commit before cherry-picking
   - **Cherry-pick the range as-is**: cherry-pick all commits individually

5. Fetch the latest `origin/prod`:
   ```
   git fetch origin prod
   ```

6. Determine the `-prod` branch name: `<current-branch>-prod`.
   - If this branch already exists locally, ask the user whether to delete and recreate it, or pick a different name.

7. Create the `-prod` branch from `origin/prod`:
   ```
   git checkout -b <current-branch>-prod origin/prod
   ```

8. Cherry-pick the commits:
   - If the user chose to squash: first go back to the feature branch, run `git rebase -i` is not available so instead create a squashed commit. Use `git diff origin/main...<feature-branch> | git apply` on the prod branch and create a single commit with a summary message. Alternatively, cherry-pick all and then squash with `git reset --soft` to the branch point and recommit.
   - If the user chose range: cherry-pick each commit in order:
     ```
     git cherry-pick <first-commit>^..<last-commit>
     ```

9. If there are merge conflicts during cherry-pick:
   - Show the conflicting files
   - Guide the user through resolving them
   - After resolution, continue with `git cherry-pick --continue`

10. Confirm to the user: show the `-prod` branch name, the cherry-picked commits, and suggest running `/kelos-open-prod-pr` next.
