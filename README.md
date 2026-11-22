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
$env:OPENCHAT_USERS_FILE = "data\users.sqlite"
go run .
```

Dateiendungen:

- `.json` -> JSON
- `.db`, `.sqlite`, `.sqlite3` -> SQLite

### Origin-Whitelist (WebSocket und Formulare)

Standard: gleiche Host-Origin wie der Request (`Origin` muss zu `Host` passen).

Optional zusätzliche erlaubte Origins:

```powershell
$env:OPENCHAT_ALLOWED_ORIGINS = "https://chat.example.com,https://app.example.com"
go run .
```

### Erster Admin und Registrierung

Die Registrierung ist standardmäßig geschlossen, und niemand wird automatisch Admin. Den ersten Admin legt der Betreiber beim Start fest:

```powershell
$env:OPENCHAT_ADMIN_USERNAME = "betreiber"
$env:OPENCHAT_ADMIN_PASSWORD = "<mindestens 8 Zeichen>"
go run .
```

Die beiden Variablen wirken nur, solange es keinen aktiven Admin gibt, und können danach entfernt werden. Existiert der Name schon, wird der Account nur mit seinem eigenen Passwort befördert.

| Variable | Standard | Bedeutung |
|---|---|---|
| `OPENCHAT_ALLOW_REGISTRATION` | `false` | `true` öffnet die Selbstregistrierung |
| `OPENCHAT_COOKIE_SECURE` | `true` | `false` nur für HTTP in einem vertrauenswürdigen LAN |
| `OPENCHAT_TRUSTED_PROXIES` | leer | IPs oder Netze der Reverse Proxys, deren `X-Forwarded-For` gilt |

Hinter einem Reverse Proxy `OPENCHAT_TRUSTED_PROXIES` setzen, sonst teilen sich alle Nutzer ein Login-Limit.

Fehlgeschlagene Logins sind zusätzlich je Account begrenzt: 20, danach einer alle 5 Minuten. Ein Browser, der sich schon einmal angemeldet hat, hat dafür ein eigenes Budget, damit Fremde den Besitzer nicht aussperren. Während eines Angriffs kann eine Anmeldung von einem neuen Browser warten müssen. Ein Neustart setzt die Budgets zurück.

Ein Account kann höchstens 10 Chat-Verbindungen (Tabs, Geräte) gleichzeitig offen haben.

**Upgrade von v1.0.3 oder älter:** Wer offene Registrierung braucht, muss `OPENCHAT_ALLOW_REGISTRATION=true` setzen. Bestehende Admins bleiben erhalten.

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
docker run -d --name openchat -p 8080:8080 -v openchat-data:/app/data ghcr.io/schenna43lp1/openchat:latest
```

Dev:

```bash
docker run -d --name openchat-dev -p 8080:8080 -v openchat-dev-data:/app/data ghcr.io/schenna43lp1/openchat:dev
```

### Daten und Upgrade

Accounts und Chatverlauf liegen in `/app/data`. Der Container läuft als UID `65532` und startet nicht, wenn er dort nicht schreiben kann. Bei Bind-Mounts muss das Verzeichnis dieser UID gehören.

Beim Upgrade von v1.0.1 oder älter die Daten sichern, **bevor** der alte Container entfernt wird. Die Befehle außerhalb des Repositorys ausführen, denn das Backup enthält Passwort-Hashes und Nachrichten. `openchat-backup` darf noch nicht existieren.

Die Befehle funktionieren in Bash, Git Bash und PowerShell. Einzeln ausführen und bei einem Fehler anhalten.

**1. Neues Image laden**, sonst startet Docker das zwischengespeicherte alte `latest` erneut, und die bisherigen Einstellungen notieren:

```bash
docker pull ghcr.io/schenna43lp1/openchat:latest
docker inspect openchat --format "{{json .Config.Env}} {{json .Mounts}}"
```

Zeigt eine `OPENCHAT_*`-Variable auf einen Pfad außerhalb von `/app/data`, diesen Pfad separat sichern.

**2. Stoppen und sichern:**

```bash
docker stop openchat
docker cp openchat:/app/data ./openchat-backup
```

Den Inhalt des Backups prüfen. Meldet Docker, dass `/app/data` nicht existiert, oder ist das Backup leer, hat die alte Version nichts gespeichert. Dann Schritt 3 überspringen.

**3. Backup ins Volume kopieren** (bei einem Bind-Mount stattdessen das Host-Verzeichnis mit `chown -R 65532:65532` übergeben und in Schritt 4 denselben Mount verwenden):

```bash
docker create --name openchat-restore -v openchat-data:/data alpine
docker cp ./openchat-backup/. openchat-restore:/data
docker rm openchat-restore
docker run --rm -v openchat-data:/data alpine sh -c "chown -R 65532:65532 /data && ls -ln /data"
```

Die Liste muss dieselben Dateien wie das Backup zeigen. **Ist sie leer oder unvollständig, nicht weitermachen.** Der alte Container ist bis hierhin unverändert vorhanden.

**4. Container ersetzen.** Bisherige `OPENCHAT_*`-Variablen mit `-e` und andere Optionen wie Port, Netzwerk und Restart-Policy wieder angeben:

```bash
docker rm openchat
docker run -d --name openchat -p 8080:8080 -v openchat-data:/app/data ghcr.io/schenna43lp1/openchat:latest
```

**5. Prüfen:** Mit einem bestehenden Account anmelden. `user store ready` im Log genügt nicht, das meldet der Server auch bei leerem Datenbestand. Das Backup erst danach löschen.

Ohne gespeicherte Accounts gibt es keinen Admin. Den Container dann mit `-e OPENCHAT_ADMIN_USERNAME=...` und `-e OPENCHAT_ADMIN_PASSWORD=...` starten.

## CI/CD & Security

- `CI Test`: `.github/workflows/CI.yml`
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
