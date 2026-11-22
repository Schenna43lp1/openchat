# ADR 0001: SQLite als einziger Speicher

- Status: angenommen, 22.11.2026
- Gilt ab: v1.1.0

## Kontext

Bis v1.0.x gibt es drei Speicher: Accounts in JSON oder SQLite (bei jeder Änderung komplett neu geschrieben), Sessions nur im RAM und den Chatverlauf als JSON-Datei mit 100 Einträgen für alles. Das kostet bei jedem Schreiben O(n), verliert Sessions beim Neustart und ist bei einem Abbruch während des Schreibens nicht sicher.

## Entscheidung

Eine SQLite-Datei (`openchat.db`) mit den Tabellen `users`, `sessions` und `messages`, über den vorhandenen Treiber `modernc.org/sqlite` ohne cgo.

- **Migrationen** liegen nummeriert in `internal/store/migrations` und laufen je in einer Transaktion. `PRAGMA user_version` ist die Schemaversion. Eine neuere Datenbank wird abgelehnt. Veröffentlichte Migrationen werden nie geändert, nur ergänzt.
- **Identität** ist die Nutzer-ID, die nie wiederverwendet wird. Namen in `messages` dienen nur der Anzeige. Wer einen Namen später bekommt, erbt keine Nachrichten.
- **Sessions** werden nur als SHA-256 des Tokens gespeichert.
- **Aufbewahrung** bleibt begrenzt: die neuesten 100 öffentlichen Nachrichten und die neuesten 100 Direktnachrichten je Absender. Eine längere Aufbewahrung ist eine eigene Entscheidung (Datenschutz, Plattenplatz).
- **Eine Verbindung, eine Instanz.** Das vermeidet `SQLITE_BUSY`. Es bleibt beim Standard-Journal, damit eine gestoppte Instanz mit dem Kopieren der Datei gesichert ist.
- **Import:** Eine leere Datenbank übernimmt einmalig die Dateien aus v1.0.x. Vorher entsteht ein Backup, die Originale bleiben unverändert, eine defekte Datei bricht den Start ab.

## Folgen

- Das JSON-Backend entfällt. `OPENCHAT_USERS_FILE` und `OPENCHAT_CHAT_HISTORY_FILE` bezeichnen nur noch die Quelle des Imports.
- Ein Downgrade geht nur über das Backup und verliert alles, was nach dem Upgrade entstanden ist.
- Direkte Änderungen an der Datenbank bei laufendem Server werden für offene WebSockets nicht sofort wirksam.
- Mehrere Instanzen auf derselben Datei werden nicht unterstützt.

## Verworfen

- **JSON behalten:** doppelte Implementierung und Tests, keine Transaktionen über Accounts, Sessions und Nachrichten.
- **PostgreSQL:** ein zusätzlicher Dienst für eine Anwendung, die als einzelner Container läuft.
