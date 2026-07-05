# Contributing

Thanks for contributing to Open chat.

## Pull Request Basics

1. Create a branch from `dev`
2. Keep PRs focused and small
3. Run tests before opening PR:

```bash
go test ./...
```

## Commit Convention (Conventional Commits)

Use this format:

```text
type(scope): short summary
```

Examples:

- `feat(auth): add sqlite user storage support`
- `fix(websocket): restrict checkorigin to trusted origins`
- `docs(readme): improve setup and docker sections`
- `ci(codeql): add weekly security analysis workflow`

Recommended types:

- `feat` – new feature
- `fix` – bug fix
- `docs` – documentation only
- `refactor` – internal code change without behavior change
- `test` – tests
- `ci` – CI/CD changes
- `chore` – maintenance

## Style and Quality

- Follow existing project patterns
- Avoid unrelated changes in the same PR
- Update docs when behavior changes
- Keep security-sensitive changes explicit and reviewed
