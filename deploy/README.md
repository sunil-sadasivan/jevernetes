# Controller configuration examples (no deployment)

The base preserves TypeSafe risk classification, exact grouping, one replica and
namespace-scoped read-only pod/log RBAC. It does not enable learning. No Secret values
or deployable image are supplied. Run `python3 tools/check_controller_artifacts.py`
for static checks and `kubectl kustomize deploy/base` for local rendering only.

Prefer mounted Secret files. The base projects these optional Secrets as read-only:

| Secret | Key | Environment reference |
| --- | --- | --- |
| `controller-openai` | `api-key` | `OPENAI_API_KEY_FILE=/var/run/jevernetes-secrets/openai-key` |
| `controller-anthropic` | `api-key` | `ANTHROPIC_API_KEY_FILE=/var/run/jevernetes-secrets/anthropic-key` |

These files are never read unless the corresponding provider is selected. TypeSafe's
`controller-jev` projection remains required by the compatibility base; an operator
switching entirely away from TypeSafe may remove that projection and its environment
entry in an overlay. Never grant the controller API permission to read Secrets.
Kubelet projects the approved Secret volumes. Restart the process to rotate keys.

To select a different risk provider, add `--risk-provider openai` or `anthropic`,
`--model` with a supported pinned revision, and both `--input-price`/`--output-price`
with agreement-specific rates. The software does not supply new-provider prices.

For shadow learning add `--grouping-strategy semantic`, `--template-provider openai`
or `anthropic`, `--template-model`, `--template-input-price`, and
`--template-output-price`. Optional bounds include `--template-min-support 4`,
`--template-capacity 64`, `--template-ttl 300`, `--template-max-requests 10` and
`--template-max-cost 0.05`. Use `--output` in a private writable volume to save final
candidates. Do not alias the SQLite state or sidecars. Shadow mode classifies all
events, adds bounded proposal work, and never activates a rule or saves classifications.

Review [provider/learning behavior](../docs/provider-template-learning.md) and
[controller operation](../docs/controller.md) before preparing an overlay. Set approved
egress to only the selected provider and existing notification destination. Preserve
non-root/read-only filesystem settings, self-log exclusion and read-only RBAC.
