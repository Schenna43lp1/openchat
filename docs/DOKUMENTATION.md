# Open chat – Vollständige Projektdokumentation

## 1. Ziel und Überblick

Open chat ist eine leichtgewichtige Echtzeit-Chat-Anwendung auf Basis von Go und WebSockets.

Sie bietet:

- Login/Registrierung mit Session-Management
- Rollen- und Benutzerverwaltung (Admin/Moderator/User)
- Live-Chat mit Online-Liste und Verlauf
- JSON- oder SQLite-basierten User-Storage
- Docker-Build/Publish via GitHub Actions

Standard-URL im lokalen Betrieb:

```text
http://localhost:8080
```

---

## 2. Feature-Umfang

### 2.1 Authentifizierung

- Benutzer können sich registrieren und einloggen.
- Session wird serverseitig gehalten und über Cookie `openchat_session` referenziert.
- Passwort wird mit `bcrypt` gehasht.

### 2.2 Rollenmodell

- `admin`
- `moderator`
- `user`

Regeln:

- Erste Registrierung wird automatisch `admin`.
- Moderatoren dürfen keine Rollen ändern.
- Moderatoren dürfen nur Nutzer mit Rolle `user` sperren.
- Der letzte aktive Admin darf nicht herabgestuft/gesperrt werden.
- Eigene Sperrung wird verhindert.

### 2.3 Chat

- WebSocket-Chat mit Broadcast über zentralen Hub.
- Systemmeldungen bei Join/Leave.
- In-Memory-History (limitierter Verlauf).
- Frontend mit Auto-Reconnect bei Verbindungsabbruch.
- Splash-Screen beim Laden.

### 2.4 Benutzerverwaltung

`/admin/users` erlaubt:

- Rolle ändern (nur Admin)
- Sperren/Entsperren (Admin + Moderator, mit Regeln)

---

## 3. Architektur

## 3.1 Server-Komponenten

- `main.go`
  - Server-Start, Routing, Graceful Shutdown
- `auth.go`
  - UserStore, Auth, Sessions, Rollen-/Ban-Logik
- `admin.go`
  - Handler für Benutzerverwaltung
- `websocket.go`
  - Upgrade HTTP -> WebSocket
- `hub.go`
  - Event-Broker/Broadcast für Clients
- `client.go`
  - Read/Write-Pumps je WebSocket-Client

## 3.2 Frontend-Komponenten

- `templates/index.html` – Chat-UI
- `templates/login.html` – Login/Registrierung
- `templates/admin.html` – Benutzerverwaltung
- `static/app.js` – WebSocket-Client, Rendering, Reconnect
- `static/style.css` – zentraler CSS-Einstieg (Importe)

## 3.3 CSS-Aufteilung

`static/style.css` importiert:

- `static/css/root.css` (Design Tokens)
- `static/css/base.css` (Basis-Komponenten)
- `static/css/splash.css` (Splash-Screen)
- `static/css/chat.css` (Chat-Layout)
- `static/css/auth.css` (Auth-Seiten)
- `static/css/admin.css` (Admin-Seite)
- `static/css/responsive.css` (Breakpoints)

---

## 4. Datenhaltung

## 4.1 Unterstützte Backends

Benutzerdaten können in:

- JSON-Datei oder
- SQLite

gespeichert werden.

Backend-Auswahl erfolgt über Dateiendung des konfigurierten Pfads:

- `.json` -> JSON
- `.db`, `.sqlite`, `.sqlite3` -> SQLite

## 4.2 Konfiguration über Umgebungsvariable

Variable:

```text
OPENCHAT_USERS_FILE
```

Beispiel (Windows):

```powershell
set OPENCHAT_USERS_FILE=data\users.sqlite
go run .
```

Default ohne Variable:

```text
data/users.json
```

## 4.3 SQLite-Schema

Wird automatisch erstellt:

```sql
CREATE TABLE IF NOT EXISTS users (
  username TEXT PRIMARY KEY COLLATE NOCASE,
  password_hash TEXT NOT NULL,
  role TEXT NOT NULL,
  banned INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL
);
```

---

## 5. HTTP- und WebSocket-Schnittstellen

## 5.1 HTTP-Endpunkte

- `GET /`  
  Chat-Frontend (auth required)

- `GET /ws`  
  WebSocket-Endpoint (auth required)

- `GET /admin/users`  
  Benutzerverwaltung anzeigen (staff required)

