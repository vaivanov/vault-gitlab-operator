# vault-gitlab-operator

Syncs secrets from HashiCorp Vault (KV v2) into GitLab CI/CD variables —
at **instance**, **group** and **project** level — so runners never need
network access to Vault at job runtime. Think
[vault-secrets-operator](https://github.com/hashicorp/vault-secrets-operator),
but the destination is GitLab variables instead of Kubernetes Secrets,
and configuration is a plain YAML file instead of CRDs.

```
Vault (KV v2)  --poll metadata-->  vault-gitlab-operator  --REST API-->  GitLab CI/CD variables
```

## How it works

- A reconcile loop builds the desired state from the config file plus the
  referenced Vault secrets, lists the actual variables via the GitLab
  API, diffs, and applies **creates and updates only** — the operator
  **never deletes** anything, so manually-created variables are safe.
- Change detection is cheap: each pass first reads KV v2 **metadata**
  (`current_version`) and only fetches secret data when the version
  changed (Vault event notifications are Enterprise-only; polling is the
  Community-edition pattern, same as vault-secrets-operator's default).
- All GitLab variable attributes are supported: `variable_type`
  (`env_var`/`file`), `protected`, `masked`, `raw`, `environment_scope`,
  `description`.
- Masked values are pre-validated against GitLab's masking rules (≥8
  chars, single line, restricted charset) before any API call.
- Secret values never appear in logs or diff output — a dedicated string
  type redacts them in every rendering path, with tests enforcing it.

## Commands

| Command | Purpose | Exit codes |
|---|---|---|
| `validate` | Parse and validate the config | 0 ok, 2 invalid |
| `diff` | Show pending changes, write nothing | 0 ok, 1 errors, 3 with `--exit-nonzero-on-diff` and pending changes |
| `once` | One reconcile pass (cron-friendly) | 0 ok, 1 sync errors, 2 config/auth error |
| `daemon` | Continuous loop + `/healthz` `/readyz` `/metrics` | runs until SIGTERM |

Common flags: `--config/-c` (default `config.yaml`), `--log-level`,
`--log-format text|json`. Daemon: `--listen` (default `:8080`, empty
disables). `SIGHUP` reloads the config (a changed `sync.interval`
requires a restart).

## Configuration

See [examples/config.yaml](examples/config.yaml) for the full annotated
reference. The essentials:

```yaml
vault:
  address: https://vault.internal:8200
  auth:
    method: approle            # token | approle | kubernetes
    approle:
      role_id_env: VGO_VAULT_ROLE_ID
      secret_id_file: /etc/vgo/vault-secret-id

gitlab:
  url: https://gitlab.internal
  token_env: GITLAB_TOKEN      # plaintext tokens cannot appear in this file

defaults:                      # merged into every variable spec
  vault_mount: kv

bundles:                       # reusable variable sets
  backend-common:
    - key: SENTRY_DSN
      vault: { path: ci/common/sentry, field: dsn }

targets:
  instance:                    # needs an admin PAT
    variables:
      - key: GLOBAL_REGISTRY_PASSWORD
        vault: { path: ci/registry, field: password }
        masked: true
  groups:
    - group: platform          # path or numeric ID; needs Owner
      bundles: [backend-common]
  projects:
    - project: platform/backend  # path or numeric ID; needs Maintainer
      bundles: [backend-common]
      variables:
        - key: DB_PASSWORD
          vault: { path: ci/backend/db, field: password }
          masked: true
          protected: true
          environment_scope: production
        - from_secret: { path: ci/backend/dotenv }  # whole secret -> variables
          prefix: APP_                              # field db_url -> APP_DB_URL
```

Semantics worth knowing:

- **Identity** of a variable is `(key, environment_scope)`; the same key
  may exist in several scopes, updates use `filter[environment_scope]`.
- **Bundles** merge in listed order; an inline variable with the same
  identity overrides the bundle entry; any other duplicate is a config
  error.
- **`from_secret`** maps every field of a Vault secret to a variable:
  field names are upper-cased and sanitized (`db-url` → `DB_URL`),
  prefixed. Explicit keys always win over derived ones; collisions are
  reported as errors.
- **No pruning**: variables removed from the config simply stop being
  managed; delete them by hand if needed.
- **`raw` defaults to `true`** and is always sent explicitly (the GitLab
  API default flipped in 18.6).
- **Failure isolation**: a broken Vault path, target, or single API
  error never aborts the rest of the run; everything is collected into
  the run report (and metrics in daemon mode).
- `environment_scope` on group variables requires GitLab Premium
  (validation warns); it does not exist on instance variables
  (validation rejects).
- `masked_and_hidden` is intentionally unsupported: GitLab returns
  `value: null` for hidden variables, which would break drift detection.

## Tokens & permissions

| Level | Required role | Token |
|---|---|---|
| Project variables | Maintainer | PAT / project access token, `api` scope |
| Group variables | Owner | PAT / group access token, `api` scope |
| Instance variables | Administrator | admin PAT, `api` scope (+`admin_mode` if Admin Mode is on) |

Vault policy: `read` on `<mount>/data/<path>` and `<mount>/metadata/<path>`
for every referenced secret.

## Deployment

- **Kubernetes**: [examples/k8s/deployment.yaml](examples/k8s/deployment.yaml)
  — single replica (`Recreate`), Vault Kubernetes auth via the projected
  SA token, liveness/readiness probes, Prometheus annotations.
- **Cron**: run `once` on a schedule if a daemon is unwanted.
- **Local playground**: [examples/docker-compose.yaml](examples/docker-compose.yaml)
  with a dev-mode Vault.

### Metrics (daemon)

`vgo_sync_runs_total{result}`, `vgo_actions_total{op,target_kind}`,
`vgo_last_sync_success_timestamp_seconds`, `vgo_sync_duration_seconds`.

## Development

```sh
make test    # go test -race ./...
make lint    # golangci-lint
make build   # bin/vault-gitlab-operator
make cover   # coverage profile + total
```

The test suite runs entirely against in-process fakes of the Vault and
GitLab APIs (httptest) — no containers needed.
