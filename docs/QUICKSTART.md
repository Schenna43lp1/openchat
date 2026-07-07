# Open chat – Quickstart (Endnutzer)

## 1) App starten

### Lokal (Go)

```bash
go mod tidy
go run .
```

Danach im Browser öffnen:

```text
http://localhost:8080
```

### Oder per Docker

```bash
docker run -d --name openchat -p 8080:8080 ghcr.io/schenna43lp1/openchat:latest
```

## 2) Registrieren / Einloggen

1. Öffne `/login`
2. Benutzername + Passwort eingeben
3. Entweder **Einloggen** oder **Registrieren**

## 3) Chat verwenden

- Nachricht schreiben und senden
- Für private Nachricht im Dropdown `Direkt an <Benutzer>` wählen
- Online-Liste rechts sehen
- Logout über den Button im Account-Bereich

## 4) Rollen und Admin-Bereich

- Admins und Moderatoren sehen den Link **Benutzer verwalten**
- Admins können Rollen ändern und Nutzer sperren/entsperren
- Moderatoren können Nutzer mit Rolle `user` sperren/entsperren

## 5) Häufige Probleme

- **Login geht nicht:** Passwort falsch oder Account gesperrt
- **Keine Verbindung:** Seite neu laden, Serverstatus prüfen