- `POST /admin/users`  
  Rollen/Sperraktionen ausführen (staff required, inkl. Regelprüfung)

- `GET /login`  
  Login-Seite

- `POST /login`  
  Login oder Registrierung (`mode=login|register`)

- `POST /logout`  
  Session löschen

- `GET /static/*`  
  Auslieferung statischer Assets

## 5.2 WebSocket-Protokoll

### Client -> Server

```json
{ "message": "Hallo Welt" }
```

### Server -> Client Eventtypen

- `history` – vergangene Nachrichten beim Verbindungsaufbau
- `message` – normale Chatnachricht
- `system` – Systemmeldung (Join/Leave)
- `users` – aktuelle Online-Liste

---

## 6. Sicherheit und Validierung

- Passwort-Hashing über `bcrypt`
- Session-Cookie mit `HttpOnly` und `SameSite=Lax`
- Username-Pattern: `^[A-Za-z0-9_.-]{3,32}$`
- Passwortlänge: 8–128 Zeichen
- Nachrichtenlänge serverseitig begrenzt
- Rollen- und Sperrregeln werden serverseitig validiert

Hinweis:

- `websocket.Upgrader.CheckOrigin` ist aktuell permissiv (`true`).
  Für produktive öffentliche Deployments sollte eine restriktive Origin-Prüfung ergänzt werden.

---

## 7. Lokaler Betrieb

## 7.1 Start

```bash
go mod tidy
go run .
```

Dann im Browser öffnen:

```text
http://localhost:8080
```

## 7.2 Tests

```bash
go test ./...
```

Getestet werden u. a.:

- Rollen- und Admin-Schutzlogik
- Ban-Logik
- Zugriffsbeschränkungen
- SQLite-Persistenz
- Konfigurationsauflösung über Env

---

## 8. Docker & CI/CD

## 8.1 Verfügbare Images

- Stable: `ghcr.io/schenna43lp1/openchat:latest`
- Dev: `ghcr.io/schenna43lp1/openchat:dev`
- Zusätzlich SHA-Tags: `sha-...`

## 8.2 Container starten

Stable:

```bash
docker run -d --name openchat -p 8080:8080 ghcr.io/schenna43lp1/openchat:latest
```

Dev:

```bash
docker run -d --name openchat-dev -p 8080:8080 ghcr.io/schenna43lp1/openchat:dev
```

## 8.3 GitHub Actions Workflow

Datei:

```text
.github/workflows/docker-image.yml
```

Trigger:

- Push auf `main`, `dev`, `release/*`
- Pull Request auf `main`, `dev`, `release/*`
- `workflow_dispatch`

Tagging:

- `latest` auf Default-Branch
- `dev` auf Branch `dev`
- `sha-...` commitbasiert

---

## 9. Troubleshooting

## 9.1 Login funktioniert nicht

- Username/Passwort prüfen
- ggf. Account gesperrt (`banned`)
- prüfen, ob richtiger Storage-Pfad gesetzt ist (`OPENCHAT_USERS_FILE`)

## 9.2 Änderungen an Usern „verschwinden“

- sicherstellen, dass auf denselben Storage-Pfad geschrieben und gelesen wird
- bei SQLite Dateiberechtigungen prüfen

## 9.3 WebSocket trennt sich sofort

- Session/Cookie ungültig oder abgelaufen
- Benutzer evtl. gesperrt
- Browser-Konsole und Server-Logs prüfen

## 9.4 Port 8080 belegt

- anderen Prozess beenden oder Port in Serverkonfiguration anpassen

---

## 10. Projektstruktur

```text
openchat/
|- main.go
|- auth.go
|- admin.go
|- websocket.go
|- hub.go
|- client.go
|- templates/
|  |- index.html
|  |- login.html
|  `- admin.html
|- static/
|  |- app.js
|  |- style.css
|  `- css/
|     |- root.css
|     |- base.css
|     |- splash.css
|     |- chat.css
|     |- auth.css
|     |- admin.css
|     `- responsive.css
|- data/
|  `- users.json (Default)
|- docs/
|  `- DOKUMENTATION.md
|- .github/workflows/docker-image.yml
|- go.mod
`- README.md
```

---

## 11. Weiterentwicklung (Empfehlungen)

- restriktive `CheckOrigin`-Strategie für öffentliche Deployments
- persistente Chat-History (DB statt In-Memory)
- Rate Limiting / Spam-Protection
- Audit-Log für Admin-/Moderator-Aktionen
- konfigurierbarer HTTP-Port via Env
