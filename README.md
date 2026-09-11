# stayinformed-go

[![Go Reference](https://pkg.go.dev/badge/github.com/tonobo/stayinformed-go.svg)](https://pkg.go.dev/github.com/tonobo/stayinformed-go)

`stayinformed-go` is an unofficial Go client and a set of small services for the Stay Informed parent application.

> [!NOTE]
> This project is almost entirely vibe-coded. Review the implementation and test it against your own account before relying on it.

The project separates authentication, read-only API access, calendar conversion, and message delivery:

```text
Stay Informed credentials -> stayinformed-token -> short-lived token file
                                                   |             |
                                                   v             v
                                                webcal      message2mail
                                                   |             |
                                                   v             v
                                               live ICS       SMTP + IMAP
                                                                  |
                                                                  v
                                                        durable delivery state
```

The API client never receives a password, refreshes tokens, or retries requests. `webcal` has no calendar cache and fetches the current event list for every request. `message2mail` uses a durable delivery ledger plus IMAP reconciliation because SMTP acceptance and a deterministic `Message-ID` alone do not guarantee exactly-once delivery.

This project uses an undocumented private API. Upstream changes can break it without notice. The news detail endpoint can be interpreted by the upstream service as viewing a message; message forwarding therefore cannot promise that upstream read state remains unchanged.

## Commands

### stayinformed-token

Perform one login and print only the access token:

```sh
stayinformed-token login \
  --username user@example.invalid \
  --password-command 'secret-tool lookup service example'
```

Run a token sidecar which logs in once, refreshes explicitly before expiry, and atomically replaces an access-token file:

```sh
stayinformed-token serve \
  --username-command 'printf "%s\n" "user@example.invalid"' \
  --password-command 'cat /var/run/secrets/stayinformed/password' \
  --output-file /run/stayinformed/access-token
```

The sidecar exits when login or refresh fails. It does not loop indefinitely on invalid credentials; a process supervisor can apply bounded restart backoff.

### webcal

For a private local listener, callers can provide the Stay Informed token directly:

```sh
webcal --listen 127.0.0.1:8787 --allow-caller-token
curl -H 'Authorization: Bearer TOKEN' http://127.0.0.1:8787/calendar.ics
```

Behind an authentication proxy, use a separate token file:

```sh
webcal --listen 0.0.0.0:8787 --token-file /run/stayinformed/access-token
```

This separation is security-critical: the reverse proxy owns the incoming `Authorization` header while webcal uses only the rotating token file for the upstream API.

### message2mail

`message2mail` lists news, renders MIME messages with attachments, submits them through authenticated SMTP, and confirms delivery by searching a target mailbox over IMAP. It also listens for IMAP IDLE changes and performs a bounded periodic fallback sync.

Run `message2mail -h` for all connection, state, retry, and bootstrap options. On the first run, existing messages are recorded without forwarding. Set `--forward-existing` only when intentionally importing the existing archive.

## Kubernetes

The Helm chart is in [`charts/stayinformed-go`](charts/stayinformed-go). It deliberately accepts only existing Secret names and never renders credentials into Helm release data.

Create credentials outside Helm, for example through SOPS, External Secrets, or a one-time administrative command:

```sh
kubectl -n example create secret generic stayinformed-credentials \
  --from-literal=username='user@example.invalid' \
  --from-literal=password='replace-me'
```

The password is mounted only into the token sidecar. The access token is shared with the application through a memory-backed `emptyDir`. `message2mail` always runs as a single replica with `Recreate` strategy and stores its delivery ledger on a PVC; a replicated storage class such as Longhorn can keep that small state available after a node failure.

See [`charts/stayinformed-go/examples/values.yaml`](charts/stayinformed-go/examples/values.yaml) for anonymized values and [`docs/authentik.md`](docs/authentik.md) for app-password authentication.

## Development

```sh
make test
helm lint charts/stayinformed-go --set credentials.existingSecret=example
```

No credentials or live response bodies belong in fixtures, logs, documentation, or commits.
