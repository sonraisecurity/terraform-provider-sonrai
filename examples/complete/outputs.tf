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
