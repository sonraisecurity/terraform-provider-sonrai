# Everything federated users reach this account through.
data "sonrai_entitlements" "account" {
  scope = "aws/r-abcd/123456789012"
}

# Only the account access roles.
data "sonrai_entitlements" "roles" {
  scope = "aws/r-abcd/123456789012"
  type  = "AWS_ACCOUNT_ACCESS_ROLE"
}

# Enrol everything that is not the broadest permission set, which would
# otherwise put administrators behind a JIT session.
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

output "entitlement_usage" {
  value = {
    for e in data.sonrai_entitlements.account.entitlements : e.name => e.user_count
  }
}
