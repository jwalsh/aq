# aq Config Schema Specification

The config file at `~/.aq/config.json` (or `.aq/config.json` for local-first)
is the contract between the aq binary and transport plugins.

## Schema Version

Every config file MUST have a `config_version` integer field. This is the
schema version, not the aq binary version. It increments only when the
config schema has a breaking change.

```json
{
  "config_version": 1,
  "default_channel": "broadcast",
  "default_ttl": 3600
}
```

## Version History

| config_version | aq version | Breaking change |
|---|---|---|
| 1 | 0.4.0+ | Initial versioned schema. Replaces unversioned `version: "0.1.0"` string field. |

## Migration Rules

`aq doctor` MUST check config_version and report:

- **Missing config_version**: warn + offer `aq doctor --migrate`
- **config_version < current**: warn + offer `aq doctor --migrate`
- **config_version == current**: ok
- **config_version > current**: error (config from a newer aq version)

`aq doctor --migrate` applies migrations forward:

- **Unversioned → v1**: rename `version` string to `config_version` integer 1, ensure `default_ttl` is 3600 (not 300)
- **v1 → v2** (future): TBD

Migrations are idempotent. Running `--migrate` on an already-current config is a no-op.

## Schema v1 Fields

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `config_version` | integer | yes | 1 | Schema version |
| `default_channel` | string | no | `"broadcast"` | Default channel name |
| `default_ttl` | integer | no | 3600 | Default TTL in seconds |
| `mqtt` | object | no | (disabled) | MQTT transport config |
| `mqtt.enabled` | boolean | no | false | Enable MQTT fanout |
| `mqtt.host` | string | no | `"localhost"` | Broker hostname |
| `mqtt.port` | integer | no | 1883 | Broker port |
| `mqtt.topic` | string | no | `"aq"` | Topic prefix |
| `mdns` | object | no | (disabled) | mDNS config |
| `mdns.enabled` | boolean | no | false | Enable mDNS |
| `mdns.service_type` | string | no | `"_aq._tcp"` | Service type |
| `mdns.domain` | string | no | `"local"` | mDNS domain |
| `mesh` | object | no | (disabled) | Meshtastic config |
| `mesh.enabled` | boolean | no | false | Enable mesh |
| `mesh.via` | string | no | `"serial"` | Transport: serial or mqtt |

## CLI Compatibility

CLI flags and subcommands follow semver at the binary level:

- **Patch** (0.x.Y): no CLI changes
- **Minor** (0.X.0): new subcommands or flags allowed, existing ones stable
- **Major** (X.0.0): breaking CLI changes allowed with deprecation warnings one minor version prior

`aq doctor` reports the binary version and config_version together so
operators can diagnose version skew between the binary and the config.

## Environment Override Precedence

1. CLI flags (highest)
2. Environment variables (`AQ_MQTT_HOST`, etc.)
3. Config file (`~/.aq/config.json`)
4. Defaults (lowest)

This order is a contract. Transports MUST respect it.
