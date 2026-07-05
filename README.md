# Go Chat

Ein einfaches Echtzeit-Chat-System mit Go, Gorilla WebSocket und einem responsiven dunklen Frontend.


![GitHub Actions Workflow Status](https://img.shields.io/github/actions/workflow/status/Schenna43lp1/openchat/.github%2Fworkflows%2Fdocker-image.yml)
## Start

```bash
go mod tidy
go run .
```

Optional kannst du statt JSON auch SQLite fuer die Benutzerdaten verwenden:

```bash
# Beispiel:
set OPENCHAT_USERS_FILE=data\users.sqlite
go run .
```

Standard bleibt `data/users.json`. Bei Dateiendungen `.db`, `.sqlite` oder `.sqlite3` nutzt Open chat automatisch SQLite.

Der Server läuft standardmäßig auf:

```text
http://localhost:8080
```

## Docker

```bash

docker run -d --name openchat -p 8080:8080 ghcr.io/schenna43lp1/openchat:latest
# Dev-Image:
docker run -d --name openchat-dev -p 8080:8080 ghcr.io/schenna43lp1/openchat:dev
```

## Endpunkte

- `GET /` liefert das Frontend
- `GET /ws` öffnet die WebSocket-Verbindung
- `GET|POST /admin/users` Benutzerverwaltung (Admins: Rollen + Sperren, Moderatoren: Sperren)
- `GET /static/*` liefert CSS und JavaScript

## Projektstruktur

```text
chat/
├── main.go
├── websocket.go
├── hub.go
├── client.go
├── templates/
│   └── index.html
├── static/
│   ├── style.css
│   └── app.js
├── go.mod
└── README.md
```
