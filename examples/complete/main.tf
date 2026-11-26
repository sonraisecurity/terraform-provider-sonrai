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

  root_id = reverse(split("/", var.root_scope))[0]

  # A control key naming an entry in custom_controls stands for that control's
  # real key. Going through the resource is also what makes Terraform define
  # the control before attaching it, and detach it before deleting it.
  custom_control_keys = { for name, c in sonrai_custom_control.this : name => c.control_key }

  # One entry per policy and target, keyed "name@target_id".
  policy_attachments = merge([
    for name, p in var.organization_policies : {
      for target in p.attach_to : "${name}@${target}" => { policy = name, resource_id = target }
    }
  ]...)

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

  # Same shape for regions, keyed "region_id@account_id".
  region_controls = merge([
    for account_id, regions in var.region_controls : {
      for region_id, state in regions : "${region_id}@${account_id}" => {
        resource_id = account_id
        region_id   = region_id
        state       = state
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

# A custom control belongs to the whole organization and is defined as soon as
# it is applied. It protects nothing until protected_services or
# disabled_services names it, and that part is staged like any other control.
resource "sonrai_custom_control" "this" {
  for_each = var.custom_controls

  title       = each.value.title
  description = each.value.description
  cloud_type  = each.value.cloud_type
  permissions = each.value.permissions
}

# An organization policy is a service control or resource control policy of
# your own. Defining one takes effect as soon as it is applied and is in force
# nowhere; the attachments below are staged like any other control.
resource "sonrai_organization_policy" "this" {
  for_each = var.organization_policies

  root_scope  = var.root_scope
  name        = coalesce(each.value.name, each.key)
  policy_type = each.value.policy_type
  description = each.value.description
  content     = each.value.content
}

# The policy applies at the target and everything beneath it. Attach at one
# level of a branch: an account under an attached organizational unit already
# has the policy.
resource "sonrai_organization_policy_attachment" "this" {
  for_each = local.policy_attachments

  policy_id   = sonrai_organization_policy.this[each.value.policy].id
  root_scope  = var.root_scope
  resource_id = each.value.resource_id
}

resource "sonrai_account_disabled_service" "this" {
  for_each = local.disabled_services

  root_scope  = var.root_scope
  resource_id = each.value.resource_id
  control_key = lookup(local.custom_control_keys, each.value.control_key, each.value.control_key)
}

resource "sonrai_account_protected_service" "this" {
  for_each = var.protected_services

  root_scope           = var.root_scope
  resource_id          = each.value.resource_id
  control_key          = lookup(local.custom_control_keys, each.value.control_key, each.value.control_key)
  exempt_identities    = each.value.exempt_identities
  exempt_sso_actor_ids = each.value.exempt_sso_actor_ids
}

resource "sonrai_exempted_identity" "this" {
  for_each = var.exempted_identities

  root_scope = var.root_scope
  # The root's own id, the last part of its scope, exempts across the whole
  # organization.
  resource_id = coalesce(each.value.resource_id, local.root_id)
  identity    = each.value.identity
  sso         = each.value.sso
  exempt_from = each.value.exempt_from
}

resource "sonrai_region_control" "this" {
  for_each = local.region_controls

  root_scope  = var.root_scope
  resource_id = each.value.resource_id
  region_id   = each.value.region_id
  state       = each.value.state
}

# Authoritative for each scope named: whatever the scope already has is
# replaced by what is declared here, including enrolments made in the console.
#
# Keys are ids, resolved to their path on every run, so an account moved to
# another organizational unit needs no change here.
resource "sonrai_jit_configuration" "this" {
  for_each = var.jit_configurations

  root_scope            = var.root_scope
  resource_id           = each.key
  deny_first            = each.value.deny_first
  enrolled_entitlements = each.value.enrolled_entitlements
}

# Approvers are not staged: this takes effect as soon as it is applied, so it
# has no revision and stays out of the submission's triggers below. Writing a
# scope's approvers also removes any approver exceptions set on it in the
# console.
resource "sonrai_approvers" "this" {
  for_each = var.approvers

  root_scope    = var.root_scope
  resource_id   = each.key
  approvers     = each.value.approvers
  approval_type = each.value.approval_type
}

resource "sonrai_control_submission" "firewall" {
  count = local.deployed ? 1 : 0

  root_scope = var.root_scope

  # revision changes whenever its resource stages anything, including a plan
  # that only reverts a change made outside Terraform. A trigger built from
  # configured values would not move in that case, and the revert would be
  # staged without being submitted.
  triggers = merge(
    { for name, r in sonrai_account_disabled_service.this : "disabled_${name}" => r.revision },
    { for name, r in sonrai_account_protected_service.this : "protected_${name}" => r.revision },
    { for name, r in sonrai_exempted_identity.this : "exempt_${name}" => r.revision },
    { for name, r in sonrai_region_control.this : "region_${name}" => r.revision },
    { for target, r in sonrai_jit_configuration.this : "jit_${target}" => r.revision },
    # Moves when the permissions of a deployed custom control are edited, which
    # is what redeploys them.
    { for name, r in sonrai_custom_control.this : "custom_${name}" => r.revision },
    # Moves when a deployed organization policy is edited, which is what
    # redeploys it. Destroying a deployed policy only stages its removal, and
    # dropping its entry here is what submits that.
    { for name, r in sonrai_organization_policy.this : "policy_${name}" => r.revision },
    { for name, r in sonrai_organization_policy_attachment.this : "policy_attachment_${name}" => r.revision },
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

  name = var.stack_name
  # The new template, or the one already deployed when this submission changed
  # nothing deployable (JIT, for one). Never empty once a template exists, which
  # keeps the stack from being handed a null template_url.
  template_url = sonrai_control_submission.firewall[0].stack_template_location
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
