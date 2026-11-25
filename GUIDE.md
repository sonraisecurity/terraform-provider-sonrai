# Operating the Sonrai Terraform provider

The full walkthrough and the per-attribute reference for every resource and
data source. [`README.md`](README.md) covers installation and the shape of a
working configuration; this is the detail behind it.

## Requirements

- Terraform 1.5 or later, or OpenTofu 1.6 or later
- A Sonrai API token issued to an App identity, with write scope

Everything here is a resource or a data source. The provider declares no
Terraform actions, which OpenTofu does not implement, so both tools get the
same thing.

## Provider configuration

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

| Argument | Environment variable | Default | |
|---|---|---|---|
| `org` | `SONRAI_ORG` | — | required |
| `domain` | `SONRAI_DOMAIN` | `sonraisecurity.com` | optional |
| `token` | `SONRAI_TOKEN` | — | required |

Set the token through `SONRAI_TOKEN`. A token set in configuration is also
written to state.

An argument set in the `provider` block takes precedence over its environment
variable. Set each value in one place: an `org` copied from an example
silently overrides `SONRAI_ORG`.

Blocks in this guide go in any `.tf` file in the directory you run Terraform
from; Terraform reads them all together. Declare `required_providers` once per
directory.

## Walkthrough

A complete configuration that disables one service, protects another, submits
both, and deploys the result.

### Before you start

Collect four things:

| | How to find it |
|---|---|
| Organization name | The first label of your console hostname |
| API token | Issue one to an App identity in the console |
| Root scope | The cloud organization root, such as `aws/r-abcd` |
| Account id | The AWS account id, GCP project id or Azure subscription id to act on |

Export the credentials:

```bash
export SONRAI_ORG=crc12345678
export SONRAI_TOKEN='...'
```

Control keys are the firewall's name for a service — `cloud9`, `iam`,
`sagemaker`. They are listed beside each service in the console.

### Set up the configuration

This walkthrough builds one configuration a block at a time. It is not the same
as [`examples/complete`](examples/complete), which declares the same resources
with `count` and `for_each` so it can be driven from `terraform.tfvars`; follow
one or the other, not a mix, or resource addresses will not line up.

```hcl
terraform {
  required_providers {
    sonrai = { source = "sonraisecurity/sonrai", version = "~> 0.1" }
    aws    = { source = "hashicorp/aws", version = "~> 5.67" }
  }
}

provider "sonrai" {}

provider "aws" {
  region = "us-east-1"
}
```

The AWS provider deploys the CloudFormation template the firewall produces.
Point its credentials at the account reported by `sonrai_deploy_account` — the
delegated admin account where one is configured, the management account
otherwise:

```hcl
data "sonrai_deploy_account" "firewall" {
  root_scope = "aws/r-abcd"
}

output "deploy_target" {
  value = data.sonrai_deploy_account.firewall.account_id
}
```

The template url isn't pre-signed and the bucket lives in the deploy account,
so CloudFormation fetches it with whatever credentials the `aws` provider has.
Apply from the wrong account and you get `S3 error: Access Denied` — after the
submission has already gone through, leaving a transaction counting down to its
24-hour deadline with nothing deployed.

Guard it with a precondition. `aws_caller_identity` reports the credentials
that will actually create the stack:

```hcl
data "aws_caller_identity" "current" {}

resource "sonrai_control_submission" "firewall" {
  root_scope = "aws/r-abcd"

  lifecycle {
    precondition {
      condition     = data.aws_caller_identity.current.account_id == data.sonrai_deploy_account.firewall.account_id
      error_message = "AWS credentials are for the wrong account; the template deploys into ${data.sonrai_deploy_account.firewall.account_id}."
    }
  }
}
```

[`examples/complete`](examples/complete) wires this up.

### Disable a service

Disabling blocks a service outright.

```hcl
resource "sonrai_account_disabled_service" "cloud9_prod" {
  root_scope  = "aws/r-abcd"
  resource_id = "123456789012"
  control_key = "cloud9"
}
```

### Protect a service

Protecting firewalls a service's sensitive permissions for every identity
except those exempted. Read the platform's suggested exemptions first.

#### 1. Review the suggestions

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

```bash
terraform apply
terraform output iam_suggestions
```

Each entry names an identity that recently used one of the service's sensitive
permissions. Protecting without exempting these will deny them the permissions
they are using.

#### 2. Declare the protect

Copy the identities you want to keep working into the resource. Non-SSO
identities go in `exempt_identities` as full ARNs, exactly as the suggestions
give them; patterns are not supported there. SSO identities go in
`exempt_sso_actor_ids` using their `sso_actor_id`:

```hcl
resource "sonrai_account_protected_service" "iam_prod" {
  root_scope  = "aws/r-abcd"
  resource_id = "123456789012"
  control_key = "iam"

  exempt_identities = [
    "arn:aws:iam::123456789012:role/CrossAccountIAMAdminRole",
  ]

  exempt_sso_actor_ids = [
    "someone@example.com",
  ]
}
```

Only the identities listed here are exempted. The platform's suggestions are
not applied automatically.

### Submit and deploy

Nothing takes effect until the staged changes are submitted and the resulting
template deployed. Add both to the same configuration so one apply does the
whole cycle:

