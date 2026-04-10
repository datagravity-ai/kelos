Cherry-pick new commits onto an existing `-prod` branch after upstream change requests, for the Anomalo fork contributing workflow.

## Instructions

1. Detect the current branch with `git branch --show-current`.

2. Determine the feature branch and its `-prod` counterpart:
   - If currently on a `-prod` branch, the feature branch is the name without the `-prod` suffix
   - If currently on a feature branch, the `-prod` branch is `<current-branch>-prod`
   - If the `-prod` branch doesn't exist locally, inform the user and suggest running `/kelos-cherry-pick-to-prod` first, then stop.

3. Identify new commits on the feature branch that are not on the `-prod` branch:
   ```
   git log --oneline <prod-branch>..<feature-branch>
   ```
   Show these commits to the user.

4. If there are no new commits, inform the user that the `-prod` branch is already up to date and stop.

5. If there are multiple new commits, ask the user:
   - **Squash first**: squash the new commits into one before cherry-picking
   - **Cherry-pick the range as-is**: cherry-pick all new commits individually

6. Switch to the `-prod` branch:
   ```
   git checkout <prod-branch>
   ```

7. Cherry-pick the new commits:
   - If squash: cherry-pick all and squash with `git reset --soft` to the previous HEAD and recommit
   - If range: cherry-pick each commit in order

8. If there are merge conflicts:
   - Show the conflicting files
   - Guide the user through resolving them
   - After resolution, continue with `git cherry-pick --continue`

9. Push the updated `-prod` branch:
   ```
   git push origin <prod-branch>
   ```

10. Check if there's an open PR for this branch:
    ```
    gh pr list --head <prod-branch> --base prod --state open
    ```
    - If a PR exists, show its URL
    - If no PR exists, ask the user if they want to open one (follow the `/kelos-open-prod-pr` workflow)
