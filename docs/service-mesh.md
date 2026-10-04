# Service mesh integration

cfgate can share a cluster with another Gateway API controller when each Gateway
selects the intended GatewayClass. cfgate reconciles classes whose controller name
is `cfgate.io/cloudflare-tunnel-controller`; mesh-specific classes remain with
their own implementation. Shared API kinds do not imply identical routing features.
See [the supported subset](gateway-api-primer.md#supported-httproute-behavior).

## Kiali

Kiali may report `KIA1504` for a GatewayClass it does not recognize. If that is the
only warning, inspect cfgate's Gateway and route conditions before treating it as
a forwarding failure. Add the actual class name to Kiali's configuration when
that class is intentionally managed by cfgate.

### Kiali CR

If you manage Kiali through the Kiali Operator, add the `cfgate` class under `spec.external_services.istio.gateway_api_classes`:

```yaml
spec:
  external_services:
    istio:
      gateway_api_classes:
        - class_name: "istio"
          name: "Istio"
        - class_name: "cfgate"
          name: "cfgate"
```

### Kiali ConfigMap

If you deploy Kiali without the Operator, apply the same configuration in the Kiali ConfigMap:

```yaml
external_services:
  istio:
    gateway_api_classes:
      - class_name: "istio"
        name: "Istio"
      - class_name: "cfgate"
        name: "cfgate"
```

Setting `gateway_api_classes` explicitly replaces Kiali's auto-discovery. Include all `GatewayClass` resources you want Kiali to recognize (for example, `istio`, `istio-remote`, `cfgate`).

See the [Kiali CR Reference](https://kiali.io/docs/configuration/kialis.kiali.io/) for all configuration options.
