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
- **Adoption of an existing deployment.** Every control resource supports
  `terraform import`, and `sonrai_account_disabled_services_authoritative`
  takes over an account's whole disabled-service set at once.
- **Reorganization-proof addressing.** Scope paths are resolved on every
  operation, so moving an account between organizational units needs no
  configuration change.
- **Terraform and OpenTofu alike.** The provider declares no Terraform actions,
  which OpenTofu does not implement. No documented workflow needs `-target` or
  `-replace` either; those are for recovering from a bad state, not for routine
  operation.

### Resources

| Resource | Manages |
|---|---|
| `sonrai_account_disabled_service` | One service disabled in one account |
| `sonrai_account_protected_service` | One service protected in one account, with its exemptions |
| `sonrai_account_disabled_services_authoritative` | The complete disabled-service set for an account |
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

Per-attribute reference for each of these is in [`GUIDE.md`](GUIDE.md) and in
[`docs/`](docs), which is what the Terraform Registry publishes.

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

- Terraform 1.5 or later, or OpenTofu 1.6 or later
- A Sonrai API token issued to an App identity, with write scope
- AWS credentials for the account the firewall deploys into, if you want the
  same apply to create the CloudFormation stack. `sonrai_deploy_account`
  reports which account that is: the delegated admin account where one is
  configured, the management account otherwise.

To *build* it:

- Go 1.26 or later
- `make`

## Installation & Quick Start

### Declare the provider

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
  org = "crc12345678" # the first label of your console hostname
}
```

**The provider is not on the Terraform Registry yet**, so `terraform init`
cannot fetch it. Until it is published, build it and drop the binary into the
CLI's local plugin directory, where Terraform finds it without any CLI
configuration:

```sh
make build

PLUGINS=~/.terraform.d/plugins/registry.terraform.io/sonraisecurity/sonrai/0.1.0/darwin_arm64
mkdir -p "$PLUGINS"
cp terraform-provider-sonrai "$PLUGINS/terraform-provider-sonrai_v0.1.0"
```

Match the directory to your platform — `darwin_arm64`, `linux_amd64` — and the
version to the one your `required_providers` block asks for. Once the provider
is published, `terraform init` fetches it like any other provider.

### Start from the worked example

[`examples/complete`](examples/complete) is a working root module covering the
whole cycle. Copy it and edit one file:

```sh
git clone https://github.com/sonraisecurity/terraform-provider-sonrai.git
cp -r terraform-provider-sonrai/examples/complete my-firewall
cd my-firewall

export SONRAI_ORG=crc12345678   # the first label of your console hostname
export SONRAI_TOKEN='...'       # issued to an App identity, with write scope

terraform init
terraform plan
```

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

## Usage Examples

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

List the controls a submission covers in `triggers`, so that changing any of
them produces a new submission. A submission covers **every** change staged
under its root scope, including changes staged from the console — use one per
root scope and avoid concurrent applies against the same one.

### Exempt an identity from whole classes of control

```hcl
resource "sonrai_exempted_identity" "break_glass" {
  scope       = "aws/r-abcd"
  identity    = "arn:aws:iam::*:role/BreakGlass"
  exempt_from = ["service_protect", "service_block"]
}
```

Identities are matched as patterns, so one entry covers every account under the
scope. AWS only.

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

### Adopt an existing deployment

`terraform import` writes state for a resource the configuration already
declares, so write the block first:

```sh
terraform import aws_cloudformation_stack.firewall SonraiCloudPermissionFirewall
terraform import sonrai_account_disabled_service.cloud9_prod 'cloud9@aws/r-abcd@123456789012'
terraform import sonrai_account_protected_service.iam_prod 'iam@aws/r-abcd@123456789012'
```

Then check the plan is empty **and** that Pending Changes in the console holds
nothing you do not recognise: a submission carries everything staged under the
root scope, not only what Terraform staged.

## Configuration

### Provider

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
| `disabled_services` | map(object) | `{}` | `resource_id`, `control_key` |
| `protected_services` | map(object) | `{}` | `resource_id`, `control_key`, `exempt_identities`, `exempt_sso_actor_ids` |
| `exempted_identities` | map(object) | `{}` | `identity`, `exempt_from`, `sso`, `scope` |
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
└── GUIDE.md                 # walkthrough and per-attribute reference
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
