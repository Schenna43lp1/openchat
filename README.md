# Open chat

![GitHub Actions Workflow Status](https://img.shields.io/github/actions/workflow/status/Schenna43lp1/openchat/.github%2Fworkflows%2Fdocker-image.yml)
![CodeQL](https://img.shields.io/github/actions/workflow/status/Schenna43lp1/openchat/codeql.yml?label=codeql)

Ein leichtgewichtiges Echtzeit-Chat-System zum Selberhosten: ein Go-Binary, eine SQLite-Datei, kein externer Dienst.

- öffentlicher Chat und Direktnachrichten über WebSocket
- Accounts mit Rollen, Sperren und Verwaltung im Browser
- Sessions und Nachrichten überleben Neustarts
- responsives Frontend ohne externe Ressourcen

Diese README ist die Dokumentation für Betrieb, Nutzung und Entwicklung. Architekturentscheidungen stehen in [docs/adr](docs/adr).

## Schnellstart

Die Registrierung ist standardmäßig geschlossen, und niemand wird automatisch Admin. Den ersten Admin legt der Betreiber beim Start fest.

Lokal (PowerShell):

```powershell
$env:OPENCHAT_ADMIN_USERNAME = "betreiber"
$env:OPENCHAT_ADMIN_PASSWORD = "<mindestens 8 Zeichen>"
go run .
```

Docker:

```bash
docker run -d --name openchat -p 8080:8080 -v openchat-data:/app/data -e OPENCHAT_ADMIN_USERNAME=betreiber -e OPENCHAT_ADMIN_PASSWORD="<mindestens 8 Zeichen>" ghcr.io/schenna43lp1/openchat:latest
```

Danach <http://localhost:8080> öffnen und anmelden. Die beiden Admin-Variablen wirken nur, solange es keinen aktiven Admin gibt, und können dann entfernt werden. Existiert der Name schon, wird der Account nur mit seinem eigenen Passwort befördert.

Das Session-Cookie ist `Secure`. Browser akzeptieren es über HTTPS und auf `localhost`. Für andere Adressen einen Reverse Proxy mit HTTPS davorschalten oder, nur im vertrauenswürdigen LAN, `OPENCHAT_COOKIE_SECURE=false` setzen.

## Konfiguration

| Variable | Standard | Bedeutung |
|---|---|---|
| `OPENCHAT_PORT` | `8080` | HTTP-Port |
| `OPENCHAT_DB_FILE` | `data/openchat.db` | SQLite-Datei. Ist `OPENCHAT_USERS_FILE` gesetzt, liegt sie standardmäßig in dessen Verzeichnis |
| `OPENCHAT_ALLOW_REGISTRATION` | `false` | `true` öffnet die Selbstregistrierung |
| `OPENCHAT_ADMIN_USERNAME` | leer | erster Admin, nur zusammen mit dem Passwort |
| `OPENCHAT_ADMIN_PASSWORD` | leer | mindestens 8 Zeichen, höchstens 72 Byte |
| `OPENCHAT_COOKIE_SECURE` | `true` | `false` nur für HTTP in einem vertrauenswürdigen LAN |
| `OPENCHAT_ALLOWED_ORIGINS` | leer | zusätzliche Origins für WebSocket und Formulare, durch Komma getrennt |
| `OPENCHAT_TRUSTED_PROXIES` | leer | IPs oder Netze der Reverse Proxys, deren `X-Forwarded-For` gilt |
| `OPENCHAT_USERS_FILE` | `data/users.json` | nur für den Import aus v1.0.x |
| `OPENCHAT_CHAT_HISTORY_FILE` | `data/chat-history.json` | nur für den Import aus v1.0.x |

Ungültige Werte beenden den Start mit einer Fehlermeldung. Ungültige Origins werden gemeldet und ignoriert.

### Reverse Proxy

- WebSocket-Upgrades für `/ws` durchreichen.
- Den `Host`-Header erhalten oder die öffentliche Adresse in `OPENCHAT_ALLOWED_ORIGINS` eintragen.
- `OPENCHAT_TRUSTED_PROXIES` setzen, sonst teilen sich alle Nutzer ein Login-Limit.
- HSTS setzt der Proxy, denn nur er kennt TLS.

## Nutzung

- **Chat** (`/`): öffentliche Nachrichten, bis 4096 Zeichen.
- **Direkt-Chat** (`/direct`): Empfänger aus der Online-Liste wählen. Nur Sender und Empfänger sehen die Nachricht. Wer einen Namen später bekommt, erbt keine Nachrichten.
- **Admin** (`/admin/users`): Rollen und Sperren.

| Rolle | Darf |
|---|---|
| `admin` | Rollen ändern, alle anderen sperren und entsperren |
| `moderator` | Nutzer mit Rolle `user` sperren und entsperren |
| `user` | chatten |

Der letzte aktive Admin kann nicht entfernt oder gesperrt werden. Eine Sperre beendet alle Sessions und offenen Verbindungen des Accounts.

### Grenzen

| Was | Grenze |
|---|---|
| Login | 10 Versuche je Adresse, danach einer alle 6 Sekunden |
| Registrierung | 5 je Adresse, danach eine alle 12 Minuten |
| Nachrichten | 10 je Account, danach eine pro Sekunde |
| Request-Body | 64 KiB |
| Aufbewahrung | die neuesten 100 öffentlichen Nachrichten und die neuesten 100 Direktnachrichten je Absender |

## Daten, Backup und Upgrade

Alles liegt in `/app/data/openchat.db`, auch Passwort-Hashes und Nachrichten im Klartext. Der Container läuft als UID `65532` und startet nicht, wenn er dort nicht schreiben kann. Bei Bind-Mounts muss das Verzeichnis dieser UID gehören.

Für ein Backup den Container stoppen und das Verzeichnis kopieren.

### Upgrade von v1.0.x auf v1.1

- Beim ersten Start mit leerer Datenbank werden `users.json` (oder die SQLite-Datei aus `OPENCHAT_USERS_FILE`) und `chat-history.json` einmalig importiert.
- Vorher entsteht daneben `backup-v1.0-<Zeitpunkt>/`. Die Originale bleiben unverändert. Eine defekte Datei bricht den Start ab, ohne etwas zu ändern.
- Enthalten die alten Dateien Direktnachrichten, stehen sie dort und im Backup weiter im Klartext. Beides nach erfolgreicher Prüfung löschen, wenn es nicht mehr gebraucht wird.
- Wer `OPENCHAT_USERS_FILE` später entfernt, setzt vorher `OPENCHAT_DB_FILE` auf den bisherigen Ort der Datenbank.
- Die Registrierung ist seit v1.0.4 geschlossen. Bei Bedarf `OPENCHAT_ALLOW_REGISTRATION=true` setzen.
- Ein Downgrade geht nur mit den alten Dateien und verliert alles, was nach dem Upgrade entstanden ist.

Die Daten sichern, **bevor** der alte Container entfernt wird. Die Befehle außerhalb des Repositorys ausführen, denn das Backup enthält Passwort-Hashes und Nachrichten. `openchat-backup` darf noch nicht existieren. Sie funktionieren in Bash, Git Bash und PowerShell. Einzeln ausführen und bei einem Fehler anhalten.

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

Den Inhalt des Backups prüfen. Meldet Docker, dass `/app/data` nicht existiert, oder ist das Backup leer, hat die alte Version nichts gespeichert. Dann Schritt 3 überspringen. Hängt an `/app/data` schon das Volume `openchat-data` (üblich seit v1.0.2), Schritt 3 ebenfalls überspringen.

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

**5. Prüfen:** `docker logs openchat` muss `imported data from v1.0.x` mit der erwarteten Anzahl zeigen, und ein bestehender Account muss sich anmelden können. `server ready` allein genügt nicht. Meldet das Log `files from v1.0.x are ignored`, hatte die neue Datenbank schon Daten: Container stoppen, `openchat.db` wegsichern und erneut starten.

Ohne gespeicherte Accounts gibt es keinen Admin. Dann wie im Schnellstart mit den Admin-Variablen starten.

## Endpunkte

| Endpunkt | Zugriff |
|---|---|
| `GET /`, `GET /direct` | angemeldet |
| `GET /ws` | angemeldet, WebSocket |
| `GET /session` | 204 angemeldet, sonst 401 oder 403 |
| `GET, POST /admin/users` | Admin und Moderator |
| `GET, POST /login` | alle |
| `POST /logout` | alle |
| `GET /healthz` | alle, 200 oder 503 |
| `GET /static/*` | alle |

Browser-POSTs von fremden Origins erhalten 403.

### WebSocket-Protokoll

Client an Server:

```json
{ "message": "Hallo", "to": "bob" }
```

`to` entfällt bei öffentlichen Nachrichten. Der Server sendet Events mit `type`: `history`, `message`, `direct`, `system`, `users` oder `error`.

| Close-Code | Bedeutung | Reaktion des Clients |
|---|---|---|
| 1008 | Session beendet | zum Login |
| 1001 | Server fährt herunter | neu verbinden |
| 1013 | vorübergehender Fehler | neu verbinden |
| 1006 | Verbindung abgebrochen oder abgelehnt | `/session` fragen, dann Login oder neu verbinden |

## Entwicklung

```text
main.go, config.go     Konfiguration, Start, Shutdown
internal/web           HTTP, WebSocket-Upgrade, Schutz, Templates, Assets
internal/chat          Hub und Verbindungen
internal/auth          Regeln für Accounts, Rollen und Sessions
internal/store         SQLite, Migrationen, Import aus v1.0.x
```

`web` nutzt `chat` und `auth`, beide nutzen `store`. Der Hub besitzt die Verbindungen in einer Goroutine und wartet nie auf einen Schreibzugriff: Der Sender speichert, danach verteilt der Hub. Schemaänderungen kommen als neue Datei nach `internal/store/migrations`.

Prüfen wie die CI:

```bash
go vet ./...
go test -race -cover -coverpkg=./... ./...
go run honnef.co/go/tools/cmd/staticcheck@v0.8.2 ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.10.0 ./...
docker build --tag openchat:smoke .
bash scripts/container-smoke.sh openchat:smoke
```

### Releases

Tags folgen SemVer mit kleinem `v`, zum Beispiel `v1.1.0`. Ein solcher Tag veröffentlicht die Images `1.1.0` und `1.1`, aber erst nach grüner CI. `latest` folgt `main`, `dev` dem Branch `dev`.

Beiträge: [CONTRIBUTING.md](CONTRIBUTING.md). Sicherheitslücken bitte nicht öffentlich melden: [SECURITY.md](SECURITY.md).
