# JIT across the whole organization: two entitlements enrolled, everything
# else denied.
resource "sonrai_jit_configuration" "org" {
  root_scope  = "aws/r-abcd"
  resource_id = "r-abcd"
  deny_first  = true

  enrolled_entitlements = [
    {
      name = "PowerUserAccess"
    },
    {
      name     = "BreakGlassAccess"
      type     = "AWS_ACCOUNT_ACCESS_ROLE"
      auto_pod = true
    },
  ]
}

# A single account, enrolled without deny-first, with AutoPoD on its one
# entitlement: protected actions through it need no Permissions on Demand
# request.
#
# resource_id is an account here, and keeps working if the account is moved to
# another organizational unit. It takes an organizational unit id the same way.
resource "sonrai_jit_configuration" "sandbox" {
  root_scope  = "aws/r-abcd"
  resource_id = "123456789012"

  enrolled_entitlements = [
    {
      name     = "DeveloperAccess"
      auto_pod = true
    },
  ]
}
