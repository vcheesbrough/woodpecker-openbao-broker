# woodpecker-openbao-broker

A small Go HTTP service that bridges [Woodpecker CI](https://woodpecker-ci.org)'s
external secret-extension API to [OpenBao](https://openbao.org) (also works
with HashiCorp Vault — the API is wire-compatible). Pipeline secrets live in
OpenBao at runtime instead of Woodpecker's database; the broker reads them
on each request and returns them in Woodpecker's secret format.

[![CI](https://github.com/vcheesbrough/woodpecker-openbao-broker/actions/workflows/ci.yml/badge.svg)](https://github.com/vcheesbrough/woodpecker-openbao-broker/actions/workflows/ci.yml)

---

## Quickstart

Run the broker on the same Docker network as your Woodpecker server:

```sh
docker run --rm -p 8080:8080 \
  -e OPENBAO_ADDR=https://bao.example.com \
  -e OPENBAO_ROLE_ID=$ROLE_ID \
  -e OPENBAO_SECRET_ID=$SECRET_ID \
  -e SECRET_PATH_TEMPLATES='woodpecker/global,woodpecker/repos/{{.Repo.FullName}}' \
  -e WOODPECKER_PUBLIC_KEY_FILE=/etc/woodpecker/pubkey.pem \
  -v $PWD/pubkey.pem:/etc/woodpecker/pubkey.pem:ro \
  ghcr.io/vcheesbrough/woodpecker-openbao-broker:latest
```

Get the public-key file from your Woodpecker server first:

```sh
curl -fsS -H "Authorization: Bearer $WOODPECKER_TOKEN" \
  https://woodpecker.example.com/api/signature/public-key > pubkey.pem
```

Point Woodpecker at the broker:

```sh
WOODPECKER_SECRET_EXTENSION_ENDPOINT=http://woodpecker-broker:8080/secrets
```

Use `from_secret:` in a pipeline as you would for native secrets — the
broker's response is injected identically:

```yaml
steps:
  - name: push
    image: alpine
    commands: [ "echo $REGISTRY_USER" ]
    environment:
      REGISTRY_USER:
        from_secret: registry_user
```

---

## Configuration

| Variable | Required | Default | Purpose |
|---|---|---|---|
| `OPENBAO_ADDR` | yes | — | OpenBao/Vault URL, e.g. `https://bao.example.com` |
| `OPENBAO_ROLE_ID` | yes | — | AppRole `role_id` |
| `OPENBAO_SECRET_ID` | yes | — | AppRole `secret_id` |
| `OPENBAO_NAMESPACE` | no | — | OpenBao namespace (Enterprise/Vault only) |
| `OPENBAO_KV_MOUNT` | no | `secret` | KV-v2 mount name |
| `SECRET_PATH_TEMPLATES` | no | _empty_ | Comma-separated list of [Go templates](https://pkg.go.dev/text/template) rendered per request; see [Path templates](#path-templates) |
| `WOODPECKER_PUBLIC_KEY_FILE` | one of | — | Path to the Woodpecker server's ed25519 public key (PEM) |
| `WOODPECKER_URL` | one of | — | Alternative: fetch the public key from this Woodpecker server at startup |
| `WOODPECKER_TOKEN` | with `WOODPECKER_URL` | — | Token used to fetch the public key |
| `LISTEN_ADDR` | no | `:8080` | Bind address |

Either `WOODPECKER_PUBLIC_KEY_FILE` **or** `WOODPECKER_URL` + `WOODPECKER_TOKEN`
must be set. The first is preferred for production — it removes a startup
network dependency on Woodpecker. `OPENBAO_ROLE_ID` and `OPENBAO_SECRET_ID`
should be the only secrets remaining in Woodpecker's native secret store.

---

## Path templates

`SECRET_PATH_TEMPLATES` is a comma-separated list of Go templates evaluated
against the inbound Woodpecker request. Each rendered path is read from KV-v2
and merged into the response; later paths override earlier keys.

Available fields:

- `{{.Repo.FullName}}` — `org/repo`
- `{{.Repo.Owner}}`
- `{{.Repo.Name}}`
- `{{.Repo.ForgeID}}`
- `{{.Pipeline.Branch}}`
- `{{.Pipeline.Event}}` — `push`, `pull_request`, `tag`, `manual`, …

Missing paths and 403s are skipped silently — empty values are a normal
outcome, not a pipeline failure.

### Examples

**Single global path** — every pipeline gets the same secrets:

```
SECRET_PATH_TEMPLATES=woodpecker/global
```

**Per-repo only** — no shared baseline:

```
SECRET_PATH_TEMPLATES=woodpecker/repos/{{.Repo.FullName}}
```

**Layered global → repo → branch** — repo overrides global, branch overrides
repo. A `staging`/`production` deploy pipeline pattern:

```
SECRET_PATH_TEMPLATES=woodpecker/global,woodpecker/repos/{{.Repo.FullName}},woodpecker/repos/{{.Repo.FullName}}/branches/{{.Pipeline.Branch}}
```

A typo in a template field name fails the request rather than silently
rendering to nothing — `missingkey=error` is set on the parser.

---

## Compatibility

| Component | Tested |
|---|---|
| Woodpecker | v3.x (built against the `v3.13.0` Go module) |
| OpenBao | 2.5.x (CI runs against `openbao/openbao:2.5.3`) |
| HashiCorp Vault | not tested but expected to work — wire-compatible KV-v2 + AppRole |

---

## Threat model

The broker is a **trusted in-network bridge**: it sees the plaintext value of
every pipeline secret and authenticates Woodpecker callers with an ed25519
HTTP message-signature checked on every request (`/secrets`); only `/health`
is unauthenticated. AppRole credentials live in environment variables on the
broker host, so the deployment surface is the AppRole policy combined with
your container runtime's process isolation. **Secret values are never written
to logs or error messages** — the broker logs paths, key names, and counts
only. Woodpecker cannot mask external secrets in step logs, so pipeline
authors must still avoid `echo`-ing values into pipeline output.

---

## Contributing

Run unit tests against fakes:

```sh
go test ./...
```

Run integration tests against a real OpenBao. The simplest local setup is
[`openbao/devbao`](https://github.com/openbao/devbao):

```sh
docker run --rm -d --name bao-dev \
  -e BAO_DEV_ROOT_TOKEN_ID=root -p 8200:8200 \
  openbao/openbao:2.5.3

BAO_ADDR=http://127.0.0.1:8200 \
BAO_TOKEN=root \
BAO_KV_MOUNT=secret \
go test ./...
```

Lint:

```sh
golangci-lint run
```

CI runs all three on every PR. Tests gated on `BAO_ADDR` are skipped when
the variable is unset, so `go test ./...` works on a clean machine.

---

## License

Apache-2.0 — see [LICENSE](LICENSE).
