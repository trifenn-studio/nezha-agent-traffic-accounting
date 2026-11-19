# Linux traffic accounting

Add `traffic` to the local Agent YAML configuration to enable traffic accounting. When `traffic` is omitted, the Agent uses its existing interface collector, default exclusions, and `nic_allowlist`.

```yaml
traffic:
  include: [example-uplink, example-tunnel]
  exclude: [example-tunnel]
  report: billing
  state_file: /var/lib/nezha-agent/traffic.json
  weights:
    example-uplink:
      rx_to_in_ppm: 1000000
      tx_to_out_ppm: 1500000
```

Replace the example names with local interfaces. List full interface names in `include`. Entries in `exclude` take precedence. A custom policy can select virtual interfaces. Diagnostics show missing interfaces, and only matching interfaces contribute to totals. Use either `traffic` or a nonempty `nic_allowlist`. Set `state_file` to a writable, persistent absolute path reserved for this Agent. Use paths distinct from the Agent configuration for both the ledger and its lock.

| Weight | Direction |
| --- | --- |
| `rx_to_in_ppm` | RX contributes to inbound traffic |
| `tx_to_in_ppm` | TX contributes to inbound traffic |
| `rx_to_out_ppm` | RX contributes to outbound traffic |
| `tx_to_out_ppm` | TX contributes to outbound traffic |

Weights are integers in parts per million (ppm). Use `1000000` for a multiplier of 1. Each weight can range from `0` to `1000000000`. Without an interface-specific rule, RX contributes to inbound traffic and TX contributes to outbound traffic at 1× each. Within an explicit rule, omitted weights are zero. For each output direction, the Agent multiplies the RX and TX deltas by their weights, adds the results, and divides by `1000000`. The ledger retains fractional bytes for the next sample.

`report: raw` reports the selected interfaces' unweighted totals. `report: billing` reports estimated totals after applying the configured weights. Both use the existing `NetInTransfer` and `NetOutTransfer` fields. Live rates always use unweighted RX and TX deltas from the selected interfaces. The ledger keeps both sets of totals. The Dashboard field names stay the same. A node note can identify the use of estimated totals.

## Sampling and recovery

- A new ledger, a newly selected interface, a returning interface, or a system reboot establishes a baseline from current counters and preserves previous totals. With the same boot ID and interface identity, restarting the Agent continues from the saved counters. If one direction's counter decreases, its current value is counted as the increment since that direction reset.
- The selection and weights saved at the previous successful sample apply to the interval that just ended. A successful sample records the new policy for subsequent intervals. This also applies to policy changes made while the Agent was stopped. Resets or topology changes that cannot be observed between samples can cause undercounting.
- The ledger uses an exclusive file lock, a temporary file in the same directory, file synchronization, atomic replacement, and directory synchronization. The Agent validates the ledger and its reporting mode at startup and restores totals with rates set to zero. A collection or persistence error during operation retains the last reported totals and sets rates to zero. If directory synchronization fails after replacement, the Agent keeps the in-memory baseline for the next sample.

## Diagnostics and reload

```sh
nezha-agent -c /path/to/config.yml traffic-check
nezha-agent -c /path/to/config.yml traffic-status
```

Both commands run locally and are read-only. `traffic-check` shows the interface selection, kernel counters, and potential overlaps. It uses the current observation as its baseline, so the diagnostic delta is zero. `traffic-status` shows the most recent ledger, including unweighted and estimated totals, fractional remainders, and the latest selection, events, and interval weights. Overlap hints identify some tunnels and lower-layer interfaces visible to the kernel for manual review.

After editing `include`, `exclude`, or `weights` in the local YAML, send `SIGHUP` to the Linux Agent with accounting enabled. The Agent publishes only the new traffic policy for the next sample to read. If a Dashboard configuration change is pending, wait for it to complete, edit the local policy, and retry. Restart the Agent to enable or disable traffic accounting or change `state_file` or `report`. Each ledger is tied to its original reporting mode. To change modes, use a separate ledger and coordinate the baseline with the Dashboard.

Dashboard configuration changes and server migration tasks preserve the local traffic policy. The interactive `edit` command preserves `traffic`. Use the YAML to change interface selection when accounting is enabled. The Agent blocks official automatic and forced updates while accounting is enabled to preserve the ledger and reporting mode.

The Agent relies on the local ledger for continuous totals. The Dashboard maintains its own history and reporting baseline. Verify initial enrollment, restarts, and long periods with traffic in only one direction against the target Dashboard version.
