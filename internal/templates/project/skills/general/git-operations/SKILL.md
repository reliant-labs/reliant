---
name: git-operations
description: Git operations methodology — conventional commits, branching strategies, conflict resolution, and version control best practices
---

# Git Operations

## Conventional Commits

Always use conventional commit format unless the project uses a different standard:

```
<type>(<scope>): <subject>

[optional body]

[optional footer]
```

### Types
| Type | When to Use |
|------|-------------|
| `feat` | New feature for the user |
| `fix` | Bug fix for the user |
| `docs` | Documentation only changes |
| `style` | Formatting, missing semicolons (no code change) |
| `refactor` | Code change that neither fixes a bug nor adds a feature |
| `perf` | Performance improvement |
| `test` | Adding or correcting tests |
| `chore` | Maintenance tasks, dependencies, build changes |
| `ci` | CI/CD configuration changes |
| `revert` | Reverting a previous commit |

### Scope
Optional, describes the section of codebase:
- `feat(auth):` - authentication feature
- `fix(api):` - API bug fix
- `docs(readme):` - README update

### Subject Rules
- Imperative mood: "add" not "added" or "adds"
- No period at the end
- Max 50 characters
- Lowercase

### Body Rules
- Explain WHAT and WHY, not HOW (code shows how)
- Wrap at 72 characters
- Separate from subject with blank line

### Examples
```
feat(auth): add OAuth2 login support

Users can now authenticate via Google and GitHub OAuth providers.
This reduces friction for new user signups.

Closes #123
```

```
fix(api): handle null response from payment provider

The Stripe API occasionally returns null for pending transactions.
Added defensive check to prevent crash in checkout flow.
```

```
refactor(db): extract query builders into separate module

Improves testability and reduces duplication across repositories.
No functional changes.
```

## Commit Best Practices

### Atomic Commits
Each commit should be one logical change:
- One commit per feature/fix
- Tests included with the code they test
- No "Fix multiple things" commits
- No WIP commits in main history

### Before Committing
```bash
# Review what you're committing
git diff --staged

# Check for accidentally staged files
git status

# Ensure tests pass
npm test  # or equivalent
```

### Amending (with caution)
```bash
# Only amend unpushed commits
git commit --amend

# Add forgotten file to last commit
git add forgotten-file.ts
git commit --amend --no-edit
```

## Branching Strategies

### Feature Branches
```bash
# Create feature branch from main
git checkout main
git pull origin main
git checkout -b feat/user-authentication

# Keep up to date with main
git fetch origin
git rebase origin/main
```

### Branch Naming
| Pattern | Use Case |
|---------|----------|
| `feat/description` | New features |
| `fix/description` | Bug fixes |
| `refactor/description` | Code improvements |
| `docs/description` | Documentation |
| `chore/description` | Maintenance |

### Rebase vs Merge
**Prefer rebase** for:
- Updating feature branch with main
- Keeping linear history
- Before PR/merge

**Use merge** for:
- Integrating feature into main
- Preserving branch history when meaningful
- When rebase would rewrite shared history

## Common Operations

### Interactive Rebase (cleaning history)
```bash
# Squash last 3 commits
git rebase -i HEAD~3

# In editor: change 'pick' to 'squash' or 's' for commits to combine
```

### Cherry-Pick
```bash
# Apply specific commit to current branch
git cherry-pick <commit-sha>

# Cherry-pick without committing (stage only)
git cherry-pick -n <commit-sha>
```

### Bisect (finding bug introduction)
```bash
git bisect start
git bisect bad                 # Current commit is bad
git bisect good <known-good>   # Known working commit

# Git checks out middle commit, test it, then:
git bisect good  # or
git bisect bad

# Repeat while not found, then:
git bisect reset
```

### Reflog (recovery)
```bash
# See recent HEAD history
git reflog

# Recover lost commit
git checkout <sha-from-reflog>
git branch recovered-work
```

### Stash
```bash
# Save work in progress
git stash push -m "WIP: feature description"

# List stashes
git stash list

# Apply and keep stash
git stash apply stash@{0}

# Apply and remove stash
git stash pop
```

## Conflict Detection & Delegation

### Conflict Resolution

When you encounter merge or rebase conflicts, load the `conflict-resolver` skill for a comprehensive resolution framework:
```
skill load conflict-resolver
```

For trivial conflicts (import ordering, lock files, non-overlapping additions), resolve directly:
```bash
# Regenerate lock files instead of merging
rm package-lock.json
npm install
git add package-lock.json
```

## Safety Rules

### NEVER (without explicit user request):
- Force push to main/master
- Rebase shared/pushed branches
- Delete remote branches
- Reset --hard without backup
- Amend pushed commits

### ALWAYS:
- Create backup branch before risky operations
- Verify current branch before operations
- Use `--dry-run` when available
- Confirm destructive operations with user

```bash
# Backup before risky operation
git branch backup-$(date +%Y%m%d-%H%M%S)
```

## Project Convention Detection

Before making commits, check for project-specific conventions:

```bash
# Check for commitlint config
cat commitlint.config.js 2>/dev/null
cat .commitlintrc* 2>/dev/null

# Check for commit message template
cat .gitmessage 2>/dev/null

# Check recent commit history for patterns
git log --oneline -20

# Check for husky/hooks
ls -la .husky/ 2>/dev/null
cat .git/hooks/commit-msg 2>/dev/null
```

Adapt to project conventions when they differ from conventional commits.
