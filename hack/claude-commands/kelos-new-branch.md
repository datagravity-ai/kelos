Create a new feature branch from `main` for the Anomalo fork contributing workflow.

## Instructions

1. Check for uncommitted changes with `git status`. If there are any, warn the user and ask whether to stash them first with `git stash`.

2. Fetch the latest `origin/main`:
   ```
   git fetch origin main
   ```

3. Determine the branch name:
   - If `$ARGUMENTS` is provided, use it as the branch name
   - Otherwise, ask the user for a branch name

4. The full branch name should be `feature/<name>` (prepend `feature/` if the user didn't include it).

5. Check if the branch already exists locally or remotely:
   - If it exists, ask the user whether to switch to it or pick a different name
   - If it doesn't exist, create it from `origin/main`:
     ```
     git checkout -b feature/<name> origin/main
     ```

6. Confirm to the user: show the new branch name and that it was created from `origin/main`.
