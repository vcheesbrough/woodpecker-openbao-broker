# woodpecker-openbao-broker

A small HTTP service that bridges [Woodpecker CI](https://woodpecker-ci.org)'s external secret-extension API to [OpenBao](https://openbao.org) (Vault-compatible), so pipeline secrets are fetched from OpenBao at runtime rather than stored in Woodpecker's database.

> Status: pre-release scaffold. See `phases A–E` in the project board.

---

## Quickstart

_Filled in during Phase D._

```sh
docker run --rm -p 8080:8080 \
  -e OPENBAO_ADDR=https://bao.example.com \
  -e OPENBAO_ROLE_ID=... \
  -e OPENBAO_SECRET_ID=... \
  -e SECRET_PATH_TEMPLATES='woodpecker/global,woodpecker/repos/{{.Repo.FullName}}' \
  -e WOODPECKER_PUBLIC_KEY_FILE=/etc/woodpecker/pubkey.pem \
  -v $PWD/pubkey.pem:/etc/woodpecker/pubkey.pem:ro \
  ghcr.io/vcheesbrough/woodpecker-openbao-broker:latest
```

On the Woodpecker server:

```sh
WOODPECKER_SECRET_EXTENSION_ENDPOINT=http://woodpecker-broker:8080/secrets
```

In a pipeline:

```yaml
steps:
  - name: deploy
    image: alpine
    commands: [ "echo $REGISTRY_USER" ]
    environment:
      REGISTRY_USER:
        from_secret: registry_user
```

---

## Configuration

_Env-var reference table filled in during Phase D._

| Variable | Purpose |
|---|---|
| `OPENBAO_ADDR` | OpenBao server URL |
| `OPENBAO_ROLE_ID` | AppRole role_id |
| `OPENBAO_SECRET_ID` | AppRole secret_id |
| `OPENBAO_NAMESPACE` | (optional) OpenBao namespace |
| `OPENBAO_KV_MOUNT` | KV-v2 mount name (default `secret`) |
| `SECRET_PATH_TEMPLATES` | Comma-separated Go-template paths, evaluated per request |
| `WOODPECKER_PUBLIC_KEY_FILE` | Path to Woodpecker's ed25519 public key (PEM) |
| `WOODPECKER_URL` | Alternative: fetch the public key from this Woodpecker server |
| `WOODPECKER_TOKEN` | Token used with `WOODPECKER_URL` |
| `LISTEN_ADDR` | Bind address, default `:8080` |
| `LOG_LEVEL` | `debug`, `info` (default), `warn`, `error` |

---

## Path templates

`SECRET_PATH_TEMPLATES` is a comma-separated list of [Go templates](https://pkg.go.dev/text/template) evaluated against the incoming Woodpecker request. Each path resolves to a KV-v2 read; results are merged in declared order, later wins.

Available fields:

- `{{.Repo.FullName}}`
- `{{.Repo.Owner}}`
- `{{.Repo.Name}}`
- `{{.Repo.ForgeID}}`
- `{{.Pipeline.Branch}}`
- `{{.Pipeline.Event}}`

Examples filled in during Phase D.

---

## Threat model

_One paragraph filled in during Phase D — covers signature verification, AppRole creds in env, no secret values logged._

---

## Contributing

_Local development notes filled in during Phase D — running tests against [`openbao/devbao`](https://github.com/openbao/devbao)._

---

## License

Apache-2.0 — see [LICENSE](LICENSE).
