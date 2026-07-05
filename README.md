# Open chat

Ein leichtgewichtiges Echtzeit-Chat-System mit:

- Go (HTTP-Server + Auth + Rollenverwaltung)
- Gorilla WebSocket (Live-Nachrichten)
- responsivem Frontend (Chat, Login, Admin-Bereich)
- JSON **oder** SQLite als User-Storage

## Features

- Registrierung und Login mit Session-Cookie
- Rollenmodell: `admin`, `moderator`, `user`
- Benutzerverwaltung im Admin-Bereich
  - Admin: Rollen aendern + Accounts sperren/entsperren
  - Moderator: Accounts (nur `user`) sperren/entsperren
- Schutzlogik:
  - letzter aktiver Admin kann nicht entfernt/gesperrt werden
  - eigene Sperrung wird verhindert
- Chat mit:
  - Live-Online-Liste
  - Join/Leave-Systemmeldungen
  - Message-History (In-Memory)
  - Auto-Reconnect im Frontend
  - Splash-Screen beim Laden

## Voraussetzungen

- Go (siehe `go.mod`, aktuell `go 1.25.0`)
- Optional Docker, wenn du Container nutzen willst

## Schnellstart (lokal)

```bash
go mod tidy
go run .
```

Server-URL:

```text
http://localhost:8080
```

## Konfiguration

### User-Storage waehlen (JSON oder SQLite)

Standard:

- `data/users.json`

Du kannst den Speicherort per Env-Variable ueberschreiben:

```powershell
set OPENCHAT_USERS_FILE=data\users.sqlite
go run .
```

Die Wahl des Backends erfolgt ueber die Dateiendung:

- `.json` -> JSON-Datei
- `.db`, `.sqlite`, `.sqlite3` -> SQLite

Hinweis: Bei SQLite wird die `users`-Tabelle automatisch erzeugt.

## Rollen und Rechte

- **admin**
  - darf Rollen aendern
  - darf sperren/entsperren
- **moderator**
  - darf sperren/entsperren
  - darf **keine** Rollen aendern
  - darf nur Nutzer mit Rolle `user` sperren
- **user**
  - normaler Chat-Zugriff

Zusatzregeln:

- gesperrte Nutzer koennen sich nicht anmelden
- aktive Sessions von gesperrten Nutzern werden beendet

## HTTP-Endpunkte

- `GET /` - Chat-Frontend (auth required)
- `GET /ws` - WebSocket-Verbindung (auth required)
- `GET|POST /admin/users` - Benutzerverwaltung (staff required)
- `GET|POST /login` - Login/Registrierung
- `POST /logout` - Logout
- `GET /static/*` - statische Assets (CSS/JS)

## WebSocket-Events (JSON)

Der Server sendet Event-Typen:

- `history` - bisherige Nachrichten beim Join
- `message` - normale Chat-Nachricht
- `system` - Systemmeldung (Join/Leave)
- `users` - aktuelle Online-Nutzerliste

Client -> Server:

```json
{ "message": "Hallo zusammen" }
```

## Docker

### Container starten

Stable:

```bash
docker run -d --name openchat -p 8080:8080 ghcr.io/schenna43lp1/openchat:latest
```

Dev:

```bash
docker run -d --name openchat-dev -p 8080:8080 ghcr.io/schenna43lp1/openchat:dev
```

### Build/Publish Workflow

GitHub Actions Workflow: `.github/workflows/docker-image.yml`

- Trigger auf `main`, `dev`, `release/*` (push + pull_request)
- Tags:
  - `latest` auf Default-Branch
  - `dev` auf Branch `dev`
  - `sha-...` fuer commit-basierte Images

## Frontend-Struktur (CSS-Splitting)

`static/style.css` importiert modulare Styles:

- `css/root.css` - Design Tokens
- `css/base.css` - Basis/Controls
- `css/splash.css` - Splash-Screen
- `css/chat.css` - Chat-UI
- `css/auth.css` - Login/Register
- `css/admin.css` - Admin-UI
- `css/responsive.css` - Breakpoints

## Tests

Tests starten:

```bash
go test ./...
```

Es gibt u. a. Tests fuer:

- Rollen-/Admin-Schutzregeln
- Ban-Logik
- Zugriffsschutz
- SQLite-Persistenz
- Env-Aufloesung fuer Storage-Pfad

## Projektstruktur (Kurzueberblick)

```text
openchat/
|- main.go
|- auth.go
|- admin.go
|- hub.go
|- client.go
|- websocket.go
|- templates/
|  |- index.html
|  |- login.html
|  `- admin.html
|- static/
|  |- app.js
|  |- style.css
|  `- css/
|- data/
|  `- users.json (Default)
|- .github/workflows/docker-image.yml
|- go.mod
`- README.md
```
