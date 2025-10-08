# Control Plane Isolation Test Data

Legacy behaviour:
- Worker-only feedback; no escalation to control-plane peers.
- Diagnostics run only after worker peers declare CR.
- Isolation inferred late, leading to false healthy outcome.

Redesigned behaviour expectations:
- workers report CR -> query control-plane quorum -> treat absence as unhealthy.
- isolate after `PeerQuorumTimeout` without waiting for worker chatter.

This file documents test expectations to keep the scenario reproducible.
