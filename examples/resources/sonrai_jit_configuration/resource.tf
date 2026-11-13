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
      name        = "BreakGlassAccess"
      type        = "AWS_ACCOUNT_ACCESS_ROLE"
      full_access = true
    },
  ]
}

# A single account, enrolled without deny-first and approving Permissions on
# Demand automatically.
resource "sonrai_jit_configuration" "sandbox" {
  scope    = "aws/r-abcd/123456789012"
  auto_pod = true

  enrolled_entitlements = [
    {
      name = "DeveloperAccess"
    },
  ]
}
