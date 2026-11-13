# Protect a service, firewalling its sensitive permissions for every identity
# except those listed. Exemptions belong to the control: removing this resource,
# or switching the service to a disable, removes them too.
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

# Protect with no exemptions.
resource "sonrai_account_protected_service" "lambda_prod" {
  root_scope  = "aws/r-abcd"
  resource_id = "123456789012"
  control_key = "lambda"
}

output "iam_prod" {
  value = {
    scope   = sonrai_account_protected_service.iam_prod.scope
    status  = sonrai_account_protected_service.iam_prod.status
    pending = sonrai_account_protected_service.iam_prod.pending
  }
}
