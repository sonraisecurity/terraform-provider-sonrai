# JIT across the whole organization: two entitlements enrolled, everything
# else denied.
resource "sonrai_jit_configuration" "org" {
  scope      = "aws/r-abcd"
  deny_first = true

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
# Named by account id, which keeps working if the account is moved to another
# organizational unit. resource_id also takes an organizational unit or the
# root; scope, with the full path, is the alternative.
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
