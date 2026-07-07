# Open chat – Entwickler-Doku

## Überblick

Diese Doku beschreibt die technische Struktur sowie zentrale Flows mit Sequenzabläufen.

---

## 1. Komponenten

### Backend

- `main.go` – Bootstrap, Routing, Lifecycle
- `auth.go` – UserStore, Auth, Session, Rollen-/Ban-Regeln
- `admin.go` – Benutzerverwaltung
- `websocket.go` – HTTP->WebSocket Upgrade
- `hub.go` – Event-Broker für Chat
- `client.go` – Read/Write-Pump pro Client

### Frontend

- `templates/index.html` – Chat-View
- `templates/login.html` – Auth-View
- `templates/admin.html` – Benutzerverwaltung
- `static/app.js` – WebSocket-Client, Rendering, Reconnect
- `static/style.css` + `static/css/*` – modulare Styles

---

## 2. Laufzeit-Flow (Serverstart)

```mermaid
sequenceDiagram
    participant M as main.go
    participant U as UserStore
    participant H as Hub
    participant S as HTTP Server

    M->>M: Templates laden
    M->>U: NewUserStore(resolveUsersStorePath())
    U-->>M: UserStore bereit
    M->>H: NewHub()
    M->>H: go Hub.Run()
    M->>S: ListenAndServe()
```

---

## 3. Auth-Flow (Login)

```mermaid
sequenceDiagram
    participant B as Browser
    participant L as /login Handler
    participant U as UserStore
    participant SM as SessionManager

    B->>L: POST /login (mode=login, username, password)
    L->>U: Authenticate(username, password)
    U-->>L: authUser / error
    alt erfolgreich
      L->>SM: Create(session)
      SM-->>B: Set-Cookie(openchat_session)
      L-->>B: 303 Redirect /
    else Fehler
      L-->>B: Login mit Fehlermeldung
    end
```

---

## 4. Registrierungs-Flow

```mermaid
sequenceDiagram
    participant B as Browser
    participant L as /login Handler
    participant U as UserStore
    participant SM as SessionManager

    B->>L: POST /login (mode=register, username, password)
    L->>U: Register()
    U->>U: Validierung + bcrypt + Persistenz
    U-->>L: ok / error
    alt erfolgreich
      L->>SM: Create(session)
      L-->>B: 303 Redirect /
    else Fehler
      L-->>B: Registrierung mit Fehlermeldung
    end
```

---

## 5. Auth-Middleware-Flow

```mermaid
sequenceDiagram
    participant C as Client
    participant A as authRequired
    participant SM as SessionManager
    participant U as UserStore
    participant H as Ziel-Handler

    C->>A: Request auf geschützte Route
    A->>SM: Username(cookie)
    alt keine gültige Session
      A-->>C: Redirect /login (oder 401 bei /ws)
    else Session ok
      A->>U: Find(username)
      alt user fehlt oder gesperrt
        A-->>C: Session clear + Redirect /login (oder 403 bei /ws)
      else user ok
        A->>H: Request mit currentUser im Context
      end
    end
```

---

## 6. WebSocket-Flow

```mermaid
sequenceDiagram
    participant B as Browser
    participant W as /ws Handler
    participant H as Hub
    participant C as Client(read/write pumps)

    B->>W: GET /ws
    W->>W: Upgrade
    W->>H: register <- client
    W->>C: go writePump()
    W->>C: go readPump()
    C->>H: broadcast <- message (bei Eingabe)
    H-->>B: message/system/users/history Events
```

## 6.1 Direktnachrichten-Flow

```mermaid
sequenceDiagram
    participant S as Sender
    participant H as Hub
    participant R as Empfänger

    S->>H: {message, to}
    H->>H: EventDirect erzeugen
    H-->>S: direct event
    H-->>R: direct event
```

---

## 7. Admin-/Moderator-Flow (Ban/Role)

```mermaid
sequenceDiagram
    participant B as Browser
    participant A as /admin/users
    participant U as UserStore

    B->>A: POST action=toggle_ban | set_role
    A->>A: Rollenprüfung (staff/admin)
    alt toggle_ban
      A->>U: Find(target)
      A->>A: Regelprüfung (keine Selbstsperre, Moderator nur user)
      A->>U: SetBanned()
    else set_role
      A->>A: nur Admin erlaubt
      A->>U: SetRole()
    end
    U-->>A: ok / Fehler
    A-->>B: aktualisierte Admin-View
```

---

## 8. Datenhaltung: JSON vs SQLite

Auswahl über Dateiendung von `OPENCHAT_USERS_FILE`:

- `.json` -> JSON-Datei
- `.db`, `.sqlite`, `.sqlite3` -> SQLite

SQLite-Schema wird automatisch angelegt (`users`-Tabelle).

---

## 9. Frontend-Flow (app.js)

1. Seite lädt, Splash sichtbar
2. `connect()` öffnet WebSocket
3. Bei `open`: Status online, Splash ausblenden
4. Bei `message`: Events rendern (`history`, `message`, `system`, `users`)
5. Bei `close/error`: Reconnect mit Backoff

---

## 10. Teststrategie (aktuell)

`go test ./...` deckt zentrale Regeln ab:

- Admin-Schutzregeln
- Ban-Verhalten
- Zugriffsmiddleware
- SQLite-Persistenz
- Env-basierte Store-Pfadauflösung

---

## 11. Erweiterungspunkte

- restriktive `CheckOrigin` Strategie
- persistente Chat-History
- Audit-Log für Adminaktionen
- konfigurierbarer Server-Port via Env
