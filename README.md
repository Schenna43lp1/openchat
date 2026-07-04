# Go Chat

Ein einfaches Echtzeit-Chat-System mit Go, Gorilla WebSocket und einem responsiven dunklen Frontend.

## Start

```bash
go mod tidy
go run .
```

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
