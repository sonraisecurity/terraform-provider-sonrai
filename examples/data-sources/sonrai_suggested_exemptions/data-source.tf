# Identities the platform would exempt when protecting a service. Review these,
# then list the ones you want on the protect resource.
data "sonrai_suggested_exemptions" "iam_prod" {
  root_scope  = "aws/r-abcd"
  resource_id = "123456789012"
  control_key = "iam"
}

output "review" {
  value = data.sonrai_suggested_exemptions.iam_prod.suggestions
}

# Stable: the reviewed list is written into configuration, so it changes only
# when you change it.
resource "sonrai_account_protected_service" "iam_prod" {
  root_scope  = "aws/r-abcd"
  resource_id = "123456789012"
  control_key = "iam"

  exempt_identities = [
    "arn:aws:iam::123456789012:role/CrossAccountIAMAdminRole",
  ]
}

# Tracking: exemptions follow the platform's rolling window. The set changes as
# identities age out of it, so plans show changes nobody made.
resource "sonrai_account_protected_service" "lambda_prod" {
  root_scope  = "aws/r-abcd"
  resource_id = "123456789012"
  control_key = "lambda"

  exempt_identities    = data.sonrai_suggested_exemptions.lambda_prod.identities
  exempt_sso_actor_ids = data.sonrai_suggested_exemptions.lambda_prod.sso_actor_ids
}

data "sonrai_suggested_exemptions" "lambda_prod" {
  root_scope  = "aws/r-abcd"
  resource_id = "123456789012"
  control_key = "lambda"
}