```hcl
resource "sonrai_control_submission" "firewall" {
  root_scope = "aws/r-abcd"

  triggers = {
    cloud9_prod = sonrai_account_disabled_service.cloud9_prod.revision
    iam_prod    = sonrai_account_protected_service.iam_prod.revision
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

Apply with credentials for the deploy account:

```bash
terraform apply
```

List the `revision` of each control in `triggers`. Every resource that stages
changes has one, and it changes whenever the resource stages something, so the
submission runs again for a new control, a changed one and a removed one.

It also covers drift. When something is changed outside Terraform and deployed,
the next plan reverts it in place. Nothing in the configuration changed, so a
trigger built from configured values would stay the same and the revert would
be staged without being submitted. `revision` is unknown in any plan that
changes its resource, which is what makes the submission run.

This walkthrough assumes the firewall has not been deployed under the root
scope before. A control that is already applied — from the console or another
configuration — is rejected rather than taken over. To manage one, import it:
see [Importing an existing deployment](#importing-an-existing-deployment).

### Confirm

```bash
terraform plan
```

`No changes` means every control and exemption read back as declared, and
`rollout.final_status` reads `COMPLETED`.

The controls keep a computed `pending` attribute, which state records as true
from when they were created and a later refresh reads as false — the submission
deploys them after they were read back. Nothing depends on it and it drives no
change, so it is deliberately left out of the outputs: piping a value that
settles on its own into an output turns an invisible refresh into a diff that
only another apply can clear.

### Remove everything

Do not reach for `terraform destroy`. What it does depends on whether the
CloudFormation stack still carries its `prevent_destroy` block, and neither
outcome is the one you want:

- **With the block in place** Terraform refuses to produce a destroy plan at
  all, so nothing happens.
- **Without it** the destroy runs: it deletes the stack, which stops
  enforcement everywhere under the root scope, stages a removal for every
  control on the way down, and drops the submission from state without
  submitting any of them.

That second case is the dangerous one. The staged removals are real and
nothing has submitted them, while Terraform's state now says the controls are
gone. The next submission made with the same token against that root scope
deploys the lot — including one made by another configuration.

Removing controls is a configuration change, not a destroy. Delete them from
the configuration, or empty the maps feeding them, and apply:

```bash
terraform apply
```

One apply does both halves. Each removal stages an unmanage for its control,
discarding that control's exemptions with it; the submission then submits those
removals and updates the CloudFormation stack with the reduced template.

The order holds on its own. `sonrai_control_submission` depends on the control
resources through `triggers`, and Terraform destroys a dependency's removed
instances before creating the resource that depends on them — so every unmanage
is staged before the submission runs. That holds whether you emptied a map or
deleted the resource block outright, which is why this needs no `-target`.

Afterwards no controls remain, no exemptions remain, and Pending Changes is
empty.

This leaves the CloudFormation stack in place, holding a template with nothing
in it. To take that down too, and with it everything under the root scope that
this configuration never managed, use
[Reset everything at once](#reset-everything-at-once) instead — it is the one
teardown that leaves nothing staged behind it.

### Reset everything at once

Unapplies every control under a root scope and tears down the deployment. The
controls return to *staged* rather than being removed, so Terraform state stays
accurate and they can be deployed again.

It is not the same as removing the controls you manage. A reset clears the
whole root scope, not only what Terraform created: controls made in the console
go too, along with constructs this provider does not model, such as quarantined
identities and JIT levels.

A reset cannot finish while the CloudFormation stack is still standing. The
transaction hands off to the cloud, waits for the stack to go, and only then
does the rest of its work. So `sonrai_control_reset` depends on the stack, and
a single apply runs the whole sequence: delete the stack, start the reset, wait
for it to complete.

[`examples/complete`](examples/complete) has this wired up already. The
submission and the stack stand down while `reset` is set, which is what puts
the stack's deletion into the same apply as the reset:

```hcl
locals {
  deployed = var.submit && !var.reset
}

resource "sonrai_control_submission" "firewall" {
  count = local.deployed ? 1 : 0
  # ...
}

resource "aws_cloudformation_stack" "firewall" {
  count = local.deployed ? 1 : 0
  # ...
}

resource "sonrai_control_reset" "firewall" {
  count = var.reset ? 1 : 0

  root_scope = var.root_scope
  confirm    = var.reset_confirm

  depends_on = [aws_cloudformation_stack.firewall]
}
```

**1. Let the stack be deleted.** `prevent_destroy` is on the stack to stop it
being torn down by accident, and a reset tears it down on purpose. Remove the
block from `aws_cloudformation_stack.firewall` in `main.tf`:

```hcl
  lifecycle {
    prevent_destroy = true
  }
