# Go Chat

Ein einfaches Echtzeit-Chat-System mit Go, Gorilla WebSocket und einem responsiven dunklen Frontend.

## Funktionen

- Backend ausschließlich in Go
- Keine Datenbank, Nachrichten werden im RAM gehalten
- WebSocket-Kommunikation in Echtzeit
- Mehrere gleichzeitige Benutzer
- Benutzername beim Verbinden
- Broadcast an alle verbundenen Clients
- Join- und Leave-Nachrichten
- Zeitstempel pro Nachricht
- Online-Benutzerliste
- Ping/Pong zum Offenhalten der Verbindung
- Graceful Shutdown per `Ctrl+C` oder `SIGTERM`
- Automatische Client-Wiederverbindung
- Enter sendet, Shift+Enter erzeugt eine neue Zeile
- Responsives dunkles UI

## Start

```bash
go mod tidy
go run .
```

Der Server läuft standardmäßig auf:

```text
http://localhost:8080
```

## Endpunkte

- `GET /` liefert das Frontend
- `GET /ws` öffnet die WebSocket-Verbindung
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
