terraform {
  required_providers {
    sonrai = {
      source  = "sonraisecurity/sonrai"
      version = "~> 0.1"
    }
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.67"
    }
  }
}

provider "sonrai" {}

provider "aws" {
  region = var.aws_region
}

locals {
  # A reset tears the deployment down, so the submission and the stack stand
  # down while one is armed. Clearing reset brings both back, which submits
  # afresh and redeploys.
  deployed = var.submit && !var.reset

  # One entry per account and service, keyed "control_key@account_id". Each
  # becomes its own sonrai_account_disabled_service, so a service already
  # disabled outside this configuration is rejected rather than silently taken
  # over or re-enabled.
  disabled_services = merge([
    for account_id, control_keys in var.disabled_services : {
      for control_key in control_keys : "${control_key}@${account_id}" => {
        resource_id = account_id
        control_key = control_key
      }
    }
  ]...)
}

# Which account the template deploys into: the delegated admin account where
# one is configured, the management account otherwise.
data "sonrai_deploy_account" "firewall" {
  root_scope = var.root_scope
}

# The identity the aws provider above is actually using, which is what
# CloudFormation will fetch the template with.
data "aws_caller_identity" "current" {}

resource "sonrai_account_disabled_service" "this" {
  for_each = local.disabled_services

  root_scope  = var.root_scope
  resource_id = each.value.resource_id
  control_key = each.value.control_key
}

resource "sonrai_account_protected_service" "this" {
  for_each = var.protected_services

  root_scope           = var.root_scope
  resource_id          = each.value.resource_id
  control_key          = each.value.control_key
  exempt_identities    = each.value.exempt_identities
  exempt_sso_actor_ids = each.value.exempt_sso_actor_ids
}

resource "sonrai_exempted_identity" "this" {
  for_each = var.exempted_identities

  scope       = coalesce(each.value.scope, var.root_scope)
  identity    = each.value.identity
  sso         = each.value.sso
  exempt_from = each.value.exempt_from
}

resource "sonrai_control_submission" "firewall" {
  count = local.deployed ? 1 : 0

  root_scope = var.root_scope

  triggers = merge(
    { for name, r in sonrai_account_disabled_service.this : "disabled_${name}" => r.id },
    { for name, r in sonrai_account_protected_service.this : "protected_${name}" => r.id },
    { for name, r in sonrai_exempted_identity.this : "exempt_${name}" => join("|", sort(r.exempt_from)) },
  )

  # Checked before anything is submitted. Getting this wrong leaves a live
  # transaction with a 24-hour deadline and no template deployed, because
  # CloudFormation reads the template from a bucket in the deploy account and
  # the url it is given is not pre-signed.
  lifecycle {
    precondition {
      condition = data.aws_caller_identity.current.account_id == data.sonrai_deploy_account.firewall.account_id
      error_message = format(
        "AWS credentials are for account %s, but the firewall template deploys into %s (%s). Assume into %s and apply again.",
        data.aws_caller_identity.current.account_id,
        data.sonrai_deploy_account.firewall.account_id,
        data.sonrai_deploy_account.firewall.delegated ? "the delegated admin account" : "the management account",
        data.sonrai_deploy_account.firewall.account_id,
      )
    }
  }
}

# The submission returns once a template exists; the transaction then sits at
# DEPLOY_STARTED until that template is deployed. Doing it here closes the gap,
# which matters because the transaction has a deadline.
resource "aws_cloudformation_stack" "firewall" {
  count = local.deployed ? 1 : 0

  name         = var.stack_name
  template_url = sonrai_control_submission.firewall[0].template_location
  capabilities = ["CAPABILITY_NAMED_IAM"]

  # This stack is the firewall's whole IAM footprint, not just the controls in
  # this configuration. Deleting it stops enforcement everywhere under the root
  # scope, so make it a deliberate act: remove this block first. A reset needs
  # the stack gone, so it needs this removed too -- put it back afterwards.
  lifecycle {
    prevent_destroy = true
  }
}

# Confirms the firewall saw the rollout land, rather than assuming it did.
data "sonrai_control_transaction" "firewall" {
  count = local.deployed ? 1 : 0

  transaction_id = sonrai_control_submission.firewall[0].transaction_id
  wait_for       = "COMPLETED"
  wait_timeout   = "2h"

  depends_on = [aws_cloudformation_stack.firewall]
}

# Unapplies every control under the root scope and returns it to staged. The
# reset transaction hands off to the cloud and cannot finish while the stack
# exists, so depends_on orders the two: one apply deletes the stack, then runs
# the reset and waits for it.
#
# This clears the whole root scope, not only what this configuration manages.
resource "sonrai_control_reset" "firewall" {
  count = var.reset ? 1 : 0

  root_scope = var.root_scope
  confirm    = var.reset_confirm

  depends_on = [aws_cloudformation_stack.firewall]
}