```

**2. Arm the reset** in `terraform.tfvars`:

```hcl
reset         = true
reset_confirm = "aws/r-abcd" # write your own root scope here by hand
```

`reset_confirm` is a second variable rather than `var.root_scope` so that one
flipped flag is not enough to clear an organization. Typing the scope is you
saying which organization you mean, and pointing both at the same variable
throws that away. The provider compares the two while the plan is being built
and refuses a mismatch, so a wrong scope stops the run before anything is
deleted.

**3. Apply**, with credentials for the deploy account:

```bash
terraform apply
```

That deletes the stack, starts the reset and waits for it to complete —
`wait_timeout` defaults to `2h`. When the apply returns, the reset is done.
There is nothing to confirm afterwards and no transaction id to carry between
steps; `terraform output reset` reports how it finished.

#### Where this leaves you

Nothing is applied and no stack remains. Every control is staged, exemptions
included, and a plan shows no changes.

To deploy it all again, disarm the reset:

```hcl
reset = false
```

```bash
terraform apply
```

That submits afresh — a new transaction, a new template — and creates the stack
from it. The reset record leaves state on the way, which is the end of it.

To discard the staged controls instead, clear Pending Changes in the console,
then drop the control resources from state:

```sh
terraform state rm 'sonrai_account_disabled_service.this["cloud9@123456789012"]'
terraform state rm 'sonrai_account_protected_service.this["iam_123456789012"]'
```

Addresses there match [`examples/complete`](examples/complete), where the
controls are collections, so they carry a key. `terraform state list` prints
the addresses you actually have.

#### Afterwards

Put `prevent_destroy` back on the stack. Without it the next `terraform
destroy`, or a `submit = false` typed by mistake, deletes the firewall's IAM
footprint with nothing standing in the way.

Leaving `reset = false` in `terraform.tfvars` is safe. Arming it again takes
both variables and a `main.tf` edit, and the plan names the stack it will
destroy before you approve any of it.

### Importing an existing deployment

A firewall already configured in the console can be brought under Terraform
with `import` blocks. Each resource's import ID:

| Resource | Import ID | Example |
|---|---|---|
| `sonrai_account_disabled_service` | `control_key@root_scope@resource_id` | `cloud9@aws/r-abcd@123456789012` |
| `sonrai_account_protected_service` | `control_key@root_scope@resource_id` | `iam@aws/r-abcd@123456789012` |
| `sonrai_exempted_identity` | `identity@scope`, plus `@sso` for an SSO actor id | `*e/BreakGlass@aws/r-abcd` |
| `sonrai_region_control` | `region_id@root_scope@resource_id` | `ap-south-1@aws/r-abcd@123456789012` |
| `sonrai_jit_configuration` | `root_scope@resource_id` | `aws/r-abcd@123456789012` |
| `sonrai_approvers` | `root_scope@resource_id` | `aws/r-abcd@123456789012` |
| `sonrai_control_submission` | The transaction id | `a1b2c3d4-0000-4000-8000-000000000000` |
| `aws_cloudformation_stack` | The stack name | `SonraiCloudPermissionFirewall` |

`sonrai_control_reset` cannot be imported; it records an action, not a
setting.

**Import the stack and its submission together.** The stack's `SonraiId`
output names the transaction it is running, as `<org>:<transaction id>`. That
is the last transaction that produced a template, which is not always the most
recent one: a submission that changes nothing deployable leaves the stack, and
this output, as they were.
Import that transaction as the submission. The imported submission takes on
the configured `triggers` without submitting, and its `stack_template_location`
gives the stack a known `template_url`. Without it, the first apply submits
again.

With [`examples/complete`](examples/complete), which keys its resources from
`terraform.tfvars`, `for_each` import blocks (Terraform 1.7 or later) cover
every entry at once:

```hcl
data "aws_cloudformation_stack" "adopt" {
  name = var.stack_name
}

locals {
  adopt_transaction = regex(
    "^[A-Za-z0-9-]+:([0-9a-f-]{36})$",
    data.aws_cloudformation_stack.adopt.outputs["SonraiId"],
  )[0]
}

import {
  to = aws_cloudformation_stack.firewall[0]
  id = var.stack_name
}

import {
  to = sonrai_control_submission.firewall[0]
  id = local.adopt_transaction
}

import {
  for_each = local.disabled_services
  to       = sonrai_account_disabled_service.this[each.key]
  id       = "${each.value.control_key}@${var.root_scope}@${each.value.resource_id}"
}

import {
  for_each = local.region_controls
  to       = sonrai_region_control.this[each.key]
  id       = "${each.value.region_id}@${var.root_scope}@${each.value.resource_id}"
}

import {
  for_each = var.jit_configurations
  to       = sonrai_jit_configuration.this[each.key]
  id       = "${var.root_scope}@${each.key}"
}

