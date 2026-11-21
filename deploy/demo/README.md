# Demo

Wrench with everything faked: the OpenRouter-compatible LLM mock, the GitLab mock with an account picker,
PostgreSQL and 30 days of seeded history. Nothing reaches the internet and no secrets are needed.

## Start

From the repository root:

```sh
make demo        # build, start, wait, seed; prints http://localhost:18080
make demo-logs   # follow the logs
make demo-down   # stop and keep the data
make demo-reset  # stop and delete the data
```

Open <http://localhost:18080>, choose "Sign in with GitLab" and pick a user (`user3@example.com` is an admin).

| Service      | Address                    |
| ------------ | -------------------------- |
| Wrench       | `http://localhost:18080`   |
| GitLab mock  | `http://localhost:19000`   |
| LLM mock     | `http://localhost:18000`   |

## Notes

- The stack runs in HTTP-only mode (`WRENCH_HTTP_ONLY=true`): cookies are not Secure. Never expose it.
- `config/` is mounted at `/etc/wrench`; edits of `config/config.yaml` apply without a restart.
- The seed fills an empty database only; `make demo` on existing data seeds nothing. `make demo-reset` starts over.
- `make e2e` runs the same compose file under its own project (`wrench-e2e`) without the seed.
