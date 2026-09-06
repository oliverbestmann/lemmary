# mailgate

Receive documents by email. `mailgate` runs an SMTP server, turns each message
and its attachments into a single PDF, and writes that PDF into Lemmary's watch
directory — where the server imports it exactly as it would a file dropped in
the browser: same checksum dedup, same OCR, same AI metadata.

```
mail --> mailgate --> $WATCH_DIR/alice@example.com/2026-09-06 Invoice 1234.pdf
                          |
                          '--> Lemmary's watch import
```

It is a separate process from the server, and that is the point: filing a
document needs nothing but a file in the right directory, so the listener that
faces the internet holds no database access, no credentials, and no part of the
archive. It is a separate Go module too (`lemmary/mailgate`), so it shares none
of the server's dependencies -- no PocketBase, no bleve, no FAISS, no CGO -- and
its own four are kept out of the binary that holds the archive.

```
mailgate/
  main.go       configuration and startup
  aliases.go    who a recipient address belongs to
  server.go     the SMTP session: what is accepted and what is refused
  spool.go      writing the document where the importer will find it
  mailpdf/      one mail, with its attachments, to one PDF
```

Build and test it on its own:

```bash
cd mailgate
go test ./...
go build .
```

## What the PDF looks like

One document per mail:

1. A cover page with the sender, recipient, date, subject, the list of attached
   files, and the message text.
2. Each attached PDF, in the order the mail listed them.
3. Each attached image (JPEG, PNG, GIF, TIFF, WebP), one page apiece.

Attachments it cannot lay out — Word documents, spreadsheets, calendar invites —
are named on the cover page and contribute no pages. The mail is still filed.

A PDF or an image sent as `application/octet-stream` is still rendered; scanners
and phone mail clients do this constantly, and the file extension is used as the
fallback.

## Configuration

All environment variables; there are no configuration files.

| Variable | Required | Default | Meaning |
| --- | --- | --- | --- |
| `MAILGATE_DOMAIN` | yes | — | The domain this instance receives mail for. |
| `MAILGATE_ALIASES` | yes | — | JSON object mapping alias to owner email. |
| `MAILGATE_SPOOL` | yes | — | Lemmary's `WATCH_DIR`. |
| `MAILGATE_ADDR` | no | `:2525` | Listen address. |
| `MAILGATE_MAX_SIZE` | no | `52428800` | Largest accepted message, in bytes. Advertised through the SIZE extension, so an oversized mail is refused before it is transferred. |
| `MAILGATE_MAX_PER_HOUR` | no | `60` | Accepted messages per connecting IP per hour. |
| `MAILGATE_LOG_LEVEL` | no | `info` | `debug`, `info`, `warn`, `error`. |

`mailgate -check` validates the configuration and prints what it resolved to,
then exits.

## Aliases

`MAILGATE_ALIASES` is a JSON object mapping each address to the account that
owns it. The owner email is the name of that account's directory under
`WATCH_DIR`, which is how Lemmary decides whose document this is.

```json
{
  "in-9f2c7a41b0e83d56a1c4f70b2e9d8351": "alice@example.com",
  "in-4b81e0d7c3a95f26b8d012ea7c4f9d63": "bob@example.com",
  "in-7d3e05fa9c18b426e7a3d5c081b6f294": "bob@example.com"
}
```

Several aliases may point at one account — one per scanner, say, so a leaked one
can be revoked without disturbing the rest. Aliases are matched case-insensitively
and a `+tag` suffix is ignored, so `in-9f2c…+scanner@` still resolves.

It is read once at startup. Adding an account means restarting the container,
the same as every other setting here. Anything malformed — not an object, an
owner that is not a plain email address, one alias mapped to two accounts — is
refused at startup rather than skipped, because a silently dropped mapping looks
like working software until somebody notices months of refused mail.

`mailgate -alias` prints a fresh random alias to paste in.

## Security

This is designed to be the MX for a domain, so anything on the internet can open
a connection to it. Three properties keep that safe, and changing any of them
needs thinking about:

- **It never relays.** A recipient that is not a configured alias is refused at
  `RCPT TO`, before the message body is transferred at all. The reply is the
  same 550 for an unknown alias, a wrong domain and a malformed address, so
  probing learns nothing.
- **The alias is the credential.** There is no authentication, because there is
  no address worth guessing: `-alias` mints 16 random bytes. Treat one like a
  password — anyone holding it can put documents in that account, and it sits in
  the environment, so it is as exposed as any other secret there. Revoke one by
  removing it from `MAILGATE_ALIASES` and restarting.
- **Nothing is accepted before it is stored.** The `250` goes out after the PDF
  is in the spool directory, so a failure leaves the message in the sending
  server's queue instead of dropping it. Rate limiting replies `451`, which is
  temporary, so a legitimate sender retries rather than bounces.

The sender is not authenticated and a `From` header is trivially forged. That is
tolerable because the alias decides ownership and the mail is only ever filed,
never acted on. If that is not enough for you, put a real MTA in front and have
it forward locally.

## Deploying

```bash
docker build -f Dockerfile.mailgate -t lemmary-mailgate:local .
docker run -p 25:2525 \
  -e MAILGATE_DOMAIN=docs.example.com \
  -e MAILGATE_ALIASES='{"in-9f2c7a41b0e83d56a1c4f70b2e9d8351":"alice@example.com"}' \
  -e MAILGATE_SPOOL=/watch \
  -v lemmary-watch:/watch \
  lemmary-mailgate:local
```

The volume at `/watch` is the same one the server has mounted at its `WATCH_DIR`.
The container listens on 2525 and runs unprivileged; publish it as 25 on the
host, because sending servers only ever try 25.

Both containers write to that volume. `mailgate` creates the per-owner
directories and the documents in them; the server's watch import then *moves*
each file into `import-archived/` once it has read it, so it needs write access
to the same directories. The image therefore runs as uid 1000 — the server
image's `app` user — and creates everything group-writable, so running the two
under different accounts works too as long as they share a group:

```bash
docker run --user 1000:1000 ...   # the default
docker run --user 1234:1000 ...   # different user, shared group
```

If the volume already exists and is owned by someone else, `mailgate` refuses to
start rather than accepting mail it cannot file: `spool root /watch is not
writable`. Fix the ownership on the volume, not the user here — the server has
to keep its access too.

For mail to arrive you also need an `MX` record for `MAILGATE_DOMAIN` pointing
at the host, and a provider that does not block inbound port 25 — several cloud
and most residential ISPs do.

There is no TLS. Sending servers that offer STARTTLS and do not get it deliver
anyway, in cleartext. Nothing here authenticates, so there is no credential a
cleartext connection could leak; the message contents are exposed in transit,
which is the same exposure ordinary mail has had all along.
