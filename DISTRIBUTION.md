# Distribution checklist

Steps to make `karpenter-provider-hetzner` discoverable and drive adoption once
`v1.0.0` is tagged and the signed image + OCI Helm chart are published.

**Backlink strategy:** the anchor is a Paperclip.inc page, ideally a blog
article about the provider (e.g. `https://paperclip.inc/blog/karpenter-hetzner`).
Every external listing's primary/Homepage link points at Paperclip.inc so the
domain earns the backlinks; the GitHub repo is only the secondary "source" link.
The chart `home` and Artifact Hub "Homepage" link already point at
`https://paperclip.inc/karpenter-hetzner` (update to the final blog URL once
published).

Legend: [auto] doable from a repo · [acct] needs an external account/login ·
[ext-pr] needs a PR to an external repo.

## Anchor: Paperclip.inc blog article  [auto + acct]
- Write a Paperclip.inc engineering blog post ("A production Karpenter provider
  for Hetzner") in the marketing site, brand-voice compliant. This is the
  backlink target everything else points at. No fabricated adoption numbers.
- Optionally a thin `/karpenter-hetzner` landing page that features the post and
  the install command; set the chart `home` to whichever is canonical.

## Artifact Hub  [acct]
- Register the OCI chart repo at https://artifacthub.io/control-panel/repositories
  (Add repository -> Helm charts -> OCI -> `oci://ghcr.io/paperclipinc/charts`).
- Paste the generated `repositoryID` into `artifacthub-repo.yml`, re-push it to
  the OCI registry. The chart's `artifacthub.io/*` annotations already set the
  Homepage link to Paperclip.inc.

## karpenter-core community providers docs  [ext-pr]
- PR to `kubernetes-sigs/karpenter` adding Hetzner to the community/third-party
  providers list. Link text -> Paperclip.inc page; repo as the code link.

## awesome-karpenter  [ext-pr]
- PR to the `awesome-karpenter` providers section. Primary link -> Paperclip.inc
  page; mention the repo.

## CNCF landscape  [ext-pr]
- PR to `cncf/landscape` under the autoscaling category (homepage_url ->
  Paperclip.inc, repo_url -> GitHub, license Apache-2.0).

## Hetzner community  [acct]
- Tutorial on the Hetzner Community portal: "Autoscale a Talos k8s cluster on
  Hetzner with Karpenter", linking the Paperclip.inc article.

## README badges  [auto]
- Add the Artifact Hub badge once the listing exists.
