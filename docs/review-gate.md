# The three-lens review gate

Every pull request in this repository passes through three review lenses —
minimalism, maintainability and security — in that order. It lands only when
all three lenses hold an undismissed approval at the same head; a push
invalidates the approvals that came before it, sending the change back
through the chain.

The engine's side of the gate is specified in
[§forgejo/decisions/review-lenses](../specification/forgejo/decisions.md#review-lenses).
The forge enforces the same rule independently through branch protection, so
neither the engine nor a human can land a pull request on the strength of a
single reviewer.