import {
  for_each = var.approvers
  to       = sonrai_approvers.this[each.key]
  id       = "${var.root_scope}@${each.key}"
}
```

Protected services and exempted identities follow the same pattern. Then:

1. Fill `terraform.tfvars` with what is deployed, and set `submit = true`.
2. Run `terraform plan`. Every import should show no changes, apart from the
   stack's `template_url` being set and the submission's `triggers` being
   recorded. Any other difference means the configuration does not match what
   is deployed; fix the configuration, not the platform.
3. Apply, then delete the import blocks. Left in place, a new entry added later
   gets an import attempt and fails with `Cannot import non-existent remote
   object`.

Things to know about imported resources:

- **Region controls** have no recorded `previous_state`, so destroying one
  re-enables the region rather than restoring an earlier state.
- **JIT configuration** is authoritative for its scope. List every entitlement
  already enrolled there, or the next apply unenrols it.
- **Protected services** read their exemptions from the platform. Declare the
  same ones, or the next apply changes them.
- **Approvers** are authoritative for the approvers assigned at their scope.
  List every one, or the next apply removes the rest. Nothing is recorded to
  restore, so destroying one leaves the approval type as it is.

## Troubleshooting

**`Instance cannot be destroyed` on `aws_cloudformation_stack.firewall`.**
The plan wants to delete the stack, and `prevent_destroy` stopped it; nothing
has changed. In `examples/complete` this almost always means `submit` is
`false` — often because `terraform.tfvars` is missing or was not loaded — which
sets the stack's `count` to 0. Set `submit = true` in `terraform.tfvars`.

Do **not** follow the error's own suggestion to remove `prevent_destroy` or use
`-target`. Either lets the apply delete the stack: the firewall's IAM footprint
for the whole root scope goes, and nothing is unapplied. To take the firewall
down on purpose, use [Reset everything at once](#reset-everything-at-once).

**`no such host` for an organization that is not yours.** An `org` argument in
the `provider` block takes precedence over `SONRAI_ORG`. Remove it — a copied
example's `crc12345678` is the usual cause.

**`Provider produced inconsistent final plan` on the stack's `outputs`.** The
stack's `template_url` was empty in state and the apply gave it a template.
Point `template_url` at `stack_template_location`, not `template_location`:
the latter is empty whenever a submission changes nothing deployable, such as a
JIT change. Applying again clears the error.

**`Cannot import non-existent remote object`.** An `import` block is still in
the configuration for an entry that was never deployed. Delete the import
blocks once the import has been applied.

**`Failed to query available provider packages`.** Check that
`required_providers` has `source = "sonraisecurity/sonrai"` and that the machine
can reach `registry.terraform.io`, or `registry.opentofu.org` with OpenTofu.

**A checksum mismatch on `sonraisecurity/sonrai`.** The lock file records a
different build than the one being installed — usually a local build left in
`~/.terraform.d/plugins/` or a `dev_overrides` entry in `~/.terraformrc`.
Remove it so Terraform uses the signed Registry release, then run
`terraform init` again. Do not delete the lock file to make the error go away
without finding out which build it was expecting.

## Addressing

Control resources take a `root_scope` and a `resource_id`. `resource_id` is the
cloud-native id of the account to act on:

| Target | Example |
|---|---|
| AWS account | `123456789012` |
| GCP project | `my-project` |
| Azure subscription | `00000000-0000-0000-0000-000000000000` |

Give ids bare. `my-project` resolves the same as `projects|my-project`, and
already-qualified ids are accepted unchanged.

Organizational units, folders, management groups and roots are not accepted by
control resources. To cover several accounts, list each one. `sonrai_scope`
resolves containers as well, for reference.

The provider resolves the scope path on every operation, so moving an account
between organizational units requires no configuration change.

## Identities: ARNs and patterns

Exemptions name identities in two places, and only one of them accepts
patterns:

| Where | Form | Patterns |
|---|---|---|
| `sonrai_exempted_identity.identity` | Smallest pattern that names the identity | **Yes, always** |
| `sonrai_account_protected_service.exempt_identities` | Full ARN, including the account id | **No** |
| `sonrai_suggested_exemptions.identities` | Full ARN (output) | — |
| `exempt_sso_actor_ids`, or `identity` with `sso = true` | SSO actor id, not an ARN | — |

**Exempted identities** are matched as patterns. Prefer a pattern such as
`*e/BreakGlass`: one entry covers the role in every account under the scope,
including accounts added later.

Use the smallest pattern that names the identity, even when the exemption is
for one account. `scope` is what limits an exemption to an account. A full ARN
adds nothing but length, and is still repeated in every statement of every
policy for that account:

```hcl
resource "sonrai_exempted_identity" "deploy_robot" {
  scope       = "aws/r-abcd/123456789012"
  identity    = "*e/DeployRobot"
  exempt_from = ["service_protect"]
}
```

Keep patterns short. The platform writes each exemption pattern into many
statements of the policies it deploys, and those policies have a size limit.
Every character in a pattern is paid for again in each statement, so a long
pattern can use up room that other controls need. `*e/BreakGlass` names the same
role as `arn:aws:iam::*:role/BreakGlass` at a fraction of the length: the `e/`
is the end of `role/`, and nothing before it needs spelling out.

A short pattern matches any ARN that ends the same way. If your organization
uses `/` in role or user names, for example IAM paths such as
`role/platform/BreakGlass`, `*e/BreakGlass` can match more than you intend.
Spell out enough to tell them apart, such as `*role/BreakGlass`.

**Protected-service exemptions** must be full ARNs, such as
`arn:aws:iam::123456789012:role/CrossAccountIAMAdminRole`. A protect covers
one service in one account, so each exemption names one identity there.
`sonrai_suggested_exemptions` always returns full ARNs, so its `identities` can
be copied or referenced as they are.

To exempt a role from protects in every account, don't list its ARN on each
protect. Use one exempted identity with a pattern:

```hcl
resource "sonrai_exempted_identity" "iam_admin" {
  scope       = "aws/r-abcd"
  identity    = "*e/CrossAccountIAMAdminRole"
  exempt_from = ["service_protect"]
}
```

This exempts the role from every protected service, not just one. Keep
per-service exemptions on the protect, and use exempted identities for roles
that should get past all protects.

## Resources

### `sonrai_account_disabled_service`

Disables one cloud service in one account, denying its sensitive permissions.
Claims only the service it names. A service already disabled — from the
console, or by another configuration — is rejected rather than adopted; import
it to manage it.

| Attribute | Type | | |
|---|---|---|---|
| `root_scope` | string | required | Cloud organization root scope. Forces replacement. |
| `resource_id` | string | required | Id of the account. Forces replacement. |
| `control_key` | string | required | Firewall control key, such as `cloud9`. Forces replacement. |
| `scope` | string | computed | Resolved scope path. |
| `status` | string | computed | `Disabled`, `Protected`, `Unprotected` or `Non-Privileged`. |
| `pending` | bool | computed | True while a staged change has not been deployed. |
| `revision` | string | computed | Changes whenever this resource stages a change. Reference it in the submission's `triggers`. |

Creating the resource stages the change; it takes effect once submitted and
deployed. `status` reads `Disabled` from the moment it is staged.

Changing a managed control outside Terraform causes the next plan to show the
resource as deleted; applying re-stages it.


### `sonrai_account_protected_service`

Protects one cloud service in one account, firewalling its sensitive
permissions for every identity except those exempted. A service already
protected is rejected rather than adopted; import it to manage it.

| Attribute | Type | | |
|---|---|---|---|
| `root_scope` | string | required | Cloud organization root scope. Forces replacement. |
| `resource_id` | string | required | Id of the account. Forces replacement. |
| `control_key` | string | required | Firewall control key, such as `iam`. Forces replacement. |
| `exempt_identities` | set(string) | optional | Identities exempt from this control, as full ARNs. Patterns are not supported. Defaults to empty. |
| `exempt_sso_actor_ids` | set(string) | optional | SSO actor ids exempt from this control. Defaults to empty. |
| `timeboxed_exempt_identities` | set(string) | computed | Identities exempt through a time-limited Permissions on Demand grant. |
| `timeboxed_exempt_sso_actor_ids` | set(string) | computed | SSO actor ids exempt through a time-limited Permissions on Demand grant. |
| `scope` | string | computed | Resolved scope path. |
| `status` | string | computed | `Protected`, `Disabled`, `Unprotected` or `Non-Privileged`. |
| `pending` | bool | computed | True while a staged change has not been deployed. |
| `revision` | string | computed | Changes whenever this resource stages a change. Reference it in the submission's `triggers`. |

Exemptions are attributes of the protect, not resources of their own. They are
removed when the control is unmanaged or switched to a disable.

The platform's suggested exemptions are not applied. Read them with
`sonrai_suggested_exemptions` and list the ones you want.

Disabled services do not take control exemptions.

#### Exemptions granted on demand

An identity that hits a protected control can be granted an exemption through
Permissions on Demand. A grant with a time limit is reported in
`timeboxed_exempt_identities` and `timeboxed_exempt_sso_actor_ids` and is left
alone: it expires on its own, and managing it here would mean removing access
the approval workflow granted.

A grant approved **permanently** is indistinguishable from an exemption made
here, so it appears in `exempt_identities` and the next apply removes it. Add
it to the configuration to keep it.


### `sonrai_exempted_identity`

Exempts an identity from whole classes of control at a scope and everything
beneath it. An identity already exempted at the scope is rejected rather than
adopted; import it to manage it. AWS only.

| Attribute | Type | | |
|---|---|---|---|
| `scope` | string | required | Scope to define the exemption at. Forces replacement. |
| `identity` | string | required | Smallest pattern that names the identity, such as `*e/BreakGlass`, or SSO actor id when `sso`. Forces replacement. |
| `sso` | bool | optional | Whether `identity` is an IAM Identity Center user. Defaults to false. Forces replacement. |
| `exempt_from` | set(string) | required | `service_protect`, `service_block`, `quarantine`, `region_block`, `jit`, `tamper_protect`. |
| `pending` | bool | computed | True while a staged change has not been deployed. |
| `revision` | string | computed | Changes whenever this resource stages a change. Reference it in the submission's `triggers`. |

This is not the per-control exemption on `sonrai_account_protected_service`.
That one names identities exempt from a single control; this exempts an
identity from every control of the listed kinds.

Identities are matched as patterns, and a pattern is the recommended form. One
entry covers every account (see
[Identities: ARNs and patterns](#identities-arns-and-patterns)):

```hcl
resource "sonrai_exempted_identity" "break_glass" {
  scope       = "aws/r-abcd"
  identity    = "*e/BreakGlass"
  exempt_from = ["service_protect", "service_block"]
}
```

The platform holds one entry per identity at a scope, so `exempt_from` carries
every kind at once.

`exempt_from` is the complete list. Changing it on an existing exemption stages
the difference from what is deployed, so a kind taken out of the list is removed
when the change is submitted and deployed, and the identity stays exempt from
the rest throughout. A kind added outside Terraform shows up as drift and is
removed the same way unless it is added to `exempt_from`.

Scopes are exact: an
exemption defined at a root scope is in force in every account under it but
belongs to the root, and is not readable as an entry at the account.

Management-account scopes are rejected when the change is deployed, not when it
is staged.

The platform's own built-in exemptions, such as the AWS reserved and StackSet
execution roles, cannot be removed. Drop them from state with
`terraform state rm` rather than destroying them.


### `sonrai_jit_configuration`

Configures Just-in-Time access at a scope. AWS only.

| Attribute | Type | | |
|---|---|---|---|
| `root_scope` | string | required | Cloud organization root scope. |
| `resource_id` | string | required | Id of an account, organizational unit or the root. |
| `scope` | string | computed | Scope path `resource_id` resolved to. |
| `deny_first` | bool | optional | Deny every entitlement not enrolled here. Defaults to false. |
| `enrolled_entitlements` | set(object) | optional | `name`, `type`, `auto_pod`. |
| `pending` | bool | computed | True while a staged change has not been deployed. |
| `revision` | string | computed | Changes whenever this resource stages a change. Reference it in the submission's `triggers`. |

Declaring this resource replaces whatever the scope already has: enrolments
absent from `enrolled_entitlements` are unenrolled, and `deny_first` takes the
value declared here.

Each entitlement takes a `name` — the permission set, account access role or
SAML role as the platform names it — an optional `type` of
`AWS_PERMISSION_SET` (the default), `AWS_ACCOUNT_ACCESS_ROLE` or
`AWS_SAML_ROLE`, and `auto_pod` — AutoPoD in the console — which approves
protected actions through that entitlement without a Permissions on Demand
request.

```hcl
resource "sonrai_jit_configuration" "org" {
  root_scope  = "aws/r-abcd"
  resource_id = "r-abcd"
  deny_first  = true

  enrolled_entitlements = [
    { name = "PowerUserAccess" },
    { name = "BreakGlassAccess", type = "AWS_ACCOUNT_ACCESS_ROLE", auto_pod = true },
  ]
}
```

`resource_id` is the id of an account, an organizational unit or the root
itself, and the configuration covers everything beneath it. The id is resolved
to its path on every run, reported as `scope`, so an account or organizational
unit that is moved needs no change here. Pointing `resource_id` somewhere else
replaces the resource.

The platform replaces the whole configuration at a scope in one call, so this
resource is authoritative for the scope it names. An entitlement enrolled from
the console and left out of `enrolled_entitlements` is unenrolled on the next
apply.

Scopes are exact. An enrolment at an ancestor governs this scope but is not a
record here, so it is not reported and cannot be managed from here; declare it
on the ancestor's own resource.

Destroying the resource clears deny-first and every enrolment at the scope.
There is no delete for a JIT configuration, so removal is a write of the empty
configuration; where the change was staged and never deployed, that cancels the
staged row rather than adding a second one.


### `sonrai_region_control`

Enables or disables one cloud region in one account. AWS only.

| Attribute | Type | | |
|---|---|---|---|
| `root_scope` | string | required | Cloud organization root scope. Forces replacement. |
| `resource_id` | string | required | AWS account id. OUs and roots are not accepted. Forces replacement. |
| `region_id` | string | required | Cloud region id, such as `ap-south-1`. Forces replacement. |
| `state` | string | required | `disabled` or `enabled`. |
| `previous_state` | string | computed | The state the account had before this resource, which destroy restores. |
| `scope` | string | computed | Canonical scope path the account resolved to. |
| `status` | string | computed | `ENABLED` or `DISABLED`. |
| `pending` | bool | computed | True while a staged change has not been deployed. |
| `revision` | string | computed | Changes whenever this resource stages a change. Reference it in the submission's `triggers`. |
| `critical` | bool | computed | Whether the platform treats the region as critical. |
| `region_name` | string | computed | Human-readable name. |

Regions are enabled by default, and which regions exist comes from the cloud —
the platform tracks what each account has switched on, so a region the account
does not have cannot be controlled. Disabling one stops the firewall deploying
into it.

Accounts only, for the same reason as the service controls: a control staged at
an OU or root expands to the accounts present at the time, and an account added
later does not join it — while the container's aggregate status keeps reading
as the declared state, hiding the gap from every plan. Declaring per account
makes a new account a visible planned change:

```hcl
resource "sonrai_region_control" "no_apac" {
  for_each    = var.account_ids
  root_scope  = "aws/r-abcd"
  resource_id = each.value
  region_id   = "ap-south-1"
  state       = "disabled"
}
```

`state` is declared rather than implied, because enabling is an override and
not merely the absence of a disable: one account can opt back in to a region
disabled for the accounts around it.

A critical region cannot be disabled, and a region not enabled in the account
cannot be controlled at all. The platform accepts both requests and silently
does nothing, so the provider checks first and fails the apply rather than
reporting a change it did not make. A declaration the platform already
satisfies is rejected the same way: it would record a control this
configuration never caused.

Destroying the resource restores the state the account had before it —
`previous_state`, recorded at create. An imported control has no recorded
state, so destroying it re-enables the region.


### `sonrai_control_submission`

Submits every change staged under a root scope as one transaction.

| Attribute | Type | | |
|---|---|---|---|
| `root_scope` | string | required | Forces replacement. |
| `triggers` | map(string) | optional | Values that produce a new submission when they change. Forces replacement. |
| `wait` | bool | optional | Wait for the transaction to settle. Defaults to `true`. |
| `wait_timeout` | string | optional | Go duration. Defaults to `30m`, maximum `24h`. |
| `transaction_id` | string | computed | |
| `status` | string | computed | Platform transaction status. Only `COMPLETED`, `FAILED` and `CANCELED` are terminal; intermediate values vary with what is being deployed. |
| `status_message` | string | computed | |
| `template_location` | string | computed | CloudFormation template this transaction produced, empty when none is required. |
| `stack_template_location` | string | computed | Template the stack should run: `template_location`, or the one already deployed when this transaction produced none. Use this as the stack's `template_url`. |

Use `stack_template_location` as the stack's `template_url`. It never goes
empty once a template has been deployed, so a submission that changes nothing
deployable leaves the stack as it is. The platform's pre-signed download URL is
deliberately not exposed, here or on `sonrai_control_transaction`: it is a
short-lived credential and would sit in state.

Reference the `revision` of each control it covers in `triggers`, so that
anything one of them stages produces a new submission. That includes a plan
that only reverts a change made outside Terraform.

Use one submission per root scope, and do not run concurrent applies against the
same root scope. A submission covers every change the token's identity has
staged under it, whether or not this configuration staged it, along with
changes the platform has queued for the whole organization, such as denies for
newly discovered third parties. Changes another user staged are left for that
user to submit, so give each configuration its own App identity.

A plan that creates or replaces the submission checks what is already pending
under the root scope and warns when it finds anything, naming up to twenty of
the changes. Nothing in the configuration has staged yet at that point, so
these came from somewhere else: an earlier apply that did not finish, another
configuration or a console session using the same identity, or the platform.
The submission deploys them with everything the plan shows. Clear the ones that
should not go out, in Pending Changes in the console, before applying. The
warning cannot see anything staged between the plan and the apply.

Waiting ends when the transaction reaches a terminal status or produces a
template, whichever comes first. Use `sonrai_control_transaction` after
deploying to wait for completion.

Raise `wait_timeout` for a first rollout across a very large organization; two
hours is not unusual. Transactions expire after 24 hours, so that is the
ceiling.

Cancelling a transaction returns its changes to staged. The submission is then
removed from state on the next refresh, so the following plan proposes a fresh
submission for those still-staged changes.

Destroying the resource removes it from state without reverting the submitted
changes.


### `sonrai_control_reset`

Rolls every control under a root scope back to staged and tears down the
deployment. See [Reset everything at once](#reset-everything-at-once).

| Attribute | Type | | |
|---|---|---|---|
| `root_scope` | string | required | Cloud organization root scope to reset. Forces replacement. |
| `confirm` | string | required | Must repeat `root_scope` exactly. Forces replacement. |
| `wait` | bool | optional | Wait for the reset to complete. Defaults to `true`. |
| `wait_timeout` | string | optional | Go duration. Defaults to `2h`, maximum `24h`. |
| `transaction_id` | string | computed | |
| `status` | string | computed | |
| `status_message` | string | computed | |

**Destructive and organization-wide.** It clears the whole root scope, not only
what Terraform manages: controls created in the console go too, along with
quarantined identities and JIT levels.

The transaction cannot complete while the firewall's CloudFormation stack
exists — it waits for the stack to go before doing the rest of its work. Give
the resource a `depends_on` for the stack so one apply deletes the stack and
then runs the reset.

`confirm` is compared with `root_scope` while the plan is built, not when it is
applied, so a mismatch stops the run before the stack is deleted. Write it out
by hand: pointing both attributes at the same variable makes the check satisfy
itself.

Destroying the resource forgets the reset. It does not undo it, and nothing
needs undoing — the controls it unapplied are staged, and submitting them
deploys them again.

### `sonrai_approvers`

Sets who approves access requests at an account, organizational unit or root,
and who is allowed to.

| Attribute | Type | | |
|---|---|---|---|
| `root_scope` | string | required | Cloud organization root scope. Forces replacement. |
| `resource_id` | string | required | Id of an account, organizational unit or the root. Forces replacement. |
| `scope` | string | computed | Scope path `resource_id` resolved to. |
| `approvers` | set(string) | required | Email addresses of users and groups, in lower case. May be empty. At most 100. |
| `approval_type` | string | required | `exclude_self`, `assigned_approvers` or `everyone_at_scope`. |
| `previous_approval_type` | string | computed | The approval type the scope had before this resource, which destroy restores. |

```hcl
resource "sonrai_approvers" "prod" {
  root_scope    = "aws/r-abcd"
  resource_id   = "123456789012"
  approvers     = ["cloud-approvers@example.com", "amy@example.com"]
  approval_type = "exclude_self"
}
```

Unlike the control resources, `resource_id` also takes an organizational unit
such as `ou-ab12-cdef3456` or the root itself, such as `r-abcd`. The id is
resolved to its path on every run, so an account or organizational unit that is
moved needs no change here.

This is the one resource that does not stage. It takes effect when it is
applied, it has no `revision`, and it does not belong in a submission's
`triggers`.

`approval_type` decides who may approve:

- `exclude_self`: the assigned approvers, but never their own request.
- `assigned_approvers`: the assigned approvers, their own requests included.
- `everyone_at_scope`: anyone, their own requests included.

The resource is authoritative for the approvers assigned at its scope. Any
assigned there and left out of `approvers` are removed. Approvers a scope
inherits from its parents are not listed and not affected, so an empty
`approvers` leaves the scope to the ones it inherits. A scope that already has
approvers of its own is rejected rather than adopted; import it.

Three things happen beyond the scope's own approver list:

- **Approver exceptions are removed.** Conditional approver rules set on the
  scope in the console are not managed here yet, and the platform deletes them
  whenever the scope's approvers are written. Every apply that creates, changes
  or destroys the resource removes them, and warns when it has.
- **People can be invited and granted a role.** An address that is not yet a
  Sonrai user is sent an invite, and an approver without an approver role is
  granted `CloudPermissionFirewallApprover`. Removing the approver later undoes
  neither.
- **The approval type is copied down.** The platform also writes it to every
  scope beneath this one that has no approvers of its own. A `sonrai_approvers`
  on one of those scopes reports that as drift, so declare the same
  `approval_type` there or give the scope approvers.

Destroying the resource removes the scope's approvers and restores
`previous_approval_type`. An imported resource has nothing recorded to restore,
so destroying it leaves the approval type as it is.

The token's identity needs the `edit.cloudcontrols` permission.

## Data sources

### `sonrai_scope`

Resolves a canonical scope path.

| Attribute | Type | | |
|---|---|---|---|
| `root_scope` | string | required | |
| `resource_id` | string | required | |
| `entry_type` | string | optional | Narrows the search, such as `account`, `organizationalUnit`, `project`, `folder`, `subscription`, `managementGroup`. |
| `scope` | string | computed | |
| `name` | string | computed | |
| `cloud_type` | string | computed | |
| `parent_scope` | string | computed | |

Set `entry_type` when an id could name more than one kind of entry; otherwise
the matched type is reported back.

### `sonrai_suggested_exemptions`

Identities the platform would exempt when protecting a service, because they
recently used one of its sensitive permissions.

| Attribute | Type | | |
|---|---|---|---|
| `root_scope` | string | required | |
| `resource_id` | string | required | Id of the account. |
| `control_key` | string | required | |
| `scope` | string | computed | Resolved scope path. |
| `identities` | set(string) | computed | Suggested non-SSO identities as full ARNs, for `exempt_identities`. |
| `sso_actor_ids` | set(string) | computed | Suggested SSO actor ids, for `exempt_sso_actor_ids`. |
| `suggestions` | list(object) | computed | Each suggestion with `name`, `is_sso`, `last_sensitive_used` and `last_sensitive_used_date`. |

The lookback window is an organization setting, so this set changes over time on
its own. Copy the values into configuration to keep exemptions stable; reference
`identities` directly to let them track the window, accepting that plans will
then show changes nobody made.

### `sonrai_entitlements`

AWS entitlements available at a scope: Identity Center permission sets,
account access roles and SAML roles. AWS only.

| Attribute | Type | | |
|---|---|---|---|
| `scope` | string | required | Scope to read entitlements at. |
| `type` | string | optional | Return only `AWS_PERMISSION_SET`, `AWS_ACCOUNT_ACCESS_ROLE` or `AWS_SAML_ROLE`. |
| `names` | set(string) | computed | Just the names. |
| `entitlements` | list(object) | computed | `name`, `type`, `labels`, `user_count`, `user_count_display`. |

`sonrai_jit_configuration` enrols by name, and a name matching nothing enrols
nothing without reporting an error, so read them here rather than copying them
out of the console.

```hcl
data "sonrai_scope" "account" {
  root_scope  = "aws/r-abcd"
  resource_id = "123456789012"
}

