// Builds the CheckLimit JSON request body from proxy flow variables
// (FR6). Runs before SC-Unigate-CheckLimit.
//
// Per-proxy/per-API-product configuration is expected to set these
// flow variables earlier in the flow (e.g. via an AssignMessage or
// ExtractVariables step, or a Flow Callout param), so this script
// stays generic across every proxy that attaches the shared flow:
//   unigate.rule_id    - required, which Unigate rule to evaluate
//   unigate.key_parts   - comma-separated: "ip", "consumer_username",
//                          "header:<Name>" (default "ip")
//   unigate.namespace   - optional namespace override
//   unigate.cost        - optional request weight (default 1)
var keyPartsRaw = context.getVariable('unigate.key_parts') || 'ip';
var keyParts = keyPartsRaw.split(',');
var key = [];

for (var i = 0; i < keyParts.length; i++) {
    var part = keyParts[i].replace(/^\s+|\s+$/g, '');
    var value = null;
    var kind = part;

    if (part === 'ip') {
        value = context.getVariable('client.ip');
    } else if (part === 'consumer_username') {
        kind = 'username';
        value = context.getVariable('client.username') || context.getVariable('apiproduct.developer.email');
    } else if (part.indexOf('header:') === 0) {
        var headerName = part.substring('header:'.length);
        kind = headerName;
        value = context.getVariable('request.header.' + headerName);
    }

    if (value) {
        key.push({ kind: kind, value: String(value) });
    }
}

var body = {
    rule_id: context.getVariable('unigate.rule_id'),
    key: key,
    cost: Number(context.getVariable('unigate.cost') || 1),
    gateway: 'apigee',
    namespace: context.getVariable('unigate.namespace') || ''
};

context.setVariable('unigate.request.body', JSON.stringify(body));
