# Operating the Sonrai Terraform provider

The full walkthrough and the per-attribute reference for every resource and
data source. [`README.md`](README.md) covers installation and the shape of a
working configuration; this is the detail behind it.

## Requirements

- Terraform 1.5 or later, or OpenTofu 1.6 or later
- A Sonrai API token issued to an App identity, with write scope

Everything here is a resource or a data source. The provider declares no
Terraform actions, which OpenTofu does not implement, so both tools get the
same thing. Nor does any documented workflow need `-target` or `-replace`:
those are for digging out of a bad state, not for routine operation.

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

provider "sonrai" {
  org = "crc12345678"
}
```

| Argument | Environment variable | Default | |
|---|---|---|---|
| `org` | `SONRAI_ORG` | — | required |
| `domain` | `SONRAI_DOMAIN` | `sonraisecurity.com` | optional |
| `token` | `SONRAI_TOKEN` | — | required |

Set the token through `SONRAI_TOKEN`. A token set in configuration is also
written to state.

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

```hcl
terraform {
  required_providers {
    sonrai = { source = "sonraisecurity/sonrai" }
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
identities go in `exempt_identities`, SSO identities in
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
    cloud9_prod = sonrai_account_disabled_service.cloud9_prod.id
    iam_prod    = sonrai_account_protected_service.iam_prod.id
  }
}

