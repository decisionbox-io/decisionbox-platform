# Verifying image signatures

DecisionBox container images published to
`ghcr.io/decisionbox-io` are signed with [Sigstore **cosign**](https://docs.sigstore.dev/),
so you can confirm an image came from DecisionBox and was not altered in transit
before you run it.

Signing is **keyed** — verification uses a published public key and contacts no
external service, so it works the same on a connected host or a fully offline
one. The signature binds to the image's immutable content **digest**, so it
covers every tag that resolves to that digest.

## Verify an image

1. Install [cosign](https://docs.sigstore.dev/system_config/installation/) (v2.x).
2. Get the public key `cosign.pub` from the repository root
   (`https://raw.githubusercontent.com/decisionbox-io/decisionbox-platform/main/cosign.pub`)
   and confirm its fingerprint against a copy you trust.
3. Verify:

```bash
cosign verify \
  --key cosign.pub \
  --insecure-ignore-tlog=true \
  ghcr.io/decisionbox-io/decisionbox-api:vX.Y.Z
```

`--insecure-ignore-tlog=true` is required because the signature is keyed and is
**not** recorded in a public transparency log; it skips only that lookup and does
not weaken the cryptographic check. A successful run prints
`The signatures were verified against the specified public key`; a tampered or
unsigned image fails with `no matching signatures`.

Repeat for each image you run — `decisionbox-api`, `decisionbox-agent`,
`decisionbox-dashboard`.

### Pin and verify by digest

A tag can move; a digest cannot. For the strongest guarantee, deploy and verify
by digest:

```bash
cosign verify --key cosign.pub --insecure-ignore-tlog=true \
  ghcr.io/decisionbox-io/decisionbox-api@sha256:<digest>
```

### Re-hosting in your own registry

If you mirror the images into your own registry, use `cosign copy` so the
signature travels with the image and stays verifiable there (a plain
`docker pull` + `docker push` moves only the image):

```bash
cosign copy ghcr.io/decisionbox-io/decisionbox-api:vX.Y.Z \
            your-registry.example.com/decisionbox-api:vX.Y.Z
```

## Verify a release tag

Release tags (`vX.Y.Z`) are GPG-signed, so GitHub shows them **Verified**. To
verify a source checkout locally, import the public key and check the tag:

```bash
gpg --import release-signing-key.asc     # from the repository root
git tag -v vX.Y.Z
```
