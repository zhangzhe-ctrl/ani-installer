# Independent Higress gateway

Official Higress {{ (index .ani.components "higress").version }} is owned by this
installation in `higress-system`, Helm release `ani-higress`. The shared Gateway
API CRDs are checked and reused without replacing KCN's Envoy resources.

GatewayClass: `{{ (index .ani.components "higress").gateway_class }}`.
Gateway: `higress-system/{{ (index .ani.components "higress").gateway_name }}`.
HTTP NodePort: `{{ (index .ani.components "higress").http_port }}`, served on each
declared node. HTTPS NodePort `{{ (index .ani.components "higress").https_port }}`
is reserved; it is available only when the configured named TLS Secret and
HTTPS listener pass their explicit checks. Allowed namespace names are bound
by the rendered Gateway selector; the temporary probe namespace is removed.

The four OCI Wasm artifacts and module content digests are recorded in the
source-approved `wasm/wasm.lock.json`. Registry mode:
`{{ (index .ani.components "higress").wasm_registry }}`. Harbor mode uses its
verified CA copied to `higress-system/ani-higress-wasm-trust`; no global insecure
registry setting is installed. The bootstrap material mode uses only this
site's declared material registry transport and exclusive filesystem additions.

The platform WasmPlugin objects keep credential-free disabled defaults. The
formal external-engine-boundary probe temporarily activates all four, asserts
body.model routing, key rejection/acceptance and 429, hashes the actual loaded
Wasm files, then restores defaults and normally deletes its identity and route.
ANI subsequently owns business model mappings, API consumers, rate keys and
HTTPRoute publication. No business key is provisioned by this installation.
