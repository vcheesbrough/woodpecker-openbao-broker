# woodpecker-openbao-broker

Bridges Woodpecker CI's external-secret API to [OpenBao](https://openbao.org)
(or HashiCorp Vault — wire-compatible). Secrets stay in OpenBao; the broker
fetches them per request and returns them in Woodpecker's secret format.

[![CI](https://github.com/vcheesbrough/woodpecker-openbao-broker/actions/workflows/ci.yml/badge.svg)](https://github.com/vcheesbrough/woodpecker-openbao-broker/actions/workflows/ci.yml)
[![GHCR](https://img.shields.io/github/v/release/vcheesbrough/woodpecker-openbao-broker?logo=docker&label=ghcr.io&color=blue)](https://github.com/vcheesbrough/woodpecker-openbao-broker/pkgs/container/woodpecker-openbao-broker)

---

## Quickstart

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

Public key, fetched from Woodpecker:

```sh
curl -fsS -H "Authorization: Bearer $WOODPECKER_TOKEN" \
  https://woodpecker.example.com/api/signature/public-key > pubkey.pem
```

On the Woodpecker server:

```sh
WOODPECKER_SECRET_EXTENSION_ENDPOINT=http://woodpecker-broker:8080/secrets
```

Pipeline usage is identical to native secrets:

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
| `OPENBAO_ADDR` | yes | — | OpenBao/Vault URL |
| `OPENBAO_ROLE_ID` | yes | — | AppRole `role_id` |
| `OPENBAO_SECRET_ID` | yes | — | AppRole `secret_id` |
| `OPENBAO_NAMESPACE` | no | — | OpenBao namespace (Enterprise/Vault) |
| `OPENBAO_KV_MOUNT` | no | `secret` | KV-v2 mount name |
| `SECRET_PATH_TEMPLATES` | no | _empty_ | [Go templates](https://pkg.go.dev/text/template) rendered per request; see [Path templates](#path-templates) |
| `WOODPECKER_PUBLIC_KEY_FILE` | one of | — | Path to Woodpecker's ed25519 public key (PEM) |
| `WOODPECKER_URL` | one of | — | Alternative: fetch the key from this server at startup |
| `WOODPECKER_TOKEN` | with `WOODPECKER_URL` | — | Token used to fetch the key |
| `LISTEN_ADDR` | no | `:8080` | Bind address |

Prefer `WOODPECKER_PUBLIC_KEY_FILE` in production — no startup-time
dependency on Woodpecker. After migration, `OPENBAO_ROLE_ID` and
`OPENBAO_SECRET_ID` should be the only secrets left in Woodpecker.

---

## Path templates

`SECRET_PATH_TEMPLATES` is a list of Go templates evaluated per request.
Separate entries with commas, newlines, or both. Each rendered path is read
from KV-v2; results are merged in declared order, later paths win.

Fields:

- `{{.Repo.FullName}}` — `org/repo`
- `{{.Repo.Owner}}`
- `{{.Repo.Name}}`
- `{{.Repo.ForgeID}}`
- `{{.Pipeline.Branch}}`
- `{{.Pipeline.Event}}` — `push`, `pull_request`, `tag`, `manual`, …

Missing paths and 403s are skipped silently. A typo in a field name fails
the request (`missingkey=error`).

### Examples

Single global:

```
SECRET_PATH_TEMPLATES=woodpecker/global
```

Per-repo:

```
SECRET_PATH_TEMPLATES=woodpecker/repos/{{.Repo.FullName}}
```

Layered global → repo → branch, multi-line in `docker-compose.yml`:

```yaml
environment:
  SECRET_PATH_TEMPLATES: |
    woodpecker/global
    woodpecker/repos/{{.Repo.FullName}}
    woodpecker/repos/{{.Repo.FullName}}/branches/{{.Pipeline.Branch}}
```

Equivalent comma-separated form:

```
SECRET_PATH_TEMPLATES=woodpecker/global,woodpecker/repos/{{.Repo.FullName}},woodpecker/repos/{{.Repo.FullName}}/branches/{{.Pipeline.Branch}}
```

---

## Compatibility

| Component | Tested |
|---|---|
| Woodpecker | v3.x (built against `v3.13.0`) |
| OpenBao | 2.5.x (CI: `openbao/openbao:2.5.3`) |
| HashiCorp Vault | untested; expected to work |

---

## Threat model

The broker is a trusted in-network bridge — it sees the plaintext of every
pipeline secret. `/secrets` requires a valid Woodpecker ed25519 signature;
`/health` is the only unauthenticated route. AppRole credentials live in
environment variables on the broker host, so the perimeter is the AppRole
policy plus container-runtime isolation. Secret values are not logged (only
paths, key names, counts). Woodpecker does not mask external secrets in
step logs, so don't `echo` them in pipelines.

---

## Contributing

```sh
# unit
go test ./...
```

```sh
# integration — requires a running OpenBao
docker run --rm -d --name bao-dev \
  -e BAO_DEV_ROOT_TOKEN_ID=root -p 8200:8200 \
  openbao/openbao:2.5.3

BAO_ADDR=http://127.0.0.1:8200 BAO_TOKEN=root BAO_KV_MOUNT=secret \
  go test ./...
```

```sh
# lint
golangci-lint run
```

CI runs all three on every PR. Integration tests skip when `BAO_ADDR` is
unset, so a plain `go test ./...` works on a fresh checkout.

---

## License

Apache-2.0 — see [LICENSE](LICENSE).
