# ADR 0002: Passwort-Resets und Direktnachrichten

- Status: angenommen, 22.11.2026
- Gilt ab: v1.2.0

## Kontext

Admins der App können keine fremden Direktnachrichten lesen. Wer ein Passwort zurücksetzt, kann sich als dieses Konto anmelden. Ohne Gegenmaßnahme würde ein Reset diese Grenze aufheben. Einen zweiten Kanal wie E-Mail gibt es nicht.

## Entscheidung

- **Reset durch einen Admin:** Das Konto sieht danach nur noch neue Direktnachrichten (`users.dm_visible_after_id`). Die Gesprächspartner behalten ihren Verlauf. Die Grenze sinkt nie, auch nicht bei späteren Passwortänderungen.
- **Admins und frühere Admins** setzt nur der Betreiber zurück (`users.operator_reset_only`). Das Merkmal wird nie gelöscht, sonst würde Herabstufen den Reset erlauben.
- **Reset durch den Betreiber** (`openchat reset-password`) blendet nichts aus. Der Betreiber kann die Datenbank ohnehin lesen.
- **Startpasswörter** wählt der Server und zeigt sie einmal. Sie erlauben nur, ein eigenes Passwort zu setzen.
- **Protokoll für den Besitzer** (`password_issues`): Jedes vergebene Startpasswort und sein Einlösen werden in derselben Transaktion gespeichert. Die App kann das nicht löschen, und niemand kann es als gelesen markieren, denn der erste Leser kann der Admin sein.
- **Anzeige:** auf der erzwungenen Passwortseite und nach jedem Login aus einem Browser ohne Geräte-Cookie für den aktuellen Hash. Die Zwangsseite allein reicht nicht: Wer ein Startpasswort einlöst, kann denselben Wert danach als eigenes Passwort setzen und übergeben.
- **Jede Passwortänderung** löscht die Sessions in derselben Transaktion. Eine Session entsteht nur, solange der geprüfte Hash noch gilt.

## Folgen

- Wer sein Passwort vergisst und vom Admin zurückgesetzt wird, verliert die Sicht auf ältere Direktnachrichten.
- Ein Admin kann ein Startpasswort selbst benutzen, künftige Nachrichten an dieses Konto lesen und in dessen Namen schreiben. Ohne zweiten Kanal ist das nicht verhinderbar, nur erkennbar: Das Protokoll zeigt es dem Besitzer. Es hilft nur, wenn er es liest und den Betreiber fragt.
- Wer vor der Migration auf Schema 2 herabgestuft wurde, ist nicht mehr als früherer Admin erkennbar.
- Ausgeblendete Nachrichten bleiben in der Datenbank.
- Der Betreiber-Befehl braucht einen gestoppten Server, weil offene Verbindungen Änderungen eines anderen Prozesses nicht bemerken (ADR 0001).

## Verworfen

- **Verlauf sichtbar lassen:** Admins könnten die Direktnachrichten aller Nutzer lesen.
- **Nachrichten löschen:** Der Gesprächspartner verlöre seinen Verlauf.
- **Admins setzen einander zurück:** Eine gekaperte Admin-Sitzung samt Passwort würde alle Admins übernehmen.