data "sonrai_entitlements" "account" {
  scope = data.sonrai_scope.account.scope
}

resource "sonrai_jit_configuration" "account" {
  root_scope  = "aws/r-abcd"
  resource_id = "123456789012"
  deny_first  = true

  enrolled_entitlements = [
    for e in data.sonrai_entitlements.account.entitlements : {
      name = e.name
      type = e.type
    } if e.name != "AdministratorAccess"
  ]
}
```

`user_count` is how many identities reach the scope through the entitlement
today, which is usually what decides whether enrolling it is disruptive. It is
null where no count applies, as it does not for a SAML role assigned in the
identity provider — null is not zero, and `user_count_display` renders it as
`N/A`.

`scope` matches the scope and everything beneath it. An entitlement that
qualifies as more than one type is reported as the more specific one, account
access role ahead of SAML role.

### `sonrai_deploy_account`

Resolves the account a root scope's CloudFormation template deploys into.

| Attribute | Type | | |
|---|---|---|---|
| `root_scope` | string | required | |
| `account_id` | string | computed | Account to deploy into. |
| `delegated` | bool | computed | True when `account_id` is a delegated admin account. |
| `management_account_id` | string | computed | |
| `organization_id` | string | computed | |
| `onboarded` | bool | computed | |

Point AWS credentials at `account_id`. It is the delegated admin account where
one is configured, and the management account otherwise.

### `sonrai_control_transaction`

Reads a transaction, optionally waiting for a terminal status.

| Attribute | Type | | |
|---|---|---|---|
| `transaction_id` | string | required | |
| `wait_for` | string | optional | `COMPLETED`, `FAILED` or `CANCELED`. Omit to read once. |
| `wait_timeout` | string | optional | Go duration. Defaults to `30m`, maximum `24h`. |
| `root_scope` | string | computed | |
| `status` | string | computed | |
| `status_message` | string | computed | |
| `template_location` | string | computed | |

Reaching a terminal status other than `wait_for` is an error.

### `sonrai_pending_changes`

Lists what a submission under a root scope would deploy if it ran now.

| Attribute | Type | | |
|---|---|---|---|
| `root_scope` | string | required | |
| `total` | number | computed | Number of pending changes. |
| `changes` | list(object) | computed | `id`, `change_type`, `name`, `action`, `scope`, `control_key`, `staged_by`, `identities`, `organization_wide`. |

```hcl
data "sonrai_pending_changes" "firewall" {
  root_scope = "aws/r-abcd"
}

check "nothing_unexpected_is_pending" {
  assert {
    condition     = data.sonrai_pending_changes.firewall.total == 0
    error_message = "${data.sonrai_pending_changes.firewall.total} change(s) are already pending."
  }
}
```

`organization_wide` is true for changes the platform queued for every
submitter: updates to known third party accounts, and edits to custom controls
and organization policies that are already deployed. The platform reports those
under the caller, so `staged_by` does not tell them apart.

It is read while planning, so it lists what was pending before this
configuration staged anything. Do not give it a `depends_on` on the controls:
that defers the read until they have staged, and it lists them too.

Prefer a `check` block to a `precondition` on the submission. A precondition
that requires nothing to be pending also blocks the apply that recovers from
one which staged its changes and then failed before submitting them.
