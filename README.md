# woodpecker-openbao-broker

Tiny Go service that lets [Woodpecker CI](https://woodpecker-ci.org) fetch
pipeline secrets from [OpenBao](https://openbao.org) instead of stuffing them
into its own database. (Vault works too — same wire protocol.)

[![CI](https://github.com/vcheesbrough/woodpecker-openbao-broker/actions/workflows/ci.yml/badge.svg)](https://github.com/vcheesbrough/woodpecker-openbao-broker/actions/workflows/ci.yml)
[![GHCR](https://img.shields.io/github/v/release/vcheesbrough/woodpecker-openbao-broker?logo=docker&label=ghcr.io&color=blue)](https://github.com/vcheesbrough/woodpecker-openbao-broker/pkgs/container/woodpecker-openbao-broker)

The shape of it: Woodpecker POSTs a signed request, the broker checks the
signature, reads from OpenBao, hands the secrets back. Woodpecker injects
them into your pipeline like they'd always lived there. Your team gets one
place to manage secrets; you stop copy-pasting tokens into a web form.

---

## Quickstart

Spin up the broker next to your Woodpecker server (same Docker network):

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

You'll need Woodpecker's public key first — grab it:

```sh
curl -fsS -H "Authorization: Bearer $WOODPECKER_TOKEN" \
  https://woodpecker.example.com/api/signature/public-key > pubkey.pem
```

Tell Woodpecker the broker exists:

```sh
WOODPECKER_SECRET_EXTENSION_ENDPOINT=http://woodpecker-broker:8080/secrets
```

Use `from_secret:` exactly like you would for a native secret — Woodpecker
can't tell the difference:

```yaml
steps:
  - name: push
    image: alpine
    commands: [ "echo $REGISTRY_USER" ]
    environment:
      REGISTRY_USER:
        from_secret: registry_user
```

That's it.

---

## Configuration

| Variable | Required | Default | Purpose |
|---|---|---|---|
| `OPENBAO_ADDR` | yes | — | OpenBao/Vault URL, e.g. `https://bao.example.com` |
| `OPENBAO_ROLE_ID` | yes | — | AppRole `role_id` |
| `OPENBAO_SECRET_ID` | yes | — | AppRole `secret_id` |
| `OPENBAO_NAMESPACE` | no | — | OpenBao namespace (Enterprise/Vault only) |
| `OPENBAO_KV_MOUNT` | no | `secret` | KV-v2 mount name |
| `SECRET_PATH_TEMPLATES` | no | _empty_ | List of [Go templates](https://pkg.go.dev/text/template) rendered per request; see [Path templates](#path-templates) |
| `WOODPECKER_PUBLIC_KEY_FILE` | one of | — | Path to Woodpecker's ed25519 public key (PEM) |
| `WOODPECKER_URL` | one of | — | Alternative — fetch the key from this Woodpecker server at startup |
| `WOODPECKER_TOKEN` | with `WOODPECKER_URL` | — | Token used to fetch the key |
| `LISTEN_ADDR` | no | `:8080` | Bind address |

You need either `WOODPECKER_PUBLIC_KEY_FILE` **or** the `WOODPECKER_URL` +
`WOODPECKER_TOKEN` pair. The file version is nicer for production — no
"oops, Woodpecker isn't up yet" surprises at boot. Once you've migrated,
`OPENBAO_ROLE_ID` and `OPENBAO_SECRET_ID` should be the only two things
left sitting in Woodpecker's native secret store.

---

## Path templates

`SECRET_PATH_TEMPLATES` is a list of Go templates the broker evaluates
against each incoming request. Separate entries with commas, newlines, or
both — whichever reads nicer in your config. Every rendered path is read
from KV-v2 and merged into the response; later paths win ties.

Available fields:

- `{{.Repo.FullName}}` — `org/repo`
- `{{.Repo.Owner}}`
- `{{.Repo.Name}}`
- `{{.Repo.ForgeID}}`
- `{{.Pipeline.Branch}}`
- `{{.Pipeline.Event}}` — `push`, `pull_request`, `tag`, `manual`, …

Missing paths and 403s get skipped silently. That's by design: a missing
path is just "no extra secrets here", which is a perfectly fine answer.

### Examples

**One shared bucket** — every pipeline gets the same secrets:

```
SECRET_PATH_TEMPLATES=woodpecker/global
```

**Per-repo only** — no shared baseline:

```
SECRET_PATH_TEMPLATES=woodpecker/repos/{{.Repo.FullName}}
```

**Layered global → repo → branch** — repo overrides global, branch overrides
repo. Handy for `staging`/`production` deploy pipelines. The multi-line form
in `docker-compose.yml` keeps it scannable:

```yaml
environment:
  SECRET_PATH_TEMPLATES: |
    woodpecker/global
    woodpecker/repos/{{.Repo.FullName}}
    woodpecker/repos/{{.Repo.FullName}}/branches/{{.Pipeline.Branch}}
```

Same thing comma-separated, for shells and `.env` files:

```
SECRET_PATH_TEMPLATES=woodpecker/global,woodpecker/repos/{{.Repo.FullName}},woodpecker/repos/{{.Repo.FullName}}/branches/{{.Pipeline.Branch}}
```

Typo a field name? The request fails loudly instead of quietly rendering an
empty path. (`missingkey=error` is on; you're welcome.)

---

## Compatibility

| Component | Tested |
|---|---|
| Woodpecker | v3.x (built against the `v3.13.0` Go module) |
| OpenBao | 2.5.x (CI runs against `openbao/openbao:2.5.3`) |
| HashiCorp Vault | not tested but expected to work — wire-compatible KV-v2 + AppRole |

---

## Threat model

The broker is a trusted in-network bridge: it sees the plaintext value of
every pipeline secret. Every request to `/secrets` is verified against
Woodpecker's ed25519 signature; `/health` is the only route that doesn't
need one. The AppRole credentials live in environment variables on the
broker host, so your real perimeter is the AppRole policy combined with
your container runtime's process isolation.

Secret values never appear in logs or error messages — only paths, key
names, and counts. One thing the broker can't help with: Woodpecker doesn't
mask external secrets in step logs, so don't `echo $MY_SECRET` in your
pipeline. The broker can hand the value over carefully; what your pipeline
does with it is on you.

---

## Contributing

PRs welcome. Three things to run before pushing:

```sh
# unit tests, against fakes
go test ./...
```

```sh
# integration tests, against a real OpenBao. Easiest local setup
# is just the container:
docker run --rm -d --name bao-dev \
  -e BAO_DEV_ROOT_TOKEN_ID=root -p 8200:8200 \
  openbao/openbao:2.5.3

BAO_ADDR=http://127.0.0.1:8200 \
BAO_TOKEN=root \
BAO_KV_MOUNT=secret \
go test ./...
```

```sh
# lint
golangci-lint run
```

CI runs all three on every PR. Integration tests skip themselves when
`BAO_ADDR` is unset, so a plain `go test ./...` works on a fresh checkout
with nothing else running.

---

## License

Apache-2.0 — see [LICENSE](LICENSE).
