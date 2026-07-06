# Open chat

![GitHub Actions Workflow Status](https://img.shields.io/github/actions/workflow/status/Schenna43lp1/openchat/.github%2Fworkflows%2Fdocker-image.yml)
![CodeQL](https://img.shields.io/github/actions/workflow/status/Schenna43lp1/openchat/codeql.yml?label=codeql)

Ein leichtgewichtiges Echtzeit-Chat-System mit:

- Go (HTTP-Server + Auth + Rollenverwaltung)
- Gorilla WebSocket (Live-Nachrichten)
- responsivem Frontend (Chat, Login, Admin-Bereich)
- JSON **oder** SQLite als User-Storage
- öffentlichen Nachrichten und Direktnachrichten

## Schnellstart

```bash
go mod tidy
go run .
```

Server:

```text
http://localhost:8080
```

## Konfiguration

### User-Storage (JSON/SQLite)

Default:

- `data/users.json`

Override:

```powershell
set OPENCHAT_USERS_FILE=data\users.sqlite
go run .
```

Dateiendungen:

- `.json` -> JSON
- `.db`, `.sqlite`, `.sqlite3` -> SQLite

### WebSocket Origin-Whitelist

Standard: gleiche Host-Origin wie der Request (`Origin` muss zu `Host` passen).

Optional zusätzliche erlaubte Origins:

```powershell
set OPENCHAT_ALLOWED_ORIGINS=https://chat.example.com,https://app.example.com
go run .
```

## Rollen

- `admin`: Rollen ändern + sperren/entsperren
- `moderator`: sperren/entsperren (nur Nutzer mit Rolle `user`)
- `user`: Chat-Nutzung

Regeln:

- letzter aktiver Admin kann nicht entfernt/gesperrt werden
- gesperrte Nutzer können sich nicht anmelden

## Direktnachrichten

Im Chat-Composer kannst du den Empfänger wählen:

- `Alle (öffentlicher Chat)` für normale Nachrichten
- `Direkt an <Benutzer>` für private Nachricht

Direktnachrichten werden nur an Sender und Empfänger zugestellt.

## Endpunkte

- `GET /` - Chat-Frontend (auth required)
- `GET /ws` - WebSocket (auth required)
- `GET|POST /admin/users` - Benutzerverwaltung (staff required)
- `GET|POST /login` - Login/Registrierung
- `POST /logout` - Logout
- `GET /static/*` - statische Assets

## Docker

Stable:

```bash
docker run -d --name openchat -p 8080:8080 ghcr.io/schenna43lp1/openchat:latest
```

Dev:

```bash
docker run -d --name openchat-dev -p 8080:8080 ghcr.io/schenna43lp1/openchat:dev
```

## CI/CD & Security

- `CI Test`: `.github/workflows/ci-test.yml`
- `CodeQL`: `.github/workflows/codeql.yml`
- Docker Build/Publish: `.github/workflows/docker-image.yml`
- Dependabot: `.github/dependabot.yml`
- PR Labeler: `.github/workflows/pr-labeler.yml` + `.github/labeler.yml`

## Dokumentation

- Endnutzer Quickstart: `docs/QUICKSTART.md`
- Entwickler-Doku (Flows/Sequenzen): `docs/DEVELOPER_GUIDE.md`
- Vollständige Projektdoku: `docs/DOKUMENTATION.md`
- Security Policy: `SECURITY.md`
- Contribution/Commit-Konvention: `CONTRIBUTING.md`

## Tests

```bash
go test ./...
```
