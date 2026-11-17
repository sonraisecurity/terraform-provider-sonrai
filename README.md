# Terraform Provider for Sonrai Security

Manage Sonrai Cloud Permissions Firewall configuration as code. Declare which
cloud services are disabled or protected, who is exempt from them, which
regions the firewall covers and which entitlements need a Just-in-Time session
— then submit those changes and deploy the resulting CloudFormation template in
the same `terraform apply`.

Without it, the firewall is configured by hand in the console, and the state of
an organization's controls lives nowhere you can review or diff.

## Key Features

- **The whole cycle in one apply.** Staging a control does nothing until it is
  submitted and the template it produces is deployed. The provider models all
  three steps, so one `terraform apply` stages, submits, deploys the
  CloudFormation stack and confirms the rollout landed.
- **Values read from the platform, not copied from the console.** Suggested
  exemptions, available JIT entitlements and the account the template deploys
  into are all data sources.
- **Reversible teardown.** One apply deletes the CloudFormation stack and then
  rolls every control under a root scope back to *staged*, with
  `sonrai_control_reset` ordered after the stack so the reset can finish. It is
  gated by a confirmation compared while the plan is built, so a wrong scope
  stops the run before anything is deleted.
- **Accounts addressed by id.** Scope paths are resolved on every operation, so
  moving an account between organizational units needs no configuration
  change.
- **Import for existing deployments.** Controls, exemptions, regions, JIT
  configuration and the current submission can all be imported, so a firewall
  configured in the console can be brought under Terraform.
- **Works with Terraform and OpenTofu.** Every feature is a plain resource or
  data source.

### Resources

| Resource | Manages |
|---|---|
| `sonrai_account_disabled_service` | One service disabled in one account |
| `sonrai_account_protected_service` | One service protected in one account, with its exemptions |
| `sonrai_exempted_identity` | An identity exempt from whole classes of control at a scope |
| `sonrai_region_control` | One cloud region enabled or disabled in one account |
| `sonrai_jit_configuration` | Just-in-Time access at a scope |
| `sonrai_control_submission` | Submission of everything staged under a root scope |
| `sonrai_control_reset` | Organization-wide rollback of applied controls to staged |

### Data sources

| Data source | Reads |
|---|---|
| `sonrai_scope` | The canonical scope path for an account, OU or project |
| `sonrai_deploy_account` | The account a root scope's template deploys into |
| `sonrai_suggested_exemptions` | Identities the platform would exempt when protecting a service |
| `sonrai_entitlements` | AWS permission sets, account access roles and SAML roles at a scope |
| `sonrai_control_transaction` | A submission's progress, optionally waiting for it to finish |

