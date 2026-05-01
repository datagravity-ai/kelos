Push the current `-prod` branch and open a PR against `prod` for the Anomalo fork contributing workflow.

## Instructions

1. Detect the current branch with `git branch --show-current`.
   - If the branch does not end with `-prod`, warn the user and ask if they want to continue anyway.

2. Push the branch to origin:
   ```
   git push -u origin <current-branch>
   ```

3. Auto-detect the `/kind` label from the branch name prefix:
   - `feature/` -> `/kind feature`
   - `fix/` or `bugfix/` -> `/kind bug`
   - `docs/` -> `/kind docs`
   - Anything else -> `/kind cleanup`
   Show the detected kind to the user and ask them to confirm or change it.

4. Gather PR information by asking the user:
   - **PR title**: suggest one based on the branch name and commit messages
   - **What this PR does / why we need it**: draft a summary from the commit messages, let the user review
   - **Related issue(s)**: ask for an issue number, default to "N/A"
   - **Special notes for reviewer**: ask, default to empty
   - **User-facing change**: ask if this introduces a user-facing change. If no, use "NONE" in the release-note block. If yes, ask for the release note text.

5. Create the PR using `gh pr create`. The body must follow the template from `.github/PULL_REQUEST_TEMPLATE.md`:

   `````
   gh pr create --base prod --title "<title>" --body "$(cat <<'EOF'
   #### What type of PR is this?

   /kind <detected-kind>

   #### What this PR does / why we need it:

   <summary>

   #### Which issue(s) this PR is related to:

   <issue or N/A>

   #### Special notes for your reviewer:

   <notes or empty>

   #### Does this PR introduce a user-facing change?

   ```release-note
   <release note or NONE>
   ```
   EOF
   )"
   `````

6. Show the PR URL to the user.
