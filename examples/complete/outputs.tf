output "disabled" {
  value = {
    for name, r in sonrai_account_disabled_service.this : name => {
      scope = r.scope, status = r.status
    }
  }
}

output "protected" {
  value = {
    for name, r in sonrai_account_protected_service.this : name => {
      scope             = r.scope
      status            = r.status
      exempt_identities = r.exempt_identities
    }
  }
}

output "exempted" {
  value = {
    for name, r in sonrai_exempted_identity.this : name => {
      scope       = r.scope
      identity    = r.identity
      sso         = r.sso
      exempt_from = r.exempt_from
    }
  }
}

output "regions" {
  value = {
    for name, r in sonrai_region_control.this : name => {
      scope = r.scope, status = r.status, previous_state = r.previous_state
    }
  }
}

output "jit" {
  value = {
    for target, r in sonrai_jit_configuration.this : target => {
      scope      = r.scope
      deny_first = r.deny_first
      auto_pod   = sort([for e in r.enrolled_entitlements : e.name if e.auto_pod])
    }
  }
}

output "custom_controls" {
  value = {
    for name, r in sonrai_custom_control.this : name => {
      control_key = r.control_key
      permissions = r.permissions
    }
  }
}

output "organization_policies" {
  value = {
    for name, r in sonrai_organization_policy.this : name => {
      id          = r.id
      attached_to = sort([for a in sonrai_organization_policy_attachment.this : a.scope if a.policy_id == r.id])
    }
  }
}

output "approvers" {
  value = {
    for target, r in sonrai_approvers.this : target => {
      scope         = r.scope
      approvers     = r.approvers
      approval_type = r.approval_type
    }
  }
}

output "rollout" {
  value = local.deployed ? {
    transaction_id = one(sonrai_control_submission.firewall[*].transaction_id)
    stack_id       = one(aws_cloudformation_stack.firewall[*].id)
    final_status   = one(data.sonrai_control_transaction.firewall[*].status)
    detail         = one(data.sonrai_control_transaction.firewall[*].status_message)
  } : null
}

output "reset" {
  description = "Result of a reset, while one is armed."
  value = var.reset ? {
    transaction_id = one(sonrai_control_reset.firewall[*].transaction_id)
    status         = one(sonrai_control_reset.firewall[*].status)
    detail         = one(sonrai_control_reset.firewall[*].status_message)
  } : null
}

output "deploy_target" {
  description = "Account the CloudFormation template deploys into. Apply with credentials for this account."
  value = {
    account_id = data.sonrai_deploy_account.firewall.account_id
    delegated  = data.sonrai_deploy_account.firewall.delegated
  }
}
