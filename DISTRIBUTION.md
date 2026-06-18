# Distribution checklist

Steps to make `karpenter-provider-hetzner` discoverable and drive adoption now
that `v1.0.0` is tagged and the signed image + OCI Helm chart are published.

**Backlink strategy:** the anchor is the Paperclip.inc blog article
[Open-sourcing our Karpenter provider for Hetzner](https://paperclip.inc/blog/karpenter-hetzner)
(live). Where a listing allows a homepage link separate from the code link,
the homepage points at Paperclip.inc so the domain earns the backlink, and the
GitHub repo is the secondary "source" link. Where a list only takes a single
link per entry and that list is developer-facing (the Karpenter implementations
list, awesome lists), use the repo as the primary link to maximise acceptance,
and embed the Paperclip.inc link in the entry description instead.

Legend: [auto] doable from a repo. [acct] needs an external account/login.
[ext-pr] needs a PR to an external repo. [community] a forum/issue/discussion
post in someone's voice.

Status: DONE = merged/published. SUBMITTED = PR/post open, awaiting review.
TODO = not started.

## Prerequisites and open gaps

- **GitHub Release for v1.0.0** [auto, TODO]. The `v1.0.0` tag exists and the
  OCI chart `1.0.0` is published, but the latest GitHub *Release* is still
  `v0.3.0`. Cut a `v1.0.0` release with notes. Reviewers of the lists below
  glance at the releases page, and the "1.0.0, production-ready" claim should be
  visible there.
- **CNCF CLA** [acct, TODO]. The kubernetes-sigs PR (below) is blocked until the
  commit author email (`jannes.stubbemann@gmail.com`) has signed the CNCF CLA.
  Sign in when the EasyCLA/cncf-cla bot comments on the PR.
- **Artifact Hub repository registration** [acct, TODO]. Finish registering the
  OCI chart repo so the listing verifies (see below).
- **Chart `home` URL** [auto]. Confirm the chart `home` and Artifact Hub
  "Homepage" annotation point at the canonical blog URL above.

## Anchor: Paperclip.inc blog article  [DONE]

- [Open-sourcing our Karpenter provider for Hetzner](https://paperclip.inc/blog/karpenter-hetzner)
  is live. This is the backlink target everything else points at. No fabricated
  adoption numbers; lead with "runs in production at Paperclip.inc".

## GitHub project listings

### kubernetes-sigs/karpenter implementations list  [ext-pr, SUBMITTED]
- PR https://github.com/kubernetes-sigs/karpenter/pull/3098 adds Hetzner Cloud
  to the `## Karpenter Implementations` list (the canonical providers list,
  alphabetically between GCP and Huawei Cloud). Highest-signal listing for the
  Karpenter audience. Needs the CNCF CLA (see gaps) plus a maintainer LGTM.

### hetznercloud/awesome-hcloud  [ext-pr, SUBMITTED]
- PR https://github.com/hetznercloud/awesome-hcloud/pull/158 adds the provider
  to the Integrations section. Official Hetzner curated list (~1.3k stars). The
  entry links the repo and embeds the Paperclip.inc blog link in the
  description.

### Artifact Hub  [acct, TODO]
- Register the OCI chart repo at https://artifacthub.io/control-panel/repositories
  (Add repository, Helm charts, OCI, `oci://ghcr.io/paperclipinc/charts`).
- The `repositoryID` is already in `artifacthub-repo.yml`; confirm it matches the
  one Artifact Hub issues, re-push the file to the OCI registry if needed.
- Add the Artifact Hub badge to the README once the listing is live.

### CNCF landscape  [ext-pr, TODO]
- PR to `cncf/landscape` under the autoscaling category. `homepage_url` points
  at the Paperclip.inc blog, `repo_url` at GitHub, license Apache-2.0.

### General Kubernetes "awesome" lists  [ext-pr, OPTIONAL]
- Lower priority and lower signal: a Hetzner-specific provider is niche for a
  general list, and some of these are generated from stars rather than PRs.
  Candidates if we want the extra backlinks: `pditommaso/awesome-k8s`,
  `collabnix/kubetools`. Note: there is no maintained `awesome-karpenter` repo
  (an earlier version of this file assumed one; it does not exist).

## Integration repos  [community / docs, TODO]

These take a Discussion, issue, or a docs/guide PR rather than a one-line list
entry, so they read as engagement in our voice. Drafts are ready; post when we
pick up the community track. The awesome-hcloud entry already reaches this same
audience.

### hcloud-k8s/terraform-hcloud-kubernetes  (the module our mono runs on)
The most natural integration partner: we run Karpenter on top of this Talos
module in production. Best contribution is a short "Autoscaling with Karpenter"
docs page or a Discussion. Draft opener:

> We run the Karpenter provider for Hetzner on clusters built with this module
> and it pairs cleanly: the module brings up the Talos control plane and the
> base pool, and Karpenter handles burst capacity by launching the cheapest
> Hetzner server that fits pending pods, then consolidating idle nodes. Happy to
> contribute a short guide if useful. Provider:
> https://github.com/paperclipinc/karpenter-provider-hetzner

### kube-hetzner/terraform-hcloud-kube-hetzner  (~3.9k stars)
Their users run cluster-autoscaler today; Karpenter is the upgrade story. Open a
Discussion in "Show and tell" / ideas. Draft opener:

> For anyone wanting cost-based autoscaling beyond fixed pools: we open-sourced a
> Karpenter provider for Hetzner that launches the cheapest server fitting your
> pending pods and consolidates idle nodes, with a Talos bootstrap path.
> https://github.com/paperclipinc/karpenter-provider-hetzner background:
> https://paperclip.inc/blog/karpenter-hetzner

### Talos / Sidero community
We ship a Talos bootstrap path, so the Talos crowd is a direct fit. Share in the
Talos community channels and any "built on Talos" list.

## Blogs and online magazines  (self-serve: needs accounts / editorial)

Each of these needs a login or an editorial pitch, so they are owner actions.
The content is the same blog post; where a venue allows it, republish with a
`rel=canonical` back to the Paperclip.inc article so SEO credit stays on our
domain. Reusable title and blurb below.

**Reusable title options**
- Cost-based Kubernetes autoscaling on Hetzner with Karpenter
- Open-sourcing our Karpenter provider for Hetzner

**Reusable short blurb (under 300 chars)**
> We open-sourced a production Karpenter provider for Hetzner Cloud. Instead of
> fixed node pools, Karpenter launches the cheapest Hetzner server that fits your
> pending pods and consolidates idle nodes. Apache-2.0, with a signed image, an
> OCI Helm chart, and Talos and cloud-init bootstrap.

### KubeWeekly (CNCF newsletter)  [BEST first move]
- What: CNCF's weekly newsletter, curated links, large cloud-native reach.
- Do: submit the blog URL via the KubeWeekly submission form (linked from
  cncf.io/kubeweekly) or email kubeweekly@cncf.io with the title and blurb.
- Needs: nothing but the link. Lowest effort, highest reach on this list.

### CNCF blog (cncf.io/blog)
- What: community/project posts; on-topic because Karpenter is CNCF.
- Do: follow the CNCF community blog submission guidelines (submission form on
  cncf.io/blog). Provide the article, title, and blurb.
- Needs: a CNCF account; review by their editorial team.

### The New Stack (thenewstack.io)
- What: cloud-native trade publication that takes contributed articles.
- Do: pitch their editors with the angle "cost-based autoscaling on a European
  cloud, an open alternative to cluster-autoscaler". Offer the article.
- Needs: an editor pitch; they may want a lightly adapted version.

### InfoQ
- What: developer publication; accepts contributed/pitched articles.
- Do: pitch via the InfoQ contributor process.
- Needs: contributor account + editorial review.

### Republish: dev.to, Hashnode, Medium (ITNEXT / Better Programming)
- What: developer blogging platforms; good for reach and backlinks.
- Do: cross-post the article with a canonical URL set to the Paperclip.inc post.
  dev.to and Hashnode both have a "canonical URL" field; on Medium use the
  import-with-canonical flow.
- Needs: an account on each. The post body is ready to paste.

### Hetzner blog (hetzner.com/blog)
- What: Hetzner occasionally features community tooling on their own blog.
- Do: ask the Hetzner community manager handling the tutorial PR whether the
  provider could be featured. Warm contact, low cost.

### Podcasts and dev newsletters
- Kubernetes Podcast (pitch via their guest/topic form).
- Last Week in Kubernetes Development (community-content / dev-focused).

## README badges  [auto]
- Add the Artifact Hub badge once the listing exists.
- Add a CNCF landscape badge once accepted.
