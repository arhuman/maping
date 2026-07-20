# mAPI-ng

mAPI-ng is hosted, zero-config API observability for Go services. Add one
middleware call and set one environment variable, and a service starts
reporting **rate, error rate, and duration (RED)** for every HTTP endpoint to
a dashboard, with no Prometheus, no Grafana, and no YAML to write.

It is built for teams running Go HTTP services who want endpoint-level RED
metrics without standing up and operating a metrics stack. It does not
replace distributed tracing or a general-purpose dashboarding tool; it
answers "which endpoints are slow or failing, and how has that changed"
in the time it takes to set an environment variable.

## Documentation

- [Quickstart](/doc/quickstart) - get metrics flowing in minutes
- [What data is collected](/doc/data-collected) - the exact fields shipped, and what is not collected
- [Runtime overhead](/doc/runtime-overhead) - why instrumenting a hot path is safe
- [Failure & retry behaviour](/doc/failure-retry) - what happens when the key is missing or the collector is unreachable
- [Security & data flow](/doc/security-data-flow) - how data moves from your service to the dashboard, and how it is isolated
- [Self-hosting](/doc/self-hosting) - run the whole stack yourself
- [Architecture](/doc/architecture) - control plane, data plane, and the dashboard
- [Benchmarks](/doc/benchmarks) - measured client and server overhead
- [Licensing](/doc/licensing) - MIT, and what that means for self-hosting