resource "aws_cloudformation_stack" "firewall" {
  name         = "SonraiCloudPermissionFirewall"
  template_url = sonrai_control_submission.firewall.template_location
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

List the controls in `triggers` so that changing any of them produces a new
submission.

Where firewall changes have been deployed before, the stack already exists and
the controls are already applied. See
[Adopting an existing deployment](#adopting-an-existing-deployment).

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
gone. The next submission against that root scope deploys the lot — including
one made from the console, or by a colleague, or by another configuration.

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
terraform state rm 'sonrai_account_disabled_service.this["cloud9_prod"]'
terraform state rm 'sonrai_account_protected_service.this["iam_prod"]'
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

## Adopting an existing deployment

Where the firewall is already deployed, Terraform has to be told about both
halves: the CloudFormation stack, and every control.

`terraform import` writes state for a resource the configuration already
declares — it does not write the declaration. Each resource below needs a block
in your configuration first, or the import fails with *"resource address does
not exist in the configuration"*.

Where you do not know the values to declare, let Terraform write the block for
you from the live resource:

```hcl
import {
  to = sonrai_account_disabled_services_authoritative.prod
  id = "aws/r-abcd@123456789012"
}
```

```sh
terraform plan -generate-config-out=generated.tf
```

That reads the resource and generates its configuration, arguments filled in,
without applying anything. Review the result, move it into your configuration,
then apply to record the import. Config generation only works where the
resource is not already declared.

**1. Import the stack.**

```sh
terraform import aws_cloudformation_stack.firewall SonraiCloudPermissionFirewall
```

This covers the stack alone. The controls stay applied and unknown to
Terraform.

**2. Import the controls.** Disabled services, protects, region controls and
exempted identities reject a declaration that matches what is already there, so
import each one:

```sh
terraform import sonrai_account_disabled_service.cloud9_prod 'cloud9@aws/r-abcd@123456789012'
terraform import sonrai_account_protected_service.iam_prod 'iam@aws/r-abcd@123456789012'
```

To take over every disabled service in an account at once, use
`sonrai_account_disabled_services_authoritative`, which adopts what it finds:

```sh
terraform import sonrai_account_disabled_services_authoritative.prod 'aws/r-abcd@123456789012'
```

There is no equivalent for protects or region controls.

`sonrai_jit_configuration` is authoritative for its whole scope and does not
reject: declaring it replaces the enrolment, `deny_first` and `auto_pod` the
scope already has. Import the scope before declaring it.

**3. Check the plan is empty, and check Pending Changes**, before applying
anything:

```bash
terraform plan
```

An empty plan is only half the check. A submission covers every change staged
under the root scope, not only the ones Terraform made, and changes staged
elsewhere — from the console, by another configuration, by hand during an
adoption — do not appear in a Terraform plan at all. Read Pending Changes in
the console and make sure you recognise everything in it.

Note that this is not limited to a deliberate submission. Where
`sonrai_control_submission` is part of the configuration, any apply that
creates or replaces it submits, so an apply made to record an import deploys
whatever else was staged at the time.

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

## Resources

### `sonrai_account_disabled_service`

Disables one cloud service in one account, denying its sensitive permissions.
Claims only the service it names. A service already disabled — from the
console, or by another configuration — is rejected rather than adopted;
import it instead.

| Attribute | Type | | |
|---|---|---|---|
| `root_scope` | string | required | Cloud organization root scope. Forces replacement. |
| `resource_id` | string | required | Id of the account. Forces replacement. |
| `control_key` | string | required | Firewall control key, such as `cloud9`. Forces replacement. |
| `scope` | string | computed | Resolved scope path. |
| `status` | string | computed | `Disabled`, `Protected`, `Unprotected` or `Non-Privileged`. |
| `pending` | bool | computed | True while a staged change has not been deployed. |

Creating the resource stages the change; it takes effect once submitted and
deployed. `status` reads `Disabled` from the moment it is staged.

Changing a managed control outside Terraform causes the next plan to show the
resource as deleted; applying re-stages it.

```sh
terraform import sonrai_account_disabled_service.cloud9_prod 'cloud9@aws/r-abcd@123456789012'
```

### `sonrai_account_protected_service`

Protects one cloud service in one account, firewalling its sensitive
permissions for every identity except those exempted. A service already
protected is rejected rather than adopted; import it instead.

| Attribute | Type | | |
|---|---|---|---|
| `root_scope` | string | required | Cloud organization root scope. Forces replacement. |
| `resource_id` | string | required | Id of the account. Forces replacement. |
| `control_key` | string | required | Firewall control key, such as `iam`. Forces replacement. |
| `exempt_identities` | set(string) | optional | Identities exempt from this control, as ARNs or resource ids. Defaults to empty. |
| `exempt_sso_actor_ids` | set(string) | optional | SSO actor ids exempt from this control. Defaults to empty. |
| `timeboxed_exempt_identities` | set(string) | computed | Identities exempt through a time-limited Permissions on Demand grant. |
| `timeboxed_exempt_sso_actor_ids` | set(string) | computed | SSO actor ids exempt through a time-limited Permissions on Demand grant. |
| `scope` | string | computed | Resolved scope path. |
| `status` | string | computed | `Protected`, `Disabled`, `Unprotected` or `Non-Privileged`. |
| `pending` | bool | computed | True while a staged change has not been deployed. |

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

```sh
terraform import sonrai_account_protected_service.iam_prod 'iam@aws/r-abcd@123456789012'
```

### `sonrai_exempted_identity`

Exempts an identity from whole classes of control at a scope and everything
beneath it. An identity already exempted at the scope is rejected rather than
adopted; import it instead. AWS only.

| Attribute | Type | | |
|---|---|---|---|
| `scope` | string | required | Scope to define the exemption at. Forces replacement. |
| `identity` | string | required | ARN pattern, or SSO actor id when `sso`. Forces replacement. |
| `sso` | bool | optional | Whether `identity` is an IAM Identity Center user. Defaults to false. Forces replacement. |
| `exempt_from` | set(string) | required | `service_protect`, `service_block`, `quarantine`, `region_block`, `jit`, `tamper_protect`. |
| `pending` | bool | computed | True while a staged change has not been deployed. |

This is not the per-control exemption on `sonrai_account_protected_service`.
That one names identities exempt from a single control; this exempts an
identity from every control of the listed kinds.

Identities are matched as patterns, so one entry covers every account:

```hcl
resource "sonrai_exempted_identity" "break_glass" {
  scope       = "aws/r-abcd"
  identity    = "arn:aws:iam::*:role/BreakGlass"
  exempt_from = ["service_protect", "service_block"]
}
```

A pattern that names no resource, such as `arn:aws:iam::`, matches every ARN in
the organization and is refused at plan time.

The platform holds one entry per identity at a scope, so `exempt_from` carries
every kind at once; kinds left out are turned off. Scopes are exact: an
exemption defined at a root scope is in force in every account under it but
belongs to the root, and is not readable as an entry at the account.

Management-account scopes are rejected when the change is deployed, not when it
is staged.

The platform's own built-in exemptions, such as the AWS reserved and StackSet
execution roles, cannot be removed. Drop them from state with
`terraform state rm` rather than destroying them.

```sh
terraform import sonrai_exempted_identity.break_glass 'arn:aws:iam::*:role/BreakGlass@aws/r-abcd'
```

### `sonrai_jit_configuration`

Configures Just-in-Time access at a scope. AWS only.

| Attribute | Type | | |
|---|---|---|---|
| `scope` | string | required | Scope to configure. Forces replacement. |
| `deny_first` | bool | optional | Deny every entitlement not enrolled here. Defaults to false. |
| `auto_pod` | bool | optional | Approve Permissions on Demand automatically. Defaults to false. |
| `enrolled_entitlements` | set(object) | optional | `name`, `type`, `full_access`. |
| `pending` | bool | computed | True while a staged change has not been deployed. |

Writing this resource without importing first replaces whatever the scope
already has: enrolments absent from `enrolled_entitlements` are unenrolled, and
`deny_first` and `auto_pod` take the values declared here.

Each entitlement takes a `name` — the permission set, account access role or
SAML role as the platform names it — an optional `type` of
`AWS_PERMISSION_SET` (the default), `AWS_ACCOUNT_ACCESS_ROLE` or
`AWS_SAML_ROLE`, and `full_access`, which exempts a session through that
entitlement from every Protected control.

```hcl
resource "sonrai_jit_configuration" "org" {
  scope      = "aws/r-abcd"
  deny_first = true

  enrolled_entitlements = [
    { name = "PowerUserAccess" },
    { name = "BreakGlassAccess", type = "AWS_ACCOUNT_ACCESS_ROLE", full_access = true },
  ]
}
```

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

```sh
terraform import sonrai_jit_configuration.org 'aws/r-abcd'
```

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
configuration never caused. Import the existing control instead.

Destroying the resource restores the state the account had before it —
`previous_state`, recorded at create. An imported control has no recorded
before, so destroying it lifts a disable but never stages one: an enable
override that was imported stays in force, with a warning saying so.

```sh
terraform import sonrai_region_control.no_apac_sandbox 'ap-south-1@aws/r-abcd@123456789012'
```

### `sonrai_account_disabled_services_authoritative`

Declares the complete set of disabled services for one account.

| Attribute | Type | | |
|---|---|---|---|
| `root_scope` | string | required | Cloud organization root scope. Forces replacement. |
| `resource_id` | string | required | Id of the account. Forces replacement. |
| `control_keys` | set(string) | required | Firewall control keys to disable. |
| `scope` | string | computed | Resolved scope path. |

Services disabled in the account but absent from `control_keys` are re-enabled
on the next apply, including changes made from the console, by another
configuration, or by automation.

Import the account before first use. Declare the block with the services you
expect to be disabled, import, and confirm the plan is empty:

```hcl
resource "sonrai_account_disabled_services_authoritative" "prod" {
  root_scope   = "aws/r-abcd"
  resource_id  = "123456789012"
  control_keys = ["cloud9", "healthlake"]
}
```

```sh
terraform import sonrai_account_disabled_services_authoritative.prod 'aws/r-abcd@123456789012'
terraform plan
```

Do not declare `control_keys = []` and read the set back afterwards. Between
the import and editing the configuration, state holds the real set while the
configuration asks for none — and because this resource is authoritative, an
apply in that window re-enables every service in the account. Generating the
configuration, as above, never opens that window.

Writing this resource without importing first plans to re-enable every service
not listed.

Find available control keys with the `CloudServices` query, or in the console.

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
| `template_location` | string | computed | CloudFormation template to deploy, empty when none is required. |

Read the pre-signed download URL from `sonrai_control_transaction`; it is not
stored here.

Reference the controls it covers in `triggers` so that changing any of them
produces a new submission.

Use one submission per root scope, and do not run concurrent applies against the
same root scope. A submission covers every change staged under it, including
changes staged from the console.

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

```sh
terraform import sonrai_control_submission.firewall 'a1b2c3d4-0000-0000-0000-000000000000'
```

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

There is no import. The resource records a reset that this configuration ran.

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
| `identities` | set(string) | computed | Suggested non-SSO identities, for `exempt_identities`. |
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
data "sonrai_entitlements" "account" {
  scope = "aws/r-abcd/123456789012"
}

resource "sonrai_jit_configuration" "account" {
  scope      = "aws/r-abcd/123456789012"
  deny_first = true

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