Per-attribute reference for each of these is in [`GUIDE.md`](GUIDE.md) and on
the [Terraform Registry](https://registry.terraform.io/providers/sonraisecurity/sonrai/latest/docs),
generated from [`docs/`](docs).

## Tech Stack / Dependencies

- **Go 1.26** — the provider is a single Go binary served over the Terraform
  plugin protocol.
- **[terraform-plugin-framework](https://github.com/hashicorp/terraform-plugin-framework)
  v1.19** with `terraform-plugin-framework-validators`, `terraform-plugin-go`
  and `terraform-plugin-log`.
- **`golang.org/x/time/rate`** for client-side rate limiting against the Sonrai
  GraphQL API.
- **[tfplugindocs](https://github.com/hashicorp/terraform-plugin-docs)**,
  pinned and fetched through `go run`, for generated docs.

The provider talks to the Sonrai GraphQL API directly. It has no SDK
dependency and no runtime dependency on the AWS SDK — deploying the
CloudFormation template is the `hashicorp/aws` provider's job, in your
configuration.

## Prerequisites

To *use* the provider:

- Terraform 1.5 or later, or OpenTofu 1.6 or later (1.7 or later to import
  with `for_each`). On macOS,
  `brew install hashicorp/tap/terraform` or `brew install opentofu`; plain
  `brew install terraform` is frozen at 1.5.7. Elsewhere, see
  [Install Terraform](https://developer.hashicorp.com/terraform/install).
- A Sonrai API token issued to an App identity, with write scope
- AWS credentials for the account the firewall deploys into, if you want the
  same apply to create the CloudFormation stack. `sonrai_deploy_account`
  reports which account that is: the delegated admin account where one is
  configured, the management account otherwise.

To *build* it:

- Go 1.26 or later
- `make`

## Installation & Quick Start

The provider is published on the
[Terraform Registry](https://registry.terraform.io/providers/sonraisecurity/sonrai/latest),
so `terraform init` downloads it and verifies its signature like any other
provider. There is nothing to build or install.

Commit the `.terraform.lock.hcl` that `terraform init` writes. It pins the
provider version and the checksums of the release you reviewed; move to a newer
release deliberately with `terraform init -upgrade`.

### Using OpenTofu

The provider is also published on the OpenTofu Registry, and the same
configuration works unchanged. `tofu init` reads the same
`source = "sonraisecurity/sonrai"` and installs it from `registry.opentofu.org`:

```sh
tofu init
tofu plan
```

Commit the lock file `tofu init` writes, as with Terraform. Every `terraform`
command in this README and the GUIDE has a `tofu` equivalent.

### Start from the worked example

[`examples/complete`](examples/complete) is a working root module covering the
whole cycle. Copy it and edit one file:

```sh
git clone https://github.com/sonraisecurity/terraform-provider-sonrai.git
cp -r terraform-provider-sonrai/examples/complete my-firewall
cd my-firewall
# edit terraform.tfvars -- see below

export SONRAI_ORG=crc12345678   # the first label of your console hostname
export SONRAI_TOKEN='...'       # issued to an App identity, with write scope
# and AWS credentials for the deploy account, e.g. aws sso login --profile ...

terraform init
terraform plan
```

The example is complete on its own: it declares the providers itself, so there
is nothing else to add. To use the provider in a configuration of your own, see
[Provider](#provider) under Configuration.

`terraform plan` is read-only: it changes nothing in Sonrai or AWS. Even so, it
needs **both** the Sonrai variables and AWS credentials for the deploy account,
because the example reads the AWS identity to guard the deploy. Its
`deploy_target` output names that account. If the first plan fails and the
error does not make clear which side is wrong, see
[Troubleshooting](#troubleshooting).

`terraform.tfvars` is the only file you need to edit. Put your root scope,
account ids, control keys and exemptions there; `main.tf` loops over whatever
you declare. Start by replacing `aws/r-abcd` and the two example account ids.

Two things to know before applying it:

- It ships with `submit = true`, so `terraform apply` submits the staged
  changes **and deploys the CloudFormation template**. Set it to `false` to
  stage only and inspect Pending Changes in the console first.
- The apply needs AWS credentials for the deploy account, because it creates
  the stack there. Getting this wrong fails *after* the submission has gone
  through, leaving a transaction counting down to its 24-hour deadline, so the
  example guards it with a `precondition`.

### Build from source

```sh
git clone https://github.com/sonraisecurity/terraform-provider-sonrai.git
cd terraform-provider-sonrai

make build     # compile ./terraform-provider-sonrai
make check     # gofmt, go vet, go test -race
make install   # install into $GOBIN for use with dev_overrides
make docs      # regenerate docs/ from the provider schemas
```

To run Terraform against a local build, point it at `$GOBIN` with a
`dev_overrides` block in `~/.terraformrc` and skip `terraform init`:

```hcl
provider_installation {
  dev_overrides {
    "sonraisecurity/sonrai" = "/Users/you/go/bin"
  }
  direct {}
}
```

Remove the `dev_overrides` entry when you are done, so Terraform goes back to
the signed Registry release. The same goes for a binary copied into
`~/.terraform.d/plugins/registry.terraform.io/sonraisecurity/` by an earlier
version of these instructions: while it is there, `terraform init` installs it
instead of the Registry release.

## Usage Examples

Each snippet assumes the provider is declared as in [Provider](#provider).

### Disable a service

Disabling blocks a service outright.

```hcl
resource "sonrai_account_disabled_service" "cloud9_prod" {
  root_scope  = "aws/r-abcd"
  resource_id = "123456789012"
  control_key = "cloud9"
}
```

`control_key` is the firewall's name for a service — `cloud9`, `iam`,
`sagemaker` — listed beside each service in the console. `resource_id` is the
cloud-native account id, project id or subscription id, given bare.

### Protect a service, keeping the identities that need it

Protecting firewalls a service's sensitive permissions for every identity
except those exempted. Read the platform's suggestions before choosing:

```hcl
data "sonrai_suggested_exemptions" "iam_prod" {
  root_scope  = "aws/r-abcd"
  resource_id = "123456789012"
  control_key = "iam"
}

output "iam_suggestions" {
  value = data.sonrai_suggested_exemptions.iam_prod.suggestions
}
```

Each entry names an identity that recently used one of the service's sensitive
permissions. Copy the ones to keep working into the resource — suggestions are
never applied automatically:

```hcl
resource "sonrai_account_protected_service" "iam_prod" {
  root_scope  = "aws/r-abcd"
  resource_id = "123456789012"
  control_key = "iam"

  exempt_identities    = ["arn:aws:iam::123456789012:role/CrossAccountIAMAdminRole"]
  exempt_sso_actor_ids = ["someone@example.com"]
}
```

### Submit the staged changes and deploy the template

Nothing takes effect until the staged changes are submitted and the template
they produce is deployed. Put both in the same configuration so one apply does
the whole cycle:

```hcl
resource "sonrai_control_submission" "firewall" {
  root_scope = "aws/r-abcd"

  triggers = {
    cloud9_prod = sonrai_account_disabled_service.cloud9_prod.id
    iam_prod    = sonrai_account_protected_service.iam_prod.id
  }
}

resource "aws_cloudformation_stack" "firewall" {
  name         = "SonraiCloudPermissionFirewall"
  template_url = sonrai_control_submission.firewall.stack_template_location
  capabilities = ["CAPABILITY_NAMED_IAM"]

  lifecycle {
    prevent_destroy = true
  }
}

data "sonrai_control_transaction" "firewall" {
  transaction_id = sonrai_control_submission.firewall.transaction_id
  wait_for       = "COMPLETED"
  wait_timeout   = "2h"

  depends_on = [aws_cloudformation_stack.firewall]
}
```

List the controls a submission covers in `triggers`, so that changing any of
them produces a new submission. A submission covers **every** change staged
under its root scope, including changes staged from the console — use one per
root scope and avoid concurrent applies against the same one.

### Exempt an identity from whole classes of control

```hcl
resource "sonrai_exempted_identity" "break_glass" {
  scope       = "aws/r-abcd"
  identity    = "*e/BreakGlass"
  exempt_from = ["service_protect", "service_block"]
}
```

Identities are matched as patterns, so one entry covers every account under the
scope. AWS only.

### ARNs and patterns

Where an identity is given decides whether a pattern is allowed:

| Where | Form |
|---|---|
| `sonrai_exempted_identity.identity` | Short pattern, **recommended**, such as `*e/BreakGlass`. A full ARN also works, for one identity in one account. |
| `sonrai_account_protected_service.exempt_identities` | Full ARN, including the account id. **Patterns are not supported.** |
| `sonrai_suggested_exemptions.identities` | Always full ARNs, ready for `exempt_identities`. |
| `exempt_sso_actor_ids`, or `identity` with `sso = true` | The SSO actor id, not an ARN. |

Keep patterns short. Each pattern is repeated across the statements of the
size-limited policies the platform deploys, so every extra character is spent
many times. `*e/BreakGlass` names the same role as
`arn:aws:iam::*:role/BreakGlass` at a fraction of the cost. If your role or user
names contain `/`, a short pattern can match more than you intend; spell out
more, such as `*role/BreakGlass`.

To exempt a role from protects in every account, don't repeat its ARN on each
protect. Use one `sonrai_exempted_identity` with a pattern and
`service_protect` in `exempt_from`.

### Disable a region

```hcl
resource "sonrai_region_control" "no_apac_sandbox" {
  root_scope  = "aws/r-abcd"
  resource_id = "123456789012"
  region_id   = "ap-south-1"
  state       = "disabled"
}
```

Use `state = "enabled"` to override an ancestor that disabled the region.
Critical regions cannot be disabled. Destroying the resource restores the state
the account had before, recorded in `previous_state`.

### Configure Just-in-Time access

```hcl
resource "sonrai_jit_configuration" "sandbox" {
  scope      = "aws/r-abcd/123456789012"
  deny_first = true

  enrolled_entitlements = [
    { name = "PowerUserAccess" },
    { name = "BreakGlassAccess", type = "AWS_ACCOUNT_ACCESS_ROLE", auto_pod = true },
  ]
}
```

This resource is authoritative for its scope: declaring it replaces whatever
the scope already has, including enrolments made in the console. List what is
available with the `sonrai_entitlements` data source first.

### Import existing controls

Every resource except `sonrai_control_reset` can be imported. Each resource
page on the Registry lists its import ID, for example:

```hcl
import {
  to = sonrai_account_disabled_service.cloud9_prod
  id = "cloud9@aws/r-abcd@123456789012"
}
```

To adopt a whole deployment, also import the CloudFormation stack and the
submission it is running, so the first apply does not submit again. See
[Importing an existing deployment](GUIDE.md#importing-an-existing-deployment).

### Remove controls

Removing controls is a configuration change, not a destroy. Delete them from
the configuration, or empty the maps feeding them, and apply once:

```sh
terraform apply
```

Each removal stages an unmanage; the submission then submits those removals and
updates the stack with the reduced template. Terraform destroys a dependency's
removed instances before creating the resource that depends on them, so the
ordering holds without `-target`.

Do not reach for `terraform destroy` — see [`GUIDE.md`](GUIDE.md#remove-everything)
for what it actually does here.

### Reset an organization

```hcl
resource "sonrai_control_reset" "firewall" {
  count = var.reset ? 1 : 0

  root_scope = var.root_scope
  confirm    = var.reset_confirm # write the scope out by hand

  depends_on = [aws_cloudformation_stack.firewall]
}
```

The reset transaction waits for the CloudFormation stack to be deleted before
doing the rest of its work, so `depends_on` puts both into one apply: the stack
goes, then the reset runs and the resource waits for it to complete.

This is destructive and organization-wide — it clears the whole root scope, not
only what Terraform manages. `confirm` must repeat `root_scope` exactly and is
compared while the plan is built, so a mismatch stops the run before anything
is deleted.

### Troubleshooting

**The first `plan` fails and it is unclear whether Sonrai or AWS is the
problem.** Test the Sonrai side alone. In an empty directory of its own — not
the worked example's, which already declares these blocks — save this as
`check.tf`, with your root scope:

```hcl
terraform {
  required_providers {
    sonrai = {
      source  = "sonraisecurity/sonrai"
      version = "~> 0.1"
    }
  }
}

provider "sonrai" {}

data "sonrai_deploy_account" "check" {
  root_scope = "aws/r-abcd" # your cloud organization root
}

output "deploy_account" {
  value = data.sonrai_deploy_account.check.account_id
}
```

```sh
terraform init
terraform plan
```

It needs only `SONRAI_ORG` and `SONRAI_TOKEN`, and reads without changing
anything. If it shows `deploy_account`, the provider, the token and the root
scope are fine and the problem is on the AWS side. If it fails, its error is
about Sonrai alone. Delete the directory afterwards.

**`Instance cannot be destroyed` on `aws_cloudformation_stack.firewall`.** The
plan wants to delete the stack and `prevent_destroy` stopped it. Nothing has
changed. In `examples/complete` this almost always means `submit` is `false`
— often because `terraform.tfvars` is missing or was not loaded — which sets
the stack's `count` to 0. Set `submit = true` again.

Do **not** follow the error's suggestion to remove `prevent_destroy` or use
`-target`. Either lets the apply delete the stack, which removes the
firewall's IAM footprint for the whole root scope without unapplying anything.
To take the firewall down deliberately, see [Reset an organization](#reset-an-organization).

**`no such host` for `crc12345678.sonraisecurity.com`** (or another org than
yours). An `org` argument in the `provider` block is overriding `SONRAI_ORG`.
Remove it.

**`Provider produced inconsistent final plan` on the stack's `outputs`.** The
stack's `template_url` was empty in state and the apply gave it a template.
Point `template_url` at `stack_template_location` rather than
`template_location`, which is empty whenever a submission changes nothing
deployable. Applying again clears the error.

**A plan that disagrees with the console.** Within one run, the provider
shares reads between controls in the same account -- the account's hierarchy
entry, its service list, its exceptions -- rather than asking once per control.
That is what keeps the API calls of a plan over thousands of controls to a
handful per account. The sharing stops at the first change the run makes, and nothing is kept
between runs. To rule it out, set `SONRAI_DISABLE_READ_CACHE=true` and plan
again: every read then goes to the platform, at the cost of a much slower plan.

## Configuration

### Provider

To use the provider in a configuration of your own, declare it once per
directory:

```hcl
terraform {
  required_providers {
    sonrai = {
      source  = "sonraisecurity/sonrai"
      version = "~> 0.1"
    }
  }
}

# Configured from SONRAI_ORG and SONRAI_TOKEN.
provider "sonrai" {}
```

The provider reads `SONRAI_ORG` and `SONRAI_TOKEN` from the environment. An
argument set in the `provider` block **takes precedence over its environment
variable**, so a copied `org = "..."` silently overrides `SONRAI_ORG`. Set each
value in one place only, and the token only in the environment.

Put this block in any `.tf` file in the directory you run Terraform from —
Terraform reads every `.tf` file there together. `versions.tf` is the usual
name. Declare `required_providers` once per directory.

| Argument | Environment variable | Default | |
|---|---|---|---|
| `org` | `SONRAI_ORG` | — | required. The first label of your console hostname |
| `token` | `SONRAI_TOKEN` | — | required. Bearer token for an App identity, with write scope |
| `domain` | `SONRAI_DOMAIN` | `sonraisecurity.com` | optional. Console domain |

Supply the token through `SONRAI_TOKEN`. A token written into configuration is
also written into state.

```sh
export SONRAI_ORG=crc12345678
export SONRAI_TOKEN='...'
```

### `examples/complete` variables

Set these in `terraform.tfvars`:

| Variable | Type | Default | |
|---|---|---|---|
| `root_scope` | string | `aws/r-abcd` | Cloud organization root scope |
| `disabled_services` | map(set(string)) | `{}` | Account id => control keys to disable, e.g. `"123456789012" = ["cloud9"]` |
| `protected_services` | map(object) | `{}` | `resource_id`, `control_key`, `exempt_identities`, `exempt_sso_actor_ids` |
| `exempted_identities` | map(object) | `{}` | `identity`, `exempt_from`, `sso`, `scope` |
| `region_controls` | map(map(string)) | `{}` | Account id => region id => `disabled` or `enabled`, e.g. `"123456789012" = { "ap-south-1" = "disabled" }` |
| `jit_configurations` | map(object) | `{}` | Keyed by scope: `deny_first`, `enrolled_entitlements` (`name`, `type`, `auto_pod`) |
| `submit` | bool | `false` | Submit the staged changes and deploy the template |
| `reset` | bool | `false` | Tear the deployment down and return every control to staged |
| `reset_confirm` | string | `""` | Must repeat `root_scope` to arm a reset |
| `aws_region` | string | `us-east-1` | Region the CloudFormation deploy runs in |
| `stack_name` | string | `SonraiCloudPermissionFirewall` | Name of the firewall's stack |

Those are the variable defaults. The `terraform.tfvars` the example ships with
overrides `submit` to `true`, so a first apply deploys.

## Project Structure

```
.
├── main.go                  # provider server entrypoint
├── internal/
│   ├── provider/            # resources, data sources, schemas
│   └── client/              # Sonrai GraphQL client, rate limiting, polling
├── examples/
│   ├── complete/            # worked root module covering the whole cycle
│   ├── provider/            # provider block, rendered into docs/index.md
│   ├── resources/           # per-resource examples, rendered into docs/
│   └── data-sources/        # per-data-source examples
├── docs/                    # Terraform Registry docs, generated by `make docs`
├── GUIDE.md                 # walkthrough and per-attribute reference
└── signing-key.asc          # public key the release checksums are signed with
```

`docs/` is generated — edit the schema descriptions in `internal/provider` and
run `make docs` rather than editing it by hand; `make docs-check` fails if the
two disagree.

## Contributing

Issues and pull requests are welcome. Before opening one:

```sh
make check      # gofmt, go vet, go test -race
make docs       # if you changed a schema description
```

Schema descriptions are the source for everything under `docs/`, so keep them
accurate; [`GUIDE.md`](GUIDE.md) is maintained by hand and worth updating in
the same change.

## License

[Mozilla Public License 2.0](LICENSE).
