// Parses the Unigate CheckLimit response and sets the flow variables
// that RF-Unigate-RateLimited (block path) and AM-Unigate-SetHeaders
// (allow path) read (FR6, FR7). Runs after SC-Unigate-CheckLimit.
//
// unigate.fail_open (flow variable, "true"/"false") controls what
// happens if the ServiceCallout itself failed or timed out - this is
// the adapter-level circuit breaker, independent of the rule's own
// server-side fail_open/fail_closed which only applies once the
// request reaches the service.
var failOpen = context.getVariable('unigate.fail_open') === 'true';
var calloutFailed = context.getVariable('SC-Unigate-CheckLimit.failed');

function blockWithServiceError(message) {
    context.setVariable('unigate.blocked', !failOpen);
    context.setVariable('unigate.status_code', 503);
    context.setVariable('unigate.message', message);
}

if (calloutFailed) {
    blockWithServiceError('rate limit service unavailable');
} else {
    var raw = context.getVariable('unigate.response.content');
    var decision = null;
    try {
        decision = JSON.parse(raw);
    } catch (e) {
        decision = null;
    }

    if (!decision) {
        blockWithServiceError('invalid rate limit service response');
    } else {
        if (decision.limit !== undefined && decision.limit !== null) {
            context.setVariable('unigate.header.limit', String(decision.limit));
        }
        if (decision.remaining !== undefined && decision.remaining !== null) {
            context.setVariable('unigate.header.remaining', String(decision.remaining));
        }
        if (decision.reset_seconds !== undefined && decision.reset_seconds !== null) {
            context.setVariable('unigate.header.reset', String(decision.reset_seconds));
        }

        if (decision.allow) {
            context.setVariable('unigate.blocked', false);
        } else {
            context.setVariable('unigate.blocked', true);
            context.setVariable('unigate.status_code', 429);
            context.setVariable('unigate.header.retry_after', String(decision.retry_after_seconds || 1));
            context.setVariable(
                'unigate.message',
                decision.locked_out
                    ? 'temporarily locked out due to repeated violations'
                    : 'rate limit exceeded'
            );
        }
    }
}
