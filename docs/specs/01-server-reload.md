# Configuration reload

Part of [server requirements](01-server.md): the server reloads `config.yaml` automatically on change.

## Decisions

- Two triggers, one check: an `fsnotify` watch on the configuration directory for speed, and polling every 10 seconds
  as a fallback when events are lost. Each trigger compares the sha256 of `config.yaml` with the current revision and
  reloads only on a difference.
- A candidate that fails to load or validate is not applied. The previous configuration keeps serving; the error is
  logged, kept in the reload status, raised as the critical alert `config:invalid` and shown to administrators.
- Everything in `config.yaml` is reloadable. Environment variables need a restart.
- Each environment mounts its whole `config/` directory, so an edit reaches the container however the file is saved.

## Holder

`config.Holder` keeps the current configuration in an atomic pointer.

- `Current() *Config` returns the current snapshot. A request handler reads it once, so one request never mixes two
  configurations.
- `Status() ReloadStatus` returns `{Revision, LoadedAt, Error}`; `Revision` is `sha256:<hex>` of the file bytes.
- `Reload() (changed bool, err error)` loads the file; on success with a new revision it swaps the snapshot and
  clears `Error`; on failure it keeps the snapshot and records `Error`.

Every consumer (proxy, HTTP API, auth, login, budget, alerts, model statuses) takes the holder, never a fixed
`*config.Config`.

## Watcher

`config.Watch(ctx, holder, every, onChange)` watches the directory of the configuration file and ticks every `every`.
Events for any file in the directory and ticks call `Reload`. `onChange` runs after a successful swap; the server
logs `configuration reloaded` with the revision, refreshes the model catalog and publishes a `status` event. A watch
that cannot be created logs a warning and leaves polling in charge.

## Effects of a reload

- Users, roles and tiers apply on the next request: a removed user is refused, a changed tier changes the model list
  and budget.
- Budget limits apply to the next admission check; spend is unchanged.
- The `alerts` section may appear or disappear; the alert runner always runs and skips its checks while the section
  is absent.

## Reporting

- `config:invalid` (critical) is active while `Status().Error` is set; its message is the error.
- `GET /api/system` gives administrators `configuration: {revision, loaded_at, error}`; the Overview status widget
  shows them.
