# A break-glass role exempt across the whole organization: resource_id is the
# root's own id. The identity is matched as a pattern, so one entry covers the role in every account,
# including accounts added later. Prefer a short pattern: it is repeated across
# the size-limited policies the platform deploys, so *e/BreakGlass leaves far
# more room than arn:aws:iam::*:role/BreakGlass, which names the same role.
resource "sonrai_exempted_identity" "break_glass" {
  root_scope  = "aws/r-abcd"
  resource_id = "r-abcd"
  identity    = "*e/BreakGlass"
  exempt_from = ["service_protect", "service_block"]
}

# An IAM Identity Center user rather than an ARN pattern.
resource "sonrai_exempted_identity" "sso_operator" {
  root_scope  = "aws/r-abcd"
  resource_id = "r-abcd"
  identity    = "operator@example.com"
  sso         = true
  exempt_from = ["service_block"]
}

# Limited to a single account instead of the whole organization. resource_id
# does the limiting, so the pattern stays as short as ever; a full ARN
# would only add length to every policy statement for this account.
resource "sonrai_exempted_identity" "deploy_robot" {
  root_scope  = "aws/r-abcd"
  resource_id = "123456789012"
  identity    = "*e/DeployRobot"
  exempt_from = ["service_protect"]
}
